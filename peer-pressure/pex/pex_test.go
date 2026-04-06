package pex

import (
	"net"
	"sync"
	"testing"
)

// --- Message encode/decode tests ---

func TestEncodeDecodeRoundTrip(t *testing.T) {
	msg := &Message{
		Added: []PeerEntry{
			{IP: net.IPv4(192, 168, 1, 1), Port: 6881, Flags: FlagSeed},
			{IP: net.IPv4(10, 0, 0, 1), Port: 51413, Flags: FlagEncryption | FlagUTP},
		},
		Dropped: []PeerEntry{
			{IP: net.IPv4(172, 16, 0, 1), Port: 8080},
		},
		Added6: []PeerEntry{
			{IP: net.ParseIP("::1"), Port: 6881, Flags: FlagReachable},
		},
		Dropped6: nil,
	}

	encoded := msg.Encode()
	decoded, err := Decode(encoded)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}

	if len(decoded.Added) != 2 {
		t.Fatalf("expected 2 added, got %d", len(decoded.Added))
	}
	if !decoded.Added[0].IP.Equal(net.IPv4(192, 168, 1, 1)) {
		t.Errorf("added[0].IP = %v", decoded.Added[0].IP)
	}
	if decoded.Added[0].Port != 6881 {
		t.Errorf("added[0].Port = %d", decoded.Added[0].Port)
	}
	if decoded.Added[0].Flags != FlagSeed {
		t.Errorf("added[0].Flags = %02x", decoded.Added[0].Flags)
	}
	if decoded.Added[1].Flags != FlagEncryption|FlagUTP {
		t.Errorf("added[1].Flags = %02x", decoded.Added[1].Flags)
	}

	if len(decoded.Dropped) != 1 {
		t.Fatalf("expected 1 dropped, got %d", len(decoded.Dropped))
	}
	if decoded.Dropped[0].Port != 8080 {
		t.Errorf("dropped[0].Port = %d", decoded.Dropped[0].Port)
	}

	if len(decoded.Added6) != 1 {
		t.Fatalf("expected 1 added6, got %d", len(decoded.Added6))
	}
	if decoded.Added6[0].Port != 6881 {
		t.Errorf("added6[0].Port = %d", decoded.Added6[0].Port)
	}
	if decoded.Added6[0].Flags != FlagReachable {
		t.Errorf("added6[0].Flags = %02x", decoded.Added6[0].Flags)
	}
}

func TestDecodeEmpty(t *testing.T) {
	msg := &Message{}
	encoded := msg.Encode()
	decoded, err := Decode(encoded)
	if err != nil {
		t.Fatalf("decode empty: %v", err)
	}
	if len(decoded.Added) != 0 || len(decoded.Dropped) != 0 ||
		len(decoded.Added6) != 0 || len(decoded.Dropped6) != 0 {
		t.Error("expected all empty slices")
	}
}

func TestDecodeInvalidLength(t *testing.T) {
	// added field with 7 bytes (not a multiple of 6)
	raw := []byte("d5:added7:1234567e")
	_, err := Decode(raw)
	if err == nil {
		t.Error("expected error for invalid compact length")
	}
}

func TestDecodeFlagsMismatch(t *testing.T) {
	// 2 peers (12 bytes in added) but only 1 flag byte
	added := make([]byte, 12)
	copy(added[0:4], net.IPv4(1, 2, 3, 4).To4())
	added[4], added[5] = 0x1A, 0xE1 // port 6881
	copy(added[6:10], net.IPv4(5, 6, 7, 8).To4())
	added[10], added[11] = 0x1A, 0xE1

	flags := []byte{0x02} // only 1 flag for 2 peers

	// Manually construct bencoded dict
	raw := "d5:added12:" + string(added) + "7:added.f1:" + string(flags) + "e"
	_, err := Decode([]byte(raw))
	if err == nil {
		t.Error("expected error for flags/peers count mismatch")
	}
}

func TestFlagBits(t *testing.T) {
	if FlagEncryption != 0x01 {
		t.Errorf("FlagEncryption = %02x", FlagEncryption)
	}
	if FlagSeed != 0x02 {
		t.Errorf("FlagSeed = %02x", FlagSeed)
	}
	if FlagUTP != 0x04 {
		t.Errorf("FlagUTP = %02x", FlagUTP)
	}
	if FlagHolepunch != 0x08 {
		t.Errorf("FlagHolepunch = %02x", FlagHolepunch)
	}
	if FlagReachable != 0x10 {
		t.Errorf("FlagReachable = %02x", FlagReachable)
	}
	if FlagSeed|FlagUTP != 0x06 {
		t.Errorf("FlagSeed|FlagUTP = %02x, want 0x06", FlagSeed|FlagUTP)
	}
}

func TestPeerEntryAddr(t *testing.T) {
	e := PeerEntry{IP: net.IPv4(10, 0, 0, 1), Port: 51413}
	got := e.Addr()
	if got != "10.0.0.1:51413" {
		t.Errorf("Addr() = %q", got)
	}
}

// --- DiffTracker tests ---

