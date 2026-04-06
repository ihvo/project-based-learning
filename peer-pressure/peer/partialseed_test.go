package peer

import (
	"testing"

	"github.com/ihvo/peer-pressure/bencode"
)

func TestEncodeUploadOnlyTrue(t *testing.T) {
	data := EncodeUploadOnly(true)
	val, err := bencode.Decode(data)
	if err != nil {
		t.Fatal(err)
	}
	d, ok := val.(bencode.Dict)
	if !ok {
		t.Fatal("expected dict")
	}
	if v, ok := d["upload_only"].(bencode.Int); !ok || v != 1 {
		t.Errorf("got %v, want 1", d["upload_only"])
	}
}

func TestEncodeUploadOnlyFalse(t *testing.T) {
	data := EncodeUploadOnly(false)
	val, err := bencode.Decode(data)
	if err != nil {
		t.Fatal(err)
	}
	d, ok := val.(bencode.Dict)
	if !ok {
		t.Fatal("expected dict")
	}
	if v, ok := d["upload_only"].(bencode.Int); !ok || v != 0 {
		t.Errorf("got %v, want 0", d["upload_only"])
	}
}

func TestDecodeUploadOnlyDictTrue(t *testing.T) {
	data := bencode.Encode(bencode.Dict{"upload_only": bencode.Int(1)})
	got, err := DecodeUploadOnly(data)
	if err != nil {
		t.Fatal(err)
	}
	if !got {
		t.Error("expected true")
	}
}

func TestDecodeUploadOnlyDictFalse(t *testing.T) {
	data := bencode.Encode(bencode.Dict{"upload_only": bencode.Int(0)})
	got, err := DecodeUploadOnly(data)
	if err != nil {
		t.Fatal(err)
	}
	if got {
		t.Error("expected false")
	}
}

func TestDecodeUploadOnlyBareIntTrue(t *testing.T) {
	data := bencode.Encode(bencode.Int(1))
	got, err := DecodeUploadOnly(data)
	if err != nil {
		t.Fatal(err)
	}
	if !got {
		t.Error("expected true")
	}
}

func TestDecodeUploadOnlyBareIntFalse(t *testing.T) {
	data := bencode.Encode(bencode.Int(0))
	got, err := DecodeUploadOnly(data)
	if err != nil {
		t.Fatal(err)
	}
	if got {
		t.Error("expected false")
	}
}

func TestDecodeUploadOnlyEmpty(t *testing.T) {
	_, err := DecodeUploadOnly([]byte{})
	if err == nil {
		t.Error("expected error for empty payload")
	}
}

func TestDecodeUploadOnlyInvalid(t *testing.T) {
	_, err := DecodeUploadOnly([]byte{0xFF, 0xFF})
	if err == nil {
		t.Error("expected error for invalid payload")
	}
}

func TestUploadOnlyRoundTrip(t *testing.T) {
	for _, want := range []bool{true, false} {
		data := EncodeUploadOnly(want)
		got, err := DecodeUploadOnly(data)
		if err != nil {
			t.Fatalf("roundtrip(%v): %v", want, err)
		}
		if got != want {
			t.Errorf("roundtrip(%v) = %v", want, got)
		}
	}
}

func TestNewUploadOnlyMsg(t *testing.T) {
	msg := NewUploadOnlyMsg(3, true)
	if msg.ID != MsgExtended {
		t.Errorf("ID = %d, want %d", msg.ID, MsgExtended)
	}
	if msg.Payload[0] != 3 {
		t.Errorf("sub-ID = %d, want 3", msg.Payload[0])
	}
	got, err := DecodeUploadOnly(msg.Payload[1:])
	if err != nil {
		t.Fatal(err)
	}
	if !got {
		t.Error("expected upload_only=true in message")
	}
}

func TestExtHandshakeUploadOnlyTrue(t *testing.T) {
	d := bencode.Dict{
		"m":           bencode.Dict{"upload_only": bencode.Int(3)},
		"upload_only": bencode.Int(1),
		"v":           bencode.String("Test"),
	}
	data := bencode.Encode(d)
	payload := make([]byte, 1+len(data))
	payload[0] = 0
	copy(payload[1:], data)

	hs, err := ParseExtHandshake(payload)
	if err != nil {
		t.Fatal(err)
	}
	if !hs.UploadOnly {
		t.Error("expected UploadOnly=true")
	}
	if hs.M["upload_only"] != 3 {
		t.Errorf("m[upload_only] = %d, want 3", hs.M["upload_only"])
	}
}

func TestExtHandshakeUploadOnlyAbsent(t *testing.T) {
	d := bencode.Dict{
		"m": bencode.Dict{"ut_metadata": bencode.Int(1)},
		"v": bencode.String("Test"),
	}
	data := bencode.Encode(d)
	payload := make([]byte, 1+len(data))
	payload[0] = 0
	copy(payload[1:], data)

	hs, err := ParseExtHandshake(payload)
	if err != nil {
		t.Fatal(err)
	}
	if hs.UploadOnly {
		t.Error("expected UploadOnly=false when absent")
	}
}

func TestExtHandshakeUploadOnlyInMOnly(t *testing.T) {
	d := bencode.Dict{
		"m": bencode.Dict{"upload_only": bencode.Int(3)},
		"v": bencode.String("Test"),
	}
	data := bencode.Encode(d)
	payload := make([]byte, 1+len(data))
	payload[0] = 0
	copy(payload[1:], data)

	hs, err := ParseExtHandshake(payload)
	if err != nil {
		t.Fatal(err)
	}
	if hs.UploadOnly {
		t.Error("expected UploadOnly=false when only in m dict (not top-level)")
	}
	if hs.M["upload_only"] != 3 {
		t.Errorf("m[upload_only] = %d, want 3", hs.M["upload_only"])
	}
}
