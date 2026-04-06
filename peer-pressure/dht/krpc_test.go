package dht

import (
	"net"
	"sync"
	"testing"
	"time"

	"github.com/ihvo/peer-pressure/bencode"
)

func TestEncodeDecodePing(t *testing.T) {
	var id NodeID
	copy(id[:], "abcdefghij0123456789")

	msg := Message{
		TxnID:  "aa",
		Type:   "q",
		Method: "ping",
		Args:   bencode.Dict{"id": bencode.String(id[:])},
	}

	data := EncodeMessage(msg)
	got, err := DecodeMessage(data)
	if err != nil {
		t.Fatalf("DecodeMessage: %v", err)
	}
	if got.TxnID != "aa" {
		t.Errorf("TxnID: got %q, want %q", got.TxnID, "aa")
	}
	if got.Type != "q" {
		t.Errorf("Type: got %q, want %q", got.Type, "q")
	}
	if got.Method != "ping" {
		t.Errorf("Method: got %q, want %q", got.Method, "ping")
	}
	gotID, ok := got.Args["id"].(bencode.String)
	if !ok {
		t.Fatalf("Args[id] not a string")
	}
	if string(gotID) != string(id[:]) {
		t.Errorf("Args[id]: got %x, want %x", gotID, id)
	}
}

func TestDecodeResponse(t *testing.T) {
	resp := Message{
		TxnID: "bb",
		Type:  "r",
		Reply: bencode.Dict{"id": bencode.String("12345678901234567890")},
	}
	data := EncodeMessage(resp)

	got, err := DecodeMessage(data)
	if err != nil {
		t.Fatalf("DecodeMessage: %v", err)
	}
	if got.Type != "r" {
		t.Errorf("Type: got %q, want %q", got.Type, "r")
	}
	if got.Reply == nil {
		t.Fatal("Reply is nil")
	}
	gotID := string(got.Reply["id"].(bencode.String))
	if gotID != "12345678901234567890" {
		t.Errorf("Reply[id]: got %q", gotID)
	}
}

func TestDecodeError(t *testing.T) {
	msg := Message{
		TxnID: "cc",
		Type:  "e",
		Error: []any{201, "A Generic Error Occurred"},
	}
	data := EncodeMessage(msg)

	got, err := DecodeMessage(data)
	if err != nil {
		t.Fatalf("DecodeMessage: %v", err)
	}
	if got.Type != "e" {
		t.Errorf("Type: got %q, want %q", got.Type, "e")
	}
	if len(got.Error) != 2 {
		t.Fatalf("Error length: got %d, want 2", len(got.Error))
	}
	if got.Error[0] != 201 {
		t.Errorf("Error[0]: got %v, want 201", got.Error[0])
	}
	if got.Error[1] != "A Generic Error Occurred" {
		t.Errorf("Error[1]: got %v", got.Error[1])
	}
}

func TestTransportRoundTrip(t *testing.T) {
	// Set up two UDP endpoints on loopback.
	clientConn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatalf("listen client: %v", err)
	}
	serverConn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatalf("listen server: %v", err)
	}

	client := NewTransport(clientConn)
	server := NewTransport(serverConn)
	defer client.Close()
	defer server.Close()

	serverID := NodeID{0x01}

	// Server responds to pings.
	go server.Listen(func(msg Message, addr *net.UDPAddr) {
		if msg.Method == "ping" {
			resp := Message{
				TxnID: msg.TxnID,
				Type:  "r",
				Reply: bencode.Dict{"id": bencode.String(serverID[:])},
			}
			serverConn.WriteToUDP(EncodeMessage(resp), addr)
		}
	})

	go client.Listen(nil)

	// Send ping from client to server.
	serverAddr := serverConn.LocalAddr().(*net.UDPAddr)
	clientID := NodeID{0x02}
	resp, err := client.Send(serverAddr, Message{
		Type:   "q",
		Method: "ping",
		Args:   bencode.Dict{"id": bencode.String(clientID[:])},
	}, 2*time.Second)
	if err != nil {
		t.Fatalf("Send: %v", err)
	}

	if resp.Type != "r" {
		t.Errorf("response type: got %q, want %q", resp.Type, "r")
	}
	gotID := string(resp.Reply["id"].(bencode.String))
	if gotID != string(serverID[:]) {
		t.Errorf("response id mismatch")
	}
}

func TestTransportTimeout(t *testing.T) {
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	tr := NewTransport(conn)
	defer tr.Close()
	go tr.Listen(nil)

	// Send to a port that nobody is listening on.
	_, err = tr.Send(&net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 1}, Message{
		Type:   "q",
		Method: "ping",
		Args:   bencode.Dict{"id": bencode.String(make([]byte, 20))},
	}, 200*time.Millisecond)
	if err == nil {
		t.Fatal("expected timeout error")
	}
}

