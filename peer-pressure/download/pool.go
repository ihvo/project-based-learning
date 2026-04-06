package download

import (
	"context"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ihvo/peer-pressure/peer"
	"github.com/ihvo/peer-pressure/torrent"
)

// peerPool manages dynamic peer selection based on download speed.
// It maintains a fixed number of worker slots and periodically replaces
// the slowest workers with untried peers from the pool.
type peerPool struct {
	// Immutable config
	peerID        [20]byte
	torrent       *torrent.Torrent
	picker        *Picker
	results       chan<- pieceResult
	prog          *Progress
	pipelineDepth int
	maxSlots      int
	rotateCount   int           // how many to rotate per evaluation (10%, min 1)
	evalInterval  time.Duration // time between speed evaluations
	gracePeriod   time.Duration // minimum time before a worker can be evicted

	// Mutable state (protected by mu)
	mu      sync.Mutex
	untried []string                  // peers we haven't connected to yet
	active  map[string]*activeWorker  // addr → running worker

	// Concurrency
	wg     sync.WaitGroup       // tracks all active worker goroutines
	doneCh chan string           // workers send their addr here on exit
}

// activeWorker tracks a single running worker's state.
type activeWorker struct {
	addr      string
	cancel    context.CancelFunc
	bytes     atomic.Int64 // updated atomically by worker goroutine
	prevBytes int64        // snapshot for speed computation
	prevTime  time.Time    // when prevBytes was taken
	started   time.Time    // when the worker was launched
}

// speed computes bytes/sec since the last snapshot.
func (w *activeWorker) speed(now time.Time) float64 {
	current := w.bytes.Load()
	elapsed := now.Sub(w.prevTime).Seconds()
	if elapsed <= 0 {
		return 0
	}
	return float64(current-w.prevBytes) / elapsed
}

// snapshot updates the baseline for the next speed measurement.
func (w *activeWorker) snapshot(now time.Time) {
	w.prevBytes = w.bytes.Load()
	w.prevTime = now
}

func newPeerPool(cfg Config, picker *Picker, results chan<- pieceResult, prog *Progress) *peerPool {
	maxSlots := cfg.MaxPeers
	if maxSlots <= 0 {
		maxSlots = 30
	}

	pipelineDepth := cfg.PipelineDepth
	if pipelineDepth <= 0 {
		pipelineDepth = 5
	}

	// Reserve 10% of slots for rotation, minimum 1.
	rotateCount := maxSlots / 10
	if rotateCount < 1 {
		rotateCount = 1
	}

	// Split peers: first maxSlots go to initial active set, rest are untried pool.
	initialPeers := cfg.Peers
	var untried []string
	if len(initialPeers) > maxSlots {
		untried = append(untried, initialPeers[maxSlots:]...)
		initialPeers = initialPeers[:maxSlots]
	}

	return &peerPool{
		peerID:        cfg.PeerID,
		torrent:       cfg.Torrent,
		picker:        picker,
		results:       results,
		prog:          prog,
		pipelineDepth: pipelineDepth,
		maxSlots:      maxSlots,
		rotateCount:   rotateCount,
		evalInterval:  10 * time.Second,
		gracePeriod:   15 * time.Second,
		untried:       untried,
		active:        make(map[string]*activeWorker, maxSlots),
		doneCh:        make(chan string, maxSlots),
	}
}

// run starts workers and manages the peer rotation loop.
// Blocks until ctx is canceled. Waits for all workers to finish,
// then closes the results channel.
func (p *peerPool) run(ctx context.Context, initialPeers []string) {
	defer func() {
		p.wg.Wait()
		close(p.results)
	}()

	// Launch initial workers.
	for _, addr := range initialPeers {
		p.startWorker(ctx, addr)
	}

	evalTicker := time.NewTicker(p.evalInterval)
	defer evalTicker.Stop()

	for {
		select {
		case addr := <-p.doneCh:
			p.handleWorkerExit(ctx, addr)
		case <-evalTicker.C:
			p.evaluate(ctx)
		case <-ctx.Done():
			return
		}
	}
}

// startWorker launches a worker goroutine for the given peer address.
// Caller must NOT hold p.mu.
func (p *peerPool) startWorker(ctx context.Context, addr string) {
	workerCtx, cancel := context.WithCancel(ctx)
	now := time.Now()

	aw := &activeWorker{
		addr:     addr,
		cancel:   cancel,
		prevTime: now,
		started:  now,
	}

	p.mu.Lock()
	p.active[addr] = aw
	p.mu.Unlock()

	p.wg.Add(1)
	go func() {
		defer p.wg.Done()
		p.runWorker(workerCtx, aw)
		p.doneCh <- addr
	}()
}

