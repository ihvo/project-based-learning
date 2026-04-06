# BEP 6 — Fast Extension

> Reference: <https://www.bittorrent.org/beps/bep_0006.html>

## Summary

BEP 6 adds five new message types to the peer wire protocol that reduce round-trips and improve connection startup performance:

| Message | ID | Purpose |
|---|---|---|
| Suggest Piece | `0x0D` (13) | Hint that a piece is cached and cheap to serve |
| Have All | `0x0E` (14) | Replace a full bitfield — "I have everything" |
| Have None | `0x0F` (15) | Replace an empty bitfield — "I have nothing" |
| Reject Request | `0x10` (16) | Explicitly refuse a block request |
| Allowed Fast | `0x11` (17) | Grant a piece for download even while choked |

**Why it matters:**

Without BEP 6, a seeder with 10,000 pieces must send a ~1,250-byte bitfield with every bit set. `Have All` replaces that with a single 5-byte message (4-byte length prefix + 1-byte ID). Similarly, `Have None` eliminates the empty bitfield a fresh leecher would send.

`Reject Request` solves a real protocol ambiguity: in base BEP 3, when a peer chokes you, any outstanding requests are silently dropped. The requesting peer doesn't know whether the block was lost or is still in flight. With `Reject Request`, the choking peer explicitly rejects each pending request, letting the requester immediately re-request from another peer instead of waiting for a timeout.

`Allowed Fast` lets a peer download a small deterministic set of pieces even while choked, which is critical for bootstrapping new peers that have nothing to offer yet (and would otherwise be permanently choked by tit-for-tat algorithms).

`Suggest Piece` enables a seeder to steer leechers toward pieces it has cached in memory, improving I/O efficiency.

## Protocol Specification

### Handshake Negotiation

Fast Extension support is advertised in the 8-byte reserved field of the BitTorrent handshake:

```
Byte index:   [0]  [1]  [2]  [3]  [4]  [5]  [6]  [7]
                                                    ^
                                              bit 2 = 0x04
```

A peer sets `reserved[7] |= 0x04` to advertise BEP 6 support. Both peers must set this bit for Fast Extension messages to be used on the connection. If only one side sets it, the connection falls back to base BEP 3 behavior.

This is independent of BEP 10 (Extension Protocol), which uses `reserved[5] |= 0x10` (bit 43). A peer can support both.

### Message Formats

All messages use the standard BEP 3 framing: 4-byte big-endian length prefix followed by the message body.

#### Have All (ID = 0x0E)

Sent instead of a Bitfield message to indicate the sender has every piece. Must be the first message after the handshake (same position as Bitfield). Payload is empty.

```
Wire format (5 bytes total):

 0                   1                   2                   3
 0 1 2 3 4 5 6 7 8 9 0 1 2 3 4 5 6 7 8 9 0 1 2 3 4 5 6 7 8 9 0 1
+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
|                     length = 0x00000001                       |
+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
|  ID = 0x0E    |
+-+-+-+-+-+-+-+-+
```

#### Have None (ID = 0x0F)

Sent instead of a Bitfield to indicate the sender has no pieces. Must be the first message after the handshake. Payload is empty.

```
Wire format (5 bytes total):

 0                   1                   2                   3
 0 1 2 3 4 5 6 7 8 9 0 1 2 3 4 5 6 7 8 9 0 1 2 3 4 5 6 7 8 9 0 1
+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
|                     length = 0x00000001                       |
+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
|  ID = 0x0F    |
+-+-+-+-+-+-+-+-+
```

#### Suggest Piece (ID = 0x0D)

Hints that the sender has the specified piece cached in memory and can serve it quickly. The receiver is not obligated to request it. May be sent at any time after the handshake.

```
Wire format (9 bytes total):

 0                   1                   2                   3
 0 1 2 3 4 5 6 7 8 9 0 1 2 3 4 5 6 7 8 9 0 1 2 3 4 5 6 7 8 9 0 1
+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
|                     length = 0x00000005                       |
+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
|  ID = 0x0D    |                 piece index                   |
+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
|  (cont.)      |
+-+-+-+-+-+-+-+-+
```

Payload: 4-byte big-endian piece index.

#### Reject Request (ID = 0x10)

Sent by a choking peer to explicitly reject a pending block request. The payload mirrors the original Request message exactly.

