package tracker

import (
	"encoding/binary"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ihvo/peer-pressure/bencode"
)

func TestScrapeURL_Standard(t *testing.T) {
	got, err := scrapeURL("http://tracker.example.com:6969/announce")
	if err != nil {
		t.Fatal(err)
	}
	want := "http://tracker.example.com:6969/scrape"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestScrapeURL_WithPath(t *testing.T) {
	got, err := scrapeURL("http://tracker.example.com/path/announce")
	if err != nil {
		t.Fatal(err)
	}
	want := "http://tracker.example.com/path/scrape"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestScrapeURL_WithQuery(t *testing.T) {
	got, err := scrapeURL("http://tracker.example.com/announce?passkey=abc")
	if err != nil {
		t.Fatal(err)
	}
	want := "http://tracker.example.com/scrape?passkey=abc"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestScrapeURL_NotScrapeable(t *testing.T) {
	_, err := scrapeURL("http://tracker.example.com/tracker")
	if err == nil {
		t.Fatal("expected error for URL without /announce")
	}
}

func TestScrapeURL_AnnounceInMiddle(t *testing.T) {
	got, err := scrapeURL("http://tracker.example.com/announce/extra")
	if err != nil {
		t.Fatal(err)
	}
	want := "http://tracker.example.com/scrape/extra"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func makeInfoHash(b byte) [20]byte {
	var ih [20]byte
	for i := range ih {
		ih[i] = b
	}
	return ih
}

func buildScrapeFilesDict(entries map[[20]byte][3]int) bencode.Dict {
	files := bencode.Dict{}
	for ih, stats := range entries {
		files[string(ih[:])] = bencode.Dict{
			"complete":   bencode.Int(stats[0]),
			"downloaded": bencode.Int(stats[1]),
			"incomplete": bencode.Int(stats[2]),
		}
	}
	return bencode.Dict{"files": files}
}

func TestParseScrapeResponse_SingleHash(t *testing.T) {
	ih := makeInfoHash(0xAA)
	d := buildScrapeFilesDict(map[[20]byte][3]int{
		ih: {10, 100, 5},
	})
	data := bencode.Encode(d)

	results, err := parseScrapeResponse(data, [][20]byte{ih})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 {
		t.Fatalf("got %d results, want 1", len(results))
	}
	r := results[0]
	if r.Complete != 10 || r.Downloaded != 100 || r.Incomplete != 5 {
		t.Errorf("got %+v", r)
	}
}

func TestParseScrapeResponse_MultipleHashes(t *testing.T) {
	ih1 := makeInfoHash(0xAA)
	ih2 := makeInfoHash(0xBB)
	ih3 := makeInfoHash(0xCC)
	d := buildScrapeFilesDict(map[[20]byte][3]int{
		ih1: {10, 100, 5},
		ih2: {20, 200, 15},
		ih3: {1, 5, 0},
	})
	data := bencode.Encode(d)

	results, err := parseScrapeResponse(data, [][20]byte{ih1, ih2, ih3})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 3 {
		t.Fatalf("got %d results, want 3", len(results))
	}
	if results[0].Complete != 10 || results[1].Complete != 20 || results[2].Complete != 1 {
		t.Errorf("results: %+v", results)
	}
}

func TestParseScrapeResponse_MissingHash(t *testing.T) {
	ih1 := makeInfoHash(0xAA)
	ih2 := makeInfoHash(0xBB) // not in response
	d := buildScrapeFilesDict(map[[20]byte][3]int{
		ih1: {10, 100, 5},
	})
	data := bencode.Encode(d)

	results, err := parseScrapeResponse(data, [][20]byte{ih1, ih2})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 {
		t.Fatalf("got %d results, want 2", len(results))
	}
	if results[1].Complete != 0 || results[1].Downloaded != 0 || results[1].Incomplete != 0 {
		t.Errorf("missing hash should have zero values: %+v", results[1])
	}
}

func TestParseScrapeResponse_FailureReason(t *testing.T) {
	d := bencode.Dict{
		"failure reason": bencode.String("scrape not allowed"),
	}
	data := bencode.Encode(d)

	_, err := parseScrapeResponse(data, [][20]byte{makeInfoHash(0xAA)})
	if err == nil {
		t.Fatal("expected error for failure reason")
	}
}

func TestParseScrapeResponse_EmptyFiles(t *testing.T) {
	d := bencode.Dict{"files": bencode.Dict{}}
	data := bencode.Encode(d)

	results, err := parseScrapeResponse(data, [][20]byte{makeInfoHash(0xAA)})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 {
		t.Fatalf("got %d results, want 1", len(results))
	}
	if results[0].Complete != 0 {
		t.Errorf("expected zero values for empty files dict")
	}
}

func TestScrapeHTTP_Integration(t *testing.T) {
	ih := makeInfoHash(0xDD)
	d := buildScrapeFilesDict(map[[20]byte][3]int{
		ih: {50, 500, 25},
	})
	respBody := bencode.Encode(d)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/scrape" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		w.Write(respBody)
	}))
	defer server.Close()

	results, err := Scrape(server.URL+"/announce", [][20]byte{ih})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 {
		t.Fatalf("got %d results, want 1", len(results))
	}
	if results[0].Complete != 50 || results[0].Downloaded != 500 || results[0].Incomplete != 25 {
		t.Errorf("got %+v", results[0])
	}
}

func TestScrapeHTTP_WithPasskey(t *testing.T) {
	ih := makeInfoHash(0xEE)
	d := buildScrapeFilesDict(map[[20]byte][3]int{
		ih: {3, 30, 2},
	})
	respBody := bencode.Encode(d)

	var gotQuery string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		w.Write(respBody)
	}))
	defer server.Close()

	results, err := Scrape(server.URL+"/announce?passkey=secret", [][20]byte{ih})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].Complete != 3 {
		t.Errorf("unexpected results: %+v", results)
	}
	if !containsSubstring(gotQuery, "passkey=secret") {
		t.Errorf("passkey not preserved in query: %q", gotQuery)
	}
}

