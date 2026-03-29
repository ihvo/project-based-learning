# Peer Pressure — A BitTorrent Client in Go

## Goal

Build a fully BEP-compliant BitTorrent client from scratch, progressing through stages that teach both Go fundamentals and BitTorrent internals simultaneously. Each stage produces a working, testable artifact.

## Tech Stack

- **Language:** Go (latest stable)
- **Concurrency:** goroutines + channels (stdlib)
- **Hashing:** crypto/sha1 (stdlib)
- **Byte handling:** encoding/binary, bytes, bufio (stdlib)
- **CLI:** cobra (or stdlib flag)
- **Bencode:** hand-rolled (learning exercise)
- **HTTP:** net/http (stdlib)

## Stages

---

### Stage 1 — Bencode Codec

**What you learn:**
- Go: custom types with methods, `switch` statements, `(value, error)` return pattern, `_test.go` files, `[]byte` for binary data, `sort.Strings` for deterministic dict key ordering
- BitTorrent: the bencode serialization format (foundation of everything)

**What you build:**
- A bencode encoder and decoder that handles all four types: byte strings, integers, lists, and dictionaries
- Property: `decode(encode(value)) == value` for all valid inputs

**Deliverables:**
- `bencode.Value` type (interface or struct with type tag)
- `bencode.Decode(data []byte) (Value, error)`
- `bencode.Encode(v Value) []byte`
- Comprehensive tests with edge cases (negative ints, nested structures, empty containers, binary strings)

