package tracker

import (
	"encoding/binary"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/ihvo/peer-pressure/bencode"
)

// mockUDPTracker runs a minimal UDP tracker that handles connect + announce.
// It listens on a random local port and serves one connect/announce cycle.
type mockUDPTracker struct {
	conn    *net.UDPConn
	addr    *net.UDPAddr
	peers   []byte // compact peer data to return
	seeders uint32
	leechers uint32
}

func newMockUDPTracker(t *testing.T, peers []byte) *mockUDPTracker {
	t.Helper()
	addr, _ := net.ResolveUDPAddr("udp", "127.0.0.1:0")
	conn, err := net.ListenUDP("udp", addr)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	return &mockUDPTracker{
		conn:     conn,
		addr:     conn.LocalAddr().(*net.UDPAddr),
		peers:    peers,
		seeders:  42,
		leechers: 7,
	}
}

// serve handles exactly one connect + one announce, then returns.
// Runs in a goroutine; errors are sent to errCh.
func (m *mockUDPTracker) serve(errCh chan<- error) {
	defer m.conn.Close()
	buf := make([]byte, 512)

	// Phase 1: connect request
	m.conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	n, clientAddr, err := m.conn.ReadFromUDP(buf)
	if err != nil {
		errCh <- err
		return
	}
	if n < 16 {
		errCh <- fmt.Errorf("connect request too short: %d bytes", n)
		return
	}

	protocolID := binary.BigEndian.Uint64(buf[0:8])
	action := binary.BigEndian.Uint32(buf[8:12])
	txnID := binary.BigEndian.Uint32(buf[12:16])

	if protocolID != udpProtocolID {
		errCh <- fmt.Errorf("bad protocol ID: %x", protocolID)
		return
	}
	if action != actionConnect {
		errCh <- fmt.Errorf("expected connect action, got %d", action)
		return
	}

	// Send connect response with a known connection_id.
	connID := uint64(0xDEADBEEF12345678)
	var resp [16]byte
	binary.BigEndian.PutUint32(resp[0:4], actionConnect)
	binary.BigEndian.PutUint32(resp[4:8], txnID)
	binary.BigEndian.PutUint64(resp[8:16], connID)
	m.conn.WriteToUDP(resp[:], clientAddr)

	// Phase 2: announce request
	m.conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	n, clientAddr, err = m.conn.ReadFromUDP(buf)
	if err != nil {
		errCh <- err
		return
	}
	if n < 98 {
		errCh <- fmt.Errorf("announce request too short: %d bytes", n)
		return
	}

	gotConnID := binary.BigEndian.Uint64(buf[0:8])
	action = binary.BigEndian.Uint32(buf[8:12])
	txnID = binary.BigEndian.Uint32(buf[12:16])

	if gotConnID != connID {
		errCh <- fmt.Errorf("bad connection ID: %x", gotConnID)
		return
	}
	if action != actionAnnounce {
		errCh <- fmt.Errorf("expected announce action, got %d", action)
		return
	}

	// Send announce response with peers.
	announceResp := make([]byte, 20+len(m.peers))
	binary.BigEndian.PutUint32(announceResp[0:4], actionAnnounce)
	binary.BigEndian.PutUint32(announceResp[4:8], txnID)
	binary.BigEndian.PutUint32(announceResp[8:12], 1800) // interval
	binary.BigEndian.PutUint32(announceResp[12:16], m.leechers)
	binary.BigEndian.PutUint32(announceResp[16:20], m.seeders)
	copy(announceResp[20:], m.peers)
	m.conn.WriteToUDP(announceResp, clientAddr)

	errCh <- nil
}

func (m *mockUDPTracker) url() string {
	return fmt.Sprintf("udp://127.0.0.1:%d/announce", m.addr.Port)
}

// --- Tests ---

func TestUDPConnectRequestEncoding(t *testing.T) {
	// Verify the connect request is exactly 16 bytes with correct fields.
	txnID := randUint32()

	var req [16]byte
	binary.BigEndian.PutUint64(req[0:8], udpProtocolID)
	binary.BigEndian.PutUint32(req[8:12], actionConnect)
	binary.BigEndian.PutUint32(req[12:16], txnID)

	if len(req) != 16 {
		t.Fatalf("connect request size = %d, want 16", len(req))
	}
	if binary.BigEndian.Uint64(req[0:8]) != 0x41727101980 {
		t.Error("bad protocol ID")
	}
	if binary.BigEndian.Uint32(req[8:12]) != 0 {
		t.Error("action should be 0 (connect)")
	}
	if binary.BigEndian.Uint32(req[12:16]) != txnID {
		t.Error("transaction ID mismatch")
	}
}

