package magnet

import (
	"encoding/hex"
	"testing"
)

const testHashHex = "a89dd41fc8201849488a04623b3c0dc45d1a8c4e"

func TestParseHex(t *testing.T) {
	uri := "magnet:?xt=urn:btih:a89dd41fc8201849488a04623b3c0dc45d1a8c4e&dn=openttd&tr=udp://tracker.opentrackr.org:1337/announce"
	link, err := Parse(uri)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	wantHash := "a89dd41fc8201849488a04623b3c0dc45d1a8c4e"
	gotHash := hex.EncodeToString(link.InfoHash[:])
	if gotHash != wantHash {
		t.Errorf("info_hash: got %s, want %s", gotHash, wantHash)
	}
	if link.Name != "openttd" {
		t.Errorf("name: got %q, want %q", link.Name, "openttd")
	}
	if len(link.Trackers) != 1 || link.Trackers[0] != "udp://tracker.opentrackr.org:1337/announce" {
		t.Errorf("trackers: got %v", link.Trackers)
	}
}

func TestParseBase32(t *testing.T) {
	// Base32 of all-zero 20-byte hash: 32 chars of 'A'
	uri := "magnet:?xt=urn:btih:AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	link, err := Parse(uri)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if link.InfoHash != [20]byte{} {
		t.Errorf("expected zero hash, got %x", link.InfoHash)
	}
}

func TestParseMultipleTrackers(t *testing.T) {
	uri := "magnet:?xt=urn:btih:a89dd41fc8201849488a04623b3c0dc45d1a8c4e&tr=udp://one:6969&tr=udp://two:6969"
	link, err := Parse(uri)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(link.Trackers) != 2 {
		t.Fatalf("expected 2 trackers, got %d", len(link.Trackers))
	}
	if link.Trackers[0] != "udp://one:6969" {
		t.Errorf("tracker[0]: got %q", link.Trackers[0])
	}
	if link.Trackers[1] != "udp://two:6969" {
		t.Errorf("tracker[1]: got %q", link.Trackers[1])
	}
}

func TestParseNoName(t *testing.T) {
	uri := "magnet:?xt=urn:btih:a89dd41fc8201849488a04623b3c0dc45d1a8c4e"
	link, err := Parse(uri)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if link.Name != "" {
		t.Errorf("expected empty name, got %q", link.Name)
	}
}

func TestParseErrors(t *testing.T) {
	tests := []struct {
		name string
		uri  string
	}{
		{"not magnet", "http://example.com"},
		{"missing xt", "magnet:?dn=hello"},
		{"wrong xt scheme", "magnet:?xt=urn:sha1:abcdef"},
		{"bad hex", "magnet:?xt=urn:btih:zzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzz"},
		{"wrong length", "magnet:?xt=urn:btih:abcdef"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Parse(tc.uri)
			if err == nil {
				t.Errorf("expected error for %q", tc.uri)
			}
		})
	}
}

func TestLinkString(t *testing.T) {
	hash, _ := hex.DecodeString("a89dd41fc8201849488a04623b3c0dc45d1a8c4e")
	var infoHash [20]byte
	copy(infoHash[:], hash)

	link := &Link{
		InfoHash: infoHash,
		Name:     "test",
		Trackers: []string{"udp://one:6969"},
	}

	s := link.String()
	// Round-trip: parse it back.
	got, err := Parse(s)
	if err != nil {
		t.Fatalf("round-trip parse: %v", err)
	}
	if got.InfoHash != link.InfoHash {
		t.Errorf("hash mismatch after round-trip")
	}
	if got.Name != link.Name {
		t.Errorf("name mismatch: got %q, want %q", got.Name, link.Name)
	}
}

// --- BEP 53: Select-only file indices ---

func TestParseMagnetSelectOnly(t *testing.T) {
	uri := "magnet:?xt=urn:btih:" + testHashHex + "&so=0,2,4,6-8"
	link, err := Parse(uri)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	want := []int{0, 2, 4, 6, 7, 8}
	if len(link.SelectOnly) != len(want) {
		t.Fatalf("SelectOnly: got %v, want %v", link.SelectOnly, want)
	}
	for i, idx := range link.SelectOnly {
		if idx != want[i] {
			t.Errorf("SelectOnly[%d] = %d, want %d", i, idx, want[i])
		}
	}
}

func TestParseMagnetSelectOnlySingle(t *testing.T) {
	uri := "magnet:?xt=urn:btih:" + testHashHex + "&so=3"
	link, err := Parse(uri)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(link.SelectOnly) != 1 || link.SelectOnly[0] != 3 {
		t.Errorf("SelectOnly: got %v, want [3]", link.SelectOnly)
	}
}

func TestParseMagnetSelectOnlyAbsent(t *testing.T) {
	uri := "magnet:?xt=urn:btih:" + testHashHex
	link, err := Parse(uri)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(link.SelectOnly) != 0 {
		t.Errorf("SelectOnly should be empty, got %v", link.SelectOnly)
	}
}

func TestParseMagnetSelectOnlyInvalid(t *testing.T) {
	uri := "magnet:?xt=urn:btih:" + testHashHex + "&so=a,b"
	_, err := Parse(uri)
	if err == nil {
		t.Error("expected error for invalid so parameter")
	}
}

func TestParseMagnetSelectOnlyBadRange(t *testing.T) {
	uri := "magnet:?xt=urn:btih:" + testHashHex + "&so=8-3"
	_, err := Parse(uri)
	if err == nil {
		t.Error("expected error for reversed range")
	}
}

func TestFormatSelectOnly(t *testing.T) {
	tests := []struct {
		indices []int
		want    string
	}{
		{[]int{0, 2, 4, 6, 7, 8}, "0,2,4,6-8"},
		{[]int{3}, "3"},
		{[]int{1, 2, 3, 4, 5}, "1-5"},
		{[]int{0, 1, 3, 5, 6, 7}, "0-1,3,5-7"},
		{nil, ""},
	}
	for _, tt := range tests {
		got := formatSelectOnly(tt.indices)
		if got != tt.want {
			t.Errorf("formatSelectOnly(%v) = %q, want %q", tt.indices, got, tt.want)
		}
	}
}

func TestSelectOnlyRoundTrip(t *testing.T) {
	link := &Link{
		InfoHash:   [20]byte{0xde, 0xad, 0xbe, 0xef, 0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08, 0x09, 0x0a, 0x0b, 0x0c, 0x0d, 0x0e, 0x0f, 0x10},
		SelectOnly: []int{0, 2, 4, 6, 7, 8},
	}
	uri := link.String()
	got, err := Parse(uri)
	if err != nil {
		t.Fatalf("round-trip parse: %v", err)
	}
	if len(got.SelectOnly) != len(link.SelectOnly) {
		t.Fatalf("SelectOnly length: got %d, want %d", len(got.SelectOnly), len(link.SelectOnly))
	}
	for i, idx := range got.SelectOnly {
		if idx != link.SelectOnly[i] {
			t.Errorf("SelectOnly[%d] = %d, want %d", i, idx, link.SelectOnly[i])
		}
	}
}
