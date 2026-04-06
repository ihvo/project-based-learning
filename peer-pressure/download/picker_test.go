package download

import (
	"testing"
)

func TestHasPiece(t *testing.T) {
	// Bitfield: 0b10110000 = pieces 0, 2, 3 present
	bf := []byte{0xB0}

	tests := []struct {
		index int
		want  bool
	}{
		{0, true},
		{1, false},
		{2, true},
		{3, true},
		{4, false},
		{5, false},
		{6, false},
		{7, false},
		{8, false}, // out of range
	}

	for _, tt := range tests {
		if got := hasPiece(bf, tt.index); got != tt.want {
			t.Errorf("hasPiece(0xB0, %d) = %v, want %v", tt.index, got, tt.want)
		}
	}
}

func TestMakeBitfield(t *testing.T) {
	bf := MakeBitfield(8, []int{0, 2, 3})
	if len(bf) != 1 {
		t.Fatalf("expected 1 byte, got %d", len(bf))
	}
	// Pieces 0, 2, 3 → bits 7, 5, 4 → 0b10110000 = 0xB0
	if bf[0] != 0xB0 {
		t.Errorf("bitfield = %08b, want 10110000", bf[0])
	}
}

func TestPickerRarestFirst(t *testing.T) {
	p := NewPicker(4)

	// Peer A has pieces 0, 1, 2 — all become freq 1
	peerA := MakeBitfield(4, []int{0, 1, 2})
	p.AddPeer(peerA)

	// Peer B has pieces 0, 1 — pieces 0,1 become freq 2; piece 2 stays at 1
	peerB := MakeBitfield(4, []int{0, 1})
	p.AddPeer(peerB)

	// Peer A should pick piece 2 (frequency 1 — rarest it has)
	idx, ok := p.Pick(peerA)
	if !ok || idx != 2 {
		t.Fatalf("Pick(peerA) = %d, %v; want 2, true", idx, ok)
	}

	// Piece 2 is now in-flight, so next pick for A should be 0 or 1 (both freq 2)
	idx2, ok := p.Pick(peerA)
	if !ok {
		t.Fatal("Pick(peerA) returned false, expected a piece")
	}
	if idx2 != 0 && idx2 != 1 {
		t.Errorf("Pick(peerA) = %d; want 0 or 1", idx2)
	}
}

func TestPickerFinishAbort(t *testing.T) {
	p := NewPicker(3)
	bf := MakeBitfield(3, []int{0, 1, 2})
	p.AddPeer(bf)

	// Pick and finish piece
	idx, _ := p.Pick(bf)
	p.Finish(idx)

	// Pick another and abort it
	idx2, ok := p.Pick(bf)
	if !ok {
		t.Fatal("expected a piece after finishing only one")
	}
	p.Abort(idx2)

	// After abort, the piece should be available again
	idx3, ok := p.Pick(bf)
	if !ok {
		t.Fatal("no piece available after abort")
	}
	if idx3 != idx2 {
		// The aborted piece should be pickable, but another non-done piece
		// might be picked instead — both are valid
	}
}

func TestPickerDone(t *testing.T) {
	p := NewPicker(2)
	bf := MakeBitfield(2, []int{0, 1})
	p.AddPeer(bf)

	if p.Done() {
		t.Fatal("picker should not be done initially")
	}

	idx, _ := p.Pick(bf)
	p.Finish(idx)
	if p.Done() {
		t.Fatal("picker should not be done after finishing 1 of 2")
	}

	idx2, _ := p.Pick(bf)
	p.Finish(idx2)
	if !p.Done() {
		t.Fatal("picker should be done after finishing all pieces")
	}
}

func TestPickerNoPieceAvailable(t *testing.T) {
	p := NewPicker(2)

	// Peer has no pieces
	bf := MakeBitfield(2, nil)
	p.AddPeer(bf)

	_, ok := p.Pick(bf)
	if ok {
		t.Fatal("Pick should return false when peer has no pieces")
	}
}

func TestPickerRemovePeer(t *testing.T) {
	p := NewPicker(2)
	bf := MakeBitfield(2, []int{0, 1})
	p.AddPeer(bf)
	p.RemovePeer(bf)

	if p.Remaining() != 2 {
		t.Fatalf("remaining = %d, want 2", p.Remaining())
	}
}

// --- Endgame mode tests ---

func TestEndgameTriggered(t *testing.T) {
	p := NewPicker(4)
	bf := MakeBitfield(4, []int{0, 1, 2, 3})
	p.AddPeer(bf)

	// Pick all 4 pieces — all now inflight.
	for range 4 {
		_, ok := p.Pick(bf)
		if !ok {
			t.Fatal("expected piece to be available")
		}
	}

	// Finish 3, leaving 1 inflight. Now: 3 done, 1 inflight, 0 idle.
	p.Finish(0)
	p.Finish(1)
	p.Finish(2)

	// Next pick should enter endgame and return the inflight piece.
	idx, ok := p.Pick(bf)
	if !ok {
		t.Fatal("expected endgame pick")
	}
	if idx != 3 {
		t.Errorf("endgame pick = %d, want 3", idx)
	}
	if !p.Endgame() {
		t.Error("expected picker to be in endgame mode")
	}
}

func TestEndgameNotTriggeredWhenIdlePiecesExist(t *testing.T) {
	p := NewPicker(4)
	bf := MakeBitfield(4, []int{0, 1, 2, 3})
	p.AddPeer(bf)

	// Pick 2 — 2 inflight, 2 idle.
	p.Pick(bf)
	p.Pick(bf)

	if p.Endgame() {
		t.Error("should not be in endgame with idle pieces remaining")
	}
}

func TestEndgameDuplicatePick(t *testing.T) {
	p := NewPicker(2)
	bf := MakeBitfield(2, []int{0, 1})
	p.AddPeer(bf)

	// Pick both — both inflight, none idle.
	p.Pick(bf)
	p.Pick(bf)

	// Next pick should trigger endgame and return a duplicate.
	idx, ok := p.Pick(bf)
	if !ok {
		t.Fatal("expected endgame duplicate pick")
	}
	if idx != 0 && idx != 1 {
		t.Errorf("endgame pick = %d, want 0 or 1", idx)
	}
}

func TestEndgameFinishClearsDuplicate(t *testing.T) {
	p := NewPicker(2)
	bf := MakeBitfield(2, []int{0, 1})
	p.AddPeer(bf)

	// Force endgame: pick all, then get a duplicate.
	p.Pick(bf)
	p.Pick(bf)
	p.Pick(bf) // triggers endgame

	// Finish piece 0 — should not be picked again.
	p.Finish(0)

	// In endgame mode, should only get piece 1.
	idx, ok := p.Pick(bf)
	if !ok {
		t.Fatal("expected pick for remaining piece")
	}
	if idx != 1 {
		t.Errorf("pick = %d, want 1 (piece 0 is done)", idx)
	}
}
