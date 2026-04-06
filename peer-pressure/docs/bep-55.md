# BEP 55 — Holepunch Extension

> Reference: <https://www.bittorrent.org/beps/bep_0055.html>

---

## 1. Summary

BEP 55 enables peers behind NAT to connect to each other using a **relay peer** to coordinate simultaneous connection attempts (holepunching). Without this, two NATed peers that discover each other via DHT or PEX can never connect because neither can accept incoming connections.

**How NAT traversal works at a high level:**

1. Peer A (behind NAT) wants to connect to Peer B (also behind NAT).
2. Both A and B are connected to Relay R (a peer with a public IP, or at least reachable by both).
3. A sends a "please connect me to B" message to R.
4. R forwards a "A wants to connect to you" message to B.
5. A and B simultaneously open connections to each other's public-facing `ip:port` (the address their NAT assigned for the relay connection).
6. Because both sides send SYN packets at roughly the same time, both NATs see an "outgoing" connection and allow the packets through.
7. A TCP connection is established directly between A and B.

This is not guaranteed to work — it depends on NAT type (full cone, restricted cone, symmetric). Symmetric NATs assign different ports per destination, making holepunching fail. But it works often enough to significantly improve peer connectivity.

---

## 2. Protocol Specification

### 2.1 Extension Negotiation

Holepunch is negotiated via BEP 10. A supporting peer includes `ut_holepunch` in its extension handshake `m` dictionary:

```
d
  1:md
    12:ut_holepunchi4e
  e
  1:v25:Peer Pressure 0.1
e
```

### 2.2 PEX Integration (BEP 11)

The `added.f` flags byte in PEX messages includes a holepunch support bit:

| Bit | Flag | Meaning |
|---|---|---|
| 0x01 | `e` | Prefers encrypted connections |
| 0x02 | `s` | Is a seed / upload-only |
| 0x04 | `u` | Supports uTP (BEP 29) |
| 0x08 | `h` | Supports holepunch (BEP 55) |

When a peer is advertised via PEX with the `h` flag, it indicates that peer can participate in holepunch as either initiator or target. This is how peers discover that holepunch is possible.

### 2.3 Message Format

All holepunch messages are carried as BEP 10 extended messages with the `ut_holepunch` sub-ID. The payload is a bencoded dictionary:

```
d
  8:msg_typei0e
  4:porti12345e
  4:addri4:\xc0\xa8\x01\x01e
  ...
e
```

**Common fields:**

| Field | Type | Description |
|---|---|---|
| `msg_type` | integer | 0 = rendezvous, 1 = connect, 2 = error |
| `addr_type` | integer | 0 = IPv4, 1 = IPv6 |
| `addr` | string | Raw IP address bytes (4 bytes for IPv4, 16 for IPv6) |
| `port` | integer | Port number |
| `err_code` | integer | Error code (only in error messages) |

### 2.4 Message Types

#### Rendezvous (msg_type = 0)

Sent by the **initiator** to the **relay**, requesting a connection to the target.

```
Initiator → Relay:
d
  8:msg_typei0e
  9:addr_typei0e
  4:addr4:<target_ipv4_bytes>
  4:porti<target_port>e
e
```

The `addr` and `port` identify the target peer that the initiator wants to reach. The relay must be connected to both the initiator and the target.

#### Connect (msg_type = 1)

Sent by the **relay** to the **target**, informing it that the initiator wants to connect.

```
Relay → Target:
d
  8:msg_typei1e
  9:addr_typei0e
  4:addr4:<initiator_ipv4_bytes>
  4:porti<initiator_port>e
e
```

The `addr` and `port` are the initiator's public-facing address (as seen by the relay). The target should immediately attempt to connect to this address.

#### Error (msg_type = 2)

Sent by the **relay** back to the **initiator** when the rendezvous fails.

```
Relay → Initiator:
d
  8:msg_typei2e
  9:addr_typei0e
  4:addr4:<target_ipv4_bytes>
  4:porti<target_port>e
  8:err_codei1e
e
```

**Error codes:**

