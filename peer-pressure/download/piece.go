// Package download implements BitTorrent piece downloading and verification.
//
// A piece is the unit of verification in BitTorrent — each has a SHA-1 hash
// recorded in the .torrent file. Pieces are transferred as smaller blocks
// (typically 16 KiB) via request/piece message pairs.
//
// Reference: https://www.bittorrent.org/beps/bep_0003.html
package download

import (
	"crypto/sha1"
	"fmt"
	"time"

	"github.com/ihvo/peer-pressure/peer"
)

const BlockSize = 16384 // 16 KiB — standard block size per convention

// Piece downloads and verifies a single piece from a peer connection.
//
// The caller must have already completed the handshake. This function handles
// the interested/unchoke negotiation, sends block requests, reassembles the
// piece, and verifies its SHA-1 hash.
func Piece(conn *peer.Conn, pieceIndex int, pieceLength int, expectedHash [20]byte) ([]byte, error) {
	if _, err := NegotiateUnchoke(conn, 0); err != nil {
		return nil, fmt.Errorf("negotiate unchoke: %w", err)
	}
	return DownloadAndVerify(conn, pieceIndex, pieceLength, expectedHash, nil)
}

// BlockCallback is called each time a block is received during piece download.
// Arguments: piece index, block begin offset, block length.
type BlockCallback func(pieceIndex, blockBegin, blockLen int)

// DownloadAndVerify downloads all blocks for a piece and verifies the SHA-1
// hash. The connection must already be unchoked. Used by the session
// orchestrator which negotiates unchoke once per peer, then downloads many
// pieces. Pass nil for onBlock if block-level reporting is not needed.
func DownloadAndVerify(conn *peer.Conn, pieceIndex, pieceLength int, expectedHash [20]byte, onBlock BlockCallback) ([]byte, error) {
	buf, err := downloadBlocks(conn, pieceIndex, pieceLength, onBlock)
	if err != nil {
		return nil, fmt.Errorf("download blocks: %w", err)
	}

	actualHash := sha1.Sum(buf)
	if actualHash != expectedHash {
		return nil, fmt.Errorf("piece %d hash mismatch: got %x, want %x", pieceIndex, actualHash, expectedHash)
	}

	return buf, nil
}

// NegotiateUnchoke sends interested and reads messages until we receive unchoke.
// numPieces is needed to synthesize a bitfield when the peer sends HaveAll
// (BEP 6) or unchokes without any availability info.
//
// The write is done in a goroutine to avoid deadlocking on unbuffered
// transports (like net.Pipe) where the peer may also be writing.
//
// Returns the peer's bitfield. If the peer didn't send a Bitfield or HaveAll,
// we assume it has all pieces (a peer wouldn't unchoke us with nothing to offer).
func NegotiateUnchoke(conn *peer.Conn, numPieces int) (bitfield []byte, err error) {
	// Give the peer 30 seconds to unchoke us.
	conn.SetDeadline(time.Now().Add(30 * time.Second))

	writeErr := make(chan error, 1)
	go func() {
		writeErr <- conn.WriteMessage(peer.NewInterested())
	}()

	for {
		msg, err := conn.ReadMessage()
		if err != nil {
			return nil, fmt.Errorf("waiting for unchoke: %w", err)
		}
		if msg == nil {
			continue // keep-alive
		}

		switch msg.ID {
		case peer.MsgUnchoke:
			if err := <-writeErr; err != nil {
				return nil, fmt.Errorf("send interested: %w", err)
			}
			if bitfield == nil {
				// Peer unchoked us without telling us what it has.
				// Assume it has everything — if a specific request fails,
				// the download loop will handle it per-piece.
				bitfield = makeFullBitfield(numPieces)
			}
			conn.SetDeadline(time.Time{}) // clear for subsequent I/O
			return bitfield, nil

		case peer.MsgBitfield:
			bitfield = msg.Payload

		case peer.MsgHaveAll:
			// BEP 6: peer has every piece.
			bitfield = makeFullBitfield(numPieces)

		case peer.MsgHaveNone:
			// BEP 6: peer has nothing. Leave bitfield nil — will be
			// overridden to "all" at unchoke since we can't know better.
			bitfield = nil

		case peer.MsgHave, peer.MsgChoke:
			if msg.ID == peer.MsgChoke {
				return nil, fmt.Errorf("peer choked us")
			}
			continue

		default:
			continue
		}
	}
}

