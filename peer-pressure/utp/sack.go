package utp

// SelectiveAck provides operations on BEP 29 selective ACK bitmasks.
//
// The bitmask uses reversed byte order: bit 0 of byte 0 represents
// ack_nr + 2, bit 7 of byte 0 represents ack_nr + 9, bit 0 of byte 1
// represents ack_nr + 10, and so on. The offset parameter in these
// functions is relative to ack_nr + 2 (so offset 0 = ack_nr + 2).

// NewSelectiveAck creates a bitmask large enough to represent n packet
// offsets (from ack_nr+2). The length is rounded up to a multiple of 4.
func NewSelectiveAck(n int) []byte {
	if n <= 0 {
		return make([]byte, 4) // minimum 32 bits per BEP 29
	}
	byteCount := (n + 7) / 8
	// Round up to multiple of 4.
	byteCount = ((byteCount + 3) / 4) * 4
	return make([]byte, byteCount)
}

// SetBit marks the packet at the given offset as received.
// Offset 0 corresponds to ack_nr + 2.
func SetBit(sack []byte, offset int) {
	if offset < 0 || offset >= len(sack)*8 {
		return
	}
	byteIdx := offset / 8
	bitIdx := offset % 8
	sack[byteIdx] |= 1 << uint(bitIdx)
}

// GetBit returns true if the packet at the given offset is marked as received.
func GetBit(sack []byte, offset int) bool {
	if offset < 0 || offset >= len(sack)*8 {
		return false
	}
	byteIdx := offset / 8
	bitIdx := offset % 8
	return sack[byteIdx]&(1<<uint(bitIdx)) != 0
}

// AckedPackets returns the list of offsets (from ack_nr+2) that are set
// in the selective ACK bitmask.
func AckedPackets(sack []byte) []int {
	var offsets []int
	for i := 0; i < len(sack)*8; i++ {
		if GetBit(sack, i) {
			offsets = append(offsets, i)
		}
	}
	return offsets
}
