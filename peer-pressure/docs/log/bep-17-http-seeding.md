# BEP 17 — HTTP Seeding (Hoffman Style)

## What We Built

Implemented BEP 17, the "Hoffman style" HTTP seeding protocol. Unlike BEP 19
(GetRight style, which uses HTTP Range requests on static file URLs), BEP 17
uses script URLs with query parameters. The server-side script maps piece
numbers to file bytes.

**Files created:**
- `download/httpseed.go` — BEP 17 worker: constructs `?info_hash=&piece=N` URLs, handles 503 retry
- `download/httpseed_test.go` — 6 tests

**Files modified:**
- `torrent/torrent.go` — added `HTTPSeeds` field, parsed `httpseeds` key
- `torrent/torrent_test.go` — 3 httpseeds parsing tests
- `download/session.go` — starts BEP 17 workers alongside BEP 19 workers
- `cmd/peer-pressure/main.go` — passes `HTTPSeeds` to download config

## BitTorrent Concepts

### Two HTTP Seeding Approaches

The BitTorrent ecosystem has two competing HTTP seeding specs:

| | BEP 17 (Hoffman) | BEP 19 (GetRight) |
|---|---|---|
| Torrent key | `httpseeds` | `url-list` |
| URL format | Script URL + query params | Direct file URL |
| Server needs | PHP/Python/Go script | Static file server |
| Request | `?info_hash=<hash>&piece=3` | `Range: bytes=768-1023` |
| Setup difficulty | Harder (need script) | Easier (nginx/Apache) |

Both achieve the same goal: supplement the peer swarm with an HTTP server that
has the complete file.

### The BEP 17 Request Format

```
GET /seed.php?info_hash=%01%02...%14&piece=3 HTTP/1.1
Host: www.example.com
```

- `info_hash`: 20-byte hash, percent-encoded
- `piece`: zero-indexed piece number
- Optional: `ranges=<start>-<end>` for sub-piece requests

### The 503 Back-Pressure Mechanism

BEP 17 servers can throttle clients:

```
HTTP/1.1 503 Service Unavailable

30
```

The body is an ASCII integer: seconds to wait before retrying. This is a
simple but effective rate limiting mechanism — no need for complex headers.

Our implementation:
```go
if resp.StatusCode == http.StatusServiceUnavailable {
    body, _ := io.ReadAll(resp.Body)
    secs, _ := strconv.Atoi(string(body))
    if secs <= 0 {
        secs = 30  // default if body is empty or invalid
    }
    return nil, &retryError{wait: time.Duration(secs) * time.Second}
}
```

### Why Both BEP 17 and BEP 19?

Real-world torrents may have both `httpseeds` and `url-list`. Our client
launches workers for each, and they all feed into the same piece picker.
The picker doesn't care where pieces come from — peers, BEP 19 seeds, or
BEP 17 seeds all produce `pieceResult` values.

## Go Idioms Used

### Custom Error Type for Control Flow

```go
type retryError struct {
    wait time.Duration
}

func (e *retryError) Error() string {
    return fmt.Sprintf("503 retry after %s", e.wait)
}

func retryAfter(err error) (time.Duration, bool) {
    if re, ok := err.(*retryError); ok {
        return re.wait, true
    }
    return 0, false
}
```

The 503 response is an error (the request failed), but it carries structured
data (how long to wait). A custom error type lets the caller decide whether
to retry, without parsing error strings.

The type assertion `err.(*retryError)` is the standard Go pattern for this.
Compare to `errors.As()` — here we own the error type, so direct assertion
is fine.

### Shared Abstractions Between Workers

Both `webseedWorker` (BEP 19) and `httpseedWorker` (BEP 17) follow the
same pattern:

1. Register a full bitfield with the picker
2. Loop: pick piece → fetch → verify hash → send result
3. On error: abort piece, back off, retry

They don't share code through inheritance (Go doesn't have it). Instead,
they share the contract: both write `pieceResult` to the same channel.
The consumer (session.go) doesn't distinguish between piece sources.

This is the Go way — shared behavior through shared interfaces/channels,
not through shared base classes.

### Fixed-Size Buffer for Percent Encoding

```go
func percentEncodeInfoHash(hash [20]byte) string {
    var buf [60]byte // worst case: 20 × 3 = 60
    n := 0
    for _, b := range hash {
        // ...
    }
    return string(buf[:n])
}
```

Since info hashes are always exactly 20 bytes, the worst-case output is
60 bytes (every byte percent-encoded). A fixed-size array on the stack
avoids heap allocation entirely.

## Test Coverage

| Test | What It Verifies |
|------|-----------------|
| `TestHTTPSeedFetchPiece` | Single piece download, query params, hash verification |
| `TestHTTPSeedMultiplePieces` | Three pieces downloaded sequentially |
| `TestHTTPSeed503Retry` | 503 → wait → retry succeeds on second attempt |
| `TestHTTPSeedHashMismatch` | Corrupt data → error reported |
| `TestPercentEncodeInfoHash` | Mixed unreserved/reserved bytes encoded correctly |
| `TestPercentEncodeInfoHashAllZeros` | All-zero hash → all `%00` |
| `TestParseHTTPSeeds/list` | Two httpseeds parsed from torrent |
| `TestParseHTTPSeeds/absent` | No httpseeds key → empty slice |
| `TestParseHTTPSeeds/empty strings filtered` | Empty URLs filtered out |
