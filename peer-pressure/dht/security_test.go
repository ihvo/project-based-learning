package dht

import (
	"encoding/hex"
	"net"
	"testing"
)

// BEP 42 test vectors from the specification.
var bep42TestVectors = []struct {
	ip       string
	rand     byte // r value (low 3 bits used)
	idPrefix string // first 3 bytes hex
	idLast   byte   // id[19]
}{
	{"124.31.75.21", 1, "5fbfbf", 0x01},
	{"21.75.31.124", 86, "5a3ce9", 0x56},
	{"65.23.51.170", 22, "a5d432", 0x16},
	{"84.124.73.14", 65, "1b0321", 0x41},
	{"43.213.53.83", 90, "e56f6c", 0x5a},
}

func TestBEP42TestVectors(t *testing.T) {
	for _, tc := range bep42TestVectors {
		ip := net.ParseIP(tc.ip).To4()
		r := tc.rand & 0x07

		// Build an ID with the expected r value in byte 19.
		var id NodeID
		id[19] = r

		applyBEP42(&id, ip, v4Mask)

		gotPrefix := hex.EncodeToString(id[:3])
		wantPrefix := tc.idPrefix

		// Only the top 21 bits matter. Byte 2's low 3 bits are random.
		// The test vectors show byte 2 with specific low bits that come from CRC,
		// but per spec only top 5 bits of byte 2 are checked.
		// Compare byte 0, byte 1 fully, and byte 2 top 5 bits.
		wantBytes, _ := hex.DecodeString(wantPrefix)
		if id[0] != wantBytes[0] || id[1] != wantBytes[1] || (id[2]&0xf8) != (wantBytes[2]&0xf8) {
			t.Errorf("IP=%s rand=%d: prefix got %s, want %s (21-bit match)", tc.ip, tc.rand, gotPrefix, wantPrefix)
		}

		if id[19] != (r | (id[19] & 0xf8)) {
			t.Errorf("IP=%s: id[19] low 3 bits should be r=%d", tc.ip, r)
		}
	}
}

func TestGenerateSecureNodeIDValidates(t *testing.T) {
	ip := net.ParseIP("124.31.75.21")
	id := GenerateSecureNodeID(ip)
	if !ValidateNodeID(id, ip) {
		t.Errorf("generated ID %x does not validate against %s", id, ip)
	}
}

func TestGenerateSecureNodeIDIPv6(t *testing.T) {
	ip := net.ParseIP("2001:db8::1")
	id := GenerateSecureNodeID(ip)
	if !ValidateNodeID(id, ip) {
		t.Errorf("generated IPv6 ID %x does not validate against %s", id, ip)
	}
}

func TestValidateNodeIDRejectsWrong(t *testing.T) {
	ip := net.ParseIP("124.31.75.21")
	id := GenerateSecureNodeID(ip)

	otherIP := net.ParseIP("8.8.8.8")
	if ValidateNodeID(id, otherIP) {
		t.Error("ID generated for 124.31.75.21 should not validate against 8.8.8.8")
	}
}

func TestValidateNodeIDLocalExempt(t *testing.T) {
	locals := []string{
		"10.0.0.1",
		"172.16.0.1",
		"172.31.255.255",
		"192.168.1.1",
		"169.254.0.1",
		"127.0.0.1",
	}
	for _, ipStr := range locals {
		ip := net.ParseIP(ipStr)
		// Any random ID should pass for local IPs.
		id := RandomNodeID()
		if !ValidateNodeID(id, ip) {
			t.Errorf("local IP %s should be exempt from BEP 42", ipStr)
		}
	}
}

func TestValidateNodeIDPublicNotExempt(t *testing.T) {
	ip := net.ParseIP("8.8.8.8")
	// A random ID is extremely unlikely to pass.
	for range 100 {
		id := RandomNodeID()
		if ValidateNodeID(id, ip) {
			// Statistically possible but astronomically unlikely for 100 tries.
			// If this happens, the test is still technically correct.
			continue
		}
		return // At least one failed validation — good.
	}
	t.Error("100 random IDs all validated against 8.8.8.8 — extremely unlikely")
}

func TestIsLocalIP(t *testing.T) {
	tests := []struct {
		ip    string
		local bool
	}{
		{"10.0.0.1", true},
		{"10.255.255.255", true},
		{"172.16.0.1", true},
		{"172.31.255.255", true},
		{"172.15.0.1", false},
		{"172.32.0.1", false},
		{"192.168.0.1", true},
		{"192.168.255.255", true},
		{"192.169.0.1", false},
		{"169.254.0.1", true},
		{"127.0.0.1", true},
		{"127.255.255.255", true},
		{"8.8.8.8", false},
		{"1.1.1.1", false},
	}
	for _, tc := range tests {
		ip := net.ParseIP(tc.ip)
		if got := isLocalIP(ip); got != tc.local {
			t.Errorf("isLocalIP(%s) = %v, want %v", tc.ip, got, tc.local)
		}
	}
}

func TestParseIPField(t *testing.T) {
	t.Run("ipv4", func(t *testing.T) {
		// 124.31.75.21 + port 6881
		data := []byte{124, 31, 75, 21, 0x1a, 0xe1}
		ip := ParseIPField(data)
		if ip == nil {
			t.Fatal("nil IP")
		}
		if ip.String() != "124.31.75.21" {
			t.Errorf("got %s", ip)
		}
	})

	t.Run("invalid_length", func(t *testing.T) {
		if ip := ParseIPField([]byte{1, 2, 3}); ip != nil {
			t.Errorf("expected nil for 3 bytes, got %s", ip)
		}
	})
}

func TestExternalIPVote(t *testing.T) {
	v := NewExternalIPVote()

	v.Add(net.ParseIP("1.2.3.4"))
	v.Add(net.ParseIP("1.2.3.4"))
	v.Add(net.ParseIP("5.6.7.8"))

	winner := v.Winner()
	if winner == nil {
		t.Fatal("nil winner")
	}
	if winner.String() != "1.2.3.4" {
		t.Errorf("winner = %s, want 1.2.3.4", winner)
	}
	if v.Count() != 3 {
		t.Errorf("count = %d, want 3", v.Count())
	}
}

func TestExternalIPVoteEmpty(t *testing.T) {
	v := NewExternalIPVote()
	if v.Winner() != nil {
		t.Error("empty vote should return nil winner")
	}
}

func TestEncodeIPField(t *testing.T) {
	t.Run("ipv4", func(t *testing.T) {
		buf := EncodeIPField(net.ParseIP("124.31.75.21"), 6881)
		if len(buf) != 6 {
			t.Fatalf("len = %d, want 6", len(buf))
		}
		if buf[0] != 124 || buf[1] != 31 || buf[2] != 75 || buf[3] != 21 {
			t.Errorf("IP bytes wrong: %v", buf[:4])
		}
		if buf[4] != 0x1a || buf[5] != 0xe1 {
			t.Errorf("port bytes wrong: %v", buf[4:])
		}
	})

	t.Run("ipv6", func(t *testing.T) {
		buf := EncodeIPField(net.ParseIP("2001:db8::1"), 8080)
		if len(buf) != 18 {
			t.Fatalf("len = %d, want 18", len(buf))
		}
	})
}
