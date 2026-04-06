package peer

import (
	"crypto/sha1"
	"encoding/binary"
	"net"
)

// AllowedFastSet computes the deterministic set of piece indices that a
// peer may download while choked, per the BEP 6 algorithm.
//
// The algorithm is only defined for IPv4. Returns nil for IPv6 addresses.
func AllowedFastSet(peerIP net.IP, infoHash [20]byte, numPieces, k int) []uint32 {
	ip4 := peerIP.To4()
	if ip4 == nil {
		return nil
	}

	// Can't produce more unique indices than pieces exist.
	if k > numPieces {
		k = numPieces
	}

	// Mask to /24
	var masked [4]byte
	copy(masked[:], ip4)
	masked[3] = 0

	// x = SHA-1(masked_ip + infohash)
	h := sha1.New()
	h.Write(masked[:])
	h.Write(infoHash[:])
	x := h.Sum(nil)

	seen := make(map[uint32]bool)
	var result []uint32

	for len(result) < k {
		for j := 0; j < 5 && len(result) < k; j++ {
			idx := binary.BigEndian.Uint32(x[j*4:j*4+4]) % uint32(numPieces)
			if !seen[idx] {
				seen[idx] = true
				result = append(result, idx)
			}
		}
		// Re-hash
		h.Reset()
		h.Write(x)
		x = h.Sum(nil)
	}

	return result
}
