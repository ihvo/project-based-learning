# BEP 24 — Tracker Returns External IP

## What We Built

Extended the HTTP tracker response parser to extract the `external ip` field,
which tells the client its public IP address as seen by the tracker.

**Files modified:**
- `tracker/tracker.go` — added `ExternalIP net.IP` to Response, parsing in `parseResponse`
- `tracker/tracker_test.go` — 8 new tests

## BitTorrent Concepts

### The Problem: "What's My IP?"

When you're behind a NAT router, your machine has a private IP like `192.168.1.5`,
but the outside world sees the router's public IP, say `203.0.113.42`. Your client
doesn't inherently know this public address.

This matters for three things:
1. **NAT detection** — if local IP ≠ external IP, you're NATed and likely can't
   receive incoming connections
2. **DHT node IDs (BEP 42)** — the DHT security extension requires your node ID
   to be derived from your external IP
3. **PEX (BEP 11)** — when sharing your own address with peers, you need to
   advertise the reachable one

### The Solution: Ask the Tracker

The tracker already knows your public IP — it's the source address of your HTTP
request. BEP 24 simply says: *"include it in the response."*

```
Tracker response dict:
{
  "interval": 1800,
  "peers": <compact peers>,
  "external ip": <4 or 16 raw bytes>   ← BEP 24
}
```

The IP is sent as **raw binary bytes**, not a dotted-decimal string:
- IPv4: 4 bytes (e.g., `\xcb\x00\x71\x2a` = 203.0.113.42)
- IPv6: 16 bytes

### Why Raw Bytes Instead of a String?

BitTorrent loves compact binary formats — it's the same philosophy as compact
peer encoding (BEP 23). Four bytes for an IPv4 address vs. up to 15 bytes for
the dotted-decimal string. In a protocol that shaves bytes everywhere, this is
consistent.

### Trust Model

The external IP is *informational* — a malicious tracker could lie. It's fine
for DHT node ID generation (where a wrong ID just makes you less efficient) but
shouldn't be used for security decisions.

## Go Idioms Used

### `net.IP` — The Swiss Army Knife

Go's `net.IP` is a byte slice (`type IP []byte`) that handles both IPv4 and IPv6:

```go
// IPv4: net.IPv4 returns a 16-byte IP (IPv4-in-IPv6 mapped)
ip := net.IPv4(203, 0, 113, 42)  // internally [0,0,0,0,0,0,0,0,0,0,0xff,0xff,203,0,113,42]

// IPv6: parse from string or raw bytes
ip := net.ParseIP("2001:db8::1")

// Comparison works for both
ip1.Equal(ip2)  // handles IPv4 vs IPv4-in-IPv6 correctly
```

The key insight: `net.IPv4()` returns a **16-byte** representation (IPv4-mapped
IPv6), but `Equal()` correctly compares a 4-byte and 16-byte representation of
the same address. This is why our parsing uses `net.IPv4(raw[0], raw[1], ...)` 
for 4-byte inputs — it normalizes to the canonical form.

### `net.IPv4len` and `net.IPv6len` Constants

Instead of magic numbers:

```go
switch len(raw) {
case net.IPv4len:  // 4
    r.ExternalIP = net.IPv4(raw[0], raw[1], raw[2], raw[3])
case net.IPv6len:  // 16
    ip := make(net.IP, net.IPv6len)
    copy(ip, raw)
    r.ExternalIP = ip
}
```

These stdlib constants make the intent clear and prevent typos.

### Graceful Degradation with Type Switches

The parsing silently ignores unexpected types — a pattern we use throughout:

```go
if extIP, ok := d["external ip"]; ok {
    if s, ok := extIP.(bencode.String); ok {  // only process if it's a string
        // ...
    }
    // If it's an Int, List, or Dict — we just skip it
}
```

This follows the robustness principle: be liberal in what you accept. A tracker
sending `"external ip": 42` (wrong type) shouldn't crash our client.

### Test Helper with `map[string]bencode.Value`

To avoid repetitive response construction:

```go
func buildResponseDict(extra map[string]bencode.Value) []byte {
    d := bencode.Dict{
        "interval": bencode.Int(900),
        "peers":    bencode.String(""),
    }
    for k, v := range extra {
        d[k] = v
    }
    return bencode.Encode(d)
}
```

The parameter type matters: `map[string]bencode.Value`, not `map[string]interface{}`.
Since `bencode.Dict` values must satisfy the `bencode.Value` interface, using
`interface{}` would fail at compile time. Go's type system catches this.

## Test Coverage

| Test | What It Verifies |
|------|-----------------|
| `TestParseResponse_ExternalIPv4` | 4-byte raw IP → `net.IPv4(203,0,113,42)` |
| `TestParseResponse_ExternalIPv6` | 16-byte raw IP → `2001:db8::1` |
| `TestParseResponse_ExternalIPMissing` | No `external ip` key → nil |
| `TestParseResponse_ExternalIPInvalidLength` | 7 bytes (neither 4 nor 16) → nil |
| `TestParseResponse_ExternalIPEmptyString` | Empty string → nil |
| `TestParseResponse_ExternalIPNotString` | Integer value → nil (type mismatch) |
| `TestParseResponse_ExternalIPWithPeers` | External IP doesn't break peer parsing |
| `TestParseResponse_ExternalIPLoopback` | Loopback `127.0.0.1` parsed normally |
