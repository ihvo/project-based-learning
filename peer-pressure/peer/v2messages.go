package peer

import (
	"encoding/binary"
	"fmt"
)

// HashRequest represents a BEP 52 hash request message (msg ID 21).
//
// Fields:
//   PiecesRoot  [32]byte — root hash of the file's Merkle tree
//   BaseLayer   uint32   — lowest requested layer (0 = leaf hashes)
//   Index       uint32   — offset of first hash in the base layer
//   Length      uint32   — number of hashes (power of 2, >= 2)
//   ProofLayers uint32   — number of ancestor layers to include
type HashRequest struct {
	PiecesRoot  [32]byte
	BaseLayer   uint32
	Index       uint32
	Length      uint32
	ProofLayers uint32
}

// HashRequestSize is the fixed payload size for hash request/reject messages.
const HashRequestSize = 32 + 4*4 // 48 bytes

// EncodeHashRequest serializes a HashRequest into wire format.
func EncodeHashRequest(r HashRequest) []byte {
	buf := make([]byte, HashRequestSize)
	copy(buf[:32], r.PiecesRoot[:])
	binary.BigEndian.PutUint32(buf[32:], r.BaseLayer)
	binary.BigEndian.PutUint32(buf[36:], r.Index)
	binary.BigEndian.PutUint32(buf[40:], r.Length)
	binary.BigEndian.PutUint32(buf[44:], r.ProofLayers)
	return buf
}

// DecodeHashRequest parses a HashRequest from wire format.
func DecodeHashRequest(data []byte) (HashRequest, error) {
	if len(data) < HashRequestSize {
		return HashRequest{}, fmt.Errorf("hash request too short: %d bytes", len(data))
	}
	var r HashRequest
	copy(r.PiecesRoot[:], data[:32])
	r.BaseLayer = binary.BigEndian.Uint32(data[32:])
	r.Index = binary.BigEndian.Uint32(data[36:])
	r.Length = binary.BigEndian.Uint32(data[40:])
	r.ProofLayers = binary.BigEndian.Uint32(data[44:])
	return r, nil
}

// Hashes represents a BEP 52 hashes response message (msg ID 22).
//
// Fields mirror HashRequest, plus the actual hash data:
//   PiecesRoot  [32]byte
//   BaseLayer   uint32
//   Index       uint32
//   Length      uint32
//   ProofLayers uint32
//   HashData    []byte — concatenated 32-byte SHA-256 hashes: base layer
//                        hashes followed by proof (uncle) hashes
type Hashes struct {
	PiecesRoot  [32]byte
	BaseLayer   uint32
	Index       uint32
	Length      uint32
	ProofLayers uint32
	HashData    []byte
}

// EncodeHashes serializes a Hashes message into wire format.
func EncodeHashes(h Hashes) []byte {
	buf := make([]byte, HashRequestSize+len(h.HashData))
	copy(buf[:32], h.PiecesRoot[:])
	binary.BigEndian.PutUint32(buf[32:], h.BaseLayer)
	binary.BigEndian.PutUint32(buf[36:], h.Index)
	binary.BigEndian.PutUint32(buf[40:], h.Length)
	binary.BigEndian.PutUint32(buf[44:], h.ProofLayers)
	copy(buf[HashRequestSize:], h.HashData)
	return buf
}

// DecodeHashes parses a Hashes message from wire format.
func DecodeHashes(data []byte) (Hashes, error) {
	if len(data) < HashRequestSize {
		return Hashes{}, fmt.Errorf("hashes message too short: %d bytes", len(data))
	}
	var h Hashes
	copy(h.PiecesRoot[:], data[:32])
	h.BaseLayer = binary.BigEndian.Uint32(data[32:])
	h.Index = binary.BigEndian.Uint32(data[36:])
	h.Length = binary.BigEndian.Uint32(data[40:])
	h.ProofLayers = binary.BigEndian.Uint32(data[44:])
	if len(data) > HashRequestSize {
		h.HashData = make([]byte, len(data)-HashRequestSize)
		copy(h.HashData, data[HashRequestSize:])
	}
	return h, nil
}

// ExtractHashes splits the concatenated HashData into individual 32-byte hashes.
func ExtractHashes(data []byte) ([][32]byte, error) {
	if len(data)%32 != 0 {
		return nil, fmt.Errorf("hash data length %d not a multiple of 32", len(data))
	}
	count := len(data) / 32
	hashes := make([][32]byte, count)
	for i := range count {
		copy(hashes[i][:], data[i*32:(i+1)*32])
	}
	return hashes, nil
}
