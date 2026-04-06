# BEP 48 — Tracker Protocol Extension: Scrape

> Reference: <https://www.bittorrent.org/beps/bep_0048.html>

## Summary

Scrape lets a client query a tracker for torrent statistics (seeders, leechers, completed downloads) without performing a full announce. This is useful for:

1. **Swarm health display**: Show the user how many seeders and leechers exist for a torrent before starting the download, or in a torrent list UI.
2. **Tracker selection**: When multiple trackers are available, scrape can identify which tracker has the healthiest swarm.
3. **Reduced tracker load**: Scraping is lighter than announcing — no peer list is returned, just aggregate stats.

The scrape protocol exists for both HTTP and UDP trackers with different wire formats.

## Protocol Specification

### HTTP Scrape

#### URL Construction

The scrape URL is derived from the announce URL by replacing the last occurrence of `/announce` in the path with `/scrape`:

```
Announce: http://tracker.example.com:6969/announce
Scrape:   http://tracker.example.com:6969/scrape

Announce: http://tracker.example.com/path/to/announce?passkey=abc
Scrape:   http://tracker.example.com/path/to/scrape?passkey=abc

Announce: http://tracker.example.com/x/announce/y
          ↑ NOT scrapeable — "announce" is not the last path component
```

**Rule**: If the announce URL path does not contain `/announce` as a path component (i.e., `strings.Contains(path, "/announce")` is false), the tracker does not support scrape via HTTP.

#### Request

```
GET /scrape?info_hash=<20-byte percent-encoded>&info_hash=<20-byte percent-encoded> HTTP/1.1
```

- The `info_hash` parameter can be repeated to scrape multiple torrents in a single request.
- Each `info_hash` is the 20-byte raw binary hash, percent-encoded just like the announce request.
- If no `info_hash` parameters are provided, some trackers return stats for ALL torrents — but most modern trackers reject this or limit results.

#### Response

The response is a bencoded dictionary:

```
d
  5:filesd
    <20-byte info_hash binary>d
      8:completei150e
      10:downloadedi1000e
      10:incompletei30e
    e
    <20-byte info_hash binary>d
      8:completei5e
      10:downloadedi42e
      10:incompletei10e
    e
  e
e
```

**Structure:**

```
{
  "files": {
    <raw 20-byte info_hash>: {
      "complete": <int>,     // number of seeders (peers with complete file)
      "downloaded": <int>,   // total number of completed downloads (cumulative)
      "incomplete": <int>,   // number of leechers (peers still downloading)
    },
    ...
  }
}
```

The `files` dictionary maps raw 20-byte info hashes (as bencode byte strings) to stat dictionaries. The keys in the stat dictionary:

| Key | Type | Description |
|-----|------|-------------|
| `complete` | int | Number of peers that have the complete file (seeders) |
| `downloaded` | int | Total number of times the torrent has been fully downloaded |
| `incomplete` | int | Number of peers that do not have the complete file (leechers) |

An optional `name` key (string) may be present with a human-readable torrent name, but this is non-standard.

#### Error Response

If the tracker rejects the scrape request, it returns a bencoded dictionary with a `failure reason` key:

```
d14:failure reason22:scrape not supportede
```

### UDP Scrape

#### Action Code

UDP scrape uses **action = 2** (connect is 0, announce is 1, scrape is 2, error is 3).

#### Request Format

The UDP scrape request uses the connection_id obtained from the BEP 15 connect handshake:

```
 0                   1                   2                   3
 0 1 2 3 4 5 6 7 8 9 0 1 2 3 4 5 6 7 8 9 0 1 2 3 4 5 6 7 8 9 0 1
+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
|                       connection_id (8)                       |
|                                                               |
+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
|                       action = 2 (4)                          |
+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
|                       transaction_id (4)                      |
+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
|                                                               |
|                       info_hash (20)                          |
|                                                               |
|                                                               |
|                       ...                                     |
+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
```

**Byte layout:**

| Offset | Size | Field |
|--------|------|-------|
| 0 | 8 | connection_id (from connect handshake) |
| 8 | 4 | action = 2 (scrape) |
| 12 | 4 | transaction_id (random) |
| 16 | 20×N | N info_hashes, concatenated (20 bytes each) |

Multiple info_hashes can be included. The maximum number is limited by the UDP packet size: `(packet_size - 16) / 20`. In practice, trackers typically accept up to ~74 hashes per request (fitting in a standard 1500-byte MTU packet).

#### Response Format

