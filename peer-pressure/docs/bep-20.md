# BEP 20 — Peer ID Conventions

> Reference: <https://www.bittorrent.org/beps/bep_0020.html>

## Summary

BEP 20 standardizes the format of the 20-byte peer ID that every BitTorrent client sends in tracker announces and peer handshakes. The peer ID serves two purposes:

1. **Unique identifier.** Each running client instance has a unique peer ID so trackers and peers can distinguish between different clients in the swarm.
2. **Client fingerprinting.** The first 8 bytes encode the client name and version, allowing trackers, peers, and debugging tools to identify what software a peer is running (e.g., "Peer Pressure 0.0.0.1" vs "qBittorrent 4.6.2").

**Why it matters:**

- Trackers use peer IDs to enforce per-client announce policies, detect spoofing, and compile client statistics.
- Other peers use it for informational display ("Connected to: Peer Pressure 0.0.0.1").
- Debugging becomes much easier when you can identify which client version a misbehaving peer is running.
- Without a standard format, every client would invent its own scheme and identification would be guesswork.

## Protocol Specification

### Where the Peer ID Appears

The peer ID is a fixed 20-byte value generated once at client startup and reused for the lifetime of that process. It appears in two places:

1. **Tracker announce request:** Sent as the `peer_id` query parameter (URL-encoded 20 bytes) in HTTP tracker announces, or as 20 raw bytes at offset 36 in UDP tracker announce packets (BEP 15).

2. **Peer wire handshake:** The last 20 bytes of the 68-byte handshake (BEP 3):

```
Handshake layout (68 bytes):

Offset  Length  Field
0       1       Protocol string length (= 19)
1       19      Protocol string ("BitTorrent protocol")
20      8       Reserved bytes
28      20      Info hash
48      20      Peer ID  ← here
```

### Peer ID Formats

Two major conventions exist. Each encodes client identity in the first 8 bytes and fills the remaining 12 bytes with random data.

#### Azureus-style (modern standard)

Used by the vast majority of current clients. Format:

```
-CCVVVV-xxxxxxxxxxxx

Byte layout:
Offset  Length  Content
0       1       '-' (0x2D)
1       2       Client ID — two ASCII letters
3       4       Version — four ASCII digits
7       1       '-' (0x2D)
8       12      Random bytes (any value 0x00–0xFF)
```

**Examples:**

| Peer ID (hex-escaped) | Client | Version |
|---|---|---|
| `-PP0001-\xab\xcd...` | Peer Pressure | 0.0.0.1 |
| `-qB4620-\x12\x34...` | qBittorrent | 4.6.2.0 |
| `-TR4040-\xff\xee...` | Transmission | 4.0.4.0 |
| `-DE1390-\x01\x02...` | Deluge | 1.3.9.0 |
| `-AZ5750-\xaa\xbb...` | Vuze (Azureus) | 5.7.5.0 |
| `-lt0D70-\xcc\xdd...` | libtorrent | 0.13.7.0 |
| `-UT3560-\xee\xff...` | µTorrent | 3.5.6.0 |

**Version encoding:** The four digits represent four version components. For Peer Pressure version `0.1.0`:
- Major: `0`, Minor: `1`, Patch: `0`, Tweak: `0` → `"0100"`
- The peer ID prefix becomes `-PP0100-`

For version `1.2.3`:
- `"1230"` → prefix `-PP1230-`

#### Shadow-style (legacy)

Used by older clients. Largely obsolete but encountered in the wild.

```
CVVVV--xxxxxxxxxxxxx

Byte layout:
Offset  Length  Content
0       1       Client letter (single ASCII char)
1       4       Version (ASCII digits or hex chars)
5       2       '--' (0x2D 0x2D)
7       13      Random bytes
```

**Examples:**

| Prefix | Client |
|---|---|
| `S5890--...` | Shadow |
| `T0340--...` | BitTornado |
| `M7210--...` | Mainline (official) |
| `A20530-...` | ABC |

### Our Peer ID: Peer Pressure

We use Azureus-style with client code `PP`:

```
-PP<MAJOR><MINOR><PATCH><TWEAK>-<12 random bytes>

For version 0.1.0:
  -PP0100-<12 random bytes>

Byte-by-byte:
  [0x2D] [0x50] [0x50] [0x30] [0x31] [0x30] [0x30] [0x2D] [??] [??] [??] [??] [??] [??] [??] [??] [??] [??] [??] [??]
    -      P      P      0      1      0      0      -     r    a    n    d    o    m    .    .    .    .    .    .
```

**Current state in the codebase:** The peer ID is generated in `cmd/peer-pressure/main.go`:

