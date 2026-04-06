# Structured Logging

> Replaces ad-hoc `fmt.Printf` calls with Go's `log/slog` structured logging,
> giving consistent, filterable, machine-readable output across all packages.

---

## 1. Summary

Peer Pressure currently uses `fmt.Printf` / `fmt.Fprintf` for debug output,
status messages, and error reporting. This creates several problems:

- **No log levels** — debug noise is always on or requires manual commenting.
- **No structure** — parsing log output requires regex, not key-value lookup.
- **No per-subsystem control** — can't enable DHT debug logging without also
  flooding tracker messages.
- **stdout pollution** — log output interferes with the progress display.

The fix: adopt Go's `log/slog` package (stdlib since Go 1.21) with structured
key-value logging, per-subsystem logger instances, and level-based filtering.

### Log levels

| Level | Use for | Examples |
|-------|---------|----------|
| Debug | Protocol details, per-block events | `"block received" piece=42 begin=0 len=16384 peer=1.2.3.4:6881` |
| Info | Connections, piece completions, milestones | `"piece verified" piece=42 hash=abc123` |
| Warn | Retries, slow peers, recoverable issues | `"peer slow, rotating" addr=1.2.3.4:6881 speed=1024` |
| Error | Failures, unrecoverable issues | `"tracker announce failed" url=http://... err="timeout"` |

### CLI flags

| Flag | Level | Description |
|------|-------|-------------|
| (default) | Warn | Only warnings and errors |
| `-v` | Info | Connections, completions, tracker announces |
| `-vv` | Debug | Everything — protocol-level detail |
| `-q` | Error | Only errors |

---

## 2. Design

### 2.1 slog basics

`log/slog` provides structured logging with three core concepts:

1. **Logger** — the entry point: `slog.Info("msg", "key", value, ...)`
2. **Handler** — formats and outputs records (TextHandler for human-readable,
   JSONHandler for machine-readable)
3. **Attrs** — structured key-value pairs attached to each log record

```go
logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
    Level: slog.LevelInfo,
}))

logger.Info("piece verified", "piece", 42, "hash", hex.EncodeToString(hash[:8]))
// Output: time=2025-01-15T10:30:00Z level=INFO msg="piece verified" piece=42 hash=abc12345
```

### 2.2 Per-subsystem loggers

Each package gets its own logger with a `component` attribute, created by
calling `logger.With("component", "name")`:

```go
// In dht/node.go
func New(conn *net.UDPConn, logger *slog.Logger) *DHT {
    d := &DHT{
        log: logger.With("component", "dht"),
    }
}

// Usage within the dht package:
d.log.Debug("ping sent", "addr", addr.String())
d.log.Info("bootstrap complete", "nodes", rt.Len())
d.log.Warn("query timeout", "method", "find_node", "addr", addr.String())
```

### 2.3 Component list

| Component | Package | Typical volume |
|-----------|---------|----------------|
| `dht` | `dht/` | High (many KRPC messages) |
| `tracker` | `tracker/` | Low (periodic announces) |
| `peer` | `peer/` | High (per-message in debug) |
| `download` | `download/` | Medium (piece events) |
| `picker` | `download/picker.go` | Medium (piece selection) |
| `pool` | `download/pool.go` | Medium (peer rotation) |
| `webseed` | `download/webseed.go` | Low |
| `seed` | `seed/` | Medium (upload events) |
| `throttle` | `throttle/` | Low (rate changes only) |
| `magnet` | `magnet/` | Low |
| `torrent` | `torrent/` | Low |
| `main` | `cmd/peer-pressure/` | Low |

### 2.4 Logger propagation

The logger is created in `main()` and passed down through constructors:

```
main()
  │
  ├─ slog.New(TextHandler(os.Stderr, {Level: parsedLevel}))
  │
  ├─ dht.New(conn, logger)          → logger.With("component", "dht")
  ├─ download.File(ctx, cfg)        → cfg.Logger.With("component", "download")
  ├─ seed.New(cfg)                  → cfg.Logger.With("component", "seed")
  └─ tracker.Announce(url, params)  → params.Logger.With("component", "tracker")
```

Functions that don't need a logger parameter (e.g. pure encoding functions)
should not take one.

### 2.5 Output destination

- **slog** → writes to `os.Stderr` (never stdout).
- **Progress display** → writes directly to stderr via ANSI escape codes
  (existing behavior in `download/progress.go`).
- **Final output** (e.g. `info` subcommand) → writes to stdout.

