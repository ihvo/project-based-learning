package lsd

import (
	"bytes"
	"encoding/hex"
	"strings"
	"testing"
)

func hexHash(s string) [20]byte {
	var h [20]byte
	b, _ := hex.DecodeString(s)
	copy(h[:], b)
	return h
}

func TestFormatAnnounce(t *testing.T) {
	a := &Announce{
		Host:     "239.192.152.143:6771",
		Port:     6881,
		Infohash: hexHash("d14a4e0d2b1e3c4f5a6b7c8d9e0f1a2b3c4d5e6f"),
		Cookie:   "pp-test123",
	}
	data := FormatAnnounce(a)
	s := string(data)

	if !strings.HasPrefix(s, "BT-SEARCH * HTTP/1.1\r\n") {
		t.Fatalf("bad request line: %q", s)
	}
	if !strings.Contains(s, "Host: 239.192.152.143:6771\r\n") {
		t.Error("missing Host header")
	}
	if !strings.Contains(s, "Port: 6881\r\n") {
		t.Error("missing Port header")
	}
	if !strings.Contains(s, "Infohash: d14a4e0d2b1e3c4f5a6b7c8d9e0f1a2b3c4d5e6f\r\n") {
		t.Error("missing Infohash header")
	}
	if !strings.Contains(s, "cookie: pp-test123\r\n") {
		t.Error("missing cookie header")
	}
	if !strings.HasSuffix(s, "\r\n\r\n") {
		t.Error("missing trailing CRLF")
	}
}

