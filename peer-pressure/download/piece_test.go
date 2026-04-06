package download

import (
	"crypto/sha1"
	"net"
	"testing"

	"github.com/ihvo/peer-pressure/peer"
)

// mockPeer runs a fake peer on one side of a net.Pipe that:
// 1. Completes the handshake
// 2. Sends a bitfield
// 3. Waits for interested, sends unchoke
// 4. Responds to request messages with the correct block data
//
// All writes are serialized through a single goroutine to avoid
// interleaving on unbuffered transports.
func mockPeer(t *testing.T, conn net.Conn, infoHash [20]byte, pieceData []byte, pieceIndex int) {
	t.Helper()

	serverID := [20]byte{'-', 'M', 'O', 'C', 'K', '-'}
	pc, err := peer.FromConn(conn, infoHash, serverID)
	if err != nil {
		t.Errorf("mock peer handshake: %v", err)
		return
	}
	defer pc.Close()

	// Serialize all writes through a channel to avoid interleaving
	writeCh := make(chan *peer.Message, 16)
	writesDone := make(chan struct{})
	go func() {
		defer close(writesDone)
		for msg := range writeCh {
			pc.WriteMessage(msg)
			pc.Flush()
		}
	}()
	defer func() {
		close(writeCh)
		<-writesDone
	}()

	// Send bitfield — goes through the writer goroutine, so reads can proceed
	writeCh <- peer.NewBitfield([]byte{0xFF})

	// Wait for interested
	for {
		msg, err := pc.ReadMessage()
		if err != nil {
			t.Errorf("mock peer read: %v", err)
			return
		}
		if msg == nil {
			continue
		}
		if msg.ID == peer.MsgInterested {
			break
		}
	}

	// Send unchoke
	writeCh <- peer.NewUnchoke()

	// Respond to requests
	for {
		msg, err := pc.ReadMessage()
		if err != nil {
			return // connection closed, test done
		}
		if msg == nil {
			continue
		}

		if msg.ID == peer.MsgRequest {
			rp, err := peer.ParseRequest(msg.Payload)
			if err != nil {
				t.Errorf("mock peer parse request: %v", err)
				return
			}

			if int(rp.Index) != pieceIndex {
				t.Errorf("mock peer: wrong piece index %d", rp.Index)
				return
			}

			begin := int(rp.Begin)
			end := begin + int(rp.Length)
			if end > len(pieceData) {
				end = len(pieceData)
			}

			block := make([]byte, end-begin)
			copy(block, pieceData[begin:end])
			writeCh <- peer.NewPiece(rp.Index, rp.Begin, block)
		}
	}
}

func TestPieceDownloadSingleBlock(t *testing.T) {
	// A piece smaller than one block (simplest case)
	pieceData := []byte("hello, bittorrent world!!")
	pieceIndex := 0
	expectedHash := sha1.Sum(pieceData)
	infoHash := [20]byte{0x01}
	clientID := [20]byte{'-', 'P', 'P', '-'}

	client, server := net.Pipe()
	defer client.Close()

	go mockPeer(t, server, infoHash, pieceData, pieceIndex)

	pc, err := peer.FromConn(client, infoHash, clientID)
	if err != nil {
		t.Fatalf("client handshake: %v", err)
	}
	defer pc.Close()

	got, err := Piece(pc, pieceIndex, len(pieceData), expectedHash)
	if err != nil {
		t.Fatalf("Piece() error: %v", err)
	}

	if string(got) != string(pieceData) {
		t.Errorf("data mismatch: got %q", got)
	}
}

func TestPieceDownloadMultiBlock(t *testing.T) {
	// A piece that spans multiple blocks (3 blocks: 16384 + 16384 + remainder)
	pieceLength := BlockSize*2 + 5000 // 37768 bytes
	pieceData := make([]byte, pieceLength)
	for i := range pieceData {
		pieceData[i] = byte(i % 251) // fill with deterministic pattern
	}

	pieceIndex := 3
	expectedHash := sha1.Sum(pieceData)
	infoHash := [20]byte{0x02}
	clientID := [20]byte{'-', 'P', 'P', '-'}

	client, server := net.Pipe()
	defer client.Close()

	go mockPeer(t, server, infoHash, pieceData, pieceIndex)

	pc, err := peer.FromConn(client, infoHash, clientID)
	if err != nil {
		t.Fatalf("client handshake: %v", err)
	}
	defer pc.Close()

	got, err := Piece(pc, pieceIndex, pieceLength, expectedHash)
	if err != nil {
		t.Fatalf("Piece() error: %v", err)
	}

	for i := range pieceData {
		if got[i] != pieceData[i] {
			t.Fatalf("data mismatch at byte %d: got %d, want %d", i, got[i], pieceData[i])
		}
	}
}

func TestPieceDownloadExactBlock(t *testing.T) {
	// Piece is exactly one block size
	pieceData := make([]byte, BlockSize)
	for i := range pieceData {
		pieceData[i] = byte(i % 256)
	}

	pieceIndex := 0
	expectedHash := sha1.Sum(pieceData)
	infoHash := [20]byte{0x03}
	clientID := [20]byte{'-', 'P', 'P', '-'}

	client, server := net.Pipe()
	defer client.Close()

	go mockPeer(t, server, infoHash, pieceData, pieceIndex)

	pc, err := peer.FromConn(client, infoHash, clientID)
	if err != nil {
		t.Fatalf("client handshake: %v", err)
	}
	defer pc.Close()

	got, err := Piece(pc, pieceIndex, BlockSize, expectedHash)
	if err != nil {
		t.Fatalf("Piece() error: %v", err)
	}

	if len(got) != BlockSize {
		t.Errorf("got %d bytes, want %d", len(got), BlockSize)
	}
}

func TestPieceDownloadBadHash(t *testing.T) {
	pieceData := []byte("good data")
	pieceIndex := 0
	badHash := [20]byte{0xFF} // wrong hash
	infoHash := [20]byte{0x04}
	clientID := [20]byte{'-', 'P', 'P', '-'}

	client, server := net.Pipe()
	defer client.Close()

	go mockPeer(t, server, infoHash, pieceData, pieceIndex)

	pc, err := peer.FromConn(client, infoHash, clientID)
	if err != nil {
		t.Fatalf("client handshake: %v", err)
	}
	defer pc.Close()

	_, err = Piece(pc, pieceIndex, len(pieceData), badHash)
	if err == nil {
		t.Error("expected hash mismatch error")
	}
}

func TestBlockCount(t *testing.T) {
	tests := []struct {
		pieceLen int
		want     int
	}{
		{BlockSize, 1},
		{BlockSize + 1, 2},
		{BlockSize * 3, 3},
		{BlockSize*2 + 5000, 3},
		{100, 1},
		{0, 0},
	}

	for _, tt := range tests {
		got := BlockCount(tt.pieceLen)
		if got != tt.want {
			t.Errorf("BlockCount(%d) = %d, want %d", tt.pieceLen, got, tt.want)
		}
	}
}
