# BEP 32 — DHT IPv6

## What We Built

Added IPv6 support to the DHT: parsing `nodes6` (compact IPv6 nodes) and
`values6` (compact IPv6 peers) in `find_node` and `get_peers` responses.

**Files modified:**
- `dht/node.go` — Added `EncodeCompactNodes6`, `DecodeCompactNodes6`, `DecodeCompactPeers6`. Updated `sendFindNode` and `sendGetPeers` to merge IPv6 results.
- `dht/node_test.go` — 7 new tests

## BitTorrent Concepts

### DHT's IPv4 Assumption

The original BEP 5 DHT uses fixed-size compact formats:
- **Compact node**: 26 bytes = 20 (node ID) + 4 (IPv4) + 2 (port)
- **Compact peer**: 6 bytes = 4 (IPv4) + 2 (port)

These can't represent IPv6 addresses (16 bytes instead of 4).

### BEP 32: Parallel IPv6 Keys

BEP 32 adds new keys alongside existing ones:

| IPv4 key | IPv6 key | Format |
|----------|----------|--------|
| `nodes` (26B/node) | `nodes6` (38B/node) | 20B ID + 16B IPv6 + 2B port |
| `values` (6B/peer) | `values6` (18B/peer) | 16B IPv6 + 2B port |

A response can contain both:

```
{
  "r": {
    "id": "<20 bytes>",
    "nodes": "<compact IPv4 nodes>",
    "nodes6": "<compact IPv6 nodes>",
    "values": ["<6B peer>", ...],
    "values6": ["<18B peer>", ...],
    "token": "abc"
  }
}
```

### Same Routing Table, Mixed Addresses

BEP 32 nodes live in the same routing table as IPv4 nodes. The routing table
is keyed by 160-bit node IDs (which are address-independent). A node at
`2001:db8::1:6881` and a node at `192.168.1.1:6881` both go into buckets
determined by their XOR distance to us.

This means our existing `RoutingTable` works unchanged. The `Node` struct
already holds `net.UDPAddr`, which handles both IPv4 and IPv6.

## Go Idioms Used

### `net.IP` Dual-Stack Flexibility

Go's `net.IP` is a byte slice that can hold either IPv4 (4 bytes) or IPv6
(16 bytes). We store both in the same `Node` struct:

```go
type Node struct {
    ID   NodeID
    Addr net.UDPAddr  // Addr.IP can be v4 or v6
}
```

When encoding, we use `.To4()` or `.To16()` to get the right representation.
When decoding, we allocate the correct size:

```go
// IPv4: make 4-byte IP
ip := net.IPv4(data[20], data[21], data[22], data[23])

// IPv6: make 16-byte IP, copy
ip := make(net.IP, net.IPv6len)
copy(ip, data[20:36])
```

### Additive Merging (append, not replace)

When parsing responses, we merge IPv6 results into the same slices:

```go
// Before: only IPv4
r.nodes = DecodeCompactNodes([]byte(nodesStr))

// After: IPv4 + IPv6
if nodesStr, ok := resp.Reply["nodes"].(bencode.String); ok {
    r.nodes = append(r.nodes, DecodeCompactNodes([]byte(nodesStr))...)
}
if nodes6Str, ok := resp.Reply["nodes6"].(bencode.String); ok {
    r.nodes = append(r.nodes, DecodeCompactNodes6([]byte(nodes6Str))...)
}
```

The rest of the code (routing table insertion, distance sorting, iterative
lookup) works unchanged because it operates on `Node` values, not IP addresses.

### Graceful Degradation for Short Buffers

```go
func DecodeCompactNodes6(data []byte) []Node {
    var nodes []Node
    for len(data) >= 38 {
        // ... decode one node ...
        data = data[38:]
    }
    return nodes
}
```

If the buffer has trailing bytes that don't form a complete entry, they're
silently ignored. This matches the IPv4 decoder's behavior and is appropriate
for a protocol where implementations vary.

### `net.JoinHostPort` for IPv6 Peers

```go
addrs = append(addrs, net.JoinHostPort(ip.String(), fmt.Sprintf("%d", port)))
```

This automatically adds brackets for IPv6 addresses:
- `net.JoinHostPort("2001:db8::1", "6881")` → `"[2001:db8::1]:6881"`
- `net.JoinHostPort("192.168.1.1", "6881")` → `"192.168.1.1:6881"`

## Test Coverage

| Test | What It Verifies |
|------|-----------------|
| `TestEncodeDecodeCompactNodes6` | Round-trip 2 IPv6 nodes (38B each) |
| `TestDecodeCompactNodes6Empty` | Empty data → empty slice |
| `TestDecodeCompactNodes6Short` | 37 bytes → no nodes (graceful) |
| `TestDecodeCompactPeers6` | Two IPv6 peers with bracket formatting |
| `TestDecodeCompactPeers6Empty` | Empty data → empty slice |
| `TestDecodeCompactPeers6Short` | 17 bytes → no peers |
