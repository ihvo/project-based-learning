package torrent

import (
	"crypto/sha256"
	"fmt"
)

// MerkleBlockSize is the leaf block size for BEP 52 Merkle trees: 16 KiB.
const MerkleBlockSize = 16384

// MerkleHash computes the SHA-256 Merkle tree root for a piece of data.
// The data is split into 16 KiB blocks; each block is hashed to form the
// leaf layer. Leaves are paired and hashed up to the root. If a layer has
// an odd number of nodes, the last node is paired with a zero-filled hash.
func MerkleHash(data []byte) [32]byte {
	if len(data) == 0 {
		return sha256.Sum256(nil)
	}

	// Build leaf layer.
	numLeaves := (len(data) + MerkleBlockSize - 1) / MerkleBlockSize
	// Pad to next power of two.
	paddedCount := nextPow2(numLeaves)

	leaves := make([][32]byte, paddedCount)
	for i := range numLeaves {
		start := i * MerkleBlockSize
		end := start + MerkleBlockSize
		if end > len(data) {
			end = len(data)
		}
		leaves[i] = sha256.Sum256(data[start:end])
	}
	// Remaining leaves are zero (padding nodes).

	return merkleRoot(leaves)
}

// MerkleLeaves computes the leaf layer (SHA-256 of each 16 KiB block),
// padded to a power-of-two count with zero hashes.
func MerkleLeaves(data []byte) [][32]byte {
	if len(data) == 0 {
		return [][32]byte{sha256.Sum256(nil)}
	}

	numLeaves := (len(data) + MerkleBlockSize - 1) / MerkleBlockSize
	paddedCount := nextPow2(numLeaves)

	leaves := make([][32]byte, paddedCount)
	for i := range numLeaves {
		start := i * MerkleBlockSize
		end := start + MerkleBlockSize
		if end > len(data) {
			end = len(data)
		}
		leaves[i] = sha256.Sum256(data[start:end])
	}
	return leaves
}

// MerkleRootFromLeaves computes the root from a pre-computed leaf layer.
// The input must have power-of-two length.
func MerkleRootFromLeaves(leaves [][32]byte) [32]byte {
	if len(leaves) == 0 {
		return sha256.Sum256(nil)
	}
	layer := make([][32]byte, len(leaves))
	copy(layer, leaves)
	return merkleRoot(layer)
}

// MerkleLayers computes all layers of the tree from leaves to root.
// layers[0] = leaves, layers[len-1] = [root]. Each layer has half the
// nodes of the previous one.
func MerkleLayers(leaves [][32]byte) [][][32]byte {
	if len(leaves) == 0 {
		return nil
	}

	var layers [][][32]byte
	layer := make([][32]byte, len(leaves))
	copy(layer, leaves)
	layers = append(layers, layer)

	for len(layer) > 1 {
		next := make([][32]byte, len(layer)/2)
		for i := range next {
			next[i] = hashPair(layer[i*2], layer[i*2+1])
		}
		layers = append(layers, next)
		layer = next
	}
	return layers
}

// VerifyMerkleProof verifies a leaf hash against the root using uncle hashes.
// leafIndex is the 0-based index in the leaf layer. proof contains uncle
// hashes from the leaf's sibling up towards the root (one per layer).
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

func hashPair(left, right [32]byte) [32]byte {
	var combined [64]byte
	copy(combined[:32], left[:])
	copy(combined[32:], right[:])
	return sha256.Sum256(combined[:])
}

func nextPow2(n int) int {
	if n <= 1 {
		return 1
	}
	p := 1
	for p < n {
		p *= 2
	}
	return p
}

// V2FileEntry represents a file in a BEP 52 v2 torrent.
type V2FileEntry struct {
	Path       []string // path components
	Length     int64    // file size in bytes
	PiecesRoot [32]byte // Merkle root of the file's piece hashes
}

// V2Info holds the v2-specific metadata from a torrent.
type V2Info struct {
	MetaVersion int          // should be 2
	FileTree    []V2FileEntry
	PieceLength int
}

// String returns a summary of the v2 info.
func (v *V2Info) String() string {
	return fmt.Sprintf("v2 torrent: %d files, piece_length=%d", len(v.FileTree), v.PieceLength)
}

// IsHybrid returns true if a torrent contains both v1 (pieces) and v2 (file tree)
// metadata, enabling cross-swarm interoperability.
func IsHybrid(hasV1Pieces bool, metaVersion int) bool {
	return hasV1Pieces && metaVersion == 2
}
