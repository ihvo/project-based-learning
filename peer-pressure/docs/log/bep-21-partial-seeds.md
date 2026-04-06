# BEP 21 — Extension for Partial Seeds

## What We Built

Support for the `upload_only` extension, which lets peers signal they are
**partial seeds** — they have some pieces and are only uploading, not downloading.

**Files created:**
- `peer/partialseed.go` — encode/decode/message constructor
- `peer/partialseed_test.go` — 13 tests

**Files modified:**
- `peer/extension.go` — added `UploadOnly` field to `ExtHandshake`, parsing

## BitTorrent Concepts

### What Is a Partial Seed?

A normal **seed** has all pieces and uploads to everyone. A normal **leecher**
downloads pieces and uploads what it has. A **partial seed** is a hybrid:

```
Seed:         Has ALL pieces, uploads only
Leecher:      Has SOME pieces, downloads AND uploads
Partial Seed: Has SOME pieces, uploads only (done downloading what it wants)
```

The most common case: **selective file download**. You download a multi-file
torrent but only wanted 2 of the 10 files. You have all the pieces covering
those 2 files. You don't need anything else, but you can still upload the
pieces you have.

### Why Tell Other Peers?

Without BEP 21, other peers don't know you've stopped downloading. They might:
- Wait for you to reciprocate downloads (tit-for-tat) — but you never will
- Count you as a leecher in their unchoke algorithm — wrong assumption
- Not unchoke you because you never send `Interested` — missed upload opportunity

By sending `upload_only=1`, you tell peers: *"Treat me like a seed. I won't
download from you, but I can upload."*

### How It Works

BEP 21 piggybacks on the extension protocol (BEP 10):

1. **Handshake**: Advertise `upload_only` in the `m` dict and declare initial
   state in a top-level `upload_only` key:
   ```
   {
     "m": {"upload_only": 3},    ← "my sub-ID for this extension is 3"
     "upload_only": 1,           ← "I'm currently a partial seed"
     "v": "Peer Pressure 0.1"
   }
   ```

2. **State changes**: When transitioning to/from partial seed, send an extended
   message (ID 20, sub-ID from handshake) with the payload:
   ```
   {"upload_only": 1}    or    {"upload_only": 0}
   ```

3. **Compatibility**: Some clients send a bare bencoded integer (`i1e`) instead
   of a dict. A robust parser handles both forms.

### Impact on Choking

When you know a peer is `upload_only`:
- **Skip tit-for-tat**: They'll never reciprocate, so don't penalize them
- **Include in optimistic unchoke**: They can still upload useful pieces to you
- **Mark as seed in PEX**: Set the seed flag (0x02) when sharing via BEP 11

## Go Idioms Used

### Type Switch for Polymorphic Parsing

The `upload_only` payload can be either a dict or a bare integer. Go's type
switch handles both cleanly:

```go
switch v := val.(type) {
case bencode.Dict:
    if uoVal, ok := v["upload_only"]; ok {
        if n, ok := uoVal.(bencode.Int); ok {
            return n != 0, nil
        }
    }
    return false, nil
case bencode.Int:
    return v != 0, nil
default:
    return false, fmt.Errorf("unexpected type: %T", val)
}
```

Each `case` branch gets `v` typed to the matched type — no casts needed inside
the branch. The `default` branch catches anything unexpected.

### Zero Value as "Not Set"

Go's zero value for `bool` is `false`, which is exactly the right default for
`UploadOnly` — if the field isn't in the handshake, the peer is not a partial
seed. No pointer or `*bool` needed:

```go
type ExtHandshake struct {
    // ...
    UploadOnly bool  // false by default = downloading normally
}
```

This is a deliberate Go design philosophy: zero values should be useful defaults.

### Thin Message Constructor

`NewUploadOnlyMsg` composes two existing pieces rather than doing everything
from scratch:

```go
func NewUploadOnlyMsg(subID uint8, uploadOnly bool) *Message {
    return NewExtMessage(subID, EncodeUploadOnly(uploadOnly))
}
```

`NewExtMessage` (from BEP 10) handles the extended message framing. 
`EncodeUploadOnly` handles the BEP 21 payload. The constructor is a one-liner
that glues them together. This is composition over inheritance — small,
focused functions composed into larger behavior.

### Testing Both Forms of the Same Message

Since clients in the wild send different formats, we test both:

```go
func TestDecodeUploadOnlyDictTrue(t *testing.T) {
    data := bencode.Encode(bencode.Dict{"upload_only": bencode.Int(1)})
    got, err := DecodeUploadOnly(data)
    // ...
}

func TestDecodeUploadOnlyBareIntTrue(t *testing.T) {
    data := bencode.Encode(bencode.Int(1))
    got, err := DecodeUploadOnly(data)
    // ...
}
```

This pattern — testing spec-compliant AND real-world variant formats — is
essential for BitTorrent interoperability. Specs say one thing; actual clients
do another.

## Test Coverage

| Test | What It Verifies |
|------|-----------------|
| `TestEncodeUploadOnlyTrue` | Encodes dict with `upload_only: 1` |
| `TestEncodeUploadOnlyFalse` | Encodes dict with `upload_only: 0` |
| `TestDecodeUploadOnlyDictTrue` | Parses `{upload_only: 1}` → true |
| `TestDecodeUploadOnlyDictFalse` | Parses `{upload_only: 0}` → false |
| `TestDecodeUploadOnlyBareIntTrue` | Parses bare `i1e` → true |
| `TestDecodeUploadOnlyBareIntFalse` | Parses bare `i0e` → false |
| `TestDecodeUploadOnlyEmpty` | Empty payload → error |
| `TestDecodeUploadOnlyInvalid` | Garbage bytes → error |
| `TestUploadOnlyRoundTrip` | Encode→Decode preserves value |
| `TestNewUploadOnlyMsg` | Full message has correct ID, sub-ID, payload |
| `TestExtHandshakeUploadOnlyTrue` | Handshake with `upload_only: 1` top-level |
| `TestExtHandshakeUploadOnlyAbsent` | Missing key → UploadOnly=false |
| `TestExtHandshakeUploadOnlyInMOnly` | In `m` dict but not top-level → false |
