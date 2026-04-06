package download

import (
	"context"
	"crypto/sha1"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/ihvo/peer-pressure/torrent"
)

func TestHTTPSeedFetchPiece(t *testing.T) {
	// Known piece data.
	pieceData := make([]byte, 256)
	for i := range pieceData {
		pieceData[i] = byte(i)
	}
	hash := sha1.Sum(pieceData)

	tor := &torrent.Torrent{
		Name:        "test",
		PieceLength: 256,
		Pieces:      [][20]byte{hash},
		Length:      256,
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("info_hash") == "" {
			t.Error("missing info_hash parameter")
		}
		if q.Get("piece") != "0" {
			t.Errorf("piece = %q, want 0", q.Get("piece"))
		}
		w.Write(pieceData)
	}))
	defer server.Close()

	picker := NewPicker(1)
	results := make(chan pieceResult, 1)
	ws := newHTTPSeedWorker(server.URL+"/seed", tor, picker, results, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	go ws.run(ctx)

	select {
	case res := <-results:
		if res.err != nil {
			t.Fatalf("unexpected error: %v", res.err)
		}
		if res.index != 0 {
			t.Errorf("index = %d, want 0", res.index)
		}
		if len(res.data) != 256 {
			t.Errorf("len(data) = %d, want 256", len(res.data))
		}
	case <-ctx.Done():
		t.Fatal("timeout waiting for result")
	}
}

func TestHTTPSeedMultiplePieces(t *testing.T) {
	pieces := make([][]byte, 3)
	hashes := make([][20]byte, 3)
	for i := range pieces {
		pieces[i] = make([]byte, 128)
		for j := range pieces[i] {
			pieces[i][j] = byte(i*128 + j)
		}
		hashes[i] = sha1.Sum(pieces[i])
	}

	tor := &torrent.Torrent{
		Name:        "multi",
		PieceLength: 128,
		Pieces:      hashes,
		Length:      384,
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		idx := q.Get("piece")
		switch idx {
		case "0":
			w.Write(pieces[0])
		case "1":
			w.Write(pieces[1])
		case "2":
			w.Write(pieces[2])
		default:
			http.Error(w, "bad piece", http.StatusBadRequest)
		}
	}))
	defer server.Close()

	picker := NewPicker(3)
	results := make(chan pieceResult, 3)
	ws := newHTTPSeedWorker(server.URL+"/seed", tor, picker, results, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	go ws.run(ctx)

	got := 0
	for got < 3 {
		select {
		case res := <-results:
			if res.err != nil {
				t.Fatalf("piece %d: %v", res.index, res.err)
			}
			got++
		case <-ctx.Done():
			t.Fatalf("timeout after %d pieces", got)
		}
	}
}

func TestHTTPSeed503Retry(t *testing.T) {
	calls := 0
	pieceData := make([]byte, 64)
	hash := sha1.Sum(pieceData)

	tor := &torrent.Torrent{
		Name:        "retry",
		PieceLength: 64,
		Pieces:      [][20]byte{hash},
		Length:      64,
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			fmt.Fprint(w, "1") // retry after 1 second
			return
		}
		w.Write(pieceData)
	}))
	defer server.Close()

	picker := NewPicker(1)
	results := make(chan pieceResult, 2)
	ws := newHTTPSeedWorker(server.URL+"/seed", tor, picker, results, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	go ws.run(ctx)

	// First result is the error.
	select {
	case res := <-results:
		if res.err == nil {
			t.Fatal("expected 503 error on first attempt")
		}
	case <-ctx.Done():
		t.Fatal("timeout")
	}

	// Second result should succeed after retry.
	select {
	case res := <-results:
		if res.err != nil {
			t.Fatalf("second attempt error: %v", res.err)
		}
		if res.index != 0 {
			t.Errorf("index = %d", res.index)
		}
	case <-ctx.Done():
		t.Fatal("timeout waiting for retry result")
	}
}

func TestHTTPSeedHashMismatch(t *testing.T) {
	badData := make([]byte, 64)
	goodHash := sha1.Sum([]byte("correct data that doesn't match"))

	tor := &torrent.Torrent{
		Name:        "mismatch",
		PieceLength: 64,
		Pieces:      [][20]byte{goodHash},
		Length:      64,
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(badData)
	}))
	defer server.Close()

	picker := NewPicker(1)
	results := make(chan pieceResult, 2)
	ws := newHTTPSeedWorker(server.URL+"/seed", tor, picker, results, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	go ws.run(ctx)

	select {
	case res := <-results:
		if res.err == nil {
			t.Fatal("expected hash mismatch error")
		}
	case <-ctx.Done():
		t.Fatal("timeout")
	}
}

func TestPercentEncodeInfoHash(t *testing.T) {
	var hash [20]byte
	// Mix of unreserved and reserved bytes
	hash[0] = 'a'
	hash[1] = 0xFF
	hash[2] = '5'
	hash[3] = 0x00

	got := percentEncodeInfoHash(hash)
	// First 4 bytes encode to: a%FF5%00 = 8 chars
	if len(got) < 8 {
		t.Fatalf("too short: %q", got)
	}
	if got[:8] != "a%FF5%00" {
		t.Errorf("prefix = %q, want a%%FF5%%00", got[:8])
	}
}

func TestPercentEncodeInfoHashAllZeros(t *testing.T) {
	var hash [20]byte
	got := percentEncodeInfoHash(hash)
	// All zeros → all percent-encoded
	want := "%00%00%00%00%00%00%00%00%00%00%00%00%00%00%00%00%00%00%00%00"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}
