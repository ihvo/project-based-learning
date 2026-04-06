# BEP 42 — DHT Security Extension

> **Specification:** <https://www.bittorrent.org/beps/bep_0042.html>
> **Status:** Not started
> **Phase:** 8 — DHT Enhancements

---

## 1. Summary

BEP 42 prevents DHT node ID spoofing by tying a node's ID to its external IP
address. Without this, an attacker can choose arbitrary node IDs to position
themselves near any target in the ID space (a Sybil attack), allowing them to
intercept lookups, poison routing tables, or eclipse specific info_hashes.

The defense: the first 21 bits of a valid node ID must match a CRC32-C hash of
the node's IP address. This means:

- A node's ID is constrained by its IP — it can't freely choose where it sits
  in the keyspace
- Given a single IP, there are only ~8 valid node IDs (controlled by a random
  byte)
- Other nodes can verify compliance and deprioritize non-compliant nodes in
  their routing tables
- Nodes learn their external IP from the `ip` field in KRPC responses (also
  used by BEP 24)

This is critical for routing table health. Without ID enforcement, a single
machine can pretend to be thousands of nodes at strategic positions.

---

## 2. Protocol Specification

### 2.1 Node ID Generation Algorithm

Given the node's external IP address, generate a compliant node ID:

```
1. Pick a random byte: rand ∈ [0, 255]
2. Compute the masked IP:
     For IPv4: masked = ip & 0x030f3fff          (4-byte mask)
     For IPv6: masked = ip & 0x0103070f1f3f7fff  (8-byte mask, applied to first 8 bytes)
3. XOR the last byte of the masked value with rand:
     masked[last] ^= rand
4. Compute CRC32-C of the masked bytes:
     hash = crc32c(masked)
5. Set node ID bits:
     id[0] = (hash >> 24) & 0xFF
     id[1] = (hash >> 16) & 0xFF
     id[2] = ((hash >> 8) & 0xF8) | (random_bits & 0x07)
     id[3..18] = random bytes
     id[19] = rand
```

The result:
- Bits 0–20 of the node ID are deterministic (derived from IP + rand)
- Bits 21–152 are random
- Byte 19 (last byte) is `rand`
- For any given IP, there are 256 values of `rand`, but only the top 21 bits
  must match, so effectively ~8 distinct "slots" in the 160-bit ID space

### 2.2 Byte-Level Layout

**IPv4 mask:** `0x030f3fff`

```
IP bytes:    [ a  ,  b  ,  c  ,  d  ]
Mask:        [0x03, 0x0f, 0x3f, 0xff]
Masked:      [a&03, b&0f, c&3f, d^rand]  (XOR rand into last byte)
```

**IPv6 mask:** `0x0103070f1f3f7fff`

```
IP bytes:    [ a  ,  b  ,  c  ,  d  ,  e  ,  f  ,  g  ,  h  ]
Mask:        [0x01, 0x03, 0x07, 0x0f, 0x1f, 0x3f, 0x7f, 0xff]
Masked:      [a&01, b&03, c&07, d&0f, e&1f, f&3f, g&7f, h^rand]
```

**Node ID construction:**

```
┌──────────────────────────────────────────────────────┐
│  Byte 0  │  Byte 1  │  Byte 2  │ Bytes 3-18 │Byte 19│
├──────────┼──────────┼──────────┼────────────┼───────┤
│hash[31:24]│hash[23:16]│hash[15:11]│   random   │ rand  │
│          │          │ | rnd[2:0]│            │       │
└──────────────────────────────────────────────────────┘
         ←— 21 bits from CRC32-C —→
```

### 2.3 Validation Algorithm

To validate another node's ID given its observed IP:

```
1. Extract rand = id[19]
2. Compute masked IP (same as generation, using observed IP)
3. XOR last byte of masked with rand
4. hash = crc32c(masked)
5. Check:
     id[0] == (hash >> 24) & 0xFF
     id[1] == (hash >> 16) & 0xFF
     id[2] & 0xF8 == (hash >> 8) & 0xF8
   If all three match → compliant
```

### 2.4 External IP Discovery

Nodes need to know their own external IP before they can generate a compliant
ID. BEP 42 leverages the `ip` key in KRPC responses:

```
{
  "t": "<txn>",
  "y": "r",
  "ip": "<compact IP+port>",  // 6 bytes (IPv4) or 18 bytes (IPv6)
  "r": { ... }
}
```

The `ip` field contains the requester's address as seen by the responder. This
is the same mechanism described in BEP 24 (Tracker Returns External IP).

