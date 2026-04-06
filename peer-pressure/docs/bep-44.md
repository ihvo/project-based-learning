# BEP 44 — Storing Arbitrary Data in the DHT

> Extends the DHT beyond peer discovery into a general-purpose distributed
> key-value store for small payloads, enabling higher-level protocols like
> BEP 46 (updateable torrents) and BEP 51 (infohash indexing).

Reference: <https://www.bittorrent.org/beps/bep_0044.html>

---

## 1. Summary

BEP 44 adds two new KRPC query types — `put` and `get` — to the DHT. These
let any node store and retrieve small blobs (≤ 1000 bytes of bencoded data).

There are two item types:

| Type | Key derivation | Authentication | Mutability |
|-----------|-------------------------------|----------------|------------|
| Immutable | `SHA-1(bencode(value))` | None — the hash *is* the proof | Write-once |
| Mutable | `SHA-1(public_key + salt)` | Ed25519 signature over value + seq + salt | Update via incrementing `seq` |

Immutable items are content-addressed: anyone can store them, and the key is
the hash of the data itself. Mutable items are identity-addressed: only the
holder of the Ed25519 private key can update the value.

This is the foundation for BEP 46 (updateable torrent feeds) and any
application that needs a small, authenticated, decentralised data store.

---

## 2. Protocol Specification

### 2.1 Immutable Items

#### Storage key

```
target = SHA-1(bencode(v))
```

where `v` is the bencoded value being stored. The bencoded form must be a
single bencoded element (string, integer, list, or dictionary).

#### `get` query

```
Request:
  { "t": <txn_id>,
    "y": "q",
    "q": "get",
    "a": { "id": <20-byte node ID>,
           "target": <20-byte SHA-1 target> } }

Response:
  { "t": <txn_id>,
    "y": "r",
    "r": { "id": <20-byte node ID>,
           "v": <bencoded value>,
           "token": <write token>,
           "nodes": <compact node info> } }
```

- `token` is the same write token used by `announce_peer` — it proves the
  requester recently contacted this node (prevents store-flooding).
- `nodes` contains the K closest nodes the responder knows about, enabling
  iterative lookup (same as `find_node`).
- If the node doesn't have the item, `v` is omitted and only `token` + `nodes`
  are returned.

#### `put` query (immutable)

```
Request:
  { "t": <txn_id>,
    "y": "q",
    "q": "put",
    "a": { "id": <20-byte node ID>,
           "v": <bencoded value>,
           "token": <write token> } }

Response:
  { "t": <txn_id>,
    "y": "r",
    "r": { "id": <20-byte node ID> } }
```

- `token` must match a token previously received from a `get` response from
  this node.
- The receiving node computes `SHA-1(bencode(v))` and stores the item keyed
  by that hash.
- Maximum size of `v` when bencoded: **1000 bytes**.

### 2.2 Mutable Items

#### Storage key

```
target = SHA-1(public_key)           // when salt is empty
target = SHA-1(public_key + salt)    // when salt is provided
```

- `public_key` is the 32-byte Ed25519 public key.
- `salt` is an optional byte string (max 64 bytes) that allows one key pair
  to publish multiple independent items.

#### Signature computation

The signature covers a bencoded message constructed as:

```
if salt is empty:
    sig_input = "3:seqi" + str(seq) + "e1:v" + bencode(v)
else:
    sig_input = "4:salt" + str(len(salt)) + ":" + salt +
                "3:seqi" + str(seq) + "e1:v" + bencode(v)
```

More precisely, the signed buffer is the concatenation of bencoded key-value
pairs (without the outer `d...e` dictionary wrapper):

```
if salt:
    buf = bencode_kv("salt", salt) + bencode_kv("seq", seq) + bencode_kv("v", v)
else:
    buf = bencode_kv("seq", seq) + bencode_kv("v", v)
```

where `bencode_kv(key, val)` produces `<len(key)>:<key>` followed by the
bencoded form of `val`.

The signature is: `Ed25519_Sign(private_key, buf)`.

#### `get` query (mutable)

Same request format as immutable — the caller just provides the `target`.
The response includes additional fields:

```
Response:
  { "t": <txn_id>,
    "y": "r",
    "r": { "id": <20-byte node ID>,
           "v": <bencoded value>,
           "k": <32-byte Ed25519 public key>,
           "sig": <64-byte Ed25519 signature>,
           "seq": <integer, sequence number>,
           "token": <write token>,
           "nodes": <compact node info> } }
```

