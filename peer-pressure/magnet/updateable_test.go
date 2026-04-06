package magnet

import (
	"encoding/hex"
	"testing"
)

const testPubKeyHex = "8543d3e6115f0f98c944077a4493dcd543e49c739fd998550a1f614ab36ed63e"

func TestUpdateableTargetID(t *testing.T) {
	// BEP 46 test vector 1: no salt.
	keyBytes, _ := hex.DecodeString(testPubKeyHex)
	var key [32]byte
	copy(key[:], keyBytes)

	link := &UpdateableLink{PublicKey: key}
	target := link.TargetID()

	wantHex := "cc3f9d90b572172053626f9980ce261a850d050b"
	gotHex := hex.EncodeToString(target[:])
	if gotHex != wantHex {
		t.Errorf("target = %s, want %s", gotHex, wantHex)
	}
}

func TestUpdateableTargetIDWithSalt(t *testing.T) {
	// BEP 46 test vector 2: salt = "n" (hex 6e).
	keyBytes, _ := hex.DecodeString(testPubKeyHex)
	var key [32]byte
	copy(key[:], keyBytes)

	link := &UpdateableLink{PublicKey: key, Salt: "n"}
	target := link.TargetID()

	wantHex := "59ee7c2cb9b4f7eb1986ee2d18fd2fdb8a56554f"
	gotHex := hex.EncodeToString(target[:])
	if gotHex != wantHex {
		t.Errorf("target = %s, want %s", gotHex, wantHex)
	}
}

func TestParseUpdateable(t *testing.T) {
	uri := "magnet:?xs=urn:btpk:" + testPubKeyHex + "&dn=test%20feed"
	link, err := ParseUpdateable(uri)
	if err != nil {
		t.Fatalf("ParseUpdateable: %v", err)
	}

	wantKey, _ := hex.DecodeString(testPubKeyHex)
	for i, b := range link.PublicKey {
		if b != wantKey[i] {
			t.Errorf("key byte %d: got 0x%02x, want 0x%02x", i, b, wantKey[i])
		}
	}

	if link.Name != "test feed" {
		t.Errorf("name = %q, want 'test feed'", link.Name)
	}
}

func TestParseUpdateableWithSalt(t *testing.T) {
	// Salt "n" = hex "6e".
	uri := "magnet:?xs=urn:btpk:" + testPubKeyHex + "&s=6e"
	link, err := ParseUpdateable(uri)
	if err != nil {
		t.Fatalf("ParseUpdateable: %v", err)
	}

	if link.Salt != "n" {
		t.Errorf("salt = %q, want 'n'", link.Salt)
	}
}

func TestParseUpdateableMissingXS(t *testing.T) {
	uri := "magnet:?dn=test"
	_, err := ParseUpdateable(uri)
	if err == nil {
		t.Error("expected error for missing xs")
	}
}

func TestParseUpdateableBadScheme(t *testing.T) {
	uri := "magnet:?xs=urn:btih:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	_, err := ParseUpdateable(uri)
	if err == nil {
		t.Error("expected error for btih scheme in xs")
	}
}

func TestParseUpdateableBadKeyLength(t *testing.T) {
	uri := "magnet:?xs=urn:btpk:abcd"
	_, err := ParseUpdateable(uri)
	if err == nil {
		t.Error("expected error for short public key")
	}
}

func TestUpdateableRoundTrip(t *testing.T) {
	keyBytes, _ := hex.DecodeString(testPubKeyHex)
	var key [32]byte
	copy(key[:], keyBytes)

	link := &UpdateableLink{PublicKey: key, Salt: "foobar", Name: "my feed"}
	uri := link.String()

	got, err := ParseUpdateable(uri)
	if err != nil {
		t.Fatalf("round-trip parse: %v", err)
	}
	if got.PublicKey != link.PublicKey {
		t.Error("public key mismatch")
	}
	if got.Salt != link.Salt {
		t.Errorf("salt = %q, want %q", got.Salt, link.Salt)
	}
	if got.Name != link.Name {
		t.Errorf("name = %q, want %q", got.Name, link.Name)
	}
}
