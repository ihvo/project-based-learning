// Package peer implements the BitTorrent peer wire protocol (BEP 3).
//
// The protocol operates over TCP. Each connection begins with a 68-byte
// handshake, followed by a stream of length-prefixed messages. Messages
// control choking/interest state and transfer piece data as blocks.
//
// Reference: https://www.bittorrent.org/beps/bep_0003.html
// Reference: https://www.bittorrent.org/beps/bep_0020.html
package peer

import (
	"encoding/binary"
	"fmt"
	"io"
)

const (
	protocolStr = "BitTorrent protocol"
	handshakeLen = 1 + len(protocolStr) + 8 + 20 + 20 // 68 bytes
)

// Message IDs as defined in BEP 3.
const (
	MsgChoke         uint8 = 0
	MsgUnchoke       uint8 = 1
	MsgInterested    uint8 = 2
	MsgNotInterested uint8 = 3
	MsgHave          uint8 = 4
	MsgBitfield      uint8 = 5
	MsgRequest       uint8 = 6
	MsgPiece         uint8 = 7
	MsgCancel        uint8 = 8

	// BEP 6 — Fast Extension.
	MsgSuggestPiece  uint8 = 13
	MsgHaveAll       uint8 = 14
	MsgHaveNone      uint8 = 15
	MsgRejectRequest uint8 = 16
	MsgAllowedFast   uint8 = 17

	// BEP 10 — Extension Protocol. Extended messages carry a sub-ID
	// in the first payload byte. Sub-ID 0 is the extension handshake.
	MsgExtended uint8 = 20
)

// --- Handshake ---

// Handshake is the 68-byte handshake that opens every peer connection.
type Handshake struct {
	InfoHash [20]byte
	PeerID   [20]byte
	Reserved [8]byte // reserved bytes — bit 43 signals BEP 10 extension support
}

// SupportsExtensions returns true if the peer set bit 43 (BEP 10).
func (h *Handshake) SupportsExtensions() bool {
	return h.Reserved[5]&0x10 != 0
}

// SupportsFast returns true if the peer set bit 61 (BEP 6 Fast Extension).
func (h *Handshake) SupportsFast() bool {
	return h.Reserved[7]&0x04 != 0
}

// WriteHandshake serializes and writes a handshake to w.
// Sets reserved bit 43 to advertise BEP 10 extension protocol support.
func WriteHandshake(w io.Writer, h *Handshake) error {
	var buf [handshakeLen]byte
	buf[0] = byte(len(protocolStr))
	copy(buf[1:20], protocolStr)
	// bytes 20-27: reserved — set bit 43 for BEP 10, bit 61 for BEP 6
	copy(buf[20:28], h.Reserved[:])
	buf[25] |= 0x10 // bit 43 = byte 5, bit 4 (BEP 10)
	buf[27] |= 0x04 // bit 61 = byte 7, bit 2 (BEP 6)
	copy(buf[28:48], h.InfoHash[:])
	copy(buf[48:68], h.PeerID[:])

	_, err := w.Write(buf[:])
	return err
}

// ReadHandshake reads and parses a 68-byte handshake from r.
// Returns an error if the protocol string doesn't match.
func ReadHandshake(r io.Reader) (*Handshake, error) {
	var buf [handshakeLen]byte
	if _, err := io.ReadFull(r, buf[:]); err != nil {
		return nil, fmt.Errorf("read handshake: %w", err)
	}

	pstrLen := int(buf[0])
	if pstrLen != len(protocolStr) {
		return nil, fmt.Errorf("unexpected protocol string length: %d", pstrLen)
	}
	if string(buf[1:20]) != protocolStr {
		return nil, fmt.Errorf("unexpected protocol string: %q", string(buf[1:20]))
	}

	h := &Handshake{}
	copy(h.Reserved[:], buf[20:28])
	copy(h.InfoHash[:], buf[28:48])
	copy(h.PeerID[:], buf[48:68])
	return h, nil
}

// --- Messages ---

// Message is a peer wire protocol message.
// A nil Message (ID irrelevant, Payload nil) represents a keep-alive.
type Message struct {
	ID      uint8
	Payload []byte
}

// WriteMessage serializes and writes a message to w.
// A nil message is written as a keep-alive (4 zero bytes).
func WriteMessage(w io.Writer, m *Message) error {
	if m == nil {
		// Keep-alive: length = 0
		_, err := w.Write([]byte{0, 0, 0, 0})
		return err
	}

	// length = 1 (ID) + len(payload)
	length := uint32(1 + len(m.Payload))
	if err := binary.Write(w, binary.BigEndian, length); err != nil {
		return err
	}
	if _, err := w.Write([]byte{m.ID}); err != nil {
		return err
	}
	if len(m.Payload) > 0 {
		if _, err := w.Write(m.Payload); err != nil {
			return err
		}
	}
	return nil
}

// MaxMessageLen limits the maximum message payload to 16 MiB.
// A piece message for a 16 KiB block is ~16 KiB + 9 bytes.
// This guard prevents malicious peers from causing OOM.
const MaxMessageLen = 1 << 24 // 16 MiB