- `seq` is a monotonically increasing integer. Higher `seq` means newer.
- The caller **must** verify the signature before trusting the value.
- If the node has no item for the target, `v`/`k`/`sig`/`seq` are omitted.

The `get` request may include an optional `seq` field:

```
"a": { "id": ..., "target": ..., "seq": <min_seq> }
```

If the stored item's `seq` is ≤ the requested `seq`, the node omits `v` from
the response (the caller already has an up-to-date or newer copy). The `k`,
`sig`, `seq`, and `token` fields are still returned.

#### `put` query (mutable)

```
Request:
  { "t": <txn_id>,
    "y": "q",
    "q": "put",
    "a": { "id": <20-byte node ID>,
           "v": <bencoded value>,
           "k": <32-byte Ed25519 public key>,
           "sig": <64-byte Ed25519 signature>,
           "seq": <integer>,
           "salt": <byte string, optional>,
           "cas": <integer, optional>,
           "token": <write token> } }
```

- `cas` (compare-and-swap): if present, the node only accepts the update if the
  currently stored `seq` equals `cas`. Returns error code 301
  (`"CAS mismatch"`) if the stored seq ≠ cas.
- Receiving node validates:
  1. `token` is valid (same mechanism as `announce_peer`)
  2. Verify Ed25519 signature against `k` over the constructed `sig_input`
  3. If an item already exists for this target, the new `seq` must be strictly
     greater than the stored `seq`
  4. `bencode(v)` ≤ 1000 bytes
  5. `salt` ≤ 64 bytes (if present)
  6. If `cas` is present, stored `seq` must equal `cas`

### 2.3 Error Codes

| Code | Description |
|------|-------------|
| 205 | Message (`v`) too large (> 1000 bytes bencoded) |
| 206 | Invalid signature |
| 207 | Salt too large (> 64 bytes) |
| 301 | CAS mismatch (stored seq ≠ expected cas) |
| 302 | Sequence number less than or equal to current |

### 2.4 Storage Semantics

- Nodes store items in memory (or optionally on disk) keyed by the 20-byte
  target hash.
- Items should have a TTL — BEP 44 doesn't specify a mandatory TTL, but
  implementations commonly use **2 hours** and require periodic re-puts.
- Nodes should limit total storage to prevent memory exhaustion (e.g. cap at
  10,000 items, evict LRU).
- The iterative lookup for `get` and `put` uses the same Kademlia closest-node
  lookup as `find_node` / `get_peers`.

### 2.5 Iterative Put/Get Flow

**Get:**
1. Compute `target`.
2. Find the K closest nodes to `target` using iterative `find_node`.
3. Send `get` to each of those K nodes.
4. Collect responses. For mutable items, keep the response with the highest
   `seq` (after verifying signature).
5. Return the value (and token set for a subsequent put).

**Put:**
1. Perform a `get` first to collect tokens from the K closest nodes.
2. Send `put` to each node, using the token received from that node.
3. A put is considered successful if a majority of the K closest nodes accept.

---

## 3. Implementation Plan

### 3.1 Package placement

All BEP 44 code lives in the existing `dht/` package since it extends KRPC.

### 3.2 New files

| File | Purpose |
|------|---------|
| `dht/store.go` | Item storage — in-memory store with TTL and LRU eviction |
| `dht/store_test.go` | Unit tests for storage |
| `dht/bep44.go` | Put/Get operations, signature construction/verification, iterative flows |
| `dht/bep44_test.go` | Unit and integration tests for BEP 44 |

### 3.3 Modified files

| File | Changes |
|------|---------|
| `dht/krpc.go` | Add `"get"` and `"put"` to message dispatch; add encoding helpers for BEP 44 fields |
| `dht/node.go` | Wire `get`/`put` query handlers into `Transport.Listen` handler; expose `DHT.Get` and `DHT.Put` methods |

### 3.4 Key types

