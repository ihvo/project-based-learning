# BEP 55: Holepunch Extension

## What It Does

BEP 55 enables NAT traversal for BitTorrent peers. When a peer is behind a
NAT or firewall that blocks incoming connections, a third-party "relay" peer
facilitates the connection by coordinating simultaneous outbound connections.

### The Three-Party Protocol

```
Initiator ──rendezvous──> Relay ──connect──> Target
                          Relay ──connect──> Initiator
```

1. **Initiator** sends `rendezvous` to a peer (the relay) that's already
   connected to the target
2. **Relay** sends `connect` messages to both initiator and target, each
   containing the other's endpoint (IP + port)
3. Both peers simultaneously initiate uTP connections to each other
4. NATs see outbound traffic and open pinholes for the return packets

### Message Format (Binary)

```
msg_type  (1 byte):  0x00=rendezvous, 0x01=connect, 0x02=error
addr_type (1 byte):  0x00=IPv4, 0x01=IPv6
addr      (4|16 B):  big-endian IP address
port      (2 bytes): big-endian port
err_code  (4 bytes): error code (0 for non-errors)
```

IPv4 message = 12 bytes, IPv6 = 24 bytes. Sent via the extension protocol
(BEP 10) as `ut_holepunch`.

### Error Codes

| Code | Meaning |
|------|---------|
| 0x01 | NoSuchPeer — invalid target endpoint |
| 0x02 | NotConnected — relay not connected to target |
| 0x03 | NoSupport — target doesn't support holepunch |
| 0x04 | NoSelf — target is the relay itself |

### Important: Requires uTP (BEP 29)

Holepunching works with UDP-based protocols (uTP) because UDP NAT mappings
are symmetric — both sides can open pinholes simultaneously. TCP holepunching
is unreliable because most NATs don't support simultaneous TCP open.

### What We Implemented

- **`EncodeHolepunch`/`DecodeHolepunch`** — binary codec for all three
  message types, both IPv4 and IPv6
- **Constants** for message types, address types, and error codes
- **`HolepunchErrorString`** — human-readable error descriptions

The actual NAT traversal (uTP connection initiation) requires BEP 29 (uTP),
which is a separate implementation. This module provides the signaling layer.

## Go Idioms

### Fixed Binary Protocol with Variable-Length Fields

```go
func EncodeHolepunch(msg HolepunchMessage) []byte {
    var addrLen int
    if msg.AddrType == HolepunchIPv6 {
        addrLen = 16
    } else {
        addrLen = 4
    }
    buf := make([]byte, 2+addrLen+2+4)
```

The message has a fixed structure but the address field varies. Computing
`addrLen` first, then allocating exactly `2 + addrLen + 2 + 4` bytes,
avoids both over-allocation and dynamic growth. The total is always either
12 (IPv4) or 24 (IPv6) bytes.

### Defensive Copy for IP Addresses

```go
msg.IP = make(net.IP, addrLen)
copy(msg.IP, data[2:2+addrLen])
```

Allocating a new `net.IP` slice and copying prevents the decoded message
from aliasing the input buffer. This is critical in network code where the
same buffer may be reused for the next received packet.

### Switch-Based Dispatch for Small Enums

```go
switch msg.AddrType {
case HolepunchIPv4:
    addrLen = 4
case HolepunchIPv6:
    addrLen = 16
default:
    return HolepunchMessage{}, fmt.Errorf("unknown addr_type")
}
```

For small enums with known valid values, a switch with explicit default
error is clearer than a map lookup or if-else chain. The compiler can also
warn about missing cases (though Go doesn't do this for byte types).
