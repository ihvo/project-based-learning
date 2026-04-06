package torrent

import (
	"bytes"
	"crypto/sha256"
	"testing"
)

func TestMerkleHashSingleBlock(t *testing.T) {
	data := bytes.Repeat([]byte{0xAA}, 1000) // < 16 KiB
	root := MerkleHash(data)

	// Single block → root = SHA-256(data), since padded to 1 leaf = root.
	want := sha256.Sum256(data)
	if root != want {
		t.Errorf("single block root mismatch")
	}
}

func TestMerkleHashExactBlock(t *testing.T) {
	data := bytes.Repeat([]byte{0xBB}, MerkleBlockSize) // exactly 16 KiB
	root := MerkleHash(data)

	want := sha256.Sum256(data)
	if root != want {
		t.Errorf("exact block root mismatch")
	}
}

func TestMerkleHashTwoBlocks(t *testing.T) {
	data := bytes.Repeat([]byte{0xCC}, MerkleBlockSize+1) // 2 blocks
	root := MerkleHash(data)

	leaf0 := sha256.Sum256(data[:MerkleBlockSize])
	leaf1 := sha256.Sum256(data[MerkleBlockSize:])

	var combined [64]byte
	copy(combined[:32], leaf0[:])
	copy(combined[32:], leaf1[:])
	want := sha256.Sum256(combined[:])

	if root != want {
		t.Errorf("two-block root mismatch")
	}
}

func TestMerkleHashThreeBlocks(t *testing.T) {
	// 3 blocks → padded to 4 leaves (power of 2).
	data := bytes.Repeat([]byte{0xDD}, MerkleBlockSize*3)
	root := MerkleHash(data)

	leaves := MerkleLeaves(data)
	if len(leaves) != 4 {
		t.Fatalf("expected 4 padded leaves, got %d", len(leaves))
	}

	rootFromLeaves := MerkleRootFromLeaves(leaves)
	if root != rootFromLeaves {
		t.Errorf("root from data != root from leaves")
	}
}

func TestMerkleHashEmpty(t *testing.T) {
	root := MerkleHash(nil)
	want := sha256.Sum256(nil)
	if root != want {
		t.Errorf("empty data root mismatch")
	}
}

func TestMerkleLeavesCount(t *testing.T) {
	tests := []struct {
		name      string
		dataSize  int
		wantLeaves int
	}{
		{"1 byte", 1, 1},
		{"16 KiB", MerkleBlockSize, 1},
		{"16 KiB + 1", MerkleBlockSize + 1, 2},
		{"32 KiB", MerkleBlockSize * 2, 2},
		{"48 KiB", MerkleBlockSize * 3, 4}, // padded to 4
		{"64 KiB", MerkleBlockSize * 4, 4},
		{"65 KiB", MerkleBlockSize*4 + 1024, 8}, // 5 → padded to 8
	}

	for _, tc := range tests {
		data := make([]byte, tc.dataSize)
		leaves := MerkleLeaves(data)
		if len(leaves) != tc.wantLeaves {
			t.Errorf("%s: got %d leaves, want %d", tc.name, len(leaves), tc.wantLeaves)
		}
	}
}

func TestMerkleLayers(t *testing.T) {
	data := bytes.Repeat([]byte{0xEE}, MerkleBlockSize*4) // 4 blocks
	leaves := MerkleLeaves(data)
	layers := MerkleLayers(leaves)

	// 4 leaves → 3 layers: [4], [2], [1]
	if len(layers) != 3 {
		t.Fatalf("expected 3 layers, got %d", len(layers))
	}
	if len(layers[0]) != 4 {
		t.Errorf("leaf layer has %d nodes", len(layers[0]))
	}
	if len(layers[1]) != 2 {
		t.Errorf("middle layer has %d nodes", len(layers[1]))
	}
	if len(layers[2]) != 1 {
		t.Errorf("root layer has %d nodes", len(layers[2]))
	}

	root := MerkleRootFromLeaves(leaves)
	if layers[2][0] != root {
		t.Errorf("top layer[0] != root")
	}
}

func TestVerifyMerkleProof(t *testing.T) {
	data := bytes.Repeat([]byte{0xFF}, MerkleBlockSize*4) // 4 blocks
	leaves := MerkleLeaves(data)
	layers := MerkleLayers(leaves)
	root := layers[len(layers)-1][0]

	// Build proof for leaf 0: uncle is leaf 1, then parent's sibling.
	proof := [][32]byte{
		layers[0][1], // sibling at leaf layer
		layers[1][1], // uncle at next layer
	}

	if !VerifyMerkleProof(root, leaves[0], 0, proof) {
		t.Error("valid proof for leaf 0 rejected")
	}

	// Bad proof: corrupt one uncle hash.
	badProof := make([][32]byte, len(proof))
	copy(badProof, proof)
	badProof[0][0] ^= 0xFF
	if VerifyMerkleProof(root, leaves[0], 0, badProof) {
		t.Error("corrupted proof should fail")
	}
}

func TestVerifyMerkleProofLeaf2(t *testing.T) {
	data := bytes.Repeat([]byte{0xAB}, MerkleBlockSize*4)
	leaves := MerkleLeaves(data)
	layers := MerkleLayers(leaves)
	root := layers[len(layers)-1][0]

	// Proof for leaf 2: sibling is leaf 3, uncle is layers[1][0].
	proof := [][32]byte{
		layers[0][3], // sibling of leaf 2
		layers[1][0], // uncle at next layer
	}

	if !VerifyMerkleProof(root, leaves[2], 2, proof) {
		t.Error("valid proof for leaf 2 rejected")
	}
}

func TestNextPow2(t *testing.T) {
	tests := []struct {
		n, want int
	}{
		{0, 1}, {1, 1}, {2, 2}, {3, 4}, {4, 4},
		{5, 8}, {7, 8}, {8, 8}, {9, 16},
	}
	for _, tc := range tests {
		if got := nextPow2(tc.n); got != tc.want {
			t.Errorf("nextPow2(%d) = %d, want %d", tc.n, got, tc.want)
		}
	}
}

func TestIsHybrid(t *testing.T) {
	if !IsHybrid(true, 2) {
		t.Error("should be hybrid with v1 pieces and meta_version=2")
	}
	if IsHybrid(false, 2) {
		t.Error("not hybrid without v1 pieces")
	}
	if IsHybrid(true, 1) {
		t.Error("not hybrid with meta_version=1")
	}
}

func TestV2InfoString(t *testing.T) {
	v := V2Info{MetaVersion: 2, PieceLength: 262144, FileTree: []V2FileEntry{
		{Path: []string{"test.txt"}, Length: 1024},
	}}
	s := v.String()
	if s != "v2 torrent: 1 files, piece_length=262144" {
		t.Errorf("unexpected string: %s", s)
	}
}
