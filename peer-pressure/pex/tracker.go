package pex

import (
	"net"
	"sync"
)

// MaxAdded is the maximum number of added peers per PEX message.
const MaxAdded = 50

// MaxDropped is the maximum number of dropped peers per PEX message.
const MaxDropped = 50

// DiffTracker tracks the set of known peers for a swarm and computes
// diffs for PEX messages. Each peer connection should have its own
// DiffTracker to track what that specific connection has been told.
type DiffTracker struct {
	mu       sync.Mutex
	current  map[string]PeerEntry // key: "ip:port"
	lastSent map[string]PeerEntry // snapshot at last Diff()
}

// NewDiffTracker creates a DiffTracker with empty initial state.
func NewDiffTracker() *DiffTracker {
	return &DiffTracker{
		current:  make(map[string]PeerEntry),
		lastSent: make(map[string]PeerEntry),
	}
}

// AddPeer records a peer in the current known set.
func (t *DiffTracker) AddPeer(entry PeerEntry) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.current[entry.Addr()] = entry
}

// RemovePeer removes a peer from the current known set.
func (t *DiffTracker) RemovePeer(ip net.IP, port uint16) {
	t.mu.Lock()
	defer t.mu.Unlock()
	e := PeerEntry{IP: ip, Port: port}
	delete(t.current, e.Addr())
}

// Diff computes the added/dropped sets since the last Diff() call.
// Results are capped at MaxAdded and MaxDropped. After calling,
// the internal lastSent state is updated to the current snapshot.
func (t *DiffTracker) Diff() *Message {
	t.mu.Lock()
	defer t.mu.Unlock()

	var added4, dropped4 []PeerEntry
	var added6, dropped6 []PeerEntry

	// added = current - lastSent
	for key, entry := range t.current {
		if _, ok := t.lastSent[key]; !ok {
			if entry.IP.To4() != nil {
				added4 = append(added4, entry)
			} else {
				added6 = append(added6, entry)
			}
		}
	}

	// dropped = lastSent - current
	for key, entry := range t.lastSent {
		if _, ok := t.current[key]; !ok {
			if entry.IP.To4() != nil {
				dropped4 = append(dropped4, entry)
			} else {
				dropped6 = append(dropped6, entry)
			}
		}
	}

	// Cap at limits.
	if len(added4) > MaxAdded {
		added4 = added4[:MaxAdded]
	}
	if len(dropped4) > MaxDropped {
		dropped4 = dropped4[:MaxDropped]
	}
	if len(added6) > MaxAdded {
		added6 = added6[:MaxAdded]
	}
	if len(dropped6) > MaxDropped {
		dropped6 = dropped6[:MaxDropped]
	}

	// Snapshot current as lastSent.
	t.lastSent = make(map[string]PeerEntry, len(t.current))
	for k, v := range t.current {
		t.lastSent[k] = v
	}

	return &Message{
		Added:    added4,
		Dropped:  dropped4,
		Added6:   added6,
		Dropped6: dropped6,
	}
}

// Count returns the number of peers currently tracked.
func (t *DiffTracker) Count() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return len(t.current)
}
