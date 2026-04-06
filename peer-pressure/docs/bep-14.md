# BEP 14 — Local Peer Discovery (LSD)

> Reference: <https://www.bittorrent.org/beps/bep_0014.html>

## Summary

Local Peer Discovery (LSD) uses UDP multicast to find peers on the same local network (LAN) without requiring a tracker, DHT, or any internet connectivity. When a client is downloading a torrent, it periodically multicasts an announcement containing the torrent's infohash and its listen port. Other clients on the same network that are interested in the same infohash can then connect directly.

**Why it matters:**

- **LAN speed.** Peers on the same LAN can transfer at gigabit speeds instead of being bottlenecked by the internet connection. Without LSD, two machines on the same network might never discover each other (the tracker gives them each other's public IP, and the traffic may or may not hairpin through the router).
- **Offline operation.** LSD works without any internet connectivity at all. If two laptops are on the same WiFi and both have the `.torrent` file, they can find each other and transfer. This is useful in classrooms, conferences, and air-gapped networks.
- **Zero configuration.** No tracker URL, no DHT bootstrap node, no manual IP entry. Just join the multicast group and announce.
- **Complements other discovery.** LSD is the fastest discovery mechanism — a peer appears within 5 minutes (or immediately if both are already running). It works alongside tracker, DHT, and PEX.

**Constraints:**
- LSD must be **disabled for private torrents** (BEP 27), since the tracker controls peer access.
- Some networks block or don't support multicast — LSD silently degrades to doing nothing.

## Protocol Specification

### Multicast Groups

| Protocol | Multicast Address | Port |
|---|---|---|
| IPv4 | `239.192.152.143` | `6771` |
| IPv6 | `ff15::efc0:988f` | `6771` |

`239.192.152.143` is in the Organization-Local Scope range (`239.192.0.0/14`), meaning it stays within the site/organization boundary and is not routed to the internet.

`ff15::efc0:988f` uses IPv6 multicast scope `5` (site-local), providing the same containment. The last 4 bytes (`efc0:988f`) encode `239.192.152.143`.

### Announce Message Format

The announce is a text-based message using HTTP-like syntax (but it is **not** HTTP — it is sent over UDP multicast):

```
BT-SEARCH * HTTP/1.1\r\n
Host: <multicast_address>:<port>\r\n
Port: <listen_port>\r\n
Infohash: <hex_infohash>\r\n
cookie: <unique_cookie>\r\n
\r\n
```

**Field details:**

| Field | Value | Notes |
|---|---|---|
| Request line | `BT-SEARCH * HTTP/1.1` | Fixed. Not a real HTTP request — just the format convention. |
| `Host` | `239.192.152.143:6771` (IPv4) or `[ff15::efc0:988f]:6771` (IPv6) | Must match the multicast group the message was sent to. |
| `Port` | The TCP port this client is listening on for incoming peer connections | Decimal integer, e.g., `6881`. |
| `Infohash` | 40-character lowercase hex encoding of the 20-byte infohash | e.g., `d14a4e0d2b1e3c4f5a6b7c8d9e0f1a2b3c4d5e6f`. One infohash per announce message. |
| `cookie` | A random, unique identifier for this client instance | Used to filter out our own multicast messages. Typically a random alphanumeric string generated at startup. |

**Example message (IPv4):**

```
BT-SEARCH * HTTP/1.1\r\n
Host: 239.192.152.143:6771\r\n
Port: 6881\r\n
Infohash: d14a4e0d2b1e3c4f5a6b7c8d9e0f1a2b3c4d5e6f\r\n
cookie: pp-a1b2c3d4e5f6\r\n
\r\n
```

Total size: ~140-160 bytes. Well within the UDP datagram limit.

**Byte layout on the wire:**

```
Offset  Content
0       "BT-SEARCH * HTTP/1.1\r\n"                          (23 bytes)
23      "Host: 239.192.152.143:6771\r\n"                    (30 bytes)
53      "Port: 6881\r\n"                                    (12 bytes)
65      "Infohash: d14a4e0d2b1e3c4f5a6b7c8d9e0f1a2b3c4d5e6f\r\n"  (51 bytes)
116     "cookie: pp-a1b2c3d4e5f6\r\n"                       (27 bytes)
143     "\r\n"                                               (2 bytes)
        ─────────────────────────────
        ~145 bytes total
```

### Parsing an Announce

When receiving a UDP datagram on port 6771:

1. Split the datagram on `\r\n` to get lines.
2. First line must be exactly `BT-SEARCH * HTTP/1.1`. If not, discard.
3. Parse remaining lines as `Key: Value` pairs (case-insensitive key matching).
4. Extract `Port`, `Infohash`, and `cookie`.
5. **Cookie check:** If `cookie` matches our own cookie, discard (it's our own announcement reflected back).
6. **Infohash check:** If we are not active on the given infohash, discard.
7. Otherwise, the sender's IP (from the UDP source address) + `Port` value gives us a connectable peer address.

### Timing and Rate Limiting

- **Announce interval:** Every **~5 minutes** (300 seconds) per active torrent. Jitter by ±30 seconds to avoid synchronized bursts.
- **Startup announce:** Send an immediate announce when starting a new torrent, then start the 5-minute timer.
- **Multiple torrents:** Each torrent gets its own announce message with its own infohash. If running 10 torrents, send 10 separate multicast datagrams per interval. Stagger them slightly (e.g., 100ms apart) to avoid packet loss.
- **TTL:** Set the multicast TTL to 1 or a small value to prevent announcements from leaking beyond the local network segment.

### State Machine

```
                    ┌─────────────┐
                    │   STOPPED   │
                    └──────┬──────┘
                           │  Start(infohash, port)
                           ▼
                    ┌─────────────┐
         ┌────────→│  ANNOUNCING  │←────────┐
         │         └──────┬──────┘          │
         │                │                 │
         │   timer fires  │   received      │
         │   (5 min)      │   announce      │
         │                ▼                 │
         │         ┌─────────────┐          │
         │         │   SENDING   │          │
         │         │  multicast  │──────────┘
         │         └─────────────┘
         │
         │  Stop(infohash)
         │
         └── (removed from active set)
```

Per-connection (on receiving an announce from another peer):
```
Receive datagram
  → Parse announce
  → Cookie matches ours? → discard
  → Infohash not active? → discard
  → Otherwise: emit peer address (source_ip, port) to peer discovery channel
```

### IPv4 and IPv6 Dual-Stack

A client should listen on and announce to both the IPv4 and IPv6 multicast groups if the system supports it. The `Host` header must match the group the message was sent to:
- IPv4: `Host: 239.192.152.143:6771`
- IPv6: `Host: [ff15::efc0:988f]:6771`

If the system does not support IPv6, only use the IPv4 group. Do not fail — LSD is best-effort.

## Implementation Plan

### Package: `lsd/`

Create a new `lsd/` package. LSD is self-contained — it doesn't depend on any BitTorrent wire protocol state, just infohashes and port numbers.

#### `lsd/announce.go`

Message encoding and parsing.

**Key types:**

```go
// Announce represents a parsed LSD announcement.
type Announce struct {
    Host     string // multicast address from Host header
    Port     uint16 // TCP listen port
    Infohash [20]byte
    Cookie   string
}
```

**Key functions:**

```go
// FormatAnnounce serializes an Announce into the wire format (BT-SEARCH message).
func FormatAnnounce(a *Announce) []byte

// ParseAnnounce parses a raw UDP datagram into an Announce.
// Returns an error if the message is malformed or not a BT-SEARCH message.
func ParseAnnounce(data []byte) (*Announce, error)
```

#### `lsd/service.go`

The multicast listener and announcer lifecycle.

**Key types:**

```go
// Service manages LSD multicast announcing and listening.
type Service struct {
    listenPort uint16             // our TCP listen port for incoming peer connections
    cookie     string             // our unique cookie for filtering own announcements
    active     map[[20]byte]bool  // infohashes we're currently active on (protected by mu)
    mu         sync.RWMutex
    peers      chan<- Peer        // discovered peers are sent here
}

// Peer represents a discovered LSD peer.
type Peer struct {
    Addr     string   // "ip:port" ready for net.Dial
    Infohash [20]byte
}
```

**Key functions:**

```go
// New creates a new LSD service. Discovered peers are sent to the peers channel.
func New(listenPort uint16, peers chan<- Peer) *Service

// AddInfohash registers a torrent for LSD announcing.
func (s *Service) AddInfohash(infohash [20]byte)

// RemoveInfohash stops announcing a torrent.
func (s *Service) RemoveInfohash(infohash [20]byte)

// Run starts the multicast listener and announcer goroutines.
// Blocks until ctx is cancelled. Returns nil on clean shutdown.
func (s *Service) Run(ctx context.Context) error
```

Internal goroutines:
- **Listener:** Joins the multicast group, reads datagrams in a loop, parses them, filters by cookie and active infohashes, emits to the `peers` channel.
- **Announcer:** On a 5-minute ticker (with jitter), iterates active infohashes and sends one multicast datagram per infohash.

### Files to Modify

#### `download/session.go`

Add an `EnableLSD bool` field to `Config`. When enabled and the torrent is not private, start an `lsd.Service` alongside the download and feed discovered peers into the peer pool.

#### `cmd/peer-pressure/main.go`

Add a `--lsd` flag (default: on) to the `download` subcommand. Pass it through to the download `Config`.

### Network Details (Go-specific)

**Joining multicast (IPv4):**

```go
addr, _ := net.ResolveUDPAddr("udp4", "239.192.152.143:6771")
conn, _ := net.ListenMulticastUDP("udp4", nil, addr) // nil = all interfaces
conn.SetReadBuffer(4096)
```

**Sending multicast (IPv4):**

```go
dst, _ := net.ResolveUDPAddr("udp4", "239.192.152.143:6771")
conn, _ := net.DialUDP("udp4", nil, dst)
conn.Write(FormatAnnounce(&announce))
```

**Cookie generation:** Generate once at client startup using `crypto/rand`:

```go
func generateCookie() string {
    b := make([]byte, 8)
    rand.Read(b)
    return "pp-" + hex.EncodeToString(b) // "pp-" prefix + 16 hex chars
}
```

## Dependencies

| BEP | Relationship |
|---|---|
| BEP 3 (Peer Wire Protocol) | **Required.** Discovered peers connect via the standard TCP peer wire protocol. |
| BEP 27 (Private Torrents) | **Constraint.** LSD must be disabled for private torrents. |
| BEP 5 (DHT) | **Complementary.** Both are trackerless peer discovery. DHT covers the internet; LSD covers the LAN. |
| BEP 11 (PEX) | **Complementary.** LSD-discovered peers can be shared via PEX (and vice versa). |
| BEP 10 (Extension Protocol) | **None.** LSD does not use the extension protocol — it's a separate UDP multicast mechanism. |

## Testing Strategy

### Unit Tests — `lsd/announce_test.go`

1. **`TestFormatAnnounce`** — Format an announce with infohash `aabbcc...` and port `6881`. Verify the output starts with `BT-SEARCH * HTTP/1.1\r\n`, contains `Host: 239.192.152.143:6771`, `Port: 6881`, the correct hex infohash, the cookie, and ends with `\r\n\r\n`.

2. **`TestParseAnnounce`** — Parse a well-formed announce string. Verify all fields (port, infohash bytes, cookie) are extracted correctly.

3. **`TestParseAnnounceRoundTrip`** — Format an announce, parse it back, compare all fields to the original.

4. **`TestParseAnnounceBadRequestLine`** — Input with `GET / HTTP/1.1` instead of `BT-SEARCH * HTTP/1.1`. Verify error.

5. **`TestParseAnnounceMissingPort`** — Omit the `Port:` header. Verify error.

6. **`TestParseAnnounceMissingInfohash`** — Omit the `Infohash:` header. Verify error.

7. **`TestParseAnnounceInvalidInfohashLength`** — Provide a 38-character hex infohash (not 40). Verify error.

8. **`TestParseAnnounceInvalidInfohashHex`** — Provide `Infohash: zzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzz`. Verify error.

9. **`TestParseAnnounceCaseInsensitiveHeaders`** — Use `port:`, `INFOHASH:`, `Cookie:` in mixed case. Verify successful parsing (headers should be case-insensitive).

10. **`TestParseAnnounceExtraHeaders`** — Include unknown headers (e.g., `X-Custom: foo`). Verify they are ignored and parsing succeeds.

### Unit Tests — `lsd/service_test.go`

11. **`TestCookieFiltering`** — Create a service with cookie `"pp-abc123"`. Feed it a datagram with the same cookie. Verify no peer is emitted. Feed it a datagram with a different cookie. Verify a peer is emitted.

12. **`TestInfohashFiltering`** — Register infohash A. Feed an announce for infohash A — peer emitted. Feed an announce for infohash B — no peer emitted.

13. **`TestAddRemoveInfohash`** — Add infohash A, verify it's in the active set. Remove it, verify it's gone. Add it again, verify it's back. This tests the `AddInfohash` / `RemoveInfohash` lifecycle.

14. **`TestPeerAddressFormat`** — Receive an announce from source IP `192.168.1.5` with `Port: 51413`. Verify the emitted `Peer.Addr` is `"192.168.1.5:51413"`.

### Integration Tests — `lsd/integration_test.go`

15. **`TestMulticastRoundTrip`** — Start two `Service` instances on the loopback interface, each with a different cookie. Service A registers infohash X. Service B registers infohash X. Wait for the announce interval (or send a manual announce). Verify Service B receives a peer from Service A and vice versa.

16. **`TestMulticastOwnAnnounceFiltered`** — Start one `Service`. Register infohash X. Trigger an announce. Verify the service does NOT emit a peer from its own announcement (cookie filtering).

17. **`TestMulticastShutdown`** — Start a `Service` with a cancellable context. Cancel the context. Verify `Run()` returns nil without hanging.

18. **`TestMultipleInfohashes`** — Register 3 infohashes. Trigger an announce cycle. Verify 3 separate datagrams are sent (one per infohash), each with the correct infohash.