```
Wire format (17 bytes total):

 0                   1                   2                   3
 0 1 2 3 4 5 6 7 8 9 0 1 2 3 4 5 6 7 8 9 0 1 2 3 4 5 6 7 8 9 0 1
+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
|                     length = 0x0000000D                       |
+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
|  ID = 0x10    |                 piece index                   |
+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
|  (cont.)      |              block offset                     |
+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
|  (cont.)      |              block length                     |
+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
|  (cont.)      |
+-+-+-+-+-+-+-+-+
```

Payload: 12 bytes — same layout as Request (index, begin, length), all big-endian uint32.

When a peer chokes a Fast Extension connection, it must send a Reject Request for every pending request from that peer. The receiver must not re-request rejected blocks from the same peer until unchoked.

#### Allowed Fast (ID = 0x11)

Informs the peer that it may request the specified piece even while choked. Sent after the handshake (typically right after Have All/Have None/Bitfield). The set of allowed fast pieces is deterministic per connection.

```
Wire format (9 bytes total):

 0                   1                   2                   3
 0 1 2 3 4 5 6 7 8 9 0 1 2 3 4 5 6 7 8 9 0 1 2 3 4 5 6 7 8 9 0 1
+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
|                     length = 0x00000005                       |
+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
|  ID = 0x11    |                 piece index                   |
+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
|  (cont.)      |
+-+-+-+-+-+-+-+-+
```

Payload: 4-byte big-endian piece index.

### Allowed Fast Set Algorithm

The allowed fast set is computed deterministically from the peer's IP address and the torrent's infohash. This means both sides of the connection independently compute the same set without any negotiation.

**Algorithm (for IPv4):**

```
Input:
  ip        = peer's IPv4 address (4 bytes)
  infohash  = torrent's 20-byte SHA-1 info hash
  k         = desired set size (typically 10)
  n         = number of pieces in the torrent

Steps:
  1. Mask IP to /24: ip[3] = 0  (e.g., 80.4.4.200 → 80.4.4.0)
  2. x = SHA-1(masked_ip + infohash)    — 24-byte input, 20-byte output
  3. allowed = empty set
  4. While len(allowed) < k:
       For j = 0; j < 5 and len(allowed) < k; j++:
         index = BigEndian.Uint32(x[j*4 : j*4+4]) % n
         If index not in allowed:
           allowed.add(index)
       x = SHA-1(x)                     — re-hash the 20-byte digest
  5. Return allowed
```

**Pseudocode (Go-style):**

```go
func AllowedFastSet(ip net.IP, infohash [20]byte, numPieces, k int) []uint32 {
    // Use IPv4 form; mask to /24
    ip4 := ip.To4()
    if ip4 == nil {
        return nil // IPv6 — BEP 6 only defines the algorithm for IPv4
    }
    masked := make([]byte, 4)
    copy(masked, ip4)
    masked[3] = 0

    // Initial hash: SHA-1(masked_ip + infohash)
    buf := make([]byte, 24)
    copy(buf[0:4], masked)
    copy(buf[4:24], infohash[:])
    x := sha1.Sum(buf)

    allowed := make([]uint32, 0, k)
    seen := make(map[uint32]bool)

    for len(allowed) < k {
        for j := 0; j < 5 && len(allowed) < k; j++ {
            index := binary.BigEndian.Uint32(x[j*4:j*4+4]) % uint32(numPieces)
            if !seen[index] {
                seen[index] = true
                allowed = append(allowed, index)
            }
        }
        x = sha1.Sum(x[:])
    }
    return allowed
}
```

**Edge cases:**
- If `numPieces` ≤ `k`, return all piece indices `[0, 1, ..., numPieces-1]`.
- For IPv6 peers, the BEP does not define the algorithm. Common practice: skip the allowed fast set (treat as empty) or use the last 4 bytes of the IPv6 address.
- The IP used is the peer's *remote* address as seen by the local peer (i.e., from `conn.RemoteAddr()`), not any self-reported address.

### Behavioral Rules

1. **Have All / Have None** must be the first message after the handshake, occupying the same slot as Bitfield. Sending both, or sending one followed by a Bitfield, is a protocol error — disconnect.

2. **Allowed Fast** messages should be sent immediately after the bitfield-equivalent message (Have All / Have None / Bitfield). A peer should send one Allowed Fast message per piece index in the computed set.

