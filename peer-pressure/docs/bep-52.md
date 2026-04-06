# BEP 52 — BitTorrent Protocol v2

> Reference: <https://www.bittorrent.org/beps/bep_0052.html>

---

## 1. Summary

BEP 52 is the largest single revision to the BitTorrent protocol. It replaces SHA-1 with SHA-256, restructures piece hashing as per-file merkle trees, and introduces a new info dictionary format. It also defines **hybrid torrents** that contain both v1 and v2 metadata for backward compatibility.

**Key motivations:**

- **SHA-1 weakness:** SHA-1 is cryptographically broken. SHA-256 provides long-term collision resistance.
- **Per-file piece trees:** v1 hashes pieces across concatenated files, making file-level operations (selective download, deduplication, integrity checking) difficult. v2 gives each file its own merkle hash tree.
- **File deduplication:** Because each file's identity is its `pieces root` (the merkle tree root), identical files across different torrents share the same hash — enabling cross-torrent data reuse.
- **Efficient verification:** Merkle proofs let peers verify individual pieces without downloading the entire hash list upfront.

---

## 2. Protocol Specification

### 2.1 Info Dictionary (v2)

A v2 info dictionary uses `meta version` = 2 and replaces `files`/`length` with `file tree`:

```
d
  9:file treed
    11:example.txtd
      0:d
        6:lengthi1048576e
        11:pieces root32:<SHA-256 root, 32 bytes>
      e
    e
    9:photo.jpgd
      0:d
        6:lengthi524288e
        11:pieces root32:<SHA-256 root, 32 bytes>
      e
    e
  e
  12:meta versioni2e
  4:name7:my_data
  12:piece lengthi262144e
e
```

**Removed from v2:** The `pieces` key (flat SHA-1 hash list) is absent. Each file carries its own `pieces root` instead.

**Required keys:**

| Key | Type | Description |
|---|---|---|
| `meta version` | integer | Must be `2` |
| `name` | string | Suggested root directory/file name |
| `piece length` | integer | Bytes per piece; must be a power of 2, minimum 16384 (16 KiB) |
| `file tree` | dict | BEP 47-style file tree (see § 2.2) |

### 2.2 File Tree Structure

The `file tree` is a nested dictionary representing the directory hierarchy:

```
file tree = {
    "dirname": {
        "filename": {
            "": {                        ← empty-string key marks a file leaf
                "length": <int>,
                "pieces root": <32 bytes> ← SHA-256 merkle root
            }
        },
        "subdir": {
            "another.txt": {
                "": {
                    "length": <int>,
                    "pieces root": <32 bytes>
                }
            }
        }
    }
}
```

**Rules:**
- Directories are dicts with string keys for children.
- Files have a single `""` (empty string) key whose value is a dict with `length` and `pieces root`.
- Files of length 0 omit `pieces root` (no data to hash).
- The canonical file ordering is depth-first, with entries sorted lexicographically at each level.
- Padding files are **implicit** in v2 — each file's pieces are independent, so there is no cross-file piece overlap.

### 2.3 Piece Hashing — Per-File Merkle Trees

Each file has its own binary merkle tree built from SHA-256 hashes of its piece data:

```
                    pieces root
                   /            \
              h(0,1)            h(2,3)
             /      \          /      \
         h(p0)    h(p1)    h(p2)    h(p3)
          |        |        |        |
        piece0   piece1   piece2   piece3
```

**Construction algorithm:**

1. Split the file into pieces of `piece length` bytes. The last piece may be shorter.
2. Hash each piece with SHA-256 to get the leaf hashes: `h(piece_i) = SHA-256(piece_data_i)`.
3. If the last piece is shorter than `piece length`, hash it as-is (no padding of the data), but the leaf hash is still placed at its position in the tree.
4. The leaf count must be padded to the next power of 2 using the zero hash: `SHA-256("")` = `e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855`.
5. Build the tree bottom-up: each parent is `SHA-256(left_child || right_child)`.
6. The root hash is the `pieces root` stored in the file tree.

**Byte layout of a leaf hash computation:**

```
leaf_hash = SHA-256(piece_data)

       ┌──────────────────────────────────┐
       │ piece_data (up to piece_length)  │
       └──────────────────────────────────┘
                       │
                   SHA-256
                       │
                       ▼
              ┌────────────────┐
              │  32-byte hash  │  ← leaf of merkle tree
              └────────────────┘
```