// runWorker is the worker entry point. It's the same as the old worker()
// function but reports bytes to the activeWorker's atomic counter.
func (p *peerPool) runWorker(ctx context.Context, aw *activeWorker) {
	const maxRetries = 5
	retries := 0

	for retries < maxRetries {
		select {
		case <-ctx.Done():
			return
		default:
		}

		downloaded := p.runWorkerSession(ctx, aw)
		if downloaded > 0 {
			retries = 0
		} else {
			retries++
		}

		select {
		case <-ctx.Done():
			return
		case <-time.After(time.Duration(retries) * time.Second):
		}
	}
}

// runWorkerSession handles one connection lifecycle, reporting bytes
// to the activeWorker's atomic counter for speed tracking.
func (p *peerPool) runWorkerSession(ctx context.Context, aw *activeWorker) int {
	conn, err := peer.Dial(aw.addr, p.torrent.InfoHash, p.peerID)
	if err != nil {
		return 0
	}
	defer conn.Close()

	bitfield, err := NegotiateUnchoke(conn, len(p.torrent.Pieces))
	if err != nil {
		return 0
	}

	p.picker.AddPeer(bitfield)
	defer p.picker.RemovePeer(bitfield)

	if p.prog != nil {
		p.prog.PeerConnected(aw.addr, bitfield)
		defer p.prog.PeerDisconnected(aw.addr)
	}

	var onBlock BlockCallback
	onBlock = func(_, _, blockLen int) {
		aw.bytes.Add(int64(blockLen))
		if p.prog != nil {
			p.prog.BlockReceived(aw.addr, blockLen)
		}
	}

	return pipelinedDownload(conn, aw.addr, p.torrent, p.picker, bitfield,
		p.results, p.prog, onBlock, p.pipelineDepth)
}

// handleWorkerExit cleans up after a worker exits and backfills the slot.
func (p *peerPool) handleWorkerExit(ctx context.Context, addr string) {
	p.mu.Lock()
	delete(p.active, addr)
	slotsFree := p.maxSlots - len(p.active)
	p.mu.Unlock()

	// Backfill from untried pool.
	for range slotsFree {
		next := p.pickUntried()
		if next == "" {
			break
		}
		p.startWorker(ctx, next)
	}
}

// evaluate ranks active workers by speed, cancels the slowest rotateCount,
// and starts replacements from the untried pool.
func (p *peerPool) evaluate(ctx context.Context) {
	p.mu.Lock()

	now := time.Now()

	// Collect speeds for workers past the grace period.
	type ranked struct {
		addr  string
		speed float64
	}
	var eligible []ranked
	for _, aw := range p.active {
		if now.Sub(aw.started) < p.gracePeriod {
			aw.snapshot(now) // update baseline but don't evaluate yet
			continue
		}
		eligible = append(eligible, ranked{aw.addr, aw.speed(now)})
		aw.snapshot(now)
	}

	// Need untried peers to rotate in — no point evicting without replacements.
	available := len(p.untried)
	if available == 0 || len(eligible) == 0 {
		p.mu.Unlock()
		return
	}

	// Sort slowest first.
	sort.Slice(eligible, func(i, j int) bool {
		return eligible[i].speed < eligible[j].speed
	})

	// How many to rotate: min(rotateCount, available untried, eligible workers).
	toRotate := p.rotateCount
	if toRotate > available {
		toRotate = available
	}
	if toRotate > len(eligible) {
		toRotate = len(eligible)
	}

	// Cancel the slowest workers.
	var toCancel []string
	for i := range toRotate {
		addr := eligible[i].addr
		if aw, ok := p.active[addr]; ok {
			aw.cancel()
			toCancel = append(toCancel, addr)
		}
	}

	p.mu.Unlock()

	// Start replacements (workers will clean up asynchronously via doneCh).
	for range len(toCancel) {
		next := p.pickUntried()
		if next == "" {
			break
		}
		p.startWorker(ctx, next)
	}
}

// pickUntried pops the next untried peer address, or "" if none remain.
func (p *peerPool) pickUntried() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.untried) == 0 {
		return ""
	}
	addr := p.untried[0]
	p.untried = p.untried[1:]
	return addr
}
