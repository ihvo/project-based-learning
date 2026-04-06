package seed

import (
	"context"
	"crypto/sha1"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ihvo/peer-pressure/bencode"
	"github.com/ihvo/peer-pressure/peer"
	"github.com/ihvo/peer-pressure/torrent"
)

// --- Helpers ---

func buildTestTorrent(t *testing.T, data []byte, pieceLen int) *torrent.Torrent {
	t.Helper()
	numPieces := (len(data) + pieceLen - 1) / pieceLen
	var pieces [][20]byte
	for i := range numPieces {
		start := i * pieceLen
		end := start + pieceLen
		if end > len(data) {
			end = len(data)
		}
		pieces = append(pieces, sha1.Sum(data[start:end]))
	}

	// Build bencoded info dict to compute infohash.
	piecesConcat := make([]byte, 0, 20*len(pieces))
	for _, h := range pieces {
		piecesConcat = append(piecesConcat, h[:]...)
	}
	info := bencode.Dict{
		"length":       bencode.Int(len(data)),
		"name":         bencode.String("test.bin"),
		"piece length": bencode.Int(pieceLen),
		"pieces":       bencode.String(piecesConcat),
	}
	raw := bencode.Encode(info)

	return &torrent.Torrent{
		Name:        "test.bin",
		Length:      len(data),
		PieceLength: pieceLen,
		Pieces:      pieces,
		InfoHash:    sha1.Sum(raw),
	}
}

func writeTestFile(t *testing.T, data []byte) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "test.bin")
	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatal(err)
	}
	return path
}

// --- Verify tests ---

func TestVerifyValidSingleFile(t *testing.T) {
	data := make([]byte, 1024)
	for i := range data {
		data[i] = byte(i % 256)
	}
	tor := buildTestTorrent(t, data, 256)
	path := writeTestFile(t, data)

	result, err := VerifyData(tor, path)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if result.ValidPieces != result.TotalPieces {
		t.Errorf("expected %d valid, got %d (invalid: %v)",
			result.TotalPieces, result.ValidPieces, result.InvalidPieces)
	}
}

func TestVerifyCorruptPiece(t *testing.T) {
	data := make([]byte, 1024)
	for i := range data {
		data[i] = byte(i % 256)
	}
	tor := buildTestTorrent(t, data, 256)

	// Corrupt piece 2
	data[512] ^= 0xFF
	path := writeTestFile(t, data)

	result, err := VerifyData(tor, path)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if result.ValidPieces != 3 {
		t.Errorf("expected 3 valid, got %d", result.ValidPieces)
	}
	if len(result.InvalidPieces) != 1 || result.InvalidPieces[0] != 2 {
		t.Errorf("expected invalid=[2], got %v", result.InvalidPieces)
	}
}

func TestVerifyTruncatedFile(t *testing.T) {
	data := make([]byte, 1024)
	for i := range data {
		data[i] = byte(i % 256)
	}
	tor := buildTestTorrent(t, data, 256)

	// Truncate to half
	path := writeTestFile(t, data[:512])

	result, err := VerifyData(tor, path)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if result.ValidPieces != 2 {
		t.Errorf("expected 2 valid, got %d", result.ValidPieces)
	}
}

// --- Full bitfield test ---

func TestMakeFullBitfield(t *testing.T) {
	tests := []struct {
		n    int
		want []byte
	}{
		{8, []byte{0xFF}},
		{9, []byte{0xFF, 0x80}},
		{1, []byte{0x80}},
		{16, []byte{0xFF, 0xFF}},
	}
	for _, tt := range tests {
		got := makeFullBitfield(tt.n)
		if len(got) != len(tt.want) {
			t.Errorf("n=%d: len=%d, want %d", tt.n, len(got), len(tt.want))
			continue
		}
		for i := range got {
			if got[i] != tt.want[i] {
				t.Errorf("n=%d: byte[%d]=%02x, want %02x", tt.n, i, got[i], tt.want[i])
			}
		}
	}
}

// --- Choker tests ---

func TestChokerTitForTat(t *testing.T) {
	choker := NewChoker(2) // 2 unchoke slots

	conns := make([]*uploadConn, 4)
	for i := range conns {
		conns[i] = &uploadConn{
			addr:       string(rune('A' + i)),
			interested: true,
			choked:     true,
		}
	}
	// Peer C uploaded most, then A, then D, then B.
	conns[0].uploadBytes.Store(500) // A
	conns[1].uploadBytes.Store(100) // B
	conns[2].uploadBytes.Store(900) // C
	conns[3].uploadBytes.Store(300) // D

	choker.evaluate(conns, false)

	// Top 2 by upload: C (900) and A (500) should be unchoked.
	if conns[2].choked {
		t.Error("C should be unchoked (highest upload)")
	}
	if conns[0].choked {
		t.Error("A should be unchoked (second highest)")
	}
	if !conns[1].choked {
		t.Error("B should remain choked (lowest upload)")
	}
}

func TestChokerNotInterestedIgnored(t *testing.T) {
	choker := NewChoker(4)

	conns := []*uploadConn{
		{addr: "a", interested: true, choked: true},
		{addr: "b", interested: false, choked: true},
	}
	conns[0].uploadBytes.Store(100)
	conns[1].uploadBytes.Store(999)

	choker.evaluate(conns, false)

	// Only interested peer should be considered.
	if conns[0].choked {
		t.Error("interested peer should be unchoked")
	}
	// Not-interested peer stays choked (not in unchoke set, and was already choked).
	if !conns[1].choked {
		t.Error("not-interested peer should remain choked")
	}
}