This ensures `peer-pressure download ... > file` captures only the downloaded
file path, not log noise.

### 2.6 Handler configuration

Default handler is `slog.TextHandler` for human-readable output:

```
time=2025-01-15T10:30:00Z level=INFO component=dht msg="bootstrap complete" nodes=87
```

An optional `--log-json` flag switches to `slog.JSONHandler` for machine
parsing:

```json
{"time":"2025-01-15T10:30:00Z","level":"INFO","component":"dht","msg":"bootstrap complete","nodes":87}
```

### 2.7 Audit of existing print calls

Every `fmt.Printf`, `fmt.Fprintf`, and `fmt.Println` call in non-test Go
files must be audited and converted. The mapping:

| Current pattern | Replacement |
|-----------------|-------------|
| `fmt.Printf("connected to %s\n", addr)` | `logger.Info("connected", "addr", addr)` |
| `fmt.Printf("piece %d verified\n", idx)` | `logger.Info("piece verified", "piece", idx)` |
| `fmt.Fprintf(os.Stderr, "error: %v\n", err)` | `logger.Error("operation failed", "err", err)` |
| `fmt.Printf("debug: ...")` | `logger.Debug(...)` |
| Progress bar prints | Keep as-is (direct terminal output, not logging) |
| `info` subcommand output | Keep as `fmt.Printf` (user-facing output, not logging) |

---

## 3. Implementation Plan

### 3.1 Package placement

No new package needed. Logger initialization lives in `cmd/peer-pressure/main.go`.
Each existing package is modified to accept and use `*slog.Logger`.

Optionally, a thin `logging/` package can hold helper functions if needed
(e.g. a `NewLogger` convenience constructor), but this is not strictly
necessary since `slog` setup is a few lines.

### 3.2 New files

| File | Purpose |
|------|---------|
| `logging/logging.go` | `NewLogger(level, format)` convenience constructor, level parsing from CLI flags |
| `logging/logging_test.go` | Tests for logger construction and level parsing |

### 3.3 Modified files

| File | Changes |
|------|---------|
| `cmd/peer-pressure/main.go` | Add `-v`, `-vv`, `-q`, `--log-json` flags; create logger; pass to all subsystems |
| `dht/node.go` | Add `log *slog.Logger` field to `DHT`; accept in `New()`; replace `fmt.Printf` |
| `dht/krpc.go` | Accept logger or use from parent; log KRPC send/receive at Debug level |
| `tracker/tracker.go` | Accept logger parameter (or in `AnnounceParams`); log announce/response at Info |
| `peer/conn.go` | Add optional logger to `Conn`; log handshake at Info, messages at Debug |
| `download/session.go` | Add `Logger *slog.Logger` to `Config`; pass sub-loggers to pool, picker, progress |
| `download/pool.go` | Replace `fmt.Printf` with logger calls; log peer rotation at Info, evaluation at Debug |
| `download/picker.go` | Log piece picks at Debug |
| `download/progress.go` | Keep direct terminal writes for the progress bar (not slog) |
| `download/webseed.go` | Log webseed requests at Info, errors at Warn |
| `magnet/metadata.go` | Log metadata fetch progress at Debug |
| `seed/seed.go` | Accept logger in `Config`; log accept/disconnect at Info |
| `seed/upload.go` | Log request serving at Debug |

### 3.4 Key types

```go
// logging/logging.go

// Config holds logging configuration parsed from CLI flags.
type Config struct {
    Level  slog.Level // slog.LevelDebug, Info, Warn, Error
    JSON   bool       // true for JSON output
    Output io.Writer  // default: os.Stderr
}
```

### 3.5 Key functions

```go
// logging/logging.go

// NewLogger creates a configured slog.Logger from the given config.
func NewLogger(cfg Config) *slog.Logger

// ParseVerbosity converts CLI flag counts to slog.Level.
//   0 flags  → slog.LevelWarn  (default)
//   -v       → slog.LevelInfo
//   -vv      → slog.LevelDebug
//   -q       → slog.LevelError
func ParseVerbosity(v int, quiet bool) slog.Level
```

### 3.6 Integration pattern

Each package that currently uses `fmt.Printf` for logging follows this
pattern:

**Before:**
```go
func New(conn *net.UDPConn) *DHT {
    return &DHT{conn: conn}
}

// somewhere later:
fmt.Printf("bootstrap: added %d nodes\n", count)
```

