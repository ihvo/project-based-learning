# BEP 17 — HTTP Seeding (Hoffman Style)

> Reference: <https://www.bittorrent.org/beps/bep_0017.html>

---

## 1. Summary

BEP 17 defines a second HTTP seeding mechanism distinct from BEP 19 (GetRight style). Where BEP 19 treats the HTTP server as a plain file mirror and uses standard `Range` headers against a file URL, BEP 17 treats the server as a BitTorrent-aware seed that understands pieces and blocks natively.

**Key differences from BEP 19:**

| Aspect | BEP 19 (GetRight) | BEP 17 (Hoffman) |
|---|---|---|
| Torrent key | `url-list` | `httpseeds` |
| Request method | GET with `Range` header | GET with `?piece=N&ranges=...` query params |
| Server awareness | Dumb file server | Torrent-aware — knows about pieces |
| Multi-block fetch | One Range header per request | Multiple ranges within a piece in one request |
| Response format | Raw file bytes | Raw piece data (concatenated ranges) |

**Why it matters:** Some trackers and private communities still publish `.torrent` files with `httpseeds` URLs. Supporting BEP 17 alongside BEP 19 means our client can exploit every available HTTP source, not just GetRight-style mirrors.

---

## 2. Protocol Specification

### 2.1 Torrent Metainfo

BEP 17 seeds are declared in the top-level `httpseeds` key of the `.torrent` file:

```
d
  ...
  8:httpseedsl
    30:http://seed.example.com/seed
    35:http://mirror.example.com/serve
  e
  ...
e
```

The value is a bencoded list of URL strings. Each URL is the base endpoint for piece requests.

### 2.2 Request Format

To fetch data, the client issues an HTTP GET to the seed URL with query parameters:

```
GET <seed_url>?info_hash=<urlencoded_20_bytes>&piece=<N>&ranges=<start>-<end>[,<start>-<end>...]
```

**Parameters:**

| Parameter | Type | Description |
|---|---|---|
| `info_hash` | URL-encoded 20 bytes | The raw info_hash identifying the torrent |
| `piece` | integer | Zero-based piece index |
| `ranges` | comma-separated `start-end` pairs | Byte offsets within the piece (inclusive start, exclusive end) |

**Example — requesting blocks 0–16383 and 16384–32767 of piece 42:**

```
GET http://seed.example.com/seed?info_hash=%a3%f1...&piece=42&ranges=0-16384,16384-32768
```

Note: ranges use `start-end` where `start` is inclusive and `end` is exclusive (i.e., `end = start + block_length`). This matches standard block addressing: a 16 KiB block at offset 0 is `0-16384`.

### 2.3 Response Format

On success, the server responds with:

```
HTTP/1.1 200 OK
Content-Type: application/octet-stream
Content-Length: <total bytes across all requested ranges>

<raw concatenated block data>
```

The response body contains the requested byte ranges concatenated in the order they were requested. If two 16 KiB blocks were requested, the response is exactly 32768 bytes.

### 2.4 Requesting a Full Piece

To request an entire piece, omit the `ranges` parameter:

```
GET http://seed.example.com/seed?info_hash=%a3%f1...&piece=42
```

The server responds with the full piece data. For the last piece, the response length may be shorter than `piece_length`.

### 2.5 Error Handling

#### HTTP 503 — Server Busy

When the server is overloaded, it returns HTTP 503 with an optional `Retry-After` header:

```
HTTP/1.1 503 Service Unavailable
Retry-After: 120
```

The client **must** respect the `Retry-After` value (in seconds) before retrying. If `Retry-After` is absent, the client should use exponential backoff starting at 30 seconds.

#### Other Errors

| Status Code | Action |
|---|---|
| 200 | Success — process response body |
| 400 | Bad request — malformed parameters, do not retry |
| 404 | Piece not available — treat seed as not having this piece |
| 503 | Busy — backoff per `Retry-After` |
| Other 4xx/5xx | Temporary error — backoff and retry |

### 2.6 State Machine

```
┌──────────┐
│   IDLE   │◄──────────────────────────────────────┐
└────┬─────┘                                       │
     │ Pick piece from Picker                      │
     ▼                                             │
┌──────────────┐                                   │
│ BUILD REQUEST│ Construct URL with piece + ranges  │
└────┬─────────┘                                   │
     │                                             │
     ▼                                             │
┌──────────────┐  HTTP 503   ┌───────────┐         │
│ SEND REQUEST ├────────────►│  BACKOFF  ├─────────┘
└────┬─────────┘             └───────────┘
     │ HTTP 200                   ▲
     ▼                            │
┌──────────────┐  hash fail       │
│ VERIFY HASH  ├──────────────────┘
└────┬─────────┘
     │ hash OK
     ▼
┌──────────┐
│   DONE   │ → send to results channel
└──────────┘
```

### 2.7 URL-Encoding the info_hash

The `info_hash` parameter must be URL-encoded as raw bytes, not as a hex string. Each of the 20 bytes is percent-encoded:

```go
// Correct: raw byte URL encoding
info_hash=%a3%f1%00%de...  // 20 bytes, each percent-encoded

// Wrong: hex string
info_hash=a3f100de...  // This is NOT what BEP 17 specifies
```

Use `url.QueryEscape(string(infoHash[:]))` in Go — this percent-encodes all non-unreserved characters.

---

## 3. Implementation Plan

### 3.1 Files to Create

**`download/httpseed.go`** — The BEP 17 HTTP seed worker, analogous to `download/webseed.go` (BEP 19).

**`download/httpseed_test.go`** — Unit tests with a mock HTTP server.