| Code | Name | Meaning |
|---|---|---|
| 0 | NoError | No error (reserved / unused in practice) |
| 1 | NoSuchPeer | Relay is not connected to the target peer |
| 2 | NotConnected | Target peer is known but not currently connected |
| 3 | NoSupport | Target peer does not support holepunch |
| 4 | MessageTooLong | The rendezvous message was malformed or too large |

### 2.5 Three-Way Handshake Flow

```
     Initiator (A)                Relay (R)                Target (B)
          │                          │                          │
          │  rendezvous(B's addr)    │                          │
          ├─────────────────────────►│                          │
          │                          │  connect(A's addr)       │
          │                          ├─────────────────────────►│
          │                          │                          │
          │          ┌───────────────┼──────────────────────────┤
          │          │ Both attempt  │  simultaneous connect    │
          │◄─────────┘               │               ┌─────────┘
          │ TCP SYN to B's addr      │               │ TCP SYN to A's addr
          │──────────────────────────┼──────────────►│
          │◄─────────────────────────┼───────────────│
          │          TCP connection established       │
          │◄════════════════════════════════════════►│
          │                                          │
          │      normal BitTorrent handshake         │
          │◄════════════════════════════════════════►│
```

**Timing is critical:** Both sides must attempt the connection within a narrow window (~few seconds) so their SYN packets cross in flight. The relay's `connect` message triggers both sides.

### 2.6 Connection Attempt Details

After the relay sends `connect` to the target:

1. **Initiator** starts connecting to the target's `addr:port` (from the original rendezvous request).
2. **Target** starts connecting to the initiator's `addr:port` (from the connect message).
3. Both set short connection timeouts (e.g., 5 seconds).
4. Whichever connection succeeds first is used. The other is closed.
5. Once a TCP connection is established, a normal BitTorrent handshake follows.

**uTP support:** BEP 55 also works with uTP (BEP 29) connections. The same addr:port is used, but the transport is uTP over UDP instead of TCP. Our implementation can start with TCP only.

### 2.7 Relay Behavior

When a relay receives a rendezvous request:

1. Look up the target `addr:port` in the set of connected peers.
2. If the target is not connected → send `error(NoSuchPeer)` back to initiator.
3. If the target doesn't support holepunch (no `ut_holepunch` in its handshake) → send `error(NoSupport)`.
4. Otherwise → forward a `connect` message to the target with the initiator's public `addr:port`.
5. The relay does **not** wait for the result. Its job is done after forwarding.

**Security consideration:** A relay should rate-limit rendezvous requests to prevent abuse (using the relay as an amplifier for connection floods).

### 2.8 Determining Public Address

The initiator's address (as included in the `connect` message to the target) is the address the relay sees for the initiator's TCP connection — i.e., `conn.RemoteAddr()`. This is the NAT-mapped public address.

Similarly, the target's address in the `rendezvous` message should be the address the initiator saw via PEX or DHT (which is the target's NAT-mapped address as observed by the peer that reported it).

---

## 3. Implementation Plan

### 3.1 Files to Create

**`peer/holepunch.go`** — Message encoding/decoding and orchestration types:

```go
// HolepunchMsgType identifies the holepunch message type.
type HolepunchMsgType uint8

const (
    HolepunchRendezvous HolepunchMsgType = 0
    HolepunchConnect    HolepunchMsgType = 1
    HolepunchError      HolepunchMsgType = 2
)

// HolepunchErrCode identifies holepunch error reasons.
type HolepunchErrCode uint8

const (
    HolepunchErrNone           HolepunchErrCode = 0
    HolepunchErrNoSuchPeer     HolepunchErrCode = 1
    HolepunchErrNotConnected   HolepunchErrCode = 2
    HolepunchErrNoSupport      HolepunchErrCode = 3
    HolepunchErrMessageTooLong HolepunchErrCode = 4
)

// HolepunchMsg represents a BEP 55 holepunch message.
type HolepunchMsg struct {
    MsgType  HolepunchMsgType
    AddrType uint8    // 0 = IPv4, 1 = IPv6
    Addr     net.IP
    Port     uint16
    ErrCode  HolepunchErrCode // only meaningful when MsgType == HolepunchError
}

// EncodeHolepunch serializes a HolepunchMsg to bencoded bytes.
func EncodeHolepunch(msg *HolepunchMsg) []byte

// DecodeHolepunch parses bencoded bytes into a HolepunchMsg.
func DecodeHolepunch(data []byte) (*HolepunchMsg, error)

// NewHolepunchRendezvous creates a rendezvous message to send to a relay.
func NewHolepunchRendezvous(targetIP net.IP, targetPort uint16) *HolepunchMsg

// NewHolepunchConnect creates a connect message to forward to a target.
func NewHolepunchConnect(initiatorIP net.IP, initiatorPort uint16) *HolepunchMsg

// NewHolepunchError creates an error message to send back to an initiator.
func NewHolepunchError(targetIP net.IP, targetPort uint16, code HolepunchErrCode) *HolepunchMsg
```