// ReadMessage reads one message from r. Returns nil for keep-alive messages.
func ReadMessage(r io.Reader) (*Message, error) {
	var length uint32
	if err := binary.Read(r, binary.BigEndian, &length); err != nil {
		return nil, fmt.Errorf("read message length: %w", err)
	}

	// Keep-alive
	if length == 0 {
		return nil, nil
	}

	if length > MaxMessageLen {
		return nil, fmt.Errorf("message too large: %d bytes", length)
	}

	// Read the message ID + payload
	buf := make([]byte, length)
	if _, err := io.ReadFull(r, buf); err != nil {
		return nil, fmt.Errorf("read message body: %w", err)
	}

	return &Message{
		ID:      buf[0],
		Payload: buf[1:],
	}, nil
}

// --- Message constructors ---
// These build Message values for each protocol message type, encoding
// the payload in the correct binary format.

// MsgKeepAlive returns nil, which WriteMessage encodes as a keep-alive.
func MsgKeepAlive() *Message { return nil }

func NewChoke() *Message         { return &Message{ID: MsgChoke} }
func NewUnchoke() *Message       { return &Message{ID: MsgUnchoke} }
func NewInterested() *Message    { return &Message{ID: MsgInterested} }
func NewNotInterested() *Message { return &Message{ID: MsgNotInterested} }

func NewHave(pieceIndex uint32) *Message {
	payload := make([]byte, 4)
	binary.BigEndian.PutUint32(payload, pieceIndex)
	return &Message{ID: MsgHave, Payload: payload}
}

func NewBitfield(bitfield []byte) *Message {
	return &Message{ID: MsgBitfield, Payload: bitfield}
}

func NewRequest(index, begin, length uint32) *Message {
	payload := make([]byte, 12)
	binary.BigEndian.PutUint32(payload[0:4], index)
	binary.BigEndian.PutUint32(payload[4:8], begin)
	binary.BigEndian.PutUint32(payload[8:12], length)
	return &Message{ID: MsgRequest, Payload: payload}
}

func NewPiece(index, begin uint32, block []byte) *Message {
	payload := make([]byte, 8+len(block))
	binary.BigEndian.PutUint32(payload[0:4], index)
	binary.BigEndian.PutUint32(payload[4:8], begin)
	copy(payload[8:], block)
	return &Message{ID: MsgPiece, Payload: payload}
}

func NewCancel(index, begin, length uint32) *Message {
	payload := make([]byte, 12)
	binary.BigEndian.PutUint32(payload[0:4], index)
	binary.BigEndian.PutUint32(payload[4:8], begin)
	binary.BigEndian.PutUint32(payload[8:12], length)
	return &Message{ID: MsgCancel, Payload: payload}
}

// BEP 6 — Fast Extension message constructors.

func NewSuggestPiece(index uint32) *Message {
	payload := make([]byte, 4)
	binary.BigEndian.PutUint32(payload, index)
	return &Message{ID: MsgSuggestPiece, Payload: payload}
}

func NewHaveAll() *Message  { return &Message{ID: MsgHaveAll} }
func NewHaveNone() *Message { return &Message{ID: MsgHaveNone} }

func NewRejectRequest(index, begin, length uint32) *Message {
	payload := make([]byte, 12)
	binary.BigEndian.PutUint32(payload[0:4], index)
	binary.BigEndian.PutUint32(payload[4:8], begin)
	binary.BigEndian.PutUint32(payload[8:12], length)
	return &Message{ID: MsgRejectRequest, Payload: payload}
}

func NewAllowedFast(index uint32) *Message {
	payload := make([]byte, 4)
	binary.BigEndian.PutUint32(payload, index)
	return &Message{ID: MsgAllowedFast, Payload: payload}
}

// --- Payload parsers ---
// These extract structured data from message payloads.

// ParseHave extracts the piece index from a Have message payload.
func ParseHave(payload []byte) (uint32, error) {
	if len(payload) != 4 {
		return 0, fmt.Errorf("have payload: expected 4 bytes, got %d", len(payload))
	}
	return binary.BigEndian.Uint32(payload), nil
}

// RequestPayload holds the fields of a Request or Cancel message.
type RequestPayload struct {
	Index  uint32
	Begin  uint32
	Length uint32
}

// ParseRequest extracts fields from a Request or Cancel message payload.
func ParseRequest(payload []byte) (RequestPayload, error) {
	if len(payload) != 12 {
		return RequestPayload{}, fmt.Errorf("request payload: expected 12 bytes, got %d", len(payload))
	}
	return RequestPayload{
		Index:  binary.BigEndian.Uint32(payload[0:4]),
		Begin:  binary.BigEndian.Uint32(payload[4:8]),
		Length: binary.BigEndian.Uint32(payload[8:12]),
	}, nil
}

// PiecePayload holds the fields of a Piece message.
type PiecePayload struct {
	Index uint32
	Begin uint32
	Block []byte
}

// ParsePiece extracts fields from a Piece message payload.
func ParsePiece(payload []byte) (PiecePayload, error) {
	if len(payload) < 8 {
		return PiecePayload{}, fmt.Errorf("piece payload: expected at least 8 bytes, got %d", len(payload))
	}
	return PiecePayload{
		Index: binary.BigEndian.Uint32(payload[0:4]),
		Begin: binary.BigEndian.Uint32(payload[4:8]),
		Block: payload[8:],
	}, nil
}
