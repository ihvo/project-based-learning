package dht

import (
	"crypto/ed25519"
	"crypto/sha1"
	"testing"
	"time"

	"github.com/ihvo/peer-pressure/bencode"
)

func TestImmutableTarget(t *testing.T) {
	v := bencode.String("Hello World!")
	target := ImmutableTarget(v)
	// SHA-1 of bencoded "12:Hello World!" = "e5f96f6f38320f0f33959cb4d3d656452117aadb"
	got := sha1.Sum(bencode.Encode(v))
	if target != got {
		t.Errorf("target mismatch: %x vs %x", target, got)
	}
}

func TestMutableTarget(t *testing.T) {
	var key [32]byte
	for i := range key {
		key[i] = byte(i)
	}
	target := MutableTarget(key, "")
	// SHA-1 of 32-byte key with no salt.
	h := sha1.New()
	h.Write(key[:])
	var want [20]byte
	copy(want[:], h.Sum(nil))
	if target != want {
		t.Errorf("target mismatch: %x vs %x", target, want)
	}
}

func TestMutableTargetWithSalt(t *testing.T) {
	var key [32]byte
	target1 := MutableTarget(key, "")
	target2 := MutableTarget(key, "foobar")
	if target1 == target2 {
		t.Error("different salts should produce different targets")
	}
}

func TestStoreImmutable(t *testing.T) {
	s := NewStore()
	v := bencode.String("test data")
	target := ImmutableTarget(v)

	if err := s.PutImmutable(target, v); err != nil {
		t.Fatalf("PutImmutable: %v", err)
	}

	got := s.Get(target)
	if got == nil {
		t.Fatal("Get returned nil")
	}
	if string(got.Value.(bencode.String)) != "test data" {
		t.Errorf("value = %q", got.Value)
	}
	if got.Mutable {
		t.Error("should not be mutable")
	}
}

func TestStoreImmutableHashMismatch(t *testing.T) {
	s := NewStore()
	v := bencode.String("test data")
	var badTarget [20]byte // all zeros — won't match
	if err := s.PutImmutable(badTarget, v); err == nil {
		t.Error("expected hash mismatch error")
	}
}

func TestStoreImmutableTooBig(t *testing.T) {
	s := NewStore()
	big := bencode.String(make([]byte, 1001))
	target := ImmutableTarget(big)
	if err := s.PutImmutable(target, big); err == nil {
		t.Error("expected value too big error")
	}
}

func TestStoreMutable(t *testing.T) {
	s := NewStore()
	pub, priv, _ := ed25519.GenerateKey(nil)

	v := bencode.String("hello mutable")
	var key [32]byte
	copy(key[:], pub)
	sig := SignMutable(priv, "", 1, v)

	if err := s.PutMutable(key, "", 1, sig, v, nil); err != nil {
		t.Fatalf("PutMutable: %v", err)
	}

	target := MutableTarget(key, "")
	got := s.Get(target)
	if got == nil {
		t.Fatal("Get returned nil")
	}
	if !got.Mutable {
		t.Error("should be mutable")
	}
	if got.Seq != 1 {
		t.Errorf("seq = %d, want 1", got.Seq)
	}
}

func TestStoreMutableUpdate(t *testing.T) {
	s := NewStore()
	pub, priv, _ := ed25519.GenerateKey(nil)
	var key [32]byte
	copy(key[:], pub)

	v1 := bencode.String("version 1")
	sig1 := SignMutable(priv, "", 1, v1)
	if err := s.PutMutable(key, "", 1, sig1, v1, nil); err != nil {
		t.Fatalf("put v1: %v", err)
	}

	v2 := bencode.String("version 2")
	sig2 := SignMutable(priv, "", 2, v2)
	if err := s.PutMutable(key, "", 2, sig2, v2, nil); err != nil {
		t.Fatalf("put v2: %v", err)
	}

	target := MutableTarget(key, "")
	got := s.Get(target)
	if string(got.Value.(bencode.String)) != "version 2" {
		t.Errorf("value = %q, want version 2", got.Value)
	}
}

