// Package utp implements the uTorrent Transport Protocol (BEP 29).
//
// uTP is a reliable, ordered transport layered on UDP with delay-based
// congestion control (LEDBAT). This package provides the packet header
// codec, extension parsing, connection state machine, and selective ACK
// bitmask operations.
package utp

import (
	"encoding/binary"
	"fmt"
)

// Packet types (4-bit field in the header).
const (
	StData  byte = 0 // regular data packet
	StFin   byte = 1 // finalize (close) connection
	StState byte = 2 // ACK, no data (does not increment seq_nr)
	StReset byte = 3 // forceful termination
	StSyn   byte = 4 // connection initiation
)

// Extension types.
const (
	ExtNone         byte = 0 // no extension / end of chain
	ExtSelectiveAck byte = 1 // selective ACK bitmask
)

// Connection states.
const (
	CsIdle      byte = 0
	CsSynSent   byte = 1
	CsSynRecv   byte = 2
	CsConnected byte = 3
	CsFinSent   byte = 4
	CsDestroy   byte = 5
)

// HeaderSize is the fixed uTP v1 header size in bytes.
const HeaderSize = 20

// Version is the uTP protocol version.
const Version byte = 1

// Header represents a uTP v1 packet header.
type Header struct {
	Type      byte   // 4-bit packet type (upper nibble of byte 0)
	Version   byte   // 4-bit version (lower nibble of byte 0)
	Extension byte   // first extension type (0 = none)
	ConnID    uint16 // connection identifier
	Timestamp uint32 // microseconds timestamp
	TimeDiff  uint32 // timestamp_difference_microseconds
	WndSize   uint32 // advertised receive window (bytes)
	SeqNr     uint16 // sequence number (packet-based, not byte-based)
	AckNr     uint16 // last received sequence number
}

// ExtensionHeader represents a uTP extension in the linked list.
type ExtensionHeader struct {
	Type    byte   // this extension's type (set when decoding, used for identification)
	Payload []byte // extension data
}

// Packet represents a parsed uTP packet: header + extensions + data payload.
type Packet struct {
	Header     Header
	Extensions []ExtensionHeader
	Payload    []byte
}

// EncodeHeader writes the 20-byte uTP v1 header to buf.
// buf must be at least 20 bytes.
func EncodeHeader(h Header, buf []byte) {
	buf[0] = (h.Type << 4) | (h.Version & 0x0f)
	buf[1] = h.Extension
	binary.BigEndian.PutUint16(buf[2:], h.ConnID)
	binary.BigEndian.PutUint32(buf[4:], h.Timestamp)
	binary.BigEndian.PutUint32(buf[8:], h.TimeDiff)
	binary.BigEndian.PutUint32(buf[12:], h.WndSize)
	binary.BigEndian.PutUint16(buf[16:], h.SeqNr)
	binary.BigEndian.PutUint16(buf[18:], h.AckNr)
}

// DecodeHeader reads the 20-byte uTP v1 header from data.
func DecodeHeader(data []byte) (Header, error) {
	if len(data) < HeaderSize {
		return Header{}, fmt.Errorf("utp header too short: %d bytes", len(data))
	}
	h := Header{
		Type:      (data[0] >> 4) & 0x0f,
		Version:   data[0] & 0x0f,
		Extension: data[1],
		ConnID:    binary.BigEndian.Uint16(data[2:]),
		Timestamp: binary.BigEndian.Uint32(data[4:]),
		TimeDiff:  binary.BigEndian.Uint32(data[8:]),
		WndSize:   binary.BigEndian.Uint32(data[12:]),
		SeqNr:     binary.BigEndian.Uint16(data[16:]),
		AckNr:     binary.BigEndian.Uint16(data[18:]),
	}
	if h.Version != Version {
		return h, fmt.Errorf("unsupported utp version: %d", h.Version)
	}
	return h, nil
}

// Encode serializes a full uTP packet (header + extensions + payload).
func (p *Packet) Encode() []byte {
	extSize := 0
	for _, ext := range p.Extensions {
		extSize += 2 + len(ext.Payload)
	}
	buf := make([]byte, HeaderSize+extSize+len(p.Payload))
	EncodeHeader(p.Header, buf)

	// Write the extension linked list. The "next extension" pointer for each
	// entry is stored in the first byte of that extension's 2-byte header.
	// The last extension's "next" is 0 (ExtNone).
	off := HeaderSize
	for i, ext := range p.Extensions {
		if i+1 < len(p.Extensions) {
			buf[off] = p.Extensions[i+1].Type // next pointer
		} else {
			buf[off] = ExtNone // end of chain
		}
		buf[off+1] = byte(len(ext.Payload))
		copy(buf[off+2:], ext.Payload)
		off += 2 + len(ext.Payload)
	}

	copy(buf[off:], p.Payload)
	return buf
}

// DecodePacket parses a full uTP packet from raw bytes.
func DecodePacket(data []byte) (Packet, error) {
	h, err := DecodeHeader(data)
	if err != nil {
		return Packet{}, err
	}

	pkt := Packet{Header: h}
	off := HeaderSize

	nextExt := h.Extension
	for nextExt != ExtNone {
		if off+2 > len(data) {
			return Packet{}, fmt.Errorf("utp extension header truncated at offset %d", off)
		}
		ext := ExtensionHeader{Type: nextExt}
		nextExt = data[off]      // next extension in chain
		extLen := int(data[off+1]) // payload length
		off += 2

		if off+extLen > len(data) {
			return Packet{}, fmt.Errorf("utp extension payload truncated: need %d at offset %d", extLen, off)
		}
		ext.Payload = make([]byte, extLen)
		copy(ext.Payload, data[off:off+extLen])
		off += extLen
		pkt.Extensions = append(pkt.Extensions, ext)
	}

	if off < len(data) {
		pkt.Payload = make([]byte, len(data)-off)
		copy(pkt.Payload, data[off:])
	}

	return pkt, nil
}

// TypeString returns a human-readable name for a uTP packet type.
func TypeString(t byte) string {
	switch t {
	case StData:
		return "ST_DATA"
	case StFin:
		return "ST_FIN"
	case StState:
		return "ST_STATE"
	case StReset:
		return "ST_RESET"
	case StSyn:
		return "ST_SYN"
	default:
		return fmt.Sprintf("ST_UNKNOWN(%d)", t)
	}
}

// StateString returns a human-readable name for a connection state.
func StateString(s byte) string {
	switch s {
	case CsIdle:
		return "CS_IDLE"
	case CsSynSent:
		return "CS_SYN_SENT"
	case CsSynRecv:
		return "CS_SYN_RECV"
	case CsConnected:
		return "CS_CONNECTED"
	case CsFinSent:
		return "CS_FIN_SENT"
	case CsDestroy:
		return "CS_DESTROY"
	default:
		return fmt.Sprintf("CS_UNKNOWN(%d)", s)
	}
}
