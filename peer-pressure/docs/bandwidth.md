# Bandwidth Throttling

> Rate-limits upload and download traffic using a token bucket algorithm,
> preventing Peer Pressure from saturating the user's network connection.

---

## 1. Summary

Bandwidth throttling lets users set maximum upload and download rates. Without
it, a BitTorrent client will aggressively consume all available bandwidth,
degrading web browsing, video calls, and other applications on the same
network.

The implementation uses a **token bucket algorithm** — a well-understood
rate-limiting primitive that allows smooth rate control with configurable
burst capacity. The throttle is implemented as `io.Reader` and `io.Writer`
wrappers that block when the bucket is empty, making integration with the
existing peer connection code straightforward.

Key properties:

- **Separate limits**: independent upload and download rate limits.
- **Global limit**: optional combined limit across all connections.
- **Per-connection fairness**: when multiple peers share a limiter, each gets
  a fair share of the available bandwidth.
- **Burst tolerance**: short bursts above the rate are allowed (bucket
  capacity = 2× the per-second rate, minimum 64 KiB).
- **No throttle by default**: unlimited when no flags are set.

---

## 2. Design

### 2.1 Token bucket algorithm

A token bucket works as follows:

```
bucket:
    tokens    float64     // current tokens (bytes)
    capacity  int         // max tokens (burst size)
    rate      int         // tokens added per second (bytes/sec)
    lastFill  time.Time   // last time tokens were added

consume(n int):
    fill()  // add tokens for elapsed time
    if tokens >= n:
        tokens -= n
        return immediately
    else:
        wait_time = (n - tokens) / rate
        sleep(wait_time)
        tokens = 0
        return

fill():
    elapsed = now - lastFill
    tokens = min(tokens + rate * elapsed, capacity)
    lastFill = now
```

### 2.2 Burst capacity

The bucket capacity determines how much burst traffic is allowed:

```
capacity = max(2 * rate, 64 * 1024)
```

- For a 1 MiB/s limit: capacity = 2 MiB (can burst to 2 MiB then throttle).
- For a 10 KiB/s limit: capacity = 64 KiB (minimum burst).
- The 64 KiB minimum ensures that even very low rate limits can handle at
  least one full TCP segment burst.

### 2.3 Wrapper architecture

```
              ┌─────────────────────────────┐
              │     Global Download Limiter  │  (optional)
              │     rate = --max-download    │
              └──────────┬──────────────────┘
                         │
         ┌───────────────┼───────────────┐
         │               │               │
    ┌────▼────┐    ┌────▼────┐    ┌────▼────┐
    │  Peer 1 │    │  Peer 2 │    │  Peer 3 │
    │  conn   │    │  conn   │    │  conn   │
    └─────────┘    └─────────┘    └─────────┘

Each peer.Conn wraps its net.Conn reader through the global limiter.
The limiter's consume() blocks all readers fairly.
```

Similarly for upload:

```
              ┌─────────────────────────────┐
              │     Global Upload Limiter    │
              │     rate = --max-upload      │
              └──────────┬──────────────────┘
                         │
         ┌───────────────┼───────────────┐
         │               │               │
    peer.Conn.Write  peer.Conn.Write  peer.Conn.Write
```

### 2.4 io.Reader / io.Writer wrappers

The throttle integrates with existing code by wrapping the underlying
connection's reader/writer:

```go
// Wrapping a reader (download throttle):
throttledReader := limiter.Reader(conn)
// throttledReader.Read() calls limiter.consume(n) before returning bytes

// Wrapping a writer (upload throttle):
throttledWriter := limiter.Writer(conn)
// throttledWriter.Write() calls limiter.consume(n) before writing bytes
```

This is transparent to `peer.Conn` — it already reads from a `bufio.Reader`
which can wrap any `io.Reader`.

### 2.5 Fairness

When multiple goroutines share the same `Limiter`, fairness comes naturally
from Go's `sync.Cond` or channel-based wake mechanism:

```
All blocked goroutines wait on a condition variable.
When tokens are replenished (time passes), signal all waiters.
Waiters re-check: if enough tokens, consume and proceed.
Otherwise, go back to waiting.
```

This gives roughly FIFO fairness. For stricter fairness, a per-connection
sub-limiter can be derived:

```
per_conn_rate = global_rate / active_connections
```

But the simple shared bucket is sufficient for most use cases — BitTorrent
traffic is already bursty at the block level.

