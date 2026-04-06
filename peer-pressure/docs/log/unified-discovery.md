# Unified Peer Discovery Interface

## What It Does

A BitTorrent client discovers peers from multiple independent sources:
HTTP/UDP trackers, DHT, PEX (Peer Exchange), and LSD (Local Service
Discovery). Each source has different latency, reliability, and scope.
The discovery manager provides a single, deduplicated stream of peer
addresses by running all sources concurrently.

### The Problem

Without unification, the download logic must know about each discovery
mechanism individually — calling tracker announce, DHT get_peers, reading
PEX messages, and listening for LSD broadcasts. This creates tight coupling
and makes it hard to add new sources.

### The Solution: PeerSource Interface

```go
type PeerSource interface {
    Name() string
    Peers(ctx context.Context, infoHash [20]byte) ([]string, error)
}
```

Any discovery mechanism wraps itself in this interface. The Manager:
1. Runs all sources as concurrent goroutines
2. Collects peer addresses from each
3. Deduplicates (same address from tracker AND DHT = one emission)
4. Delivers on a buffered channel
5. Closes the channel when all sources finish or ctx is cancelled

### Failure Isolation

If a tracker is down or DHT bootstrap fails, the other sources still
deliver peers. The Manager silently ignores errors from individual sources.
This matches how real BitTorrent clients work — they don't block on any
single source.

### What We Implemented

- **`PeerSource` interface** — `Name()` + `Peers(ctx, infoHash)`
- **`Manager`** — concurrent fan-in, deduplication, context cancellation
- **`Seen()`/`Count()`/`Reset()`** — introspection and state management

## Go Idioms

### Interface-Based Dependency Inversion

```go
type PeerSource interface {
    Name() string
    Peers(ctx context.Context, infoHash [20]byte) ([]string, error)
}
```

The Manager depends on an interface, not concrete types. This means we can
test with mock sources, add new discovery mechanisms without modifying the
Manager, and compose sources freely. Go's implicit interface satisfaction
means existing types just need to add these two methods.

### Fan-In with WaitGroup + Channel

```go
var wg sync.WaitGroup
for _, src := range m.sources {
    wg.Add(1)
    go func(s PeerSource) {
        defer wg.Done()
        // ... send peers to ch ...
    }(src)
}
go func() {
    wg.Wait()
    close(ch)
}()
return ch
```

This is the standard Go fan-in pattern:
- One goroutine per source (fan-out)
- All write to the same channel (fan-in)
- A separate goroutine waits for all to finish, then closes the channel
- The caller ranges over the channel until it's closed

The `wg.Wait()` + `close()` goroutine ensures the channel is closed exactly
once, after all sources are done. No data races, no premature close.

### Mutex-Protected Deduplication

```go
func (m *Manager) addIfNew(addr string) bool {
    m.mu.Lock()
    defer m.mu.Unlock()
    if m.seen[addr] {
        return false
    }
    m.seen[addr] = true
    return true
}
```

Multiple goroutines call `addIfNew` concurrently. The lock protects the
`seen` map. Returning a bool lets the caller decide whether to send to
the channel — keeping the critical section minimal (just the map check,
no channel operations under lock).

### Context for Cancellation

```go
select {
case ch <- addr:
case <-ctx.Done():
    return
}
```

Every send to the channel also checks ctx. This prevents goroutine leaks
when the caller cancels early (e.g., enough peers found, or download
complete). Without this, slow sources would block on a full channel forever.
