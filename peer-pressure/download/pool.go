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
	strikes map[string]int            // how many times a peer has been tried

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
		pipelineDepth = 20
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
		evalInterval:  5 * time.Second,
		gracePeriod:   5 * time.Second,
		untried:       untried,
		active:        make(map[string]*activeWorker, maxSlots),
		strikes:       make(map[string]int),
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
	p.pushPoolStats()

	evalTicker := time.NewTicker(p.evalInterval)
	defer evalTicker.Stop()

	for {
		select {
		case addr := <-p.doneCh:
			p.handleWorkerExit(ctx, addr)
			p.pushPoolStats()
		case <-evalTicker.C:
			p.evaluate(ctx)
			p.pushPoolStats()
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
	const maxRetries = 3
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
			// Reset strikes for productive peers so they get unlimited recycling.
			p.mu.Lock()
			delete(p.strikes, aw.addr)
			p.mu.Unlock()
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
// Peers are recycled (up to 3 strikes) so we don't permanently lose slots.
func (p *peerPool) handleWorkerExit(ctx context.Context, addr string) {
	p.mu.Lock()
	aw := p.active[addr]
	delete(p.active, addr)

	// Recycle peer if it hasn't struck out (max 3 attempts).
	if aw != nil {
		p.strikes[addr]++
		if p.strikes[addr] < 3 {
			p.untried = append(p.untried, addr)
		}
	}

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
// Dead workers (0 bytes after grace period) are evicted immediately without
// counting toward rotateCount — that budget is reserved for tuning among
// productive peers.
func (p *peerPool) evaluate(ctx context.Context) {
	p.mu.Lock()

	now := time.Now()

	type ranked struct {
		addr  string
		speed float64
	}
	var dead []string   // past grace, 0 bytes ever — evict unconditionally
	var live []ranked   // past grace, has downloaded something — rotate slowest

	for _, aw := range p.active {
		spd := aw.speed(now)

		// Push every worker's speed to the progress display.
		if p.prog != nil {
			p.prog.UpdatePeerSpeed(aw.addr, spd)
		}

		if now.Sub(aw.started) < p.gracePeriod {
			aw.snapshot(now)
			continue
		}

		aw.snapshot(now)

		if aw.bytes.Load() == 0 {
			dead = append(dead, aw.addr)
		} else {
			live = append(live, ranked{aw.addr, spd})
		}
	}

	// Cancel all dead workers immediately — they hold slots without contributing.
	var toCancel []string
	for _, addr := range dead {
		if aw, ok := p.active[addr]; ok {
			aw.cancel()
			toCancel = append(toCancel, addr)
		}
	}

	// From live workers, rotate the slowest rotateCount (speed optimization).
	available := len(p.untried)
	if available > len(toCancel) && len(live) > 0 {
		sort.Slice(live, func(i, j int) bool {
			return live[i].speed < live[j].speed
		})

		budget := p.rotateCount
		remaining := available - len(toCancel)
		if budget > remaining {
			budget = remaining
		}
		if budget > len(live) {
			budget = len(live)
		}
		for i := range budget {
			addr := live[i].addr
			if aw, ok := p.active[addr]; ok {
				aw.cancel()
				toCancel = append(toCancel, addr)
			}
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

// pushPoolStats sends current slot/queue counts to the progress display.
func (p *peerPool) pushPoolStats() {
	if p.prog == nil {
		return
	}
	p.mu.Lock()
	stats := PoolStats{
		ActiveSlots:  len(p.active),
		MaxSlots:     p.maxSlots,
		UntriedPeers: len(p.untried),
	}
	p.mu.Unlock()
	p.prog.SetPoolStats(stats)
}
