# BEP 43 — Read-only DHT Nodes

> **Specification:** <https://www.bittorrent.org/beps/bep_0043.html>
> **Status:** Not started
> **Phase:** 8 — DHT Enhancements

---

## 1. Summary

BEP 43 introduces a "read-only" mode for DHT nodes that can send queries and
receive responses but should not be stored in other nodes' routing tables.

This solves a real problem: nodes behind symmetric NATs or restrictive
firewalls can reach out to other nodes (their outgoing UDP works) but cannot
receive unsolicited incoming queries (the NAT drops them). If these nodes get
added to routing tables, other nodes waste time querying addresses that will
never respond — degrading lookup performance for everyone.

With BEP 43:
- A read-only node sets `ro=1` in every outgoing KRPC query
- Nodes receiving a `ro=1` query must **not** add the sender to their routing
  table
- Nodes must **not** include read-only nodes in `nodes` or `nodes6` responses
- Read-only nodes can still perform lookups, find peers, and announce — they
  just don't serve as routing infrastructure for others

This is especially important for our client because many users will be behind
NATs. Rather than silently degrading the DHT, we participate honestly.

---

## 2. Protocol Specification

### 2.1 The `ro` Flag

The flag is included in the top-level dictionary of every outgoing KRPC
**query**:

```
{
  "t": "<txn_id>",
  "y": "q",
  "q": "find_node",
  "ro": 1,              ← BEP 43 read-only flag
  "a": {
    "id": "<20-byte node ID>",
    "target": "<20-byte target>"
  }
}
```

| Key  | Type        | Value | Meaning                |
|------|-------------|-------|------------------------|
| `ro` | bencode int | `1`   | Node is read-only      |
| (absent) | —       | —     | Node is regular (default) |

The `ro` flag is:
- Only present in **queries** (not responses or errors)
- Placed at the **top level** of the message dictionary (alongside `t`, `y`, `q`, `a`)
- Only the integer `1` is meaningful; any other value or absence means
  regular mode

### 2.2 Behavioral Rules

**When we are a read-only node** (sending queries):

| Action | Allowed? |
|--------|----------|
| Send `ping` queries | ✅ Yes (with `ro=1`) |
| Send `find_node` queries | ✅ Yes (with `ro=1`) |
| Send `get_peers` queries | ✅ Yes (with `ro=1`) |
| Send `announce_peer` queries | ✅ Yes (with `ro=1`) |
| Receive responses | ✅ Yes |
| Be added to other nodes' routing tables | ❌ No (they should ignore us) |
| Appear in `nodes`/`nodes6` responses | ❌ No |

**When we receive a query with `ro=1`** (from a read-only node):

| Action | Required? |
|--------|-----------|
| Respond to the query | ✅ Yes (treat normally) |
| Add sender to our routing table | ❌ Must NOT |
| Include sender in `nodes`/`nodes6` responses | ❌ Must NOT |

### 2.3 Message Flow: Regular vs Read-only

**Regular node querying:**

```
Node A (regular)                    Node B
    │                                  │
    │──── find_node (no ro) ──────────►│
    │                                  │ B adds A to routing table ✓
    │◄──── response ──────────────────│
    │                                  │
```

**Read-only node querying:**

```
Node A (read-only)                  Node B
    │                                  │
    │──── find_node (ro=1) ───────────►│
    │                                  │ B does NOT add A to table ✗
    │◄──── response ──────────────────│
    │                                  │
```

### 2.4 NAT Detection

To decide whether to enable read-only mode, the node should detect whether it
can receive unsolicited incoming UDP. A practical heuristic:

1. **Bind a UDP socket** on the announced port
2. **Ask other nodes** to ping us (or rely on organic incoming queries during
   operation)
3. **Track incoming queries** over a window (e.g., 5 minutes after bootstrap)
4. If no incoming queries are received after we've been in the network for a
   reasonable time → we're likely behind a restrictive NAT → enable read-only