func TestUDPAnnounceRequestEncoding(t *testing.T) {
	// Verify announce request is 98 bytes with correct field placement.
	params := AnnounceParams{
		InfoHash:   [20]byte{0xAA, 0xBB},
		PeerID:     [20]byte{'-', 'P', 'P'},
		Port:       6881,
		Downloaded: 1000,
		Left:       5000,
		Uploaded:   500,
		Event:      "started",
		NumWant:    200,
	}

	var req [98]byte
	connID := uint64(0x1234567890ABCDEF)
	txnID := uint32(42)

	binary.BigEndian.PutUint64(req[0:8], connID)
	binary.BigEndian.PutUint32(req[8:12], actionAnnounce)
	binary.BigEndian.PutUint32(req[12:16], txnID)
	copy(req[16:36], params.InfoHash[:])
	copy(req[36:56], params.PeerID[:])
	binary.BigEndian.PutUint64(req[56:64], uint64(params.Downloaded))
	binary.BigEndian.PutUint64(req[64:72], uint64(params.Left))
	binary.BigEndian.PutUint64(req[72:80], uint64(params.Uploaded))
	binary.BigEndian.PutUint32(req[80:84], eventCode(params.Event))
	binary.BigEndian.PutUint16(req[96:98], params.Port)

	if len(req) != 98 {
		t.Fatalf("announce request size = %d, want 98", len(req))
	}
	// Verify info_hash is at offset 16
	if req[16] != 0xAA || req[17] != 0xBB {
		t.Error("info_hash at wrong offset")
	}
	// Verify event code for "started" = 2
	if binary.BigEndian.Uint32(req[80:84]) != 2 {
		t.Errorf("event = %d, want 2 (started)", binary.BigEndian.Uint32(req[80:84]))
	}
	// Verify port at offset 96
	if binary.BigEndian.Uint16(req[96:98]) != 6881 {
		t.Errorf("port = %d, want 6881", binary.BigEndian.Uint16(req[96:98]))
	}
}

func TestUDPFullExchange(t *testing.T) {
	// Integration: mock UDP tracker handles connect + announce, returns peers.
	peerData := make([]byte, 12) // 2 peers
	peerData[0], peerData[1], peerData[2], peerData[3] = 10, 0, 0, 1
	binary.BigEndian.PutUint16(peerData[4:6], 6881)
	peerData[6], peerData[7], peerData[8], peerData[9] = 172, 16, 0, 1
	binary.BigEndian.PutUint16(peerData[10:12], 8080)

	mock := newMockUDPTracker(t, peerData)
	errCh := make(chan error, 1)
	go mock.serve(errCh)

	resp, err := announceUDP(mock.url(), AnnounceParams{
		InfoHash: [20]byte{1, 2, 3},
		PeerID:   [20]byte{'-', 'P', 'P'},
		Port:     6881,
		Left:     999,
		Event:    "started",
		NumWant:  50,
	})
	if err != nil {
		t.Fatalf("announceUDP: %v", err)
	}

	// Check mock server didn't error
	if mockErr := <-errCh; mockErr != nil {
		t.Fatalf("mock tracker error: %v", mockErr)
	}

	if resp.Interval != 1800 {
		t.Errorf("Interval = %d, want 1800", resp.Interval)
	}
	if resp.Complete != 42 {
		t.Errorf("Complete (seeders) = %d, want 42", resp.Complete)
	}
	if resp.Incomplete != 7 {
		t.Errorf("Incomplete (leechers) = %d, want 7", resp.Incomplete)
	}
	if len(resp.Peers) != 2 {
		t.Fatalf("len(Peers) = %d, want 2", len(resp.Peers))
	}
	if !resp.Peers[0].IP.Equal(net.IPv4(10, 0, 0, 1)) {
		t.Errorf("Peers[0].IP = %v", resp.Peers[0].IP)
	}
	if resp.Peers[0].Port != 6881 {
		t.Errorf("Peers[0].Port = %d", resp.Peers[0].Port)
	}
	if !resp.Peers[1].IP.Equal(net.IPv4(172, 16, 0, 1)) {
		t.Errorf("Peers[1].IP = %v", resp.Peers[1].IP)
	}
}

