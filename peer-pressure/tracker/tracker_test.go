package tracker

import (
	"encoding/binary"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ihvo/peer-pressure/bencode"
)

// --- Compact peer parsing ---

func TestParseCompactPeers(t *testing.T) {
	// Two peers: 192.168.1.100:6881 and 10.0.0.1:8080
	data := make([]byte, 12)
	// Peer 1
	data[0], data[1], data[2], data[3] = 192, 168, 1, 100
	binary.BigEndian.PutUint16(data[4:6], 6881)
	// Peer 2
	data[6], data[7], data[8], data[9] = 10, 0, 0, 1
	binary.BigEndian.PutUint16(data[10:12], 8080)

	peers, err := parseCompactPeers(data)
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	if len(peers) != 2 {
		t.Fatalf("got %d peers, want 2", len(peers))
	}

	if !peers[0].IP.Equal(net.IPv4(192, 168, 1, 100)) {
		t.Errorf("peer[0].IP = %v", peers[0].IP)
	}
	if peers[0].Port != 6881 {
		t.Errorf("peer[0].Port = %d", peers[0].Port)
	}
	if !peers[1].IP.Equal(net.IPv4(10, 0, 0, 1)) {
		t.Errorf("peer[1].IP = %v", peers[1].IP)
	}
	if peers[1].Port != 8080 {
		t.Errorf("peer[1].Port = %d", peers[1].Port)
	}
}

func TestParseCompactPeersEmpty(t *testing.T) {
	peers, err := parseCompactPeers([]byte{})
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	if len(peers) != 0 {
		t.Errorf("got %d peers, want 0", len(peers))
	}
}

func TestParseCompactPeersBadLength(t *testing.T) {
	_, err := parseCompactPeers(make([]byte, 7)) // not a multiple of 6
	if err == nil {
		t.Error("expected error for bad length")
	}
}

// --- Dict peer parsing ---

func TestParseDictPeers(t *testing.T) {
	list := bencode.List{
		bencode.Dict{
			"ip":   bencode.String("192.168.1.100"),
			"port": bencode.Int(6881),
		},
		bencode.Dict{
			"ip":   bencode.String("10.0.0.1"),
			"port": bencode.Int(8080),
		},
	}

	peers, err := parseDictPeers(list)
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	if len(peers) != 2 {
		t.Fatalf("got %d peers, want 2", len(peers))
	}
	if peers[0].Port != 6881 {
		t.Errorf("peer[0].Port = %d", peers[0].Port)
	}
	if peers[1].Addr() != "10.0.0.1:8080" {
		t.Errorf("peer[1].Addr() = %q", peers[1].Addr())
	}
}

// --- Response parsing ---

func TestParseResponseCompact(t *testing.T) {
	peerData := make([]byte, 6)
	peerData[0], peerData[1], peerData[2], peerData[3] = 127, 0, 0, 1
	binary.BigEndian.PutUint16(peerData[4:6], 9999)

	resp := bencode.Dict{
		"complete":   bencode.Int(10),
		"incomplete": bencode.Int(5),
		"interval":   bencode.Int(900),
		"peers":      bencode.String(peerData),
	}

	r, err := parseResponse(bencode.Encode(resp))
	if err != nil {
		t.Fatalf("error: %v", err)
	}

	if r.Interval != 900 {
		t.Errorf("Interval = %d", r.Interval)
	}
	if r.Complete != 10 {
		t.Errorf("Complete = %d", r.Complete)
	}
	if r.Incomplete != 5 {
		t.Errorf("Incomplete = %d", r.Incomplete)
	}
	if len(r.Peers) != 1 {
		t.Fatalf("len(Peers) = %d", len(r.Peers))
	}
	if !r.Peers[0].IP.Equal(net.IPv4(127, 0, 0, 1)) {
		t.Errorf("Peers[0].IP = %v", r.Peers[0].IP)
	}
	if r.Peers[0].Port != 9999 {
		t.Errorf("Peers[0].Port = %d", r.Peers[0].Port)
	}
}

