package peer

import (
	"encoding/binary"
	"fmt"
	"net"
)

// BEP 55: Holepunch extension message types.
const (
	HolepunchRendezvous byte = 0x00
	HolepunchConnect    byte = 0x01
	HolepunchError      byte = 0x02
)

// BEP 55: Address types.
const (
	HolepunchIPv4 byte = 0x00
	HolepunchIPv6 byte = 0x01
)

// BEP 55: Error codes.
const (
	HolepunchErrNoSuchPeer   uint32 = 0x01
	HolepunchErrNotConnected uint32 = 0x02
	HolepunchErrNoSupport    uint32 = 0x03
	HolepunchErrNoSelf       uint32 = 0x04
)

// HolepunchMessage represents a BEP 55 holepunch extension message.
type HolepunchMessage struct {
	MsgType  byte   // rendezvous, connect, or error
	AddrType byte   // 0x00 = IPv4, 0x01 = IPv6
	IP       net.IP // 4 bytes (IPv4) or 16 bytes (IPv6)
	Port     uint16
	ErrCode  uint32 // 0 for non-error messages
}

// EncodeHolepunch encodes a BEP 55 holepunch message to binary.
// Returns the payload (without the extension message header).
func EncodeHolepunch(msg HolepunchMessage) []byte {
	var addrLen int
	var addrBytes []byte

	if msg.AddrType == HolepunchIPv6 {
		addrLen = 16
		addrBytes = msg.IP.To16()
	} else {
		addrLen = 4
		addrBytes = msg.IP.To4()
	}
	if addrBytes == nil {
		addrBytes = make([]byte, addrLen)
	}

	// msg_type(1) + addr_type(1) + addr(4|16) + port(2) + err_code(4) = 12 or 24
	buf := make([]byte, 2+addrLen+2+4)
	buf[0] = msg.MsgType
	buf[1] = msg.AddrType
	copy(buf[2:2+addrLen], addrBytes)
	binary.BigEndian.PutUint16(buf[2+addrLen:], msg.Port)
	binary.BigEndian.PutUint32(buf[2+addrLen+2:], msg.ErrCode)
	return buf
}

// DecodeHolepunch parses a BEP 55 holepunch message from binary.
func DecodeHolepunch(data []byte) (HolepunchMessage, error) {
	if len(data) < 2 {
		return HolepunchMessage{}, fmt.Errorf("holepunch message too short: %d bytes", len(data))
	}

	msg := HolepunchMessage{
		MsgType:  data[0],
		AddrType: data[1],
	}

	var addrLen int
	switch msg.AddrType {
	case HolepunchIPv4:
		addrLen = 4
	case HolepunchIPv6:
		addrLen = 16
	default:
		return HolepunchMessage{}, fmt.Errorf("unknown holepunch addr_type: 0x%02x", msg.AddrType)
	}

	minLen := 2 + addrLen + 2 + 4 // msg_type + addr_type + addr + port + err_code
	if len(data) < minLen {
		return HolepunchMessage{}, fmt.Errorf("holepunch message too short: %d bytes, need %d", len(data), minLen)
	}

	msg.IP = make(net.IP, addrLen)
	copy(msg.IP, data[2:2+addrLen])
	msg.Port = binary.BigEndian.Uint16(data[2+addrLen:])
	msg.ErrCode = binary.BigEndian.Uint32(data[2+addrLen+2:])
	return msg, nil
}

// HolepunchErrorString returns a human-readable description of a BEP 55 error code.
func HolepunchErrorString(code uint32) string {
	switch code {
	case HolepunchErrNoSuchPeer:
		return "no such peer"
	case HolepunchErrNotConnected:
		return "not connected to target"
	case HolepunchErrNoSupport:
		return "target does not support holepunch"
	case HolepunchErrNoSelf:
		return "target is the relaying peer"
	default:
		return fmt.Sprintf("unknown error 0x%08x", code)
	}
}
