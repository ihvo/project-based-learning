# BEP 46 — Updating Torrents via DHT Mutable Items

> Enables publishing updateable torrent feeds through the DHT, so a content
> publisher can point a stable public key at a changing infohash without any
> central server.

Reference: <https://www.bittorrent.org/beps/bep_0046.html>

---

## 1. Summary

BEP 46 builds on BEP 44 mutable items to create a "pointer" from an Ed25519
public key to a torrent infohash. The publisher stores a small mutable item in
the DHT containing the current infohash. When content is updated, the publisher
increments the sequence number and stores the new infohash. Clients discover
the content through a new magnet link format that encodes the public key
instead of the infohash.

Use cases:

- **Software update channels** — a distro publishes a stable key; clients
  always resolve the latest ISO infohash.
- **Mutable content feeds** — a creator updates a catalog without needing DNS
  or a web server.
- **DNS-less content addressing** — the public key *is* the stable identifier,
  independent of any domain name.
- **Multi-feed publishing** — the `salt` field lets one key pair manage
  multiple independent feeds (e.g. "stable", "nightly", "archive").

---

## 2. Protocol Specification

### 2.1 Magnet link format

```
magnet:?xs=urn:btpk:<public-key-hex>
```

- `xs` (eXact Source) contains the public key as a 64-character hex string.
- Additional parameters are allowed: `dn` (display name), `tr` (trackers).
- Example:

```
magnet:?xs=urn:btpk:8543d3e6115f0f98c944077a4493dcd543e49c739fd998550a1f614ab36ed63e&dn=archlinux-2025.01
```

### 2.2 Stored value format

The value stored in the DHT mutable item is a bencoded dictionary:

```
{ "ih": <20-byte infohash> }
```

- `ih` is the raw 20-byte SHA-1 infohash of the torrent's info dictionary.
- The total bencoded size is well within BEP 44's 1000-byte limit:
  `d2:ih20:XXXXXXXXXXXXXXXXXXXXe` = 28 bytes.

### 2.3 Publishing flow

```
Publisher has:
  - Ed25519 key pair (private_key, public_key)
  - infohash of the torrent to publish

1. Build value:  v = bencode({"ih": infohash})
2. Compute target: SHA-1(public_key)  [no salt]
   or SHA-1(public_key + salt)         [with salt for multi-feed]
3. Sign:  sig = Ed25519_Sign(private_key, signatureInput(v, seq, salt))
4. Iterative put via BEP 44:
   a. Get tokens from K closest nodes to target
   b. Send put with k, sig, seq, v, token (and salt/cas if applicable)
5. Increment seq for the next update.
```

### 2.4 Resolution flow

```
Client has: magnet:?xs=urn:btpk:<hex_pubkey>

1. Parse public key from magnet link (32 bytes from 64 hex chars).
2. Compute target = SHA-1(public_key)  [or SHA-1(public_key + salt)].
3. BEP 44 iterative get(target):
   a. Find K closest nodes to target
   b. Send get to each, collect responses
   c. Among responses with valid signatures, keep the one with highest seq
4. Extract infohash from value: ih = v["ih"]
5. Now resolve the torrent via standard BEP 9 metadata exchange:
   a. Use the infohash to get_peers from DHT (or announce to trackers if tr= present)
   b. Connect to peers, do extension handshake, fetch metadata via ut_metadata
6. Download proceeds normally.
```

### 2.5 Multi-feed with salt

A single key pair can publish multiple independent feeds by using the `salt`
parameter:

```
Feed "stable":   target = SHA-1(public_key + "stable")
Feed "nightly":  target = SHA-1(public_key + "nightly")

Magnet:  magnet:?xs=urn:btpk:<hex>&s=stable
```

The `s` query parameter in the magnet link carries the salt value. Each feed
has its own `seq` counter.

### 2.6 Update semantics

- Each update **must** increment `seq` — the DHT nodes reject puts with `seq`
  ≤ the currently stored `seq`.
- `cas` (compare-and-swap from BEP 44) can be used to avoid race conditions
  when multiple publishers share a key pair (not typical but possible).
- Clients should periodically re-resolve to detect updates. A reasonable poll
  interval is 30–60 minutes, or on user action.
- Publishers should re-put periodically (every ~1 hour) to refresh the item's
  TTL on DHT nodes.

### 2.7 Security considerations