3. **Request handling while choked:** On a Fast Extension connection, when a peer is choked it may still request pieces from the allowed fast set. If a choked peer requests a non-allowed piece, the choking peer must respond with Reject Request (not silently drop).

4. **Choke transition:** When a peer chokes a Fast Extension connection, it must send Reject Request for all pending requests that are not in the allowed fast set. Requests for allowed fast pieces may still be served.

5. **Suggest Piece** is advisory — the receiver may ignore it. A peer should limit the rate of Suggest Piece messages to avoid flooding.

### State Machine Changes

On a Fast Extension connection, the per-peer state machine gains these transitions:

```
CHOKED state:
  Can request:   only pieces in allowed_fast_set
  On choke:      receive Reject Request for non-allowed pending requests
  On unchoke:    can request any piece (normal behavior)

UNCHOKED state:
  No change from BEP 3 — all pieces are requestable

Incoming messages:
  Have All     → set all bits in peer's bitfield
  Have None    → clear all bits in peer's bitfield
  Suggest      → add to suggested_pieces list (advisory)
  Reject       → remove from pending_requests, re-queue piece for retry
  Allowed Fast → add to peer's allowed_fast_set (we can request while choked)
```

## Implementation Plan

### Current State

`peer/message.go` already defines `MsgHaveAll = 14` and `MsgHaveNone = 15` but has no constructors, parsers, or the other three message types. The handshake in `peer/conn.go` sets the BEP 10 bit (`reserved[5] |= 0x10`) but not the BEP 6 bit.

### Files to Modify

#### `peer/message.go`

1. **Add message ID constants** for the three missing types:
   ```go
   MsgSuggestPiece  uint8 = 13 // BEP 6
   MsgRejectRequest uint8 = 16 // BEP 6
   MsgAllowedFast   uint8 = 17 // BEP 6
   ```

2. **Add message constructors:**
   ```go
   func NewHaveAll() *Message
   func NewHaveNone() *Message
   func NewSuggestPiece(index uint32) *Message
   func NewRejectRequest(index, begin, length uint32) *Message
   func NewAllowedFast(index uint32) *Message
   ```

3. **Add payload parsers:**
   ```go
   // ParseSuggestPiece extracts the piece index from a Suggest Piece payload.
   func ParseSuggestPiece(payload []byte) (uint32, error)

   // ParseRejectRequest extracts fields from a Reject Request payload.
   // Reuses RequestPayload since the format is identical to Request/Cancel.
   func ParseRejectRequest(payload []byte) (RequestPayload, error)

   // ParseAllowedFast extracts the piece index from an Allowed Fast payload.
   func ParseAllowedFast(payload []byte) (uint32, error)
   ```

   `ParseRejectRequest` can delegate to `ParseRequest` since the payload layout is identical.

#### `peer/conn.go`

1. **Add `SupportsFastExtension()` method** to `Conn`:
   ```go
   func (c *Conn) SupportsFastExtension() bool {
       return c.Reserved[7]&0x04 != 0
   }
   ```

2. **Update `WriteHandshake`** to also set the BEP 6 bit:
   ```go
   buf[27] |= 0x04 // bit 2 of reserved[7] = BEP 6 Fast Extension
   ```

### Files to Create

#### `peer/fast.go`

Contains the Allowed Fast Set algorithm and fast extension helper logic:

```go
package peer

// AllowedFastSet computes the deterministic set of piece indices that a peer
// may request while choked, per BEP 6. Returns up to k indices.
func AllowedFastSet(peerIP net.IP, infohash [20]byte, numPieces, k int) []uint32

// DefaultAllowedFastCount is the standard set size (10 pieces).
const DefaultAllowedFastCount = 10
```

### Key Types

No new exported types are needed. The existing `Message`, `RequestPayload`, and `Conn` types absorb all the new functionality. Internally, `download/` may want to track:

- `allowedFastSet map[uint32]bool` — pieces the peer allows us to request while choked
- `suggestedPieces []uint32` — pieces the peer has suggested

### Integration Points

#### `download/pool.go` / `download/session.go`

The download worker loop needs to:
1. Check `SupportsFastExtension()` after handshake.
2. Send `Have All` or `Have None` instead of a Bitfield when appropriate.
3. Handle incoming `Reject Request` by re-queueing the piece to the `Picker`.
4. Track the allowed fast set and request from it when choked.
5. Handle `Suggest Piece` as a hint for the `Picker`.

