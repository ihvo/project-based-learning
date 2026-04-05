package peer

import (
	"bytes"
	"encoding/binary"
	"io"
	"net"
	"testing"
)

// --- Handshake tests ---

func TestHandshakeRoundTrip(t *testing.T) {
	original := &Handshake{
		InfoHash: [20]byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19, 20},
		PeerID:   [20]byte{'-', 'P', 'P', '0', '0', '0', '1', '-', 'a', 'b', 'c', 'd', 'e', 'f', 'g', 'h', 'i', 'j', 'k', 'l'},
	}

	var buf bytes.Buffer
	if err := WriteHandshake(&buf, original); err != nil {
		t.Fatalf("write error: %v", err)
	}

	if buf.Len() != handshakeLen {
		t.Fatalf("handshake size = %d, want %d", buf.Len(), handshakeLen)
	}

	got, err := ReadHandshake(&buf)
	if err != nil {
		t.Fatalf("read error: %v", err)
	}

	if got.InfoHash != original.InfoHash {
		t.Errorf("InfoHash = %x, want %x", got.InfoHash, original.InfoHash)
	}
	if got.PeerID != original.PeerID {
		t.Errorf("PeerID = %x, want %x", got.PeerID, original.PeerID)
	}
}

func TestHandshakeProtocolString(t *testing.T) {
	h := &Handshake{}
	var buf bytes.Buffer
	WriteHandshake(&buf, h)

	raw := buf.Bytes()
	if raw[0] != 19 {
		t.Errorf("protocol string length = %d, want 19", raw[0])
	}
	if string(raw[1:20]) != "BitTorrent protocol" {
		t.Errorf("protocol string = %q", string(raw[1:20]))
	}
}

func TestReadHandshakeBadProtocol(t *testing.T) {
	var buf [68]byte
	buf[0] = 19
	copy(buf[1:20], "NotBitTorrent proto")
	_, err := ReadHandshake(bytes.NewReader(buf[:]))
	if err == nil {
		t.Error("expected error for bad protocol string")
	}
}

func TestReadHandshakeTruncated(t *testing.T) {
	_, err := ReadHandshake(bytes.NewReader(make([]byte, 30)))
	if err == nil {
		t.Error("expected error for truncated handshake")
	}
}

// --- Message round-trip tests ---

func TestMessageRoundTrips(t *testing.T) {
	tests := []struct {
		name string
		msg  *Message
	}{
		{"choke", NewChoke()},
		{"unchoke", NewUnchoke()},
		{"interested", NewInterested()},
		{"not interested", NewNotInterested()},
		{"have", NewHave(42)},
		{"bitfield", NewBitfield([]byte{0xFF, 0x00, 0xAB})},
		{"request", NewRequest(1, 0, 16384)},
		{"piece", NewPiece(1, 0, []byte("hello block data"))},
		{"cancel", NewCancel(1, 0, 16384)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			if err := WriteMessage(&buf, tt.msg); err != nil {
				t.Fatalf("write error: %v", err)
			}

			got, err := ReadMessage(&buf)
			if err != nil {
				t.Fatalf("read error: %v", err)
			}

			if got.ID != tt.msg.ID {
				t.Errorf("ID = %d, want %d", got.ID, tt.msg.ID)
			}
			if !bytes.Equal(got.Payload, tt.msg.Payload) {
				t.Errorf("Payload mismatch")
			}
		})
	}
}

func TestKeepAliveRoundTrip(t *testing.T) {
	var buf bytes.Buffer
	if err := WriteMessage(&buf, MsgKeepAlive()); err != nil {
		t.Fatalf("write error: %v", err)
	}

	// Should be exactly 4 zero bytes
	if buf.Len() != 4 {
		t.Fatalf("keep-alive size = %d, want 4", buf.Len())
	}

	got, err := ReadMessage(&buf)
	if err != nil {
		t.Fatalf("read error: %v", err)
	}
	if got != nil {
		t.Errorf("keep-alive should return nil, got ID=%d", got.ID)
	}
}