func TestTransportConcurrent(t *testing.T) {
	clientConn, _ := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	serverConn, _ := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})

	client := NewTransport(clientConn)
	server := NewTransport(serverConn)
	defer client.Close()
	defer server.Close()

	// Server echoes back a response with a delay to test concurrent matching.
	go server.Listen(func(msg Message, addr *net.UDPAddr) {
		resp := Message{
			TxnID: msg.TxnID,
			Type:  "r",
			Reply: bencode.Dict{
				"id":    bencode.String(make([]byte, 20)),
				"echo":  msg.Args["val"],
			},
		}
		time.Sleep(10 * time.Millisecond)
		serverConn.WriteToUDP(EncodeMessage(resp), addr)
	})

	go client.Listen(nil)

	serverAddr := serverConn.LocalAddr().(*net.UDPAddr)
	const n = 5
	var wg sync.WaitGroup
	results := make([]string, n)
	errors := make([]error, n)

	for i := range n {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			val := string(rune('A' + idx))
			resp, err := client.Send(serverAddr, Message{
				Type:   "q",
				Method: "echo",
				Args: bencode.Dict{
					"id":  bencode.String(make([]byte, 20)),
					"val": bencode.String(val),
				},
			}, 2*time.Second)
			errors[idx] = err
			if err == nil {
				results[idx] = string(resp.Reply["echo"].(bencode.String))
			}
		}(i)
	}

	wg.Wait()

	for i := range n {
		if errors[i] != nil {
			t.Errorf("query %d: %v", i, errors[i])
			continue
		}
		want := string(rune('A' + i))
		if results[i] != want {
			t.Errorf("query %d: got %q, want %q", i, results[i], want)
		}
	}
}

func TestRandomNodeID(t *testing.T) {
	a := RandomNodeID()
	b := RandomNodeID()
	if a == b {
		t.Error("two random IDs should not be equal")
	}
	if a == (NodeID{}) {
		t.Error("random ID should not be all zeros")
	}
}

// --- BEP 43: Read-only DHT tests ---

func TestEncodeQueryWithRO(t *testing.T) {
	msg := Message{
		TxnID:    "aa",
		Type:     "q",
		Method:   "ping",
		Args:     bencode.Dict{"id": bencode.String("12345678901234567890")},
		ReadOnly: true,
	}
	data := EncodeMessage(msg)
	decoded, err := DecodeMessage(data)
	if err != nil {
		t.Fatal(err)
	}
	if !decoded.ReadOnly {
		t.Error("expected ReadOnly=true after round-trip")
	}
}

func TestEncodeQueryWithoutRO(t *testing.T) {
	msg := Message{
		TxnID:  "aa",
		Type:   "q",
		Method: "ping",
		Args:   bencode.Dict{"id": bencode.String("12345678901234567890")},
	}
	data := EncodeMessage(msg)
	// Verify no "ro" key in the bencoded output
	val, _ := bencode.Decode(data)
	d := val.(bencode.Dict)
	if _, ok := d["ro"]; ok {
		t.Error("should not contain 'ro' key when ReadOnly=false")
	}
}

func TestEncodeResponseIgnoresRO(t *testing.T) {
	msg := Message{
		TxnID:    "bb",
		Type:     "r",
		Reply:    bencode.Dict{"id": bencode.String("12345678901234567890")},
		ReadOnly: true,
	}
	data := EncodeMessage(msg)
	val, _ := bencode.Decode(data)
	d := val.(bencode.Dict)
	if _, ok := d["ro"]; ok {
		t.Error("response should not contain 'ro' key")
	}
}

func TestDecodeQueryWithRO(t *testing.T) {
	d := bencode.Dict{
		"t":  bencode.String("aa"),
		"y":  bencode.String("q"),
		"q":  bencode.String("ping"),
		"a":  bencode.Dict{"id": bencode.String("12345678901234567890")},
		"ro": bencode.Int(1),
	}
	data := bencode.Encode(d)
	msg, err := DecodeMessage(data)
	if err != nil {
		t.Fatal(err)
	}
	if !msg.ReadOnly {
		t.Error("expected ReadOnly=true")
	}
}

func TestDecodeQueryWithROZero(t *testing.T) {
	d := bencode.Dict{
		"t":  bencode.String("aa"),
		"y":  bencode.String("q"),
		"q":  bencode.String("ping"),
		"a":  bencode.Dict{"id": bencode.String("12345678901234567890")},
		"ro": bencode.Int(0),
	}
	data := bencode.Encode(d)
	msg, err := DecodeMessage(data)
	if err != nil {
		t.Fatal(err)
	}
	if msg.ReadOnly {
		t.Error("expected ReadOnly=false for ro=0")
	}
}

func TestDecodeQueryWithoutRO(t *testing.T) {
	d := bencode.Dict{
		"t": bencode.String("aa"),
		"y": bencode.String("q"),
		"q": bencode.String("ping"),
		"a": bencode.Dict{"id": bencode.String("12345678901234567890")},
	}
	data := bencode.Encode(d)
	msg, err := DecodeMessage(data)
	if err != nil {
		t.Fatal(err)
	}
	if msg.ReadOnly {
		t.Error("expected ReadOnly=false when absent")
	}
}

func TestNewQueryReadOnly(t *testing.T) {
	conn, _ := net.ListenPacket("udp4", "127.0.0.1:0")
	defer conn.Close()
	d := New(conn.(*net.UDPConn))
	d.ReadOnly = true

	msg := d.newQuery("ping", bencode.Dict{"id": bencode.String(d.ID[:])})
	if msg.Type != "q" {
		t.Errorf("Type = %q, want 'q'", msg.Type)
	}
	if !msg.ReadOnly {
		t.Error("expected ReadOnly=true")
	}
}

func TestNewQueryRegular(t *testing.T) {
	conn, _ := net.ListenPacket("udp4", "127.0.0.1:0")
	defer conn.Close()
	d := New(conn.(*net.UDPConn))

	msg := d.newQuery("find_node", bencode.Dict{"id": bencode.String(d.ID[:])})
	if msg.ReadOnly {
		t.Error("expected ReadOnly=false for regular node")
	}
}
