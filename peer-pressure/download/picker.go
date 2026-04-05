// Picker implements rarest-first piece selection for concurrent downloads.
//
// The picker tracks how many peers have each piece (frequency), which pieces
// are currently being downloaded (in-flight), and which are done. Pick()
// returns the rarest piece that the given peer has and isn't already in-flight
// or completed.
//
// All methods are safe for concurrent use.
package download

import (
	"math/rand/v2"
	"sync"
)

// Picker selects which piece to download next using rarest-first strategy.
type Picker struct {
	mu        sync.Mutex
	numPieces int
	frequency []int  // how many peers have each piece
	done      []bool // verified pieces
	inflight  []bool // pieces currently being downloaded
}

// NewPicker creates a picker for a torrent with numPieces pieces.
func NewPicker(numPieces int) *Picker {
	return &Picker{
		numPieces: numPieces,
		frequency: make([]int, numPieces),
		done:      make([]bool, numPieces),
		inflight:  make([]bool, numPieces),
	}
}

// AddPeer registers a peer's bitfield, incrementing frequency counts.
// The bitfield uses MSB-first bit ordering: bit 7 of byte 0 = piece 0.
func (p *Picker) AddPeer(bitfield []byte) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for i := range p.numPieces {
		if hasPiece(bitfield, i) {
			p.frequency[i]++
		}
	}
}

// RemovePeer unregisters a peer's bitfield, decrementing frequency counts.
func (p *Picker) RemovePeer(bitfield []byte) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for i := range p.numPieces {
		if hasPiece(bitfield, i) {
			p.frequency[i]--
		}
	}
}

// Pick returns the rarest piece that the peer has (per its bitfield) and that
// is not already done or in-flight. Returns -1, false if no suitable piece exists.
//
// When multiple pieces tie for rarest, one is chosen at random to avoid all
// workers requesting the same piece from different peers.
func (p *Picker) Pick(peerBitfield []byte) (int, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()

	bestFreq := int(^uint(0) >> 1) // max int
	var candidates []int

	for i := range p.numPieces {
		if p.done[i] || p.inflight[i] {
			continue
		}
		if !hasPiece(peerBitfield, i) {
			continue
		}
		f := p.frequency[i]
		if f < bestFreq {
			bestFreq = f
			candidates = candidates[:0]
			candidates = append(candidates, i)
		} else if f == bestFreq {
			candidates = append(candidates, i)
		}
	}

	if len(candidates) == 0 {
		return -1, false
	}

	choice := candidates[rand.IntN(len(candidates))]
	p.inflight[choice] = true
	return choice, true
}

// Finish marks a piece as successfully downloaded and verified.
func (p *Picker) Finish(index int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.done[index] = true
	p.inflight[index] = false
}

// Abort marks a piece as no longer in-flight (download failed).
func (p *Picker) Abort(index int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.inflight[index] = false
}

// Done reports whether all pieces have been completed.
func (p *Picker) Done() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, d := range p.done {
		if !d {
			return false
		}
	}
	return true
}

// Remaining returns how many pieces are not yet done.
func (p *Picker) Remaining() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	n := 0
	for _, d := range p.done {
		if !d {
			n++
		}
	}
	return n
}

// hasPiece checks whether a bitfield has the bit set for piece index.
// BitTorrent bitfields use MSB-first ordering: bit 7 of byte 0 = piece 0.
func hasPiece(bitfield []byte, index int) bool {
	byteIdx := index / 8
	if byteIdx >= len(bitfield) {
		return false
	}
	bitIdx := 7 - (index % 8) // MSB-first
	return bitfield[byteIdx]&(1<<bitIdx) != 0
}

// MakeBitfield creates a bitfield with the given piece indices set.
func MakeBitfield(numPieces int, pieces []int) []byte {
	numBytes := (numPieces + 7) / 8
	bf := make([]byte, numBytes)
	for _, idx := range pieces {
		byteIdx := idx / 8
		bitIdx := 7 - (idx % 8)
		bf[byteIdx] |= 1 << bitIdx
	}
	return bf
}