### 3.2 Files to Modify

**`torrent/torrent.go`** — Parse the `httpseeds` key from the metainfo dictionary.

Add a new field to the `Torrent` struct:

```go
type Torrent struct {
    // ... existing fields ...
    HTTPSeeds []string // BEP 17 HTTP seed URLs; nil if absent
}
```

Add parsing logic in `Parse()` to extract `httpseeds` from the top-level dict, following the same pattern as the existing `url-list` parsing for BEP 19.

**`download/session.go`** — Launch `httpseedWorker` goroutines alongside the existing `webseedWorker` goroutines. Add an `HTTPSeeds` field to `Config`:

```go
type Config struct {
    // ... existing fields ...
    HTTPSeeds []string // BEP 17 HTTP seed URLs
}
```

### 3.3 Key Types

```go
// httpseedWorker downloads pieces from a BEP 17 HTTP seed.
// Unlike webseedWorker (BEP 19) which uses byte-range requests against a
// file URL, this uses piece-index + block-range query parameters against
// a torrent-aware seed endpoint.
type httpseedWorker struct {
    url       string
    torrent   *torrent.Torrent
    picker    *Picker
    results   chan<- pieceResult
    prog      *Progress
    client    *http.Client
    bytes     atomic.Int64
    backoff   time.Duration  // current backoff after 503
}
```

### 3.4 Key Functions

```go
// newHTTPSeedWorker constructs a BEP 17 worker.
func newHTTPSeedWorker(seedURL string, t *torrent.Torrent, picker *Picker,
    results chan<- pieceResult, prog *Progress) *httpseedWorker

// run picks pieces and downloads them until ctx is canceled or all done.
func (w *httpseedWorker) run(ctx context.Context)

// fetchPiece downloads one full piece from the BEP 17 seed.
// Constructs the URL with info_hash, piece index, and optional block ranges.
func (w *httpseedWorker) fetchPiece(ctx context.Context, idx int) ([]byte, error)

// buildURL constructs the request URL with query parameters.
func (w *httpseedWorker) buildURL(pieceIdx int, ranges [][2]int) string

// handleRetryAfter parses the Retry-After header and returns the duration.
// Falls back to exponential backoff if the header is absent.
func (w *httpseedWorker) handleRetryAfter(resp *http.Response) time.Duration
```

### 3.5 Package Placement

All new code lives in the existing `download/` and `torrent/` packages. No new packages required — BEP 17 is a download source, just like BEP 19.

---

## 4. Dependencies

| BEP | Relationship |
|---|---|
| **BEP 3** | Base protocol — piece hashing, piece length, info dict structure |
| **BEP 19** | Sister HTTP seeding spec — shares the `Picker` and `pieceResult` infrastructure. Clients should support both and run them concurrently |
| **BEP 12** | Multi-tracker — the torrent may have multiple tracker tiers alongside HTTP seeds |
| **BEP 52** | BitTorrent v2 — if the torrent is v2, piece hashing changes from SHA-1 to SHA-256, and piece alignment is per-file. The worker must use the correct hash function |

### Internal Dependencies

- `torrent.Torrent` — for piece hashes, piece length, info hash
- `download.Picker` — rarest-first piece selection
- `download.pieceResult` — result channel shared with peer workers
- `download.Progress` — progress tracking integration

---

## 5. Testing Strategy

### 5.1 Unit Tests (`download/httpseed_test.go`)

**`TestHTTPSeedBuildURL`** — Verify URL construction:
- Single piece, no ranges → `?info_hash=...&piece=5`
- Single piece, one range → `?info_hash=...&piece=5&ranges=0-16384`
- Single piece, multiple ranges → `?info_hash=...&piece=5&ranges=0-16384,16384-32768`
- Info hash with special bytes is correctly percent-encoded

**`TestHTTPSeedFetchPiece`** — Use `httptest.NewServer` to simulate a BEP 17 seed:
- Return correct piece data → verify hash matches
- Return short data → verify error
- Return wrong data → verify hash mismatch detection

**`TestHTTPSeedRetryAfter`** — Mock server returning 503:
- With `Retry-After: 60` header → verify worker waits ~60s before retry
- Without `Retry-After` header → verify exponential backoff is used
- Verify worker resumes downloading after backoff completes

**`TestHTTPSeedHTTP404`** — Mock server returning 404 for a specific piece:
- Verify worker aborts that piece and moves on to the next

**`TestHTTPSeedFullPieceDownload`** — End-to-end within the test:
- Create a mock server serving 3 pieces with known SHA-1 hashes
- Create a `Picker` and run the worker
- Verify all 3 pieces are received on the results channel with correct data

**`TestHTTPSeedCancelContext`** — Verify graceful shutdown:
- Start the worker, cancel the context mid-download
- Verify the worker exits without panic and in-flight pieces are aborted

### 5.2 Torrent Parsing Tests (`torrent/torrent_test.go`)

**`TestParseHTTPSeeds`** — Construct a bencoded torrent with `httpseeds` key:
- Single URL → `HTTPSeeds` has one entry
- Multiple URLs → `HTTPSeeds` has all entries in order
- Missing `httpseeds` key → `HTTPSeeds` is nil
- Both `httpseeds` and `url-list` present → both fields populated independently

### 5.3 Integration Test Outline

**`tests/httpseed_integration_test.go`** — Optional, higher-level:
- Spin up `httptest.NewServer` serving a small (3-piece) torrent's data
- Create a `Config` with no peers but one HTTP seed
- Call `download.File()` and verify the output file matches the original data
- Combine with one webseed (BEP 19) to verify both source types work simultaneously