**Consensus strategy:** The node should collect `ip` values from multiple
responses and use the most frequently reported IP before generating its final
node ID. A simple majority-of-N approach:

1. Send initial queries with a random (non-compliant) ID
2. Collect `ip` values from responses
3. Once N responses agree (e.g., 3 out of 5), adopt that IP as our external IP
4. Generate a compliant node ID
5. Restart with the new ID (clear routing table, re-bootstrap)

### 2.5 Routing Table Enforcement

When inserting a node into our routing table, verify its ID compliance:

| Validation result | Action |
|-------------------|--------|
| Compliant         | Insert normally |
| Non-compliant     | Insert only if bucket has empty slots; never displace a compliant node |
| Unknown IP        | Accept provisionally (we may not know their IP yet) |

Over time, non-compliant nodes are naturally displaced by compliant ones as
buckets fill up.

### 2.6 KRPC Response: Include `ip` Field

When responding to any KRPC query, include the requester's IP in the response:

```go
resp := Message{
    TxnID: query.TxnID,
    Type:  "r",
    Reply: bencode.Dict{
        "id": bencode.String(d.ID[:]),
        // ... other response fields ...
    },
}
// Add ip field with requester's compact address
resp.Reply["ip"] = bencode.String(compactAddr(requesterAddr))
```

---

## 3. Implementation Plan

### 3.1 `dht/security.go` — New File

Core security functions:

```go
package dht

import (
    "crypto/rand"
    "encoding/binary"
    "hash/crc32"
    "net"
)

// crc32cTable is the Castagnoli CRC32-C table.
var crc32cTable = crc32.MakeTable(crc32.Castagnoli)

var (
    ipv4Mask = [4]byte{0x03, 0x0f, 0x3f, 0xff}
    ipv6Mask = [8]byte{0x01, 0x03, 0x07, 0x0f, 0x1f, 0x3f, 0x7f, 0xff}
)

// GenerateSecureID creates a BEP 42 compliant node ID for the given IP.
func GenerateSecureID(ip net.IP) NodeID {
    var r byte
    rand.Read([]byte{r}[:])  // this is conceptual; actual impl below
    return generateSecureIDWithRand(ip, r)
}

// generateSecureIDWithRand creates a compliant ID with a specific rand value.
// Exposed for deterministic testing.
func generateSecureIDWithRand(ip net.IP, r byte) NodeID {
    masked := maskedIP(ip)
    masked[len(masked)-1] ^= r

    hash := crc32.Checksum(masked, crc32cTable)

    var id NodeID
    id[0] = byte(hash >> 24)
    id[1] = byte(hash >> 16)
    id[2] = byte(hash>>8)&0xF8 | randomBits()&0x07
    rand.Read(id[3:19])
    id[19] = r
    return id
}

// ValidateID checks if a node ID is BEP 42 compliant for the given IP.
func ValidateID(id NodeID, ip net.IP) bool {
    r := id[19]
    masked := maskedIP(ip)
    masked[len(masked)-1] ^= r

    hash := crc32.Checksum(masked, crc32cTable)

    if id[0] != byte(hash>>24) {
        return false
    }
    if id[1] != byte(hash>>16) {
        return false
    }
    if id[2]&0xF8 != byte(hash>>8)&0xF8 {
        return false
    }
    return true
}

// maskedIP applies the BEP 42 mask appropriate for the IP version.
func maskedIP(ip net.IP) []byte {
    if v4 := ip.To4(); v4 != nil {
        out := make([]byte, 4)
        for i := range 4 {
            out[i] = v4[i] & ipv4Mask[i]
        }
        return out
    }
    v6 := ip.To16()
    out := make([]byte, 8)
    for i := range 8 {
        out[i] = v6[i] & ipv6Mask[i]
    }
    return out
}

func randomBits() byte {
    var b [1]byte
    rand.Read(b[:])
    return b[0]
}
```

### 3.2 `dht/security.go` — External IP Tracker

```go
// ExternalIP tracks reported external IPs and determines consensus.
type ExternalIP struct {
    mu      sync.Mutex
    reports map[string]int // IP string → count
    threshold int          // how many agreeing reports needed
}

// NewExternalIP creates a tracker that requires n agreeing reports.
func NewExternalIP(threshold int) *ExternalIP {
    return &ExternalIP{
        reports:   make(map[string]int),
        threshold: threshold,
    }
}

// Report records an external IP observation. Returns the consensus IP
// if threshold is reached, or nil otherwise.
func (e *ExternalIP) Report(ip net.IP) net.IP {
    e.mu.Lock()
    defer e.mu.Unlock()
    key := ip.String()
    e.reports[key]++
    if e.reports[key] >= e.threshold {
        return ip
    }
    return nil
}
```