// makeFullBitfield creates a bitfield with all numPieces bits set to 1.
// The last byte may have trailing zero bits if numPieces isn't a multiple of 8.
func makeFullBitfield(numPieces int) []byte {
	if numPieces <= 0 {
		return nil
	}
	numBytes := (numPieces + 7) / 8
	bf := make([]byte, numBytes)
	// Fill all bytes with 0xFF (all bits set)
	for i := range bf {
		bf[i] = 0xFF
	}
	// Clear the trailing bits in the last byte that don't correspond to real pieces.
	// E.g. if numPieces=10, last byte should be 0b11000000 (bits 8,9 set, 10-15 cleared).
	spare := numBytes*8 - numPieces
	if spare > 0 {
		bf[numBytes-1] = 0xFF << spare
	}
	return bf
}

// downloadBlocks requests and collects all blocks for a piece.
// Requests are sent from a goroutine so reads and writes happen concurrently —
// this avoids deadlocks on unbuffered transports and naturally pipelines
// requests on real TCP connections.
func downloadBlocks(conn *peer.Conn, pieceIndex, pieceLength int, onBlock BlockCallback) ([]byte, error) {
	buf := make([]byte, pieceLength)

	numBlocks := pieceLength / BlockSize
	if pieceLength%BlockSize != 0 {
		numBlocks++
	}

	// Allow 30 seconds per piece for data transfer.
	conn.SetDeadline(time.Now().Add(30 * time.Second))
	defer conn.SetDeadline(time.Time{})

	// Send all requests concurrently
	requestErr := make(chan error, 1)
	go func() {
		for i := range numBlocks {
			begin := i * BlockSize
			length := BlockSize
			if begin+length > pieceLength {
				length = pieceLength - begin
			}

			err := conn.WriteMessage(peer.NewRequest(uint32(pieceIndex), uint32(begin), uint32(length)))
			if err != nil {
				requestErr <- fmt.Errorf("send request (begin=%d): %w", begin, err)
				return
			}
		}
		requestErr <- nil
	}()

	// Collect responses
	received := 0
	for received < numBlocks {
		msg, err := conn.ReadMessage()
		if err != nil {
			return nil, fmt.Errorf("read piece response: %w", err)
		}
		if msg == nil {
			continue // keep-alive
		}

		switch msg.ID {
		case peer.MsgPiece:
			pp, err := peer.ParsePiece(msg.Payload)
			if err != nil {
				return nil, fmt.Errorf("parse piece message: %w", err)
			}
			if int(pp.Index) != pieceIndex {
				return nil, fmt.Errorf("wrong piece index: got %d, want %d", pp.Index, pieceIndex)
			}
			if int(pp.Begin)+len(pp.Block) > pieceLength {
				return nil, fmt.Errorf("block overflows piece: begin=%d block_len=%d piece_len=%d",
					pp.Begin, len(pp.Block), pieceLength)
			}
			copy(buf[pp.Begin:], pp.Block)
			received++
			if onBlock != nil {
				onBlock(pieceIndex, int(pp.Begin), len(pp.Block))
			}

		case peer.MsgChoke:
			return nil, fmt.Errorf("peer choked us during download")

		default:
			continue
		}
	}

	// Check that all requests were sent successfully
	if err := <-requestErr; err != nil {
		return nil, err
	}

	return buf, nil
}

// BlockCount returns how many blocks a piece of the given size requires.
func BlockCount(pieceLength int) int {
	n := pieceLength / BlockSize
	if pieceLength%BlockSize != 0 {
		n++
	}
	return n
}