func TestReadMessageTooLarge(t *testing.T) {
	var buf bytes.Buffer
	binary.Write(&buf, binary.BigEndian, uint32(MaxMessageLen+1))
	_, err := ReadMessage(&buf)
	if err == nil {
		t.Error("expected error for oversized message")
	}
}

func TestMultipleMessages(t *testing.T) {
	// Write several messages in sequence, read them back
	var buf bytes.Buffer
	messages := []*Message{
		NewInterested(),
		nil, // keep-alive
		NewHave(7),
		NewRequest(0, 0, 16384),
	}

	for _, m := range messages {
		if err := WriteMessage(&buf, m); err != nil {
			t.Fatalf("write error: %v", err)
		}
	}

	for i, want := range messages {
		got, err := ReadMessage(&buf)
		if err != nil {
			t.Fatalf("message[%d] read error: %v", i, err)
		}

		if want == nil {
			if got != nil {
				t.Errorf("message[%d]: expected keep-alive, got ID=%d", i, got.ID)
			}
			continue
		}
		if got == nil {
			t.Errorf("message[%d]: expected ID=%d, got keep-alive", i, want.ID)
			continue
		}
		if got.ID != want.ID {
			t.Errorf("message[%d]: ID = %d, want %d", i, got.ID, want.ID)
		}
	}
}

// --- Payload parser tests ---

func TestParseHave(t *testing.T) {
	idx, err := ParseHave(NewHave(42).Payload)
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	if idx != 42 {
		t.Errorf("got %d, want 42", idx)
	}
}

func TestParseHaveBadLength(t *testing.T) {
	_, err := ParseHave([]byte{1, 2})
	if err == nil {
		t.Error("expected error for bad payload length")
	}
}

func TestParseRequest(t *testing.T) {
	rp, err := ParseRequest(NewRequest(5, 16384, 16384).Payload)
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	if rp.Index != 5 || rp.Begin != 16384 || rp.Length != 16384 {
		t.Errorf("got %+v", rp)
	}
}

func TestParsePiece(t *testing.T) {
	block := []byte("test data 1234567890")
	pp, err := ParsePiece(NewPiece(3, 0, block).Payload)
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	if pp.Index != 3 || pp.Begin != 0 {
		t.Errorf("got index=%d begin=%d", pp.Index, pp.Begin)
	}
	if !bytes.Equal(pp.Block, block) {
		t.Errorf("block mismatch")
	}
}

func TestParsePieceTooShort(t *testing.T) {
	_, err := ParsePiece([]byte{1, 2, 3})
	if err == nil {
		t.Error("expected error for short piece payload")
	}
}

// --- Conn integration test using net.Pipe ---

func TestConnHandshake(t *testing.T) {
	// net.Pipe creates a synchronous in-memory connection pair.
	// This is Go's way to test networking without actual TCP sockets.
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()

	infoHash := [20]byte{0xAA, 0xBB, 0xCC}
	clientID := [20]byte{'-', 'P', 'P', '0', '0', '0', '1', '-'}
	serverID := [20]byte{'-', 'P', 'P', '0', '0', '0', '2', '-'}

	errCh := make(chan error, 2)

	// Server side
	var serverConn *Conn
	go func() {
		var err error
		serverConn, err = FromConn(server, infoHash, serverID)
		errCh <- err
	}()

	// Client side
	var clientConn *Conn
	go func() {
		var err error
		clientConn, err = FromConn(client, infoHash, clientID)
		errCh <- err
	}()

	// Wait for both handshakes
	for range 2 {
		if err := <-errCh; err != nil {
			t.Fatalf("handshake error: %v", err)
		}
	}

	// Verify each side sees the other's peer ID
	if clientConn.PeerID != serverID {
		t.Errorf("client sees peer ID %x, want %x", clientConn.PeerID, serverID)
	}
	if serverConn.PeerID != clientID {
		t.Errorf("server sees peer ID %x, want %x", serverConn.PeerID, clientID)
	}
}

