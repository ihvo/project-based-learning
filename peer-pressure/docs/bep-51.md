# BEP 51 — DHT Infohash Indexing

> Lets DHT nodes expose a sample of the infohashes they track, enabling
> DHT-wide indexing, search engines, and network monitoring.

Reference: <https://www.bittorrent.org/beps/bep_0051.html>

---

## 1. Summary

BEP 51 adds a single new KRPC query — `sample_infohashes` — that lets a
querying node ask a DHT node for a random sample of the infohashes it knows
about. This is the mechanism behind DHT search engines and crawlers that
build a global index of active torrents across the network.

Unlike `get_peers` (which asks "who has *this* torrent?"), `sample_infohashes`
asks "what torrents do *you* know about?" — a fundamentally different question
that turns the DHT into a crawlable index.

Key properties:

- **Sampling, not dumping** — nodes return a random subset, not their full
  database, to limit bandwidth and prevent abuse.
- **Crawlable** — by querying many nodes across the keyspace, a crawler can
  build a comprehensive index over time.
- **Rate-limited** — the `interval` response field tells the querier how long
  to wait before re-querying the same node.
- **Opt-in** — nodes that don't want to participate simply don't implement the
  query (return a standard "method unknown" error).

---

## 2. Protocol Specification

### 2.1 `sample_infohashes` query

#### Request

```
{ "t": <txn_id>,
  "y": "q",
  "q": "sample_infohashes",
  "a": { "id": <20-byte querying node ID>,
         "target": <20-byte ID> } }
```

- `target` is a 20-byte identifier that specifies which region of the keyspace
  the querier is interested in. Nodes should return infohashes that are "close"
  to this target (same prefix region), but implementations vary — some return
  a global random sample regardless of `target`.
- The `target` serves the same routing purpose as in `find_node`: the
  responding node returns `nodes` close to the target for further crawling.

#### Response

```
{ "t": <txn_id>,
  "y": "r",
  "r": { "id": <20-byte responding node ID>,
         "samples": <concatenated 20-byte infohashes>,
         "num": <integer, total infohashes stored>,
         "interval": <integer, seconds between re-queries>,
         "nodes": <compact node info for routing> } }
```

| Field | Type | Description |
|-------|------|-------------|
| `samples` | byte string | Concatenation of 20-byte infohashes. Length is a multiple of 20. |
| `num` | integer | Total number of infohashes this node tracks. Informational — helps the crawler estimate network size. |
| `interval` | integer | Minimum seconds the querier should wait before querying this node again. Typically 60–600. |
| `nodes` | byte string | Compact node info (26 bytes/node) for the K closest nodes to `target`. Same format as `find_node` response. |

### 2.2 Sampling behavior

- Nodes should return a **random subset** of their stored infohashes. The BEP
  does not mandate a specific sample size, but common implementations return
  20–200 infohashes per response.
- The randomness prevents a single query from extracting the full database.
- Nodes may filter by keyspace proximity to `target` or return a global sample
  — both approaches are seen in practice.
- If the node stores zero infohashes, `samples` is an empty string and `num`
  is 0.

### 2.3 Crawling strategy

To build a comprehensive index:

```
1. Pick a random target (or systematically sweep the 160-bit keyspace).
2. Do an iterative find_node to get nodes near the target.
3. Send sample_infohashes to each of those nodes.
4. Record the returned infohashes.
5. Follow the returned `nodes` to discover more nodes.
6. Respect `interval` — don't re-query a node until its cooldown expires.
7. Repeat with different targets to cover the full keyspace.
```

A full crawl of the DHT can discover millions of infohashes over several hours.

### 2.4 Infohash source

Nodes accumulate infohashes from:

- `announce_peer` messages received from peers (this is the primary source).
- `get_peers` queries (the queried infohash itself is interesting).
- Results from the node's own `get_peers` lookups.

The stored infohashes represent the node's "knowledge" of active torrents, not
necessarily torrents it has data for.

### 2.5 Rate limiting

- The `interval` field is a polite request, not an enforceable limit.
- Crawlers that ignore it risk being blacklisted by nodes.
- Nodes may also limit the total number of `sample_infohashes` responses per
  minute globally (e.g. 10 responses/minute) to prevent resource exhaustion.