func TestDiffFirstMessage(t *testing.T) {
	dt := NewDiffTracker()
	dt.AddPeer(PeerEntry{IP: net.IPv4(1, 1, 1, 1), Port: 6881})
	dt.AddPeer(PeerEntry{IP: net.IPv4(2, 2, 2, 2), Port: 6882})
	dt.AddPeer(PeerEntry{IP: net.IPv4(3, 3, 3, 3), Port: 6883})

	msg := dt.Diff()
	if len(msg.Added) != 3 {
		t.Errorf("first diff: expected 3 added, got %d", len(msg.Added))
	}
	if len(msg.Dropped) != 0 {
		t.Errorf("first diff: expected 0 dropped, got %d", len(msg.Dropped))
	}
}

func TestDiffSubsequentMessage(t *testing.T) {
	dt := NewDiffTracker()
	dt.AddPeer(PeerEntry{IP: net.IPv4(1, 1, 1, 1), Port: 6881})
	dt.AddPeer(PeerEntry{IP: net.IPv4(2, 2, 2, 2), Port: 6882})
	dt.AddPeer(PeerEntry{IP: net.IPv4(3, 3, 3, 3), Port: 6883})

	dt.Diff() // consume initial

	// Add one, remove one.
	dt.AddPeer(PeerEntry{IP: net.IPv4(4, 4, 4, 4), Port: 6884})
	dt.RemovePeer(net.IPv4(1, 1, 1, 1), 6881)

	msg := dt.Diff()
	if len(msg.Added) != 1 {
		t.Errorf("expected 1 added, got %d", len(msg.Added))
	}
	if len(msg.Dropped) != 1 {
		t.Errorf("expected 1 dropped, got %d", len(msg.Dropped))
	}
	if msg.Added[0].Port != 6884 {
		t.Errorf("added peer port = %d, want 6884", msg.Added[0].Port)
	}
	if msg.Dropped[0].Port != 6881 {
		t.Errorf("dropped peer port = %d, want 6881", msg.Dropped[0].Port)
	}
}

func TestDiffNoDelta(t *testing.T) {
	dt := NewDiffTracker()
	dt.AddPeer(PeerEntry{IP: net.IPv4(1, 1, 1, 1), Port: 6881})
	dt.Diff()

	msg := dt.Diff()
	if len(msg.Added) != 0 || len(msg.Dropped) != 0 {
		t.Errorf("expected empty diff, got %d added, %d dropped", len(msg.Added), len(msg.Dropped))
	}
}

func TestDiffCapAdded(t *testing.T) {
	dt := NewDiffTracker()
	for i := range 60 {
		dt.AddPeer(PeerEntry{IP: net.IPv4(byte(i/256), byte(i%256), 0, 1), Port: uint16(6000 + i)})
	}

	msg := dt.Diff()
	if len(msg.Added) > MaxAdded {
		t.Errorf("added count %d exceeds max %d", len(msg.Added), MaxAdded)
	}
}

func TestDiffCapDropped(t *testing.T) {
	dt := NewDiffTracker()
	for i := range 60 {
		dt.AddPeer(PeerEntry{IP: net.IPv4(byte(i/256), byte(i%256), 0, 1), Port: uint16(6000 + i)})
	}
	dt.Diff()

	// Remove all.
	for i := range 60 {
		dt.RemovePeer(net.IPv4(byte(i/256), byte(i%256), 0, 1), uint16(6000+i))
	}

	msg := dt.Diff()
	if len(msg.Dropped) > MaxDropped {
		t.Errorf("dropped count %d exceeds max %d", len(msg.Dropped), MaxDropped)
	}
}

func TestDiffConcurrent(t *testing.T) {
	dt := NewDiffTracker()
	var wg sync.WaitGroup

	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := range 100 {
			dt.AddPeer(PeerEntry{IP: net.IPv4(byte(i), 0, 0, 1), Port: uint16(6000 + i)})
		}
	}()
	go func() {
		defer wg.Done()
		for range 50 {
			dt.Diff()
		}
	}()

	wg.Wait()
}

func TestDiffIPv6(t *testing.T) {
	dt := NewDiffTracker()
	dt.AddPeer(PeerEntry{IP: net.ParseIP("2001:db8::1"), Port: 6881, Flags: FlagSeed})
	dt.AddPeer(PeerEntry{IP: net.IPv4(1, 1, 1, 1), Port: 6882})

	msg := dt.Diff()
	if len(msg.Added) != 1 {
		t.Errorf("expected 1 IPv4 added, got %d", len(msg.Added))
	}
	if len(msg.Added6) != 1 {
		t.Errorf("expected 1 IPv6 added, got %d", len(msg.Added6))
	}
}

func TestCount(t *testing.T) {
	dt := NewDiffTracker()
	if dt.Count() != 0 {
		t.Error("fresh tracker should have 0 count")
	}
	dt.AddPeer(PeerEntry{IP: net.IPv4(1, 1, 1, 1), Port: 6881})
	dt.AddPeer(PeerEntry{IP: net.IPv4(2, 2, 2, 2), Port: 6882})
	if dt.Count() != 2 {
		t.Errorf("count = %d, want 2", dt.Count())
	}
	dt.RemovePeer(net.IPv4(1, 1, 1, 1), 6881)
	if dt.Count() != 1 {
		t.Errorf("count = %d, want 1", dt.Count())
	}
}