func TestParseResponseFailure(t *testing.T) {
	resp := bencode.Dict{
		"failure reason": bencode.String("torrent not found"),
	}

	_, err := parseResponse(bencode.Encode(resp))
	if err == nil {
		t.Error("expected error for failure reason")
	}
}

// --- Percent encoding ---

func TestPercentEncodeBytes(t *testing.T) {
	// Mix of unreserved and reserved bytes
	input := []byte{0x12, 'a', 0xFF, '5', 0x00}
	got := percentEncodeBytes(input)
	want := "%12a%FF5%00"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestPercentEncodeBytesAllUnreserved(t *testing.T) {
	input := []byte("hello-world_2.0~")
	got := percentEncodeBytes(input)
	want := "hello-world_2.0~"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// --- Peer.String and Peer.Addr ---

func TestPeerString(t *testing.T) {
	p := Peer{IP: net.IPv4(10, 0, 0, 1), Port: 6881}
	if p.String() != "10.0.0.1:6881" {
		t.Errorf("String() = %q", p.String())
	}
	if p.Addr() != "10.0.0.1:6881" {
		t.Errorf("Addr() = %q", p.Addr())
	}
}

// --- Full announce integration with mock tracker ---

func TestAnnounceWithMockTracker(t *testing.T) {
	// Build a mock tracker that validates the request and returns peers
	peerData := make([]byte, 6)
	peerData[0], peerData[1], peerData[2], peerData[3] = 1, 2, 3, 4
	binary.BigEndian.PutUint16(peerData[4:6], 5555)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify required params are present
		q := r.URL.Query()
		if q.Get("port") == "" {
			t.Error("missing 'port' param")
		}
		if q.Get("compact") != "1" {
			t.Error("compact should be 1")
		}
		if q.Get("left") == "" {
			t.Error("missing 'left' param")
		}
		// info_hash and peer_id are in the raw query (percent-encoded binary)
		if !containsSubstring(r.URL.RawQuery, "info_hash=") {
			t.Error("missing info_hash in raw query")
		}
		if !containsSubstring(r.URL.RawQuery, "peer_id=") {
			t.Error("missing peer_id in raw query")
		}

		resp := bencode.Dict{
			"interval": bencode.Int(1800),
			"peers":    bencode.String(peerData),
		}
		w.Write(bencode.Encode(resp))
	}))
	defer server.Close()

	params := AnnounceParams{
		InfoHash:   [20]byte{0x01, 0x02, 0x03},
		PeerID:     [20]byte{'-', 'P', 'P', '0', '0', '0', '1', '-'},
		Port:       6881,
		Uploaded:   0,
		Downloaded: 0,
		Left:       1000000,
	}

	resp, err := Announce(server.URL+"/announce", params)
	if err != nil {
		t.Fatalf("Announce error: %v", err)
	}

	if resp.Interval != 1800 {
		t.Errorf("Interval = %d, want 1800", resp.Interval)
	}
	if len(resp.Peers) != 1 {
		t.Fatalf("len(Peers) = %d, want 1", len(resp.Peers))
	}
	if !resp.Peers[0].IP.Equal(net.IPv4(1, 2, 3, 4)) {
		t.Errorf("Peers[0].IP = %v", resp.Peers[0].IP)
	}
	if resp.Peers[0].Port != 5555 {
		t.Errorf("Peers[0].Port = %d", resp.Peers[0].Port)
	}
}

func TestAnnounceTrackerError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := bencode.Dict{
			"failure reason": bencode.String("info_hash not found"),
		}
		w.Write(bencode.Encode(resp))
	}))
	defer server.Close()

	_, err := Announce(server.URL, AnnounceParams{})
	if err == nil {
		t.Error("expected error for tracker failure response")
	}
}

func TestAnnounceHTTPError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	_, err := Announce(server.URL, AnnounceParams{})
	if err == nil {
		t.Error("expected error for HTTP 500")
	}
}

