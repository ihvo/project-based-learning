package download

import (
	"strings"
	"testing"
)

func TestProgressRender(t *testing.T) {
	prog := NewProgress("test-file.iso", 20, 256*1024, 20*256*1024)

	// Simulate 2 peers
	bf1 := MakeBitfield(20, []int{0, 1, 2, 3, 4, 5, 6, 7, 8, 9})
	bf2 := MakeBitfield(20, []int{10, 11, 12, 13, 14, 15, 16, 17, 18, 19})

	prog.PeerConnected("192.168.1.5:6881", bf1)
	prog.PeerConnected("192.168.1.10:6882", bf2)

	// Simulate some progress
	prog.PieceStarted(0)
	prog.BlockReceived("192.168.1.5:6881", 16384)
	prog.BlockReceived("192.168.1.5:6881", 16384)
	prog.PieceDone(0, "192.168.1.5:6881")

	prog.PieceStarted(10)
	prog.BlockReceived("192.168.1.10:6882", 16384)
	prog.PieceStarted(1)

	output := prog.Render(80)

	// Verify key elements are present
	checks := []string{
		"Peer Pressure",
		"test-file.iso",
		"1/20 pcs",         // one piece done
		"Piece Map",
		"done",
		"active",
		"pending",
		"192.168.1.5:6881",
		"192.168.1.10:6882",
		"Peer Pool",
	}

	for _, want := range checks {
		if !strings.Contains(output, want) {
			t.Errorf("Render output missing %q", want)
		}
	}

	// Verify peer stats — block count visible
	if !strings.Contains(output, "2 blks") { // peer A received 2 blocks
		t.Error("expected peer A to show 2 blks")
	}
}

func TestProgressPieceFailed(t *testing.T) {
	prog := NewProgress("test.bin", 4, 16384, 4*16384)
	bf := MakeBitfield(4, []int{0, 1, 2, 3})
	prog.PeerConnected("peer1", bf)

	prog.PieceStarted(0)
	prog.PieceFailed(0)

	// After failure, piece should be back to pending
	output := prog.Render(80)
	if !strings.Contains(output, "0/4 pcs") {
		t.Error("expected 0 completed pieces after failure")
	}
}

func TestProgressAllDone(t *testing.T) {
	prog := NewProgress("done.bin", 3, 16384, 3*16384)
	bf := MakeBitfield(3, []int{0, 1, 2})
	prog.PeerConnected("peer1", bf)

	for i := range 3 {
		prog.PieceStarted(i)
		prog.BlockReceived("peer1", 16384)
		prog.PieceDone(i, "peer1")
	}

	output := prog.Render(80)
	if !strings.Contains(output, "100%") {
		t.Error("expected 100% when all pieces done")
	}
	if !strings.Contains(output, "3/3 pcs") {
		t.Error("expected 3/3 completed pieces")
	}
}

func TestProgressLargePieceMap(t *testing.T) {
	// More pieces than fit in a single row — triggers compression
	numPieces := 1000
	prog := NewProgress("large.iso", numPieces, 256*1024, int64(numPieces)*256*1024)

	bf := MakeBitfield(numPieces, func() []int {
		all := make([]int, numPieces)
		for i := range all {
			all[i] = i
		}
		return all
	}())
	prog.PeerConnected("peer1", bf)

	// Mark first half as done
	for i := range numPieces / 2 {
		prog.PieceStarted(i)
		prog.PieceDone(i, "peer1")
	}

	output := prog.Render(80)
	if !strings.Contains(output, "50%") {
		t.Error("expected 50% for half-done large torrent")
	}

	// Verify the piece map isn't excessively long (should be capped)
	lines := strings.Count(output, "\n")
	if lines > 30 {
		t.Errorf("render produced %d lines, expected compact output", lines)
	}
}