```
 0                   1                   2                   3
 0 1 2 3 4 5 6 7 8 9 0 1 2 3 4 5 6 7 8 9 0 1 2 3 4 5 6 7 8 9 0 1
+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
|                       action = 2 (4)                          |
+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
|                       transaction_id (4)                      |
+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
|                       seeders (4)                             |
+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
|                       completed (4)                           |
+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
|                       leechers (4)                            |
+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
|                       ... (12 bytes per hash)                 |
+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
```

**Byte layout:**

| Offset | Size | Field |
|--------|------|-------|
| 0 | 4 | action = 2 |
| 4 | 4 | transaction_id |
| 8 | 12×N | N stat blocks (one per info_hash in the request) |

Each 12-byte stat block:

| Offset (within block) | Size | Field |
|-----------------------|------|-------|
| 0 | 4 | seeders (complete) — big-endian uint32 |
| 4 | 4 | completed (downloaded) — big-endian uint32 |
| 8 | 4 | leechers (incomplete) — big-endian uint32 |

The stat blocks appear in the same order as the info_hashes in the request. The response does NOT include the info_hashes themselves — the client matches by position.

#### Error Response

If the tracker returns action = 3 (error), the response body after the 8-byte header is a human-readable error string.

### Client Behavior

1. **When to scrape**: Before starting a download (UI display), periodically for torrent list stats, or when the user requests swarm info.
2. **Batching**: When tracking multiple torrents, batch scrape requests (multiple info_hashes in one request) to reduce tracker load.
3. **Rate limiting**: Respect the tracker's announce `interval` for scrape frequency. Do not scrape more often than the announce interval.
4. **Fallback**: If scrape fails (tracker doesn't support it, URL doesn't have `/announce`), degrade gracefully — swarm stats are optional.

## Implementation Plan

### Files to Create / Modify

| File | Action | Purpose |
|------|--------|---------|
| `tracker/scrape.go` | Create | `Scrape` function, `ScrapeResponse` type, HTTP scrape, URL construction |
| `tracker/udp.go` | Modify | Add `udpScrape` function, add `actionScrape` constant |
| `tracker/scrape_test.go` | Create | Tests for both HTTP and UDP scrape |

### Key Types

```go
// tracker/scrape.go

// ScrapeResult holds the stats for a single torrent from a scrape response.
type ScrapeResult struct {
    InfoHash   [20]byte
    Complete   int // seeders
    Downloaded int // total completed downloads
    Incomplete int // leechers
}
```

### Key Functions

```go
// tracker/scrape.go

// Scrape queries the tracker for torrent statistics without announcing.
// Dispatches to HTTP or UDP based on the tracker URL scheme.
// Multiple info_hashes can be scraped in a single request.
func Scrape(trackerURL string, infoHashes [][20]byte) ([]ScrapeResult, error)

// scrapeHTTP performs an HTTP scrape request.
func scrapeHTTP(trackerURL string, infoHashes [][20]byte) ([]ScrapeResult, error)

// scrapeURL derives the scrape URL from an announce URL by replacing the last
// "/announce" with "/scrape". Returns an error if the URL is not scrapeable.
func scrapeURL(announceURL string) (string, error)

// parseScrapeResponse parses a bencoded HTTP scrape response.
func parseScrapeResponse(data []byte, infoHashes [][20]byte) ([]ScrapeResult, error)
```

```go
// tracker/udp.go (additions)

const actionScrape uint32 = 2

// udpScrape performs a UDP scrape: connect → scrape.
func udpScrape(rawURL string, infoHashes [][20]byte) ([]ScrapeResult, error)

// udpScrapeRequest sends a scrape request over an established UDP connection.
func udpScrapeRequest(conn *net.UDPConn, connID uint64, infoHashes [][20]byte) ([]ScrapeResult, error)
```

### Implementation Detail — HTTP Scrape