#### `download/picker.go`

The piece picker should:
1. Accept `Suggest Piece` hints and give them slight priority.
2. Filter by allowed fast set when the peer is choked.

## Dependencies

| BEP | Relationship |
|---|---|
| BEP 3 (Peer Wire Protocol) | **Required.** Fast Extension extends BEP 3's message set and state machine. All five messages use BEP 3 framing. |
| BEP 10 (Extension Protocol) | **Independent.** Both use reserved handshake bits but don't interact. A connection can support one, both, or neither. |
| BEP 23 (Compact Peers) | **None.** Different protocol layer. |

## Testing Strategy

### Unit Tests — `peer/message_test.go`

1. **`TestNewHaveAll`** — Verify `NewHaveAll()` produces `Message{ID: 14, Payload: nil}`. Round-trip through `WriteMessage` → `ReadMessage` and confirm the wire bytes are `[0x00, 0x00, 0x00, 0x01, 0x0E]`.

2. **`TestNewHaveNone`** — Same as above for `Message{ID: 15}`, wire bytes `[0x00, 0x00, 0x00, 0x01, 0x0F]`.

3. **`TestNewSuggestPiece`** — Verify payload is 4-byte big-endian piece index. Test with index 0, index 42, index `0xFFFFFFFF`.

4. **`TestNewRejectRequest`** — Verify payload is 12-byte `(index, begin, length)`. Confirm identical wire layout to `NewRequest`.

5. **`TestNewAllowedFast`** — Verify payload is 4-byte big-endian piece index.

6. **`TestParseSuggestPiece`** — Parse valid 4-byte payload. Error on 3-byte and 5-byte payloads.

7. **`TestParseRejectRequest`** — Parse valid 12-byte payload. Error on wrong sizes.

8. **`TestParseAllowedFast`** — Parse valid 4-byte payload. Error on wrong sizes.

### Unit Tests — `peer/fast_test.go`

9. **`TestAllowedFastSetKnownVectors`** — BEP 6 provides a test vector:
   - IP: `80.4.4.200`, infohash: all `0xAA`, numPieces: 1313, k: 10
   - Expected set: `{1059, 431, 808, 1217, 287, 376, 1188, 353, 508, 1246}`
   - Verify our function returns exactly this set.

10. **`TestAllowedFastSetSmallTorrent`** — When `numPieces` ≤ `k`, the result should be all indices `[0..numPieces-1]`.

11. **`TestAllowedFastSetDuplicateHandling`** — Use a crafted input where `SHA-1 % numPieces` produces duplicates. Verify the output has exactly `k` unique values.

12. **`TestAllowedFastSetIPv6`** — Confirm the function returns nil/empty for IPv6 addresses.

13. **`TestAllowedFastSetDifferentSubnets`** — Two IPs in the same /24 (e.g., `10.0.0.1` and `10.0.0.200`) must produce the same set. Two IPs in different /24s must produce different sets.

### Unit Tests — `peer/conn_test.go`

14. **`TestSupportsFastExtension`** — Create a `Conn` with `Reserved[7] = 0x04`, verify `SupportsFastExtension()` returns true. With `Reserved[7] = 0x00`, returns false. With `Reserved[7] = 0xFF`, returns true (other bits don't interfere).

15. **`TestHandshakeSetsReservedBits`** — Round-trip a handshake through `WriteHandshake` → `ReadHandshake` and verify both the BEP 10 bit (`reserved[5] & 0x10`) and the BEP 6 bit (`reserved[7] & 0x04`) are set.

### Integration Tests — `peer/fast_integration_test.go`

16. **`TestHaveAllRoundTrip`** — Two peers connected via `net.Pipe`. Sender writes `Have All`, receiver reads and confirms message ID 14, empty payload.

17. **`TestRejectRequestRoundTrip`** — Sender writes `NewRejectRequest(7, 0, 16384)`, receiver reads and parses, confirms all three fields.

18. **`TestAllowedFastHandshakeNegotiation`** — Two peers connect. Both set the BEP 6 reserved bit. Verify both `SupportsFastExtension()` return true. Repeat with only one side setting the bit — verify the non-setting side returns false.