```go
// dht/store.go

// Item represents a stored BEP 44 DHT item (immutable or mutable).
type Item struct {
    Value     bencode.Value // bencoded payload (≤ 1000 bytes encoded)
    Key       [32]byte      // Ed25519 public key (zero for immutable)
    Signature [64]byte      // Ed25519 signature (zero for immutable)
    Seq       int64         // sequence number (0 for immutable)
    Salt      []byte        // optional salt (mutable only, ≤ 64 bytes)
    Created   time.Time     // for TTL expiration
}

// Store is an in-memory BEP 44 item store with TTL and capacity limits.
type Store struct {
    mu       sync.RWMutex
    items    map[[20]byte]Item // target hash → item
    order    []storeEntry      // LRU tracking
    maxItems int
    ttl      time.Duration
}

type storeEntry struct {
    target   [20]byte
    accessed time.Time
}
```

```go
// dht/bep44.go

// PutResult holds the outcome of an iterative put operation.
type PutResult struct {
    Target [20]byte // the storage key
    Stored int      // number of nodes that accepted the put
}

// GetResult holds the outcome of an iterative get operation.
type GetResult struct {
    Value     bencode.Value
    Key       [32]byte   // Ed25519 public key (zero if immutable)
    Signature [64]byte   // Ed25519 signature (zero if immutable)
    Seq       int64      // sequence number (0 if immutable)
    Salt      []byte
}

// MutablePutParams holds the parameters for a mutable put operation.
type MutablePutParams struct {
    PrivateKey ed25519.PrivateKey // 64-byte Ed25519 private key
    Value      bencode.Value
    Seq        int64
    Salt       []byte // optional, ≤ 64 bytes
    CAS        *int64 // optional compare-and-swap
}
```

### 3.5 Key functions

```go
// dht/store.go

func NewStore(maxItems int, ttl time.Duration) *Store
func (s *Store) Put(target [20]byte, item Item) error
func (s *Store) Get(target [20]byte) (Item, bool)
func (s *Store) Evict()  // remove expired + over-capacity items
func (s *Store) Len() int

// dht/bep44.go

// Iterative get — finds item across the DHT.
func (d *DHT) Get(target [20]byte) (*GetResult, error)

// Immutable put — stores value at SHA-1(bencode(v)).
func (d *DHT) PutImmutable(v bencode.Value) (*PutResult, error)

// Mutable put — stores signed value under public key (+salt).
func (d *DHT) PutMutable(params MutablePutParams) (*PutResult, error)

// ImmutableTarget computes the storage key for an immutable item.
func ImmutableTarget(v bencode.Value) [20]byte

// MutableTarget computes the storage key for a mutable item.
func MutableTarget(publicKey [32]byte, salt []byte) [20]byte

// SignMutableItem constructs the signature for a mutable put.
func SignMutableItem(privateKey ed25519.PrivateKey, v bencode.Value, seq int64, salt []byte) [64]byte

// VerifyMutableItem verifies an Ed25519 signature on a mutable item.
func VerifyMutableItem(publicKey [32]byte, sig [64]byte, v bencode.Value, seq int64, salt []byte) bool

// signatureInput builds the byte buffer that gets signed.
func signatureInput(v bencode.Value, seq int64, salt []byte) []byte
```

### 3.6 Query handler integration

In `dht/node.go`, the existing `Listen` handler dispatches on `msg.Method`.
Add two new cases:

```go
case "get":
    // 1. Extract target from msg.Args
    // 2. Look up in d.Store
    // 3. Return item fields + token + closest nodes

case "put":
    // 1. Validate token
    // 2. If "k" present → mutable: verify sig, check seq, check cas
    //    Else → immutable: compute target = SHA-1(bencode(v))
    // 3. Store in d.Store
    // 4. Return ack
```

### 3.7 Dependencies on existing code

- `bencode.Encode` — used to compute `SHA-1(bencode(v))` for immutable targets
  and to build the signature input buffer.
- `dht.Transport.Send` — used for the iterative get/put RPCs.
- `dht.DHT.FindNode` — reused for the Kademlia closest-node lookup phase that
  precedes put and get operations.
- Token mechanism — the same token generation/validation used by `announce_peer`
  must also apply to `put`.

### 3.8 New dependency

```
crypto/ed25519  (Go stdlib)
```

No external packages required — Go's standard library includes Ed25519.

---

## 4. Dependencies

