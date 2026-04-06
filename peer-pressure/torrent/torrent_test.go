package torrent

import (
	"crypto/sha1"
	"testing"

	"github.com/ihvo/peer-pressure/bencode"
)

// buildTorrent is a test helper that builds raw bencoded .torrent bytes
// from the given info dict entries, merged with a default announce URL.
func buildTorrent(t *testing.T, infoEntries bencode.Dict) []byte {
	t.Helper()
	top := bencode.Dict{
		"announce": bencode.String("http://tracker.example.com/announce"),
		"info":     infoEntries,
	}
	return bencode.Encode(top)
}

// fakePieces creates a pieces byte string with n fake 20-byte hashes.
func fakePieces(n int) bencode.String {
	buf := make([]byte, n*20)
	for i := range buf {
		buf[i] = byte(i % 256)
	}
	return bencode.String(buf)
}

func TestParseSingleFile(t *testing.T) {
	info := bencode.Dict{
		"name":         bencode.String("example.txt"),
		"piece length": bencode.Int(262144),
		"pieces":       fakePieces(3),
		"length":       bencode.Int(700000),
	}
	data := buildTorrent(t, info)

	tor, err := Parse(data)
	if err != nil {
		t.Fatalf("Parse error: %v", err)
	}

	if tor.Announce != "http://tracker.example.com/announce" {
		t.Errorf("Announce = %q", tor.Announce)
	}
	if tor.Name != "example.txt" {
		t.Errorf("Name = %q", tor.Name)
	}
	if tor.PieceLength != 262144 {
		t.Errorf("PieceLength = %d", tor.PieceLength)
	}
	if len(tor.Pieces) != 3 {
		t.Errorf("len(Pieces) = %d, want 3", len(tor.Pieces))
	}
	if tor.Length != 700000 {
		t.Errorf("Length = %d", tor.Length)
	}
	if !tor.IsSingleFile() {
		t.Error("expected single-file mode")
	}
	if tor.TotalLength() != 700000 {
		t.Errorf("TotalLength() = %d", tor.TotalLength())
	}
}

func TestParseMultiFile(t *testing.T) {
	info := bencode.Dict{
		"name":         bencode.String("my-album"),
		"piece length": bencode.Int(524288),
		"pieces":       fakePieces(5),
		"files": bencode.List{
			bencode.Dict{
				"length": bencode.Int(1000),
				"path":   bencode.List{bencode.String("track01.mp3")},
			},
			bencode.Dict{
				"length": bencode.Int(2000),
				"path":   bencode.List{bencode.String("subdir"), bencode.String("track02.mp3")},
			},
		},
	}
	data := buildTorrent(t, info)

	tor, err := Parse(data)
	if err != nil {
		t.Fatalf("Parse error: %v", err)
	}

	if tor.IsSingleFile() {
		t.Error("expected multi-file mode")
	}
	if len(tor.Files) != 2 {
		t.Fatalf("len(Files) = %d, want 2", len(tor.Files))
	}
	if tor.Files[0].Length != 1000 {
		t.Errorf("Files[0].Length = %d", tor.Files[0].Length)
	}
	if tor.Files[1].Path[0] != "subdir" || tor.Files[1].Path[1] != "track02.mp3" {
		t.Errorf("Files[1].Path = %v", tor.Files[1].Path)
	}
	if tor.TotalLength() != 3000 {
		t.Errorf("TotalLength() = %d, want 3000", tor.TotalLength())
	}
}

func TestInfoHash(t *testing.T) {
	info := bencode.Dict{
		"length":       bencode.Int(100),
		"name":         bencode.String("test.txt"),
		"piece length": bencode.Int(256),
		"pieces":       fakePieces(1),
	}
	data := buildTorrent(t, info)

	tor, err := Parse(data)
	if err != nil {
		t.Fatalf("Parse error: %v", err)
	}

	// Manually compute expected info_hash: SHA-1 of bencoded info dict
	expectedHash := sha1.Sum(bencode.Encode(info))

	if tor.InfoHash != expectedHash {
		t.Errorf("InfoHash = %x, want %x", tor.InfoHash, expectedHash)
	}
}

