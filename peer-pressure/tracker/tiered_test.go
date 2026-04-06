package tracker

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ihvo/peer-pressure/bencode"
)

func TestTieredAnnouncerShuffle(t *testing.T) {
	tiers := [][]string{
		{"http://a", "http://b", "http://c", "http://d", "http://e"},
	}

	// Create many announcers; at least one should have a different order.
	orders := make(map[string]bool)
	for range 20 {
		ta := NewTieredAnnouncer(tiers, "")
		tier := ta.Tiers()[0]
		key := fmt.Sprintf("%v", tier)
		orders[key] = true
	}
	if len(orders) < 2 {
		t.Error("expected shuffled orders to vary")
	}
}

func TestTieredAnnouncerFallbackToAnnounce(t *testing.T) {
	ta := NewTieredAnnouncer(nil, "http://fallback")
	tiers := ta.Tiers()
	if len(tiers) != 1 || len(tiers[0]) != 1 || tiers[0][0] != "http://fallback" {
		t.Errorf("expected single-tier fallback, got %v", tiers)
	}
}

func TestTieredAnnouncerNoTrackers(t *testing.T) {
	ta := NewTieredAnnouncer(nil, "")
	_, err := ta.Announce(AnnounceParams{})
	if err == nil {
		t.Error("expected error with no trackers")
	}
}

func TestTieredAnnouncerPromotion(t *testing.T) {
	// Set up: tier with 3 trackers, first two fail, third succeeds.
	callCount := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		// Return a valid tracker response.
		resp := bencode.Dict{
			"interval": bencode.Int(1800),
			"peers":    bencode.String(""),
		}
		w.Write(bencode.Encode(resp))
	}))
	defer srv.Close()

	tiers := [][]string{
		{"http://bad1.invalid:9999/announce", "http://bad2.invalid:9999/announce", srv.URL + "/announce"},
	}

	ta := NewTieredAnnouncer(tiers, "")
	// Force order: bad1, bad2, good
	ta.tiers[0] = []string{"http://bad1.invalid:9999/announce", "http://bad2.invalid:9999/announce", srv.URL + "/announce"}

	_, err := ta.Announce(AnnounceParams{
		InfoHash: [20]byte{1},
		PeerID:   [20]byte{2},
		Port:     6881,
	})
	if err != nil {
		t.Fatalf("announce: %v", err)
	}

	// After success, the working tracker should be promoted to front.
	tier := ta.Tiers()[0]
	if tier[0] != srv.URL+"/announce" {
		t.Errorf("expected promoted tracker at front, got %v", tier)
	}
}

func TestTieredAnnouncerTierCascade(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := bencode.Dict{
			"interval": bencode.Int(1800),
			"peers":    bencode.String(""),
		}
		w.Write(bencode.Encode(resp))
	}))
	defer srv.Close()

	ta := NewTieredAnnouncer(nil, "")
	// Manually set tiers: tier 0 = all bad, tier 1 = good
	ta.tiers = [][]string{
		{"http://bad1.invalid:9999/announce"},
		{srv.URL + "/announce"},
	}

	_, err := ta.Announce(AnnounceParams{
		InfoHash: [20]byte{1},
		PeerID:   [20]byte{2},
		Port:     6881,
	})
	if err != nil {
		t.Fatalf("expected tier cascade to succeed: %v", err)
	}
}