// --- Seeder accept test ---

func TestAcceptHandshake(t *testing.T) {
	data := make([]byte, 512)
	for i := range data {
		data[i] = byte(i)
	}
	tor := buildTestTorrent(t, data, 256)
	dataPath := writeTestFile(t, data)

	var peerID [20]byte
	copy(peerID[:], "-PP0100-testseeder00")

	seeder, err := New(Config{
		Torrent:    tor,
		DataPath:   dataPath,
		PeerID:     peerID,
		ListenAddr: "127.0.0.1:0",
	})
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Start seeder in background.
	errCh := make(chan error, 1)
	go func() {
		errCh <- seeder.Run(ctx)
	}()

	// Wait for listener to be ready.
	time.Sleep(100 * time.Millisecond)
	addr := seeder.listener.Addr().String()

	// Connect as a downloading peer.
	var clientID [20]byte
	copy(clientID[:], "-qB4620-testclient00")

	conn, err := peer.Dial(addr, tor.InfoHash, clientID)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	// Read messages — may get extension handshake before bitfield.
	var bfMsg *peer.Message
	for range 3 {
		msg, err := conn.ReadMessage()
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		if msg == nil {
			continue
		}
		if msg.ID == peer.MsgBitfield {
			bfMsg = msg
			break
		}
	}
	if bfMsg == nil {
		t.Fatal("never received Bitfield message")
	}

	// Verify all bits are set.
	for _, b := range bfMsg.Payload {
		if b == 0 {
			t.Error("expected all bits set in bitfield")
		}
	}

	cancel()
	<-errCh
}

func TestServeRequest(t *testing.T) {
	data := make([]byte, 512)
	for i := range data {
		data[i] = byte(i)
	}
	tor := buildTestTorrent(t, data, 256)
	dataPath := writeTestFile(t, data)

	var peerID [20]byte
	copy(peerID[:], "-PP0100-testseeder00")

	seeder, err := New(Config{
		Torrent:     tor,
		DataPath:    dataPath,
		PeerID:      peerID,
		ListenAddr:  "127.0.0.1:0",
		UploadSlots: 10,
	})
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go seeder.Run(ctx)
	time.Sleep(100 * time.Millisecond)
	addr := seeder.listener.Addr().String()

	var clientID [20]byte
	copy(clientID[:], "-qB4620-testclient00")

	conn, err := peer.Dial(addr, tor.InfoHash, clientID)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	// Drain messages until we get bitfield.
	for range 5 {
		msg, err := conn.ReadMessage()
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		if msg != nil && msg.ID == peer.MsgBitfield {
			break
		}
	}

	// Send Interested.
	conn.WriteMessage(&peer.Message{ID: peer.MsgInterested})
	conn.Flush()

	// Give the seeder time to register us, then force a choker evaluation.
	time.Sleep(200 * time.Millisecond)
	seeder.choker.evaluate(seeder.getConns(), false)

	// Wait briefly for unchoke message to be delivered.
	time.Sleep(100 * time.Millisecond)

	// Send a Request for piece 0, block at offset 0, length 256.
	reqPayload := make([]byte, 12)
	reqPayload[8] = 0x00 // length high bytes = 0
	reqPayload[9] = 0x00
	reqPayload[10] = 0x01 // 256 = 0x100
	reqPayload[11] = 0x00
	conn.WriteMessage(&peer.Message{ID: peer.MsgRequest, Payload: reqPayload})
	conn.Flush()

	// Read responses — skip Unchoke, find Piece.
	conn.SetDeadline(time.Now().Add(5 * time.Second))
	var pieceMsg *peer.Message
	for range 5 {
		msg, err := conn.ReadMessage()
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		if msg != nil && msg.ID == peer.MsgPiece {
			pieceMsg = msg
			break
		}
	}
	if pieceMsg == nil {
		t.Fatal("never received Piece message")
	}

	// Verify the data matches.
	block := pieceMsg.Payload[8:]
	if len(block) != 256 {
		t.Fatalf("block len: got %d, want 256", len(block))
	}
	for i, b := range block {
		if b != data[i] {
			t.Errorf("byte %d: got %d, want %d", i, b, data[i])
			break
		}
	}

	cancel()
}

func TestHandshakeWrongInfohash(t *testing.T) {
	data := make([]byte, 512)
	tor := buildTestTorrent(t, data, 256)
	dataPath := writeTestFile(t, data)

	var peerID [20]byte
	copy(peerID[:], "-PP0100-testseeder00")

	seeder, err := New(Config{
		Torrent:    tor,
		DataPath:   dataPath,
		PeerID:     peerID,
		ListenAddr: "127.0.0.1:0",
	})
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go seeder.Run(ctx)
	time.Sleep(100 * time.Millisecond)
	addr := seeder.listener.Addr().String()

	// Try to connect with wrong infohash.
	var clientID [20]byte
	var wrongHash [20]byte
	copy(wrongHash[:], "this is the wrong ha")

	_, err = peer.Dial(addr, wrongHash, clientID)
	if err == nil {
		t.Error("expected error for wrong infohash")
	}

	cancel()
}
