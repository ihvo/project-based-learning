# BEP 7 — IPv6 Tracker Extension

> Reference: <https://www.bittorrent.org/beps/bep_0007.html>

## Summary

BEP 7 extends the tracker announce protocol so clients can discover IPv6 peers alongside IPv4 peers. Without this extension, the standard tracker protocol (BEP 3/15/23) only carries IPv4+port pairs (6 bytes each). BEP 7 adds:

1. A way for the client to tell the tracker its IPv6 address.
2. A way for the tracker to return IPv6 peers in a compact binary format.

This matters because BitTorrent swarms increasingly run on dual-stack networks. A client that only speaks IPv4 misses peers that are reachable only via IPv6 — and in some ISP networks, IPv6 is the *only* routable address family. Implementing BEP 7 means Peer Pressure can participate in the full swarm regardless of the network stack.

## Protocol Specification

### HTTP Tracker

#### Request

The client appends an `ipv6` query parameter to the announce URL. The value is the client's IPv6 address, URL-encoded. Optionally, the client may also include an `ipv4` parameter if it wants to explicitly advertise an IPv4 address (normally inferred from the source IP).

```
GET /announce?info_hash=...&peer_id=...&port=6881&ipv6=fe80%3A%3A1&compact=1
```

The IPv6 address is the raw textual representation (RFC 5952 recommended form), percent-encoded per RFC 3986. The tracker uses this to include the announcing client in IPv6 peer lists returned to other clients.

#### Response — `peers6` Key

The tracker response is a bencoded dictionary. In addition to the standard `peers` key (compact IPv4, 6 bytes each per BEP 23), the tracker MAY include a `peers6` key containing compact IPv6 peer data.

**Compact IPv6 peer format — 18 bytes per peer:**

```
 0                   1
 0 1 2 3 4 5 6 7 8 9 0 1 2 3 4 5 6 7
+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
|                                   |
|         IPv6 Address (16)         |
|                                   |
|                                   |
+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
|       Port (2, big-endian)        |
+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
```

- Bytes 0–15: 128-bit IPv6 address in network byte order.
- Bytes 16–17: TCP port, big-endian uint16.

The `peers6` value is a byte string whose length MUST be a multiple of 18. A client MUST parse both `peers` and `peers6` if present and merge the results.

The tracker MAY also return a `peers6` list of dictionaries (non-compact), though this is rare in practice. Each dictionary has `ip` (string, IPv6 textual representation) and `port` (integer).

### UDP Tracker

BEP 15's UDP announce response already has a fixed binary format. The standard response only contains 6-byte compact IPv4 peers after the 20-byte header. BEP 7 does not formally define a new action for IPv6 in UDP trackers — instead, real-world implementations handle this in two ways:

1. **Separate socket / endpoint**: The tracker listens on an IPv6 UDP socket. Clients connecting over IPv6 receive IPv6 peers (18 bytes each) in the standard peer data section of the announce response. The response format is identical but with 18-byte peer entries instead of 6-byte entries.
2. **BEP 41 extensions**: The tracker appends IPv6 peer data as a TLV option after the standard response (see `docs/bep-41.md`).

The client determines the peer entry size from the response length:

```
peer_data = response[20:]
if len(peer_data) % 6 == 0:
    parse as IPv4 (6 bytes each)
elif len(peer_data) % 18 == 0:
    parse as IPv6 (18 bytes each)
else:
    try mixed parsing or report error
```

In practice, a single UDP response carries either all-IPv4 or all-IPv6 peers. Mixed responses are not standard.

### Client Behavior

1. **Detect local IPv6 address**: On startup, enumerate local network interfaces. If a globally-routable (non-link-local, non-loopback) IPv6 address is found, include it in HTTP announces via the `ipv6=` parameter.
2. **Dual-stack announce**: For UDP trackers, if the tracker hostname resolves to both A and AAAA records, the client SHOULD announce to both (two separate UDP flows). IPv4 announces return IPv4 peers; IPv6 announces return IPv6 peers.
3. **Merge peers**: All discovered peers (IPv4 and IPv6) feed into the same peer pool. A peer identified by both address families is treated as two distinct connection candidates — TCP will establish whichever connects first.
4. **Peer deduplication**: Peers are ultimately identified by `(info_hash, peer_id)` after the handshake. The client should deduplicate by peer_id once a connection is established, even if the peer was discovered on both IPv4 and IPv6.

## Implementation Plan

### Files to Create / Modify

| File | Action | Purpose |
|------|--------|---------|
| `tracker/ipv6.go` | Create | `ParseCompactPeers6`, `DetectIPv6Address` |
| `tracker/tracker.go` | Modify | Add `ipv6=` param to `buildAnnounceURL`, parse `peers6` in `parseResponse` |
| `tracker/udp.go` | Modify | Detect 18-byte peer entries in `udpAnnounce` response |
| `tracker/ipv6_test.go` | Create | Tests for IPv6 peer parsing and address detection |

