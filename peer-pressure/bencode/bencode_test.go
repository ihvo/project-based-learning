package bencode

import (
	"bytes"
	"testing"
)

// --- String tests ---

func TestDecodeString(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"simple", "4:spam", "spam"},
		{"empty", "0:", ""},
		{"with spaces", "11:hello world", "hello world"},
		{"digits in string", "3:123", "123"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			val, err := Decode([]byte(tt.input))
			if err != nil {
				t.Fatalf("Decode(%q) error: %v", tt.input, err)
			}
			s, ok := val.(String)
			if !ok {
				t.Fatalf("expected String, got %T", val)
			}
			if string(s) != tt.want {
				t.Errorf("got %q, want %q", string(s), tt.want)
			}
		})
	}
}

func TestDecodeStringBinary(t *testing.T) {
	// Bencode strings are byte strings — they can hold arbitrary binary data
	input := []byte{'3', ':', 0x00, 0xFF, 0x42}
	val, err := Decode(input)
	if err != nil {
		t.Fatalf("Decode error: %v", err)
	}
	s := val.(String)
	want := []byte{0x00, 0xFF, 0x42}
	if !bytes.Equal([]byte(s), want) {
		t.Errorf("got %v, want %v", []byte(s), want)
	}
}

func TestEncodeString(t *testing.T) {
	got := Encode(String("spam"))
	want := []byte("4:spam")
	if !bytes.Equal(got, want) {
		t.Errorf("Encode(String(\"spam\")) = %q, want %q", got, want)
	}
}

func TestEncodeEmptyString(t *testing.T) {
	got := Encode(String(""))
	want := []byte("0:")
	if !bytes.Equal(got, want) {
		t.Errorf("got %q, want %q", got, want)
	}
}

// --- Integer tests ---

func TestDecodeInt(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  int64
	}{
		{"positive", "i42e", 42},
		{"zero", "i0e", 0},
		{"negative", "i-5e", -5},
		{"large", "i9999999999e", 9999999999},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			val, err := Decode([]byte(tt.input))
			if err != nil {
				t.Fatalf("Decode(%q) error: %v", tt.input, err)
			}
			n, ok := val.(Int)
			if !ok {
				t.Fatalf("expected Int, got %T", val)
			}
			if int64(n) != tt.want {
				t.Errorf("got %d, want %d", n, tt.want)
			}
		})
	}
}

func TestDecodeIntInvalid(t *testing.T) {
	invalid := []struct {
		name  string
		input string
	}{
		{"leading zero", "i03e"},
		{"negative zero", "i-0e"},
		{"empty", "ie"},
		{"no closing e", "i42"},
	}

	for _, tt := range invalid {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Decode([]byte(tt.input))
			if err == nil {
				t.Errorf("Decode(%q) should have returned error", tt.input)
			}
		})
	}
}

