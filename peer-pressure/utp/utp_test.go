package utp

import (
	"bytes"
	"testing"
)

// ---- Header encode/decode ----

func TestHeaderRoundTrip(t *testing.T) {
	h := Header{
		Type:      StSyn,
		Version:   Version,
		Extension: ExtNone,
		ConnID:    0xABCD,
		Timestamp: 1000000,
		TimeDiff:  500000,
		WndSize:   65535,
		SeqNr:     1,
		AckNr:     0,
	}

	buf := make([]byte, HeaderSize)
	EncodeHeader(h, buf)

	got, err := DecodeHeader(buf)
	if err != nil {
		t.Fatalf("DecodeHeader: %v", err)
	}

	if got.Type != h.Type {
		t.Errorf("Type = %d, want %d", got.Type, h.Type)
	}
	if got.Version != h.Version {
		t.Errorf("Version = %d, want %d", got.Version, h.Version)
	}
	if got.Extension != h.Extension {
		t.Errorf("Extension = %d, want %d", got.Extension, h.Extension)
	}
	if got.ConnID != h.ConnID {
		t.Errorf("ConnID = 0x%04x, want 0x%04x", got.ConnID, h.ConnID)
	}
	if got.Timestamp != h.Timestamp {
		t.Errorf("Timestamp = %d, want %d", got.Timestamp, h.Timestamp)
	}
	if got.TimeDiff != h.TimeDiff {
		t.Errorf("TimeDiff = %d, want %d", got.TimeDiff, h.TimeDiff)
	}
	if got.WndSize != h.WndSize {
		t.Errorf("WndSize = %d, want %d", got.WndSize, h.WndSize)
	}
	if got.SeqNr != h.SeqNr {
		t.Errorf("SeqNr = %d, want %d", got.SeqNr, h.SeqNr)
	}
	if got.AckNr != h.AckNr {
		t.Errorf("AckNr = %d, want %d", got.AckNr, h.AckNr)
	}
}

func TestHeaderTypeVersionBitLayout(t *testing.T) {
	// BEP 29: byte 0 = (type << 4) | version
	h := Header{Type: StSyn, Version: Version}
	buf := make([]byte, HeaderSize)
	EncodeHeader(h, buf)

	wantByte0 := byte(0x41) // (4 << 4) | 1
	if buf[0] != wantByte0 {
		t.Errorf("byte[0] = 0x%02x, want 0x%02x", buf[0], wantByte0)
	}
}

func TestDecodeHeaderTooShort(t *testing.T) {
	_, err := DecodeHeader([]byte{0x41, 0x00})
	if err == nil {
		t.Error("expected error for short header")
	}
}

func TestDecodeHeaderBadVersion(t *testing.T) {
	buf := make([]byte, HeaderSize)
	buf[0] = (StData << 4) | 0x02 // version 2
	_, err := DecodeHeader(buf)
	if err == nil {
		t.Error("expected error for version 2")
	}
}

// ---- Packet encode/decode ----

func TestPacketDataRoundTrip(t *testing.T) {
	payload := []byte("hello uTP world")
	pkt := Packet{
		Header: Header{
			Type:      StData,
			Version:   Version,
			Extension: ExtNone,
			ConnID:    42,
			Timestamp: 999,
			WndSize:   1048576,
			SeqNr:     5,
			AckNr:     4,
		},
		Payload: payload,
	}

	data := pkt.Encode()
	got, err := DecodePacket(data)
	if err != nil {
		t.Fatalf("DecodePacket: %v", err)
	}

	if got.Header.Type != StData {
		t.Errorf("Type = %d", got.Header.Type)
	}
	if got.Header.SeqNr != 5 {
		t.Errorf("SeqNr = %d", got.Header.SeqNr)
	}
	if !bytes.Equal(got.Payload, payload) {
		t.Errorf("Payload = %q, want %q", got.Payload, payload)
	}
}

func TestPacketSynNoPayload(t *testing.T) {
	pkt := Packet{
		Header: Header{
			Type:    StSyn,
			Version: Version,
			ConnID:  0x1234,
			SeqNr:   1,
		},
	}
	data := pkt.Encode()
	if len(data) != HeaderSize {
		t.Errorf("SYN packet len = %d, want %d", len(data), HeaderSize)
	}

	got, err := DecodePacket(data)
	if err != nil {
		t.Fatalf("DecodePacket: %v", err)
	}
	if got.Header.Type != StSyn {
		t.Errorf("Type = %d", got.Header.Type)
	}
	if len(got.Payload) != 0 {
		t.Errorf("Payload len = %d", len(got.Payload))
	}
}