### 2.6 Rate suffixes

CLI flags accept human-readable rate suffixes:

| Suffix | Multiplier | Example |
|--------|-----------|---------|
| (none) | 1 | `1048576` = 1 MiB/s |
| `K` | 1024 | `500K` = 512,000 bytes/s |
| `M` | 1024² | `5M` = 5,242,880 bytes/s |
| `G` | 1024³ | `1G` = 1,073,741,824 bytes/s |

A rate of `0` means unlimited (no limiter applied).

### 2.7 Integration points

| Component | How it uses the limiter |
|-----------|------------------------|
| `peer.Conn` | Wrap the underlying `net.Conn` reader/writer with limiter |
| `download/session.go` | Pass download limiter when creating peer connections |
| `seed/upload.go` | Pass upload limiter when handling upload connections |
| `download/webseed.go` | Wrap HTTP response body reader with download limiter |

---

## 3. Implementation Plan

### 3.1 Package placement

New `throttle/` package — self-contained, no dependencies on other Peer
Pressure packages.

### 3.2 New files

| File | Purpose |
|------|---------|
| `throttle/throttle.go` | `Limiter` struct, token bucket logic, `Reader`/`Writer` wrappers |
| `throttle/throttle_test.go` | Unit tests |
| `throttle/parse.go` | Rate string parsing (e.g. `"5M"` → `5242880`) |
| `throttle/parse_test.go` | Rate parsing tests |

### 3.3 Modified files

| File | Changes |
|------|---------|
| `download/session.go` | Accept optional `Limiter` in `Config`, wrap peer connections |
| `peer/conn.go` | Accept optional `io.Reader`/`io.Writer` overrides, or wrap externally |
| `cmd/peer-pressure/main.go` | Add `--max-upload-rate` and `--max-download-rate` flags |

### 3.4 Key types

```go
// throttle/throttle.go

// Limiter implements a token bucket rate limiter.
type Limiter struct {
    mu       sync.Mutex
    cond     *sync.Cond
    tokens   float64
    capacity int       // max burst size in bytes
    rate     int       // bytes per second (0 = unlimited)
    lastFill time.Time
    closed   bool
}

// throttledReader wraps an io.Reader with rate limiting.
type throttledReader struct {
    r       io.Reader
    limiter *Limiter
}

// throttledWriter wraps an io.Writer with rate limiting.
type throttledWriter struct {
    w       io.Writer
    limiter *Limiter
}
```

```go
// throttle/parse.go

// Rate represents a bandwidth rate in bytes per second.
type Rate int
```

### 3.5 Key functions

```go
// throttle/throttle.go

// NewLimiter creates a rate limiter with the given bytes/sec rate.
// A rate of 0 means unlimited — Reader/Writer pass through without blocking.
func NewLimiter(bytesPerSec int) *Limiter

// Reader wraps an io.Reader with rate limiting.
// If the limiter's rate is 0 (unlimited), returns r unchanged.
func (l *Limiter) Reader(r io.Reader) io.Reader

// Writer wraps an io.Writer with rate limiting.
// If the limiter's rate is 0 (unlimited), returns w unchanged.
func (l *Limiter) Writer(w io.Writer) io.Writer

// consume blocks until n tokens are available, then deducts them.
func (l *Limiter) consume(n int)

// fill replenishes tokens based on elapsed time.
func (l *Limiter) fill()

// SetRate changes the rate dynamically (e.g. from a UI or config reload).
func (l *Limiter) SetRate(bytesPerSec int)

// Close wakes all blocked goroutines and prevents further blocking.
func (l *Limiter) Close()

// throttle/parse.go

// ParseRate parses a human-readable rate string like "5M", "500K", "1G".
// Returns bytes per second. Returns 0 for "0" or empty string (unlimited).
func ParseRate(s string) (int, error)
```

### 3.6 Config integration

```go
// download/session.go — additions to Config

type Config struct {
    // ... existing fields ...
    DownloadLimiter *throttle.Limiter // nil = unlimited
    UploadLimiter   *throttle.Limiter // nil = unlimited
}
```

### 3.7 CLI integration

```
peer-pressure download <torrent> --max-download-rate 5M --max-upload-rate 1M
peer-pressure seed <torrent> <data> --max-upload-rate 2M

Flags:
  --max-download-rate string    Maximum download rate (e.g. 500K, 5M, 1G). 0 = unlimited.
  --max-upload-rate string      Maximum upload rate (e.g. 500K, 5M, 1G). 0 = unlimited.
```