- **Authenticity**: Ed25519 signature guarantees only the key holder can update
  the feed. Clients MUST verify signatures (handled by BEP 44 layer).
- **Replay protection**: `seq` prevents rollback to older values.
- **No confidentiality**: the stored infohash is public. Anyone who knows the
  public key can resolve the current infohash.
- **Key distribution**: the public key in the magnet link is the trust anchor.
  It must be distributed through a trusted channel (website, signed message,
  etc.).

---

## 3. Implementation Plan

### 3.1 Package placement

New `feed/` package for BEP 46 logic. This sits above `dht/` (which provides
BEP 44) and `magnet/` (which parses magnet links).

### 3.2 New files

| File | Purpose |
|------|---------|
| `feed/feed.go` | Publisher and Resolver types, publish/resolve logic |
| `feed/feed_test.go` | Unit and integration tests |
| `feed/doc.go` | Package documentation |

### 3.3 Modified files

| File | Changes |
|------|---------|
| `magnet/magnet.go` | Parse `xs=urn:btpk:` parameter and optional `s` (salt) parameter; add `PublicKey` and `Salt` fields to `Link` struct |
| `cmd/peer-pressure/main.go` | Add `resolve` subcommand for BEP 46 magnet links; add `publish` subcommand for feed publishing |

### 3.4 Key types

```go
// feed/feed.go

// Publisher manages a mutable torrent feed backed by a BEP 44 DHT item.
type Publisher struct {
    dht        *dht.DHT
    privateKey ed25519.PrivateKey
    publicKey  [32]byte
    salt       []byte  // optional, for multi-feed
    seq        int64   // current sequence number
    mu         sync.Mutex
}

// Resolver resolves a BEP 46 public-key magnet link to a torrent infohash.
type Resolver struct {
    dht *dht.DHT
}

// Feed represents the current state of a resolved feed.
type Feed struct {
    PublicKey [32]byte
    Salt      []byte
    InfoHash  [20]byte
    Seq       int64
}
```

```go
// magnet/magnet.go — additions to existing Link struct

type Link struct {
    InfoHash  [20]byte   // from xt=urn:btih:
    PublicKey [32]byte   // from xs=urn:btpk: (BEP 46)
    HasPubKey bool       // true if xs=urn:btpk: was present
    Salt      string     // from s= parameter (BEP 46)
    Name      string     // from dn=
    Trackers  []string   // from tr=
}
```

### 3.5 Key functions

```go
// feed/feed.go

// NewPublisher creates a publisher for a single feed.
func NewPublisher(d *dht.DHT, privateKey ed25519.PrivateKey, salt []byte) *Publisher

// Publish stores the given infohash in the DHT under this publisher's key.
// Increments seq automatically. Returns the number of DHT nodes that accepted.
func (p *Publisher) Publish(ctx context.Context, infoHash [20]byte) (int, error)

// Refresh re-puts the current value to keep it alive in the DHT.
func (p *Publisher) Refresh(ctx context.Context) (int, error)

// MagnetLink returns the BEP 46 magnet URI for this feed.
func (p *Publisher) MagnetLink() string

// NewResolver creates a resolver backed by the given DHT node.
func NewResolver(d *dht.DHT) *Resolver

// Resolve looks up a BEP 46 feed by public key (+ optional salt) and returns
// the current infohash and sequence number.
func (r *Resolver) Resolve(ctx context.Context, publicKey [32]byte, salt []byte) (*Feed, error)
```

```go
// magnet/magnet.go — additions

// IsBEP46 returns true if this magnet link uses the xs=urn:btpk: scheme.
func (l *Link) IsBEP46() bool
```

### 3.6 CLI integration

```
peer-pressure resolve <magnet_uri>
  - Parses xs=urn:btpk: from magnet link
  - Boots DHT, resolves feed → infohash
  - Fetches metadata via BEP 9
  - Prints torrent info (or passes to download)

peer-pressure publish <file.torrent> --key <keyfile> [--salt <salt>]
  - Loads torrent, extracts infohash
  - Loads Ed25519 private key from file
  - Boots DHT, publishes feed
  - Prints magnet:?xs=urn:btpk:... link
```

### 3.7 End-to-end resolution pipeline

