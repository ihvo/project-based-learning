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

	"github.com/ihvo/peer-pressure/peer"
)

const BlockSize = 16384 // 16 KiB — standard block size per convention

// Piece downloads and verifies a single piece from a peer connection.
//
// The caller must have already completed the handshake. This function handles
// the interested/unchoke negotiation, sends block requests, reassembles the
// piece, and verifies its SHA-1 hash.
func Piece(conn *peer.Conn, pieceIndex int, pieceLength int, expectedHash [20]byte) ([]byte, error) {
	if _, err := NegotiateUnchoke(conn); err != nil {
		return nil, fmt.Errorf("negotiate unchoke: %w", err)
	}
	return DownloadAndVerify(conn, pieceIndex, pieceLength, expectedHash)
}

// DownloadAndVerify downloads all blocks for a piece and verifies the SHA-1
// hash. The connection must already be unchoked. Used by the session
// orchestrator which negotiates unchoke once per peer, then downloads many
// pieces.
func DownloadAndVerify(conn *peer.Conn, pieceIndex, pieceLength int, expectedHash [20]byte) ([]byte, error) {
	buf, err := downloadBlocks(conn, pieceIndex, pieceLength)
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
// Discards bitfield and have messages received during negotiation.
// The write is done in a goroutine to avoid deadlocking on unbuffered
// transports (like net.Pipe) where the peer may also be writing.
// Returns the bitfield if one was received, or nil.
func NegotiateUnchoke(conn *peer.Conn) (bitfield []byte, err error) {
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
			return bitfield, nil
		case peer.MsgBitfield:
			bitfield = msg.Payload
		case peer.MsgHave:
			continue // expected during negotiation
		case peer.MsgChoke:
			return nil, fmt.Errorf("peer choked us")
		default:
			continue
		}
	}
}

// downloadBlocks requests and collects all blocks for a piece.
// Requests are sent from a goroutine so reads and writes happen concurrently —
// this avoids deadlocks on unbuffered transports and naturally pipelines
// requests on real TCP connections.
func downloadBlocks(conn *peer.Conn, pieceIndex, pieceLength int) ([]byte, error) {
	buf := make([]byte, pieceLength)

	numBlocks := pieceLength / BlockSize
	if pieceLength%BlockSize != 0 {
		numBlocks++
	}

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
