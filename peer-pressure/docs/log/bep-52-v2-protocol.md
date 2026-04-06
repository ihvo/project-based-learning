# BEP 52: BitTorrent Protocol v2

## What It Does

BEP 52 is the second major version of the BitTorrent protocol. The biggest
change: **per-file Merkle hash trees** using SHA-256, replacing v1's flat
SHA-1 piece hashes. This enables file deduplication across torrents, more
efficient verification, and resistance against SHA-1 collision attacks.

### Key Changes from v1

| Feature | v1 (BEP 3) | v2 (BEP 52) |
|---------|-----------|-------------|
| Hash function | SHA-1 (160-bit) | SHA-256 (256-bit) |
| Piece hashing | Flat list of piece hashes | Merkle tree per file |
| Info hash | SHA-1 of info dict | SHA-256 of info dict (truncated to 20B for trackers) |
| File alignment | Files span pieces arbitrarily | Each file starts at piece boundary |
| Deduplication | Impossible (pieces cross files) | Same file → same pieces root in any torrent |
| New messages | — | hash request (21), hashes (22), hash reject (23) |

### Merkle Hash Trees

Each file in a v2 torrent has its own Merkle tree built from 16 KiB blocks:

```
         [root]           ← "pieces root" stored in torrent metadata
        /      \
     [H01]    [H23]       ← intermediate layer
     /   \    /   \
  [H0]  [H1] [H2] [H3]   ← leaf layer = SHA-256 of each 16 KiB block
```

- Leaf count is padded to the next power of 2 (padding leaves = zero hash)
- Each inner node = `SHA-256(left_child || right_child)`
- The root hash is the file's **pieces root**

### Why Merkle Trees?

1. **Incremental verification**: Download a few blocks + their proof path
   to verify against the root, without needing all hashes upfront
2. **Selective download**: Request specific hash subtrees for files you want
3. **File deduplication**: Same file content → same pieces root, regardless
   of which torrent it's in

### Wire Protocol: Hash Messages

Peers exchange Merkle tree layers via three new messages:

**Hash Request (msg 21)**: "Give me hashes from this file's tree"
```
pieces_root [32B] | base_layer [4B] | index [4B] | length [4B] | proof_layers [4B]
```
- `base_layer = 0` means leaf hashes; higher = coarser layers
- `index` + `length` select a range of hashes in that layer
- `proof_layers` requests uncle hashes for verification

**Hashes (msg 22)**: Same header + concatenated 32-byte hashes
**Hash Reject (msg 23)**: Same as request header, indicates refusal

### Hybrid Torrents

For backward compatibility, a torrent can include **both** v1 and v2 metadata:
- v1: `pieces` field (flat SHA-1 hashes) + traditional `files`/`length`
- v2: `file tree` with per-file Merkle roots + `meta version: 2`

Clients supporting both join both swarms simultaneously. They verify pieces
against both hash formats. If any inconsistency is detected → abort.

### What We Implemented

- **Merkle tree**: `MerkleHash()` builds root from raw data, `MerkleLeaves()`
  returns the padded leaf layer, `MerkleLayers()` returns all tree levels
- **Proof verification**: `VerifyMerkleProof()` validates a leaf against the
  root using uncle hashes
- **V2 metadata types**: `V2FileEntry`, `V2Info` structs
- **Wire messages**: `HashRequest`, `Hashes` encode/decode, `ExtractHashes`
- **Hybrid detection**: `IsHybrid()` checks for both v1 pieces and meta v2

## Go Idioms

### Power-of-Two Padding

```go
func nextPow2(n int) int {
    if n <= 1 { return 1 }
    p := 1
    for p < n { p *= 2 }
    return p
}
```

Merkle trees require power-of-two leaf counts. This simple loop avoids
bit-manipulation tricks that are harder to verify for correctness. For the
small values we deal with (max ~tens of thousands of leaves), the loop
completes in at most 20 iterations.

### Building Trees Bottom-Up

```go
func merkleRoot(layer [][32]byte) [32]byte {
    for len(layer) > 1 {
        next := make([][32]byte, len(layer)/2)
        for i := range next {
            next[i] = hashPair(layer[i*2], layer[i*2+1])
        }
        layer = next
    }
    return layer[0]
}
```

Each iteration halves the layer. Since we start with a power-of-two count,
every layer divides evenly. Allocating a new slice per layer is simple and
avoids the complexity of in-place tree construction. For a typical 256 MiB
file (~16K leaves), this is 14 iterations of decreasing work.

### Fixed-Size Hash Arrays

```go
type V2FileEntry struct {
    PiecesRoot [32]byte
}
```

Using `[32]byte` instead of `[]byte` for SHA-256 hashes gives us:
- **Value semantics**: can compare with `==`, use as map keys
- **Stack allocation**: no heap escape for small, temporary hashes
- **Type safety**: can't accidentally assign a SHA-1 `[20]byte` to a
  SHA-256 field

### Proof Verification with Index Tracking

```go
func VerifyMerkleProof(root, leaf [32]byte, leafIndex int, proof [][32]byte) bool {
    current := leaf
    idx := leafIndex
    for _, uncle := range proof {
        if idx%2 == 0 {
            current = hashPair(current, uncle)
        } else {
            current = hashPair(uncle, current)
        }
        idx /= 2
    }
    return current == root
}
```

The leaf index determines which side of each pair the current hash sits on.
Even index → current is left child; odd → right child. Dividing by 2 moves
up one layer. This is the standard Merkle proof algorithm — O(log n) hashes
to verify any single leaf.
