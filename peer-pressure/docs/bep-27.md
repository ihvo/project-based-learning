# BEP 27 — Private Torrents

> **Specification:** <https://www.bittorrent.org/beps/bep_0027.html>
> **Status:** Not started
> **Phase:** 5 — Protocol Hardening

---

## 1. Summary

BEP 27 defines a mechanism for private torrents — torrents whose peer
discovery is restricted to tracker(s) specified in the .torrent file. When the
`info.private` flag is set to `1`, the client **must not** use any
decentralized or gossiped peer discovery:

- **No DHT** (BEP 5)
- **No PEX** (BEP 11)
- **No Local Peer Discovery** (BEP 14)

This matters because private trackers (e.g., ratio-enforced communities) rely
on being the sole source of peers so they can enforce upload/download ratios,
enforce invitations, and ban misbehaving clients. If a client leaks info_hashes
to DHT or exchanges peers via PEX, it undermines the private tracker's
controls.

Because the `private` key lives inside the `info` dictionary, it is part of
the data hashed to produce the `info_hash`. Changing the private flag changes
the torrent's identity — a private torrent and its "public twin" are different
torrents from the protocol's perspective.

---

## 2. Protocol Specification

### 2.1 The `private` Flag

The flag lives in the bencoded info dictionary:

```
d
  ...
  7:privatei1e
  ...
e
```

| Key       | Type         | Value | Meaning                    |
|-----------|--------------|-------|----------------------------|
| `private` | bencode int  | `1`   | Torrent is private         |
| `private` | bencode int  | `0`   | Torrent is public          |
| (absent)  | —            | —     | Torrent is public (default)|

Only the integer value `1` means private. Any other value (including the string
`"1"`) means public.

### 2.2 Impact on Info Hash

The `private` key is inside the `info` dictionary, so it is included in the
SHA-1 that produces the 20-byte `info_hash`. Two otherwise-identical torrents
with different `private` values have different info hashes and are treated as
entirely separate torrents by the protocol.

```
info_hash = SHA-1( bencode( info_dict_with_private_key ) )
```

### 2.3 Behavioral Requirements

When `info.private == 1`, a compliant client MUST:

| Peer source              | Allowed? | BEP  |
|--------------------------|----------|------|
| Tracker (HTTP)           | ✅ Yes   | 3    |
| Tracker (UDP)            | ✅ Yes   | 15   |
| Multi-tracker tiers      | ✅ Yes   | 12   |
| DHT                      | ❌ No    | 5    |
| Peer Exchange (PEX)      | ❌ No    | 11   |
| Local Peer Discovery     | ❌ No    | 14   |
| WebSeed (HTTP seeds)     | ✅ Yes   | 19   |
| Metadata Exchange        | ✅ Yes   | 9    |

WebSeeds are allowed because the URLs come from the .torrent file itself (same
trust boundary as the tracker). Metadata exchange (BEP 9) is allowed because it
operates over an already-established peer connection, and the peer was
discovered through the tracker.

### 2.4 Tracker Authentication

Private trackers typically embed a passkey in the announce URL:

```
http://tracker.example.com:8080/a1b2c3d4e5f6/announce
```

The passkey is specific to the user and is used by the tracker to identify who
is announcing. Peer Pressure does not need special handling for this — it
already sends the full announce URL as-is.

### 2.5 State Machine

```
┌─────────────────────┐
│   Parse .torrent     │
│   or fetch metadata  │
└────────┬────────────┘
         │
         ▼
    ┌────────────┐
    │ private=1? │
    └──┬─────┬───┘
       │Yes  │No
       ▼     ▼
  ┌────────┐ ┌────────────────────────┐
  │Tracker │ │Tracker + DHT + PEX +   │
  │only    │ │LSD + all other sources  │
  └────────┘ └────────────────────────┘
```

---

## 3. Implementation Plan

### 3.1 `torrent/torrent.go` — Parse the Private Flag

Add a `Private` field to the `Torrent` struct and parse it from the info dict.

```go
// Torrent holds the parsed contents of a .torrent metainfo file.
type Torrent struct {
    Announce     string
    AnnounceList [][]string
    URLList      []string
    InfoHash     [hashLen]byte
    Name         string
    PieceLength  int
    Pieces       [][hashLen]byte
    Length       int
    Files        []File
    Private      bool          // BEP 27: true if info.private == 1
}
```

In `parseInfo()`, after parsing existing fields:

```go
func parseInfo(t *Torrent, info bencode.Dict) error {
    // ... existing parsing ...

    // BEP 27: private flag.
    if privVal, ok := info["private"]; ok {
        if privInt, ok := privVal.(bencode.Int); ok && privInt == 1 {
            t.Private = true
        }
    }

    return nil
}
```

Also add the flag to `FromInfoDict()` since magnet-link metadata goes through
that path as well.

Update `String()` to display the private status:

```go
if t.Private {
    fmt.Fprintf(&b, "Private:      yes\n")
}
```