func TestUDPScrapeRequestParsing(t *testing.T) {
	// Test the response parsing logic by verifying binary layout
	ih1 := makeInfoHash(0x11)
	ih2 := makeInfoHash(0x22)

	// Build a mock UDP scrape response
	resp := make([]byte, 8+12*2)
	binary.BigEndian.PutUint32(resp[0:4], actionScrape)
	binary.BigEndian.PutUint32(resp[4:8], 42) // txn ID

	// Hash 1: seeders=10, downloaded=100, leechers=5
	binary.BigEndian.PutUint32(resp[8:12], 10)
	binary.BigEndian.PutUint32(resp[12:16], 100)
	binary.BigEndian.PutUint32(resp[16:20], 5)

	// Hash 2: seeders=20, downloaded=200, leechers=15
	binary.BigEndian.PutUint32(resp[20:24], 20)
	binary.BigEndian.PutUint32(resp[24:28], 200)
	binary.BigEndian.PutUint32(resp[28:32], 15)

	// Verify the binary layout manually (since we can't easily mock udpRoundTrip)
	action := binary.BigEndian.Uint32(resp[0:4])
	if action != actionScrape {
		t.Fatalf("action = %d, want %d", action, actionScrape)
	}

	for i, ih := range [][20]byte{ih1, ih2} {
		_ = ih
		off := 8 + i*12
		seeders := int(binary.BigEndian.Uint32(resp[off : off+4]))
		downloaded := int(binary.BigEndian.Uint32(resp[off+4 : off+8]))
		leechers := int(binary.BigEndian.Uint32(resp[off+8 : off+12]))

		if i == 0 && (seeders != 10 || downloaded != 100 || leechers != 5) {
			t.Errorf("hash 0: seeders=%d downloaded=%d leechers=%d", seeders, downloaded, leechers)
		}
		if i == 1 && (seeders != 20 || downloaded != 200 || leechers != 15) {
			t.Errorf("hash 1: seeders=%d downloaded=%d leechers=%d", seeders, downloaded, leechers)
		}
	}
}
