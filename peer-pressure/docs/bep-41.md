# BEP 41 — UDP Tracker Protocol Extensions

> Reference: <https://www.bittorrent.org/beps/bep_0041.html>

## Summary

BEP 41 extends the BEP 15 UDP tracker protocol with optional data appended after the standard announce response. This data uses a Type-Length-Value (TLV) encoding scheme called "options." Trackers can append options to carry additional information (like the client's external IP or supplementary URL data) without breaking backward compatibility — old clients simply ignore the extra bytes.

This matters because:

1. **External IP for UDP trackers**: BEP 24 defines `external ip` for HTTP trackers, but the original BEP 15 UDP protocol has no equivalent. BEP 41 option type `0x03` fills this gap.
2. **Extensibility**: New tracker features can be added as new option types without changing the core protocol.
3. **Backward compatibility**: Since UDP responses are variable-length and clients already ignore trailing bytes they don't understand, this extension is fully backward compatible.

## Protocol Specification

### Response Layout

A standard BEP 15 UDP announce response is:

```
Bytes 0-3:    action (1 = announce)
Bytes 4-7:    transaction_id
Bytes 8-11:   interval
Bytes 12-15:  leechers
Bytes 16-19:  seeders
Bytes 20+:    compact peers (6 bytes each for IPv4)
```

With BEP 41, after the compact peer list, the tracker MAY append option data:

```
Bytes 0-19:              standard announce header
Bytes 20 to 20+N*6-1:   compact peers (N peers × 6 bytes)
Bytes 20+N*6 to end:    option TLVs
```

### Option TLV Format

Each option is encoded as:

```
 0       1       2  ...  2+len-1
+-------+-------+---------------+
| type  |  len  |     value     |
+-------+-------+---------------+
```

- **type** (1 byte): Option type identifier.
- **len** (1 byte): Length of the value field in bytes. For types 0x00 and 0x01, no length byte follows (they are self-delimiting).
- **value** (`len` bytes): Option-specific data.

Options are concatenated one after another. Parsing continues until:
- An `EndOfOptions` (type 0x00) is encountered, OR
- The end of the UDP packet is reached.

### Defined Option Types

| Type | Name | Length | Description |
|------|------|--------|-------------|
| 0x00 | EndOfOptions | 0 (no len byte) | Marks the end of the option list. Parsing MUST stop here. |
| 0x01 | NOP | 0 (no len byte) | No operation. Used for padding/alignment. Skip and continue parsing. |
| 0x02 | URLData | variable | Additional URL data the client should use (e.g., appended to the tracker path). |
| 0x03 | ExternalIP | 4 or 16 | Client's external IP address as seen by the tracker. 4 bytes = IPv4, 16 bytes = IPv6. Same semantics as BEP 24's `external ip` field. |

### EndOfOptions (0x00) — Detail

```
+------+
| 0x00 |
+------+
```

Just the single type byte. No length field, no value. Terminates option parsing.

### NOP (0x01) — Detail

```
+------+
| 0x01 |
+------+
```

Just the single type byte. No length field, no value. Parser skips it and moves to the next byte.

### URLData (0x02) — Detail

```
 0       1       2  ...  2+len-1
+-------+-------+---------------+
| 0x02  |  len  |   URL bytes   |
+-------+-------+---------------+
```

The value is a raw byte string containing URL path/query data. The client may need to append this to the tracker URL for future requests. The exact semantics are tracker-specific.

### ExternalIP (0x03) — Detail

```
IPv4:
 0       1       2   3   4   5
+-------+-------+---+---+---+---+
| 0x03  | 0x04  | a | b | c | d |
+-------+-------+---+---+---+---+

IPv6:
 0       1       2 ... 17
+-------+-------+---------+
| 0x03  | 0x10  | 16 bytes|
+-------+-------+---------+
```

- Length 4: IPv4 address in network byte order.
- Length 16: IPv6 address in network byte order.
- Other lengths: invalid, skip this option.

This provides the same information as BEP 24's `external ip` for HTTP trackers, enabling NAT detection and BEP 42 compliance for UDP-only tracker announces.

### Determining Where Options Begin

The challenge is distinguishing option bytes from peer data. The standard response has a 20-byte header followed by compact peers (6 bytes each for IPv4). The option data starts after the last peer.

**Approach**: The number of peers is determined by the tracker. Since the tracker knows how many peers it included, the option data follows immediately. From the client's perspective:

1. Parse the 20-byte header. Extract `leechers + seeders` to estimate peer count (though the actual count may differ).
2. The response may contain fewer peers than requested. Heuristic: consume 6-byte chunks as peers until fewer than 6 bytes remain, or until a byte pattern looks like an option type.
3. **Practical approach**: Parse peers as 6-byte chunks. When `remaining_bytes < 6`, or when `remaining_bytes >= 2` and the bytes match a known option pattern, switch to option parsing.

A simpler and more robust approach used by most clients:

```
peer_data_len = len(response) - 20
num_peers = peer_data_len / 6    // integer division
leftover = peer_data_len % 6

peer_bytes = response[20 : 20 + num_peers*6]
option_bytes = response[20 + num_peers*6 :]  // leftover bytes are options
```

If the tracker appends options, the total response length is not a multiple of 6 (after subtracting the 20-byte header). The leftover bytes are options. If there are no options, the leftover is empty.

**However**, some trackers pad the peer list to ensure alignment. A more robust strategy: always attempt to parse leftover bytes as options, and also check the last few bytes of "peer data" in case the tracker included options without proper alignment. In practice, the simple modulo approach works for all known trackers.

### Unknown Option Types

If the client encounters an option type it doesn't recognize:

1. Read the length byte.
2. Skip `length` bytes.
3. Continue parsing the next option.

This allows forward compatibility — new option types can be defined without breaking existing clients.

## Implementation Plan

### Files to Create / Modify

| File | Action | Purpose |
|------|--------|---------|
| `tracker/options.go` | Create | TLV option parsing, option types, `ParseOptions` function |
| `tracker/udp.go` | Modify | Call `ParseOptions` on leftover bytes after peer parsing |
| `tracker/options_test.go` | Create | Tests for option parsing |

### Key Types

```go
// tracker/options.go

// OptionType identifies a BEP 41 UDP tracker extension option.
type OptionType uint8

const (
    OptEndOfOptions OptionType = 0x00
    OptNOP          OptionType = 0x01
    OptURLData      OptionType = 0x02
    OptExternalIP   OptionType = 0x03
)

// Option is a single parsed TLV option from a UDP tracker response.
type Option struct {
    Type  OptionType
    Value []byte
}

// Options holds all parsed options from a UDP tracker response.
type Options struct {
    ExternalIP net.IP   // from OptExternalIP (nil if not present)
    URLData    []byte   // from OptURLData (nil if not present)
    Raw        []Option // all parsed options, including unknown types
}
```

### Key Functions

```go
// tracker/options.go

// ParseOptions parses BEP 41 TLV options from the trailing bytes of a UDP
// tracker announce response. Returns parsed options. Unrecognized option types
// are included in Raw but not interpreted. Returns an error only if the data
// is malformed (e.g., length field extends past end of data).
func ParseOptions(data []byte) (Options, error)
```

### Implementation Detail

```go
func ParseOptions(data []byte) (Options, error) {
    var opts Options
    i := 0
    for i < len(data) {
        typ := OptionType(data[i])
        i++

        switch typ {
        case OptEndOfOptions:
            return opts, nil

        case OptNOP:
            opts.Raw = append(opts.Raw, Option{Type: typ})
            continue

        default:
            // All other types have a length byte
            if i >= len(data) {
                return opts, fmt.Errorf("option 0x%02x: missing length byte", typ)
            }
            length := int(data[i])
            i++

            if i+length > len(data) {
                return opts, fmt.Errorf("option 0x%02x: length %d exceeds remaining %d bytes", typ, length, len(data)-i)
            }

            value := make([]byte, length)
            copy(value, data[i:i+length])
            i += length

            opt := Option{Type: typ, Value: value}
            opts.Raw = append(opts.Raw, opt)

            switch typ {
            case OptExternalIP:
                switch length {
                case 4:
                    opts.ExternalIP = net.IPv4(value[0], value[1], value[2], value[3])
                case 16:
                    ip := make(net.IP, 16)
                    copy(ip, value)
                    opts.ExternalIP = ip
                }
            case OptURLData:
                opts.URLData = value
            }
        }
    }
    return opts, nil
}
```

### Changes to Existing Functions

**`tracker/udp.go` — `udpAnnounce`:**

After parsing compact peers, check for leftover bytes and parse as options:

```go
// In udpAnnounce, after parsing peers:
peerData := resp[20:]
numPeers := len(peerData) / peerCompactLen
peerBytes := peerData[:numPeers*peerCompactLen]
optionBytes := peerData[numPeers*peerCompactLen:]

peers, err := parseCompactPeers(peerBytes)
if err != nil {
    return nil, fmt.Errorf("parse peers: %w", err)
}

result := &Response{
    Interval:   int(interval),
    Peers:      peers,
    Complete:   int(seeders),
    Incomplete: int(leechers),
}

// BEP 41: parse trailing options
if len(optionBytes) > 0 {
    opts, err := ParseOptions(optionBytes)
    if err == nil {
        result.ExternalIP = opts.ExternalIP
    }
    // Non-fatal: if option parsing fails, we still have peers
}

return result, nil
```

### Package Placement

All code stays in `tracker/`. The option types and parser are defined in a new file `tracker/options.go` to keep `udp.go` focused on the core BEP 15 protocol flow. The `Options` struct fields feed into the existing `Response` struct.

## Dependencies

| BEP | Relationship |
|-----|-------------|
| BEP 15 | Base UDP tracker protocol that BEP 41 extends |
| BEP 24 | BEP 41 option type `0x03` provides the same external IP functionality as BEP 24 for HTTP |
| BEP 7 | IPv6 tracker extension — external IP option may contain an IPv6 address |

## Testing Strategy

### Unit Tests (`tracker/options_test.go`)

1. **`TestParseOptions_Empty`** — Empty input returns zero-value `Options`, no error.

2. **`TestParseOptions_EndOfOptions`** — Single byte `0x00`. Returns empty options, no error.

3. **`TestParseOptions_NOP`** — Single byte `0x01`. Returns one Raw option of type NOP.

4. **`TestParseOptions_ExternalIPv4`** — Bytes: `0x03, 0x04, 203, 0, 113, 42, 0x00`. Verify `ExternalIP == 203.0.113.42` and EndOfOptions terminates parsing.

5. **`TestParseOptions_ExternalIPv6`** — Bytes: `0x03, 0x10, <16 bytes of 2001:db8::1>, 0x00`. Verify ExternalIP matches.

6. **`TestParseOptions_URLData`** — Bytes: `0x02, 0x05, 'h', 'e', 'l', 'l', 'o'`. Verify `URLData == []byte("hello")`.

7. **`TestParseOptions_MultipleOptions`** — Chain: NOP + ExternalIPv4 + URLData + EndOfOptions. Verify all fields populated.

8. **`TestParseOptions_UnknownType`** — Bytes: `0xFF, 0x02, 0xAA, 0xBB, 0x00`. Verify unknown type is in `Raw` with correct value, no error, parsing continues.

9. **`TestParseOptions_TruncatedLength`** — Bytes: `0x03`. Missing length byte. Verify error.

10. **`TestParseOptions_TruncatedValue`** — Bytes: `0x03, 0x04, 1, 2`. Length says 4 but only 2 bytes remain. Verify error.

11. **`TestParseOptions_ExternalIPInvalidLength`** — Bytes: `0x03, 0x07, <7 bytes>`. Length is neither 4 nor 16. Verify option is in Raw but `ExternalIP` is nil.

12. **`TestParseOptions_NOPPadding`** — Bytes: `0x01, 0x01, 0x03, 0x04, <4 IP bytes>, 0x00`. Two NOPs before the actual option. Verify ExternalIP is still parsed.

13. **`TestParseOptions_NoEndOfOptions`** — Options without a trailing `0x00` — parsing reaches end of input. Verify it returns successfully (end-of-data is an implicit end).

### Integration Tests

14. **`TestUDPAnnounce_WithOptions`** — Mock UDP tracker that returns a standard announce response + option bytes appended. Verify `udpAnnounce` returns a `Response` with `ExternalIP` populated from the options.

15. **`TestUDPAnnounce_NoOptions`** — Standard announce response with no trailing bytes. Verify no errors, `ExternalIP` is nil (backward compatibility).

16. **`TestUDPAnnounce_OnlyNOPs`** — Trailing bytes are all `0x01` (NOPs). Verify no errors, no data extracted.

### Edge Cases

17. **`TestParseOptions_ZeroLengthValue`** — Bytes: `0x02, 0x00, 0x00`. URLData with length 0. Verify `URLData` is empty byte slice (not nil), no error.

18. **`TestParseOptions_MaxLength`** — Option with length 255 (max uint8). Provide exactly 255 value bytes. Verify successful parse.