**After:**
```go
func New(conn *net.UDPConn, logger *slog.Logger) *DHT {
    return &DHT{
        conn: conn,
        log:  logger.With("component", "dht"),
    }
}

// somewhere later:
d.log.Info("bootstrap complete", "nodes", count)
```

### 3.7 Backward compatibility

- If `logger` is `nil`, use `slog.Default()` as fallback (which discards by
  default in tests).
- Test files should not be modified unless they call functions that now require
  a logger parameter — in that case, pass `slog.Default()` or a discard logger.

---

## 4. Dependencies

| Dependency | Type | Notes |
|------------|------|-------|
| `log/slog` | Go stdlib (1.21+) | Core structured logging package |
| `os` | Go stdlib | `os.Stderr` for output |
| `io` | Go stdlib | `io.Writer` interface for output destination |
| Go 1.21+ | Runtime | Required for `log/slog`; `go.mod` already specifies 1.26.1 |

No new external dependencies. Every package in the project is modified to
accept a logger, but no new packages are depended upon.

---

## 5. Testing Strategy

### 5.1 Logger construction tests (`logging/logging_test.go`)

| Test | Description |
|------|-------------|
| `TestParseVerbosityDefault` | v=0, quiet=false → `slog.LevelWarn` |
| `TestParseVerbosityV` | v=1, quiet=false → `slog.LevelInfo` |
| `TestParseVerbosityVV` | v=2, quiet=false → `slog.LevelDebug` |
| `TestParseVerbosityQuiet` | v=0, quiet=true → `slog.LevelError` |
| `TestParseVerbosityQuietOverridesV` | v=2, quiet=true → `slog.LevelError` (quiet wins) |
| `TestNewLoggerText` | Create logger with JSON=false, write a record, verify text format on output |
| `TestNewLoggerJSON` | Create logger with JSON=true, write a record, verify valid JSON on output |
| `TestNewLoggerLevel` | Create logger with LevelWarn, emit Debug/Info/Warn/Error, verify only Warn+Error appear |

### 5.2 Level filtering tests

| Test | Description |
|------|-------------|
| `TestDebugSuppressedAtInfo` | Logger at Info level. Emit Debug message. Capture output. Verify nothing written. |
| `TestInfoVisibleAtInfo` | Logger at Info level. Emit Info message. Capture output. Verify message present. |
| `TestWarnVisibleAtDebug` | Logger at Debug level. Emit Warn message. Verify message present. |
| `TestErrorAlwaysVisible` | Logger at any level. Emit Error. Verify present. |

### 5.3 Component attribute tests

| Test | Description |
|------|-------------|
| `TestComponentAttribute` | Create logger, call `.With("component", "dht")`, emit message, verify `component=dht` in output |
| `TestSubLoggerInherits` | Create base logger at Info, derive sub-logger with `With`, verify sub-logger respects Info level |

### 5.4 Audit verification

| Test | Description |
|------|-------------|
| `TestNoPrintfInNonTestCode` | Grep all `.go` files (excluding `_test.go`, `progress.go`, and the `info`/`create` CLI output paths) for `fmt.Printf`, `fmt.Fprintf(os.Stderr`, `fmt.Println`. Verify zero matches. This is a lint-style test that runs in CI. |

### 5.5 Integration tests

| Test | Description |
|------|-------------|
| `TestDownloadWithVerboseLogging` | Run a download with logger at Debug level. Verify no panics, no goroutine leaks, and that structured log lines appear on stderr. |
| `TestSlogDoesNotPollutionStdout` | Run a subcommand with logging, capture stdout and stderr separately. Verify stdout has no slog output. |
| `TestExistingTestsStillPass` | After the migration, run `go test ./...` to verify no test breakage from added logger parameters. (This is the primary regression gate.) |

### 5.6 Migration checklist (manual/CI)

Run this after the migration to verify completeness:

```bash
# Find remaining fmt.Print calls in non-test code (should be zero, modulo
# intentional user-facing output in cmd/ and progress.go)
grep -rn 'fmt\.Printf\|fmt\.Fprintf\|fmt\.Println' --include='*.go' \
    --exclude='*_test.go' \
    --exclude='progress.go' \
    .

# Verify every package that has a constructor now accepts *slog.Logger
grep -rn 'func New(' --include='*.go' --exclude='*_test.go' . | \
    grep -v 'slog.Logger'
# ^ This should only match packages that legitimately don't need logging
#   (e.g. bencode, which is a pure codec)
```