**Reference:** [BEP 3 — Bencode section](https://www.bittorrent.org/beps/bep_0003.html)

---

### Stage 2 — Torrent File Parser

**What you learn:**
- Go: structs, `os.ReadFile`, type assertions, `crypto/sha1` (stdlib), `fmt.Stringer` interface for pretty printing
- BitTorrent: `.torrent` file anatomy — info dictionary, piece hashes, single-file vs multi-file torrents, info_hash computation

**What you build:**
- Parse a `.torrent` file into a structured `Torrent` type
- Compute the `info_hash` (SHA-1 of the bencoded info dictionary)
- Print a human-readable summary (name, size, piece count, tracker URL, info_hash)

**Deliverables:**
- `torrent.Torrent` struct with all relevant fields
- `torrent.ParseFile(path string) (*Torrent, error)`
- CLI subcommand: `peer-pressure info <file.torrent>`
- Tests against real `.torrent` files

**Reference:** [BEP 3 — Metainfo file structure](https://www.bittorrent.org/beps/bep_0003.html)

---

### Stage 3 — Tracker Communication

**What you learn:**
- Go: `net/http` (stdlib), `net/url` for query encoding, `fmt.Errorf` with `%w` for error wrapping, goroutines for concurrent requests
- BitTorrent: tracker announce protocol (HTTP), compact peer lists, tracker response parsing, peer discovery

**What you build:**
- Send an announce request to the tracker with proper query parameters (info_hash, peer_id, port, uploaded, downloaded, left, event)
- Parse the tracker response (peer list in compact and dictionary formats)
- Display discovered peers (IP:port)

**Deliverables:**
- `tracker.Announce(t *Torrent, peerID [20]byte, port uint16) (*Response, error)`
- `tracker.Response` with interval, peers list, etc.
- CLI subcommand: `peer-pressure peers <file.torrent>`
- Tests with mocked tracker responses

**Reference:** [BEP 3 — Tracker protocol](https://www.bittorrent.org/beps/bep_0003.html), [BEP 23 — Compact peer lists](https://www.bittorrent.org/beps/bep_0023.html)

---

### Stage 4 — Peer Wire Protocol (Handshake + Messages)

**What you learn:**
- Go: `net.Conn`, `encoding/binary` for big-endian I/O, `bufio.Reader`/`bufio.Writer`, `io.ReadFull`, state machines with struct + iota constants
- BitTorrent: peer handshake, message framing (4-byte length prefix), all core message types (choke, unchoke, interested, have, bitfield, request, piece, cancel)

**What you build:**
- Establish a TCP connection to a peer and perform the handshake
- Encode/decode all peer wire protocol messages
- A `PeerConnection` abstraction that can send and receive messages

**Deliverables:**
- `peer.Handshake` — serialize/deserialize the 68-byte handshake
- `peer.Message` struct with type ID + payload
- `peer.ReadMessage(r io.Reader) (*Message, error)` / `peer.WriteMessage(w io.Writer, m *Message) error`
- `peer.Conn` wrapping a `net.Conn` with read/write helpers
- CLI subcommand: `peer-pressure handshake <file.torrent>` (connect to first peer, complete handshake, print peer's bitfield)
- Tests for message serialization round-trips

**Reference:** [BEP 3 — Peer protocol](https://www.bittorrent.org/beps/bep_0003.html), [BEP 20 — Peer ID conventions](https://www.bittorrent.org/beps/bep_0020.html)

---

### Stage 5 — Downloading a Single Piece

**What you learn:**
- Go: channels for coordination, `bytes.Buffer`, `crypto/sha1` streaming hash, `io.Writer` composability
- BitTorrent: the request/piece exchange, block-level transfers (16 KiB blocks within a piece), piece verification via SHA-1, choke/unchoke flow

**What you build:**
- Request blocks of a single piece from a peer
- Reassemble blocks into a complete piece
- Verify the piece hash against the torrent's expected hash
- Write the verified piece to disk

**Deliverables:**
- `download.DownloadPiece(conn *peer.Conn, index int, hash [20]byte, length int) ([]byte, error)`
- Proper block splitting (16 KiB requests)
- SHA-1 verification
- CLI subcommand: `peer-pressure download-piece <file.torrent> <piece_index> -o <output>`
- Tests with a local test peer (or mock)

---

### Stage 6 — Full File Download

**What you learn:**
- Go: goroutines, `sync.Mutex` / `sync.RWMutex`, `select` statement, `sync.WaitGroup`, `context.Context` for cancellation
- BitTorrent: piece selection strategy (rarest first), managing multiple peer connections, bitfield tracking, choking algorithm basics

**What you build:**
- Download all pieces concurrently from multiple peers
- Piece selection with rarest-first strategy
- Assemble pieces into the final file(s)
- Progress bar and download stats

**Deliverables:**
- `download.DownloadFile(t *torrent.Torrent) error`
- Concurrent peer connections (configurable limit)
- Piece picker with rarest-first
- Multi-file torrent support (file boundary handling)
- CLI subcommand: `peer-pressure download <file.torrent> -o <output_dir>`
- Resume support (skip already-verified pieces)

---

### Stage 7 — Seeding & Upload

**What you learn:**
- Go: `net.Listener`, accept loop, goroutine-per-connection, graceful shutdown with `context.Context` + `os/signal`
- BitTorrent: seeding protocol, responding to requests, upload tracking, announce with "completed" event

**What you build:**
- Listen for incoming peer connections
- Serve piece data to requesting peers
- Announce as a seeder to the tracker
- Track upload statistics

**Deliverables:**
- `seed.Listen(t *torrent.Torrent, port uint16) error`
- Respond to handshake, bitfield, request messages
- CLI subcommand: `peer-pressure seed <file.torrent> <data_path>`
- Rate limiting (optional)

---

### Stage 8 — UDP Tracker Protocol (BEP 15)

**What you learn:**
- Go: `net.UDPConn`, `time.After` for timeouts, `encoding/binary` for binary serialization, retry loops with backoff
- BitTorrent: UDP tracker protocol (connect, announce, scrape), why UDP is preferred over HTTP for trackers

**What you build:**
- Full UDP tracker client with connect → announce → scrape flow
- Retry logic with exponential backoff (per BEP 15 spec)
- Transparent fallback: try UDP, fall back to HTTP

**Deliverables:**
- `tracker.UDPAnnounce(...)` with full BEP 15 compliance
- Tracker URL scheme detection (`udp://` vs `http://`)
- Tests with real and mocked UDP trackers

**Reference:** [BEP 15](https://www.bittorrent.org/beps/bep_0015.html)

---

### Stage 9 — Extension Protocol & Magnet Links (BEP 10 + BEP 9)

**What you learn:**
- Go: extending existing message types, `net/url` for URI parsing, interfaces for extensibility
- BitTorrent: extension handshake, metadata exchange, magnet URI parsing, downloading torrent metadata from peers

**What you build:**
- Extension protocol handshake (BEP 10)
- Metadata exchange (BEP 9) — request and assemble torrent info from peers
- Magnet link parsing (`magnet:?xt=urn:btih:...`)
- Bootstrap from magnet link → metadata → full download

**Deliverables:**
- `magnet.Parse(uri string) (*MagnetLink, error)`
- `metadata.Fetch(infoHash [20]byte, peers []net.Addr) (*torrent.Torrent, error)`
- CLI subcommand: `peer-pressure download <magnet_uri> -o <output_dir>`

**Reference:** [BEP 9](https://www.bittorrent.org/beps/bep_0009.html), [BEP 10](https://www.bittorrent.org/beps/bep_0010.html)

---

### Stage 10 — DHT (BEP 5)

**What you learn:**
- Go: `sync.RWMutex`-protected routing table, concurrent UDP RPC with goroutines, `map` for routing, background goroutines for table maintenance
- BitTorrent: DHT node discovery, `get_peers` / `announce_peer` / `find_node` / `ping` RPCs, routing table maintenance, token management

**What you build:**
- Full Kademlia DHT node implementation
- Bootstrap from well-known nodes (router.bittorrent.com, dht.transmissionbt.com)
- Find peers for an info_hash without any tracker
- Announce presence on DHT

**Deliverables:**
- `dht.Node` with routing table and RPC handlers
- `dht.GetPeers(infoHash [20]byte) ([]net.UDPAddr, error)`
- `dht.Announce(infoHash [20]byte, port uint16) error`
- Integration with download pipeline (DHT as peer source alongside trackers)

**Reference:** [BEP 5](https://www.bittorrent.org/beps/bep_0005.html)

---

### Stage 11 — Peer Exchange (BEP 11) & Multi-Tracker (BEP 12)

**What you learn:**
- Go: interfaces for `PeerSource` abstraction, fan-in pattern with channels to merge concurrent peer sources
- BitTorrent: PEX message format, multi-tracker announce tiers, combining peer sources

**What you build:**
- Peer Exchange: exchange peer lists with connected peers
- Multi-tracker: announce to multiple tracker tiers with fallback logic
- Unified peer source manager (tracker + DHT + PEX)

**Deliverables:**
- PEX extension message handling
- Multi-tracker tier support
- `PeerSource` trait unifying all discovery methods

**Reference:** [BEP 11](https://www.bittorrent.org/beps/bep_0011.html), [BEP 12](https://www.bittorrent.org/beps/bep_0012.html)

---

### Stage 12 — Polish & Advanced Features

**What you learn:**
- Go: `pprof` for profiling, `log/slog` for structured logging, `go doc` documentation, integration testing with `TestMain`
- BitTorrent: private torrents (BEP 27), fast extension (BEP 6), choking algorithms (tit-for-tat), endgame mode

**What you build:**
- Private torrent support (disable DHT/PEX when private flag is set)
- Fast extension messages (have-all, have-none, reject, suggest, allowed-fast)
- Proper choking algorithm (optimistic unchoke rotation)
- Endgame mode for final pieces
- Bandwidth throttling
- Torrent creation (`peer-pressure create <path> -t <tracker_url>`)

**Deliverables:**
- `peer-pressure create` subcommand
- Bandwidth controls (`--max-upload`, `--max-download`)
- Private torrent compliance
- Full CLI with all subcommands working together

**Reference:** [BEP 6](https://www.bittorrent.org/beps/bep_0006.html), [BEP 27](https://www.bittorrent.org/beps/bep_0027.html)

---

## Project Structure (target)

```
peer-pressure/
├── go.mod
├── main.go              # CLI entry point
├── bencode/             # Stage 1: encoder/decoder
├── torrent/             # Stage 2: .torrent parser
├── tracker/             # Stage 3 + 8: HTTP & UDP tracker clients
├── peer/                # Stage 4: wire protocol, handshake, messages
├── download/            # Stage 5 + 6: piece/file download engine
├── seed/                # Stage 7: seeding/upload
├── magnet/              # Stage 9: magnet link + metadata exchange
├── dht/                 # Stage 10: Kademlia DHT
├── pex/                 # Stage 11: peer exchange
└── testdata/            # sample .torrent files, test data
```

## Notes

- Each stage should be a PR-sized chunk with its own tests
- Stages 1–6 are the core path to a working downloader
- Stages 7–12 add real-world completeness
- We will NOT use a bencode library — rolling our own is the best way to learn both Go and the protocol's data format
- Testing strategy: unit tests per package (`_test.go`) + integration tests using real torrents (e.g., a well-known Linux ISO torrent)
