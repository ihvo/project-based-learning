# BEP 46: Updateable Torrents via DHT

## What It Does

BEP 46 builds on BEP 44 (DHT mutable storage) to create decentralized,
updateable torrent feeds. Instead of pointing to a fixed infohash, a
publisher uses their Ed25519 key pair to publish a mutable DHT item
containing the latest torrent's infohash.

### How It Works

1. **Publisher** generates an Ed25519 key pair
2. Stores `{"ih": <20-byte infohash>}` as a BEP 44 mutable item
3. When the torrent updates, puts a new infohash with incremented seq
4. **Consumer** polls the DHT target (SHA-1 of public key + salt)
5. Gets the latest infohash and downloads the torrent

### Magnet Link Format

```
magnet:?xs=urn:btpk:<public_key_hex>&s=<salt_hex>
```

- Uses `xs` (exact source) instead of `xt` (exact topic)
- `urn:btpk:` prefix for public key scheme (not `urn:btih:`)
- Salt is optional, hex-encoded

### Target ID Computation

```
target = SHA-1(public_key || salt)
```

This is identical to BEP 44's `MutableTarget()` — BEP 46 just defines the
value format (`{"ih": ...}`) and the magnet link format.

### Real-World Use Case

Archive.org could publish database dumps as torrents. Consumers subscribe
to the publisher's key — when a new dump is available, the DHT entry
updates automatically. No HTTP feed server needed.

## Go Idioms

### Thin Layer Pattern

```go
func (l *UpdateableLink) TargetID() [20]byte {
    h := sha1.New()
    h.Write(l.PublicKey[:])
    h.Write([]byte(l.Salt))
    var target [20]byte
    copy(target[:], h.Sum(nil))
    return target
}
```

BEP 46 is a thin protocol layer — it reuses BEP 44's storage mechanism
entirely. The implementation is just: magnet parsing + target computation.
No new wire protocol, no new DHT queries. This demonstrates good protocol
design: building higher-level features from composable primitives.

### Hex-Encoded Salt in URLs

```go
if s := params.Get("s"); s != "" {
    saltBytes, err := hex.DecodeString(s)
    link.Salt = string(saltBytes)
}
```

The BEP 46 spec shows salt as hex in the magnet link (`s=6e` for "n"),
even though internally it's arbitrary bytes. This ensures URL-safe encoding
without percent-escaping. The hex→string→hex round-trip is safe because
Go strings can hold arbitrary bytes (they're just byte sequences, not
necessarily valid UTF-8).

### Test Vectors as Documentation

```go
const testPubKeyHex = "8543d3e6..."

func TestUpdateableTargetID(t *testing.T) {
    // BEP 46 test vector 1: no salt.
    wantHex := "cc3f9d90b572172053626f9980ce261a850d050b"
```

The BEP 46 spec provides two test vectors. Including the vector source in
comments (`BEP 46 test vector 1`) connects the test to the spec,
making it clear this isn't an arbitrary test but a conformance check.