```go
func scrapeURL(announceURL string) (string, error) {
    u, err := url.Parse(announceURL)
    if err != nil {
        return "", fmt.Errorf("parse URL: %w", err)
    }

    idx := strings.LastIndex(u.Path, "/announce")
    if idx == -1 {
        return "", fmt.Errorf("tracker URL does not contain /announce: %s", announceURL)
    }

    u.Path = u.Path[:idx] + "/scrape" + u.Path[idx+len("/announce"):]
    return u.String(), nil
}

func scrapeHTTP(trackerURL string, infoHashes [][20]byte) ([]ScrapeResult, error) {
    sURL, err := scrapeURL(trackerURL)
    if err != nil {
        return nil, err
    }

    base, err := url.Parse(sURL)
    if err != nil {
        return nil, fmt.Errorf("parse scrape URL: %w", err)
    }

    // Build query with percent-encoded info_hashes
    var rawQuery strings.Builder
    if base.RawQuery != "" {
        rawQuery.WriteString(base.RawQuery)
    }
    for _, ih := range infoHashes {
        if rawQuery.Len() > 0 {
            rawQuery.WriteByte('&')
        }
        rawQuery.WriteString("info_hash=")
        rawQuery.WriteString(percentEncodeBytes(ih[:]))
    }
    base.RawQuery = rawQuery.String()

    client := &http.Client{Timeout: 15 * time.Second}
    resp, err := client.Get(base.String())
    if err != nil {
        return nil, fmt.Errorf("scrape request: %w", err)
    }
    defer resp.Body.Close()

    body, err := io.ReadAll(resp.Body)
    if err != nil {
        return nil, fmt.Errorf("read scrape response: %w", err)
    }

    return parseScrapeResponse(body, infoHashes)
}

func parseScrapeResponse(data []byte, infoHashes [][20]byte) ([]ScrapeResult, error) {
    val, err := bencode.Decode(data)
    if err != nil {
        return nil, fmt.Errorf("decode scrape response: %w", err)
    }

    d, ok := val.(bencode.Dict)
    if !ok {
        return nil, fmt.Errorf("scrape response is not a dict")
    }

    if failReason, ok := d["failure reason"]; ok {
        if s, ok := failReason.(bencode.String); ok {
            return nil, fmt.Errorf("scrape error: %s", string(s))
        }
    }

    filesVal, ok := d["files"]
    if !ok {
        return nil, fmt.Errorf("scrape response missing 'files' key")
    }
    files, ok := filesVal.(bencode.Dict)
    if !ok {
        return nil, fmt.Errorf("scrape 'files' is not a dict")
    }

    var results []ScrapeResult
    for _, ih := range infoHashes {
        key := string(ih[:])
        entry, ok := files[key]
        if !ok {
            results = append(results, ScrapeResult{InfoHash: ih})
            continue
        }

        ed, ok := entry.(bencode.Dict)
        if !ok {
            results = append(results, ScrapeResult{InfoHash: ih})
            continue
        }

        r := ScrapeResult{InfoHash: ih}
        if v, ok := ed["complete"].(bencode.Int); ok {
            r.Complete = int(v)
        }
        if v, ok := ed["downloaded"].(bencode.Int); ok {
            r.Downloaded = int(v)
        }
        if v, ok := ed["incomplete"].(bencode.Int); ok {
            r.Incomplete = int(v)
        }
        results = append(results, r)
    }

    return results, nil
}
```

### Implementation Detail — UDP Scrape

```go
func udpScrape(rawURL string, infoHashes [][20]byte) ([]ScrapeResult, error) {
    host, err := udpHostFromURL(rawURL)
    if err != nil {
        return nil, err
    }

    raddr, err := net.ResolveUDPAddr("udp", host)
    if err != nil {
        return nil, fmt.Errorf("resolve tracker: %w", err)
    }

    conn, err := net.DialUDP("udp", nil, raddr)
    if err != nil {
        return nil, fmt.Errorf("dial tracker: %w", err)
    }
    defer conn.Close()

    connID, err := udpConnect(conn)
    if err != nil {
        return nil, fmt.Errorf("udp connect: %w", err)
    }

    return udpScrapeRequest(conn, connID, infoHashes)
}

func udpScrapeRequest(conn *net.UDPConn, connID uint64, infoHashes [][20]byte) ([]ScrapeResult, error) {
    txnID := randUint32()

    // Request: 16 bytes header + 20 bytes per info_hash
    reqLen := 16 + 20*len(infoHashes)
    req := make([]byte, reqLen)
    binary.BigEndian.PutUint64(req[0:8], connID)
    binary.BigEndian.PutUint32(req[8:12], actionScrape)
    binary.BigEndian.PutUint32(req[12:16], txnID)
    for i, ih := range infoHashes {
        copy(req[16+i*20:16+(i+1)*20], ih[:])
    }

    // Response: 8 bytes header + 12 bytes per info_hash
    minResp := 8 + 12*len(infoHashes)
    resp, err := udpRoundTrip(conn, req, minResp)
    if err != nil {
        return nil, err
    }

    action := binary.BigEndian.Uint32(resp[0:4])
    respTxn := binary.BigEndian.Uint32(resp[4:8])

    if action != actionScrape {
        return nil, fmt.Errorf("expected action=scrape(2), got %d", action)
    }
    if respTxn != txnID {
        return nil, fmt.Errorf("transaction ID mismatch: sent %d, got %d", txnID, respTxn)
    }

    results := make([]ScrapeResult, len(infoHashes))
    for i := range infoHashes {
        off := 8 + i*12
        results[i] = ScrapeResult{
            InfoHash:   infoHashes[i],
            Complete:   int(binary.BigEndian.Uint32(resp[off : off+4])),
            Downloaded: int(binary.BigEndian.Uint32(resp[off+4 : off+8])),
            Incomplete: int(binary.BigEndian.Uint32(resp[off+8 : off+12])),
        }
    }

    return results, nil
}
```

