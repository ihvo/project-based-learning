# BEP 32 — DHT Extensions for IPv6

> **Specification:** <https://www.bittorrent.org/beps/bep_0032.html>
> **Status:** Not started
> **Phase:** 8 — DHT Enhancements

---

## 1. Summary

BEP 32 extends the BEP 5 DHT to support IPv6 networks. The core Kademlia
algorithm remains unchanged — same 160-bit ID space, same XOR distance metric,
same k-bucket structure. What changes is the transport and encoding layer:

- **Separate routing tables** for IPv4 and IPv6 address families
- **New compact format** for IPv6 nodes (`nodes6`, 38 bytes each)
- **Dual-stack awareness** so a single DHT implementation can participate in
  both IPv4 and IPv6 networks simultaneously
- **Mixed peer responses** where `values` can contain both 6-byte IPv4 and
  18-byte IPv6 compact peer entries

This matters because IPv6 adoption continues to grow, and many peers are only
reachable via IPv6. Without BEP 32, our DHT is blind to the IPv6 half of the
swarm.

---

## 2. Protocol Specification

### 2.1 Compact Formats

**IPv4 node (existing, BEP 5):** 26 bytes

```
┌──────────────────────┬─────────────┬────────┐
│      Node ID         │   IPv4 Addr │  Port  │
│     (20 bytes)       │  (4 bytes)  │(2 bytes)│
└──────────────────────┴─────────────┴────────┘
Total: 26 bytes
```

**IPv6 node (new, BEP 32):** 38 bytes

```
┌──────────────────────┬──────────────────────┬────────┐
│      Node ID         │      IPv6 Addr       │  Port  │
│     (20 bytes)       │     (16 bytes)       │(2 bytes)│
└──────────────────────┴──────────────────────┴────────┘
Total: 38 bytes
```

**IPv4 compact peer (existing, BEP 23):** 6 bytes

```
┌─────────────┬────────┐
│  IPv4 Addr  │  Port  │
│  (4 bytes)  │(2 bytes)│
└─────────────┴────────┘
```

**IPv6 compact peer (new, BEP 32):** 18 bytes

```
┌──────────────────────┬────────┐
│      IPv6 Addr       │  Port  │
│     (16 bytes)       │(2 bytes)│
└──────────────────────┴────────┘
```

### 2.2 KRPC Message Changes

#### `find_node` Response

The response can now include both `nodes` and `nodes6` keys:

```
{
  "t": "<txn>",
  "y": "r",
  "r": {
    "id": "<20-byte queried node ID>",
    "nodes":  "<compact IPv4 node info>",   // 26 bytes each
    "nodes6": "<compact IPv6 node info>"    // 38 bytes each
  }
}
```

Both keys are optional. A node should include whichever address families it
has routing information for. An IPv4-only node will only include `nodes`. An
IPv6-only node will only include `nodes6`. A dual-stack node should include
both.

#### `get_peers` Response

Two cases: peers found (values) or no peers (nodes).

**Peers found:**

```
{
  "t": "<txn>",
  "y": "r",
  "r": {
    "id": "<20-byte node ID>",
    "token": "<opaque token>",
    "values": [
      "<6 or 18 byte compact peer>",
      "<6 or 18 byte compact peer>",
      ...
    ]
  }
}
```

Each entry in `values` is either 6 bytes (IPv4) or 18 bytes (IPv6). The
receiver determines the address family by the length of each entry.

**No peers (routing closer):**

Same as `find_node` response — may include both `nodes` and `nodes6`.

#### `announce_peer` Query

Unchanged. The queried node knows the announcer's address from the UDP source
address of the packet.

### 2.3 Dual-Stack Architecture

A dual-stack node maintains:

```
┌─────────────────────────────────────────────────┐
│                   DHT Node                       │
│                                                  │
│  ┌────────────────────┐  ┌────────────────────┐  │
│  │ IPv4 RoutingTable  │  │ IPv6 RoutingTable  │  │
│  │ 160 k-buckets      │  │ 160 k-buckets      │  │
│  │ Nodes with IPv4    │  │ Nodes with IPv6    │  │
│  └────────┬───────────┘  └────────┬───────────┘  │
│           │                       │              │
│  ┌────────▼───────────┐  ┌────────▼───────────┐  │
│  │  UDP Socket :4444  │  │  UDP Socket :4444  │  │
│  │  (IPv4)            │  │  (IPv6)            │  │
│  └────────────────────┘  └────────────────────┘  │
│                                                  │
│           Shared Node ID (20 bytes)              │
└─────────────────────────────────────────────────┘
```

Key rules:
- **Same Node ID** is used on both address families
- **Separate routing tables** — an IPv4 node is never placed in the IPv6
  routing table, and vice versa
- **Separate UDP sockets** — one bound to `0.0.0.0:port` and one to `[::]:port`
  (or a single dual-stack socket on systems that support it)
- **Queries on one family get responses on the same family** — a query arriving
  over IPv4 gets a response over IPv4
- **Cross-family hints** — a response to an IPv4 query may include `nodes6` to
  help the querier populate its IPv6 routing table, and vice versa

### 2.4 Bootstrap

IPv6 bootstrap nodes:

| Hostname                     | Port | Notes                           |
|------------------------------|------|---------------------------------|
| `router.bittorrent.com`     | 6881 | May resolve to both A and AAAA  |
| `dht.transmissionbt.com`    | 6881 | May resolve to both A and AAAA  |
| `router.utorrent.com`       | 6881 | May resolve to both A and AAAA  |

Resolution: resolve each bootstrap hostname and attempt connections to both A
(IPv4) and AAAA (IPv6) records.

### 2.5 Iterative Lookup Changes

During an iterative lookup (`find_node` or `get_peers`):

1. Query the α closest nodes (from both routing tables)
2. For each response, parse both `nodes` (IPv4) and `nodes6` (IPv6)
3. Insert IPv4 nodes into the IPv4 routing table
4. Insert IPv6 nodes into the IPv6 routing table
5. Merge all returned nodes into the candidate set, sorted by XOR distance
6. Continue until the k closest nodes have been queried

The candidate set and "closest seen" tracking use Node IDs regardless of
address family — the XOR distance metric operates on IDs, not addresses.

---

## 3. Implementation Plan

### 3.1 `dht/table.go` — No Structural Changes

The `RoutingTable` struct is already address-family-agnostic: it stores `Node`
values containing a `net.UDPAddr`, which works for both IPv4 and IPv6. We just
need two instances.

### 3.2 `dht/node.go` — Dual-Stack DHT

Introduce a wrapper that manages two transport/table pairs:

```go
// DualDHT manages IPv4 and IPv6 DHT instances sharing a single Node ID.
type DualDHT struct {
    ID    NodeID
    V4    *DHT   // nil if IPv4 is not available
    V6    *DHT   // nil if IPv6 is not available
}

// NewDualDHT creates a dual-stack DHT. Pass nil for either conn to disable
// that address family.
func NewDualDHT(v4Conn, v6Conn *net.UDPConn) *DualDHT {
    id := RandomNodeID()
    dd := &DualDHT{ID: id}
    if v4Conn != nil {
        dd.V4 = newWithID(id, v4Conn)
    }
    if v6Conn != nil {
        dd.V6 = newWithID(id, v6Conn)
    }
    return dd
}
```

Add a `newWithID` constructor to `DHT` that accepts an explicit NodeID instead
of generating a random one:

```go
func newWithID(id NodeID, conn *net.UDPConn) *DHT {
    return &DHT{
        ID:        id,
        Table:     NewRoutingTable(id),
        Transport: NewTransport(conn),
        tokens:    make(map[NodeID]string),
    }
}
```

### 3.3 `dht/node.go` — IPv6 Compact Encoding

Add encoding/decoding functions for the IPv6 compact format:

```go
// EncodeCompactNodes6 encodes nodes into BEP 32 compact IPv6 format.
// Each entry: 20-byte ID + 16-byte IPv6 address + 2-byte port = 38 bytes.
func EncodeCompactNodes6(nodes []Node) []byte {
    buf := make([]byte, 38*len(nodes))
    for i, n := range nodes {
        off := i * 38
        copy(buf[off:], n.ID[:])
        ip6 := n.Addr.IP.To16()
        if ip6 != nil {
            copy(buf[off+20:], ip6)
        }
        binary.BigEndian.PutUint16(buf[off+36:], uint16(n.Addr.Port))
    }
    return buf
}

// DecodeCompactNodes6 parses the BEP 32 compact IPv6 node format.
func DecodeCompactNodes6(data []byte) []Node {
    var nodes []Node
    for len(data) >= 38 {
        var id NodeID
        copy(id[:], data[:20])
        ip := make(net.IP, 16)
        copy(ip, data[20:36])
        port := binary.BigEndian.Uint16(data[36:38])
        nodes = append(nodes, Node{
            ID:   id,
            Addr: net.UDPAddr{IP: ip, Port: int(port)},
        })
        data = data[38:]
    }
    return nodes
}

// EncodeCompactPeers6 encodes IPv6 peer addresses into compact format.
// Each entry: 16-byte IPv6 address + 2-byte port = 18 bytes.
func EncodeCompactPeers6(addrs []string) []byte {
    var buf []byte
    for _, addr := range addrs {
        host, portStr, err := net.SplitHostPort(addr)
        if err != nil {
            continue
        }
        ip := net.ParseIP(host).To16()
        if ip == nil || ip.To4() != nil {
            continue // skip IPv4 addresses
        }
        var portBuf [2]byte
        port := 0
        fmt.Sscanf(portStr, "%d", &port)
        binary.BigEndian.PutUint16(portBuf[:], uint16(port))
        buf = append(buf, ip...)
        buf = append(buf, portBuf[:]...)
    }
    return buf
}

// DecodeCompactPeers6 parses compact IPv6 peer entries (18 bytes each).
func DecodeCompactPeers6(data []byte) []string {
    var addrs []string
    for len(data) >= 18 {
        ip := make(net.IP, 16)
        copy(ip, data[:16])
        port := binary.BigEndian.Uint16(data[16:18])
        addrs = append(addrs, fmt.Sprintf("[%s]:%d", ip, port))
        data = data[18:]
    }
    return addrs
}
```

### 3.4 `dht/krpc.go` — Parse `nodes6` from Responses

Update `DecodeMessage` to also extract `nodes6` from response dictionaries.
The `Message` struct gets a new field:

```go
type Message struct {
    TxnID  string
    Type   string
    Method string
    Args   bencode.Dict
    Reply  bencode.Dict
    Error  []any
}
```

The `Reply` dict is already generic, so `nodes6` comes through naturally.
However, the higher-level functions (`sendFindNode`, `sendGetPeers`) need to
check for it:

```go
// In sendFindNode:
if nodes6Str, ok := resp.Reply["nodes6"]; ok {
    if s, ok := nodes6Str.(bencode.String); ok {
        v6Nodes := DecodeCompactNodes6([]byte(s))
        // Insert into IPv6 routing table
    }
}
```

### 3.5 `dht/node.go` — Mixed Peer Values

In `sendGetPeers`, the `values` list can now contain entries of varying length.
Determine the address family by entry length:

```go
for _, item := range values {
    s := []byte(item.(bencode.String))
    switch len(s) {
    case 6:
        peers = append(peers, DecodeCompactPeers(s)...)
    case 18:
        peers = append(peers, DecodeCompactPeers6(s)...)
    }
}
```

### 3.6 `dht/node.go` — Bootstrap Dual-Stack

Update `Bootstrap` to resolve hostnames to both IPv4 and IPv6 addresses:

```go
func (dd *DualDHT) Bootstrap(addrs []string) error {
    for _, hostport := range addrs {
        host, port, _ := net.SplitHostPort(hostport)
        ips, err := net.LookupIP(host)
        if err != nil {
            continue
        }
        for _, ip := range ips {
            addr := &net.UDPAddr{IP: ip, Port: portInt(port)}
            if ip.To4() != nil && dd.V4 != nil {
                dd.V4.Ping(addr)
            } else if ip.To4() == nil && dd.V6 != nil {
                dd.V6.Ping(addr)
            }
        }
    }
    return nil
}
```