func TestConnMessageExchange(t *testing.T) {
	clientRaw, serverRaw := net.Pipe()
	defer clientRaw.Close()
	defer serverRaw.Close()

	infoHash := [20]byte{0x01}
	clientID := [20]byte{0x10}
	serverID := [20]byte{0x20}

	errCh := make(chan error, 2)
	var clientConn, serverConn *Conn

	go func() {
		var err error
		serverConn, err = FromConn(serverRaw, infoHash, serverID)
		errCh <- err
	}()
	go func() {
		var err error
		clientConn, err = FromConn(clientRaw, infoHash, clientID)
		errCh <- err
	}()

	for range 2 {
		if err := <-errCh; err != nil {
			t.Fatalf("handshake error: %v", err)
		}
	}

	// Client sends interested, server reads it
	go func() {
		errCh <- clientConn.WriteMessage(NewInterested())
	}()

	msg, err := serverConn.ReadMessage()
	if err != nil {
		t.Fatalf("read error: %v", err)
	}
	<-errCh // drain write result
	if msg.ID != MsgInterested {
		t.Errorf("got message ID %d, want %d (interested)", msg.ID, MsgInterested)
	}

	// Server sends unchoke, client reads it
	go func() {
		errCh <- serverConn.WriteMessage(NewUnchoke())
	}()

	msg, err = clientConn.ReadMessage()
	if err != nil {
		t.Fatalf("read error: %v", err)
	}
	<-errCh
	if msg.ID != MsgUnchoke {
		t.Errorf("got message ID %d, want %d (unchoke)", msg.ID, MsgUnchoke)
	}
}

func TestConnInfoHashMismatch(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()

	errCh := make(chan error, 2)

	go func() {
		_, err := FromConn(server, [20]byte{0xAA}, [20]byte{0x01})
		errCh <- err
	}()
	go func() {
		_, err := FromConn(client, [20]byte{0xBB}, [20]byte{0x02})
		errCh <- err
	}()

	// At least one side should get a mismatch error
	gotError := false
	for range 2 {
		if err := <-errCh; err != nil {
			gotError = true
		}
	}
	if !gotError {
		t.Error("expected info hash mismatch error")
	}
}

// --- Wire format verification ---

func TestMessageWireFormat(t *testing.T) {
	// Verify the exact wire bytes for a Request message
	msg := NewRequest(1, 16384, 16384)
	var buf bytes.Buffer
	WriteMessage(&buf, msg)

	raw := buf.Bytes()
	// 4 bytes length (13 = 1 ID + 12 payload) + 1 ID + 12 payload = 17 bytes total
	if len(raw) != 17 {
		t.Fatalf("wire size = %d, want 17", len(raw))
	}

	length := binary.BigEndian.Uint32(raw[0:4])
	if length != 13 {
		t.Errorf("length prefix = %d, want 13", length)
	}
	if raw[4] != MsgRequest {
		t.Errorf("message ID = %d, want %d", raw[4], MsgRequest)
	}

	index := binary.BigEndian.Uint32(raw[5:9])
	begin := binary.BigEndian.Uint32(raw[9:13])
	blockLen := binary.BigEndian.Uint32(raw[13:17])
	if index != 1 || begin != 16384 || blockLen != 16384 {
		t.Errorf("payload: index=%d begin=%d length=%d", index, begin, blockLen)
	}
}

func TestReadMessageEOF(t *testing.T) {
	_, err := ReadMessage(bytes.NewReader([]byte{}))
	if err == nil || err == io.EOF {
		// We expect a wrapped EOF, not bare nil
		if err == nil {
			t.Error("expected error on empty reader")
		}
	}
}