func TestParsePiecesNotMultipleOf20(t *testing.T) {
	info := bencode.Dict{
		"length":       bencode.Int(100),
		"name":         bencode.String("test.txt"),
		"piece length": bencode.Int(256),
		"pieces":       bencode.String(make([]byte, 25)), // not a multiple of 20
	}
	data := buildTorrent(t, info)

	_, err := Parse(data)
	if err == nil {
		t.Error("expected error for pieces not multiple of 20")
	}
}

func TestParseMissingAnnounce(t *testing.T) {
	// Build manually without "announce"
	data := bencode.Encode(bencode.Dict{
		"info": bencode.Dict{
			"length":       bencode.Int(100),
			"name":         bencode.String("test.txt"),
			"piece length": bencode.Int(256),
			"pieces":       fakePieces(1),
		},
	})

	_, err := Parse(data)
	if err == nil {
		t.Error("expected error for missing announce")
	}
}

func TestParseBothLengthAndFiles(t *testing.T) {
	info := bencode.Dict{
		"length":       bencode.Int(100),
		"name":         bencode.String("test.txt"),
		"piece length": bencode.Int(256),
		"pieces":       fakePieces(1),
		"files":        bencode.List{},
	}
	data := buildTorrent(t, info)

	_, err := Parse(data)
	if err == nil {
		t.Error("expected error when both 'length' and 'files' present")
	}
}

func TestStringOutput(t *testing.T) {
	info := bencode.Dict{
		"length":       bencode.Int(1048576),
		"name":         bencode.String("linux.iso"),
		"piece length": bencode.Int(262144),
		"pieces":       fakePieces(4),
	}
	data := buildTorrent(t, info)

	tor, err := Parse(data)
	if err != nil {
		t.Fatalf("Parse error: %v", err)
	}

	s := tor.String()
	// Sanity check that the output includes key fields
	for _, want := range []string{"linux.iso", "tracker.example.com", "single-file", "1.00 MiB"} {
		if !contains(s, want) {
			t.Errorf("String() missing %q:\n%s", want, s)
		}
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && searchString(s, substr)
}

func searchString(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

func TestFromInfoDict(t *testing.T) {
	// Build a minimal info dict.
	pieces := make([]byte, 20) // one piece hash (all zeros)
	info := bencode.Dict{
		"name":         bencode.String("testfile.txt"),
		"piece length": bencode.Int(262144),
		"pieces":       bencode.String(pieces),
		"length":       bencode.Int(100000),
	}
	rawInfo := bencode.Encode(info)
	infoHash := sha1.Sum(rawInfo)

	trackers := []string{
		"udp://tracker.opentrackr.org:1337/announce",
		"udp://tracker.openbittorrent.com:6969/announce",
	}

	tor, err := FromInfoDict(rawInfo, infoHash, trackers)
	if err != nil {
		t.Fatalf("FromInfoDict: %v", err)
	}

	if tor.Name != "testfile.txt" {
		t.Errorf("Name: got %q, want %q", tor.Name, "testfile.txt")
	}
	if tor.PieceLength != 262144 {
		t.Errorf("PieceLength: got %d, want %d", tor.PieceLength, 262144)
	}
	if len(tor.Pieces) != 1 {
		t.Errorf("Pieces: got %d, want 1", len(tor.Pieces))
	}
	if tor.Length != 100000 {
		t.Errorf("Length: got %d, want %d", tor.Length, 100000)
	}
	if tor.InfoHash != infoHash {
		t.Errorf("InfoHash mismatch")
	}
	if tor.Announce != trackers[0] {
		t.Errorf("Announce: got %q, want %q", tor.Announce, trackers[0])
	}
	if len(tor.AnnounceList) != 1 || len(tor.AnnounceList[0]) != 2 {
		t.Errorf("AnnounceList: got %v, want [[%v]]", tor.AnnounceList, trackers)
	}
}
