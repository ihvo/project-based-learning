# Peer Pressure — Copilot Agent Instructions

## Project context

Peer Pressure is a BitTorrent client written in Go, built as a staged learning project. The developer (Ihar) is learning both Go and the BitTorrent protocol simultaneously.

## Teaching workflow

This project follows a **teach-then-build** loop for each stage. Before writing any code for a stage:

### 1. Teach the BitTorrent concept first

- Explain the relevant BEP specification(s) in plain language
- Walk through the protocol mechanics: what bytes go on the wire, what the data structures look like, why the design decisions were made
- Use diagrams (mermaid) where they help — especially for message flows, state machines, and data layouts
- Cover edge cases and gotchas that real implementations must handle
- Reference the official BEP documents by number and link

### 2. Teach the Go idioms and features used

- Before using a Go feature in code, explain what it is and why it fits this problem
- Cover the specific language concepts the stage exercises (listed per-stage below)
- Show small isolated examples of the Go concept before applying it to the BitTorrent code
- Explain Go's concurrency model (goroutines, channels) as it comes up naturally — don't front-load all theory
- When a third-party package is introduced, explain what it does and why we chose it over stdlib or alternatives

### 3. Build incrementally with tests

- Write tests first when practical — especially for codec and parser stages
- Each stage should compile and pass tests independently
- Use `go test ./...` after every meaningful change
- Each stage adds a CLI subcommand so progress is tangible

## Stage-specific teaching notes

### Stage 1 — Bencode Codec
- **BitTorrent:** Bencode format (strings, ints, lists, dicts), why it exists (deterministic serialization), comparison to JSON
- **Go:** custom types with methods, `switch` statements, `(value, error)` return pattern, `_test.go` files, `[]byte` for binary data, `sort.Strings` for deterministic dict key ordering

### Stage 2 — Torrent File Parser
- **BitTorrent:** `.torrent` anatomy (announce, info dict, pieces, single vs multi-file), info_hash = SHA-1 of raw bencoded info dict
- **Go:** structs, `os.ReadFile`, type assertions, `crypto/sha1` (stdlib), `fmt.Stringer` interface for pretty printing

### Stage 3 — Tracker Communication
- **BitTorrent:** HTTP tracker announce protocol, query parameters, compact peer encoding (6 bytes per peer), tracker response fields
- **Go:** `net/http` (stdlib), `net/url` for query encoding, `fmt.Errorf` with `%w` for error wrapping, `errors.Is`/`errors.As`

### Stage 4 — Peer Wire Protocol
- **BitTorrent:** TCP handshake (68 bytes), message framing (4-byte big-endian length prefix + message ID), all BEP 3 message types, peer state machine (choked/interested)
- **Go:** `net.Conn`, `encoding/binary` for big-endian I/O, `bufio.Reader`/`bufio.Writer`, `io.ReadFull`, state machines with struct + iota constants

### Stage 5 — Single Piece Download
- **BitTorrent:** Block requests (16 KiB), piece ↔ block relationship, SHA-1 piece verification, request pipeline depth
- **Go:** channels for coordination, `bytes.Buffer`, `crypto/sha1` streaming hash, `io.Writer` composability

### Stage 6 — Full File Download
- **BitTorrent:** Rarest-first piece selection, concurrent peer connections, bitfield tracking, file assembly from pieces, multi-file boundary math
- **Go:** goroutines, `sync.Mutex` / `sync.RWMutex`, `select` statement, `sync.WaitGroup`, `context.Context` for cancellation

### Stage 7 — Seeding
- **BitTorrent:** Responding to incoming connections, serving piece data, upload-side announce events
- **Go:** `net.Listener`, accept loop, goroutine-per-connection, graceful shutdown with `context.Context` + `os/signal`

### Stage 8 — UDP Tracker (BEP 15)
- **BitTorrent:** UDP connect → announce → scrape flow, transaction IDs, 15×2^n timeout formula
- **Go:** `net.UDPConn`, `time.After` for timeouts, `encoding/binary` for binary serialization, retry loops with backoff

### Stage 9 — Magnet Links (BEP 9 + 10)
- **BitTorrent:** Extension protocol handshake, extended message IDs, ut_metadata extension, magnet URI scheme
- **Go:** extending existing message types, `net/url` for URI parsing, interfaces for extensibility

### Stage 10 — DHT (BEP 5)
- **BitTorrent:** Kademlia algorithm, XOR distance metric, k-buckets, iterative lookups, token-based announce, routing table maintenance
- **Go:** `sync.RWMutex`-protected routing table, concurrent UDP RPC with goroutines, `map` for routing, background goroutines for table maintenance

### Stage 11 — PEX & Multi-Tracker (BEP 11 + 12)
- **BitTorrent:** PEX message format (added/dropped peers), tracker tier logic, combining peer sources
- **Go:** interfaces for `PeerSource` abstraction, fan-in pattern with channels to merge async peer sources

### Stage 12 — Polish & Advanced
- **BitTorrent:** Private flag (BEP 27), fast extension (BEP 6), tit-for-tat choking, endgame mode
- **Go:** `pprof` for profiling, `log/slog` for structured logging, integration testing with `TestMain`, `go doc` documentation

## Code conventions

- Package-per-directory structure: `bencode/`, `torrent/`, `tracker/`, etc.
- Errors: define sentinel errors and custom error types per package, wrap with `fmt.Errorf("context: %w", err)`
- Tests live in `*_test.go` alongside source files, plus `tests/` for integration
- No `panic` in non-test code — always return errors
- Use `gofmt` and `go vet` before every commit
- Naming: Go conventions (camelCase unexported, PascalCase exported, short receiver names)

## CLI structure (cobra or stdlib)

```
peer-pressure info <file.torrent>              # Stage 2
peer-pressure peers <file.torrent>             # Stage 3
peer-pressure handshake <file.torrent>         # Stage 4
peer-pressure download-piece <file.torrent> …  # Stage 5
peer-pressure download <file.torrent|magnet>   # Stage 6+9
peer-pressure seed <file.torrent> <data_path>  # Stage 7
peer-pressure create <path> -t <tracker_url>   # Stage 12
```
