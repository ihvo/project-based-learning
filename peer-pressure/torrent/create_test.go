package torrent

import (
	"crypto/sha1"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestCreateSingleFile(t *testing.T) {
	// Create temp file with known data.
	dir := t.TempDir()
	path := filepath.Join(dir, "test.txt")
	data := make([]byte, 100_000)
	for i := range data {
		data[i] = byte(i % 251)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}

	raw, err := Create(path, CreateOpts{
		Tracker:     "http://tracker.example.com/announce",
		PieceLength: 16384,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Parse it back.
	tor, err := Parse(raw)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	if tor.Name != "test.txt" {
		t.Errorf("Name = %q", tor.Name)
	}
	if tor.PieceLength != 16384 {
		t.Errorf("PieceLength = %d", tor.PieceLength)
	}
	if tor.Length != 100_000 {
		t.Errorf("Length = %d", tor.Length)
	}
	if tor.Announce != "http://tracker.example.com/announce" {
		t.Errorf("Announce = %q", tor.Announce)
	}

	expectedPieces := (100_000 + 16383) / 16384
	if len(tor.Pieces) != expectedPieces {
		t.Errorf("Pieces = %d, want %d", len(tor.Pieces), expectedPieces)
	}
}

func TestCreateDirectory(t *testing.T) {
	dir := t.TempDir()
	subdir := filepath.Join(dir, "mydir")
	os.MkdirAll(filepath.Join(subdir, "sub"), 0o755)

	os.WriteFile(filepath.Join(subdir, "a.txt"), []byte("hello"), 0o644)
	os.WriteFile(filepath.Join(subdir, "sub", "b.txt"), []byte("world!"), 0o644)

	raw, err := Create(subdir, CreateOpts{
		Tracker:     "http://tracker.example.com/announce",
		PieceLength: 16384,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	tor, err := Parse(raw)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	if tor.Name != "mydir" {
		t.Errorf("Name = %q", tor.Name)
	}
	if len(tor.Files) != 2 {
		t.Fatalf("Files = %d, want 2", len(tor.Files))
	}

	// Files should be sorted.
	if tor.Files[0].Path[0] != "a.txt" {
		t.Errorf("Files[0].Path = %v", tor.Files[0].Path)
	}
	if tor.Files[0].Length != 5 {
		t.Errorf("Files[0].Length = %d", tor.Files[0].Length)
	}
	if tor.Files[1].Path[0] != "sub" || tor.Files[1].Path[1] != "b.txt" {
		t.Errorf("Files[1].Path = %v", tor.Files[1].Path)
	}
}

func TestCreatePrivate(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.bin")
	os.WriteFile(path, []byte("data"), 0o644)

	raw, err := Create(path, CreateOpts{
		Tracker: "http://t.example.com/announce",
		Private: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	tor, err := Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if !tor.IsPrivate() {
		t.Error("expected private torrent")
	}
}

func TestCreateRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "payload.bin")
	data := make([]byte, 50_000)
	for i := range data {
		data[i] = byte(i)
	}
	os.WriteFile(path, data, 0o644)

	raw, err := Create(path, CreateOpts{
		Tracker:     "http://t.example.com/announce",
		PieceLength: 16384,
		Comment:     "test torrent",
		WebSeeds:    []string{"http://ws.example.com/files/"},
	})
	if err != nil {
		t.Fatal(err)
	}

	tor, err := Parse(raw)
	if err != nil {
		t.Fatal(err)
	}

	// Verify hashes match by re-reading the file.
	if err := verifyCreatedTorrent(path, tor); err != nil {
		t.Fatalf("hash verification: %v", err)
	}
}

func TestCreateAutoPieceLength(t *testing.T) {
	tests := []struct {
		size    int64
		wantMin int
		wantMax int
	}{
		{10 * 1024 * 1024, 16384, 16384},           // 10MB → 16KiB
		{700 * 1024 * 1024, 256 * 1024, 1024 * 1024}, // 700MB → 256K-1M
	}

	for _, tt := range tests {
		pl := pieceLength(tt.size, 0)
		if pl < tt.wantMin || pl > tt.wantMax {
			t.Errorf("pieceLength(%d) = %d, want [%d, %d]", tt.size, pl, tt.wantMin, tt.wantMax)
		}
		// Must be power of 2.
		if pl&(pl-1) != 0 {
			t.Errorf("pieceLength(%d) = %d, not power of 2", tt.size, pl)
		}
	}
}

func TestCreateMissingTracker(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.bin")
	os.WriteFile(path, []byte("x"), 0o644)

	_, err := Create(path, CreateOpts{})
	if err == nil {
		t.Error("expected error for missing tracker")
	}
}

// verifyCreatedTorrent reads the file and verifies piece hashes match the torrent.
func verifyCreatedTorrent(path string, tor *Torrent) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}

	for i, expected := range tor.Pieces {
		start := i * tor.PieceLength
		end := start + tor.PieceLength
		if end > len(data) {
			end = len(data)
		}

		actual := sha1.Sum(data[start:end])
		if actual != expected {
			return fmt.Errorf("piece %d: hash mismatch", i)
		}
	}
	return nil
}
