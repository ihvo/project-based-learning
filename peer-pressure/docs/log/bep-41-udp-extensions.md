# BEP 41: UDP Tracker Protocol Extensions

## What It Does

BEP 41 adds an extension mechanism to the UDP tracker protocol (BEP 15).
Before this, the path and query components of UDP tracker URLs were invisible
to the tracker — `udp://tracker.example.com:80/dir?auth=token` and
`udp://tracker.example.com:80` were indistinguishable.

### The Extension Format

Extensions are appended after the 98-byte announce request. Each option is:

| Field | Size | Description |
|-------|------|-------------|
| Type  | 1 byte | Option identifier |
| Length | 1 byte | Data length (omitted for types ≤ 0x01) |
| Data  | variable | Option payload |

Three option types are defined:

- **EndOfOptions (0x00)** — stop parsing, no length byte
- **NOP (0x01)** — padding, no length byte
- **URLData (0x02)** — carries path+query from tracker URL

The key design rule: types ≤ 0x01 have no length byte, types ≥ 0x02 always
have one. This allows parsers to skip unknown future option types safely.

### URLData Chunking

Since the length byte is a single byte (max 255), paths longer than 255
bytes are split into multiple URLData options. The tracker concatenates
all URLData payloads to reconstruct the full path+query.

### What We Implemented

- **`udpParseURL()`** — replaces `udpHostFromURL()`, returns both host and
  the path+query string for BEP 41
- **`encodeURLDataOption()`** — encodes the path+query as one or more
  URLData option chunks (with automatic splitting at 255 bytes)
- **`udpAnnounce()`** — now appends BEP 41 extension bytes after the 98-byte
  announce request

Even empty URLs emit `[0x02, 0x00]` to signal BEP 41 support to the tracker.

## Go Idioms

### Slice Append for Variable-Length Packets

```go
req := make([]byte, 98)
// ... fill fixed fields ...
req = append(req, encodeURLDataOption(pathQuery)...)
```

The old code used `var req [98]byte` (fixed array). Changing to `make([]byte, 98)`
and using `append()` lets us grow the packet for extension bytes without a
second allocation or buffer copy. The `...` operator unpacks the option slice
into individual bytes for append.

### Multiple Return Values for URL Parsing

```go
func udpParseURL(rawURL string) (host, pathQuery string, err error) {
```

Named return values document the function's contract. The caller destructures
cleanly: `host, pathQuery, err := udpParseURL(url)`. When the caller only
needs the host (like `udpScrape`), it discards pathQuery: `host, _, err := ...`.

### Option Encoding with Chunking

```go
for len(data) > 0 {
    chunk := data
    if len(chunk) > 255 {
        chunk = data[:255]
    }
    opts = append(opts, optURLData, byte(len(chunk)))
    opts = append(opts, chunk...)
    data = data[len(chunk):]
}
```

This pattern processes a byte slice in fixed-size windows. `data[:255]` takes
the first 255 bytes, then `data = data[len(chunk):]` advances past the
consumed bytes. The loop terminates when `data` is empty. Using `byte(len(chunk))`
is safe because we've capped the chunk at 255, which fits in a single byte.