```
magnet:?xs=urn:btpk:<hex>&s=<salt>&tr=<tracker>
  │
  ├─ magnet.Parse() → Link{PublicKey, Salt, Trackers}
  │
  ├─ feed.Resolver.Resolve(pubkey, salt) → Feed{InfoHash, Seq}
  │    └─ dht.DHT.Get(target) → GetResult{Value, Seq, Sig}
  │         └─ verify signature, extract "ih" from value
  │
  ├─ dht.DHT.GetPeers(infohash) → peer addresses
  │
  ├─ magnet.FetchMetadata(conn, infohash) → raw info dict
  │
  └─ torrent.FromInfoDict(raw, infohash, trackers) → *Torrent
       └─ download.File(ctx, cfg) → downloaded content
```

---

## 4. Dependencies

| Dependency | Type | Notes |
|------------|------|-------|
| BEP 44 (`dht/bep44.go`) | Required | Provides `DHT.Get`, `DHT.PutMutable`, signature verification |
| BEP 5 (`dht/`) | Required | DHT network for iterative lookups |
| BEP 9 (`magnet/metadata.go`) | Required | Metadata exchange to fetch torrent info from infohash |
| BEP 10 (`peer/extension.go`) | Required | Extension protocol for ut_metadata negotiation |
| `magnet/` package | Modified | Parse `xs=urn:btpk:` magnet links |
| `crypto/ed25519` | Go stdlib | Key generation for publishing |
| `encoding/hex` | Go stdlib | Public key hex encoding/decoding |

---

## 5. Testing Strategy

### 5.1 Unit tests — magnet parsing (`magnet/magnet_test.go`)

| Test | Description |
|------|-------------|
| `TestParseBEP46Magnet` | Parse `magnet:?xs=urn:btpk:<64hex>`, verify `PublicKey` and `HasPubKey=true` |
| `TestParseBEP46MagnetWithSalt` | Parse with `&s=stable`, verify `Salt="stable"` |
| `TestParseBEP46MagnetWithTrackers` | Parse with `&tr=...`, verify `Trackers` populated |
| `TestParseBEP46InvalidHex` | Public key hex is wrong length, expect error |
| `TestParseBEP46EmptyKey` | `xs=urn:btpk:` with no hex, expect error |
| `TestIsBEP46True` | Link with `HasPubKey=true` returns `IsBEP46()=true` |
| `TestIsBEP46False` | Standard `xt=urn:btih:` link returns `IsBEP46()=false` |
| `TestParseMixedMagnet` | Magnet with both `xt=urn:btih:` and `xs=urn:btpk:`, verify both fields |

### 5.2 Unit tests — feed (`feed/feed_test.go`)

| Test | Description |
|------|-------------|
| `TestPublishBuildValue` | Verify the bencoded value is `d2:ih20:<hash>e` |
| `TestPublishSeqIncrement` | Call `Publish` twice, verify seq goes from 1 to 2 |
| `TestPublisherMagnetLink` | Verify `MagnetLink()` produces `magnet:?xs=urn:btpk:<hex>` |
| `TestPublisherMagnetLinkWithSalt` | Verify salt appears as `&s=<salt>` in magnet URI |
| `TestResolveExtractInfoHash` | Mock DHT get to return `{"ih": <hash>}`, verify resolver extracts it |
| `TestResolveInvalidValue` | DHT get returns value without `"ih"` key, expect error |
| `TestResolveValueNotDict` | DHT get returns a string instead of dict, expect error |
| `TestResolveInfoHashWrongLength` | `"ih"` value is 19 bytes, expect error |

### 5.3 Integration tests

| Test | Description |
|------|-------------|
| `TestPublishResolveRoundTrip` | Start 3 in-process DHT nodes. Publisher puts feed on node A, resolver reads from node C. Verify infohash matches. |
| `TestPublishUpdateResolve` | Publish infohash_1 with seq=1, then infohash_2 with seq=2. Resolve and verify infohash_2 is returned. |
| `TestPublishWithSalt` | Publish two feeds under the same key with different salts. Resolve each independently, verify correct infohash. |
| `TestResolveNonexistentFeed` | Resolve a public key that was never published. Expect a clear "not found" error. |
| `TestRefreshKeepsAlive` | Publish, wait, call Refresh, verify the item is still resolvable. |

### 5.4 End-to-end test

| Test | Description |
|------|-------------|
| `TestBEP46FullPipeline` | Generate Ed25519 key pair → create a .torrent from test data → publish infohash via BEP 46 → parse the magnet link → resolve via BEP 46 → verify returned infohash matches original. (Does not require actual peer connections — stops at infohash verification.) |
