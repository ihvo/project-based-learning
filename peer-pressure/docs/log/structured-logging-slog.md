# Structured Logging with `log/slog`

## What We Built

Replaced scattered `fmt.Printf` operational messages with Go 1.21's structured
logging package `log/slog`. The CLI now has three verbosity levels controlled
by global flags:

```
peer-pressure --verbose download file.torrent   # shows INFO messages
peer-pressure --debug download file.torrent     # shows DEBUG messages
peer-pressure download file.torrent             # default: WARN only (quiet)
```

**Files modified:**
- `cmd/peer-pressure/main.go` — global flag parsing, `slog.SetDefault()`, all operational prints → slog
- `seed/seed.go` — seeder startup message → `slog.Info`

## Design Decisions

### What Stays as `fmt.Printf`

Not everything should be a log message. We kept `fmt.Printf` for:

| Category | Example | Reason |
|----------|---------|--------|
| CLI output | "Total unique peers: 5" | This is the *result* the user asked for |
| Usage/help | flag descriptions | Not operational; it's UI |
| ANSI progress | download progress bars | Terminal rendering, not structured data |
| Torrent info | `t.String()` | Formatted display output |
| Fatal errors | `fatal("no peers found")` | Goes to stderr, exits immediately |
| Created file | "Created foo.torrent" | Final result confirmation |

### What Becomes `slog`

Operational messages that describe *what the program is doing*, not *what it
found for you*:

| Before | After |
|--------|-------|
| `fmt.Printf("Announcing to %s...\n", url)` | `slog.Debug("announcing", "tracker", url)` |
| `fmt.Printf("DHT: bootstrapping...\n")` | `slog.Info("DHT: bootstrapping")` |
| `fmt.Printf("  tracker error: %v\n", err)` | `slog.Debug("tracker error", "error", err)` |
| `fmt.Printf("  got %d peers\n", n)` | `slog.Info("tracker responded", "peers", n)` |

### Level Assignment

- **DEBUG**: Per-peer, per-tracker details (announcing, individual errors, metadata fetch)
- **INFO**: High-level progress (bootstrapping, peer counts, metadata acquired)
- **WARN**: Non-fatal degradation (DHT bind failure, bootstrap failure)
- **ERROR**: Not used — fatal errors `os.Exit(1)` via `fatal()`

## Go Idioms Used

### Pre-subcommand Flag Extraction

Go's `flag` package works per-FlagSet. We needed `--verbose`/`--debug` to work
regardless of which subcommand follows. Instead of adding these flags to every
FlagSet, we scan `os.Args` before dispatch:

```go
var filtered []string
logLevel := slog.LevelWarn
for _, a := range os.Args[1:] {
    switch a {
    case "--verbose":
        logLevel = slog.LevelInfo
    case "--debug":
        logLevel = slog.LevelDebug
    default:
        filtered = append(filtered, a)
    }
}
```

This removes the flags from the arg list before passing to subcommand FlagSets.
It's a simple pattern that avoids the complexity of nested flag parsing.

### `slog.SetDefault` — Configure Once, Use Everywhere

```go
slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
    Level: logLevel,
})))
```

After this call, all `slog.Info()`, `slog.Debug()` etc. throughout the program
(including library packages like `seed/`) use this handler. No need to pass
loggers around.

Key design choices:
- **`os.Stderr`**: Logs go to stderr, keeping stdout clean for piping
- **`slog.NewTextHandler`**: Human-readable `key=value` format, good for terminal
- **Level filtering**: Messages below the configured level are discarded cheaply

### Structured Key-Value Pairs

The core slog idiom — data as key-value pairs, not format strings:

```go
// Before: positional formatting, easy to mismatch %v with args
fmt.Printf("  got %d peers (seeders: %d, leechers: %d)\n",
    len(r.resp.Peers), r.resp.Complete, r.resp.Incomplete)

// After: named fields, order doesn't matter, machine-parseable
slog.Info("tracker responded", "tracker", r.url, "peers", len(r.resp.Peers),
    "seeders", r.resp.Complete, "leechers", r.resp.Incomplete)
```

Benefits:
- **Grep-friendly**: `grep "tracker responded"` finds all tracker responses
- **Machine-parseable**: Each field has a name, can be JSON with `slog.NewJSONHandler`
- **No format bugs**: Can't accidentally swap `%d` positions
- **Cheap when filtered**: If level is WARN, the args aren't even formatted

### Zero-Cost Abstraction for Disabled Levels

```go
slog.Debug("announcing", "tracker", url)
```

When the log level is INFO or higher, this call:
1. Checks the handler's level (fast integer comparison)
2. Returns immediately — no string formatting, no allocation

This means sprinkling `slog.Debug` throughout hot paths is essentially free
in production (default WARN level).

## The slog Package Architecture

```
                  ┌──────────────┐
                  │  slog.Info() │  package-level functions
                  │  slog.Debug()│  (use default logger)
                  └──────┬───────┘
                         │
                  ┌──────▼───────┐
                  │   *Logger    │  carries a Handler
                  └──────┬───────┘
                         │
              ┌──────────▼──────────┐
              │     Handler iface   │
              │  Enabled(level) bool│  ← fast level check
              │  Handle(Record) err │  ← format + write
              └──────────┬──────────┘
                         │
         ┌───────────────┼───────────────┐
         ▼               ▼               ▼
   TextHandler     JSONHandler     Custom Handler
   key=value       {"key":"val"}   (whatever you want)
```

The `Handler` interface has just two methods that matter:
- `Enabled(level)` — can the message be discarded cheaply?
- `Handle(record)` — format and write the log entry

This makes it easy to swap between text (development) and JSON (production)
without changing any call sites.