---

## 3. Implementation Plan

### 3.1 Package placement

All BEP 51 code lives in the existing `dht/` package.

### 3.2 New files

| File | Purpose |
|------|---------|
| `dht/index.go` | Infohash index — tracks known infohashes from announce/get_peers; handles `sample_infohashes` |
| `dht/index_test.go` | Unit tests for the infohash index |
| `dht/crawl.go` | Crawler client — iterative `sample_infohashes` across the keyspace |
| `dht/crawl_test.go` | Tests for the crawler |

### 3.3 Modified files

| File | Changes |
|------|---------|
| `dht/krpc.go` | Add `"sample_infohashes"` to message dispatch |
| `dht/node.go` | Record infohashes from `announce_peer` and `get_peers` into the index; handle incoming `sample_infohashes` queries; expose `DHT.SampleInfohashes` client method |

### 3.4 Key types

```go
// dht/index.go

// InfohashIndex tracks known infohashes observed by this DHT node.
type InfohashIndex struct {
    mu         sync.RWMutex
    hashes     map[[20]byte]time.Time // infohash → last seen
    maxEntries int                    // capacity limit
    sampleSize int                    // max infohashes per sample_infohashes response
    interval   int                    // seconds between re-queries (advertised to clients)
}

// SampleResult holds the response data for a sample_infohashes query.
type SampleResult struct {
    Samples  [][20]byte // sampled infohashes
    Num      int        // total infohashes stored
    Interval int        // re-query cooldown in seconds
    Nodes    []Node     // K closest nodes to target
}
```

```go
// dht/crawl.go

// CrawlResult collects infohashes discovered during a crawl.
type CrawlResult struct {
    Infohashes map[[20]byte]struct{} // deduplicated set
    NodesVisited int
    Elapsed    time.Duration
}

// CrawlConfig controls the crawl behavior.
type CrawlConfig struct {
    Targets    int           // number of random targets to sweep (default: 256)
    MaxNodes   int           // max nodes to query total (default: 10000)
    Timeout    time.Duration // per-query timeout (default: 5s)
    Concurrent int           // parallel queries (default: 16)
}
```

### 3.5 Key functions

```go
// dht/index.go

func NewInfohashIndex(maxEntries, sampleSize, interval int) *InfohashIndex

// Record adds or refreshes an infohash in the index.
func (idx *InfohashIndex) Record(infoHash [20]byte)

// Sample returns up to sampleSize random infohashes.
func (idx *InfohashIndex) Sample() [][20]byte

// Num returns the total count of tracked infohashes.
func (idx *InfohashIndex) Num() int

// Prune removes infohashes not seen within the given duration.
func (idx *InfohashIndex) Prune(maxAge time.Duration)

// dht/node.go — new method

// SampleInfohashes queries a remote node for a sample of its known infohashes.
func (d *DHT) SampleInfohashes(addr *net.UDPAddr, target NodeID) (*SampleResult, error)

// dht/crawl.go

// Crawl performs a DHT-wide crawl to discover infohashes.
func (d *DHT) Crawl(ctx context.Context, cfg CrawlConfig) (*CrawlResult, error)
```

### 3.6 Query handler integration

In the `Listen` handler dispatch in `dht/node.go`:

```go
case "sample_infohashes":
    // 1. Extract target from msg.Args
    // 2. Get sample from d.Index.Sample()
    // 3. Find K closest nodes to target from routing table
    // 4. Build response with:
    //    - "samples": concatenate 20-byte infohashes into one byte string
    //    - "num": d.Index.Num()
    //    - "interval": d.Index.interval
    //    - "nodes": EncodeCompactNodes(closest)
```

### 3.7 Infohash recording integration

Modify existing handlers in `dht/node.go`:

```go
case "announce_peer":
    // existing: store peer in peer list
    // NEW: d.Index.Record(infoHash)

case "get_peers":
    // existing: return peers or closest nodes
    // NEW: d.Index.Record(infoHash)
```

### 3.8 CLI integration

