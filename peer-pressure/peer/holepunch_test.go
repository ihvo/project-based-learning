package peer

import (
	"net"
	"testing"
)

func TestHolepunchEncodeDecodeIPv4(t *testing.T) {
	msg := HolepunchMessage{
		MsgType:  HolepunchRendezvous,
		AddrType: HolepunchIPv4,
		IP:       net.ParseIP("192.168.1.100"),
		Port:     6881,
		ErrCode:  0,
	}

	data := EncodeHolepunch(msg)
	// 1 + 1 + 4 + 2 + 4 = 12 bytes
	if len(data) != 12 {
		t.Fatalf("encoded len = %d, want 12", len(data))
	}

	got, err := DecodeHolepunch(data)
	if err != nil {
		t.Fatalf("DecodeHolepunch: %v", err)
	}

	if got.MsgType != HolepunchRendezvous {
		t.Errorf("MsgType = 0x%02x, want 0x00", got.MsgType)
	}
	if got.AddrType != HolepunchIPv4 {
		t.Errorf("AddrType = 0x%02x, want 0x00", got.AddrType)
	}
	if !got.IP.Equal(net.ParseIP("192.168.1.100")) {
		t.Errorf("IP = %s", got.IP)
	}
	if got.Port != 6881 {
		t.Errorf("Port = %d", got.Port)
	}
	if got.ErrCode != 0 {
		t.Errorf("ErrCode = %d", got.ErrCode)
	}
}

func TestHolepunchEncodeDecodeIPv6(t *testing.T) {
	msg := HolepunchMessage{
		MsgType:  HolepunchConnect,
		AddrType: HolepunchIPv6,
		IP:       net.ParseIP("2001:db8::1"),
		Port:     8080,
		ErrCode:  0,
	}

	data := EncodeHolepunch(msg)
	// 1 + 1 + 16 + 2 + 4 = 24 bytes
	if len(data) != 24 {
		t.Fatalf("encoded len = %d, want 24", len(data))
	}

	got, err := DecodeHolepunch(data)
	if err != nil {
		t.Fatalf("DecodeHolepunch: %v", err)
	}

	if got.MsgType != HolepunchConnect {
		t.Errorf("MsgType = 0x%02x, want 0x01", got.MsgType)
	}
	if got.AddrType != HolepunchIPv6 {
		t.Errorf("AddrType = 0x%02x, want 0x01", got.AddrType)
	}
	if !got.IP.Equal(net.ParseIP("2001:db8::1")) {
		t.Errorf("IP = %s", got.IP)
	}
	if got.Port != 8080 {
		t.Errorf("Port = %d", got.Port)
	}
}

func TestHolepunchError(t *testing.T) {
	msg := HolepunchMessage{
		MsgType:  HolepunchError,
		AddrType: HolepunchIPv4,
		IP:       net.ParseIP("10.0.0.1"),
		Port:     6881,
		ErrCode:  HolepunchErrNotConnected,
	}

	data := EncodeHolepunch(msg)
	got, err := DecodeHolepunch(data)
	if err != nil {
		t.Fatalf("DecodeHolepunch: %v", err)
	}

	if got.ErrCode != HolepunchErrNotConnected {
		t.Errorf("ErrCode = %d, want %d", got.ErrCode, HolepunchErrNotConnected)
	}
}

func TestHolepunchDecodeTooShort(t *testing.T) {
	_, err := DecodeHolepunch([]byte{0x00})
	if err == nil {
		t.Error("expected error for 1-byte input")
	}
}

func TestHolepunchDecodeIPv4TooShort(t *testing.T) {
	// msg_type + addr_type(IPv4) + only 3 bytes of address
	_, err := DecodeHolepunch([]byte{0x00, 0x00, 1, 2, 3})
	if err == nil {
		t.Error("expected error for short IPv4 payload")
	}
}

func TestHolepunchDecodeIPv6TooShort(t *testing.T) {
	// msg_type + addr_type(IPv6) + only 10 bytes
	data := make([]byte, 12)
	data[1] = HolepunchIPv6
	_, err := DecodeHolepunch(data)
	if err == nil {
		t.Error("expected error for short IPv6 payload")
	}
}

func TestHolepunchDecodeUnknownAddrType(t *testing.T) {
	_, err := DecodeHolepunch([]byte{0x00, 0xFF})
	if err == nil {
		t.Error("expected error for unknown addr_type")
	}
}

func TestHolepunchErrorString(t *testing.T) {
	tests := []struct {
		code uint32
		want string
	}{
		{HolepunchErrNoSuchPeer, "no such peer"},
		{HolepunchErrNotConnected, "not connected to target"},
		{HolepunchErrNoSupport, "target does not support holepunch"},
		{HolepunchErrNoSelf, "target is the relaying peer"},
		{0xFF, "unknown error 0x000000ff"},
	}
	for _, tc := range tests {
		got := HolepunchErrorString(tc.code)
		if got != tc.want {
			t.Errorf("HolepunchErrorString(0x%x) = %q, want %q", tc.code, got, tc.want)
		}
	}
}
