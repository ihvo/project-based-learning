package download

import (
	"context"
	"crypto/rand"
	"crypto/sha1"
	"net"
	"os"
	"path/filepath"
	"testing"

	"github.com/ihvo/peer-pressure/peer"
	"github.com/ihvo/peer-pressure/torrent"
)

// mockServer listens on a random port and serves piece data.
// Each mock peer has a bitfield and serves only the pieces it has.
type mockServer struct {
	addr      string
	listener  net.Listener
	pieces    map[int][]byte // piece index → data
	bitfield  []byte
	numPieces int
	infoHash  [20]byte
	peerID    [20]byte
}

func startMockServer(t *testing.T, numPieces int, pieces map[int][]byte, infoHash [20]byte) *mockServer {
	t.Helper()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}

	// Build bitfield from available pieces
	var available []int
	for idx := range pieces {
		available = append(available, idx)
	}
	bf := MakeBitfield(numPieces, available)

	var pid [20]byte
	rand.Read(pid[:])

	ms := &mockServer{
		addr:      ln.Addr().String(),
		listener:  ln,
		pieces:    pieces,
		bitfield:  bf,
		numPieces: numPieces,
		infoHash:  infoHash,
		peerID:    pid,
	}

	go ms.serve(t)
	return ms
}

func (ms *mockServer) serve(t *testing.T) {
	for {
		raw, err := ms.listener.Accept()
		if err != nil {
			return // listener closed
		}
		go ms.handleConn(t, raw)
	}
}

func (ms *mockServer) handleConn(t *testing.T, raw net.Conn) {
	defer raw.Close()

	conn, err := peer.FromConn(raw, ms.infoHash, ms.peerID)
	if err != nil {
		t.Logf("mock handshake: %v", err)
		return
	}

	// Serialize all writes through one goroutine to prevent interleaving
	writeCh := make(chan *peer.Message, 16)
	writesDone := make(chan struct{})
	go func() {
		defer close(writesDone)
		for msg := range writeCh {
			conn.WriteMessage(msg)
			conn.Flush()
		}
	}()
	defer func() {
		close(writeCh)
		<-writesDone
	}()

	// Send bitfield through the writer goroutine
	writeCh <- peer.NewBitfield(ms.bitfield)

	// Wait for interested
	for {
		msg, err := conn.ReadMessage()
		if err != nil {
			return
		}
		if msg != nil && msg.ID == peer.MsgInterested {
			break
		}
	}

	// Send unchoke
	writeCh <- peer.NewUnchoke()

	// Serve requests
	for {
		msg, err := conn.ReadMessage()
		if err != nil {
			return
		}
		if msg == nil {
			continue
		}

		if msg.ID == peer.MsgRequest {
			rp, err := peer.ParseRequest(msg.Payload)
			if err != nil {
				return
			}

			data, ok := ms.pieces[int(rp.Index)]
			if !ok {
				continue // don't have this piece
			}

			begin := int(rp.Begin)
			end := begin + int(rp.Length)
			if end > len(data) {
				end = len(data)
			}

			block := make([]byte, end-begin)
			copy(block, data[begin:end])
			writeCh <- peer.NewPiece(rp.Index, rp.Begin, block)
		}
	}
}

func (ms *mockServer) close() {
	ms.listener.Close()
}

func TestFileDownload(t *testing.T) {
	// Create fake torrent data: 4 pieces, 32 KiB each (2 blocks per piece)
	const numPieces = 4
	const pieceLen = 2 * BlockSize // 32 KiB

	// Generate random piece data and compute hashes
	allData := make([]byte, numPieces*pieceLen)
	rand.Read(allData)

	var hashes [][20]byte
	pieces := make(map[int][]byte)
	for i := range numPieces {
		start := i * pieceLen
		end := start + pieceLen
		piece := allData[start:end]
		pieces[i] = piece
		hashes = append(hashes, sha1.Sum(piece))
	}

	infoHash := sha1.Sum([]byte("test-info"))

	// Torrent metadata
	tor := &torrent.Torrent{
		InfoHash:    infoHash,
		PieceLength: pieceLen,
		Pieces:      hashes,
		Length:      numPieces * pieceLen,
		Name:        "test.bin",
	}

	// Start 2 mock peers, each with different pieces
	// Peer A has pieces 0, 1
	// Peer B has pieces 2, 3
	peerA := startMockServer(t, numPieces, map[int][]byte{
		0: pieces[0],
		1: pieces[1],
	}, infoHash)
	defer peerA.close()

	peerB := startMockServer(t, numPieces, map[int][]byte{
		2: pieces[2],
		3: pieces[3],
	}, infoHash)
	defer peerB.close()

	// Output file
	outDir := t.TempDir()
	outPath := filepath.Join(outDir, "test.bin")

	var clientID [20]byte
	copy(clientID[:], "-PP0001-test-client!")

	err := File(context.Background(), Config{
		Torrent:    tor,
		Peers:      []string{peerA.addr, peerB.addr},
		OutputPath: outPath,
		PeerID:     clientID,
		MaxPeers:   2,
		Quiet:      true,
	})
	if err != nil {
		t.Fatalf("File() error: %v", err)
	}

	// Verify output
	got, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("read output: %v", err)
	}

	if len(got) != len(allData) {
		t.Fatalf("output size = %d, want %d", len(got), len(allData))
	}

	for i := range got {
		if got[i] != allData[i] {
			t.Fatalf("byte %d: got %02x, want %02x", i, got[i], allData[i])
		}
	}
}

