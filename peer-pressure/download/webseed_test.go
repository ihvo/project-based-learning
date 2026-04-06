package download

import (
	"context"
	"crypto/sha1"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ihvo/peer-pressure/torrent"
)

func TestWebseedFetchPiece(t *testing.T) {
	// Create fake file content: 3 pieces of 32 bytes each.
	pieceLen := 32
	numPieces := 3
	fileData := make([]byte, pieceLen*numPieces)
	for i := range fileData {
		fileData[i] = byte(i % 256)
	}

	// Compute piece hashes.
	var hashes [][20]byte
	for i := range numPieces {
		start := i * pieceLen
		end := start + pieceLen
		hashes = append(hashes, sha1.Sum(fileData[start:end]))
	}

	// HTTP server that supports Range requests.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rangeHeader := r.Header.Get("Range")
		if rangeHeader == "" {
			w.Write(fileData)
			return
		}
		var start, end int
		fmt.Sscanf(rangeHeader, "bytes=%d-%d", &start, &end)
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, len(fileData)))
		w.WriteHeader(http.StatusPartialContent)
		w.Write(fileData[start : end+1])
	}))
	defer srv.Close()

	tor := &torrent.Torrent{
		Name:        "test.bin",
		PieceLength: pieceLen,
		Pieces:      hashes,
		Length:      len(fileData),
	}

	picker := NewPicker(numPieces)
	results := make(chan pieceResult, numPieces)

	ws := newWebseedWorker(srv.URL+"/test.bin", tor, picker, results, nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go ws.run(ctx)

	// Collect all 3 pieces.
	got := make(map[int][]byte)
	for range numPieces {
		r := <-results
		if r.err != nil {
			t.Fatalf("piece %d: %v", r.index, r.err)
		}
		got[r.index] = r.data
	}

	// Verify content.
	for i := range numPieces {
		start := i * pieceLen
		end := start + pieceLen
		expected := fileData[start:end]
		if string(got[i]) != string(expected) {
			t.Errorf("piece %d: data mismatch", i)
		}
	}
}

func TestWebseedBadHash(t *testing.T) {
	// Server returns valid data, but hashes don't match.
	pieceLen := 16
	fileData := make([]byte, pieceLen)
	for i := range fileData {
		fileData[i] = 0xAA
	}

	// Wrong hash on purpose.
	var badHash [20]byte
	badHash[0] = 0xFF

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var start, end int
		fmt.Sscanf(r.Header.Get("Range"), "bytes=%d-%d", &start, &end)
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, len(fileData)))
		w.WriteHeader(http.StatusPartialContent)
		w.Write(fileData[start : end+1])
	}))
	defer srv.Close()

	tor := &torrent.Torrent{
		Name:        "bad.bin",
		PieceLength: pieceLen,
		Pieces:      [][20]byte{badHash},
		Length:      pieceLen,
	}

	picker := NewPicker(1)
	results := make(chan pieceResult, 10)

	ws := newWebseedWorker(srv.URL+"/bad.bin", tor, picker, results, nil)

	ctx, cancel := context.WithCancel(context.Background())

	go ws.run(ctx)

	// Should get an error result (hash mismatch), and the piece should be
	// returned to the picker. The worker will retry, keep getting errors.
	r := <-results
	cancel()

	if r.err == nil {
		t.Fatal("expected hash mismatch error")
	}
}

func TestWebseedServerError(t *testing.T) {
	// Server returns 500.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	tor := &torrent.Torrent{
		Name:        "error.bin",
		PieceLength: 16,
		Pieces:      [][20]byte{{}},
		Length:      16,
	}

	picker := NewPicker(1)
	results := make(chan pieceResult, 10)

	ws := newWebseedWorker(srv.URL+"/error.bin", tor, picker, results, nil)

	ctx, cancel := context.WithCancel(context.Background())

	go ws.run(ctx)

	r := <-results
	cancel()

	if r.err == nil {
		t.Fatal("expected HTTP error")
	}
}