**`peer/holepunch_test.go`** — Tests for encode/decode and message construction.

**`peer/holepunchrelay.go`** — Relay-side logic for processing rendezvous requests:

```go
// Relay handles incoming rendezvous requests and forwards connect messages.
type Relay struct {
    mu    sync.RWMutex
    peers map[string]*Conn // addr → active connection
}

// HandleRendezvous processes a rendezvous request from an initiator.
// Returns an error HolepunchMsg if the target is unreachable, or nil on success.
func (r *Relay) HandleRendezvous(initiator *Conn, msg *HolepunchMsg) *HolepunchMsg

// RegisterPeer adds a peer connection to the relay's lookup table.
func (r *Relay) RegisterPeer(conn *Conn)

// UnregisterPeer removes a peer connection from the relay's lookup table.
func (r *Relay) UnregisterPeer(conn *Conn)
```

### 3.2 Files to Modify

**`peer/extension.go`** — Advertise `ut_holepunch` in the extension handshake:

Update `NewExtHandshake` to include `ut_holepunch` in the `m` dictionary.

Update `ExtHandshake` to track whether the peer supports holepunch:

```go
type ExtHandshake struct {
    // ... existing fields ...

    // SupportsHolepunch is true if the peer advertised ut_holepunch.
    SupportsHolepunch bool
}
```

**`peer/conn.go`** — Add methods for holepunch message exchange:

```go
// SendHolepunch sends a BEP 55 holepunch extended message.
func (c *Conn) SendHolepunch(msg *HolepunchMsg) error

// HolepunchSubID returns the remote peer's sub-ID for ut_holepunch,
// or 0 if the peer doesn't support it.
func (c *Conn) HolepunchSubID() uint8
```

**`download/pool.go`** — When connecting to a NATed peer fails, attempt holepunch via a relay:

```go
// attemptHolepunch tries to establish a connection to addr via a relay.
func (p *peerPool) attemptHolepunch(ctx context.Context, addr string) (*peer.Conn, error)
```

### 3.3 Key Functions

```go
// EncodeHolepunch serializes a HolepunchMsg to bencoded bytes.
func EncodeHolepunch(msg *HolepunchMsg) []byte

// DecodeHolepunch parses bencoded bytes into a HolepunchMsg.
func DecodeHolepunch(data []byte) (*HolepunchMsg, error)

// HandleRendezvous processes a rendezvous request and forwards to the target.
func (r *Relay) HandleRendezvous(initiator *Conn, msg *HolepunchMsg) *HolepunchMsg

// SimultaneousConnect attempts TCP connections to the target from both sides.
// Returns the first successful connection or an error after timeout.
func SimultaneousConnect(targetAddr string, timeout time.Duration, infoHash, peerID [20]byte) (*Conn, error)
```

### 3.4 Package Placement

All protocol logic lives in `peer/`. The relay logic also lives in `peer/` since it operates on peer connections. The download pool in `download/` gains holepunch awareness for connection establishment.

---

## 4. Dependencies

| BEP | Relationship |
|---|---|
| **BEP 10** | **Required.** Holepunch messages are carried as extended messages |
| **BEP 11** | **Strongly related.** PEX's `added.f` flag `0x08` indicates holepunch support. PEX is the primary way peers discover that holepunch is available for a given peer |
| **BEP 3** | Base protocol — the connection established via holepunch becomes a normal peer wire connection |
| **BEP 29** | uTP — holepunch also works with uTP connections (future enhancement) |
| **BEP 5** | DHT — peers discovered via DHT may need holepunching to connect |