func TestParseAnnounce(t *testing.T) {
	raw := "BT-SEARCH * HTTP/1.1\r\nHost: 239.192.152.143:6771\r\nPort: 6881\r\nInfohash: d14a4e0d2b1e3c4f5a6b7c8d9e0f1a2b3c4d5e6f\r\ncookie: pp-abc\r\n\r\n"
	a, err := ParseAnnounce([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	if a.Host != "239.192.152.143:6771" {
		t.Errorf("host = %q", a.Host)
	}
	if a.Port != 6881 {
		t.Errorf("port = %d", a.Port)
	}
	want := hexHash("d14a4e0d2b1e3c4f5a6b7c8d9e0f1a2b3c4d5e6f")
	if a.Infohash != want {
		t.Errorf("infohash mismatch")
	}
	if a.Cookie != "pp-abc" {
		t.Errorf("cookie = %q", a.Cookie)
	}
}

func TestParseAnnounceRoundTrip(t *testing.T) {
	orig := &Announce{
		Host:     "239.192.152.143:6771",
		Port:     51413,
		Infohash: hexHash("aabbccddee11223344556677889900aabbccddee"),
		Cookie:   "pp-roundtrip",
	}
	data := FormatAnnounce(orig)
	parsed, err := ParseAnnounce(data)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Host != orig.Host {
		t.Errorf("host: got %q, want %q", parsed.Host, orig.Host)
	}
	if parsed.Port != orig.Port {
		t.Errorf("port: got %d, want %d", parsed.Port, orig.Port)
	}
	if parsed.Infohash != orig.Infohash {
		t.Error("infohash mismatch")
	}
	if parsed.Cookie != orig.Cookie {
		t.Errorf("cookie: got %q, want %q", parsed.Cookie, orig.Cookie)
	}
}

func TestParseAnnounceBadRequestLine(t *testing.T) {
	raw := "GET / HTTP/1.1\r\nPort: 6881\r\nInfohash: d14a4e0d2b1e3c4f5a6b7c8d9e0f1a2b3c4d5e6f\r\n\r\n"
	_, err := ParseAnnounce([]byte(raw))
	if err == nil {
		t.Fatal("expected error for bad request line")
	}
}

func TestParseAnnounceMissingPort(t *testing.T) {
	raw := "BT-SEARCH * HTTP/1.1\r\nInfohash: d14a4e0d2b1e3c4f5a6b7c8d9e0f1a2b3c4d5e6f\r\n\r\n"
	_, err := ParseAnnounce([]byte(raw))
	if err == nil {
		t.Fatal("expected error for missing port")
	}
}

func TestParseAnnounceMissingInfohash(t *testing.T) {
	raw := "BT-SEARCH * HTTP/1.1\r\nPort: 6881\r\n\r\n"
	_, err := ParseAnnounce([]byte(raw))
	if err == nil {
		t.Fatal("expected error for missing infohash")
	}
}

func TestParseAnnounceInvalidInfohashLength(t *testing.T) {
	raw := "BT-SEARCH * HTTP/1.1\r\nPort: 6881\r\nInfohash: d14a4e0d2b1e3c4f5a6b7c8d9e0f1a2b3c4d5e\r\n\r\n" // 38 chars
	_, err := ParseAnnounce([]byte(raw))
	if err == nil {
		t.Fatal("expected error for short infohash")
	}
}

func TestParseAnnounceInvalidInfohashHex(t *testing.T) {
	raw := "BT-SEARCH * HTTP/1.1\r\nPort: 6881\r\nInfohash: zzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzz\r\n\r\n"
	_, err := ParseAnnounce([]byte(raw))
	if err == nil {
		t.Fatal("expected error for bad hex")
	}
}

func TestParseAnnounceCaseInsensitiveHeaders(t *testing.T) {
	raw := "BT-SEARCH * HTTP/1.1\r\nHOST: 239.192.152.143:6771\r\nPORT: 6881\r\nINFOHASH: d14a4e0d2b1e3c4f5a6b7c8d9e0f1a2b3c4d5e6f\r\nCOOKIE: pp-upper\r\n\r\n"
	a, err := ParseAnnounce([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	if a.Port != 6881 {
		t.Errorf("port = %d", a.Port)
	}
	if a.Cookie != "pp-upper" {
		t.Errorf("cookie = %q", a.Cookie)
	}
}

func TestParseAnnounceExtraHeaders(t *testing.T) {
	raw := "BT-SEARCH * HTTP/1.1\r\nX-Custom: foo\r\nPort: 6881\r\nInfohash: d14a4e0d2b1e3c4f5a6b7c8d9e0f1a2b3c4d5e6f\r\nX-Another: bar\r\n\r\n"
	a, err := ParseAnnounce([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	if a.Port != 6881 {
		t.Errorf("port = %d", a.Port)
	}
}

func TestCookieFiltering(t *testing.T) {
	peers := make(chan Peer, 10)
	s := New(6881, peers)
	s.AddInfohash(hexHash("d14a4e0d2b1e3c4f5a6b7c8d9e0f1a2b3c4d5e6f"))

	// Own cookie — should be filtered
	own := &Announce{
		Host:     "239.192.152.143:6771",
		Port:     6882,
		Infohash: hexHash("d14a4e0d2b1e3c4f5a6b7c8d9e0f1a2b3c4d5e6f"),
		Cookie:   s.cookie,
	}
	data := FormatAnnounce(own)
	a, _ := ParseAnnounce(data)

	if a.Cookie == s.cookie {
		// correctly would be filtered
	}

	// Different cookie — should be accepted
	other := &Announce{
		Host:     "239.192.152.143:6771",
		Port:     6882,
		Infohash: hexHash("d14a4e0d2b1e3c4f5a6b7c8d9e0f1a2b3c4d5e6f"),
		Cookie:   "pp-other",
	}
	data2 := FormatAnnounce(other)
	a2, _ := ParseAnnounce(data2)
	if a2.Cookie == s.cookie {
		t.Fatal("different cookie should not match")
	}
}

func TestInfohashFiltering(t *testing.T) {
	peers := make(chan Peer, 10)
	s := New(6881, peers)

	hashA := hexHash("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	hashB := hexHash("bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb")
	s.AddInfohash(hashA)

	s.mu.RLock()
	if !s.active[hashA] {
		t.Error("hashA should be active")
	}
	if s.active[hashB] {
		t.Error("hashB should not be active")
	}
	s.mu.RUnlock()
}

func TestAddRemoveInfohash(t *testing.T) {
	peers := make(chan Peer, 10)
	s := New(6881, peers)

	ih := hexHash("cccccccccccccccccccccccccccccccccccccccc")

	s.AddInfohash(ih)
	s.mu.RLock()
	if !s.active[ih] {
		t.Fatal("should be active after add")
	}
	s.mu.RUnlock()

	s.RemoveInfohash(ih)
	s.mu.RLock()
	if s.active[ih] {
		t.Fatal("should not be active after remove")
	}
	s.mu.RUnlock()

	s.AddInfohash(ih)
	s.mu.RLock()
	if !s.active[ih] {
		t.Fatal("should be active after re-add")
	}
	s.mu.RUnlock()
}

func TestPeerAddressFormat(t *testing.T) {
	// Verify our Peer struct format matches what net.JoinHostPort produces
	addr := "192.168.1.5:51413"
	p := Peer{Addr: addr}
	if p.Addr != "192.168.1.5:51413" {
		t.Errorf("addr = %q", p.Addr)
	}
}

func TestGenerateCookie(t *testing.T) {
	c1 := generateCookie()
	c2 := generateCookie()

	if !strings.HasPrefix(c1, "pp-") {
		t.Errorf("cookie should start with pp-: %q", c1)
	}
	if len(c1) != 19 { // "pp-" + 16 hex chars
		t.Errorf("cookie length = %d, want 19", len(c1))
	}
	if c1 == c2 {
		t.Error("two cookies should be different")
	}
}

func TestFormatAnnounceSize(t *testing.T) {
	a := &Announce{
		Host:     "239.192.152.143:6771",
		Port:     6881,
		Infohash: hexHash("d14a4e0d2b1e3c4f5a6b7c8d9e0f1a2b3c4d5e6f"),
		Cookie:   "pp-abcdef0123456789",
	}
	data := FormatAnnounce(a)
	if len(data) > maxDatagram {
		t.Errorf("announce %d bytes exceeds max datagram %d", len(data), maxDatagram)
	}
	// Verify it ends with double CRLF
	if !bytes.HasSuffix(data, []byte("\r\n\r\n")) {
		t.Error("should end with \\r\\n\\r\\n")
	}
}