**Byte layout of an internal node:**

```
parent_hash = SHA-256(left_child_hash || right_child_hash)

       ┌────────────────┬────────────────┐
       │  left (32 B)   │  right (32 B)  │  ← 64 bytes input
       └────────────────┴────────────────┘
                       │
                   SHA-256
                       │
                       ▼
              ┌────────────────┐
              │  32-byte hash  │  ← parent node
              └────────────────┘
```

### 2.4 Infohash Computation

**v2 infohash:** SHA-256 of the bencoded info dictionary, producing a 32-byte hash.

**v1-compat infohash:** For hybrid torrents (see § 2.6), the v1 infohash is still SHA-1 of the v1 portion of the bencoded info dict.

The v2 infohash is used in:
- Magnet URIs: `xt=urn:btmh:1220<hex_encoded_32_bytes>` (multihash format: `12` = SHA-256, `20` = 32 bytes)
- DHT: truncated to 20 bytes for the existing DHT info_hash field
- Peer wire handshake: truncated to 20 bytes for backward compatibility

### 2.5 Piece Alignment

In v2, pieces are **per-file**:
- Each file's pieces start at offset 0 within that file.
- There are no cross-file pieces.
- Padding is implicit — no need for BEP 47 padding files.
- The last piece of a file may be shorter than `piece length`.

This means a torrent with files A (300 KiB) and B (500 KiB) with piece length 256 KiB has:
- File A: pieces 0–1 (256 KiB + 44 KiB)
- File B: pieces 0–1 (256 KiB + 244 KiB)

Note: piece indices are per-file, not global.

### 2.6 Hybrid Torrents

A hybrid torrent contains both v1 and v2 info to be backward compatible. The info dict has:
- `meta version` = 2
- `file tree` (v2 structure)
- `pieces` (v1 flat SHA-1 hash list)
- `files` or `length` (v1 file list)

Both infohashes are valid. A v2-capable client uses the v2 structures; a v1-only client ignores the v2 keys and uses the v1 structures.

**Hybrid constraints:**
- The v1 and v2 file lists must describe the same files in the same order.
- Padding files in the v1 `files` list correspond to the implicit padding in v2.
- The v1 `pieces` hashes cover the concatenated data including padding (same as standard v1).
- `piece length` must satisfy both v1 and v2 constraints (power of 2, ≥ 16 KiB).

### 2.7 New Wire Protocol Messages

Three new message types for merkle hash tree exchange:

#### Hash Request (ID = 21)

Request hash layers from a peer for a specific file.

```
┌─────────────────────────────────────────────────────────────────┐
│ 4 bytes: message length (big-endian uint32)                     │
├─────────────────────────────────────────────────────────────────┤
│ 1 byte: message ID = 21                                        │
├─────────────────────────────────────────────────────────────────┤
│ 32 bytes: pieces root (identifies the file)                     │
├─────────────────────────────────────────────────────────────────┤
│ 4 bytes: base layer (uint32, 0 = leaf layer)                   │
├─────────────────────────────────────────────────────────────────┤
│ 4 bytes: index (uint32, position within the layer)              │
├─────────────────────────────────────────────────────────────────┤
│ 4 bytes: length (uint32, number of hashes requested)            │
├─────────────────────────────────────────────────────────────────┤
│ 4 bytes: proof layers (uint32, how many uncle-hash layers)      │
└─────────────────────────────────────────────────────────────────┘
```

Total payload: 32 + 4 + 4 + 4 + 4 = 48 bytes.

#### Hashes (ID = 22)

Response containing requested hash data.

```
┌─────────────────────────────────────────────────────────────────┐
│ 4 bytes: message length (big-endian uint32)                     │
├─────────────────────────────────────────────────────────────────┤
│ 1 byte: message ID = 22                                        │
├─────────────────────────────────────────────────────────────────┤
│ 32 bytes: pieces root (identifies the file)                     │
├─────────────────────────────────────────────────────────────────┤
│ 4 bytes: base layer (uint32)                                    │
├─────────────────────────────────────────────────────────────────┤
│ 4 bytes: index (uint32)                                         │
├─────────────────────────────────────────────────────────────────┤
│ 4 bytes: length (uint32)                                        │
├─────────────────────────────────────────────────────────────────┤
│ 4 bytes: proof layers (uint32)                                  │
├─────────────────────────────────────────────────────────────────┤
│ N×32 bytes: hash data (concatenated SHA-256 hashes)             │
└─────────────────────────────────────────────────────────────────┘
```

