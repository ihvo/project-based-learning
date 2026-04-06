# BEP 40 — Canonical Peer Priority

> Reference: <https://www.bittorrent.org/beps/bep_0040.html>

## Summary

BEP 40 defines a deterministic algorithm for assigning priority to peer connections. When a client reaches its connection limit, it uses these priorities to decide which peers to keep and which to drop. The priority is computed as a CRC32-C hash of both peers' IP addresses, masked to subnet boundaries.

This matters because without a consistent peer selection strategy, swarms tend to cluster — peers preferentially connect to nearby nodes (same subnet, same ISP) and the swarm's overlay network becomes poorly connected. Random eviction makes this worse because different clients make independent random choices, fragmenting the graph.

BEP 40 ensures that any two clients compute the *same* priority for the same pair of IPs. When both sides agree on which connections are high-priority, the swarm graph stabilizes into a well-connected topology with good cross-subnet links.

## Protocol Specification

### Priority Calculation

Given two peers with IP addresses `self_ip` and `peer_ip`:

1. **Mask the IPs to subnet boundaries:**
   - IPv4: mask both IPs to `/24` (set the last octet to 0).
   - IPv6: mask both IPs to `/48` (zero out the last 10 bytes).
   - **Exception**: If both IPs are in the same subnet (same masked value), use the full unmasked IPs instead. This gives same-subnet peers distinct priorities rather than all colliding to the same hash.

2. **Order the (possibly masked) IPs:**
   - `low = min(masked_self, masked_peer)` (lexicographic byte comparison)
   - `high = max(masked_self, masked_peer)`

3. **Compute the priority:**
   ```
   priority = crc32c(low + high)
   ```
   Where `+` is byte concatenation and `crc32c` uses the Castagnoli polynomial (`0x1EDC6F41`).

4. **Higher priority = keep the connection.** When the connection limit is reached and a new peer wants to connect, compare the new peer's priority to all existing connections. If the new peer has higher priority than the lowest-priority existing connection, drop the lowest and accept the new one.

### IPv4 Masking — Detailed

```
Original:    192.168.1.42
Mask /24:    192.168.1.0     (last byte zeroed)

Original:    10.0.0.1
Mask /24:    10.0.0.0
```

**Same-subnet check**: If `masked_self == masked_peer`, skip masking — use the original full IPs for the hash. This ensures two peers on `192.168.1.x` get different priorities.

### IPv6 Masking — Detailed

```
Original:    2001:0db8:85a3:0000:0000:8a2e:0370:7334
Mask /48:    2001:0db8:85a3:0000:0000:0000:0000:0000   (bytes 6-15 zeroed)

Original:    fe80::1
Mask /48:    fe80:0000:0000:0000:0000:0000:0000:0000
```

**Same-subnet check**: Same logic — if the `/48` masked values match, use full IPs.

### CRC32-C (Castagnoli)

The hash function is CRC32 with the Castagnoli polynomial, NOT the standard CRC32 (IEEE/Ethernet) polynomial.

- Polynomial: `0x1EDC6F41`
- Go: `hash/crc32.MakeTable(crc32.Castagnoli)` or `crc32.New(crc32.MakeTable(crc32.Castagnoli))`
- Input: concatenation of the two IP byte slices (4+4=8 bytes for IPv4, 16+16=32 bytes for IPv6)
- Output: uint32

### Usage in Peer Management

The client maintains a connection pool with a configurable maximum (e.g., 50 connections). When the pool is full:

1. A new incoming connection arrives (or we discover a new peer to connect to).
2. Compute priority for the new peer.
3. Find the existing connection with the **lowest** priority.
4. If new_priority > lowest_existing_priority: drop the lowest, accept the new one.
5. If new_priority ≤ lowest_existing_priority: reject the new connection (or don't initiate it).

When deciding which outgoing connections to initiate, prefer peers with higher priority.

### Mixed Address Families

When comparing an IPv4 peer with an IPv6 peer:

- If the IPv6 address is an IPv4-mapped address (`::ffff:a.b.c.d`), extract the IPv4 address and compare as IPv4.
- Otherwise, the addresses are different families. The spec does not strictly define cross-family priority. A practical approach: compute priority separately within each family and maintain separate budgets, or convert IPv4 to IPv4-mapped IPv6 before comparing.

### Reference Test Vectors

From the BEP 40 specification:

| Self IP | Peer IP | Same Subnet? | Masked Self | Masked Peer | Priority (CRC32-C) |
|---------|---------|-------------|-------------|-------------|---------------------|
| `123.213.32.10` | `98.76.54.32` | No | `123.213.32.0` | `98.76.54.0` | `0xec2d7224` |
| `123.213.32.10` | `123.213.32.234` | Yes | `123.213.32.10` | `123.213.32.234` | `0x99568189` |

## Implementation Plan

### Files to Create / Modify

| File | Action | Purpose |
|------|--------|---------|
| `peer/priority.go` | Create | `Priority` function, IP masking, CRC32-C computation |
| `peer/priority_test.go` | Create | Tests including BEP 40 reference vectors |
| `download/pool.go` | Modify | Use priority for connection eviction decisions |

### Key Types

```go
// No new exported types needed. The priority is a plain uint32.
```

### Key Functions

```go
// peer/priority.go

// Priority computes the BEP 40 canonical peer priority between two IP addresses.
// Uses CRC32-C (Castagnoli) of the masked, ordered IP pair.
// Returns a uint32 — higher values mean higher priority.
func Priority(self, peer net.IP) uint32

// maskIP masks an IP address to its subnet boundary.
// IPv4 → /24 (last byte zeroed). IPv6 → /48 (last 10 bytes zeroed).
// Returns the masked IP and whether masking was applied.
func maskIP(ip net.IP) net.IP

// sameSubnet returns true if both IPs, after masking, are equal.
func sameSubnet(a, b net.IP) bool
```

### Implementation Detail

```go
package peer

import (
    "bytes"
    "hash/crc32"
    "net"
)

var castagnoliTable = crc32.MakeTable(crc32.Castagnoli)

func Priority(self, peer net.IP) uint32 {
    selfIP := normalizeIP(self)
    peerIP := normalizeIP(peer)

    maskedSelf := maskIP(selfIP)
    maskedPeer := maskIP(peerIP)

    var a, b []byte
    if bytes.Equal(maskedSelf, maskedPeer) {
        // Same subnet: use full (unmasked) IPs for distinct priorities
        a, b = selfIP, peerIP
    } else {
        a, b = maskedSelf, maskedPeer
    }

    // Order: low || high
    if bytes.Compare(a, b) > 0 {
        a, b = b, a
    }

    buf := make([]byte, len(a)+len(b))
    copy(buf, a)
    copy(buf[len(a):], b)

    return crc32.Checksum(buf, castagnoliTable)
}

// normalizeIP ensures the IP is in its canonical form:
// IPv4 addresses are 4 bytes, IPv6 are 16 bytes.
// IPv4-mapped IPv6 addresses (::ffff:a.b.c.d) are converted to 4-byte IPv4.
func normalizeIP(ip net.IP) net.IP {
    if v4 := ip.To4(); v4 != nil {
        return v4
    }
    return ip.To16()
}

func maskIP(ip net.IP) net.IP {
    if len(ip) == 4 {
        // IPv4 /24: zero last byte
        return net.IP{ip[0], ip[1], ip[2], 0}
    }
    // IPv6 /48: zero bytes 6-15
    masked := make(net.IP, 16)
    copy(masked, ip[:6])
    // bytes 6-15 remain zero
    return masked
}

func sameSubnet(a, b net.IP) bool {
    return bytes.Equal(maskIP(a), maskIP(b))
}
```

### Integration with Connection Pool

In `download/pool.go`, when the pool is full and a new peer is available:

```go
func (p *Pool) shouldReplace(newPeer net.IP) (evictIdx int, ok bool) {
    selfIP := p.localIP // set from BEP 24 external IP or local detection
    newPriority := peer.Priority(selfIP, newPeer)

    lowestPriority := uint32(math.MaxUint32)
    lowestIdx := -1
    for i, conn := range p.conns {
        pri := peer.Priority(selfIP, conn.RemoteIP())
        if pri < lowestPriority {
            lowestPriority = pri
            lowestIdx = i
        }
    }

    if newPriority > lowestPriority {
        return lowestIdx, true
    }
    return -1, false
}
```

### Package Placement

The `Priority` function lives in `peer/` since it's fundamentally about peer relationship scoring. The connection pool in `download/` calls it when making eviction decisions.

## Dependencies

| BEP | Relationship |
|-----|-------------|
| BEP 3 | Base peer protocol — BEP 40 governs which peer connections to maintain |
| BEP 24 | External IP — needed to know our own IP for the priority calculation |
| BEP 7 | IPv6 support — BEP 40 defines masking rules for both IPv4 and IPv6 |

## Testing Strategy

### Unit Tests (`peer/priority_test.go`)

1. **`TestPriorityReferenceVectors`** — Use the exact test vectors from the BEP 40 spec (listed above). Verify `Priority` returns the expected CRC32-C values.

2. **`TestPrioritySymmetric`** — `Priority(A, B)` must equal `Priority(B, A)`. Test with several IP pairs.

3. **`TestPrioritySameSubnet`** — Two IPs in the same `/24` (e.g., `10.0.1.5` and `10.0.1.200`). Verify the full (unmasked) IPs are used, and the priority differs from what you'd get with masked IPs.

4. **`TestPriorityDifferentSubnets`** — Two IPs in different `/24`s. Verify masking is applied (changing the last octet of either IP doesn't change the priority).

5. **`TestPriorityIPv6Masking`** — Two IPv6 addresses in different `/48`s. Verify bytes 6–15 don't affect the priority.

6. **`TestPriorityIPv6SameSubnet`** — Two IPv6 addresses in the same `/48`. Verify full IPs are used.

7. **`TestPriorityIPv4MappedIPv6`** — Peer reports `::ffff:192.168.1.1` (IPv4-mapped IPv6). Verify it's treated as IPv4 `192.168.1.1`.

8. **`TestPriorityDeterministic`** — Call `Priority` with the same inputs 1000 times. Verify the result never changes (no randomness).

9. **`TestMaskIPv4`** — Verify `maskIP(10.0.1.42)` returns `10.0.1.0`.

10. **`TestMaskIPv6`** — Verify `maskIP(2001:db8:85a3::1)` returns `2001:db8:85a3::`.

11. **`TestSameSubnet`** — True for `10.0.1.5` / `10.0.1.200`, false for `10.0.1.5` / `10.0.2.5`.

### Integration Tests

12. **`TestPoolEviction`** — Fill a connection pool to capacity. Add a peer with higher priority than the lowest existing connection. Verify the lowest-priority connection is evicted and the new one is accepted.

13. **`TestPoolRejectLowPriority`** — Fill a pool. Try to add a peer with lower priority than all existing connections. Verify it's rejected and the pool is unchanged.
