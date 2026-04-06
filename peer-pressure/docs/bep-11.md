# BEP 11 — Peer Exchange (PEX)

> Reference: <https://www.bittorrent.org/beps/bep_0011.html>

## Summary

Peer Exchange (PEX) lets connected peers share peer addresses with each other, reducing dependence on centralized trackers and DHT for peer discovery. Once two peers are connected, they periodically exchange diffs of their known peer lists using the Extension Protocol (BEP 10).

**Why it matters:**

- **Tracker resilience.** If a tracker goes down, peers that already have connections can still discover new peers through PEX. The swarm becomes self-sustaining.
- **Faster swarm growth.** A new peer that connects to just one seed can quickly discover dozens of other peers without waiting for the next tracker announce cycle (which might be 30+ minutes apart).
- **Lower tracker load.** Peers don't need to re-announce as frequently since they're discovering each other directly.
- **Complements DHT.** DHT is great for initial bootstrap but is slow (multiple round-trips through the Kademlia lookup). PEX gives immediate results from peers you already have TCP connections with.

**Constraints:**
- PEX must be **disabled for private torrents** (BEP 27). Private torrents rely on tracker-controlled peer lists; PEX would bypass that control.
- PEX messages are capped at **50 added + 50 dropped peers** per message to prevent flooding.
- Messages are sent at **~60-second intervals** to avoid excessive bandwidth.

## Protocol Specification

### Extension Negotiation

PEX uses the Extension Protocol (BEP 10). During the extension handshake, each peer advertises support by including `ut_pex` in the `m` dictionary:

```
Extension Handshake (BEP 10, sub-ID 0):
  d
    1:md
      6:ut_pexi2e       ← "I'll use extended message ID 2 for PEX"
    e
    1:v18:Peer Pressure 0.1
  e
```

The numeric value (`2` here) is the message ID the *sender* wants the *receiver* to use when sending PEX messages back. Each side independently assigns its own ID. If a peer's extension handshake does not include `ut_pex`, that peer does not support PEX.

### Message Format

PEX messages are sent as BEP 10 extended messages:

```
Wire framing:
  [4 bytes]  length prefix (big-endian uint32)
  [1 byte]   message ID = 20 (MsgExtended)
  [1 byte]   extended sub-ID = peer's ut_pex ID from handshake
  [N bytes]  bencoded payload dictionary
```

The bencoded payload dictionary has these keys:

| Key | Type | Description |
|---|---|---|
| `added` | byte string | Compact IPv4 peers added since last PEX message. 6 bytes per peer: 4 bytes IP + 2 bytes port (big-endian). |
| `added.f` | byte string | One flag byte per peer in `added`. Same length as `added / 6`. |
| `dropped` | byte string | Compact IPv4 peers removed since last PEX message. 6 bytes each. |
| `added6` | byte string | Compact IPv6 peers added. 18 bytes per peer: 16 bytes IP + 2 bytes port. |
| `added6.f` | byte string | One flag byte per peer in `added6`. Same length as `added6 / 18`. |
| `dropped6` | byte string | Compact IPv6 peers removed. 18 bytes each. |

**Flag byte bit layout (`added.f` / `added6.f`):**

```
Bit 0 (0x01): prefers_encryption   — peer indicated it prefers encrypted connections
Bit 1 (0x02): is_seed              — peer is a seeder (has all pieces)
Bit 2 (0x04): supports_utp         — peer supports uTP (BEP 29)
Bit 3 (0x08): supports_holepunch   — peer supports holepunch (BEP 55)
Bit 4 (0x10): reachable            — peer is connectable (not behind NAT/firewall)
Bits 5-7:     reserved (0)
```

**Example payload (bencoded):**

```
d
  5:added 12:<4-byte-ip><2-byte-port><4-byte-ip><2-byte-port>
  7:added.f 2:<flag1><flag2>
  7:dropped 6:<4-byte-ip><2-byte-port>
  6:added6 0:
  8:added6.f 0:
  8:dropped6 0:
e
```

### Compact Peer Encoding

IPv4 compact peer (6 bytes):
```
Offset  Length  Field
0       4       IPv4 address (network byte order)
4       2       TCP port (big-endian)
```

IPv6 compact peer (18 bytes):
```
Offset  Length  Field
0       16      IPv6 address (network byte order)
16      2       TCP port (big-endian)
```

These are the same compact formats used by BEP 23 (tracker compact peers) and BEP 5 (DHT `nodes` / `nodes6`).

### Diff Tracking

PEX messages contain **diffs** — only the changes since the last PEX message sent to that peer. Each side of the connection independently tracks what it has already told the other side.

**State per connection:**

```
last_sent_set:  set of (ip, port) pairs included in the most recent PEX message
current_set:    set of all peers we currently know about for this torrent
```

**Computing a diff:**

```
added   = current_set - last_sent_set
dropped = last_sent_set - current_set
```

After sending, `last_sent_set = current_set` (snapshot at send time).

### Rate Limiting and Caps

