package magnet

import (
	"crypto/sha1"
	"net"
	"testing"

	"github.com/ihvo/peer-pressure/bencode"
	"github.com/ihvo/peer-pressure/peer"
)

// mockMetadataPeer simulates a peer that serves metadata via ut_metadata.
func mockMetadataPeer(conn *peer.Conn, metadata []byte, ourMetadataID uint8) {
	numPieces := (len(metadata) + metadataPieceSize - 1) / metadataPieceSize

	for {
		msg, err := conn.ReadMessage()
		if err != nil {
			return
		}
		if msg == nil || msg.ID != peer.MsgExtended {
			continue
		}
		if len(msg.Payload) < 2 {
			continue
		}

		// Parse the request.
		payload := msg.Payload[1:]
		val, _, err := bencode.DecodeFirst(payload)
		if err != nil {
			continue
		}
		d, ok := val.(bencode.Dict)
		if !ok {
			continue
		}

		mtVal, _ := d["msg_type"]
		mt, _ := mtVal.(bencode.Int)
		pVal, _ := d["piece"]
		p, _ := pVal.(bencode.Int)

		if int(mt) != MetadataRequest {
			continue
		}

		piece := int(p)
		if piece < 0 || piece >= numPieces {
			continue
		}

		// Build response: bencoded dict + raw piece data.
		offset := piece * metadataPieceSize
		end := offset + metadataPieceSize
		if end > len(metadata) {
			end = len(metadata)
		}
		data := metadata[offset:end]

		respDict := bencode.Dict{
			"msg_type":   bencode.Int(MetadataData),
			"piece":      bencode.Int(piece),
			"total_size": bencode.Int(len(metadata)),
		}
		dictBytes := bencode.Encode(respDict)

		// Concatenate dict + raw data.
		full := make([]byte, len(dictBytes)+len(data))
		copy(full, dictBytes)
		copy(full[len(dictBytes):], data)

		respMsg := peer.NewExtMessage(ourMetadataID, full)
		conn.WriteMessage(respMsg)
		conn.Flush()
	}
}

func setupExtConn(t *testing.T, a, b net.Conn, infoHash [20]byte) (*peer.Conn, *peer.Conn) {
	t.Helper()
	peerIDA := [20]byte{0xAA}
	peerIDB := [20]byte{0xBB}

	connCh := make(chan *peer.Conn, 1)
	errCh := make(chan error, 1)

	go func() {
		c, err := peer.FromConn(a, infoHash, peerIDA)
		if err != nil {
			errCh <- err
			return
		}
		connCh <- c
	}()

	connB, err := peer.FromConn(b, infoHash, peerIDB)
	if err != nil {
		t.Fatalf("handshake B: %v", err)
	}

	var connA *peer.Conn
	select {
	case connA = <-connCh:
	case err := <-errCh:
		t.Fatalf("handshake A: %v", err)
	}

	return connA, connB
}

func TestFetchMetadataSmall(t *testing.T) {
	// Metadata smaller than one piece (< 16 KiB).
	metadata := []byte("d4:name11:hello.world12:piece lengthi16384e6:pieces20:01234567890123456789e")
	infoHash := sha1.Sum(metadata)

	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()

	connA, connB := setupExtConn(t, a, b, infoHash)

	// Exchange extension handshakes concurrently.
	// A wants ut_metadata at ID 1; B offers ut_metadata at ID 2.
	extDone := make(chan error, 2)
	go func() {
		extDone <- connA.ExchangeExtHandshake(
			map[string]int{"ut_metadata": 1}, 0, "Test")
	}()
	go func() {
		extDone <- connB.ExchangeExtHandshake(
			map[string]int{"ut_metadata": 2}, len(metadata), "Test")
	}()
	for range 2 {
		if err := <-extDone; err != nil {
			t.Fatalf("ext handshake: %v", err)
		}
	}

	// B serves metadata. A's ext handshake told B "I use ID 1 for ut_metadata",
	// so B sends responses with sub-ID = 1 (A's ID).
	go mockMetadataPeer(connB, metadata, 1)

	// A fetches metadata.
	got, err := FetchMetadata(connA, infoHash)
	if err != nil {
		t.Fatalf("FetchMetadata: %v", err)
	}
	if string(got) != string(metadata) {
		t.Errorf("metadata mismatch:\ngot  %q\nwant %q", got, metadata)
	}
}