A simpler approach for initial implementation: provide a `--dht-readonly`
CLI flag and default to read-only mode. Auto-detection can be added later.

### 2.5 Interaction with Routing Table

Read-only nodes are ephemeral from the network's perspective. They:
- Maintain their own routing table (they need it for lookups)
- Populate their table from responses they receive
- Do **not** expect to appear in anyone else's table
- Do **not** serve queries from other nodes (since no one routes to them)

This is fine — a read-only node can still do everything a leeching client
needs: find peers and announce.

---

## 3. Implementation Plan

### 3.1 `dht/node.go` — Add Read-only Mode

Add a `ReadOnly` field to the `DHT` struct:

```go
type DHT struct {
    ID         NodeID
    Table      *RoutingTable
    Transport  *Transport
    tokens     map[NodeID]string
    tokensMu   sync.Mutex
    ReadOnly   bool   // BEP 43: if true, set ro=1 in all outgoing queries
}
```

### 3.2 `dht/krpc.go` — Encode `ro` Flag

Add a `ReadOnly` field to the `Message` struct:

```go
type Message struct {
    TxnID    string
    Type     string
    Method   string
    Args     bencode.Dict
    Reply    bencode.Dict
    Error    []any
    IP       *net.UDPAddr   // BEP 42
    ReadOnly bool           // BEP 43: ro=1 in outgoing queries
}
```

**EncodeMessage** — include `ro` when set:

```go
func EncodeMessage(msg Message) []byte {
    d := bencode.Dict{
        "t": bencode.String(msg.TxnID),
        "y": bencode.String(msg.Type),
    }
    if msg.ReadOnly && msg.Type == "q" {
        d["ro"] = bencode.Int(1)
    }
    // ... rest of encoding
}
```

**DecodeMessage** — parse `ro`:

```go
// In DecodeMessage, after parsing "y":
if roVal, ok := d["ro"]; ok {
    if roInt, ok := roVal.(bencode.Int); ok && roInt == 1 {
        msg.ReadOnly = true
    }
}
```

### 3.3 `dht/node.go` — Set `ro` on Outgoing Queries

Every method that constructs a query message must set `ReadOnly` if the DHT is
in read-only mode. Centralize this in a helper:

```go
// newQuery builds a KRPC query message, setting ro=1 if we're read-only.
func (d *DHT) newQuery(method string, args bencode.Dict) Message {
    return Message{
        Type:     "q",
        Method:   method,
        Args:     args,
        ReadOnly: d.ReadOnly,
    }
}
```

Update `Ping`, `sendFindNode`, `sendGetPeers`, and `AnnouncePeer` to use
`newQuery` instead of building `Message` literals directly.

### 3.4 `dht/node.go` — Handle Incoming `ro=1` Queries

In the Transport's query handler (the function passed to `Transport.Listen`),
check the `ro` flag before adding the querying node to our routing table:

```go
func (d *DHT) handleQuery(msg Message, addr *net.UDPAddr) {
    // Only add to routing table if NOT read-only.
    if !msg.ReadOnly {
        node := Node{ID: senderID, Addr: *addr}
        d.Table.Insert(node)
    }

    // Process and respond to the query normally regardless of ro.
    switch msg.Method {
    case "ping":
        d.handlePing(msg, addr)
    case "find_node":
        d.handleFindNode(msg, addr)
    case "get_peers":
        d.handleGetPeers(msg, addr)
    case "announce_peer":
        d.handleAnnouncePeer(msg, addr)
    }
}
```

### 3.5 `dht/node.go` — Exclude Read-only Nodes from Responses

When building `nodes`/`nodes6` responses, don't include nodes that are known to
be read-only. This requires tracking which nodes in our routing table are
read-only.

Since read-only nodes should not be in our table at all (we skip insertion for
`ro=1` queries), this is handled naturally — read-only nodes won't appear in
`Closest()` results.

### 3.6 `cmd/peer-pressure/main.go` — CLI Flag

Add a `--dht-readonly` flag to `runDownload` and `runPeers`:

```go
dhtReadOnly := fs.Bool("dht-readonly", false, "run DHT in read-only mode (BEP 43)")
```

When creating the DHT node:

```go
node := dht.New(conn)
node.ReadOnly = *dhtReadOnly
```

### 3.7 File Summary

| File                         | Change       | Description                                    |
|------------------------------|--------------|------------------------------------------------|
| `dht/krpc.go`               | Modify       | Add `ReadOnly` to `Message`, encode/decode `ro` |
| `dht/node.go`               | Modify       | Add `ReadOnly` field, `newQuery` helper, skip routing table insert for `ro=1` |
| `cmd/peer-pressure/main.go` | Modify       | Add `--dht-readonly` CLI flag                  |

---

## 4. Dependencies

| BEP | Relationship | Notes |
|-----|-------------|-------|
| 5   | Requires    | BEP 43 modifies BEP 5 DHT behavior |
| 32  | Interacts   | Read-only mode applies to both IPv4 and IPv6 DHT instances |
| 42  | Interacts   | Read-only nodes still need BEP 42-compliant IDs (they send queries that can be validated) |

---

## 5. Testing Strategy

### 5.1 `dht/krpc_test.go` — Message Encoding

| Test Case | Description |
|-----------|-------------|
| `TestEncodeQueryWithRO` | Encode a query with `ReadOnly=true`. Verify the bencoded output contains `2:roi1e`. |
| `TestEncodeQueryWithoutRO` | Encode a query with `ReadOnly=false`. Verify no `ro` key in output. |
| `TestEncodeResponseIgnoresRO` | Encode a response with `ReadOnly=true`. Verify no `ro` key (only queries carry it). |
| `TestDecodeQueryWithRO` | Decode a bencoded query containing `ro: 1`. Verify `msg.ReadOnly == true`. |
| `TestDecodeQueryWithROZero` | Decode a query with `ro: 0`. Verify `msg.ReadOnly == false`. |
| `TestDecodeQueryWithoutRO` | Decode a query without `ro` key. Verify `msg.ReadOnly == false`. |

### 5.2 `dht/node_test.go` — Routing Table Gating

| Test Case | Description |
|-----------|-------------|
| `TestROQueryNotAddedToTable` | Receive a `ping` query with `ro=1`. Verify the sender is NOT in our routing table. |
| `TestRegularQueryAddedToTable` | Receive a `ping` query without `ro`. Verify the sender IS in our routing table. |

### 5.3 `dht/node_test.go` — Outgoing Queries

| Test Case | Description |
|-----------|-------------|
| `TestReadOnlyNodeSetsROFlag` | Create a DHT with `ReadOnly=true`. Send a `find_node`. Capture the encoded message. Verify it contains `ro=1`. |
| `TestRegularNodeNoROFlag` | Create a DHT with `ReadOnly=false`. Send a `find_node`. Verify no `ro` key in the message. |

### 5.4 `dht/node_test.go` — `newQuery` Helper

| Test Case | Description |
|-----------|-------------|
| `TestNewQueryReadOnly` | Call `newQuery` on a read-only DHT. Verify `msg.ReadOnly == true` and `msg.Type == "q"`. |
| `TestNewQueryRegular` | Call `newQuery` on a regular DHT. Verify `msg.ReadOnly == false`. |

### 5.5 Integration

| Test Case | Description |
|-----------|-------------|
| `TestReadOnlyNodeCanFindPeers` | Start two in-process DHT nodes: one regular (R), one read-only (RO). R announces a torrent. RO looks up the torrent. Verify RO finds the peer. |
| `TestReadOnlyNodeNotInResponses` | RO queries R with `find_node`. R responds with `nodes`. Verify RO's address does not appear in R's `nodes` list (since RO was never added to R's table). |
| `TestReadOnlyNodeCanAnnounce` | RO announces a torrent via `announce_peer`. A third node does `get_peers`. Verify the announced peer (RO's address) appears in the values (announce still works even for read-only nodes). |