func TestFileDownloadOverlappingPeers(t *testing.T) {
	// Both peers have all pieces — test that work is distributed without duplication
	const numPieces = 3
	const pieceLen = BlockSize // exactly 1 block per piece

	allData := make([]byte, numPieces*pieceLen)
	rand.Read(allData)

	var hashes [][20]byte
	allPieces := make(map[int][]byte)
	for i := range numPieces {
		start := i * pieceLen
		end := start + pieceLen
		piece := allData[start:end]
		allPieces[i] = piece
		hashes = append(hashes, sha1.Sum(piece))
	}

	infoHash := sha1.Sum([]byte("overlap-test"))

	tor := &torrent.Torrent{
		InfoHash:    infoHash,
		PieceLength: pieceLen,
		Pieces:      hashes,
		Length:      numPieces * pieceLen,
		Name:        "overlap.bin",
	}

	peer1 := startMockServer(t, numPieces, allPieces, infoHash)
	defer peer1.close()

	peer2 := startMockServer(t, numPieces, allPieces, infoHash)
	defer peer2.close()

	outDir := t.TempDir()
	outPath := filepath.Join(outDir, "overlap.bin")

	var clientID [20]byte
	copy(clientID[:], "-PP0001-overlap-test")

	err := File(context.Background(), Config{
		Torrent:    tor,
		Peers:      []string{peer1.addr, peer2.addr},
		OutputPath: outPath,
		PeerID:     clientID,
		MaxPeers:   2,
		Quiet:      true,
	})
	if err != nil {
		t.Fatalf("File() error: %v", err)
	}

	got, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("read output: %v", err)
	}

	if len(got) != len(allData) {
		t.Fatalf("output size = %d, want %d", len(got), len(allData))
	}

	for i := range got {
		if got[i] != allData[i] {
			t.Fatalf("byte %d: got %02x, want %02x", i, got[i], allData[i])
		}
	}
}

func TestFileDownloadLastPieceShorter(t *testing.T) {
	// 3 pieces: first two are full BlockSize, last is half
	const numPieces = 3
	const pieceLen = BlockSize
	const lastPieceLen = BlockSize / 2
	totalLen := (numPieces-1)*pieceLen + lastPieceLen

	allData := make([]byte, totalLen)
	rand.Read(allData)

	var hashes [][20]byte
	allPieces := make(map[int][]byte)
	for i := range numPieces {
		start := i * pieceLen
		end := start + pieceLen
		if end > totalLen {
			end = totalLen
		}
		piece := allData[start:end]
		allPieces[i] = piece
		hashes = append(hashes, sha1.Sum(piece))
	}

	infoHash := sha1.Sum([]byte("short-last"))

	tor := &torrent.Torrent{
		InfoHash:    infoHash,
		PieceLength: pieceLen,
		Pieces:      hashes,
		Length:      totalLen,
		Name:        "short.bin",
	}

	server := startMockServer(t, numPieces, allPieces, infoHash)
	defer server.close()

	outDir := t.TempDir()
	outPath := filepath.Join(outDir, "short.bin")

	var clientID [20]byte
	copy(clientID[:], "-PP0001-short-last!!")

	err := File(context.Background(), Config{
		Torrent:    tor,
		Peers:      []string{server.addr},
		OutputPath: outPath,
		PeerID:     clientID,
		Quiet:      true,
	})
	if err != nil {
		t.Fatalf("File() error: %v", err)
	}

	got, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("read output: %v", err)
	}

	if len(got) != totalLen {
		t.Fatalf("output size = %d, want %d", len(got), totalLen)
	}

	for i := range totalLen {
		if got[i] != allData[i] {
			t.Fatalf("byte %d: got %02x, want %02x", i, got[i], allData[i])
		}
	}
}