func TestEncodeInt(t *testing.T) {
	tests := []struct {
		input Int
		want  string
	}{
		{42, "i42e"},
		{0, "i0e"},
		{-5, "i-5e"},
	}

	for _, tt := range tests {
		got := Encode(tt.input)
		if string(got) != tt.want {
			t.Errorf("Encode(Int(%d)) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

// --- List tests ---

func TestDecodeList(t *testing.T) {
	// l4:spami42ee → ["spam", 42]
	val, err := Decode([]byte("l4:spami42ee"))
	if err != nil {
		t.Fatalf("error: %v", err)
	}

	lst, ok := val.(List)
	if !ok {
		t.Fatalf("expected List, got %T", val)
	}
	if len(lst) != 2 {
		t.Fatalf("got %d items, want 2", len(lst))
	}

	if string(lst[0].(String)) != "spam" {
		t.Errorf("lst[0] = %q, want \"spam\"", lst[0])
	}
	if int64(lst[1].(Int)) != 42 {
		t.Errorf("lst[1] = %v, want 42", lst[1])
	}
}

func TestDecodeEmptyList(t *testing.T) {
	val, err := Decode([]byte("le"))
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	lst := val.(List)
	if len(lst) != 0 {
		t.Errorf("expected empty list, got %d items", len(lst))
	}
}

func TestDecodeNestedList(t *testing.T) {
	// ll4:spamee → [["spam"]]
	val, err := Decode([]byte("ll4:spamee"))
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	outer := val.(List)
	inner := outer[0].(List)
	if string(inner[0].(String)) != "spam" {
		t.Errorf("nested value = %q, want \"spam\"", inner[0])
	}
}

func TestEncodeList(t *testing.T) {
	v := List{String("spam"), Int(42)}
	got := Encode(v)
	want := []byte("l4:spami42ee")
	if !bytes.Equal(got, want) {
		t.Errorf("got %q, want %q", got, want)
	}
}

// --- Dict tests ---

func TestDecodeDict(t *testing.T) {
	// d3:cow3:moo4:spam4:eggse → {"cow": "moo", "spam": "eggs"}
	val, err := Decode([]byte("d3:cow3:moo4:spam4:eggse"))
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	d, ok := val.(Dict)
	if !ok {
		t.Fatalf("expected Dict, got %T", val)
	}
	if string(d["cow"].(String)) != "moo" {
		t.Errorf("cow = %q, want \"moo\"", d["cow"])
	}
	if string(d["spam"].(String)) != "eggs" {
		t.Errorf("spam = %q, want \"eggs\"", d["spam"])
	}
}

func TestDecodeEmptyDict(t *testing.T) {
	val, err := Decode([]byte("de"))
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	d := val.(Dict)
	if len(d) != 0 {
		t.Errorf("expected empty dict, got %d entries", len(d))
	}
}

func TestDecodeDictUnsortedKeys(t *testing.T) {
	// Keys "b" then "a" — out of order, should be rejected
	_, err := Decode([]byte("d1:bi1e1:ai2ee"))
	if err == nil {
		t.Error("expected error for unsorted dict keys")
	}
}

func TestDecodeDictDuplicateKeys(t *testing.T) {
	// Key "a" appears twice — second "a" <= first "a", so sorted-order check catches it
	_, err := Decode([]byte("d1:ai1e1:ai2ee"))
	if err == nil {
		t.Error("expected error for duplicate dict keys")
	}
}

func TestEncodeDictSortsKeys(t *testing.T) {
	// Even if we insert keys out of order, encoding must sort them
	d := Dict{
		"spam": String("eggs"),
		"cow":  String("moo"),
	}
	got := Encode(d)
	want := []byte("d3:cow3:moo4:spam4:eggse")
	if !bytes.Equal(got, want) {
		t.Errorf("got %q, want %q", got, want)
	}
}

// --- Round-trip tests ---

func TestRoundTrip(t *testing.T) {
	// A complex nested structure typical of a .torrent file
	original := Dict{
		"announce": String("http://tracker.example.com/announce"),
		"info": Dict{
			"name":         String("example.txt"),
			"piece length": Int(262144),
			"pieces":       String([]byte{0xAA, 0xBB, 0xCC}),
		},
		"creation date": Int(1234567890),
		"url-list": List{
			String("http://mirror1.example.com"),
			String("http://mirror2.example.com"),
		},
	}

	encoded := Encode(original)
	decoded, err := Decode(encoded)
	if err != nil {
		t.Fatalf("round-trip decode error: %v", err)
	}

	// Re-encode and compare bytes — if deterministic, they must match
	reencoded := Encode(decoded)
	if !bytes.Equal(encoded, reencoded) {
		t.Errorf("round-trip mismatch:\n  first:  %q\n  second: %q", encoded, reencoded)
	}
}

// --- Error cases ---

func TestDecodeTrailingData(t *testing.T) {
	_, err := Decode([]byte("i42eXXX"))
	if err == nil {
		t.Error("expected error for trailing data")
	}
}

func TestDecodeEmptyInput(t *testing.T) {
	_, err := Decode([]byte{})
	if err == nil {
		t.Error("expected error for empty input")
	}
}

func TestDecodeInvalidPrefix(t *testing.T) {
	_, err := Decode([]byte("x"))
	if err == nil {
		t.Error("expected error for invalid prefix byte")
	}
}

func TestDecodeStringTruncated(t *testing.T) {
	// Says 10 bytes but only 4 follow
	_, err := Decode([]byte("10:spam"))
	if err == nil {
		t.Error("expected error for truncated string")
	}
}
