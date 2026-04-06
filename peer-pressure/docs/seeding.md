# Seeding — Upload Server

> Makes Peer Pressure a complete BitTorrent client by serving piece data to
> other peers, closing the loop from download-only to full participation in
> the swarm.

---

## 1. Summary

Seeding is the upload side of BitTorrent. After a download completes (or when
a user has local data matching a .torrent file), the client listens for
incoming TCP connections, validates the remote peer's handshake, and serves
piece data on request.

This feature turns Peer Pressure from a leecher into a fully participating
member of the swarm. It includes:

- **TCP accept loop** — listen on a port, accept incoming peer connections.
- **Server-side handshake** — validate the remote peer's infohash against
  loaded torrents, respond with our handshake.
- **Request serving** — send Bitfield, respond to Request messages with Piece
  messages containing data read from disk.
- **Choking algorithm** — tit-for-tat: unchoke top N peers by upload
  contribution, plus one optimistic unchoke slot rotated every 30 seconds.
- **Tracker integration** — announce `event=completed` on download finish,
  regular re-announces with `left=0`.
- **Data verification** — hash every piece against the .torrent's piece hashes
  before starting to serve.

---

## 2. Design

### 2.1 Architecture overview

```
                    ┌──────────────┐
                    │    Seeder    │
                    │              │
                    │  listener    │──→ Accept loop (goroutine)
                    │  torrents    │      │
                    │  conns       │      ▼
                    │  choker      │    Per-connection handler (goroutine)
                    └──────────────┘      │
                         │                ├─ ReadHandshake → validate infohash
                         │                ├─ WriteHandshake → our handshake
                         │                ├─ WriteBitfield  → we have all pieces
                         │                └─ Message loop:
                         │                     Request → read from disk → send Piece
                         │                     Interested → consider unchoking
                         │                     NotInterested → may choke
                         │
                    Choker (background goroutine)
                         │
                         └─ Every 10s: rank peers, unchoke top N
                            Every 30s: rotate optimistic unchoke
```

### 2.2 Data verification

Before serving any data, the seeder must verify that the local files match the
.torrent's piece hashes:

```
for each piece index 0..N-1:
    data = read piece bytes from file(s)
    actual_hash = SHA-1(data)
    if actual_hash != torrent.Pieces[index]:
        mark piece as missing
        
if any pieces missing:
    report error (or seed only verified pieces)
```

Pieces span file boundaries in multi-file torrents — the verifier must stitch
data from consecutive files for pieces that cross boundaries.

### 2.3 Server-side handshake

When a remote peer connects:

```
1. Read 68-byte handshake from peer
2. Extract infohash from handshake
3. Look up infohash in Seeder.torrents map
4. If not found → close connection (unknown torrent)
5. Send our 68-byte handshake (same infohash, our peerID)
6. If peer supports extensions (reserved bit 43) → exchange ExtHandshake
7. Send Bitfield message (all bits set — we have every piece)
8. Enter message loop
```

### 2.4 Message loop (per connection)

```
loop:
    msg = conn.ReadMessage()
    switch msg.ID:
    
    case MsgInterested:
        record peer as interested
        (choker will decide whether to unchoke)
    
    case MsgNotInterested:
        record peer as not interested
        choke peer if currently unchoked
    
    case MsgRequest:
        if peer is choked → ignore (per BEP 3)
        parse (index, begin, length) from payload
        validate: index < numPieces, begin+length <= pieceLen, length <= 16384
        read `length` bytes from disk at piece offset
        send Piece message (index, begin, block)
        record uploaded bytes
    
    case MsgCancel:
        cancel pending request (if any queued but not yet sent)
    
    case MsgHave, MsgBitfield:
        track peer's bitfield (for choking decisions)
    
    case MsgPiece:
        ignore (we already have everything)
```

### 2.5 Choking algorithm (tit-for-tat)

The choking algorithm runs on a timer in a background goroutine:

**Every 10 seconds — regular unchoke:**

```
1. Rank all interested peers by their upload speed TO US
   (measured over the last 20 seconds)
2. Unchoke the top 4 peers (the "upload slots")
3. Choke all other peers
```

For seeders (where nobody uploads to us), the ranking uses our upload speed
to them instead — unchoke the peers we can upload to fastest.

**Every 30 seconds — optimistic unchoke:**

```
1. Pick one random choked+interested peer
2. Unchoke them (in addition to the regular 4)
3. This gives new peers a chance to prove themselves
```

### 2.6 Disk I/O

Reading piece data from disk for multi-file torrents:

```
offset_in_torrent = piece_index * piece_length + begin
remaining = length

for each file in torrent.Files:
    if offset_in_torrent >= file.Length:
        offset_in_torrent -= file.Length
        continue
    
    read_from_file = min(remaining, file.Length - offset_in_torrent)
    read `read_from_file` bytes from file at `offset_in_torrent`
    append to block buffer
    remaining -= read_from_file
    offset_in_torrent = 0
    
    if remaining == 0:
        break
```

### 2.7 Tracker integration

When seeding starts:

