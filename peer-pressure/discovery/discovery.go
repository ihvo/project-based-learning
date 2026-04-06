// Package discovery provides a unified peer discovery interface that
// aggregates peers from multiple sources: trackers, DHT, PEX, and LSD.
//
// Each source implements the PeerSource interface. The Manager runs all
// sources concurrently and delivers deduplicated peer addresses on a
// single channel.
package discovery

import (
	"context"
	"sync"
)

// PeerSource is the interface that all peer discovery mechanisms implement.
type PeerSource interface {
	// Name returns a short identifier for logging (e.g., "tracker", "dht").
	Name() string

	// Peers discovers peers for the given info hash. It may be called
	// repeatedly and should return the latest known peers each time.
	// Implementations should respect ctx cancellation.
	Peers(ctx context.Context, infoHash [20]byte) ([]string, error)
}

// Manager aggregates peers from multiple PeerSource implementations.
// It deduplicates addresses and delivers new peers on a channel.
type Manager struct {
	sources []PeerSource
	mu      sync.Mutex
	seen    map[string]bool
}

// NewManager creates a Manager with the given peer sources.
func NewManager(sources ...PeerSource) *Manager {
	return &Manager{
		sources: sources,
		seen:    make(map[string]bool),
	}
}

// Discover starts all sources concurrently and returns a channel of
// deduplicated peer addresses. The channel is closed when all sources
// finish or the context is cancelled. Each source is queried once.
func (m *Manager) Discover(ctx context.Context, infoHash [20]byte) <-chan string {
	ch := make(chan string, 64)

	var wg sync.WaitGroup
	for _, src := range m.sources {
		wg.Add(1)
		go func(s PeerSource) {
			defer wg.Done()
			peers, err := s.Peers(ctx, infoHash)
			if err != nil {
				return // silently skip failed sources
			}
			for _, addr := range peers {
				if m.addIfNew(addr) {
					select {
					case ch <- addr:
					case <-ctx.Done():
						return
					}
				}
			}
		}(src)
	}

	go func() {
		wg.Wait()
		close(ch)
	}()

	return ch
}

// Seen returns true if the address was already discovered.
func (m *Manager) Seen(addr string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.seen[addr]
}

// Count returns the number of unique peers discovered so far.
func (m *Manager) Count() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.seen)
}

// Reset clears the seen set, allowing previously discovered peers
// to be emitted again on the next Discover call.
func (m *Manager) Reset() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.seen = make(map[string]bool)
}

func (m *Manager) addIfNew(addr string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.seen[addr] {
		return false
	}
	m.seen[addr] = true
	return true
}
