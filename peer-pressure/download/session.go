package download

import (
	"context"
	"fmt"
	"os"
	"sync"
	"time"

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
	Quiet      bool // suppress progress display
}

// pieceResult carries the outcome of one piece download back to the orchestrator.
type pieceResult struct {
	index    int
	data     []byte
	fromAddr string
	err      error
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

	// Progress tracker
	var prog *Progress
	if !cfg.Quiet {
		prog = NewProgress(t.Name, numPieces, t.PieceLength, int64(t.TotalLength()))
	}

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
			worker(ctx, addr, cfg.PeerID, t, picker, results, prog)
		}()
	}

	// Close results channel once all workers finish
	go func() {
		wg.Wait()
		close(results)
	}()

	// Progress display ticker
	var tickerDone chan struct{}
	if prog != nil {
		tickerDone = make(chan struct{})
		go func() {
			defer close(tickerDone)
			tick := time.NewTicker(150 * time.Millisecond)
			defer tick.Stop()
			for {
				select {
				case <-tick.C:
					prog.PrintOver(80)
				case <-ctx.Done():
					return
				}
			}
		}()
	}

	// Collect results and write pieces to disk
	completed := 0
	for result := range results {
		if result.err != nil {
			if prog != nil {
				prog.PieceFailed(result.index)
			}
			continue
		}

		offset := int64(result.index) * int64(t.PieceLength)
		if _, err := f.WriteAt(result.data, offset); err != nil {
			return fmt.Errorf("write piece %d: %w", result.index, err)
		}

		if prog != nil {
			prog.PieceDone(result.index, result.fromAddr)
		}

		completed++
		if completed == numPieces {
			break
		}
	}

	// Final render
	cancel()
	if tickerDone != nil {
		<-tickerDone
		prog.PrintOver(80)
		fmt.Println() // blank line after final render
	}

	if completed < numPieces {
		return fmt.Errorf("download incomplete: got %d/%d pieces", completed, numPieces)
	}

	return nil
}

// worker runs the download loop for a single peer connection.
// It connects, handshakes, negotiates unchoke, then loops: pick → download → report.
// On connection errors, it reconnects and retries — connections dropping is normal
// in BitTorrent (peers come and go, seeders rate-limit, networks hiccup).
func worker(ctx context.Context, addr string, peerID [20]byte, t *torrent.Torrent, picker *Picker, results chan<- pieceResult, prog *Progress) {
	const maxRetries = 5
	retries := 0

	for retries < maxRetries {
		select {
		case <-ctx.Done():
			return
		default:
		}

		downloaded := workerSession(ctx, addr, peerID, t, picker, results, prog)
		if downloaded > 0 {
			retries = 0 // reset on progress
		} else {
			retries++
		}

		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Duration(retries) * time.Second):
			// brief backoff before reconnecting
		}
	}
}

// workerSession handles a single connection lifecycle: connect → download pieces
// until error or no more work. Returns the number of pieces successfully downloaded.
func workerSession(ctx context.Context, addr string, peerID [20]byte, t *torrent.Torrent, picker *Picker, results chan<- pieceResult, prog *Progress) int {
	conn, err := peer.Dial(addr, t.InfoHash, peerID)
	if err != nil {
		return 0
	}
	defer conn.Close()

	bitfield, err := NegotiateUnchoke(conn, len(t.Pieces))
	if err != nil {
		return 0
	}

	picker.AddPeer(bitfield)
	defer picker.RemovePeer(bitfield)

	if prog != nil {
		prog.PeerConnected(addr, bitfield)
		defer prog.PeerDisconnected(addr)
	}

	downloaded := 0
	for {
		select {
		case <-ctx.Done():
			return downloaded
		default:
		}

		idx, ok := picker.Pick(bitfield)
		if !ok {
			return downloaded // no more pieces this peer can provide
		}

		if prog != nil {
			prog.PieceStarted(idx)
		}

		pieceLen := t.PieceLen(idx)

		// Block callback for progress tracking
		var onBlock BlockCallback
		if prog != nil {
			onBlock = func(_, _, blockLen int) {
				prog.BlockReceived(addr, blockLen)
			}
		}

		data, err := DownloadAndVerify(conn, idx, pieceLen, t.Pieces[idx], onBlock)
		if err != nil {
			picker.Abort(idx)
			results <- pieceResult{index: idx, err: fmt.Errorf("peer %s: %w", addr, err)}
			return downloaded // connection likely dead, let worker reconnect
		}

		picker.Finish(idx)
		results <- pieceResult{index: idx, data: data, fromAddr: addr}
		downloaded++
	}
}