```
1. Announce to all trackers with event=completed (if just finished downloading)
   OR event=started with left=0 (if seeding existing data)
2. Set up periodic re-announce at tracker's interval (typically 30-60 minutes)
   with left=0, uploaded=<total_uploaded>
3. On shutdown: announce event=stopped
```

### 2.8 Connection limits

- **Max connections**: configurable, default 50 simultaneous peers.
- **Accept rate**: limit new connection acceptance to prevent SYN flood
  (e.g. max 10 new connections/second).
- When at max connections, still accept but immediately close with no
  handshake.

---

## 3. Implementation Plan

### 3.1 Package placement

New `seed/` package for seeding logic, keeping it separate from `download/`.

### 3.2 New files

| File | Purpose |
|------|---------|
| `seed/seed.go` | `Seeder` struct, accept loop, connection dispatch, lifecycle management |
| `seed/upload.go` | Per-connection upload handler — handshake, message loop, disk reads |
| `seed/verify.go` | Pre-seed data integrity verification |
| `seed/choke.go` | Choking algorithm — tit-for-tat with optimistic unchoke |
| `seed/seed_test.go` | Tests for Seeder lifecycle |
| `seed/upload_test.go` | Tests for upload handler |
| `seed/verify_test.go` | Tests for data verification |
| `seed/choke_test.go` | Tests for choking algorithm |

### 3.3 Modified files

| File | Changes |
|------|---------|
| `cmd/peer-pressure/main.go` | Add `seed` subcommand |
| `tracker/tracker.go` | No changes needed — `AnnounceParams` already supports `Event` and `Left` fields |

### 3.4 Key types

```go
// seed/seed.go

// Config holds seeder configuration.
type Config struct {
    Torrent      *torrent.Torrent
    DataPath     string     // path to the file or directory containing torrent data
    PeerID       [20]byte
    ListenAddr   string     // e.g. ":6881"
    MaxConns     int        // max simultaneous connections (default: 50)
    UploadSlots  int        // regular unchoke slots (default: 4)
}

// Seeder manages the accept loop, active connections, and choking.
type Seeder struct {
    cfg       Config
    listener  net.Listener
    conns     map[string]*uploadConn // remoteAddr → conn
    connsMu   sync.Mutex
    choker    *Choker
    pieces    []bool       // verified piece bitmap
    data      *diskReader  // handles multi-file reads
    uploaded  atomic.Int64 // total bytes uploaded
}
```

```go
// seed/upload.go

// uploadConn tracks state for a single connected peer.
type uploadConn struct {
    conn        *peer.Conn
    addr        string
    interested  bool       // peer has sent Interested
    choked      bool       // we are choking this peer (true by default)
    peerBitfield []byte    // what the peer has
    uploadBytes  atomic.Int64 // bytes uploaded to this peer
    uploadRate   float64      // bytes/sec (computed by choker)
    lastActive   time.Time
}
```

```go
// seed/verify.go

// VerifyResult holds the outcome of data verification.
type VerifyResult struct {
    TotalPieces    int
    ValidPieces    int
    InvalidPieces  []int // indices of pieces that failed verification
}
```

```go
// seed/choke.go

// Choker implements the tit-for-tat choking algorithm.
type Choker struct {
    mu              sync.Mutex
    uploadSlots     int           // number of regular unchoke slots
    optimistic      string        // addr of the optimistically unchoked peer
    optimisticTimer time.Duration // 30s rotation
    evalInterval    time.Duration // 10s evaluation
}
```

### 3.5 Key functions

```go
// seed/seed.go

// New creates a new Seeder. Does NOT start listening — call Run.
func New(cfg Config) (*Seeder, error)

// Verify checks all pieces against the torrent's hashes.
// Must be called before Run.
func (s *Seeder) Verify() (*VerifyResult, error)

// Run starts the accept loop, choker, and tracker announcements.
// Blocks until ctx is cancelled.
func (s *Seeder) Run(ctx context.Context) error

// Stats returns current seeding statistics.
func (s *Seeder) Stats() Stats

// seed/upload.go

// handleConn runs the full lifecycle of a single peer connection:
// handshake → bitfield → message loop. Blocks until the connection
// closes or ctx is cancelled.
func (s *Seeder) handleConn(ctx context.Context, conn net.Conn)

// readPieceData reads a block from the torrent data on disk.
func (s *Seeder) readPieceData(pieceIndex, begin, length int) ([]byte, error)

// seed/verify.go

// VerifyData hashes every piece of the local data against the torrent's
// piece hashes and returns the result.
func VerifyData(t *torrent.Torrent, dataPath string) (*VerifyResult, error)

// seed/choke.go

// NewChoker creates a choker with the given number of upload slots.
func NewChoker(uploadSlots int) *Choker

// Run starts the periodic evaluation loop. Blocks until ctx is cancelled.
func (c *Choker) Run(ctx context.Context, getConns func() []*uploadConn)

// Evaluate runs one round of the choking algorithm:
// rank peers, unchoke top N, rotate optimistic.
func (c *Choker) Evaluate(conns []*uploadConn)
```