### 3.3 `dht/node.go` — Integrate Security

Modify the `DHT` struct:

```go
type DHT struct {
    ID         NodeID
    Table      *RoutingTable
    Transport  *Transport
    tokens     map[NodeID]string
    tokensMu   sync.Mutex
    externalIP *ExternalIP     // BEP 42: tracks our external IP
    secure     bool            // true once we have a compliant ID
}
```

**Bootstrap flow changes:**

1. Start with a random ID (non-compliant)
2. During bootstrap pings, extract `ip` from each response
3. Feed IPs to `ExternalIP.Report()`
4. When consensus is reached:
   - Generate a new compliant ID with `GenerateSecureID(consensusIP)`
   - Replace `d.ID`
   - Clear and rebuild the routing table
   - Set `d.secure = true`

**Routing table insert changes:**

```go
func (d *DHT) insertNode(n Node) bool {
    if d.secure {
        if !ValidateID(n.ID, n.Addr.IP) {
            // Non-compliant: only insert if bucket has room
            // Never displace a compliant node
            return d.Table.InsertIfRoom(n)
        }
    }
    return d.Table.Insert(n)
}
```

### 3.4 `dht/table.go` — Priority Insertion

Add `InsertIfRoom` that only inserts when the bucket is not full (never
evicts):

```go
// InsertIfRoom inserts a node only if its bucket has room. Unlike Insert,
// it never displaces existing nodes. Used for non-compliant BEP 42 nodes.
func (rt *RoutingTable) InsertIfRoom(n Node) bool {
    idx := BucketIndex(XOR(rt.own, n.ID))
    if idx < 0 {
        return false
    }
    rt.mu.Lock()
    defer rt.mu.Unlock()

    bucket := rt.buckets[idx]

    // Already present — update.
    for i, existing := range bucket {
        if existing.ID == n.ID {
            bucket = append(bucket[:i], bucket[i+1:]...)
            bucket = append(bucket, n)
            rt.buckets[idx] = bucket
            return true
        }
    }

    if len(bucket) >= bucketSize {
        return false
    }
    rt.buckets[idx] = append(bucket, n)
    return true
}
```

### 3.5 `dht/krpc.go` — Parse and Emit `ip` Field

In `DecodeMessage`, extract the `ip` field:

```go
// In DecodeMessage, after parsing "y":
if ipVal, ok := d["ip"]; ok {
    if ipStr, ok := ipVal.(bencode.String); ok {
        msg.IP = parseCompactAddr([]byte(ipStr))
    }
}
```

Add `IP` to the `Message` struct:

```go
type Message struct {
    TxnID  string
    Type   string
    Method string
    Args   bencode.Dict
    Reply  bencode.Dict
    Error  []any
    IP     *net.UDPAddr  // BEP 42: requester's external address
}
```

In `EncodeMessage`, when building a response, include the `ip` field if
set.

### 3.6 File Summary

| File                    | Change       | Description                                      |
|-------------------------|--------------|--------------------------------------------------|
| `dht/security.go`      | Create       | `GenerateSecureID`, `ValidateID`, `ExternalIP`   |
| `dht/node.go`          | Modify       | Integrate IP tracking, secure ID bootstrap, compliant insert |
| `dht/table.go`         | Modify       | Add `InsertIfRoom` method                        |
| `dht/krpc.go`          | Modify       | Parse/emit `ip` field, add `IP` to `Message`     |

---

## 4. Dependencies

| BEP | Relationship | Notes |
|-----|-------------|-------|
| 5   | Requires    | BEP 42 secures the BEP 5 DHT routing table |
| 24  | Interacts   | The `ip` field in KRPC responses is the same mechanism as BEP 24 |
| 32  | Interacts   | IPv6 uses a different mask; dual-stack nodes need compliant IDs for each family |
| 43  | Interacts   | Read-only nodes still need compliant IDs (they still send queries) |

---

## 5. Testing Strategy

### 5.1 `dht/security_test.go` — ID Generation