func TestUDPTransactionIDMismatch(t *testing.T) {
	// Server responds with wrong transaction ID — client should reject.
	addr, _ := net.ResolveUDPAddr("udp", "127.0.0.1:0")
	srvConn, err := net.ListenUDP("udp", addr)
	if err != nil {
		t.Fatal(err)
	}
	srvAddr := srvConn.LocalAddr().(*net.UDPAddr)

	go func() {
		defer srvConn.Close()
		buf := make([]byte, 256)
		srvConn.SetReadDeadline(time.Now().Add(5 * time.Second))
		n, clientAddr, err := srvConn.ReadFromUDP(buf)
		if err != nil || n < 16 {
			return
		}
		// Respond with wrong txn_id (original + 1)
		txnID := binary.BigEndian.Uint32(buf[12:16])
		var resp [16]byte
		binary.BigEndian.PutUint32(resp[0:4], actionConnect)
		binary.BigEndian.PutUint32(resp[4:8], txnID+1) // WRONG
		binary.BigEndian.PutUint64(resp[8:16], 0xDEAD)
		srvConn.WriteToUDP(resp[:], clientAddr)
	}()

	url := fmt.Sprintf("udp://127.0.0.1:%d/announce", srvAddr.Port)
	_, err = announceUDP(url, AnnounceParams{})
	if err == nil {
		t.Error("expected error for transaction ID mismatch")
	}
}

func TestUDPTrackerErrorResponse(t *testing.T) {
	// Server responds with action=3 (error) + error message.
	addr, _ := net.ResolveUDPAddr("udp", "127.0.0.1:0")
	srvConn, err := net.ListenUDP("udp", addr)
	if err != nil {
		t.Fatal(err)
	}
	srvAddr := srvConn.LocalAddr().(*net.UDPAddr)

	go func() {
		defer srvConn.Close()
		buf := make([]byte, 256)
		srvConn.SetReadDeadline(time.Now().Add(5 * time.Second))
		n, clientAddr, err := srvConn.ReadFromUDP(buf)
		if err != nil || n < 16 {
			return
		}
		txnID := binary.BigEndian.Uint32(buf[12:16])
		// Send error response: action=3, txn_id, error message
		errMsg := []byte("info hash not found")
		resp := make([]byte, 8+len(errMsg))
		binary.BigEndian.PutUint32(resp[0:4], 3) // action=error
		binary.BigEndian.PutUint32(resp[4:8], txnID)
		copy(resp[8:], errMsg)
		srvConn.WriteToUDP(resp, clientAddr)
	}()

	url := fmt.Sprintf("udp://127.0.0.1:%d/announce", srvAddr.Port)
	_, err = announceUDP(url, AnnounceParams{})
	if err == nil {
		t.Error("expected error for tracker error response")
	}
}

func TestUDPParseURL(t *testing.T) {
	tests := []struct {
		url       string
		wantHost  string
		wantPQ    string
		err       bool
	}{
		{"udp://tracker.example.com:6969/announce", "tracker.example.com:6969", "/announce", false},
		{"udp://10.0.0.1:1337", "10.0.0.1:1337", "", false},
		{"udp://tracker.example.com:80/dir?a=b&c=d", "tracker.example.com:80", "/dir?a=b&c=d", false},
		{"http://example.com:80", "", "", true}, // wrong scheme
	}

	for _, tt := range tests {
		host, pq, err := udpParseURL(tt.url)
		if tt.err {
			if err == nil {
				t.Errorf("udpParseURL(%q): expected error", tt.url)
			}
			continue
		}
		if err != nil {
			t.Errorf("udpParseURL(%q): %v", tt.url, err)
			continue
		}
		if host != tt.wantHost {
			t.Errorf("udpParseURL(%q) host = %q, want %q", tt.url, host, tt.wantHost)
		}
		if pq != tt.wantPQ {
			t.Errorf("udpParseURL(%q) pathQuery = %q, want %q", tt.url, pq, tt.wantPQ)
		}
	}
}