---

## 4. Dependencies

| Dependency | Type | Notes |
|------------|------|-------|
| `sync` | Go stdlib | Mutex, Cond for blocking and wake |
| `time` | Go stdlib | Token fill timing |
| `io` | Go stdlib | Reader/Writer interfaces |
| `strconv` | Go stdlib | Rate string parsing |
| `strings` | Go stdlib | Suffix detection |
| `download/session.go` | Modified | Accepts limiter in Config |
| `peer/conn.go` | Modified | Limiter wraps the connection's underlying reader/writer |

No new external dependencies. The `throttle/` package has zero imports from
other Peer Pressure packages — it's a standalone utility.

---

## 5. Testing Strategy

### 5.1 Rate parsing tests (`throttle/parse_test.go`)

| Test | Description |
|------|-------------|
| `TestParseRateBytes` | `"1048576"` → 1048576 |
| `TestParseRateKilo` | `"500K"` → 512000 |
| `TestParseRateMega` | `"5M"` → 5242880 |
| `TestParseRateGiga` | `"1G"` → 1073741824 |
| `TestParseRateLowercase` | `"5m"` → 5242880 (case insensitive) |
| `TestParseRateZero` | `"0"` → 0 (unlimited) |
| `TestParseRateEmpty` | `""` → 0 (unlimited) |
| `TestParseRateInvalid` | `"abc"` → error |
| `TestParseRateNegative` | `"-5M"` → error |

### 5.2 Limiter unit tests (`throttle/throttle_test.go`)

| Test | Description |
|------|-------------|
| `TestLimiterUnlimited` | Rate=0, consume(1000) returns instantly (no blocking) |
| `TestLimiterReaderPassthrough` | Rate=0, `Reader(r)` returns the original `r` unchanged |
| `TestLimiterWriterPassthrough` | Rate=0, `Writer(w)` returns the original `w` unchanged |
| `TestLimiterBurstCapacity` | Rate=1024, verify capacity = max(2048, 65536) = 65536 |
| `TestLimiterBurstCapacityLarge` | Rate=1048576, verify capacity = 2097152 |
| `TestLimiterConsumeSlow` | Rate=1024 bytes/sec, consume 2048 bytes, verify ~1 second elapsed |
| `TestLimiterConsumeMultiple` | Rate=1024, consume 512 three times, verify total time ~1 second |
| `TestLimiterSetRate` | Start with rate=1024, change to 2048, verify throughput doubles |
| `TestLimiterClose` | Close while goroutine is blocked in consume, verify it unblocks |
| `TestLimiterFillAccuracy` | Fill tokens over exactly 1 second, verify token count matches rate |

### 5.3 Reader/Writer wrapper tests

| Test | Description |
|------|-------------|
| `TestThrottledReaderRate` | Wrap a bytes.Reader at 10 KiB/s, read 30 KiB, verify it takes ~3 seconds (±0.5s tolerance) |
| `TestThrottledReaderData` | Wrap a reader with known data, verify all bytes read correctly (data integrity through throttle) |
| `TestThrottledWriterRate` | Wrap a bytes.Buffer at 10 KiB/s, write 20 KiB, verify ~2 seconds elapsed |
| `TestThrottledWriterData` | Wrap a buffer, write known data through throttle, verify buffer contents match |
| `TestThrottledReaderSmallReads` | Read 1 byte at a time through throttle, verify no deadlock and data correct |

### 5.4 Fairness tests

| Test | Description |
|------|-------------|
| `TestLimiterSharedFairness` | 4 goroutines sharing a 40 KiB/s limiter, each reading 10 KiB. Verify all complete in ~1 second (not one starved while others finish early). |
| `TestLimiterSharedTotalRate` | 4 goroutines sharing a 100 KiB/s limiter, reading for 2 seconds. Verify total bytes read ≈ 200 KiB. |

### 5.5 Integration tests

| Test | Description |
|------|-------------|
| `TestDownloadWithThrottle` | Set up a mock seeder, download a small torrent with download limiter set to 50 KiB/s. Verify the download rate doesn't exceed the limit (measure elapsed time vs data size). |
| `TestThrottleDoesNotCorruptData` | Download with throttle enabled, verify all piece hashes pass. (Ensures the Reader wrapper doesn't break framing.) |