| Dependency | Type | Notes |
|------------|------|-------|
| BEP 5 (DHT) | Required | Existing `dht/` package provides KRPC transport, routing table, iterative lookups |
| `crypto/ed25519` | Go stdlib | Ed25519 signing and verification for mutable items |
| `crypto/sha1` | Go stdlib | Already used for info_hash; used here for target computation |
| `bencode/` package | Internal | `bencode.Encode` used to compute immutable targets and signature buffers |
| Token mechanism | Internal | Same token system used by `announce_peer` in existing DHT code |

BEP 44 is itself a dependency for:
- **BEP 46** — updateable torrents (stores infohash as a mutable item)
- **Custom applications** — any use case that needs small authenticated data in the DHT

---

## 5. Testing Strategy

### 5.1 Unit tests (`dht/store_test.go`)

| Test | Description |
|------|-------------|
| `TestStorePutGet` | Store an item, retrieve it by target, verify all fields match |
| `TestStoreEvictTTL` | Insert item, advance clock past TTL, verify `Get` returns `false` |
| `TestStoreEvictLRU` | Fill store to `maxItems`, insert one more, verify oldest is evicted |
| `TestStoreMutableUpdate` | Store mutable item with seq=1, update with seq=2, verify seq=2 is stored |
| `TestStoreMutableRejectOldSeq` | Store with seq=5, attempt put with seq=3, verify rejection |
| `TestStoreCapacity` | Insert `maxItems + 100` items, verify `Len() <= maxItems` |

### 5.2 Unit tests (`dht/bep44_test.go`)

| Test | Description |
|------|-------------|
| `TestImmutableTarget` | Compute `ImmutableTarget(v)` for known values, compare against reference vectors |
| `TestMutableTarget` | Compute `MutableTarget(pk, salt)` for known inputs, verify SHA-1 output |
| `TestMutableTargetNoSalt` | Verify `MutableTarget(pk, nil)` == `SHA-1(pk)` |
| `TestSignVerifyMutable` | Generate Ed25519 key pair, sign an item, verify signature passes |
| `TestSignVerifyMutableWithSalt` | Same as above but with a non-empty salt |
| `TestVerifyBadSignature` | Tamper with signature byte, verify `VerifyMutableItem` returns false |
| `TestVerifyWrongKey` | Verify with a different public key, expect false |
| `TestSignatureInput` | Build `signatureInput(v, seq, salt)` for known values, compare against reference |
| `TestSignatureInputNoSalt` | Verify salt-less path produces correct buffer |
| `TestPutImmutableValueTooLarge` | Value that bencodes to > 1000 bytes, expect error |
| `TestPutMutableSaltTooLong` | Salt > 64 bytes, expect error |

### 5.3 KRPC encoding tests

| Test | Description |
|------|-------------|
| `TestEncodeGetQuery` | Build a `get` query `Message`, encode, decode, verify fields round-trip |
| `TestEncodePutImmutableQuery` | Build immutable `put` Message, verify encoding |
| `TestEncodePutMutableQuery` | Build mutable `put` with `k`, `sig`, `seq`, `salt`, `cas`, verify all fields |
| `TestEncodeGetResponse` | Response with `v`, `k`, `sig`, `seq`, `token`, `nodes` |
| `TestDecodeGetResponseNoValue` | Response without `v` (item not found), verify token + nodes present |

### 5.4 Integration tests

| Test | Description |
|------|-------------|
| `TestImmutablePutGetRoundTrip` | Start 3 DHT nodes in-process, put immutable on node A, get from node C |
| `TestMutablePutGetRoundTrip` | Same setup, put mutable, get from a different node, verify sig + seq |
| `TestMutableUpdateSeq` | Put with seq=1, then put with seq=2, get from third node, verify seq=2 |
| `TestMutableCASSuccess` | Put with seq=1, then put with cas=1 and seq=2, verify update |
| `TestMutableCASFailure` | Put with seq=2, then put with cas=1 and seq=3, verify error 301 |
| `TestGetNonexistent` | Get a target that was never stored, verify nil value + nodes returned |
| `TestPutInvalidToken` | Send put with fabricated token, verify rejection |

### 5.5 Reference test vectors

Use the test vectors from the BEP 44 specification:

```
Immutable test vector:
  v = "Hello World!"
  target = SHA-1(bencode("Hello World!")) = SHA-1("12:Hello World!")

Mutable test vector (from BEP 44 appendix):
  Use the published Ed25519 key pair and verify that signatureInput()
  produces the exact byte sequence from the spec, and that the signature
  matches.
```