func TestPacketWithSelectiveAck(t *testing.T) {
	sack := NewSelectiveAck(32)
	SetBit(sack, 0)  // ack_nr + 2
	SetBit(sack, 3)  // ack_nr + 5
	SetBit(sack, 31) // ack_nr + 33

	pkt := Packet{
		Header: Header{
			Type:      StState,
			Version:   Version,
			Extension: ExtSelectiveAck,
			ConnID:    100,
			AckNr:     10,
		},
		Extensions: []ExtensionHeader{
			{Type: ExtSelectiveAck, Payload: sack},
		},
	}

	data := pkt.Encode()
	got, err := DecodePacket(data)
	if err != nil {
		t.Fatalf("DecodePacket: %v", err)
	}

	if len(got.Extensions) != 1 {
		t.Fatalf("extensions count = %d", len(got.Extensions))
	}
	ext := got.Extensions[0]
	if ext.Type != ExtSelectiveAck {
		t.Errorf("ext type = %d", ext.Type)
	}
	if !GetBit(ext.Payload, 0) {
		t.Error("bit 0 not set")
	}
	if !GetBit(ext.Payload, 3) {
		t.Error("bit 3 not set")
	}
	if !GetBit(ext.Payload, 31) {
		t.Error("bit 31 not set")
	}
	if GetBit(ext.Payload, 1) {
		t.Error("bit 1 should not be set")
	}
}

func TestPacketExtensionTruncated(t *testing.T) {
	// Header says extension present, but no extension data follows.
	buf := make([]byte, HeaderSize)
	buf[0] = (StState << 4) | Version
	buf[1] = ExtSelectiveAck // extension present
	_, err := DecodePacket(buf)
	if err == nil {
		t.Error("expected error for truncated extension")
	}
}

func TestPacketExtensionPayloadTruncated(t *testing.T) {
	// Extension header says 4 bytes, but only 2 present.
	buf := make([]byte, HeaderSize+4)
	buf[0] = (StState << 4) | Version
	buf[1] = ExtSelectiveAck
	buf[HeaderSize] = ExtNone // next = none
	buf[HeaderSize+1] = 4    // len = 4, but only 2 bytes follow

	_, err := DecodePacket(buf)
	if err == nil {
		t.Error("expected error for truncated extension payload")
	}
}

// ---- Selective ACK ----

func TestSelectiveAckMinimumSize(t *testing.T) {
	sack := NewSelectiveAck(0)
	if len(sack) != 4 {
		t.Errorf("len = %d, want 4 (minimum 32 bits)", len(sack))
	}
}

func TestSelectiveAckRoundUp(t *testing.T) {
	// 33 bits needs 5 bytes → rounds to 8
	sack := NewSelectiveAck(33)
	if len(sack) != 8 {
		t.Errorf("len = %d, want 8", len(sack))
	}
}

func TestSelectiveAckBitOrder(t *testing.T) {
	// BEP 29: bit 0 of byte 0 = offset 0 (ack_nr+2),
	// bit 7 of byte 0 = offset 7 (ack_nr+9).
	sack := NewSelectiveAck(8)
	SetBit(sack, 0)
	if sack[0] != 0x01 {
		t.Errorf("byte[0] = 0x%02x after setting bit 0, want 0x01", sack[0])
	}
	SetBit(sack, 7)
	if sack[0] != 0x81 {
		t.Errorf("byte[0] = 0x%02x after setting bits 0+7, want 0x81", sack[0])
	}
}

func TestSelectiveAckSetGetBoundary(t *testing.T) {
	sack := NewSelectiveAck(32)

	// Out of range: no panic.
	SetBit(sack, -1)
	SetBit(sack, 32) // exactly out of range for 4 bytes
	if GetBit(sack, -1) {
		t.Error("negative offset should return false")
	}
	if GetBit(sack, 32) {
		t.Error("offset 32 should return false for 4-byte sack")
	}
}

func TestAckedPackets(t *testing.T) {
	sack := NewSelectiveAck(16)
	SetBit(sack, 0)
	SetBit(sack, 5)
	SetBit(sack, 15)

	offsets := AckedPackets(sack)
	want := []int{0, 5, 15}
	if len(offsets) != len(want) {
		t.Fatalf("AckedPackets returned %d offsets, want %d", len(offsets), len(want))
	}
	for i, w := range want {
		if offsets[i] != w {
			t.Errorf("offsets[%d] = %d, want %d", i, offsets[i], w)
		}
	}
}

// ---- String helpers ----

func TestTypeString(t *testing.T) {
	tests := []struct {
		typ  byte
		want string
	}{
		{StData, "ST_DATA"},
		{StFin, "ST_FIN"},
		{StState, "ST_STATE"},
		{StReset, "ST_RESET"},
		{StSyn, "ST_SYN"},
		{99, "ST_UNKNOWN(99)"},
	}
	for _, tc := range tests {
		if got := TypeString(tc.typ); got != tc.want {
			t.Errorf("TypeString(%d) = %q, want %q", tc.typ, got, tc.want)
		}
	}
}

func TestStateString(t *testing.T) {
	tests := []struct {
		state byte
		want  string
	}{
		{CsIdle, "CS_IDLE"},
		{CsSynSent, "CS_SYN_SENT"},
		{CsSynRecv, "CS_SYN_RECV"},
		{CsConnected, "CS_CONNECTED"},
		{CsFinSent, "CS_FIN_SENT"},
		{CsDestroy, "CS_DESTROY"},
		{99, "CS_UNKNOWN(99)"},
	}
	for _, tc := range tests {
		if got := StateString(tc.state); got != tc.want {
			t.Errorf("StateString(%d) = %q, want %q", tc.state, got, tc.want)
		}
	}
}