### Key Types

```go
// No new types needed — tracker.Peer already uses net.IP which handles both
// IPv4 and IPv6. The existing Peer struct works unchanged:
//
// type Peer struct {
//     IP   net.IP
//     Port uint16
// }
```

### Key Functions

```go
// tracker/ipv6.go

// ParseCompactPeers6 parses compact IPv6 peer data (18 bytes per peer).
// Each entry is 16 bytes IPv6 address + 2 bytes big-endian port.
func ParseCompactPeers6(data []byte) ([]Peer, error)

// DetectIPv6Address returns the first globally-routable IPv6 address on the
// system, or nil if none is found. Used to populate the ipv6= announce param.
func DetectIPv6Address() net.IP
```

### Changes to Existing Functions

**`tracker/tracker.go` — `buildAnnounceURL`:**
Add optional `IPv6Addr net.IP` field to `AnnounceParams`. When non-nil, append `&ipv6=<percent-encoded address>` to the query string.

```go
// Add to AnnounceParams:
type AnnounceParams struct {
    // ... existing fields ...
    IPv6Addr net.IP // optional: our IPv6 address to advertise
}
```

**`tracker/tracker.go` — `parseResponse`:**
After parsing the `peers` key, also check for `peers6`:

```go
// In parseResponse, after existing peers parsing:
if peers6Val, ok := d["peers6"]; ok {
    switch p := peers6Val.(type) {
    case bencode.String:
        ipv6Peers, err := ParseCompactPeers6([]byte(p))
        if err != nil {
            return nil, fmt.Errorf("parse compact peers6: %w", err)
        }
        r.Peers = append(r.Peers, ipv6Peers...)
    }
}
```

**`tracker/udp.go` — `udpAnnounce`:**
After reading `resp[20:]`, detect whether the peer data is 6-byte (IPv4) or 18-byte (IPv6) entries:

```go
peerData := resp[20:]
if len(peerData)%18 == 0 && len(peerData)%6 != 0 {
    peers, err = ParseCompactPeers6(peerData)
} else {
    peers, err = parseCompactPeers(peerData)
}
```

### Package Placement

All changes stay in the `tracker/` package. No new packages needed — IPv6 is an extension of the existing tracker protocol, not a new subsystem.

## Dependencies

| BEP | Relationship |
|-----|-------------|
| BEP 3 | Base HTTP tracker protocol that BEP 7 extends |
| BEP 15 | UDP tracker protocol — BEP 7 affects how we parse UDP announce responses |
| BEP 23 | Compact peer format — BEP 7 defines the IPv6 analog (18 bytes vs 6 bytes) |
| BEP 41 | UDP tracker extensions may carry IPv6 peer data as TLV options (see `docs/bep-41.md`) |
| BEP 32 | DHT IPv6 extensions — uses the same compact IPv6 format for DHT node encoding |

## Testing Strategy

### Unit Tests

**`tracker/ipv6_test.go`:**

1. **`TestParseCompactPeers6_Valid`** — Construct a byte slice with known IPv6+port entries (e.g., `::1` port 6881, `2001:db8::1` port 51413). Parse and verify IP/port match.

2. **`TestParseCompactPeers6_Empty`** — Empty input returns zero peers, no error.

3. **`TestParseCompactPeers6_InvalidLength`** — Input length not a multiple of 18 returns an error.

4. **`TestParseCompactPeers6_MultiplePeers`** — 3+ peers in a single byte string. Verify count and all fields.

5. **`TestDetectIPv6Address_Loopback`** — When the only IPv6 address is `::1`, return nil (loopback is not globally routable).

6. **`TestParseResponse_Peers6`** — Build a bencoded tracker response with both `peers` (compact IPv4) and `peers6` (compact IPv6). Call `parseResponse` and verify merged peer list contains both address families.

7. **`TestParseResponse_Peers6Only`** — Tracker response with `peers6` but no `peers` key. Verify IPv6 peers are returned.

8. **`TestBuildAnnounceURL_WithIPv6`** — Set `AnnounceParams.IPv6Addr` to `2001:db8::1` and verify the resulting URL contains `ipv6=2001%3Adb8%3A%3A1`.

9. **`TestBuildAnnounceURL_WithoutIPv6`** — When `IPv6Addr` is nil, the `ipv6=` parameter is absent from the URL.

### Integration Tests

10. **`TestUDPAnnounce_IPv6Peers`** — Stand up a mock UDP tracker that returns 18-byte peer entries. Verify `udpAnnounce` parses them correctly.

11. **`TestHTTPAnnounce_DualStack`** — Mock HTTP tracker returns both `peers` and `peers6`. Verify the final `Response.Peers` slice contains both IPv4 and IPv6 entries.
