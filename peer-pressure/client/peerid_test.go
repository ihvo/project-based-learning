package client

import "testing"

func TestGeneratePeerID(t *testing.T) {
	id := GeneratePeerID()
	if id[0] != '-' || id[7] != '-' {
		t.Fatalf("bad delimiters: %q", string(id[:8]))
	}
	if string(id[1:3]) != ClientID {
		t.Errorf("client code: got %q, want %q", string(id[1:3]), ClientID)
	}
}

func TestGeneratePeerIDRandomness(t *testing.T) {
	a := GeneratePeerID()
	b := GeneratePeerID()
	if a == b {
		t.Error("two peer IDs are identical")
	}
}

func TestFormatVersionPrefix(t *testing.T) {
	tests := []struct {
		version string
		want    string
	}{
		{"0.1.0", "-PP0100-"},
		{"1.2.3", "-PP1230-"},
		{"0.0.1", "-PP0010-"},
		{"9.9.9", "-PP9990-"},
		{"1", "-PP1000-"},
		{"1.2", "-PP1200-"},
	}
	for _, tt := range tests {
		got := FormatVersionPrefix(tt.version)
		if got != tt.want {
			t.Errorf("FormatVersionPrefix(%q) = %q, want %q", tt.version, got, tt.want)
		}
	}
}

func TestParsePeerIDAzureus(t *testing.T) {
	var id [20]byte
	copy(id[:], "-PP0100-abcdefghijkl")

	name, ver, ok := ParsePeerID(id)
	if !ok {
		t.Fatal("expected ok=true")
	}
	if name != "Peer Pressure" {
		t.Errorf("name: got %q, want %q", name, "Peer Pressure")
	}
	if ver != "0100" {
		t.Errorf("version: got %q, want %q", ver, "0100")
	}
}

func TestParsePeerIDKnownClients(t *testing.T) {
	tests := []struct {
		prefix string
		name   string
	}{
		{"-qB4620-", "qBittorrent"},
		{"-TR4040-", "Transmission"},
		{"-DE1390-", "Deluge"},
	}
	for _, tt := range tests {
		var id [20]byte
		copy(id[:], tt.prefix+"randomrandom")
		name, _, ok := ParsePeerID(id)
		if !ok {
			t.Errorf("%s: expected ok=true", tt.prefix)
			continue
		}
		if name != tt.name {
			t.Errorf("%s: got %q, want %q", tt.prefix, name, tt.name)
		}
	}
}

func TestParsePeerIDUnknownClient(t *testing.T) {
	var id [20]byte
	copy(id[:], "-XX1234-randomrandom")

	name, ver, ok := ParsePeerID(id)
	if !ok {
		t.Fatal("expected ok=true for Azureus-format with unknown code")
	}
	if name != "Unknown (XX)" {
		t.Errorf("name: got %q, want %q", name, "Unknown (XX)")
	}
	if ver != "1234" {
		t.Errorf("version: got %q, want %q", ver, "1234")
	}
}

func TestParsePeerIDUnrecognized(t *testing.T) {
	var id [20]byte
	copy(id[:], "random bytes here!!!")

	_, _, ok := ParsePeerID(id)
	if ok {
		t.Error("expected ok=false for non-Azureus format")
	}
}

func TestVersionConsistency(t *testing.T) {
	id := GeneratePeerID()
	name, _, ok := ParsePeerID(id)
	if !ok {
		t.Fatal("generated peer ID not parseable")
	}
	if name != "Peer Pressure" {
		t.Errorf("name: got %q, want %q", name, "Peer Pressure")
	}
	prefix := FormatVersionPrefix(Version)
	if string(id[:8]) != prefix {
		t.Errorf("prefix: got %q, want %q", string(id[:8]), prefix)
	}
}

func TestUserAgent(t *testing.T) {
	ua := UserAgent()
	if ua != "Peer Pressure "+Version {
		t.Errorf("UserAgent() = %q, want %q", ua, "Peer Pressure "+Version)
	}
}