```go
const version = "0.1.0"

var peerID [20]byte

func init() {
    copy(peerID[:], "-PP0001-")
    rand.Read(peerID[8:])
}
```

**Problem:** The version prefix is hardcoded as `"-PP0001-"` and not derived from the `version` constant. The `0001` doesn't match the version `0.1.0` (which should be `0100`). The generation logic is inlined in `init()` rather than being a reusable function.

### Peer ID Identification (Decoding)

To identify a remote peer's client from its peer ID:

```
1. If peerID[0] == '-' and peerID[7] == '-':
     → Azureus-style
     client_code = peerID[1:3]
     version = peerID[3:7]
     Look up client_code in known client table

2. Else if peerID[0] is a letter and peerID[5:7] == "--":
     → Shadow-style
     client_code = peerID[0]
     version = peerID[1:5]
     Look up client_code in known client table

3. Else:
     → Unknown format, report as raw hex
```

## Implementation Plan

### Package: `client/`

Create a new `client/` package that owns the peer ID generation and version metadata. This centralizes identity logic that's currently scattered in `cmd/peer-pressure/main.go`.

#### `client/peerid.go`

**Constants and types:**

```go
// ClientID is the two-letter Azureus-style client identifier for Peer Pressure.
const ClientID = "PP"

// Version is the current client version (semver-style: major.minor.patch).
const Version = "0.1.0"
```

**Key functions:**

```go
// GeneratePeerID creates a 20-byte Azureus-style peer ID:
//   -PP<MJMNPTPT>-<12 random bytes>
// where MJ=major, MN=minor, PT=patch, PT=tweak (always 0).
// Uses crypto/rand for the random suffix.
func GeneratePeerID() [20]byte

// FormatVersionPrefix builds the 8-byte prefix from a version string.
// "0.1.0" → "-PP0100-"
// "1.2.3" → "-PP1230-"
func FormatVersionPrefix(version string) string

// ParsePeerID extracts the client name and version from a 20-byte peer ID.
// Returns (clientName, versionStr, ok). ok is false if the format is unrecognized.
func ParsePeerID(id [20]byte) (clientName string, version string, ok bool)
```

**`GeneratePeerID` implementation:**

```go
func GeneratePeerID() [20]byte {
    var id [20]byte
    prefix := FormatVersionPrefix(Version)
    copy(id[:], prefix)
    if _, err := rand.Read(id[8:]); err != nil {
        panic("crypto/rand failed: " + err.Error())
    }
    return id
}
```

Note: `rand.Read` from `crypto/rand` does not return an error on any supported OS (Linux, macOS, Windows all have kernel entropy sources). The `panic` is a safety net for truly broken systems — this is acceptable in `init`-level code per the codebase convention of no `panic` in runtime paths (this is startup-only).

**`FormatVersionPrefix` implementation:**

```go
func FormatVersionPrefix(version string) string {
    parts := strings.SplitN(version, ".", 3)
    var digits [4]byte
    for i := 0; i < 3 && i < len(parts); i++ {
        if len(parts[i]) > 0 {
            digits[i] = parts[i][0] // take first character
        } else {
            digits[i] = '0'
        }
    }
    digits[3] = '0' // tweak is always 0
    return fmt.Sprintf("-%s%c%c%c%c-", ClientID, digits[0], digits[1], digits[2], digits[3])
}
```

**`ParsePeerID` implementation:**

```go
// Known Azureus-style client codes.
var knownClients = map[string]string{
    "PP": "Peer Pressure",
    "qB": "qBittorrent",
    "TR": "Transmission",
    "DE": "Deluge",
    "AZ": "Vuze",
    "UT": "µTorrent",
    "lt": "libtorrent",
    "LT": "libtorrent (Rasterbar)",
    "BI": "BiglyBT",
}

func ParsePeerID(id [20]byte) (string, string, bool) {
    if id[0] == '-' && id[7] == '-' {
        code := string(id[1:3])
        ver := string(id[3:7])
        name, known := knownClients[code]
        if !known {
            name = "Unknown (" + code + ")"
        }
        return name, ver, true
    }
    // Shadow-style or unknown — return raw
    return "", "", false
}
```

### Files to Modify

#### `cmd/peer-pressure/main.go`

Replace the inline peer ID generation:

```go
// Before:
const version = "0.1.0"
var peerID [20]byte
func init() {
    copy(peerID[:], "-PP0001-")
    rand.Read(peerID[8:])
}

// After:
var peerID = client.GeneratePeerID()
```

