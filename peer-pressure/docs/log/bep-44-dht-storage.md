# BEP 44: Storing Arbitrary Data in the DHT

## What It Does

BEP 44 turns the DHT into a general-purpose key/value store beyond just
peer lookups. Two storage modes:

### Immutable Items
- Key = SHA-1(bencoded value)
- Anyone can write, nobody can update
- Perfect for content-addressed data

### Mutable Items
- Key = SHA-1(ed25519_public_key + salt)
- Only the key holder can update (must sign with private key)
- Sequence numbers prevent replay attacks (monotonically increasing)
- Optional salt allows one key to publish multiple independent items
- CAS (compare-and-swap) prevents race conditions in concurrent updates

### Message Flow

```
Client                     DHT Node
  |-- get(target) ----------->|
  |<-- {v, token, nodes} -----|
  |                            |
  |-- put(v, token, [k,sig]) ->|
  |<-- {id} ------------------|
```

The `token` works like announce_peer — proves we recently contacted this node.

### Signature Format

For mutable items, the signed buffer is constructed as:

```
[4:salt<len>:<salt>]3:seq<bencoded_seq>1:v<bencoded_value>
```

Salt is only included when non-empty. This is raw bencode key-value pairs
concatenated, not a bencoded dict — the order is fixed.

### What We Implemented

1. **`Store`** — thread-safe in-memory storage for both item types
2. **`ImmutableTarget`/`MutableTarget`** — target hash computation
3. **`PutImmutable`** — stores with hash verification
4. **`PutMutable`** — stores with Ed25519 signature verification, sequence
   number enforcement, CAS support
5. **`SignMutable`** — constructs the sign buffer and signs with Ed25519
6. **`Prune`** — removes expired items (for memory management)

## Go Idioms

### Ed25519 in the Standard Library

```go
import "crypto/ed25519"

pub, priv, _ := ed25519.GenerateKey(nil)
sig := ed25519.Sign(priv, message)
valid := ed25519.Verify(pub, message, sig)
```

Go 1.13+ includes Ed25519 in the standard library. Passing `nil` to
`GenerateKey` uses `crypto/rand` as the entropy source. The key types are
just byte slices: `PublicKey = []byte` (32 bytes), `PrivateKey = []byte`
(64 bytes, contains both seed and public key).

### Pointer as Optional Parameter

```go
func (s *Store) PutMutable(..., cas *int64) error {
    if cas != nil && *cas != existing.Seq {
        return fmt.Errorf("CAS mismatch")
    }
}
```

The BEP 44 CAS field is optional. Using `*int64` distinguishes "not provided"
(`nil`) from "provided as zero" (`&zero`). This is cleaner than a separate
`HasCAS bool` field and matches the optional semantics precisely.

### RWMutex for Read-Heavy, Write-Rare

```go
type Store struct {
    items map[[20]byte]*Item
    mu    sync.RWMutex
}

func (s *Store) Get(...) *Item {
    s.mu.RLock()
    defer s.mu.RUnlock()
    // ...
}
```

DHT gets outnumber puts significantly. `RWMutex` allows concurrent reads
while still serializing writes. The `Get` method returns a copy (`cp := *item`)
to prevent the caller from mutating store internals after releasing the lock.

### Time-Based Expiration

```go
func (s *Store) Prune(maxAge time.Duration) int {
    cutoff := time.Now().Add(-maxAge)
    for k, item := range s.items {
        if item.Stored.Before(cutoff) {
            delete(s.items, k)
        }
    }
}
```

Go allows `delete()` during `range` iteration over maps — the spec explicitly
permits this. The `Before` method on `time.Time` handles timezone-aware
comparison correctly.