### 3.7 `cmd/peer-pressure/main.go` — Create Dual-Stack Sockets

Update `discoverDHTPeers` to optionally open both an IPv4 and IPv6 UDP socket:

```go
v4Conn, _ := net.ListenPacket("udp4", ":0")
v6Conn, _ := net.ListenPacket("udp6", ":0")
dd := dht.NewDualDHT(v4Conn.(*net.UDPConn), v6Conn.(*net.UDPConn))
```

### 3.8 File Summary

| File                         | Change       | Description                                 |
|------------------------------|--------------|---------------------------------------------|
| `dht/node.go`               | Modify       | Add `DualDHT`, `newWithID`, IPv6 encode/decode functions, dual-stack bootstrap, mixed-length peer parsing |
| `dht/krpc.go`               | Modify       | Parse `nodes6` key in responses             |
| `dht/table.go`              | No change    | Already address-family-agnostic             |
| `cmd/peer-pressure/main.go` | Modify       | Open dual-stack sockets, use `DualDHT`      |

---

## 4. Dependencies

| BEP | Relationship | Notes |
|-----|-------------|-------|
| 5   | Extends     | BEP 32 extends the BEP 5 DHT with IPv6 support |
| 23  | Extends     | Compact peer format extended from 6-byte IPv4 to 18-byte IPv6 |
| 42  | Interacts   | DHT security extension applies to both IPv4 and IPv6 (different masks per family) |
| 43  | Interacts   | Read-only mode applies to both IPv4 and IPv6 DHT instances |

---

## 5. Testing Strategy

### 5.1 Compact Encoding Round-Trips

| Test Case | Description |
|-----------|-------------|
| `TestEncodeDecodeCompactNodes6` | Encode a list of IPv6 nodes, decode them back, verify IDs, IPs, and ports match. |
| `TestDecodeCompactNodes6Short` | Decode a buffer shorter than 38 bytes — should return empty. |
| `TestDecodeCompactNodes6Remainder` | Decode a buffer with 38+10 bytes — should parse one node and discard the trailing 10. |
| `TestEncodeDecodeCompactPeers6` | Round-trip IPv6 peer addresses through encode/decode. Verify `[ip]:port` format. |
| `TestEncodeCompactPeers6SkipsIPv4` | Pass an IPv4 address to `EncodeCompactPeers6` — it should be skipped. |

### 5.2 Mixed-Length Peer Values

| Test Case | Description |
|-----------|-------------|
| `TestGetPeersMixedValues` | Simulate a `get_peers` response with `values` containing both 6-byte and 18-byte entries. Verify both IPv4 and IPv6 peers are parsed. |
| `TestGetPeersIPv6Only` | Simulate a response with only 18-byte values. Verify all peers are IPv6. |

### 5.3 Response Parsing

| Test Case | Description |
|-----------|-------------|
| `TestFindNodeResponseNodes6` | Build a KRPC response with both `nodes` and `nodes6`. Verify both sets are decoded correctly. |
| `TestFindNodeResponseNodes6Only` | Build a response with only `nodes6` (no `nodes` key). Verify IPv6 nodes are returned. |

### 5.4 Routing Table Separation

| Test Case | Description |
|-----------|-------------|
| `TestDualDHTSeparateTables` | Create a `DualDHT`. Insert an IPv4 node and an IPv6 node. Verify each appears only in the correct routing table. |
| `TestDualDHTSharedID` | Verify `DualDHT.V4.ID == DualDHT.V6.ID`. |

### 5.5 Bootstrap Resolution

| Test Case | Description |
|-----------|-------------|
| `TestBootstrapDualStack` | Mock DNS resolution returning both A and AAAA records. Verify pings are sent on the correct sockets. |

### 5.6 Integration

| Test Case | Description |
|-----------|-------------|
| `TestIPv6NodeExchange` | Start two in-process DHT nodes using IPv6 loopback (`[::1]`). Verify they can find each other via `find_node`. |
| `TestIPv6GetPeers` | One node announces a torrent over IPv6, another looks it up. Verify the peer address is an IPv6 address. |
