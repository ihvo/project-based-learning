package peer

import (
	"bytes"
	"hash/crc32"
	"net"
)

var crc32cTable = crc32.MakeTable(crc32.Castagnoli)

// IPv4 masks per BEP 40.
var (
	ipv4MaskDefault = net.IPMask{0xFF, 0xFF, 0x55, 0x55}
	ipv4MaskSame16  = net.IPMask{0xFF, 0xFF, 0xFF, 0x55}
)

// Priority computes the BEP 40 canonical peer priority between two IPs.
// Higher values mean the connection is more valuable and should be kept
// when the pool is full.
func Priority(selfIP, peerIP net.IP) uint32 {
	selfIP4 := selfIP.To4()
	peerIP4 := peerIP.To4()

	var a, b []byte

	if selfIP4 != nil && peerIP4 != nil {
		a, b = maskIPv4(selfIP4, peerIP4)
	} else {
		selfIP16 := selfIP.To16()
		peerIP16 := peerIP.To16()
		if selfIP16 == nil || peerIP16 == nil {
			return 0
		}
		a, b = maskIPv6(selfIP16, peerIP16)
	}

	// Order: low, high
	if bytes.Compare(a, b) > 0 {
		a, b = b, a
	}

	buf := make([]byte, len(a)+len(b))
	copy(buf, a)
	copy(buf[len(a):], b)

	return crc32.Checksum(buf, crc32cTable)
}

func maskIPv4(a, b net.IP) ([]byte, []byte) {
	// Same /24? Use full IPs.
	if a[0] == b[0] && a[1] == b[1] && a[2] == b[2] {
		aa := make([]byte, 4)
		bb := make([]byte, 4)
		copy(aa, a)
		copy(bb, b)
		return aa, bb
	}

	// Same /16? Use FF.FF.FF.55 mask.
	mask := ipv4MaskDefault
	if a[0] == b[0] && a[1] == b[1] {
		mask = ipv4MaskSame16
	}

	ma := make([]byte, 4)
	mb := make([]byte, 4)
	for i := range 4 {
		ma[i] = a[i] & mask[i]
		mb[i] = b[i] & mask[i]
	}
	return ma, mb
}

func maskIPv6(a, b net.IP) ([]byte, []byte) {
	// Default mask: FFFF:FFFF:FFFF:5555:5555:5555:5555:5555
	// Same /48: FFFF:FFFF:FFFF:FF55:5555:5555:5555:5555
	// Same /56: FFFF:FFFF:FFFF:FFFF:5555:5555:5555:5555
	// Continue narrowing...
	// Same /128 (identical): use full IPs.

	// Find the shared prefix length to pick the right mask depth.
	// Start at /48 granularity (6 bytes), then refine.
	prefixBytes := 6 // base mask covers first 6 bytes exactly
	for i := 6; i < 16; i++ {
		if a[i] != b[i] {
			break
		}
		prefixBytes = i + 1
	}

	if prefixBytes >= 16 {
		// Same address — use ports (not implemented, use full IPs).
		aa := make([]byte, 16)
		bb := make([]byte, 16)
		copy(aa, a)
		copy(bb, b)
		return aa, bb
	}

	ma := make([]byte, 16)
	mb := make([]byte, 16)
	for i := range 16 {
		if i < prefixBytes {
			ma[i] = a[i]
			mb[i] = b[i]
		} else {
			ma[i] = a[i] & 0x55
			mb[i] = b[i] & 0x55
		}
	}
	return ma, mb
}

