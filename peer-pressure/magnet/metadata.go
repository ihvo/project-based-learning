// Package magnet implements magnet link parsing and metadata exchange (BEP 9).
//
// A magnet URI identifies a torrent by its info_hash alone. To download,
// we first need the full torrent metadata (info dictionary). BEP 9 defines
// a protocol extension (ut_metadata) that lets us fetch this metadata
// directly from peers via the extension protocol (BEP 10).
//
// The metadata is split into 16 KiB pieces. Each piece is requested via
// a bencoded {msg_type: 0, piece: N} message, and the peer responds with
// {msg_type: 1, piece: N, total_size: S} followed by the raw piece data.
//
// Reference: https://www.bittorrent.org/beps/bep_0009.html
package magnet

import (
	"crypto/sha1"
	"fmt"
	"time"

	"github.com/ihvo/peer-pressure/bencode"
	"github.com/ihvo/peer-pressure/peer"
)

const metadataPieceSize = 16384 // 16 KiB per BEP 9

// Metadata message types.
const (
	MetadataRequest = 0
	MetadataData    = 1
	MetadataReject  = 2
)

// FetchMetadata downloads the info dictionary from a connected peer using
// the ut_metadata extension (BEP 9). The peer must have already completed
// an extension handshake advertising ut_metadata support and metadata_size.
//
// Returns the raw info dictionary bytes, verified against infoHash.
func FetchMetadata(conn *peer.Conn, infoHash [20]byte) ([]byte, error) {
	ext := conn.PeerExtensions
	if ext == nil {
		return nil, fmt.Errorf("no extension handshake completed")
	}

	utMetadataID, ok := ext.M["ut_metadata"]
	if !ok || utMetadataID == 0 {
		return nil, fmt.Errorf("peer does not support ut_metadata")
	}

	metadataSize := ext.MetadataSize
	if metadataSize <= 0 {
		return nil, fmt.Errorf("peer reported metadata_size=%d", metadataSize)
	}

	numPieces := (metadataSize + metadataPieceSize - 1) / metadataPieceSize
	metadata := make([]byte, metadataSize)
	received := make([]bool, numPieces)

	// Request all pieces.
	for i := range numPieces {
		req := bencode.Dict{
			"msg_type": bencode.Int(MetadataRequest),
			"piece":    bencode.Int(i),
		}
		msg := peer.NewExtMessage(uint8(utMetadataID), bencode.Encode(req))
		if err := conn.WriteMessage(msg); err != nil {
			return nil, fmt.Errorf("send metadata request %d: %w", i, err)
		}
	}
	if err := conn.Flush(); err != nil {
		return nil, fmt.Errorf("flush metadata requests: %w", err)
	}

	// Read responses.
	pending := numPieces
	for pending > 0 {
		conn.SetDeadline(time.Now().Add(30 * time.Second))
		msg, err := conn.ReadMessage()
		if err != nil {
			return nil, fmt.Errorf("read metadata response: %w", err)
		}
		if msg == nil {
			continue // keep-alive
		}
		if msg.ID != peer.MsgExtended || len(msg.Payload) < 2 {
			continue // not an extension message
		}

		payload := msg.Payload[1:] // skip sub-ID byte

		piece, data, msgType, err := parseMetadataResponse(payload)
		if err != nil {
			continue // not a metadata message, skip
		}

		if msgType == MetadataReject {
			return nil, fmt.Errorf("peer rejected metadata piece %d", piece)
		}

		if msgType != MetadataData {
			continue
		}

		if piece < 0 || piece >= numPieces {
			continue // out of range
		}
		if received[piece] {
			continue // duplicate
		}

		// Copy piece data into the right offset.
		offset := piece * metadataPieceSize
		expectedLen := metadataPieceSize
		if piece == numPieces-1 {
			expectedLen = metadataSize - offset
		}
		if len(data) != expectedLen {
			return nil, fmt.Errorf("metadata piece %d: got %d bytes, want %d",
				piece, len(data), expectedLen)
		}

		copy(metadata[offset:], data)
		received[piece] = true
		pending--
	}

	conn.SetDeadline(time.Time{})

	// Verify the info hash.
	hash := sha1.Sum(metadata)
	if hash != infoHash {
		return nil, fmt.Errorf("metadata hash mismatch: got %x, want %x", hash, infoHash)
	}

	return metadata, nil
}

// parseMetadataResponse parses a ut_metadata response message.
// BEP 9 format: bencoded dict followed by raw piece data (for data messages).
func parseMetadataResponse(payload []byte) (piece int, data []byte, msgType int, err error) {
	val, n, err := bencode.DecodeFirst(payload)
	if err != nil {
		return 0, nil, 0, fmt.Errorf("decode metadata dict: %w", err)
	}

	d, ok := val.(bencode.Dict)
	if !ok {
		return 0, nil, 0, fmt.Errorf("expected dict, got %T", val)
	}

	mtVal, ok := d["msg_type"]
	if !ok {
		return 0, nil, 0, fmt.Errorf("missing msg_type")
	}
	mt, ok := mtVal.(bencode.Int)
	if !ok {
		return 0, nil, 0, fmt.Errorf("msg_type not int")
	}

	pVal, ok := d["piece"]
	if !ok {
		return 0, nil, 0, fmt.Errorf("missing piece")
	}
	p, ok := pVal.(bencode.Int)
	if !ok {
		return 0, nil, 0, fmt.Errorf("piece not int")
	}

	return int(p), payload[n:], int(mt), nil
}