The `hash data` section contains the requested leaf/node hashes concatenated, followed by the proof (uncle) hashes layer by layer.

#### Hash Reject (ID = 23)

Rejection of a hash request (peer doesn't have the data).

```
┌─────────────────────────────────────────────────────────────────┐
│ 4 bytes: message length (big-endian uint32)                     │
├─────────────────────────────────────────────────────────────────┤
│ 1 byte: message ID = 23                                        │
├─────────────────────────────────────────────────────────────────┤
│ 32 bytes: pieces root                                           │
├─────────────────────────────────────────────────────────────────┤
│ 4 bytes: base layer (uint32)                                    │
├─────────────────────────────────────────────────────────────────┤
│ 4 bytes: index (uint32)                                         │
├─────────────────────────────────────────────────────────────────┤
│ 4 bytes: length (uint32)                                        │
├─────────────────────────────────────────────────────────────────┤
│ 4 bytes: proof layers (uint32)                                  │
└─────────────────────────────────────────────────────────────────┘
```

Same layout as Hash Request (48 bytes payload). Echoes back the request parameters so the requester can match the rejection.

### 2.8 Merkle Proof Verification

When a peer sends piece data, the receiver needs to verify it against the file's `pieces root`. If the receiver doesn't have the full hash tree, it requests proof hashes:

```
Full tree (8 leaves, 3 layers):

Layer 3 (root):              R
                            / \
Layer 2:                  A     B
                         / \   / \
Layer 1:               C   D E   F
                      /\ /\ /\ /\
Layer 0 (leaves):    0 1 2 3 4 5 6 7

To verify leaf 5, you need uncle hashes:
  - hash[4]  (sibling at layer 0)
  - hash[C]  (uncle at layer 1, = hash of leaves 0,1)  — wait, no:
  - hash[E]  = h(4,5)'s sibling is F
  Actually: to verify leaf 5:
    1. h5 = SHA-256(piece_5_data)
    2. Need h4 (sibling) → compute E = SHA-256(h4 || h5)
    3. Need F (uncle at layer 1) → compute B = SHA-256(E || F)
    4. Need A (uncle at layer 2) → compute R = SHA-256(A || B)
    5. Compare R with known pieces root
```

**Proof path for leaf at index `i`:**
1. Start at layer 0 with the hash of the piece data.
2. At each layer, the sibling hash is needed. The sibling index is `i XOR 1`.
3. Combine: if `i` is even, `parent = SHA-256(self || sibling)`. If `i` is odd, `parent = SHA-256(sibling || self)`.
4. Move up: `i = i / 2`, repeat until reaching the root.
5. The computed root must equal the known `pieces root`.

---

## 3. Implementation Plan

### 3.1 New Package: `torrentv2/`

The v2 structures are sufficiently different from v1 that a new package is warranted. The existing `torrent/` package remains for v1. Shared types can be extracted to a common interface or embedded struct.

### 3.2 Files to Create

**`torrentv2/torrentv2.go`** — Core v2 torrent representation:

```go
type Torrent struct {
    InfoHashV2   [32]byte      // SHA-256 of bencoded info dict
    InfoHashV1   [20]byte      // SHA-1 (only for hybrid torrents)
    IsHybrid     bool          // true if both v1 and v2 info present
    Name         string
    PieceLength  int           // must be power of 2, >= 16 KiB
    Files        []File        // flattened from file tree
    Announce     string
    AnnounceList [][]string
}

type File struct {
    Length     int64
    Path       []string
    PiecesRoot [32]byte      // SHA-256 merkle root of this file's pieces
    Attr       string        // BEP 47 attributes
}
```

**`torrentv2/merkle.go`** — Merkle tree construction and verification:

```go
// Tree represents a per-file merkle hash tree.
type Tree struct {
    Layers [][][32]byte // layers[0] = leaves, layers[len-1] = root
}

// BuildTree constructs a merkle tree from piece data hashes.
func BuildTree(leafHashes [][32]byte) *Tree

// Root returns the 32-byte merkle root.
func (t *Tree) Root() [32]byte

// ProofPath returns the uncle hashes needed to verify a leaf at the given index.
func (t *Tree) ProofPath(leafIndex int) [][32]byte

// VerifyProof checks a leaf hash against a pieces root using uncle hashes.
func VerifyProof(piecesRoot [32]byte, leafIndex int, leafHash [32]byte, proof [][32]byte) bool
```

**`torrentv2/merkle_test.go`** — Tests for tree building and proof verification.

**`torrentv2/parse.go`** — Parser for v2 and hybrid torrents:

```go
// Parse decodes a v2 or hybrid .torrent file from raw bytes.
func Parse(data []byte) (*Torrent, error)

// ParseFileTree converts a "file tree" dict to a flat file list.
func ParseFileTree(tree bencode.Dict) ([]File, error)
```

**`torrentv2/parse_test.go`** — Parsing tests.

**`torrentv2/hash.go`** — SHA-256 infohash computation and message handling:

```go
// InfoHash computes the v2 infohash (SHA-256 of bencoded info dict).
func InfoHash(rawInfo []byte) [32]byte

// TruncatedInfoHash returns the first 20 bytes for DHT/handshake compat.
func TruncatedInfoHash(full [32]byte) [20]byte
```

### 3.3 Files to Modify

**`peer/message.go`** — Add new message IDs and constructors:

```go
const (
    MsgHashRequest uint8 = 21
    MsgHashes      uint8 = 22
    MsgHashReject  uint8 = 23
)

// HashRequestPayload holds the fields of a Hash Request message.
type HashRequestPayload struct {
    PiecesRoot  [32]byte
    BaseLayer   uint32
    Index       uint32
    Length      uint32
    ProofLayers uint32
}

func NewHashRequest(p HashRequestPayload) *Message
func ParseHashRequest(payload []byte) (HashRequestPayload, error)

// HashesPayload holds the fields of a Hashes response message.
type HashesPayload struct {
    PiecesRoot  [32]byte
    BaseLayer   uint32
    Index       uint32
    Length      uint32
    ProofLayers uint32
    Hashes      [][32]byte // requested hashes + proof hashes
}

func NewHashes(p HashesPayload) *Message
func ParseHashes(payload []byte) (HashesPayload, error)

func NewHashReject(p HashRequestPayload) *Message
func ParseHashReject(payload []byte) (HashRequestPayload, error)
```

**`peer/conn.go`** — Handle v2 handshake where the infohash may be 20 bytes (truncated SHA-256) or the full 32-byte v2 hash.

**`magnet/magnet.go`** — Support v2 magnet URIs with `xt=urn:btmh:1220<64 hex chars>`:

```go
type Link struct {
    InfoHash   [20]byte // v1 (from urn:btih)
    InfoHashV2 [32]byte // v2 (from urn:btmh)
    HasV2      bool
    // ... existing fields ...
}
```

**`download/pipeline.go`** — For v2 pieces, use SHA-256 instead of SHA-1 for verification. Request merkle proofs when the local hash tree is incomplete.

### 3.4 Key Algorithms

**Merkle tree construction:**

```
function BuildTree(leafHashes):
    // Pad to next power of 2
    n = nextPowerOf2(len(leafHashes))
    zeroHash = SHA-256("")
    while len(leafHashes) < n:
        leafHashes = append(leafHashes, zeroHash)

    layers = [leafHashes]
    current = leafHashes
    while len(current) > 1:
        next = []
        for i = 0; i < len(current); i += 2:
            next = append(next, SHA-256(current[i] || current[i+1]))
        layers = append(layers, next)
        current = next

    return Tree{Layers: layers}
```

**Proof verification:**

```
function VerifyProof(root, leafIndex, leafHash, uncles):
    current = leafHash
    idx = leafIndex
    for each uncle in uncles:
        if idx % 2 == 0:
            current = SHA-256(current || uncle)
        else:
            current = SHA-256(uncle || current)
        idx = idx / 2
    return current == root
```

### 3.5 Package Placement

- `torrentv2/` — new package for v2 torrent parsing, merkle trees, and v2-specific logic
- `peer/` — message definitions shared across v1 and v2 (IDs 21-23 are new additions)
- `magnet/` — extended to support v2 magnet URIs
- `download/` — v2-aware piece verification (SHA-256 + merkle proofs)

---

## 4. Dependencies

| BEP | Relationship |
|---|---|
| **BEP 3** | Base protocol — v2 extends and partially replaces the v1 info dict and piece hashing |
| **BEP 10** | Extension protocol — v2 peers may negotiate additional extensions |
| **BEP 47** | `file tree` structure and file attributes — BEP 52 adopts and extends BEP 47's file tree |
| **BEP 9** | Metadata exchange — magnet links for v2 use `urn:btmh` and the metadata may be larger due to merkle trees |
| **BEP 5** | DHT — v2 infohash is truncated to 20 bytes for DHT compatibility |
| **BEP 53** | File selection — v2's per-file pieces make selective download natural |
| **BEP 23** | Compact peers — unchanged, but tracker responses may carry both v1 and v2 infohashes for hybrid torrents |

### Internal Dependencies

- `bencode/` — for encoding/decoding the v2 info dict
- `torrent/` — shared concepts (piece length, file lists); hybrid torrents need both parsers
- `peer/` — new message types (21, 22, 23) added alongside existing messages
- `magnet/` — v2 magnet URI parsing
- `download/` — piece verification changes from SHA-1 to SHA-256

---

## 5. Testing Strategy

### 5.1 Merkle Tree Tests (`torrentv2/merkle_test.go`)

**`TestBuildTreeSinglePiece`** — One-piece file:
- One leaf → tree has 1 layer (the leaf is the root)
- `Root()` == leaf hash

**`TestBuildTreePowerOfTwo`** — File with exactly 4 pieces:
- 4 leaves → tree has 3 layers (4 leaves, 2 internal, 1 root)
- Root matches manual computation

**`TestBuildTreeNonPowerOfTwo`** — File with 3 pieces:
- 3 leaves padded to 4 with zero hash
- Root matches manual computation with the padding

**`TestBuildTreeLarge`** — File with 1024 pieces:
- Verify tree depth == 11 (log2(1024) + 1)
- Root is deterministic

**`TestProofPathLeaf0`** — Verify proof for leftmost leaf:
- 8-leaf tree → proof has 3 uncle hashes
- Each uncle is the sibling at the corresponding layer

**`TestProofPathLastLeaf`** — Verify proof for rightmost leaf.

**`TestVerifyProofValid`** — Construct tree, extract proof, verify:
- `VerifyProof(root, idx, leafHash, proof)` returns true for every leaf

**`TestVerifyProofTampered`** — Modify one byte of leaf data:
- `VerifyProof` returns false

**`TestVerifyProofWrongIndex`** — Use proof for leaf 3 but claim it's leaf 5:
- `VerifyProof` returns false

**`TestZeroLengthFile`** — File with length 0:
- No merkle tree, `pieces root` is absent

### 5.2 Parsing Tests (`torrentv2/parse_test.go`)

**`TestParseV2Torrent`** — Construct a minimal v2 .torrent in bencode:
- `meta version` = 2, `file tree`, `piece length`
- Verify all fields parse correctly
- Verify `InfoHashV2` is SHA-256 of the raw info dict bytes

**`TestParseHybridTorrent`** — Torrent with both v1 and v2 info:
- Has `pieces`, `files`, `file tree`, `meta version` = 2
- Verify both `InfoHashV1` and `InfoHashV2` are computed
- Verify `IsHybrid == true`

**`TestParsePieceLengthValidation`** — Reject invalid piece lengths:
- Non-power-of-2 → error
- Less than 16384 → error
- Valid power of 2 ≥ 16384 → success

**`TestParseV2MagnetURI`** — Parse `magnet:?xt=urn:btmh:1220<64 hex chars>`:
- Verify `HasV2 == true` and `InfoHashV2` matches

### 5.3 Wire Protocol Tests (`peer/message_test.go`)

**`TestHashRequestRoundTrip`** — Construct, serialize, deserialize:
- All fields preserved through encode/decode cycle
- Payload is exactly 48 bytes

**`TestHashesRoundTrip`** — Same for Hashes message:
- Variable-length hash data is correctly serialized

**`TestHashRejectRoundTrip`** — Same for Hash Reject.

### 5.4 Integration Tests

**`TestV2PieceVerification`** — End-to-end hash verification:
- Create a small file, compute its merkle tree
- "Download" pieces and verify each against the tree root using proofs

**`TestHybridTorrentDownload`** — Verify that a hybrid torrent can be downloaded using either v1 or v2 paths:
- Using v1 path: SHA-1 piece hashes, flat file list
- Using v2 path: SHA-256 merkle verification, per-file pieces

**`TestCrossTorrentDedup`** — Two torrents sharing an identical file:
- Both files produce the same `pieces root`
- Data downloaded for one torrent can satisfy the other
