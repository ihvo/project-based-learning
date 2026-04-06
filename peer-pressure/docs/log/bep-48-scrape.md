# BEP 48 — Tracker Scrape

## What We Built

A scrape function that queries trackers for swarm statistics (seeders, leechers,
download count) without performing a full announce. Supports both HTTP and UDP
tracker protocols.

**Files created:**
- `tracker/scrape.go` — `Scrape()`, `scrapeHTTP()`, `scrapeURL()`, `parseScrapeResponse()`, `udpScrape()`, `udpScrapeRequest()`
- `tracker/scrape_test.go` — 13 tests

## BitTorrent Concepts

### Announce vs. Scrape

**Announce** says: *"I'm downloading this torrent, here's my info. Give me peers."*
The tracker adds you to the swarm and returns a peer list.

**Scrape** says: *"How's this torrent doing?"* The tracker returns aggregate
stats: seeders, leechers, total downloads. You don't join the swarm.

```
Announce → full peer list, registers you in swarm
Scrape   → just stats, no registration
```

Scrape is useful for:
- Showing swarm health before starting a download
- Choosing the best tracker from a multi-tracker list (BEP 12)
- Dashboard/UI torrent list views

### HTTP Scrape: URL Derivation

The clever part: the scrape URL is derived from the announce URL by replacing
the **last** `/announce` in the path with `/scrape`:

```
http://tracker.example.com:6969/announce       →  .../scrape
http://tracker.example.com/path/announce       →  .../path/scrape
http://tracker.example.com/announce?passkey=abc → .../scrape?passkey=abc
```

If the path doesn't contain `/announce`, the tracker doesn't support scrape.
Query parameters (like private tracker passkeys) are preserved.

### HTTP Scrape: Response Format

The response is a bencoded dict with a `files` key mapping raw info hashes
(20-byte binary strings as dict keys!) to stat dicts:

```
{
  "files": {
    <20 raw bytes>: {
      "complete": 150,     ← seeders
      "downloaded": 1000,  ← total completed downloads ever
      "incomplete": 30     ← leechers
    }
  }
}
```

The unusual part: the dict keys are **raw binary** — not hex strings. This is
one of the few places in BitTorrent where raw bytes appear as dictionary keys.

### UDP Scrape: Binary Protocol

UDP scrape uses action code 2 (connect=0, announce=1, scrape=2):

```
Request:   [connection_id:8][action=2:4][txn_id:4][info_hash:20]...
Response:  [action=2:4][txn_id:4][seeders:4][completed:4][leechers:4]...
```

Each 12-byte stat block corresponds positionally to the info hashes in the
request. The response doesn't include the hashes — you match by position.

### Batching

Both protocols support multiple info hashes in one request — you can scrape
your entire torrent list in a single round-trip. The UDP format can fit ~74
hashes in a standard MTU packet (`(1500 - 16) / 20 ≈ 74`).

## Go Idioms Used

### `strings.LastIndex` for URL Transformation

The spec says to replace the *last* occurrence of `/announce`:

```go
idx := strings.LastIndex(u.Path, "/announce")
if idx == -1 {
    return "", fmt.Errorf("not scrapeable")
}
u.Path = u.Path[:idx] + "/scrape" + u.Path[idx+len("/announce"):]
```

`LastIndex` is critical because a path like `/announce/announce` should
transform to `/announce/scrape`, not `/scrape/announce`.

### `net/url.Parse` + `RawQuery` for URL Surgery

When building the scrape URL with info_hash parameters, we manipulate `RawQuery`
directly instead of using `url.Values`:

```go
var rawQuery strings.Builder
if base.RawQuery != "" {
    rawQuery.WriteString(base.RawQuery)  // preserve existing query params
}
for _, ih := range infoHashes {
    if rawQuery.Len() > 0 {
        rawQuery.WriteByte('&')
    }
    rawQuery.WriteString("info_hash=")
    rawQuery.WriteString(percentEncodeBytes(ih[:]))
}
base.RawQuery = rawQuery.String()
```

We use `RawQuery` because `url.Values.Encode()` would double-encode our
already percent-encoded binary hash. The `percentEncodeBytes` function handles
the raw-byte-to-percent encoding that standard URL encoding doesn't do.

### Binary Struct Parsing with Positional Matching

The UDP response has no keys — just sequential 12-byte blocks matched by
position to the request's info hashes:

```go
results := make([]ScrapeResult, len(infoHashes))
for i := range infoHashes {
    off := 8 + i*12
    results[i] = ScrapeResult{
        InfoHash:   infoHashes[i],   // matched by position, not by content
        Complete:   int(binary.BigEndian.Uint32(resp[off : off+4])),
        Downloaded: int(binary.BigEndian.Uint32(resp[off+4 : off+8])),
        Incomplete: int(binary.BigEndian.Uint32(resp[off+8 : off+12])),
    }
}
```

This positional matching is a common pattern in compact binary protocols.
It saves bandwidth (no need to repeat the 20-byte hash in the response) at
the cost of requiring strict ordering.

### Reusing Existing Infrastructure

The scrape implementation reuses helpers from the announce code:
- `udpConnect()` — the connection handshake is identical
- `udpRoundTrip()` — same retry logic and timeout handling
- `percentEncodeBytes()` — same binary-to-URL encoding
- `udpHostFromURL()` — same URL-to-host extraction

This is a good example of the DRY principle in Go: the tracker package's
internal helpers serve both announce and scrape without being exported.

### `httptest.Server` for Integration Tests

Go's `net/http/httptest` makes it trivial to test HTTP clients:

```go
server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
    if r.URL.Path != "/scrape" {
        t.Errorf("unexpected path: %s", r.URL.Path)
    }
    w.Write(respBody)
}))
defer server.Close()

results, err := Scrape(server.URL+"/announce", hashes)
```

The server starts on a random port, the URL is available immediately, and
`defer server.Close()` handles cleanup. No need for mocking HTTP transports.

## Test Coverage

| Test | What It Verifies |
|------|-----------------|
| `TestScrapeURL_Standard` | `/announce` → `/scrape` basic case |
| `TestScrapeURL_WithPath` | Nested path preserved |
| `TestScrapeURL_WithQuery` | Query parameters (passkey) preserved |
| `TestScrapeURL_NotScrapeable` | Error when no `/announce` in URL |
| `TestScrapeURL_AnnounceInMiddle` | Last `/announce` replaced, suffix kept |
| `TestParseScrapeResponse_SingleHash` | One hash parsed correctly |
| `TestParseScrapeResponse_MultipleHashes` | Three hashes, all stats correct |
| `TestParseScrapeResponse_MissingHash` | Missing hash → zero values, no error |
| `TestParseScrapeResponse_FailureReason` | `failure reason` → proper error |
| `TestParseScrapeResponse_EmptyFiles` | Empty `files` dict → zero values |
| `TestScrapeHTTP_Integration` | End-to-end HTTP scrape with httptest |
| `TestScrapeHTTP_WithPasskey` | Passkey preserved through URL transform |
| `TestUDPScrapeRequestParsing` | Binary response layout verified |