| Test Case | Description |
|-----------|-------------|
| `TestGenerateSecureIDIPv4` | Generate an ID for a known IPv4 address. Verify the first 21 bits match the expected CRC32-C output. Verify byte 19 equals `rand`. |
| `TestGenerateSecureIDIPv6` | Same as above for an IPv6 address, using the 8-byte mask. |
| `TestGenerateSecureIDDeterministic` | Call `generateSecureIDWithRand` twice with the same IP and rand. Verify the first 21 bits are identical (bytes 3-18 differ due to random fill). |

### 5.2 `dht/security_test.go` — ID Validation

| Test Case | Description |
|-----------|-------------|
| `TestValidateIDCompliant` | Generate an ID with `GenerateSecureID`, then validate it with `ValidateID`. Must return true. |
| `TestValidateIDWrongIP` | Generate an ID for IP A, validate with IP B. Must return false. |
| `TestValidateIDTampered` | Generate a compliant ID, flip a bit in the first 21 bits, validate. Must return false. |
| `TestValidateIDRandomID` | Validate a completely random ID. Should almost certainly return false. |

### 5.3 `dht/security_test.go` — Known Test Vectors

Use the test vectors from the BEP 42 specification:

| IP            | rand | Expected ID prefix (hex) |
|---------------|------|--------------------------|
| `124.31.75.21` | `1`  | `5fbfbf...01`           |
| `21.75.31.124` | `86` | `5a3ce9...86`           |
| `65.23.51.170` | `22` | `a5d432...22`           |
| `84.124.73.14` | `65` | `1b0321...65`           |
| `43.213.53.83` | `90` | `e56f6c...90`           |

```go
func TestKnownVectors(t *testing.T) {
    vectors := []struct {
        ip     string
        rand   byte
        prefix [3]byte // first 3 bytes of expected ID
        last   byte    // id[19]
    }{
        {"124.31.75.21", 1, [3]byte{0x5f, 0xbf, 0xbf}, 1},
        {"21.75.31.124", 86, [3]byte{0x5a, 0x3c, 0xe9}, 86},
        {"65.23.51.170", 22, [3]byte{0xa5, 0xd4, 0x32}, 22},
        {"84.124.73.14", 65, [3]byte{0x1b, 0x03, 0x21}, 65},
        {"43.213.53.83", 90, [3]byte{0xe5, 0x6f, 0x6c}, 90},
    }
    for _, v := range vectors {
        id := generateSecureIDWithRand(net.ParseIP(v.ip), v.rand)
        if id[0] != v.prefix[0] || id[1] != v.prefix[1] || id[2]&0xF8 != v.prefix[2]&0xF8 {
            t.Errorf("IP %s rand %d: got %x, want prefix %x", v.ip, v.rand, id[:3], v.prefix)
        }
        if id[19] != v.last {
            t.Errorf("IP %s: id[19] = %d, want %d", v.ip, id[19], v.last)
        }
    }
}
```

### 5.4 `dht/security_test.go` — External IP Consensus

| Test Case | Description |
|-----------|-------------|
| `TestExternalIPConsensus` | Report the same IP 3 times with threshold=3. First two return nil, third returns the IP. |
| `TestExternalIPNoConsensus` | Report 3 different IPs with threshold=3. All return nil. |
| `TestExternalIPMixed` | Report IP A twice, IP B once, IP A once more (threshold=3). Third report of A returns consensus. |

### 5.5 `dht/table_test.go` — Priority Insertion

| Test Case | Description |
|-----------|-------------|
| `TestInsertIfRoomNotFull` | Bucket has space — InsertIfRoom succeeds. |
| `TestInsertIfRoomFull` | Bucket is full — InsertIfRoom returns false. |
| `TestInsertIfRoomUpdate` | Node already in bucket — InsertIfRoom updates it. |

### 5.6 `dht/krpc_test.go` — IP Field

| Test Case | Description |
|-----------|-------------|
| `TestEncodeMessageWithIP` | Encode a response with the IP field set. Verify the bencoded output contains the `ip` key. |
| `TestDecodeMessageWithIP` | Decode a bencoded message containing `ip`. Verify `msg.IP` is correctly parsed. |
| `TestDecodeMessageWithoutIP` | Decode a message without `ip`. Verify `msg.IP` is nil. |

### 5.7 Integration

| Test Case | Description |
|-----------|-------------|
| `TestSecureBootstrap` | Start a DHT node, mock bootstrap responses that include consistent `ip` values. Verify the node regenerates a compliant ID after reaching consensus. |
| `TestNonCompliantNodeDeprioritized` | Insert 8 compliant nodes and 1 non-compliant node into a bucket. Verify the non-compliant node is the first to be dropped when a new compliant node arrives. |
