package peer

import (
	"net"
	"testing"
)

func TestAllowedFastSet(t *testing.T) {
	// BEP 6 spec test vector:
	// IP 80.4.4.200, infohash = 0xaa...aa, 1313 pieces, k=10
	ip := net.IPv4(80, 4, 4, 200)
	var infoHash [20]byte
	for i := range infoHash {
		infoHash[i] = 0xaa
	}

	result := AllowedFastSet(ip, infoHash, 1313, 10)
	if len(result) != 10 {
		t.Fatalf("expected 10 pieces, got %d", len(result))
	}

	// The BEP 6 spec gives these expected values for this input:
	expected := []uint32{1059, 431, 808, 1217, 287, 376, 1188, 353, 508, 1246}
	for i, want := range expected {
		if result[i] != want {
			t.Errorf("result[%d] = %d, want %d", i, result[i], want)
		}
	}
}

func TestAllowedFastSetIPv6ReturnsNil(t *testing.T) {
	ip := net.ParseIP("::1")
	var infoHash [20]byte
	result := AllowedFastSet(ip, infoHash, 100, 10)
	if result != nil {
		t.Errorf("expected nil for IPv6, got %v", result)
	}
}

func TestAllowedFastSetSmallTorrent(t *testing.T) {
	ip := net.IPv4(10, 0, 0, 1)
	var infoHash [20]byte
	// 3 pieces, k=10 — can't produce more than 3 unique.
	result := AllowedFastSet(ip, infoHash, 3, 10)
	if len(result) != 3 {
		t.Fatalf("expected 3 (capped by numPieces), got %d", len(result))
	}
	// All should be unique.
	seen := make(map[uint32]bool)
	for _, idx := range result {
		if seen[idx] {
			t.Errorf("duplicate index %d", idx)
		}
		seen[idx] = true
		if idx >= 3 {
			t.Errorf("index %d >= numPieces 3", idx)
		}
	}
}

func TestSupportsFast(t *testing.T) {
	h := &Handshake{}
	if h.SupportsFast() {
		t.Error("should not support fast by default")
	}
	h.Reserved[7] |= 0x04
	if !h.SupportsFast() {
		t.Error("should support fast after setting bit")
	}
}

func TestFastExtensionMessages(t *testing.T) {
	// Verify constructors produce correct IDs and payloads.
	t.Run("SuggestPiece", func(t *testing.T) {
		msg := NewSuggestPiece(42)
		if msg.ID != MsgSuggestPiece {
			t.Errorf("ID = %d, want %d", msg.ID, MsgSuggestPiece)
		}
		if len(msg.Payload) != 4 {
			t.Fatalf("payload len = %d, want 4", len(msg.Payload))
		}
		idx, _ := ParseHave(msg.Payload)
		if idx != 42 {
			t.Errorf("piece index = %d, want 42", idx)
		}
	})

	t.Run("HaveAll", func(t *testing.T) {
		msg := NewHaveAll()
		if msg.ID != MsgHaveAll || len(msg.Payload) != 0 {
			t.Errorf("HaveAll = {ID:%d, Payload:%d bytes}", msg.ID, len(msg.Payload))
		}
	})

	t.Run("HaveNone", func(t *testing.T) {
		msg := NewHaveNone()
		if msg.ID != MsgHaveNone || len(msg.Payload) != 0 {
			t.Errorf("HaveNone = {ID:%d, Payload:%d bytes}", msg.ID, len(msg.Payload))
		}
	})

	t.Run("RejectRequest", func(t *testing.T) {
		msg := NewRejectRequest(5, 16384, 16384)
		if msg.ID != MsgRejectRequest {
			t.Errorf("ID = %d, want %d", msg.ID, MsgRejectRequest)
		}
		rp, err := ParseRequest(msg.Payload)
		if err != nil {
			t.Fatal(err)
		}
		if rp.Index != 5 || rp.Begin != 16384 || rp.Length != 16384 {
			t.Errorf("reject = %+v", rp)
		}
	})

	t.Run("AllowedFast", func(t *testing.T) {
		msg := NewAllowedFast(99)
		if msg.ID != MsgAllowedFast {
			t.Errorf("ID = %d, want %d", msg.ID, MsgAllowedFast)
		}
		idx, _ := ParseHave(msg.Payload)
		if idx != 99 {
			t.Errorf("index = %d, want 99", idx)
		}
	})
}

func TestHandshakeAdvertisesFast(t *testing.T) {
	// Verify WriteHandshake sets the BEP 6 bit.
	var buf [68]byte
	h := &Handshake{}
	err := WriteHandshake(writerFunc(func(p []byte) (int, error) {
		copy(buf[:], p)
		return len(p), nil
	}), h)
	if err != nil {
		t.Fatal(err)
	}
	// byte 27 (reserved[7]) should have bit 2 set
	if buf[27]&0x04 == 0 {
		t.Error("BEP 6 bit not set in handshake reserved bytes")
	}
	// byte 25 (reserved[5]) should have bit 4 set (BEP 10)
	if buf[25]&0x10 == 0 {
		t.Error("BEP 10 bit not set in handshake reserved bytes")
	}
}

type writerFunc func([]byte) (int, error)

func (f writerFunc) Write(p []byte) (int, error) { return f(p) }
