# BEP 7 — IPv6 Tracker Extension

## What We Built

Added IPv6 peer support to the tracker response parser. Trackers can now
return IPv6 peers alongside IPv4 peers using the `peers6` compact format.

**Files modified:**
- `tracker/tracker.go` — `parseCompactPeers6()`, `peers6` parsing in `parseResponse`, fixed `Peer.String()` for IPv6
- `tracker/tracker_test.go` — 10 new tests

## BitTorrent Concepts

### The IPv4 Assumption

The original BitTorrent protocol (BEP 3) was designed in 2001 when IPv4 was
universal. Compact peer format (BEP 23) uses exactly 6 bytes per peer:

```
[4 bytes IPv4 address][2 bytes port]  = 6 bytes per peer
```

This is efficient — 100 peers fit in 600 bytes. But it's hard-coded for IPv4.

### BEP 7: Dual-Stack Peers

BEP 7 adds a parallel `peers6` key to the tracker response:

```
{
  "interval": 1800,
  "peers": <compact IPv4 peers>,    ← existing, 6 bytes each
  "peers6": <compact IPv6 peers>    ← new, 18 bytes each
}
```

Each IPv6 entry is 18 bytes:
```
[16 bytes IPv6 address][2 bytes port]  = 18 bytes per peer
```

Both keys can be present simultaneously. A client simply merges them:

```
all_peers = parse_v4(response["peers"]) + parse_v6(response["peers6"])
```

### Why Both Keys?

Why not just use `peers` for all addresses? Because existing clients expect
6-byte entries. If a tracker put 18-byte IPv6 entries into `peers`, old clients
would misparse them — every 6 bytes would be interpreted as a garbage IPv4 peer.

The separate key provides clean backward compatibility:
- Old clients ignore `peers6` (unknown key = skip)
- New clients read both
- No version negotiation needed

### IPv6 Address Formats in Go

IPv6 addresses need special handling in host:port strings because the colons
in IPv6 conflict with the port separator:

```
Wrong:  2001:db8::1:6881       ← is 6881 the port or part of the address?
Right:  [2001:db8::1]:6881     ← brackets disambiguate
```

Go's `net.JoinHostPort` handles this automatically:

```go
net.JoinHostPort("2001:db8::1", "6881")  // → "[2001:db8::1]:6881"
net.JoinHostPort("192.168.1.1", "6881")  // → "192.168.1.1:6881"
```

That's why we use `net.JoinHostPort` in `Peer.Addr()` instead of `fmt.Sprintf`.

## Go Idioms Used

### Symmetric Parsing Functions

`parseCompactPeers` and `parseCompactPeers6` have identical structure — only
the entry size and IP copy width differ:

```go
func parseCompactPeers(data []byte) ([]Peer, error) {    // 6 bytes per peer
    if len(data)%peerCompactLen != 0 { ... }
    for i := range numPeers {
        ip := make(net.IP, 4)
        copy(ip, data[offset:offset+4])
        peers[i] = Peer{IP: ip, Port: binary.BigEndian.Uint16(data[offset+4:])}
    }
}

func parseCompactPeers6(data []byte) ([]Peer, error) {   // 18 bytes per peer
    if len(data)%peer6CompactLen != 0 { ... }
    for i := range numPeers {
        ip := make(net.IP, net.IPv6len)
        copy(ip, data[offset:offset+16])
        peers[i] = Peer{IP: ip, Port: binary.BigEndian.Uint16(data[offset+16:])}
    }
}
```

We didn't merge them into one function with a "size" parameter. Why?
- Each is 15 lines — merging would save ~5 lines but add a parameter
- The constants are different (`peerCompactLen` vs `peer6CompactLen`)
- Separate functions are easier to test independently
- YAGNI: we won't add IPv8 parsing

### `net.IP` — One Type for Both Protocols

Go's `net.IP` is a `[]byte` that works for both IPv4 and IPv6. The `Peer`
struct doesn't need an "address family" field:

```go
type Peer struct {
    IP   net.IP
    Port uint16
}
```

Whether `IP` holds 4 bytes (IPv4) or 16 bytes (IPv6) is determined by how
it was created. All the `net` package functions (`Equal`, `String`,
`JoinHostPort`) handle both transparently.

### Defensive Copy for IP Slices

```go
ip := make(net.IP, net.IPv6len)
copy(ip, data[offset:offset+16])
```

We allocate a new slice and copy, instead of slicing `data[offset:offset+16]`.
This prevents aliasing — if the caller reuses the `data` buffer (common in
UDP receive loops), our parsed peers won't get corrupted.

This is the same pattern we use in the IPv4 parser and throughout the
bencode package.

## Test Coverage

| Test | What It Verifies |
|------|-----------------|
| `TestParseCompactPeers6` | Two IPv6 peers round-trip correctly |
| `TestParseCompactPeers6Empty` | Empty data → empty slice, no error |
| `TestParseCompactPeers6BadLength` | 19 bytes (not ×18) → error |
| `TestParseResponse_Peers6` | Mixed v4 + v6 → both in Peers slice |
| `TestParseResponse_Peers6Only` | Only `peers6`, empty `peers` → v6 peers returned |
| `TestParseResponse_Peers6BadLength` | Malformed `peers6` → parse error |
| `TestPeerAddrIPv6` | IPv6 Addr() includes brackets |
| `TestPeerStringIPv6` | IPv6 String() includes brackets |
| `TestAnnounceHTTP_Peers6` | End-to-end with httptest mock returning `peers6` |
