package dht

import (
	"crypto/ed25519"
	"crypto/sha1"
	"fmt"
	"sync"
	"time"

	"github.com/ihvo/peer-pressure/bencode"
)

// BEP 44 constants.
const (
	maxValueSize = 1000 // max bencoded size of v field
	maxSaltSize  = 64   // max salt length
)

// Item represents a BEP 44 stored item (immutable or mutable).
type Item struct {
	Value   bencode.Value // the stored value (any bencoded type)
	Key     [32]byte      // ed25519 public key (mutable only, zero for immutable)
	Sig     [64]byte      // ed25519 signature (mutable only)
	Seq     int64         // sequence number (mutable only)
	Salt    string        // optional salt (mutable only)
	Mutable bool          // true if this is a mutable item
	Stored  time.Time     // when this item was stored
}

// Store holds BEP 44 items keyed by their target hash.
type Store struct {
	items map[[20]byte]*Item
	mu    sync.RWMutex
}

// NewStore creates an empty BEP 44 data store.
func NewStore() *Store {
	return &Store{items: make(map[[20]byte]*Item)}
}

// ImmutableTarget computes the target hash for an immutable item: SHA-1(v).
func ImmutableTarget(v bencode.Value) [20]byte {
	return sha1.Sum(bencode.Encode(v))
}

// MutableTarget computes the target hash for a mutable item: SHA-1(k + salt).
func MutableTarget(publicKey [32]byte, salt string) [20]byte {
	h := sha1.New()
	h.Write(publicKey[:])
	h.Write([]byte(salt))
	var target [20]byte
	copy(target[:], h.Sum(nil))
	return target
}

// Get retrieves an item by target hash. Returns nil if not found.
func (s *Store) Get(target [20]byte) *Item {
	s.mu.RLock()
	defer s.mu.RUnlock()
	item, ok := s.items[target]
	if !ok {
		return nil
	}
	cp := *item
	return &cp
}

// PutImmutable stores an immutable item. Verifies the value hash matches target.
func (s *Store) PutImmutable(target [20]byte, v bencode.Value) error {
	encoded := bencode.Encode(v)
	if len(encoded) > maxValueSize {
		return fmt.Errorf("value too big: %d bytes (max %d)", len(encoded), maxValueSize)
	}

	got := sha1.Sum(encoded)
	if got != target {
		return fmt.Errorf("hash mismatch: computed %x, target %x", got, target)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.items[target] = &Item{
		Value:  v,
		Stored: time.Now(),
	}
	return nil
}

// PutMutable stores a mutable item. Verifies signature and sequence number.
func (s *Store) PutMutable(key [32]byte, salt string, seq int64, sig [64]byte, v bencode.Value, cas *int64) error {
	if len(salt) > maxSaltSize {
		return fmt.Errorf("salt too big: %d bytes (max %d)", len(salt), maxSaltSize)
	}

	encoded := bencode.Encode(v)
	if len(encoded) > maxValueSize {
		return fmt.Errorf("value too big: %d bytes (max %d)", len(encoded), maxValueSize)
	}

	// Verify Ed25519 signature.
	signBuf := mutableSignBuffer(salt, seq, encoded)
	pubKey := ed25519.PublicKey(key[:])
	if !ed25519.Verify(pubKey, signBuf, sig[:]) {
		return fmt.Errorf("invalid signature")
	}

	target := MutableTarget(key, salt)

	s.mu.Lock()
	defer s.mu.Unlock()

	if existing, ok := s.items[target]; ok {
		// CAS check.
		if cas != nil && *cas != existing.Seq {
			return fmt.Errorf("CAS mismatch: stored seq=%d, cas=%d", existing.Seq, *cas)
		}
		// Sequence number must not decrease.
		if seq < existing.Seq {
			return fmt.Errorf("sequence number %d < stored %d", seq, existing.Seq)
		}
		// Same seq + same value: just reset timeout.
		if seq == existing.Seq {
			existing.Stored = time.Now()
			return nil
		}
	}

	s.items[target] = &Item{
		Value:   v,
		Key:     key,
		Sig:     sig,
		Seq:     seq,
		Salt:    salt,
		Mutable: true,
		Stored:  time.Now(),
	}
	return nil
}

// mutableSignBuffer constructs the buffer to sign/verify for mutable items.
// Format: [salt]3:seqi<n>e1:v<bencoded_value>
func mutableSignBuffer(salt string, seq int64, encodedValue []byte) []byte {
	var buf []byte
	if salt != "" {
		saltEncoded := bencode.Encode(bencode.String(salt))
		buf = append(buf, []byte("4:salt")...)
		buf = append(buf, saltEncoded...)
	}
	seqEncoded := bencode.Encode(bencode.Int(seq))
	buf = append(buf, []byte("3:seq")...)
	buf = append(buf, seqEncoded...)
	buf = append(buf, []byte("1:v")...)
	buf = append(buf, encodedValue...)
	return buf
}

// SignMutable signs a mutable item value for BEP 44 put.
func SignMutable(privateKey ed25519.PrivateKey, salt string, seq int64, v bencode.Value) [64]byte {
	encoded := bencode.Encode(v)
	buf := mutableSignBuffer(salt, seq, encoded)
	sigBytes := ed25519.Sign(privateKey, buf)
	var sig [64]byte
	copy(sig[:], sigBytes)
	return sig
}

// Prune removes items older than maxAge.
func (s *Store) Prune(maxAge time.Duration) int {
	cutoff := time.Now().Add(-maxAge)
	s.mu.Lock()
	defer s.mu.Unlock()
	removed := 0
	for k, item := range s.items {
		if item.Stored.Before(cutoff) {
			delete(s.items, k)
			removed++
		}
	}
	return removed
}
