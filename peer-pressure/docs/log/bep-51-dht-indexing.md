# BEP 51: DHT Infohash Indexing

## What It Does

BEP 51 adds a `sample_infohashes` query to the DHT that returns a random
subset of infohashes a node currently stores. This enables efficient DHT
indexing without resorting to Sybil attacks or routing table pollution.

### Why This Matters

Before BEP 51, the only way to index the DHT was to passively observe
`get_peers` queries — which favors nodes with many IP addresses and
incentivizes bad behavior. With `sample_infohashes`, a single well-behaved
node can survey the entire DHT within a few hours.

### Message Format

```
Request:  { q: "sample_infohashes", a: { id, target } }
Response: { r: { id, samples, num, interval, nodes } }
```

- **`samples`**: concatenated 20-byte infohashes (N × 20 bytes)
- **`num`**: total infohashes in storage (may exceed samples count)
- **`interval`**: seconds to wait before requesting a new sample
- **`target`/`nodes`**: standard iterative lookup fields for keyspace traversal

### Keyspace Traversal Strategy

An indexer walks the 160-bit keyspace by adjusting `target` for each query.
Each response returns nodes close to the target AND a sample of stored
infohashes. One RPC per node is sufficient — the indexer moves through the
keyspace without revisiting nodes.

### What We Implemented

1. **`SampleInfohashes()`** — sends the query and parses the full response
   (samples, num, interval, nodes, nodes6)
2. **`EncodeSamples`/`DecodeSamples`** — serialize/deserialize the
   concatenated 20-byte hash format
3. Tests for round-trip encoding, empty/short/partial buffers

## Go Idioms

### Parsing Concatenated Fixed-Size Records

```go
func DecodeSamples(data []byte) [][20]byte {
    var hashes [][20]byte
    for len(data) >= 20 {
        var h [20]byte
        copy(h[:], data[:20])
        hashes = append(hashes, h)
        data = data[20:]
    }
    return hashes
}
```

This pattern appears throughout the codebase: compact nodes (26 bytes),
compact peers (6 bytes), IPv6 nodes (38 bytes). The idiom is always the same:
- Loop while enough bytes remain
- `copy` into a fixed-size array (not slice — avoids aliasing the input)
- Advance the slice past consumed bytes
- Silently ignore trailing bytes that don't form a complete record

### Pre-Allocated Capacity

```go
buf := make([]byte, 0, len(hashes)*20)
```

For `EncodeSamples`, the output size is known: exactly `N * 20` bytes. Using
`make([]byte, 0, capacity)` starts with zero length but pre-allocates the
backing array, so subsequent `append()` calls never need to grow. This is a
micro-optimization but idiomatic when the final size is predictable.
