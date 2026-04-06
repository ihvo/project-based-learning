package download

import (
	"context"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/ihvo/peer-pressure/torrent"
)

// Config holds parameters for a full file download.
type Config struct {
	Torrent       *torrent.Torrent
	Peers         []string // "host:port" addresses
	WebSeeds      []string // HTTP seed URLs (BEP 19)
	OutputPath    string   // file path for single-file torrents
	PeerID        [20]byte
	MaxPeers      int // max concurrent peer connections (0 = unlimited)
	PipelineDepth int // max pieces in flight per peer (0 = default 5)
	Quiet         bool // suppress progress display
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

	// Peer pool with speed-based rotation
	pool := newPeerPool(cfg, picker, results, prog)

	// Split initial peers from the pool
	maxSlots := cfg.MaxPeers
	if maxSlots <= 0 {
		maxSlots = 30
	}
	if maxSlots > len(cfg.Peers) {
		maxSlots = len(cfg.Peers)
	}
	initialPeers := cfg.Peers[:maxSlots]

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	// Pool manager runs in background: starts workers, evaluates speeds,
	// rotates slow peers.
	var poolDone sync.WaitGroup
	poolDone.Add(1)
	go func() {
		defer poolDone.Done()
		pool.run(ctx, initialPeers)
	}()

	// Start webseed workers — each runs independently like a peer worker.
	var wsWg sync.WaitGroup
	for _, url := range cfg.WebSeeds {
		wsWg.Add(1)
		go func(seedURL string) {
			defer wsWg.Done()
			ws := newWebseedWorker(seedURL, t, picker, results, prog)
			ws.run(ctx)
		}(url)
	}

	// Close results channel once all producers (pool + webseeds) finish.
	go func() {
		poolDone.Wait()
		wsWg.Wait()
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