func TestStoreMutableDowngrade(t *testing.T) {
	s := NewStore()
	pub, priv, _ := ed25519.GenerateKey(nil)
	var key [32]byte
	copy(key[:], pub)

	v1 := bencode.String("version 1")
	sig1 := SignMutable(priv, "", 5, v1)
	if err := s.PutMutable(key, "", 5, sig1, v1, nil); err != nil {
		t.Fatalf("put seq=5: %v", err)
	}

	v2 := bencode.String("old version")
	sig2 := SignMutable(priv, "", 3, v2)
	if err := s.PutMutable(key, "", 3, sig2, v2, nil); err == nil {
		t.Error("expected sequence downgrade rejection")
	}
}

func TestStoreMutableBadSignature(t *testing.T) {
	s := NewStore()
	pub, _, _ := ed25519.GenerateKey(nil)
	var key [32]byte
	copy(key[:], pub)

	v := bencode.String("tampered")
	var badSig [64]byte // all zeros — invalid signature
	if err := s.PutMutable(key, "", 1, badSig, v, nil); err == nil {
		t.Error("expected invalid signature error")
	}
}

func TestStoreMutableCAS(t *testing.T) {
	s := NewStore()
	pub, priv, _ := ed25519.GenerateKey(nil)
	var key [32]byte
	copy(key[:], pub)

	v1 := bencode.String("v1")
	sig1 := SignMutable(priv, "", 1, v1)
	s.PutMutable(key, "", 1, sig1, v1, nil)

	// CAS match: stored seq=1, cas=1 → should succeed.
	v2 := bencode.String("v2")
	sig2 := SignMutable(priv, "", 2, v2)
	cas := int64(1)
	if err := s.PutMutable(key, "", 2, sig2, v2, &cas); err != nil {
		t.Fatalf("CAS match failed: %v", err)
	}

	// CAS mismatch: stored seq=2, cas=1 → should fail.
	v3 := bencode.String("v3")
	sig3 := SignMutable(priv, "", 3, v3)
	cas = int64(1)
	if err := s.PutMutable(key, "", 3, sig3, v3, &cas); err == nil {
		t.Error("expected CAS mismatch error")
	}
}

func TestStoreMutableWithSalt(t *testing.T) {
	s := NewStore()
	pub, priv, _ := ed25519.GenerateKey(nil)
	var key [32]byte
	copy(key[:], pub)

	v := bencode.String("salted item")
	sig := SignMutable(priv, "mysalt", 1, v)
	if err := s.PutMutable(key, "mysalt", 1, sig, v, nil); err != nil {
		t.Fatalf("PutMutable with salt: %v", err)
	}

	target := MutableTarget(key, "mysalt")
	got := s.Get(target)
	if got == nil {
		t.Fatal("Get returned nil")
	}
	if got.Salt != "mysalt" {
		t.Errorf("salt = %q, want mysalt", got.Salt)
	}
}

func TestStorePrune(t *testing.T) {
	s := NewStore()
	v := bencode.String("ephemeral")
	target := ImmutableTarget(v)
	s.PutImmutable(target, v)

	// Artificially age the item.
	s.mu.Lock()
	s.items[target].Stored = time.Now().Add(-2 * time.Hour)
	s.mu.Unlock()

	removed := s.Prune(1 * time.Hour)
	if removed != 1 {
		t.Errorf("pruned %d items, want 1", removed)
	}
	if s.Get(target) != nil {
		t.Error("item should have been pruned")
	}
}

func TestStoreGetNotFound(t *testing.T) {
	s := NewStore()
	var target [20]byte
	if s.Get(target) != nil {
		t.Error("expected nil for missing item")
	}
}

func TestSignMutableRoundTrip(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	v := bencode.String("round trip test")
	sig := SignMutable(priv, "salt", 42, v)

	// Verify manually.
	encoded := bencode.Encode(v)
	buf := mutableSignBuffer("salt", 42, encoded)
	if !ed25519.Verify(pub, buf, sig[:]) {
		t.Error("signature verification failed")
	}
}