### Package Placement

All scrape logic lives in `tracker/`. The HTTP scrape code goes in a new `tracker/scrape.go`. The UDP scrape function is added to `tracker/udp.go` alongside the existing `udpConnect` and `udpAnnounce`.

## Dependencies

| BEP | Relationship |
|-----|-------------|
| BEP 3 | Defines the HTTP tracker protocol that scrape extends |
| BEP 15 | Defines the UDP tracker protocol — scrape uses the same connect handshake and retry logic |
| BEP 41 | UDP tracker extensions — scrape responses may include BEP 41 options (though uncommon) |

## Testing Strategy

### Unit Tests (`tracker/scrape_test.go`)

1. **`TestScrapeURL_Standard`** — Input: `http://tracker.example.com:6969/announce`. Output: `http://tracker.example.com:6969/scrape`.

2. **`TestScrapeURL_WithPath`** — Input: `http://tracker.example.com/path/announce`. Output: `http://tracker.example.com/path/scrape`.

3. **`TestScrapeURL_WithQuery`** — Input: `http://tracker.example.com/announce?passkey=abc`. Output: `http://tracker.example.com/scrape?passkey=abc`.

4. **`TestScrapeURL_NotScrapeable`** — Input: `http://tracker.example.com/tracker`. No `/announce` in path. Returns error.

5. **`TestScrapeURL_AnnounceInMiddle`** — Input: `http://tracker.example.com/announce/extra`. The *last* `/announce` is at the root. Output: `http://tracker.example.com/scrape/extra`.

6. **`TestScrapeURL_UDP`** — Input: `udp://tracker.example.com:6969/announce`. Verify `scrapeURL` either returns an error (since UDP scrape uses a different mechanism) or handles it. The `Scrape` dispatcher handles UDP separately.

7. **`TestParseScrapeResponse_SingleHash`** — Build a bencoded response with one info_hash entry: `{complete: 10, downloaded: 100, incomplete: 5}`. Verify parsed result matches.

8. **`TestParseScrapeResponse_MultipleHashes`** — Response with 3 info_hashes. Verify all 3 results are returned with correct values, in the same order as the input hashes.

9. **`TestParseScrapeResponse_MissingHash`** — Request has 2 hashes but response `files` dict only contains 1. Verify the missing hash gets zero values (not an error).

10. **`TestParseScrapeResponse_FailureReason`** — Response is `d14:failure reason17:scrape not allowede`. Verify error is returned with the message.

11. **`TestParseScrapeResponse_EmptyFiles`** — Response has `files` key but it's an empty dict. Verify zero-value results for all requested hashes.

### UDP Scrape Tests

12. **`TestUDPScrapeRequest_SingleHash`** — Build a mock UDP response with one 12-byte stat block. Call `udpScrapeRequest` with a mock conn and verify the parsed `ScrapeResult`.

13. **`TestUDPScrapeRequest_MultipleHashes`** — Mock response with 3 stat blocks. Verify all 3 results match.

14. **`TestUDPScrapeRequest_ActionMismatch`** — Response has wrong action code. Verify error.

15. **`TestUDPScrapeRequest_TxnMismatch`** — Response has wrong transaction ID. Verify error.

16. **`TestUDPScrapeRequest_TooShort`** — Response is shorter than expected (fewer stat blocks than hashes). Verify error from `udpRoundTrip` (minResp check).

### Integration Tests

17. **`TestScrapeHTTP_Integration`** — Stand up an `httptest.Server` at `/scrape` that returns a bencoded response. Call `Scrape` with the HTTP URL. Verify end-to-end.

18. **`TestScrapeUDP_Integration`** — Stand up a mock UDP server that handles connect + scrape. Call `Scrape` with a `udp://` URL. Verify end-to-end.

19. **`TestScrapeDispatch`** — Call `Scrape` with an HTTP URL and a UDP URL separately. Verify the correct protocol is used in each case (mock both servers).
