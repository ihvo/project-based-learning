# BEP 42: DHT Security Extension

## What It Does

BEP 42 ties a DHT node's ID to its external IP address, making Sybil attacks
much harder. Without this, an attacker can generate arbitrary node IDs to
cluster around a target info_hash and intercept or block all traffic to it.

### The Core Algorithm

For IPv4, the first 21 bits of a node ID must match:

```
crc32c((ip & 0x030f3fff) | (r << 29))
```

Where `r` is a random 3-bit value (0–7) stored in the last byte of the node
ID. The mask `0x030f3fff` limits how many node IDs any IP block can claim:
- A /8 block varies only 3 bits of the masked input
- That means each /8 can produce only ~8 distinct 21-bit prefixes
- Attacking the DHT requires controlling IPs across many different /8 blocks

For IPv6, the high 64 bits of the address are masked with
`0x0103070f1f3f7fff` and `r` is shifted to bit 61.

### CRC32C, Not SHA-1

The spec uses CRC32C (Castagnoli) instead of a cryptographic hash because:
1. The input space is tiny (~2^32 IPv4 addresses) — any hash is reversible
2. CRC32C has better bit-prefix uniformity than SHA-1 for this input set
3. Intel SSE 4.2 has hardware CRC32C support
4. It's fast — DHT nodes validate IDs on every received message

### What We Implemented

1. **`GenerateSecureNodeID(ip)`** — creates a BEP 42 compliant node ID
2. **`ValidateNodeID(id, ip)`** — checks the 21-bit prefix constraint
3. **`isLocalIP()`** — exempts private/loopback addresses (10/8, 172.16/12,
   192.168/16, 169.254/16, 127/8)
4. **`ExternalIPVote`** — tracks IP votes from DHT responses to determine our
   real external address (consensus mechanism — no single node is trusted)
5. **`ParseIPField`/`EncodeIPField`** — compact binary IP+port encoding for
   the `ip` response key

### Enforcement Strategy

Per BEP 42, non-compliant nodes:
- Should not be counted as valid responses in lookup termination
- Should not receive `announce_peer` messages (their tokens are untrusted)
- Should still participate in routing (gradual migration)

## Go Idioms

### CRC32 with Custom Polynomial

```go
var crc32cTable = crc32.MakeTable(crc32.Castagnoli)

crc := crc32.Checksum(buf, crc32cTable)
```

Go's `hash/crc32` package supports multiple polynomials. `crc32.IEEE` is the
default (used by Ethernet/zlib), but BEP 42 needs Castagnoli. The table is
computed once at package init time — `MakeTable` returns a `*crc32.Table`
that's safe for concurrent use.

### Bit Manipulation for ID Construction

```go
id[0] = byte(crc >> 24)
id[1] = byte(crc >> 16)
id[2] = (byte(crc>>8) & 0xf8) | (id[2] & 0x07)
id[19] = r | (id[19] & 0xf8)
```

Classic bit-field packing: the top 5 bits of byte 2 come from CRC, the low 3
are random. `& 0xf8` clears the low 3 bits, `& 0x07` preserves only the low
3. The `|` combines them. This pattern appears whenever you need to pack
multiple values into a single byte.

### Mutex-Protected Vote Counter

```go
type ExternalIPVote struct {
    votes map[string]int
    mu    sync.Mutex
}
```

DHT responses arrive from many goroutines. The vote tracker uses a simple
mutex rather than `sync.Map` because: (a) writes are frequent (every
response), (b) the map is small (typically < 10 entries), and (c) `sync.Map`
is optimized for read-heavy, write-rare workloads — the opposite of our
access pattern.

### Test Vectors as Table-Driven Tests

```go
var bep42TestVectors = []struct {
    ip       string
    rand     byte
    idPrefix string
    idLast   byte
}{
    {"124.31.75.21", 1, "5fbfbf", 0x01},
    // ...
}
```

The BEP 42 spec provides five test vectors. Encoding them as a struct slice
makes it trivial to add new vectors and ensures the error message includes
the specific input that failed. The test verifies only the 21-bit prefix
(bytes 0–1 fully, byte 2 top 5 bits) — the rest is random by design.
