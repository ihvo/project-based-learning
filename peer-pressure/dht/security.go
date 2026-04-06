package dht

import (
	"crypto/rand"
	"encoding/binary"
	"hash/crc32"
	"net"
	"sync"
)

// CRC32C (Castagnoli) table for BEP 42 node ID generation.
var crc32cTable = crc32.MakeTable(crc32.Castagnoli)

// IPv4 and IPv6 masks per BEP 42.
var (
	v4Mask = []byte{0x03, 0x0f, 0x3f, 0xff}
	v6Mask = []byte{0x01, 0x03, 0x07, 0x0f, 0x1f, 0x3f, 0x7f, 0xff}
)

// GenerateSecureNodeID creates a BEP 42 compliant node ID whose first 21 bits
// are derived from the external IP address via CRC32C.
func GenerateSecureNodeID(ip net.IP) NodeID {
	var id NodeID
	rand.Read(id[:])

	ip4 := ip.To4()
	if ip4 != nil {
		applyBEP42(&id, ip4, v4Mask)
	} else if ip6 := ip.To16(); ip6 != nil {
		applyBEP42(&id, ip6[:8], v6Mask)
	}
	return id
}

// applyBEP42 sets the first 21 bits and the last byte of id according to BEP 42.
func applyBEP42(id *NodeID, ipBytes []byte, mask []byte) {
	r := id[19] & 0x07 // random 3-bit value

	buf := make([]byte, len(mask))
	for i := range buf {
		buf[i] = ipBytes[i] & mask[i]
	}
	buf[0] |= r << 5

	// Hash exactly num_octets bytes (4 for IPv4, 8 for IPv6).
	crc := crc32.Checksum(buf, crc32cTable)

	id[0] = byte(crc >> 24)
	id[1] = byte(crc >> 16)
	id[2] = (byte(crc>>8) & 0xf8) | (id[2] & 0x07) // top 5 bits from CRC, low 3 random
	id[19] = r | (id[19] & 0xf8)                     // low 3 bits = r, rest random
}

// ValidateNodeID checks whether a node ID's first 21 bits match the BEP 42
// derivation from the given IP address. Local/private IPs are always valid.
func ValidateNodeID(id NodeID, ip net.IP) bool {
	if isLocalIP(ip) {
		return true
	}

	ip4 := ip.To4()
	if ip4 != nil {
		return validateBEP42(id, ip4, v4Mask)
	}
	if ip6 := ip.To16(); ip6 != nil {
		return validateBEP42(id, ip6[:8], v6Mask)
	}
	return false
}

// validateBEP42 checks the 21-bit prefix constraint.
func validateBEP42(id NodeID, ipBytes []byte, mask []byte) bool {
	r := id[19] & 0x07

	buf := make([]byte, len(mask))
	for i := range buf {
		buf[i] = ipBytes[i] & mask[i]
	}
	buf[0] |= r << 5

	crc := crc32.Checksum(buf, crc32cTable)

	// Check first 21 bits: bytes 0, 1 fully, byte 2 top 5 bits.
	if id[0] != byte(crc>>24) {
		return false
	}
	if id[1] != byte(crc>>16) {
		return false
	}
	if (id[2] & 0xf8) != (byte(crc>>8) & 0xf8) {
		return false
	}
	return true
}

// isLocalIP returns true for IPs exempt from BEP 42 validation.
func isLocalIP(ip net.IP) bool {
	ip4 := ip.To4()
	if ip4 == nil {
		// IPv6 loopback.
		return ip.Equal(net.IPv6loopback)
	}
	// 10.0.0.0/8
	if ip4[0] == 10 {
		return true
	}
	// 172.16.0.0/12
	if ip4[0] == 172 && ip4[1] >= 16 && ip4[1] <= 31 {
		return true
	}
	// 192.168.0.0/16
	if ip4[0] == 192 && ip4[1] == 168 {
		return true
	}
	// 169.254.0.0/16
	if ip4[0] == 169 && ip4[1] == 254 {
		return true
	}
	// 127.0.0.0/8
	if ip4[0] == 127 {
		return true
	}
	return false
}

// ParseIPField extracts the external IP from the "ip" key in a KRPC response.
// BEP 42 specifies compact binary: 4-byte IPv4 or 16-byte IPv6 + 2-byte port.
func ParseIPField(data []byte) net.IP {
	switch len(data) {
	case 6:
		return net.IP(data[:4])
	case 18:
		return net.IP(data[:16])
	default:
		return nil
	}
}

// ExternalIPVote tracks IP votes from DHT responses for self-detection.
type ExternalIPVote struct {
	votes map[string]int
	mu    sync.Mutex
}

// NewExternalIPVote creates a new vote tracker.
func NewExternalIPVote() *ExternalIPVote {
	return &ExternalIPVote{votes: make(map[string]int)}
}

// Add records a vote for an IP address.
func (v *ExternalIPVote) Add(ip net.IP) {
	if ip == nil {
		return
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	v.votes[ip.String()]++
}

// Winner returns the IP with the most votes, or nil if no votes.
func (v *ExternalIPVote) Winner() net.IP {
	v.mu.Lock()
	defer v.mu.Unlock()

	var best string
	var bestCount int
	for ip, count := range v.votes {
		if count > bestCount {
			best = ip
			bestCount = count
		}
	}
	if best == "" {
		return nil
	}
	return net.ParseIP(best)
}

// Count returns the total number of votes.
func (v *ExternalIPVote) Count() int {
	v.mu.Lock()
	defer v.mu.Unlock()
	total := 0
	for _, c := range v.votes {
		total += c
	}
	return total
}

// EncodeIPField encodes an IP+port into compact binary for the "ip" response key.
func EncodeIPField(ip net.IP, port uint16) []byte {
	ip4 := ip.To4()
	if ip4 != nil {
		buf := make([]byte, 6)
		copy(buf, ip4)
		binary.BigEndian.PutUint16(buf[4:], port)
		return buf
	}
	ip6 := ip.To16()
	if ip6 != nil {
		buf := make([]byte, 18)
		copy(buf, ip6)
		binary.BigEndian.PutUint16(buf[16:], port)
		return buf
	}
	return nil
}
