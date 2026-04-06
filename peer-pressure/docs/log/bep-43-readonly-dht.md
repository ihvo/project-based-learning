# BEP 43 — Read-only DHT Nodes

## What We Built

Added `ReadOnly` mode to the DHT node so it sets the `ro=1` flag in all
outgoing queries. This tells other DHT nodes not to add us to their routing
tables — important for nodes behind NATs that can't receive incoming queries.

**Files modified:**
- `dht/krpc.go` — added `ReadOnly` to Message, encode/decode `ro` flag
- `dht/node.go` — added `ReadOnly` to DHT struct, `newQuery()` helper
- `dht/krpc_test.go` — 8 new tests

## BitTorrent Concepts

### The NAT Problem in DHT

The DHT works by nodes routing queries to each other. Node A asks Node B for
nodes closer to a target. B responds with nodes from its routing table,
including C. Now A queries C.

But what if C is behind a symmetric NAT? C could *send* queries (its outgoing
UDP works), so B added C to its table. But when A tries to query C, the
NAT drops the unsolicited incoming packet. A wastes time waiting for a timeout.

```
A ──query──► C's NAT ──── (dropped) ──── C
             (no mapping exists for A's address)
```

This is a **routing table pollution** problem. Read-only nodes in tables degrade
lookup performance for everyone.

### The Solution: Be Honest

BEP 43 is elegantly simple. A node that can't serve queries says so upfront:

```
{
  "t": "aa",
  "y": "q",
  "q": "find_node",
  "ro": 1,          ← "don't put me in your routing table"
  "a": { "id": "<20 bytes>", "target": "<20 bytes>" }
}
```

Rules:
- **`ro=1` in queries only** — responses and errors don't carry it
- **Don't store `ro=1` senders** in your routing table
- **Don't include `ro=1` senders** in `nodes`/`nodes6` responses
- `ro=1` nodes can still **find peers and announce** — they just don't serve
  as routing infrastructure

### A Read-only Node Can Still Do Everything a Client Needs

```
✅ Find peers (get_peers iterative lookup)
✅ Announce to peers (announce_peer)
✅ Bootstrap (find_node on own ID)
✅ Discover nodes (responses still work)

❌ Appear in other nodes' routing tables
❌ Serve incoming queries from random nodes
❌ Act as routing infrastructure
```

This is perfect for a BitTorrent client that just wants to find peers via DHT.

### When to Enable Read-only

Detecting NAT programmatically is non-trivial. The practical approach:
1. Default to read-only (safe for all network configurations)
2. Add a `--dht-readonly=false` flag to opt out if you know you're reachable
3. Auto-detection (track incoming queries over time) can be added later

## Go Idioms Used

### Centralized Query Construction

Instead of duplicating the `ReadOnly` flag across 4 different call sites, we
added a `newQuery` helper:

```go
func (d *DHT) newQuery(method string, args bencode.Dict) Message {
    return Message{
        Type:     "q",
        Method:   method,
        Args:     args,
        ReadOnly: d.ReadOnly,
    }
}
```

Before:
```go
d.Transport.Send(addr, Message{Type: "q", Method: "ping", Args: ...}, timeout)
d.Transport.Send(addr, Message{Type: "q", Method: "find_node", Args: ...}, timeout)
d.Transport.Send(addr, Message{Type: "q", Method: "get_peers", Args: ...}, timeout)
d.Transport.Send(addr, Message{Type: "q", Method: "announce_peer", Args: ...}, timeout)
```

After:
```go
d.Transport.Send(addr, d.newQuery("ping", args), timeout)
d.Transport.Send(addr, d.newQuery("find_node", args), timeout)
```

One place to add cross-cutting behavior (like `ro`) instead of four.

### Conditional Encoding

The `ro` flag is only encoded when both conditions are true: the node is
read-only AND the message is a query:

```go
switch msg.Type {
case "q":
    d["q"] = bencode.String(msg.Method)
    d["a"] = msg.Args
    if msg.ReadOnly {
        d["ro"] = bencode.Int(1)
    }
```

This means responses never carry `ro`, even if `ReadOnly` is accidentally
set on a response message. Defense in depth.

### Zero-Value Defaults

`ReadOnly bool` defaults to `false` (Go's zero value). Existing code that
constructs `Message{}` literals without setting `ReadOnly` continues to work
unchanged — they produce regular (non-read-only) messages.

This is why we didn't need to update any test that constructs `Message{}`
manually. The zero value is the correct default.

### Test Pattern: Verify Absence

Testing that something is *not* present:

```go
func TestEncodeResponseIgnoresRO(t *testing.T) {
    // ...
    val, _ := bencode.Decode(data)
    d := val.(bencode.Dict)
    if _, ok := d["ro"]; ok {
        t.Error("response should not contain 'ro' key")
    }
}
```

We decode the bencoded output and check the raw dict, rather than just
checking the decoded `Message.ReadOnly` field. This verifies the wire
format, not just the round-trip logic.

## Test Coverage

| Test | What It Verifies |
|------|-----------------|
| `TestEncodeQueryWithRO` | Query with `ReadOnly=true` round-trips correctly |
| `TestEncodeQueryWithoutRO` | No `ro` key in bencoded output when false |
| `TestEncodeResponseIgnoresRO` | Response never contains `ro` key |
| `TestDecodeQueryWithRO` | Decodes `ro: 1` → `ReadOnly=true` |
| `TestDecodeQueryWithROZero` | Decodes `ro: 0` → `ReadOnly=false` |
| `TestDecodeQueryWithoutRO` | Missing `ro` → `ReadOnly=false` |
| `TestNewQueryReadOnly` | Helper sets ReadOnly from DHT struct |
| `TestNewQueryRegular` | Helper leaves ReadOnly false for regular node |
