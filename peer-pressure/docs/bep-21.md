# BEP 21 — Extension for Partial Seeds

> Reference: <https://www.bittorrent.org/beps/bep_0021.html>

---

## 1. Summary

BEP 21 defines a mechanism for peers to announce that they are **partial seeds** — peers that have some pieces but are not interested in downloading any more. A partial seed has the pieces it wants and is only uploading.

**Use cases:**

- **Selective file download:** A peer downloaded only a few files from a multi-file torrent. It has the pieces covering those files and doesn't need the rest. It can still upload the pieces it has.
- **Super-seeding optimization:** A seed can announce upload-only status to tell peers it won't request anything back, which affects choking algorithms.
- **Resource conservation:** Downloaders can deprioritize connecting to partial seeds that only have common pieces.

**How it works:** The `upload_only` flag is carried as an extended message via the Extension Protocol (BEP 10). A peer advertises the `upload_only` extension in its handshake, then sends an `upload_only` extended message when it transitions to or from partial seed state.

Other peers should treat a partial seed like a seed for the purposes of choking/unchoking: the partial seed will never send `interested`, so there is no point waiting for tit-for-tat reciprocation.

---

## 2. Protocol Specification

### 2.1 Extension Handshake

The `upload_only` extension is negotiated via BEP 10. In the extension handshake, a peer that supports this extension includes it in the `m` dictionary:

```
d
  1:md
    11:upload_onlyi3e
  e
  1:v25:Peer Pressure 0.1
e
```

This tells the remote peer: "when I send you extended message sub-ID 3, that's an `upload_only` message."

Additionally, the initial upload-only state can be declared directly in the handshake dict:

```
d
  1:md
    11:upload_onlyi3e
  e
  11:upload_onlyi1e
  1:v25:Peer Pressure 0.1
e
```

The top-level `upload_only` key (outside the `m` dict) carries the current state: `1` means the peer is currently a partial seed, `0` (or absent) means it is not.

### 2.2 Message Format

The `upload_only` extended message is sent whenever the peer's upload-only state changes.

**Wire format:**

```
┌──────────────────────────────────────────────────────┐
│ 4 bytes: message length (big-endian uint32)          │
├──────────────────────────────────────────────────────┤
│ 1 byte: message ID = 20 (MsgExtended)               │
├──────────────────────────────────────────────────────┤
│ 1 byte: extended sub-ID (from peer's handshake "m")  │
├──────────────────────────────────────────────────────┤
│ N bytes: bencoded payload                            │
└──────────────────────────────────────────────────────┘
```

**Payload** is a bencoded dictionary with one key:

```
d
  11:upload_onlyi1e
e
```

- `upload_only` = `1` → peer has transitioned to partial seed (upload-only mode)
- `upload_only` = `0` → peer has resumed downloading (no longer upload-only)

Some implementations send a bare bencoded integer (`i1e` or `i0e`) instead of a dictionary. A robust parser should handle both forms.

### 2.3 State Transitions

```
┌──────────────────┐    download complete      ┌──────────────────┐
│   DOWNLOADING    │   for selected files       │  PARTIAL SEED    │
│ upload_only = 0  ├──────────────────────────►│  upload_only = 1 │
│                  │                            │                  │
│  - interested    │    resume downloading      │  - not interested│
│  - requesting    │◄──────────────────────────┤  - only uploading│
└──────────────────┘                            └──────────────────┘
```

A peer sends `upload_only=1` when:
- It has finished downloading all pieces it wants (selective download complete)
- It transitions to super-seed mode
- It was started as a seed but doesn't have all pieces (e.g., torrent creation interrupted)

A peer sends `upload_only=0` when:
- It decides to resume downloading more pieces (e.g., user selects additional files)

### 2.4 Impact on Choking/Unchoking

When a remote peer is known to be `upload_only`:

1. **Do not wait for reciprocation.** A partial seed will never send `interested`, so tit-for-tat doesn't apply. Treat it like a seed.
2. **Include in optimistic unchoke rotation.** Partial seeds should be unchoked periodically so they can upload to us.
3. **Download normally.** If the partial seed has pieces we need, request them. The `upload_only` flag doesn't mean "don't download from me" — it means "I won't download from you."
4. **Peer exchange (PEX):** When relaying this peer via PEX (BEP 11), set the `seed` flag (`0x02`) in `added.f` if the peer is `upload_only=1`.

---

## 3. Implementation Plan

### 3.1 Files to Modify

**`peer/extension.go`** — Extend the BEP 10 handshake to:
1. Advertise `upload_only` in the `m` dict with a chosen sub-ID.
2. Parse the `upload_only` top-level key from the peer's handshake.

Add to `ExtHandshake`:

```go
type ExtHandshake struct {
    // ... existing fields ...

    // UploadOnly indicates the peer is a partial seed (BEP 21).
    // true = peer is upload-only, false = peer is downloading normally.
    UploadOnly bool
}
```