### 3.6 CLI integration

```
peer-pressure seed <file.torrent> <data_path> [--listen :6881] [--max-conns 50] [--upload-slots 4]

1. Parse .torrent file
2. Verify data integrity (with progress bar)
3. Start TCP listener
4. Announce to trackers (event=started, left=0)
5. Accept connections, serve data
6. Periodic re-announce
7. On SIGINT/SIGTERM: announce event=stopped, close listener, drain connections
```

---

## 4. Dependencies

| Dependency | Type | Notes |
|------------|------|-------|
| `peer/` package | Required | `peer.Conn`, `peer.ReadHandshake`, `peer.WriteHandshake`, message types |
| `torrent/` package | Required | `.torrent` metadata — piece hashes, file layout, piece length |
| `tracker/` package | Required | Announce with `event=completed`/`started`/`stopped`, `left=0` |
| `download/piece.go` | Reference | `BlockSize` constant (16384) for request validation |
| `crypto/sha1` | Go stdlib | Piece hash verification |
| `net` | Go stdlib | `net.Listener`, TCP accept loop |
| `context` | Go stdlib | Cancellation and graceful shutdown |
| `os/signal` | Go stdlib | SIGINT/SIGTERM handling |

---

## 5. Testing Strategy

### 5.1 Verification tests (`seed/verify_test.go`)

| Test | Description |
|------|-------------|
| `TestVerifyValidSingleFile` | Create a temp file, build a .torrent from it, verify passes 100% |
| `TestVerifyValidMultiFile` | Create a temp directory with multiple files, build .torrent, verify passes |
| `TestVerifyCorruptPiece` | Flip a byte in one piece, verify reports that piece as invalid |
| `TestVerifyMissingFile` | Delete one file from a multi-file torrent, verify reports affected pieces as invalid |
| `TestVerifyTruncatedFile` | Truncate the file to half its size, verify reports the missing pieces |
| `TestVerifyPieceBoundary` | Multi-file torrent where a piece spans two files, verify the boundary piece |
| `TestVerifyEmptyFile` | Torrent with a zero-length file in the file list, verify handles it |

### 5.2 Upload handler tests (`seed/upload_test.go`)

| Test | Description |
|------|-------------|
| `TestHandshakeSuccess` | Connect with correct infohash, verify handshake exchange completes |
| `TestHandshakeWrongInfohash` | Connect with unknown infohash, verify connection is closed |
| `TestBitfieldSent` | Connect, verify the first message after handshake is a Bitfield with all bits set |
| `TestServeRequest` | Send Interested + wait for Unchoke + send Request, verify correct Piece data returned |
| `TestServeRequestChoked` | Send Request without being unchoked, verify no Piece response |
| `TestServeMultipleRequests` | Pipeline 5 Request messages, verify all 5 Piece responses arrive with correct data |
| `TestServeRequestOutOfBounds` | Request piece index beyond numPieces, verify connection closed or error |
| `TestServeRequestBlockTooLarge` | Request with length > 16384, verify rejected |
| `TestServeCancelledRequest` | Send Request then immediately Cancel, verify the block is not sent (best effort) |
| `TestPeerDisconnect` | Peer closes connection mid-transfer, verify handler exits cleanly |

### 5.3 Choking algorithm tests (`seed/choke_test.go`)

| Test | Description |
|------|-------------|
| `TestChokeTitForTat` | 6 interested peers with different upload rates, verify top 4 by rate are unchoked |
| `TestChokeOptimisticUnchoke` | 6 interested peers, verify one outside the top 4 is unchoked (optimistic) |
| `TestChokeOptimisticRotation` | Run Evaluate twice 30s apart, verify the optimistic slot changes |
| `TestChokeNotInterestedIgnored` | Mix of interested and not-interested peers, verify only interested peers are considered for unchoke |
| `TestChokeAllSlotsFilled` | Exactly 4 interested peers, verify all are unchoked (no optimistic needed beyond regular slots) |
| `TestChokeNoInterestedPeers` | All peers are not interested, verify all remain choked |
| `TestChokeNewPeerGetsOptimistic` | Add a new peer with no upload history, verify it gets the optimistic slot eventually |

### 5.4 Seeder lifecycle tests (`seed/seed_test.go`)

| Test | Description |
|------|-------------|
| `TestSeederStartStop` | Create seeder, Run in goroutine, cancel context, verify clean shutdown |
| `TestSeederMaxConns` | Set maxConns=2, connect 3 peers, verify third is rejected |
| `TestSeederMultipleTorrents` | Load 2 different torrents, connect to each with correct infohash, verify both served |
| `TestSeederTrackerAnnounce` | Use a mock tracker server, verify seeder sends announce with `left=0` on startup and `event=stopped` on shutdown |

### 5.5 Integration test

| Test | Description |
|------|-------------|
| `TestDownloadFromSeeder` | Start a seeder with test data. Start a downloader pointed at the seeder (localhost). Verify the downloaded data matches the original. This exercises the full handshake → bitfield → request → piece → verify pipeline. |