Remove the `"crypto/rand"` import if it's no longer used elsewhere in the file. Remove the `version` constant (it now lives in `client.Version`). Update any references to `version` to use `client.Version`.

#### `peer/extension.go`

The extension handshake client string is currently hardcoded:

```go
"v": bencode.String("Peer Pressure 0.1"),
```

Update to use `client.Version`:

```go
"v": bencode.String("Peer Pressure " + client.Version),
```

This requires adding `"github.com/ihvo/peer-pressure/client"` to the imports. Alternatively, accept the version string as a parameter to `NewExtHandshake` to avoid the circular dependency — evaluate at implementation time whether `client` imports `peer` (it shouldn't, so this direction is fine).

#### `tracker/tracker.go`

If the tracker package generates or references a peer ID, ensure it uses the one passed in `AnnounceParams.PeerID` (it already does — no change needed, just verify).

### Import Graph

```
client/  (no internal dependencies — leaf package)
  ↑
  ├── cmd/peer-pressure/  (imports client for GeneratePeerID, Version)
  └── peer/extension.go   (imports client for Version string)
```

`client/` must NOT import any other internal package to stay a leaf in the dependency graph.

## Dependencies

| BEP | Relationship |
|---|---|
| BEP 3 (Peer Wire Protocol) | **Required.** The peer ID is the last 20 bytes of the 68-byte handshake. |
| BEP 15 (UDP Tracker) | **Required.** The peer ID is sent at offset 36 in the UDP announce packet. |
| BEP 10 (Extension Protocol) | **Related.** The `v` key in the extension handshake carries a human-readable client string (e.g., "Peer Pressure 0.1.0") which should be consistent with the peer ID version. |
| BEP 27 (Private Torrents) | **None.** Peer ID format is not affected by the private flag. |

## Testing Strategy

### Unit Tests — `client/peerid_test.go`

1. **`TestGeneratePeerID`** — Call `GeneratePeerID()`. Verify the result is exactly 20 bytes, starts with `-PP`, has `-` at index 7, and has the correct version digits at indices 3–6.

2. **`TestGeneratePeerIDRandomness`** — Call `GeneratePeerID()` twice. Verify the last 12 bytes differ (with overwhelming probability). This confirms `crypto/rand` is being used for the suffix.

3. **`TestGeneratePeerIDPrefix`** — Verify `id[0:8]` matches the expected prefix for the current `Version` constant. For `"0.1.0"`, prefix should be `"-PP0100-"`.

4. **`TestFormatVersionPrefix`** — Table-driven test:

   | Input | Expected |
   |---|---|
   | `"0.1.0"` | `"-PP0100-"` |
   | `"1.2.3"` | `"-PP1230-"` |
   | `"0.0.1"` | `"-PP0010-"` |
   | `"9.9.9"` | `"-PP9990-"` |

5. **`TestFormatVersionPrefixSingleDigit`** — Version `"1"` → `"-PP1000-"`. Version `"1.2"` → `"-PP1200-"`.

6. **`TestParsePeerIDAzureus`** — Parse `"-PP0100-abcdefghijkl"` → ("Peer Pressure", "0100", true).

7. **`TestParsePeerIDKnownClients`** — Table-driven: parse peer IDs for qBittorrent (`-qB4620-...`), Transmission (`-TR4040-...`), Deluge (`-DE1390-...`). Verify correct client name and version.

8. **`TestParsePeerIDUnknownClient`** — Parse `"-XX1234-randomrandom"` → ("Unknown (XX)", "1234", true). The format is recognized as Azureus-style even though the client code is unknown.

9. **`TestParsePeerIDShadowStyle`** — Parse a Shadow-style ID like `"S5890--randomrandomr"`. Verify `ok` is false (we only fully parse Azureus-style; Shadow returns unrecognized).

10. **`TestParsePeerIDGarbage`** — Parse 20 random bytes that don't match either format. Verify `ok` is false.

### Integration Tests

11. **`TestPeerIDInHandshake`** — Generate a peer ID with `GeneratePeerID()`. Perform a full handshake via `peer.Dial` / `peer.FromConn` over `net.Pipe`. Verify the remote side reads back the correct 20-byte peer ID with the expected prefix.

12. **`TestPeerIDInTrackerAnnounce`** — Set up a mock HTTP tracker. Send an announce with a `GeneratePeerID()` result. Verify the tracker receives the `peer_id` parameter and it starts with `-PP` and has the correct version digits.

13. **`TestVersionConsistency`** — Verify that `client.Version` and the prefix from `FormatVersionPrefix(client.Version)` are consistent. Parse the generated peer ID with `ParsePeerID` and verify it reports "Peer Pressure" as the client name.