- **Interval:** Send at most one PEX message per ~60 seconds per connection.
- **Cap per message:** At most 50 added peers and 50 dropped peers per message. If the diff is larger, truncate (prefer recently seen peers in `added`, oldest disconnects in `dropped`).
- **First message:** The first PEX message after the extension handshake may include up to 50 peers from the current set (since `last_sent_set` is empty, everything is "added").
- **No PEX flooding:** Do not forward PEX-learned peers in the very next PEX message. Wait until the peer has been verified (successful handshake) before including it in outgoing PEX.

### Behavioral Rules

1. **Private torrents:** If `info.private = 1` (BEP 27), do not advertise `ut_pex` in the extension handshake, do not send PEX messages, and ignore any received PEX messages.

2. **Seed-to-seed:** Seeds should not send each other addresses of other seeds (they don't need them). Only include leechers in PEX when sending to a seed.

3. **Initial exchange:** After the extension handshake, wait ~60 seconds before sending the first PEX message. Some implementations send it sooner (30 seconds) for faster bootstrapping.

4. **Connection close:** When a peer disconnects, add it to `dropped` in the next PEX message to each remaining peer.

5. **Do not trust blindly:** PEX-received addresses should be treated like tracker-received addresses — attempt connection, verify infohash in handshake. Do not assume they are valid.

### Message Flow

```
Peer A                                           Peer B
  |                                                 |
  |--- Extension Handshake {m: {ut_pex: 2}} -----→ |
  |←-- Extension Handshake {m: {ut_pex: 1}} ------- |
  |                                                 |
  |          (60 seconds pass)                      |
  |                                                 |
  |--- Extended(sub=1) {added: C,D  dropped: } --→ |
  |←-- Extended(sub=2) {added: E,F  dropped: } --- |
  |                                                 |
  |          (60 seconds pass)                      |
  |                                                 |
  |--- Extended(sub=1) {added: G  dropped: C} ---→ |
  |←-- Extended(sub=2) {added:    dropped: E} ---- |
  |                                                 |
```

Note: Peer A sends using sub-ID 1 (the ID that Peer B assigned), and Peer B sends using sub-ID 2 (the ID that Peer A assigned).

## Implementation Plan

### Package: `pex/`

Create a new `pex/` package containing the PEX message codec and diff tracker. This keeps PEX logic self-contained, following the same pattern as `dht/` and `magnet/`.

#### `pex/message.go`

Encode and decode PEX payloads.

**Key types:**

```go
// CompactPeer holds a parsed peer address from compact encoding.
type CompactPeer struct {
    IP   net.IP
    Port uint16
}

// Flags holds the per-peer flag bits from added.f / added6.f.
type Flags uint8

const (
    FlagEncryption Flags = 0x01
    FlagSeed       Flags = 0x02
    FlagUTP        Flags = 0x04
    FlagHolepunch  Flags = 0x08
    FlagReachable  Flags = 0x10
)

// PeerEntry pairs a compact peer with its flags.
type PeerEntry struct {
    Peer  CompactPeer
    Flags Flags
}

// PEXMessage represents a decoded PEX payload.
type PEXMessage struct {
    Added    []PeerEntry
    Dropped  []CompactPeer
    Added6   []PeerEntry
    Dropped6 []CompactPeer
}
```

**Key functions:**

```go
// Encode serializes a PEXMessage into a bencoded byte slice suitable for
// embedding in a BEP 10 extended message payload.
func (m *PEXMessage) Encode() []byte

// Decode parses a bencoded PEX payload into a PEXMessage.
func Decode(data []byte) (*PEXMessage, error)

// EncodeCompactIPv4 packs a list of CompactPeer into the 6-byte-per-peer format.
func EncodeCompactIPv4(peers []CompactPeer) []byte

// DecodeCompactIPv4 unpacks the 6-byte-per-peer format into CompactPeer values.
func DecodeCompactIPv4(data []byte) ([]CompactPeer, error)

// EncodeCompactIPv6 packs into 18-byte-per-peer format.
func EncodeCompactIPv6(peers []CompactPeer) []byte

// DecodeCompactIPv6 unpacks the 18-byte-per-peer format.
func DecodeCompactIPv6(data []byte) ([]CompactPeer, error)
```

#### `pex/tracker.go`

Manages diff state for one peer connection.

**Key types:**

```go
// DiffTracker tracks the set of known peers and computes diffs for PEX messages.
type DiffTracker struct {
    mu          sync.Mutex
    current     map[string]PeerEntry  // key: "ip:port"
    lastSent    map[string]PeerEntry  // snapshot at last PEX send
}
```

**Key functions:**

```go
// NewDiffTracker creates a DiffTracker with an empty initial state.
func NewDiffTracker() *DiffTracker

// AddPeer records a peer in the current set.
func (t *DiffTracker) AddPeer(entry PeerEntry)

// RemovePeer removes a peer from the current set.
func (t *DiffTracker) RemovePeer(addr CompactPeer)

// Diff computes the added/dropped sets since the last call to Diff.
// Caps results at maxAdded and maxDropped. Updates lastSent.
func (t *DiffTracker) Diff(maxAdded, maxDropped int) *PEXMessage
```

### Files to Modify

#### `peer/extension.go`

Update `NewExtHandshake` to include `ut_pex` in the `m` dictionary when PEX is enabled. Add a constant:

```go
const ExtNamePEX = "ut_pex"
```

#### `peer/conn.go`

Add a helper method to check if the remote peer supports PEX:

```go
// PeerExtID returns the extended message sub-ID the remote peer assigned
// for the given extension name. Returns 0 if the peer doesn't support it.
func (c *Conn) PeerExtID(name string) uint8
```

#### `download/pool.go`

The peer pool worker goroutine needs to:
1. Create a `DiffTracker` per connection.
2. Start a 60-second ticker that calls `Diff()` and sends PEX messages.
3. Handle incoming PEX messages by adding discovered peers to the pool's address list.
4. Skip PEX for private torrents.

#### `download/session.go`

Add a `Private bool` field to `Config` or read it from the `Torrent` struct to gate PEX.

## Dependencies

| BEP | Relationship |
|---|---|
| BEP 10 (Extension Protocol) | **Required.** PEX messages are carried inside BEP 10 extended messages. The `ut_pex` name is negotiated in the extension handshake. |
| BEP 3 (Peer Wire Protocol) | **Required.** PEX operates over established peer connections. |
| BEP 23 (Compact Peers) | **Shared format.** PEX uses the same 6-byte and 18-byte compact peer encoding. |
| BEP 5 (DHT) | **Complementary.** Both are peer discovery mechanisms. Peers found via DHT can be shared via PEX and vice versa. |
| BEP 27 (Private Torrents) | **Constraint.** PEX must be disabled for private torrents. |
| BEP 9 (Metadata Exchange) | **Independent.** Both use BEP 10 but don't interact directly. They share the extension handshake. |

## Testing Strategy

### Unit Tests — `pex/message_test.go`

1. **`TestEncodeDecodeCompactIPv4`** — Encode two IPv4 peers (`192.168.1.1:6881`, `10.0.0.1:51413`), decode back, verify IP and port match.

2. **`TestEncodeDecodeCompactIPv6`** — Encode an IPv6 peer (`[::1]:6881`), decode back, verify match.

3. **`TestDecodeCompactIPv4InvalidLength`** — Pass a byte slice whose length is not a multiple of 6. Verify error is returned.

4. **`TestDecodeCompactIPv6InvalidLength`** — Pass a byte slice whose length is not a multiple of 18. Verify error is returned.

5. **`TestPEXMessageEncode`** — Build a `PEXMessage` with 2 added IPv4 peers (with flags), 1 dropped IPv4, and 1 added IPv6 peer. Encode it and verify the bencoded output contains all expected keys with correct byte lengths.

6. **`TestPEXMessageDecode`** — Construct raw bencoded bytes for a PEX message with known peers. Decode and verify all fields.

7. **`TestPEXMessageDecodeEmpty`** — Decode a PEX message where all lists are empty byte strings. Verify no error and empty slices.

8. **`TestPEXMessageDecodeAddedFlagsMismatch`** — Provide `added` with 2 peers (12 bytes) but `added.f` with 1 byte. Verify error is returned (length mismatch).

9. **`TestFlagBits`** — Verify each `Flag*` constant maps to the correct bit. `FlagSeed | FlagUTP` should equal `0x06`.

### Unit Tests — `pex/tracker_test.go`

10. **`TestDiffFirstMessage`** — Add 3 peers to a fresh tracker. Call `Diff(50, 50)`. Verify `Added` has all 3, `Dropped` is empty.

11. **`TestDiffSubsequentMessage`** — Add 3 peers, call `Diff`. Add peer D, remove peer A. Call `Diff` again. Verify `Added` = `[D]`, `Dropped` = `[A]`.

12. **`TestDiffNoDelta`** — Add 3 peers, call `Diff`. Call `Diff` again without changes. Verify both `Added` and `Dropped` are empty.

13. **`TestDiffCapAdded`** — Add 60 peers to a fresh tracker. Call `Diff(50, 50)`. Verify `len(Added) == 50`.

14. **`TestDiffCapDropped`** — Add 60 peers, call `Diff`. Remove all 60. Call `Diff(50, 50)`. Verify `len(Dropped) == 50`.

15. **`TestDiffConcurrentAccess`** — Run `AddPeer` and `Diff` concurrently from separate goroutines. Verify no race (run with `-race`).

### Integration Tests — `pex/integration_test.go`

16. **`TestPEXRoundTrip`** — Two peers connected via `net.Pipe`. Both perform the BEP 10 extension handshake advertising `ut_pex`. Peer A sends a PEX message with 2 added peers. Peer B reads the extended message, decodes the PEX payload, and verifies the peer addresses.

17. **`TestPEXDisabledForPrivateTorrents`** — Simulate a connection for a private torrent. Verify that the extension handshake does NOT include `ut_pex` in the `m` dictionary.

18. **`TestPEXDiffTrackerOverTime`** — Simulate three PEX intervals: initial (5 peers added), middle (2 added, 1 dropped), final (no change). Verify each `Diff()` output matches expectations.