```
peer-pressure crawl [--targets N] [--max-nodes N] [--timeout 5s]
  - Boots DHT, runs Crawl(), prints discovered infohashes
  - Useful for monitoring/debugging
```

---

## 4. Dependencies

| Dependency | Type | Notes |
|------------|------|-------|
| BEP 5 (`dht/`) | Required | KRPC transport, routing table, iterative lookups |
| `dht.Transport.Send` | Internal | For sending `sample_infohashes` queries |
| `dht.EncodeCompactNodes` | Internal | Encoding `nodes` field in responses |
| `dht.RoutingTable.Closest` | Internal | Finding K closest nodes for `nodes` field |
| `math/rand/v2` | Go stdlib | Random sampling from the infohash set |

---

## 5. Testing Strategy

### 5.1 Unit tests — index (`dht/index_test.go`)

| Test | Description |
|------|-------------|
| `TestIndexRecord` | Record 10 infohashes, verify `Num()` returns 10 |
| `TestIndexRecordDuplicate` | Record same infohash twice, verify `Num()` still returns 1 |
| `TestIndexSample` | Record 100 infohashes, call `Sample()`, verify result length ≤ `sampleSize` and all returned hashes are in the index |
| `TestIndexSampleSmall` | Record 3 infohashes with `sampleSize=20`, verify `Sample()` returns all 3 |
| `TestIndexSampleEmpty` | Empty index, verify `Sample()` returns empty slice |
| `TestIndexSampleRandomness` | Record 50 infohashes, call `Sample()` 100 times, verify not all results are identical (randomness check) |
| `TestIndexCapacity` | Set `maxEntries=10`, record 15 infohashes, verify `Num() <= 10` |
| `TestIndexPrune` | Record infohashes at t=0, advance clock 2h, call `Prune(1h)`, verify all are removed |
| `TestIndexPruneMixed` | Record some at t=0, some at t=30m, prune at t=45m with maxAge=40m, verify only the old ones are removed |
| `TestIndexConcurrency` | Spawn 100 goroutines each calling Record and Sample concurrently, verify no panics or races (`go test -race`) |

### 5.2 KRPC encoding tests

| Test | Description |
|------|-------------|
| `TestEncodeSampleInfohashesQuery` | Build a `sample_infohashes` query Message, encode, decode, verify `target` field |
| `TestEncodeSampleInfohashesResponse` | Build response with `samples` (concatenated hashes), `num`, `interval`, `nodes`, verify round-trip |
| `TestDecodeSamplesField` | Parse a `samples` byte string of 60 bytes into 3 infohashes |
| `TestDecodeSamplesEmpty` | Parse empty `samples` field, verify zero infohashes |
| `TestDecodeSamplesInvalidLength` | `samples` is 25 bytes (not multiple of 20), verify graceful handling |

### 5.3 Integration tests

| Test | Description |
|------|-------------|
| `TestSampleInfohashesRoundTrip` | Start 2 DHT nodes in-process. Node A records 50 infohashes via `announce_peer` simulation. Node B sends `sample_infohashes` to A. Verify response contains valid infohashes from A's set. |
| `TestSampleInfohashesInterval` | Query a node, verify `interval` field is present and positive |
| `TestSampleInfohashesNodes` | Verify the response includes `nodes` for further crawling |
| `TestSampleInfohashesUnknownTarget` | Query with a target far from any stored hashes, verify the node still returns a sample (not filtered by proximity) or returns `nodes` pointing closer |

### 5.4 Crawler tests (`dht/crawl_test.go`)

| Test | Description |
|------|-------------|
| `TestCrawlSmallNetwork` | Start 5 DHT nodes in-process. Seed each with 10 unique infohashes. Run `Crawl` from one node. Verify the result contains a substantial fraction of all 50 infohashes. |
| `TestCrawlRespectsMaxNodes` | Set `MaxNodes=3`, verify the crawler queries at most 3 nodes |
| `TestCrawlContextCancel` | Start a crawl, cancel the context after 100ms, verify it stops cleanly |
| `TestCrawlDeduplicates` | Seed two nodes with overlapping infohashes, verify `CrawlResult.Infohashes` contains no duplicates |
