# BEP 14 — Local Peer Discovery (LSD)

## What We Built

A multicast UDP service that discovers BitTorrent peers on the local network
without needing a tracker, DHT, or any internet connectivity.

**Files:**
- `lsd/announce.go` — message encoding/parsing
- `lsd/service.go` — multicast listener + periodic announcer
- `lsd/lsd_test.go` — 16 tests

## BitTorrent Concepts

### Why LSD Exists

Every other peer discovery mechanism — trackers, DHT, PEX — requires internet.
LSD solves a different problem: *"Two laptops on the same WiFi both have the
same torrent. How do they find each other?"*

Without LSD, they'd both announce to the tracker, which returns their public
IPs. The traffic then might hairpin through the router (or fail entirely if
NAT doesn't support it). With LSD, they find each other in under 5 minutes via
a single multicast packet on the LAN.

### Multicast UDP — The Key Idea

Normal UDP is point-to-point: you send a datagram to one specific IP. **Multicast**
is one-to-many: you send to a *group address* and every machine that joined
that group receives it.

```
Unicast:    [Client] ──packet──→ [Server]

Multicast:  [Client] ──packet──→ [Group 239.192.152.143]
                                       ├──→ [Peer A]
                                       ├──→ [Peer B]
                                       └──→ [Peer C]
```

LSD uses **IPv4 group `239.192.152.143:6771`** (organization-local scope — the
OS/network guarantees it won't leave the LAN). IPv6 uses `ff15::efc0:988f`
(site-local scope 5).

### The Announce Message

Unlike the binary wire protocol, LSD uses a **text format** that looks like HTTP
but isn't:

```
BT-SEARCH * HTTP/1.1\r\n
Host: 239.192.152.143:6771\r\n
Port: 6881\r\n
Infohash: d14a4e0d2b1e3c4f5a6b7c8d9e0f1a2b3c4d5e6f\r\n
cookie: pp-a1b2c3d4e5f6\r\n
\r\n
```

~145 bytes — fits easily in one UDP datagram (no fragmentation worries).

### Cookie: Filtering Your Own Echoes

When you send a multicast packet, **you receive it back too** (you're a member of
the group). The `cookie` field — a random string generated at startup — lets you
discard your own reflections.

### Timing

- Announce every **5 minutes** (±30s jitter to prevent synchronized bursts)
- One datagram per active torrent
- Immediate announce when a new torrent starts
- **TTL = 1** — packets stay on the local network segment

### Private Torrents

LSD must be disabled for private torrents (BEP 27). The tracker controls peer
access for private swarms — announcing on multicast would leak the infohash to
anyone on the LAN.

## Go Idioms Used

### `net.ListenMulticastUDP` — Joining a Multicast Group

Go's `net` package makes multicast surprisingly easy:

```go
addr, _ := net.ResolveUDPAddr("udp4", "239.192.152.143:6771")
conn, _ := net.ListenMulticastUDP("udp4", nil, addr)
```

`nil` for the interface means "all interfaces." Under the hood, this:
1. Creates a UDP socket
2. Sets `SO_REUSEADDR` (multiple processes can bind the same port)
3. Calls `IP_ADD_MEMBERSHIP` to join the multicast group
4. Returns a `*net.UDPConn` you read from like any UDP socket

### `net.DialUDP` for Sending Multicast

Sending is just a regular UDP dial to the multicast address:

```go
dst, _ := net.ResolveUDPAddr("udp4", "239.192.152.143:6771")
conn, _ := net.DialUDP("udp4", nil, dst)
conn.Write(data)
```

The OS handles the multicast routing — you don't need to enumerate interfaces.

### `crypto/rand` + `math/big` for Jitter

For non-security random numbers with precise ranges, `math/big.Int` works with
`crypto/rand`:

```go
func jitterDuration(max time.Duration) time.Duration {
    n, _ := rand.Int(rand.Reader, big.NewInt(int64(2*max)))
    return time.Duration(n.Int64()) - max  // range: [-max, +max)
}
```

This avoids importing `math/rand/v2` for a single call. For the cookie, we use
`crypto/rand.Read` directly (random bytes → hex string).

### `sync.RWMutex` for Hot-Path Reads

The `active` infohash map is read on every incoming datagram (hot path) but
written rarely (add/remove torrent). `sync.RWMutex` lets multiple listener
goroutines read concurrently:

```go
s.mu.RLock()               // multiple readers OK
interested := s.active[ih]
s.mu.RUnlock()
```

Writers (AddInfohash/RemoveInfohash) take the exclusive lock:

```go
s.mu.Lock()
s.active[ih] = true
s.mu.Unlock()
```

### Context-Driven Shutdown

The service uses `context.Context` for clean lifecycle management:

```go
func (s *Service) Run(ctx context.Context) error {
    // ... setup ...
    <-ctx.Done()
    conn.Close()  // unblocks ReadFromUDP in the listener goroutine
    wg.Wait()     // wait for all goroutines to exit
    return nil
}
```

Closing the connection from outside the read loop is the standard Go pattern
for unblocking a blocked `Read` call. Without it, the listener goroutine would
hang forever.

### `net.JoinHostPort` — Avoiding String Formatting Bugs

When constructing `"ip:port"` strings, always use `net.JoinHostPort`:

```go
addr := net.JoinHostPort(src.IP.String(), fmt.Sprintf("%d", a.Port))
// Handles IPv6 correctly: "[::1]:6881" instead of "::1:6881"
```

Raw `fmt.Sprintf("%s:%d", ip, port)` breaks for IPv6 addresses because the
colons in the IP get confused with the port separator.

### Text Protocol Parsing with `strings`

Unlike our binary wire protocol (which uses `encoding/binary`), LSD's
HTTP-like format is parsed with string splitting:

```go
lines := strings.Split(string(data), "\r\n")
// First line: request line
// Rest: "Key: Value" pairs
idx := strings.IndexByte(line, ':')
key := strings.ToLower(strings.TrimSpace(line[:idx]))
val := strings.TrimSpace(line[idx+1:])
```

Case-insensitive header matching via `strings.ToLower` follows HTTP conventions
(the spec says headers should be case-insensitive).

## Test Coverage

| Test | What It Verifies |
|------|-----------------|
| `TestFormatAnnounce` | Wire format structure, all headers present |
| `TestParseAnnounce` | All fields extracted from well-formed message |
| `TestParseAnnounceRoundTrip` | Format→Parse preserves all fields |
| `TestParseAnnounceBadRequestLine` | Rejects non-BT-SEARCH messages |
| `TestParseAnnounceMissingPort` | Error on missing required header |
| `TestParseAnnounceMissingInfohash` | Error on missing required header |
| `TestParseAnnounceInvalidInfohashLength` | Rejects 38-char hash (not 40) |
| `TestParseAnnounceInvalidInfohashHex` | Rejects non-hex characters |
| `TestParseAnnounceCaseInsensitiveHeaders` | Headers work in any case |
| `TestParseAnnounceExtraHeaders` | Unknown headers ignored gracefully |
| `TestCookieFiltering` | Own cookie filtered, others accepted |
| `TestInfohashFiltering` | Only registered infohashes active |
| `TestAddRemoveInfohash` | Add/remove/re-add lifecycle |
| `TestPeerAddressFormat` | Peer.Addr format is "ip:port" |
| `TestGenerateCookie` | Cookie format, uniqueness, length |
| `TestFormatAnnounceSize` | Message fits in one UDP datagram |