### 3.2 `torrent/torrent.go` — `IsPrivate()` Method

A convenience method that makes call sites read clearly:

```go
// IsPrivate reports whether this torrent has the BEP 27 private flag set.
func (t *Torrent) IsPrivate() bool {
    return t.Private
}
```

### 3.3 `cmd/peer-pressure/main.go` — Gate Peer Discovery

In `runPeers()`:

```go
if !*noDHT && !t.IsPrivate() {
    dhtPeers, node := discoverDHTPeers(t.InfoHash)
    // ...
}
if t.IsPrivate() {
    fmt.Println("Private torrent — DHT disabled")
}
```

In `runDownload()`:

```go
if !*noDHT && !t.IsPrivate() {
    dhtCh = make(chan dhtResult, 1)
    go func() {
        peers, node := discoverDHTPeers(t.InfoHash)
        dhtCh <- dhtResult{peers, node}
    }()
}
```

When PEX (BEP 11) and LSD (BEP 14) are implemented, their startup paths
must also check `t.IsPrivate()` and skip if true.

### 3.4 Future: `pex/` and `discovery/` Packages

When PEX and LSD are implemented, each peer source should check privacy:

```go
// In the PEX handler:
if session.Torrent.IsPrivate() {
    return // do not send or process PEX messages
}

// In the LSD announcer:
if session.Torrent.IsPrivate() {
    return // do not multicast
}
```

### 3.5 File Summary

| File                         | Change       | Description                                |
|------------------------------|--------------|--------------------------------------------|
| `torrent/torrent.go`        | Modify       | Add `Private` field, parse in `parseInfo`, parse in `FromInfoDict`, update `String()` |
| `cmd/peer-pressure/main.go` | Modify       | Gate DHT on `IsPrivate()` in `runPeers` and `runDownload`  |
| `pex/` (future)             | Modify       | Check `IsPrivate()` before PEX exchange    |
| `discovery/` (future)       | Modify       | Check `IsPrivate()` in unified peer source |

---

## 4. Dependencies

| BEP | Relationship | Notes |
|-----|-------------|-------|
| 3   | Requires    | Private flag lives in the info dict defined by BEP 3 |
| 5   | Constrains  | DHT must be disabled for private torrents |
| 9   | Interacts   | Metadata exchange still works (peer already came from tracker) |
| 10  | Interacts   | Extension protocol handshake is still exchanged |
| 11  | Constrains  | PEX must be disabled for private torrents |
| 12  | Interacts   | Multi-tracker tiers still work (tracker is the allowed source) |
| 14  | Constrains  | Local Peer Discovery must be disabled for private torrents |
| 19  | Interacts   | WebSeeds still work (URLs come from .torrent file) |

---

## 5. Testing Strategy

### 5.1 `torrent/torrent_test.go` — Parsing Tests

| Test Case | Input | Expected |
|-----------|-------|----------|
| `TestParsePrivateFlag` | .torrent with `private: 1` | `t.Private == true` |
| `TestParsePrivateZero` | .torrent with `private: 0` | `t.Private == false` |
| `TestParsePrivateAbsent` | .torrent without `private` key | `t.Private == false` |
| `TestParsePrivateString` | .torrent with `private: "1"` (string, not int) | `t.Private == false` (only int 1 counts) |
| `TestParsePrivateNegative` | .torrent with `private: -1` | `t.Private == false` |

### 5.2 `torrent/torrent_test.go` — Info Hash Integrity

| Test Case | Description |
|-----------|-------------|
| `TestPrivateFlagAffectsInfoHash` | Construct two info dicts that differ only in the `private` key. Verify their info hashes are different. |

### 5.3 `torrent/torrent_test.go` — `FromInfoDict` Path

| Test Case | Description |
|-----------|-------------|
| `TestFromInfoDictPrivate` | Call `FromInfoDict` with raw info bytes containing `private: 1`. Verify `t.Private == true`. |

### 5.4 Integration / CLI Tests

| Test Case | Description |
|-----------|-------------|
| `TestPeersCommandPrivateSkipsDHT` | Run `runPeers` with a private torrent. Verify DHT is not started. |
| `TestDownloadCommandPrivateSkipsDHT` | Run `runDownload` with a private torrent. Verify no DHT goroutine is launched. |

### 5.5 Test Data

Create a minimal private torrent in `testdata/`:

```go
func makePrivateTorrent(private int) []byte {
    info := bencode.Dict{
        "name":         bencode.String("test"),
        "piece length": bencode.Int(262144),
        "pieces":       bencode.String(strings.Repeat("\x00", 20)),
        "length":       bencode.Int(1024),
    }
    if private >= 0 {
        info["private"] = bencode.Int(private)
    }
    meta := bencode.Dict{
        "announce": bencode.String("http://tracker.example.com/announce"),
        "info":     info,
    }
    return bencode.Encode(meta)
}
```