func TestFetchMetadataMultiPiece(t *testing.T) {
	// Metadata larger than one piece — force 3 pieces.
	metadata := make([]byte, metadataPieceSize*2+100)
	for i := range metadata {
		metadata[i] = byte(i % 251) // deterministic non-zero fill
	}
	infoHash := sha1.Sum(metadata)

	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()

	connA, connB := setupExtConn(t, a, b, infoHash)

	extDone := make(chan error, 2)
	go func() {
		extDone <- connA.ExchangeExtHandshake(
			map[string]int{"ut_metadata": 1}, 0, "Test")
	}()
	go func() {
		extDone <- connB.ExchangeExtHandshake(
			map[string]int{"ut_metadata": 2}, len(metadata), "Test")
	}()
	for range 2 {
		if err := <-extDone; err != nil {
			t.Fatalf("ext handshake: %v", err)
		}
	}

	go mockMetadataPeer(connB, metadata, 1)

	got, err := FetchMetadata(connA, infoHash)
	if err != nil {
		t.Fatalf("FetchMetadata: %v", err)
	}
	if len(got) != len(metadata) {
		t.Fatalf("length mismatch: got %d, want %d", len(got), len(metadata))
	}
	for i := range metadata {
		if got[i] != metadata[i] {
			t.Fatalf("byte %d mismatch: got %d, want %d", i, got[i], metadata[i])
		}
	}
}

func TestFetchMetadataHashMismatch(t *testing.T) {
	metadata := []byte("d4:name5:teste")
	wrongHash := [20]byte{0xFF} // intentionally wrong

	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()

	connA, connB := setupExtConn(t, a, b, wrongHash)

	extDone := make(chan error, 2)
	go func() {
		extDone <- connA.ExchangeExtHandshake(
			map[string]int{"ut_metadata": 1}, 0, "Test")
	}()
	go func() {
		extDone <- connB.ExchangeExtHandshake(
			map[string]int{"ut_metadata": 2}, len(metadata), "Test")
	}()
	for range 2 {
		if err := <-extDone; err != nil {
			t.Fatalf("ext handshake: %v", err)
		}
	}

	go mockMetadataPeer(connB, metadata, 1)

	_, err := FetchMetadata(connA, wrongHash)
	if err == nil {
		t.Fatal("expected hash mismatch error")
	}
}

func TestFetchMetadataNoExtHandshake(t *testing.T) {
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()

	infoHash := [20]byte{1}
	connA, _ := setupExtConn(t, a, b, infoHash)

	// No extension handshake — PeerExtensions is nil.
	_, err := FetchMetadata(connA, infoHash)
	if err == nil {
		t.Fatal("expected error for missing ext handshake")
	}
}

func TestFetchMetadataNoUtMetadata(t *testing.T) {
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()

	infoHash := [20]byte{1}
	connA, connB := setupExtConn(t, a, b, infoHash)

	// Peer doesn't advertise ut_metadata.
	extDone := make(chan error, 2)
	go func() {
		extDone <- connA.ExchangeExtHandshake(
			map[string]int{"ut_metadata": 1}, 0, "Test")
	}()
	go func() {
		extDone <- connB.ExchangeExtHandshake(
			map[string]int{}, 0, "Test") // no ut_metadata
	}()
	for range 2 {
		if err := <-extDone; err != nil {
			t.Fatalf("ext handshake: %v", err)
		}
	}

	_, err := FetchMetadata(connA, infoHash)
	if err == nil {
		t.Fatal("expected error for missing ut_metadata support")
	}
}

func TestDecodeFirst(t *testing.T) {
	// Encode a dict followed by raw bytes.
	d := bencode.Dict{
		"msg_type": bencode.Int(1),
		"piece":    bencode.Int(0),
	}
	dictBytes := bencode.Encode(d)
	rawData := []byte("helloworld")
	full := append(dictBytes, rawData...)

	val, n, err := bencode.DecodeFirst(full)
	if err != nil {
		t.Fatalf("DecodeFirst: %v", err)
	}

	if n != len(dictBytes) {
		t.Errorf("consumed %d bytes, want %d", n, len(dictBytes))
	}

	remaining := full[n:]
	if string(remaining) != "helloworld" {
		t.Errorf("remaining: got %q, want %q", remaining, "helloworld")
	}

	dd, ok := val.(bencode.Dict)
	if !ok {
		t.Fatalf("expected Dict, got %T", val)
	}
	mt := dd["msg_type"].(bencode.Int)
	if int(mt) != 1 {
		t.Errorf("msg_type: got %d, want 1", mt)
	}
}