### Internal Dependencies

- `peer.ExtHandshake` — for negotiating `ut_holepunch`
- `peer.NewExtMessage` — for wrapping holepunch payloads in extended messages
- `peer.Conn` — for sending/receiving holepunch messages and looking up peer addresses
- `bencode.Encode` / `bencode.Decode` — for the bencoded message payload
- `download.peerPool` — for integrating holepunch into connection establishment

---

## 5. Testing Strategy

### 5.1 Unit Tests (`peer/holepunch_test.go`)

**`TestEncodeDecodeRendezvous`** — Roundtrip:
- Create a rendezvous message with IPv4 addr + port
- Encode → decode → verify all fields match
- Same for IPv6

**`TestEncodeDecodeConnect`** — Roundtrip for connect messages.

**`TestEncodeDecodeError`** — Roundtrip for each error code:
- `NoSuchPeer` → code 1
- `NotConnected` → code 2
- `NoSupport` → code 3
- `MessageTooLong` → code 4

**`TestDecodeInvalidPayload`** — Malformed input:
- Empty payload → error
- Missing `msg_type` → error
- Missing `addr` → error
- Invalid `addr_type` → error
- `addr` length mismatch (e.g., 4 bytes but `addr_type` = 1/IPv6) → error

**`TestAddrTypeDetection`** — Verify IPv4 vs IPv6 auto-detection:
- 4-byte addr → `addr_type = 0`
- 16-byte addr → `addr_type = 1`

### 5.2 Relay Tests (`peer/holepunch_test.go`)

**`TestRelayRendezvousSuccess`** — Happy path:
- Register peer B with the relay
- Initiator A sends rendezvous for B's address
- Verify relay forwards a connect message to B with A's address
- Verify relay returns nil (no error)

**`TestRelayRendezvousNoSuchPeer`** — Target not connected:
- Relay has no peer at the requested address
- Verify relay returns error with code `NoSuchPeer`

**`TestRelayRendezvousNoSupport`** — Target doesn't support holepunch:
- Register peer B without `ut_holepunch` in its handshake
- Verify relay returns error with code `NoSupport`

**`TestRelayRegisterUnregister`** — Peer lifecycle:
- Register a peer → it's findable
- Unregister it → rendezvous for that peer returns `NoSuchPeer`

### 5.3 Wire-Level Tests

**`TestHolepunchOverConn`** — Use `net.Pipe()`:
- Peer A sends a rendezvous extended message
- Peer B reads and parses it → verify fields
- Peer B sends a connect message back
- Peer A reads and parses it → verify fields

**`TestHolepunchExtHandshake`** — Verify negotiation:
- Two peers exchange extension handshakes with `ut_holepunch`
- Verify both peers see `SupportsHolepunch == true`
- Exchange without `ut_holepunch` → `SupportsHolepunch == false`

### 5.4 Integration Tests

**`TestHolepunchFullFlow`** — End-to-end with three `net.Pipe()` connections:
- Set up three peers: Initiator, Relay, Target
- Initiator and Relay are connected (pipe 1)
- Relay and Target are connected (pipe 2)
- Initiator sends rendezvous to Relay
- Relay forwards connect to Target
- Verify Target receives the correct initiator address
- (The actual simultaneous TCP connect can't be tested with pipes, but the message flow can)

**`TestHolepunchErrorFlow`** — Verify error propagation:
- Initiator sends rendezvous for a non-existent peer
- Relay sends back error with `NoSuchPeer`
- Initiator receives and correctly interprets the error

### 5.5 Edge Cases

**`TestHolepunchRateLimiting`** — If relay implements rate limiting:
- Send 100 rapid rendezvous requests
- Verify the relay doesn't crash and handles them gracefully

**`TestHolepunchIPv6`** — Full flow with IPv6 addresses:
- Use `net.ParseIP("::1")` as addresses
- Verify `addr_type = 1` and 16-byte addr in messages

**`TestHolepunchTimeout`** — SimultaneousConnect with unreachable target:
- Verify it returns an error after the timeout period
- Verify it doesn't leak goroutines
