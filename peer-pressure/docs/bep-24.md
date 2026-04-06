# BEP 24 — Tracker Returns External IP

> Reference: <https://www.bittorrent.org/beps/bep_0024.html>

## Summary

BEP 24 defines a mechanism for the tracker to tell the client its external IP address as seen from the tracker's perspective. The tracker includes an `external ip` key in its response, containing the client's public IP as a raw binary string (4 bytes for IPv4, 16 bytes for IPv6).

This matters for three reasons:

1. **NAT detection**: The client can compare its local IP with the tracker-reported external IP. If they differ, the client is behind a NAT — it should not expect unsolicited incoming connections unless it has configured port forwarding or UPnP.
2. **DHT node ID generation (BEP 42)**: The DHT security extension requires the node ID to be derived from the node's external IP address. Without knowing the external IP, the client cannot generate a compliant node ID.
3. **Peer Exchange correctness**: When sharing peer addresses via PEX (BEP 11), the client needs to know its own external address to advertise.

## Protocol Specification

### HTTP Tracker Response

The tracker response dictionary MAY contain an `external ip` key. The value is a bencoded byte string containing the raw IP address:

**IPv4 — 4 bytes:**

```
 0       1       2       3
+-------+-------+-------+-------+
| Octet | Octet | Octet | Octet |
|   1   |   2   |   3   |   4   |
+-------+-------+-------+-------+
```

Example: `203.0.113.42` → `\xcb\x00\x71\x2a` (4 bytes)

**IPv6 — 16 bytes:**

```
 0                   1
 0 1 2 3 4 5 6 7 8 9 0 1 2 3 4 5
+-------+-------+-------+-------+
|                               |
|      IPv6 Address (16)        |
|                               |
|                               |
+-------+-------+-------+-------+
```

The client MUST handle both 4-byte and 16-byte values. Any other length is invalid and SHOULD be ignored.

### UDP Tracker Response

The standard BEP 15 UDP tracker protocol does not include an external IP field. BEP 41 (UDP Tracker Protocol Extensions) adds this as an option TLV:

- **Type**: `0x03`
- **Length**: 4 (IPv4) or 16 (IPv6)
- **Value**: Raw IP bytes in network byte order

See `docs/bep-41.md` for the full TLV parsing specification. Once BEP 41 parsing is implemented, the external IP option feeds into the same storage as the HTTP tracker's `external ip` field.

### Behavior

- The external IP may change between announces (e.g., if the client roams between networks). The client SHOULD update its stored external IP on every announce response.
- If multiple trackers report different external IPs, the client SHOULD prefer the most recently received value, or use a consensus approach (majority of trackers agree).
- The external IP MUST NOT be treated as authoritative for security decisions — it's informational and can be spoofed by a malicious tracker. However, for DHT node ID generation (BEP 42), using any single tracker's report is the standard approach.

## Implementation Plan

### Files to Create / Modify

| File | Action | Purpose |
|------|--------|---------|
| `tracker/tracker.go` | Modify | Parse `external ip` from HTTP response, add `ExternalIP` field to `Response` |
| `tracker/udp.go` | Modify | Will accept external IP from BEP 41 TLV parsing (deferred until BEP 41) |
| `tracker/tracker_test.go` | Modify | Add tests for external IP parsing |

### Key Types

```go
// Add to the existing Response struct in tracker/tracker.go:
type Response struct {
    Interval   int
    Peers      []Peer
    Complete   int
    Incomplete int
    ExternalIP net.IP // BEP 24: our IP as seen by the tracker (nil if not provided)
}
```

No new types needed. `net.IP` handles both IPv4 and IPv6 natively.

### Key Functions

No new exported functions — the external IP parsing is folded into the existing `parseResponse` function.

### Changes to Existing Functions

**`tracker/tracker.go` — `parseResponse`:**

After the existing field parsing, add:

```go
// BEP 24: external IP
if extIP, ok := d["external ip"]; ok {
    if s, ok := extIP.(bencode.String); ok {
        raw := []byte(s)
        switch len(raw) {
        case 4:
            r.ExternalIP = net.IPv4(raw[0], raw[1], raw[2], raw[3])
        case 16:
            ip := make(net.IP, 16)
            copy(ip, raw)
            r.ExternalIP = ip
        }
        // Other lengths: silently ignore per spec
    }
}
```

### Exposing External IP to Other Subsystems

The external IP needs to be accessible beyond the tracker package. The recommended approach:

```go
// In whatever orchestrator/session coordinates the download:
resp, err := tracker.Announce(url, params)
if err != nil { ... }

if resp.ExternalIP != nil {
    // Store for use by DHT (BEP 42 node ID generation), PEX, NAT detection
    session.SetExternalIP(resp.ExternalIP)
}
```

The `download/session.go` or a future top-level `client` package would hold the external IP and expose it to subsystems. The exact wiring depends on how the session/client is structured, but the tracker package's job is just to parse and return it.

### Package Placement

All parsing lives in `tracker/`. Downstream consumers (DHT, session) read it from `Response.ExternalIP`. No new packages needed.

## Dependencies

| BEP | Relationship |
|-----|-------------|
| BEP 3 | Base HTTP tracker protocol — BEP 24 adds a key to the response dict |
| BEP 15 | UDP tracker protocol — external IP is not in the base spec but added via BEP 41 |
| BEP 41 | UDP tracker extensions carry external IP as option type `0x03` |
| BEP 42 | DHT security extension — primary consumer of external IP for node ID generation |
| BEP 7 | IPv6 tracker extension — external IP may be IPv6 when connecting to an IPv6 tracker |

## Testing Strategy

### Unit Tests

**`tracker/tracker_test.go` (additions):**

1. **`TestParseResponse_ExternalIPv4`** — Build a bencoded response with `external ip` set to 4 bytes representing `203.0.113.42`. Verify `Response.ExternalIP` equals `net.IPv4(203, 0, 113, 42)`.

2. **`TestParseResponse_ExternalIPv6`** — Build a bencoded response with `external ip` set to 16 bytes representing `2001:db8::1`. Verify the parsed IP matches.

3. **`TestParseResponse_ExternalIPMissing`** — Standard response without `external ip` key. Verify `Response.ExternalIP` is nil.

4. **`TestParseResponse_ExternalIPInvalidLength`** — Set `external ip` to a 7-byte string (neither 4 nor 16). Verify `Response.ExternalIP` is nil (gracefully ignored).

5. **`TestParseResponse_ExternalIPEmptyString`** — Set `external ip` to an empty byte string. Verify nil result.

6. **`TestParseResponse_ExternalIPWithPeers`** — Full response with `interval`, `peers` (compact), and `external ip`. Verify all fields parse correctly — the external IP parsing must not interfere with peer parsing.

### Integration Tests

7. **`TestHTTPAnnounce_ExternalIP`** — Stand up an `httptest.Server` that returns a bencoded response including `external ip`. Call `announceHTTP` and verify `Response.ExternalIP` is populated.

8. **`TestExternalIPUpdate`** — Call announce twice with different mock responses (different external IPs). Verify the caller sees the updated IP on the second response. (This tests the consumer pattern, not the tracker package itself.)

### Edge Cases

9. **`TestParseResponse_ExternalIPNotString`** — Set `external ip` to a bencode integer instead of a string. Verify it's silently ignored (no panic, no error, ExternalIP is nil).

10. **`TestParseResponse_ExternalIPLoopback`** — Set `external ip` to `127.0.0.1` (4 bytes). Verify it's parsed correctly — the tracker package doesn't filter; consumers decide what to do with loopback addresses.
