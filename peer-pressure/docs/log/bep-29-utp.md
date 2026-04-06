# BEP 29: uTorrent Transport Protocol (uTP)

## What It Does

uTP is a reliable, ordered transport protocol built on top of UDP. It's
the backbone of modern BitTorrent: most peer connections and all DHT
holepunching (BEP 55) use uTP instead of TCP. The key innovation is
**delay-based congestion control (LEDBAT)** — uTP detects bufferbloat
and throttles back, yielding bandwidth to interactive traffic.

### Why Not Just TCP?

TCP distributes bandwidth evenly across connections. A BitTorrent client
with 50 TCP connections gets 50× more bandwidth than a web browser with 1.
uTP fixes this unfairness by backing off when it detects queuing delay.

### Packet Header (20 bytes)

```
 0       4       8               16              24              32
+-------+-------+---------------+---------------+---------------+
| type  | ver   | extension     | connection_id                 |
+-------+-------+---------------+---------------+---------------+
| timestamp_microseconds                                        |
+-------+-------+---------------+---------------+---------------+
| timestamp_difference_microseconds                             |
+-------+-------+---------------+---------------+---------------+
| wnd_size                                                      |
+-------+-------+---------------+---------------+---------------+
| seq_nr                        | ack_nr                        |
+-------+-------+---------------+---------------+---------------+
```

Key differences from TCP:
- **Sequence numbers count packets**, not bytes (can't repackage on resend)
- **Timestamps** in every packet enable one-way delay measurement
- **Connection IDs** identify streams (not port pairs like TCP)

### Packet Types

| Type | Name | Purpose |
|------|------|---------|
| 0 | ST_DATA | Regular data |
| 1 | ST_FIN | Close connection (like TCP FIN) |
| 2 | ST_STATE | ACK-only, no data (doesn't increment seq_nr) |
| 3 | ST_RESET | Force terminate (like TCP RST) |
| 4 | ST_SYN | Initiate connection (like TCP SYN) |

### Connection Setup (3-way)

```
Initiator                         Acceptor
    |  ST_SYN (conn_id=rand())       |
    |  seq_nr=1                       |
    | ─────────────────────────────>  |
    |                                 |  recv_id = pkt.conn_id + 1
    |                                 |  send_id = pkt.conn_id
    |       ST_STATE                  |
    |       ack_nr = 1                |
    | <─────────────────────────────  |
    |  ST_DATA                        |
    |  conn_id = recv_id + 1          |
    | ─────────────────────────────>  |
```

The initiator picks `conn_id_recv = rand()` and sends on it. The acceptor
responds on `conn_id + 1`. This asymmetry prevents ambiguity.

### Selective ACK Extension

When packets arrive out of order, the receiver sends a bitmask showing
which packets it has. The bitmask starts at `ack_nr + 2` (since `ack_nr + 1`
is the missing one). Bit order within bytes is LSB-first:

```
byte 0: [ack+2, ack+3, ack+4, ..., ack+9]
byte 1: [ack+10, ack+11, ..., ack+17]
```

Minimum bitmask size is 4 bytes (32 bits), always rounded to 4-byte multiples.

### LEDBAT Congestion Control

The genius of uTP: instead of measuring loss (TCP), it measures **queuing delay**.

1. Each packet carries a microsecond timestamp
2. Receiver computes `delay = now_us - pkt.timestamp`
3. Sends this back as `timestamp_difference_microseconds`
4. Sender tracks `base_delay` (sliding minimum over 2 minutes)
5. `our_delay = latest_diff - base_delay` = current queue depth
6. `off_target = 100ms - our_delay` (100ms is the target)
7. Window adjustment: `gain = MAX_GAIN × (off_target/target) × (inflight/window)`

When `our_delay > 100ms` → window shrinks (traffic is causing congestion).
When `our_delay < 100ms` → window grows (bandwidth available).

### What We Implemented

- **Packet codec**: header encode/decode with type+version nibble packing
- **Extension parsing**: linked-list traversal, selective ACK bitmask
- **Selective ACK operations**: set/get bits, enumerate acked packets
- **LEDBAT congestion controller**: RTT smoothing, delay-based window
  adjustment, loss/timeout handling, flow control with peer window

## Go Idioms

### Nibble Packing

```go
buf[0] = (h.Type << 4) | (h.Version & 0x0f)
// ...
h.Type = (data[0] >> 4) & 0x0f
h.Version = data[0] & 0x0f
```

The uTP header packs two 4-bit fields into one byte. Go's bitwise operators
make this natural. The `& 0x0f` mask ensures we only use the lower nibble,
even if the caller passes garbage in the upper bits.

### Linked-List Encoding in Flat Buffers

```go
for i, ext := range p.Extensions {
    if i+1 < len(p.Extensions) {
        buf[off] = p.Extensions[i+1].Type // next pointer
    } else {
        buf[off] = ExtNone // end of chain
    }
```

uTP extensions form a linked list in the packet: each extension's first byte
points to the **next** extension's type (0 = end). During encoding, we peek
ahead to write the correct "next" pointer. This pattern converts a Go slice
into a wire-format linked list.

### Defensive Copies in Network Code

```go
ext.Payload = make([]byte, extLen)
copy(ext.Payload, data[off:off+extLen])
```

Every decoded field gets its own allocation rather than slicing into the
input buffer. In network code, the same `[]byte` buffer is reused for
the next `ReadFrom()` call — any slice into it becomes stale. This is the
same pattern we use in the bencode package (noted in AGENTS.md).

### Float64 for Control Theory Math

```go
delayFactor = float64(offTarget) / float64(TargetDelay.Microseconds())
windowFactor = float64(cc.CurWindow) / float64(cc.MaxWindow)
scaledGain = float64(MaxGain) * delayFactor * windowFactor
```

The LEDBAT algorithm involves fractional scaling factors. Using `float64`
for the intermediate calculation and converting back to `uint32` for the
window avoids integer overflow and truncation issues. The final `int64()`
conversion naturally floors the result.
