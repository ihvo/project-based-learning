package download

import (
	"context"
	"fmt"
	"log"
	"os"
	"sync"

	"github.com/ihvo/peer-pressure/peer"
	"github.com/ihvo/peer-pressure/torrent"
)

// Config holds parameters for a full file download.
type Config struct {
	Torrent    *torrent.Torrent
	Peers      []string // "host:port" addresses
	OutputPath string   // file path for single-file torrents
	PeerID     [20]byte
	MaxPeers   int // max concurrent peer connections (0 = unlimited)
}

// pieceResult carries the outcome of one piece download back to the orchestrator.
type pieceResult struct {
	index int
	data  []byte
	err   error
}

// File downloads all pieces of a torrent concurrently from multiple peers and
// assembles them into the output file. Uses rarest-first piece selection.
func File(ctx context.Context, cfg Config) error {
	t := cfg.Torrent
	numPieces := len(t.Pieces)

	// Create output file — use WriteAt for random-access piece placement
	f, err := os.Create(cfg.OutputPath)
	if err != nil {
		return fmt.Errorf("create output: %w", err)
	}
	defer f.Close()

	// Pre-allocate to full size so WriteAt works at any offset
	if err := f.Truncate(int64(t.TotalLength())); err != nil {
		return fmt.Errorf("allocate output: %w", err)
	}

	picker := NewPicker(numPieces)
	results := make(chan pieceResult, numPieces)

	// Limit concurrent peers
	maxPeers := cfg.MaxPeers
	if maxPeers <= 0 || maxPeers > len(cfg.Peers) {
		maxPeers = len(cfg.Peers)
	}
	peers := cfg.Peers[:maxPeers]

	// Launch workers — one goroutine per peer
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	var wg sync.WaitGroup
	for _, addr := range peers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			worker(ctx, addr, cfg.PeerID, t, picker, results)
		}()
	}

	// Close results channel once all workers finish
	go func() {
		wg.Wait()
		close(results)
	}()

	// Collect results and write pieces to disk
	completed := 0
	for result := range results {
		if result.err != nil {
			log.Printf("piece %d failed: %v", result.index, result.err)
			continue
		}

		offset := int64(result.index) * int64(t.PieceLength)
		if _, err := f.WriteAt(result.data, offset); err != nil {
			return fmt.Errorf("write piece %d: %w", result.index, err)
		}

		completed++
		log.Printf("piece %d/%d done", completed, numPieces)

		if completed == numPieces {
			break
		}
	}

	if completed < numPieces {
		return fmt.Errorf("download incomplete: got %d/%d pieces", completed, numPieces)
	}

	return nil
}

// worker runs the download loop for a single peer connection.
// It connects, handshakes, negotiates unchoke, then loops: pick → download → report.
func worker(ctx context.Context, addr string, peerID [20]byte, t *torrent.Torrent, picker *Picker, results chan<- pieceResult) {
	conn, err := peer.Dial(addr, t.InfoHash, peerID)
	if err != nil {
		log.Printf("connect %s: %v", addr, err)
		return
	}
	defer conn.Close()

	// Negotiate unchoke (also receives bitfield)
	bitfield, err := NegotiateUnchoke(conn)
	if err != nil {
		log.Printf("unchoke %s: %v", addr, err)
		return
	}

	picker.AddPeer(bitfield)
	defer picker.RemovePeer(bitfield)

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		idx, ok := picker.Pick(bitfield)
		if !ok {
			return // no more pieces this peer can provide
		}

		pieceLen := t.PieceLen(idx)
		data, err := DownloadAndVerify(conn, idx, pieceLen, t.Pieces[idx])
		if err != nil {
			picker.Abort(idx)
			results <- pieceResult{index: idx, err: fmt.Errorf("peer %s: %w", addr, err)}
			return // assume connection is dead
		}

		picker.Finish(idx)
		results <- pieceResult{index: idx, data: data}
	}
}