func TestEventCode(t *testing.T) {
	tests := []struct {
		event string
		want  uint32
	}{
		{"", eventNone},
		{"started", eventStarted},
		{"completed", eventCompleted},
		{"stopped", eventStopped},
		{"unknown", eventNone},
	}
	for _, tt := range tests {
		if got := eventCode(tt.event); got != tt.want {
			t.Errorf("eventCode(%q) = %d, want %d", tt.event, got, tt.want)
		}
	}
}

func TestSchemeDispatch(t *testing.T) {
	// HTTP: use mock HTTP server
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := bencode.Dict{
			"interval": bencode.Int(900),
			"peers":    bencode.String([]byte{}),
		}
		w.Write(bencode.Encode(resp))
	}))
	defer server.Close()

	// HTTP announce should work via the unified Announce()
	resp, err := Announce(server.URL+"/announce", AnnounceParams{Port: 6881})
	if err != nil {
		t.Fatalf("HTTP announce: %v", err)
	}
	if resp.Interval != 900 {
		t.Errorf("HTTP interval = %d, want 900", resp.Interval)
	}

	// UDP: use mock UDP server
	peerData := make([]byte, 6)
	peerData[0], peerData[1], peerData[2], peerData[3] = 1, 2, 3, 4
	binary.BigEndian.PutUint16(peerData[4:6], 9999)
	mock := newMockUDPTracker(t, peerData)
	errCh := make(chan error, 1)
	go mock.serve(errCh)

	resp, err = Announce(mock.url(), AnnounceParams{Port: 6881, Left: 100})
	if err != nil {
		t.Fatalf("UDP announce: %v", err)
	}
	if <-errCh != nil {
		t.Fatal("mock tracker error")
	}
	if resp.Interval != 1800 {
		t.Errorf("UDP interval = %d, want 1800", resp.Interval)
	}
	if len(resp.Peers) != 1 || resp.Peers[0].Port != 9999 {
		t.Errorf("UDP peers: %+v", resp.Peers)
	}
}

// --- BEP 41: UDP Tracker Protocol Extensions ---

func TestEncodeURLDataOption(t *testing.T) {
	tests := []struct {
		name      string
		pathQuery string
		wantBytes []byte
	}{
		{
			name:      "empty",
			pathQuery: "",
			wantBytes: []byte{0x02, 0x00},
		},
		{
			name:      "short_path",
			pathQuery: "/dir?a=b&c=d",
			wantBytes: append([]byte{0x02, 12}, []byte("/dir?a=b&c=d")...),
		},
		{
			name:      "announce",
			pathQuery: "/announce",
			wantBytes: append([]byte{0x02, 9}, []byte("/announce")...),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := encodeURLDataOption(tt.pathQuery)
			if len(got) != len(tt.wantBytes) {
				t.Fatalf("len = %d, want %d", len(got), len(tt.wantBytes))
			}
			for i := range got {
				if got[i] != tt.wantBytes[i] {
					t.Errorf("byte[%d] = 0x%02x, want 0x%02x", i, got[i], tt.wantBytes[i])
				}
			}
		})
	}
}

func TestEncodeURLDataOptionLong(t *testing.T) {
	// Path longer than 255 bytes gets chunked.
	longPath := "/" + string(make([]byte, 300))
	for i := range longPath[1:] {
		longPath = longPath[:i+1] + "x" + longPath[i+2:]
	}
	longPath = "/" + repeatByte('x', 300)

	opts := encodeURLDataOption(longPath)

	// Should have at least 2 URLData chunks.
	if len(opts) < 4 {
		t.Fatalf("options too short: %d bytes", len(opts))
	}

	// Parse the options back.
	var assembled []byte
	i := 0
	for i < len(opts) {
		if opts[i] != optURLData {
			t.Fatalf("expected URLData option at offset %d, got 0x%02x", i, opts[i])
		}
		i++
		length := int(opts[i])
		i++
		assembled = append(assembled, opts[i:i+length]...)
		i += length
	}

	if string(assembled) != longPath {
		t.Errorf("reassembled path length = %d, want %d", len(assembled), len(longPath))
	}
}

func repeatByte(b byte, n int) string {
	buf := make([]byte, n)
	for i := range buf {
		buf[i] = b
	}
	return string(buf)
}
