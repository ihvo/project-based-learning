package peer

import (
	"bytes"
	"crypto/sha256"
	"testing"
)

func TestHashRequestRoundTrip(t *testing.T) {
	root := sha256.Sum256([]byte("test file data"))
	req := HashRequest{
		PiecesRoot:  root,
		BaseLayer:   0,
		Index:       0,
		Length:      4,
		ProofLayers: 2,
	}

	data := EncodeHashRequest(req)
	if len(data) != HashRequestSize {
		t.Fatalf("encoded size = %d, want %d", len(data), HashRequestSize)
	}

	got, err := DecodeHashRequest(data)
	if err != nil {
		t.Fatalf("DecodeHashRequest: %v", err)
	}

	if got.PiecesRoot != req.PiecesRoot {
		t.Error("PiecesRoot mismatch")
	}
	if got.BaseLayer != req.BaseLayer {
		t.Errorf("BaseLayer = %d", got.BaseLayer)
	}
	if got.Index != req.Index {
		t.Errorf("Index = %d", got.Index)
	}
	if got.Length != req.Length {
		t.Errorf("Length = %d", got.Length)
	}
	if got.ProofLayers != req.ProofLayers {
		t.Errorf("ProofLayers = %d", got.ProofLayers)
	}
}

func TestHashRequestTooShort(t *testing.T) {
	_, err := DecodeHashRequest(make([]byte, 10))
	if err == nil {
		t.Error("expected error for short data")
	}
}

func TestHashesRoundTrip(t *testing.T) {
	root := sha256.Sum256([]byte("file root"))
	h1 := sha256.Sum256([]byte("hash 1"))
	h2 := sha256.Sum256([]byte("hash 2"))

	hashData := make([]byte, 64)
	copy(hashData[:32], h1[:])
	copy(hashData[32:], h2[:])

	msg := Hashes{
		PiecesRoot:  root,
		BaseLayer:   0,
		Index:       0,
		Length:      2,
		ProofLayers: 0,
		HashData:    hashData,
	}

	data := EncodeHashes(msg)
	if len(data) != HashRequestSize+64 {
		t.Fatalf("encoded size = %d, want %d", len(data), HashRequestSize+64)
	}

	got, err := DecodeHashes(data)
	if err != nil {
		t.Fatalf("DecodeHashes: %v", err)
	}

	if got.PiecesRoot != msg.PiecesRoot {
		t.Error("PiecesRoot mismatch")
	}
	if got.Length != 2 {
		t.Errorf("Length = %d", got.Length)
	}
	if !bytes.Equal(got.HashData, hashData) {
		t.Error("HashData mismatch")
	}
}

func TestHashesNoHashData(t *testing.T) {
	msg := Hashes{
		PiecesRoot: sha256.Sum256([]byte("empty")),
		Length:     2,
	}

	data := EncodeHashes(msg)
	got, err := DecodeHashes(data)
	if err != nil {
		t.Fatalf("DecodeHashes: %v", err)
	}
	if len(got.HashData) != 0 {
		t.Errorf("HashData should be empty, got %d bytes", len(got.HashData))
	}
}

func TestHashesTooShort(t *testing.T) {
	_, err := DecodeHashes(make([]byte, 10))
	if err == nil {
		t.Error("expected error for short data")
	}
}

func TestExtractHashes(t *testing.T) {
	h1 := sha256.Sum256([]byte("a"))
	h2 := sha256.Sum256([]byte("b"))

	data := make([]byte, 64)
	copy(data[:32], h1[:])
	copy(data[32:], h2[:])

	hashes, err := ExtractHashes(data)
	if err != nil {
		t.Fatalf("ExtractHashes: %v", err)
	}
	if len(hashes) != 2 {
		t.Fatalf("got %d hashes", len(hashes))
	}
	if hashes[0] != h1 {
		t.Error("hash 0 mismatch")
	}
	if hashes[1] != h2 {
		t.Error("hash 1 mismatch")
	}
}

func TestExtractHashesBadLength(t *testing.T) {
	_, err := ExtractHashes(make([]byte, 33)) // not multiple of 32
	if err == nil {
		t.Error("expected error for non-multiple-of-32 length")
	}
}

func TestExtractHashesEmpty(t *testing.T) {
	hashes, err := ExtractHashes(nil)
	if err != nil {
		t.Fatalf("ExtractHashes nil: %v", err)
	}
	if len(hashes) != 0 {
		t.Errorf("expected 0 hashes, got %d", len(hashes))
	}
}

func TestV2MessageIDs(t *testing.T) {
	if MsgHashRequest != 21 {
		t.Errorf("MsgHashRequest = %d", MsgHashRequest)
	}
	if MsgHashes != 22 {
		t.Errorf("MsgHashes = %d", MsgHashes)
	}
	if MsgHashReject != 23 {
		t.Errorf("MsgHashReject = %d", MsgHashReject)
	}
}
