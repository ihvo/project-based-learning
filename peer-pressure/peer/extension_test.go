package peer

import (
	"net"
	"testing"
)

func TestExtHandshakeRoundTrip(t *testing.T) {
	exts := map[string]int{
		"ut_metadata": 1,
		"ut_pex":      2,
	}
	msg := NewExtHandshake(exts, 31415, "Test Client 1.0")

	if msg.ID != MsgExtended {
		t.Fatalf("expected MsgExtended (%d), got %d", MsgExtended, msg.ID)
	}
	if msg.Payload[0] != 0 {
		t.Fatalf("expected sub-ID 0, got %d", msg.Payload[0])
	}

	hs, err := ParseExtHandshake(msg.Payload)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	if hs.M["ut_metadata"] != 1 {
		t.Errorf("ut_metadata ID: got %d, want 1", hs.M["ut_metadata"])
	}
	if hs.M["ut_pex"] != 2 {
		t.Errorf("ut_pex ID: got %d, want 2", hs.M["ut_pex"])
	}
	if hs.MetadataSize != 31415 {
		t.Errorf("metadata_size: got %d, want 31415", hs.MetadataSize)
	}
	if hs.V != "Test Client 1.0" {
		t.Errorf("v: got %q, want %q", hs.V, "Test Client 1.0")
	}
}

func TestExtHandshakeNoMetadataSize(t *testing.T) {
	msg := NewExtHandshake(map[string]int{"ut_pex": 3}, 0, "Test")
	hs, err := ParseExtHandshake(msg.Payload)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if hs.MetadataSize != 0 {
		t.Errorf("metadata_size: got %d, want 0", hs.MetadataSize)
	}
	if hs.M["ut_pex"] != 3 {
		t.Errorf("ut_pex ID: got %d, want 3", hs.M["ut_pex"])
	}
}

func TestExtHandshakeEmptyExtensions(t *testing.T) {
	msg := NewExtHandshake(map[string]int{}, 0, "Test")
	hs, err := ParseExtHandshake(msg.Payload)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(hs.M) != 0 {
		t.Errorf("expected empty M, got %v", hs.M)
	}
}

func TestParseExtHandshakeTooShort(t *testing.T) {
	_, err := ParseExtHandshake([]byte{0})
	if err == nil {
		t.Fatal("expected error for 1-byte payload")
	}
}

func TestParseExtHandshakeWrongSubID(t *testing.T) {
	_, err := ParseExtHandshake([]byte{1, 'd', 'e'})
	if err == nil {
		t.Fatal("expected error for non-zero sub-ID")
	}
}

func TestNewExtMessage(t *testing.T) {
	data := []byte("hello")
	msg := NewExtMessage(3, data)
	if msg.ID != MsgExtended {
		t.Errorf("ID: got %d, want %d", msg.ID, MsgExtended)
	}
	if msg.Payload[0] != 3 {
		t.Errorf("sub-ID: got %d, want 3", msg.Payload[0])
	}
	if string(msg.Payload[1:]) != "hello" {
		t.Errorf("data: got %q, want %q", msg.Payload[1:], "hello")
	}
}

func TestHandshakeReservedBit(t *testing.T) {
	// Verify WriteHandshake sets bit 43 and ReadHandshake preserves it.
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()

	hs := &Handshake{
		InfoHash: [20]byte{1, 2, 3},
		PeerID:   [20]byte{4, 5, 6},
	}

	errCh := make(chan error, 1)
	go func() {
		errCh <- WriteHandshake(a, hs)
	}()

	got, err := ReadHandshake(b)
	if err != nil {
		t.Fatalf("read handshake: %v", err)
	}
	if err := <-errCh; err != nil {
		t.Fatalf("write handshake: %v", err)
	}

	if !got.SupportsExtensions() {
		t.Error("expected SupportsExtensions() == true")
	}
	if got.Reserved[5]&0x10 == 0 {
		t.Errorf("reserved byte 5 bit 4 not set: %08b", got.Reserved[5])
	}
}

func TestHandshakeReservedBitNotSet(t *testing.T) {
	// A peer that does NOT set the extension bit.
	hs := &Handshake{}
	if hs.SupportsExtensions() {
		t.Error("zero handshake should not support extensions")
	}
}

func TestExchangeExtHandshake(t *testing.T) {
	infoHash := [20]byte{1, 2, 3, 4, 5}
	peerIDA := [20]byte{10}
	peerIDB := [20]byte{20}

	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()

	// Connect both sides.
	connCh := make(chan *Conn, 1)
	errCh := make(chan error, 1)

	go func() {
		c, err := FromConn(a, infoHash, peerIDA)
		if err != nil {
			errCh <- err
			return
		}
		connCh <- c
	}()

	connB, err := FromConn(b, infoHash, peerIDB)
	if err != nil {
		t.Fatalf("handshake B: %v", err)
	}

	select {
	case connA := <-connCh:
		// Both sides should support extensions.
		if !connA.SupportsExtensions() {
			t.Fatal("connA should support extensions")
		}
		if !connB.SupportsExtensions() {
			t.Fatal("connB should support extensions")
		}

		// Exchange extension handshakes concurrently.
		extErrCh := make(chan error, 2)
		go func() {
			extErrCh <- connA.ExchangeExtHandshake(
				map[string]int{"ut_metadata": 1}, 9999, "Test")
		}()
		go func() {
			extErrCh <- connB.ExchangeExtHandshake(
				map[string]int{"ut_pex": 2}, 0, "Test")
		}()

		for range 2 {
			if err := <-extErrCh; err != nil {
				t.Fatalf("ext handshake: %v", err)
			}
		}

		// Verify A sees B's extensions.
		if connA.PeerExtensions.M["ut_pex"] != 2 {
			t.Errorf("A sees B ut_pex: got %d, want 2",
				connA.PeerExtensions.M["ut_pex"])
		}

		// Verify B sees A's extensions.
		if connB.PeerExtensions.M["ut_metadata"] != 1 {
			t.Errorf("B sees A ut_metadata: got %d, want 1",
				connB.PeerExtensions.M["ut_metadata"])
		}
		if connB.PeerExtensions.MetadataSize != 9999 {
			t.Errorf("B sees A metadata_size: got %d, want 9999",
				connB.PeerExtensions.MetadataSize)
		}

	case err := <-errCh:
		t.Fatalf("handshake A: %v", err)
	}
}