**`peer/conn.go`** — Add state tracking:

```go
type Conn struct {
    // ... existing fields ...

    // PeerUploadOnly tracks whether the remote peer is a partial seed (BEP 21).
    PeerUploadOnly bool
}
```

Add methods to send and receive `upload_only` messages:

```go
// SendUploadOnly sends a BEP 21 upload_only extended message.
func (c *Conn) SendUploadOnly(uploadOnly bool) error

// handleUploadOnly processes an incoming upload_only extended message.
func (c *Conn) handleUploadOnly(payload []byte) error
```

**`download/pool.go`** — Adjust choking behavior:
- When a peer is `upload_only`, treat it as a seed for unchoke decisions.
- Skip tit-for-tat logic for partial seeds.

### 3.2 Files to Create

**`peer/partialseed.go`** — Encode/decode logic for the `upload_only` extended message:

```go
// EncodeUploadOnly creates the bencoded payload for an upload_only message.
func EncodeUploadOnly(uploadOnly bool) []byte

// DecodeUploadOnly parses an upload_only extended message payload.
// Handles both dict form {"upload_only": N} and bare integer form.
func DecodeUploadOnly(payload []byte) (bool, error)
```

**`peer/partialseed_test.go`** — Tests for encode/decode.

### 3.3 Key Functions

```go
// EncodeUploadOnly builds the bencoded payload for the upload_only message.
// Returns d11:upload_onlyi1ee or d11:upload_onlyi0ee.
func EncodeUploadOnly(uploadOnly bool) []byte

// DecodeUploadOnly parses the upload_only payload.
// Accepts both dict form (d11:upload_onlyi1ee) and bare integer (i1e).
func DecodeUploadOnly(payload []byte) (bool, error)

// NewUploadOnlyMsg creates a full extended message for upload_only.
func NewUploadOnlyMsg(subID uint8, uploadOnly bool) *Message
```

### 3.4 Package Placement

All BEP 21 logic lives in `peer/` since it is a peer-wire extension. The download package only needs minor adjustments to its choking strategy.

---

## 4. Dependencies

| BEP | Relationship |
|---|---|
| **BEP 10** | **Required.** upload_only is carried as an extended message. Negotiated in the BEP 10 handshake |
| **BEP 3** | Base wire protocol — choking/interested states that BEP 21 affects |
| **BEP 11** | PEX — partial seeds should be marked with the seed flag in PEX messages |
| **BEP 53** | File selection — partial seeds arise naturally when a client selects only some files |

### Internal Dependencies

- `peer.ExtHandshake` — extended handshake struct to add `UploadOnly` field
- `peer.NewExtMessage` — for constructing the extended message wrapper
- `bencode.Encode` / `bencode.Decode` — for the message payload

---

## 5. Testing Strategy

### 5.1 Unit Tests (`peer/partialseed_test.go`)

**`TestEncodeUploadOnly`** — Verify bencoded output:
- `EncodeUploadOnly(true)` → bencoded dict with `upload_only` = 1
- `EncodeUploadOnly(false)` → bencoded dict with `upload_only` = 0

**`TestDecodeUploadOnly`** — Verify parsing:
- Standard dict form `d11:upload_onlyi1ee` → `true, nil`
- Standard dict form `d11:upload_onlyi0ee` → `false, nil`
- Bare integer form `i1e` → `true, nil`
- Bare integer form `i0e` → `false, nil`
- Invalid payload → non-nil error
- Empty payload → non-nil error

**`TestUploadOnlyRoundTrip`** — Encode then decode, verify identity for both `true` and `false`.

### 5.2 Extension Handshake Tests (`peer/extension_test.go`)

**`TestExtHandshakeUploadOnly`** — Construct a handshake with `upload_only` in both `m` and top-level:
- Parse it → verify `ExtHandshake.UploadOnly == true`
- Parse handshake without `upload_only` → verify `UploadOnly == false`
- Parse handshake with `upload_only` only in `m` but not top-level → `UploadOnly == false` (state not declared)

### 5.3 Wire-Level Tests

**`TestUploadOnlyOverConn`** — Use `net.Pipe()` to test send/receive:
- Peer A sends `upload_only=1` extended message
- Peer B reads and parses it → verifies `PeerUploadOnly == true`
- Peer A sends `upload_only=0` → Peer B verifies `PeerUploadOnly == false`

**`TestUploadOnlyToggle`** — Verify that a peer can transition back and forth:
- Send `upload_only=1`, then `upload_only=0`, then `upload_only=1` again
- Verify each state change is reflected correctly

### 5.4 Behavioral Tests

**`TestPartialSeedChoking`** — Verify choking logic treats partial seeds as seeds:
- A partial seed peer should be unchoked even if it never sends `interested`
- A partial seed should not count against the tit-for-tat unchoke budget