func containsSubstring(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

// --- BEP 24: External IP tests ---

func buildResponseDict(extra map[string]bencode.Value) []byte {
	d := bencode.Dict{
		"interval": bencode.Int(900),
		"peers":    bencode.String(""),
	}
	for k, v := range extra {
		d[k] = v
	}
	return bencode.Encode(d)
}

func TestParseResponse_ExternalIPv4(t *testing.T) {
	ip := bencode.String([]byte{203, 0, 113, 42})
	data := buildResponseDict(map[string]bencode.Value{"external ip": ip})
	r, err := parseResponse(data)
	if err != nil {
		t.Fatal(err)
	}
	want := net.IPv4(203, 0, 113, 42)
	if !r.ExternalIP.Equal(want) {
		t.Errorf("ExternalIP = %v, want %v", r.ExternalIP, want)
	}
}

func TestParseResponse_ExternalIPv6(t *testing.T) {
	raw := net.ParseIP("2001:db8::1")
	ip := bencode.String([]byte(raw.To16()))
	data := buildResponseDict(map[string]bencode.Value{"external ip": ip})
	r, err := parseResponse(data)
	if err != nil {
		t.Fatal(err)
	}
	want := net.ParseIP("2001:db8::1")
	if !r.ExternalIP.Equal(want) {
		t.Errorf("ExternalIP = %v, want %v", r.ExternalIP, want)
	}
}

func TestParseResponse_ExternalIPMissing(t *testing.T) {
	data := buildResponseDict(nil)
	r, err := parseResponse(data)
	if err != nil {
		t.Fatal(err)
	}
	if r.ExternalIP != nil {
		t.Errorf("ExternalIP = %v, want nil", r.ExternalIP)
	}
}

func TestParseResponse_ExternalIPInvalidLength(t *testing.T) {
	ip := bencode.String([]byte{1, 2, 3, 4, 5, 6, 7})
	data := buildResponseDict(map[string]bencode.Value{"external ip": ip})
	r, err := parseResponse(data)
	if err != nil {
		t.Fatal(err)
	}
	if r.ExternalIP != nil {
		t.Errorf("ExternalIP = %v, want nil for invalid length", r.ExternalIP)
	}
}

func TestParseResponse_ExternalIPEmptyString(t *testing.T) {
	ip := bencode.String("")
	data := buildResponseDict(map[string]bencode.Value{"external ip": ip})
	r, err := parseResponse(data)
	if err != nil {
		t.Fatal(err)
	}
	if r.ExternalIP != nil {
		t.Errorf("ExternalIP = %v, want nil for empty", r.ExternalIP)
	}
}

func TestParseResponse_ExternalIPNotString(t *testing.T) {
	data := buildResponseDict(map[string]bencode.Value{"external ip": bencode.Int(42)})
	r, err := parseResponse(data)
	if err != nil {
		t.Fatal(err)
	}
	if r.ExternalIP != nil {
		t.Errorf("ExternalIP = %v, want nil for non-string type", r.ExternalIP)
	}
}

func TestParseResponse_ExternalIPWithPeers(t *testing.T) {
	peer1 := []byte{192, 168, 1, 1, 0x1A, 0xE1}
	ip := bencode.String([]byte{10, 0, 0, 1})
	d := bencode.Dict{
		"interval":    bencode.Int(900),
		"peers":       bencode.String(peer1),
		"external ip": ip,
	}
	data := bencode.Encode(d)
	r, err := parseResponse(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Peers) != 1 {
		t.Fatalf("got %d peers, want 1", len(r.Peers))
	}
	if !r.Peers[0].IP.Equal(net.IPv4(192, 168, 1, 1)) {
		t.Errorf("peer IP = %v", r.Peers[0].IP)
	}
	if !r.ExternalIP.Equal(net.IPv4(10, 0, 0, 1)) {
		t.Errorf("ExternalIP = %v, want 10.0.0.1", r.ExternalIP)
	}
}

func TestParseResponse_ExternalIPLoopback(t *testing.T) {
	ip := bencode.String([]byte{127, 0, 0, 1})
	data := buildResponseDict(map[string]bencode.Value{"external ip": ip})
	r, err := parseResponse(data)
	if err != nil {
		t.Fatal(err)
	}
	if !r.ExternalIP.Equal(net.IPv4(127, 0, 0, 1)) {
		t.Errorf("ExternalIP = %v, want 127.0.0.1", r.ExternalIP)
	}
}
