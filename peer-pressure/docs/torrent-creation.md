# Torrent Creation

> Build .torrent files from local data, completing the content lifecycle:
> create → share → download → seed.

---

## 1. Summary

Torrent creation is the inverse of torrent parsing. Given a file or directory
on disk, plus a tracker URL and optional parameters, the client:

1. Walks the file tree and collects the file list.
2. Chooses a piece length (auto or user-specified).
3. Reads all data sequentially and hashes each piece with SHA-1.
4. Builds the info dictionary (name, piece length, pieces, files/length).
5. Computes the info_hash.
6. Wraps in the top-level dictionary (announce, creation date, etc.).
7. Bencodes and writes the .torrent file.

This is a local-only operation — no network activity. The output is a standard
.torrent file that any BitTorrent client can use.

---

## 2. Design

### 2.1 Input parameters

| Parameter | Required | Description |
|-----------|----------|-------------|
| `path` | Yes | File or directory to create torrent from |
| `tracker` | Yes | Primary tracker URL (`announce` field) |
| `trackers` | No | Additional tracker tiers (`announce-list`) |
| `output` | No | Output .torrent path (default: `<name>.torrent`) |
| `pieceLength` | No | Piece length in bytes (must be power of 2, default: auto) |
| `comment` | No | Free-text comment field |
| `private` | No | Set the `private` flag (BEP 27) — disables DHT/PEX |
| `webseeds` | No | Web seed URLs (`url-list`, BEP 19) |
| `createdBy` | No | Creator string (default: `"Peer Pressure <version>"`) |

### 2.2 Auto piece length selection

The target is approximately **1500 pieces** for the torrent. The piece length
must be a power of 2, minimum 16 KiB, maximum 16 MiB:

```
total_size = sum of all file sizes

target_piece_len = total_size / 1500
piece_len = next_power_of_2(target_piece_len)
piece_len = clamp(piece_len, 16 * 1024, 16 * 1024 * 1024)
```

Examples:

| Total size | Auto piece length | Pieces |
|------------|-------------------|--------|
| 10 MiB | 16 KiB | 640 |
| 700 MiB | 512 KiB | 1400 |
| 4 GiB | 4 MiB | 1024 |
| 50 GiB | 32 MiB → clamped to 16 MiB | 3200 |

### 2.3 File collection

**Single file:**

```
stat(path) → is regular file
name = basename(path)
length = file size
→ single-file mode: info dict gets "length" field
```

**Directory:**

```
walk(path) in sorted order (deterministic):
    skip hidden files (starting with '.')
    skip empty files (optional, but common)
    for each regular file:
        record relative path components and size

name = basename(path)  // directory name
files = [{path: ["subdir", "file.txt"], length: N}, ...]
→ multi-file mode: info dict gets "files" list
```

File paths are stored as lists of path components (not slash-separated
strings). The walk order must be deterministic — sort by path components
lexicographically.

### 2.4 Piece hashing

Pieces are hashed over the concatenation of all file data in order, regardless
of file boundaries:

```
hasher = sha1.New()
pieces = []
bytes_in_piece = 0

for each file in order:
    open file
    read in chunks:
        write chunk to hasher
        bytes_in_piece += len(chunk)
        
        if bytes_in_piece == piece_length:
            pieces = append(pieces, hasher.Sum(nil))
            hasher.Reset()
            bytes_in_piece = 0

if bytes_in_piece > 0:
    pieces = append(pieces, hasher.Sum(nil))  // final partial piece
```

The `pieces` field in the info dict is the concatenation of all 20-byte
SHA-1 hashes: `pieces = hash0 + hash1 + hash2 + ...`

### 2.5 Info dictionary structure

**Single-file:**

```
info = {
    "name":         <string, filename>,
    "piece length": <integer>,
    "pieces":       <byte string, concatenated SHA-1 hashes>,
    "length":       <integer, file size>
}
```

**Multi-file:**

```
info = {
    "name":         <string, directory name>,
    "piece length": <integer>,
    "pieces":       <byte string, concatenated SHA-1 hashes>,
    "files": [
        {"length": <integer>, "path": ["subdir", "file.txt"]},
        {"length": <integer>, "path": ["another.dat"]},
        ...
    ]
}
```

**Optional fields in info dict:**

- `"private": 1` — BEP 27, disables DHT and PEX.

### 2.6 Top-level dictionary

```
torrent = {
    "announce":      <string, primary tracker URL>,
    "announce-list": [[tracker1], [tracker2, tracker3]],  // BEP 12
    "creation date": <integer, Unix timestamp>,
    "created by":    <string, e.g. "Peer Pressure 0.1.0">,
    "comment":       <string, optional>,
    "url-list":      [<string, webseed URL>, ...],        // BEP 19
    "info":          <info dict>
}
```

### 2.7 Info hash computation

```
raw_info = bencode(info_dict)
info_hash = sha1(raw_info)
```

This hash is deterministic because bencode has canonical encoding (sorted dict
keys, no optional whitespace). The info_hash is what peers use to identify
the torrent.

### 2.8 Output

The complete top-level dictionary is bencoded and written to a file. The
output filename defaults to `<name>.torrent` in the current directory.

---

## 3. Implementation Plan

### 3.1 Package placement

New file in the existing `torrent/` package, since it's the inverse of
`torrent.Parse`.

### 3.2 New files

| File | Purpose |
|------|---------|
| `torrent/create.go` | `Create` function, file walking, piece hashing, info dict construction |
| `torrent/create_test.go` | Unit tests for torrent creation |

### 3.3 Modified files

| File | Changes |
|------|---------|
| `cmd/peer-pressure/main.go` | Add `create` subcommand |

### 3.4 Key types

```go
// torrent/create.go

// CreateParams holds the parameters for creating a .torrent file.
type CreateParams struct {
    Path         string     // file or directory to hash
    Tracker      string     // primary tracker URL
    Trackers     [][]string // additional tracker tiers (BEP 12)
    PieceLength  int        // 0 = auto-select
    Comment      string     // optional
    Private      bool       // BEP 27
    WebSeeds     []string   // BEP 19 url-list
    CreatedBy    string     // default: "Peer Pressure <version>"
    Output       string     // output path (default: <name>.torrent)
}

// CreateResult holds the output of torrent creation.
type CreateResult struct {
    Path      string     // path to the written .torrent file
    InfoHash  [20]byte   // computed info hash
    Name      string     // torrent name
    NumPieces int        // total pieces
    TotalSize int64      // total bytes across all files
    Files     int        // number of files
}

// fileEntry represents a single file in the torrent.
type fileEntry struct {
    path   []string // path components relative to torrent root
    length int64    // file size in bytes
    osPath string   // absolute path on disk for reading
}
```

### 3.5 Key functions

```go
// torrent/create.go

// Create builds a .torrent file from the given parameters, writes it to disk,
// and returns the result including the computed info hash.
func Create(params CreateParams) (*CreateResult, error)

// collectFiles walks the path and returns the sorted file list.
func collectFiles(root string) ([]fileEntry, error)

// autoPieceLength selects a piece length targeting ~1500 pieces.
func autoPieceLength(totalSize int64) int

// hashPieces reads all files sequentially and computes SHA-1 hashes for each
// piece. Returns the concatenated hash bytes.
func hashPieces(files []fileEntry, pieceLength int) ([]byte, error)

// buildInfoDict constructs the bencoded info dictionary.
func buildInfoDict(name string, pieceLength int, pieces []byte, files []fileEntry, private bool) bencode.Dict

// buildMetainfo constructs the complete top-level dictionary.
func buildMetainfo(info bencode.Dict, params CreateParams) bencode.Dict

// nextPowerOf2 returns the smallest power of 2 >= n.
func nextPowerOf2(n int64) int
```

### 3.6 CLI integration

```
peer-pressure create <path> -t <tracker_url> [-o output.torrent] \
    [--piece-length 512K] [--private] [--webseed URL] [--comment "text"]

1. Validate path exists
2. Parse piece length (supports K, M suffixes)
3. Call torrent.Create(params)
4. Print summary: name, info_hash, pieces, size, output path
```

---

## 4. Dependencies

| Dependency | Type | Notes |
|------------|------|-------|
| `bencode/` package | Required | `bencode.Encode`, `bencode.Dict`, `bencode.String`, `bencode.Int`, `bencode.List` for building the .torrent structure |
| `torrent/` package | Extended | New file in existing package; shares types like `File` |
| `crypto/sha1` | Go stdlib | Piece hashing |
| `os` / `path/filepath` | Go stdlib | File walking, reading |
| `sort` | Go stdlib | Deterministic file ordering |
| `time` | Go stdlib | `creation date` field (Unix timestamp) |

No new external dependencies.

---

## 5. Testing Strategy

### 5.1 Unit tests (`torrent/create_test.go`)

#### File collection

| Test | Description |
|------|-------------|
| `TestCollectFilesSingle` | Single file path, verify one entry with correct length and path components |
| `TestCollectFilesDirectory` | Directory with 3 files in subdirs, verify entries are sorted and paths are relative |
| `TestCollectFilesSkipHidden` | Directory containing `.hidden` file, verify it's excluded |
| `TestCollectFilesEmpty` | Empty directory, expect error |
| `TestCollectFilesNonexistent` | Path doesn't exist, expect error |
| `TestCollectFilesSorted` | Files named "b.txt", "a.txt", "c.txt", verify order is a, b, c |

#### Auto piece length

| Test | Description |
|------|-------------|
| `TestAutoPieceLengthSmall` | 1 MiB total → 16 KiB (minimum) |
| `TestAutoPieceLengthMedium` | 700 MiB → 512 KiB |
| `TestAutoPieceLengthLarge` | 4 GiB → 4 MiB |
| `TestAutoPieceLengthHuge` | 100 GiB → 16 MiB (capped at maximum) |
| `TestAutoPieceLengthPowerOf2` | Verify result is always a power of 2 for various sizes |

#### Piece hashing

| Test | Description |
|------|-------------|
| `TestHashPiecesSingleFile` | 64 KiB file with 16 KiB piece length → 4 pieces, verify SHA-1 hashes match manual computation |
| `TestHashPiecesMultiFile` | Two files totaling 48 KiB with 16 KiB pieces → 3 pieces, verify the middle piece spans the file boundary |
| `TestHashPiecesPartialLastPiece` | 40 KiB file with 16 KiB pieces → 3 pieces (last one is 8 KiB), verify hash of partial piece |
| `TestHashPiecesExactFit` | File size is exact multiple of piece length, verify no partial piece |
| `TestHashPiecesLargeFile` | 1 MiB file, verify piece count = ceil(1M / pieceLen) |

#### Info dict construction

| Test | Description |
|------|-------------|
| `TestBuildInfoDictSingleFile` | Single file, verify "length" present, "files" absent |
| `TestBuildInfoDictMultiFile` | Multiple files, verify "files" list present, "length" absent |
| `TestBuildInfoDictPrivate` | `private=true`, verify `"private": 1` in info dict |
| `TestBuildInfoDictNotPrivate` | `private=false`, verify "private" key absent from info dict |

#### Round-trip test

| Test | Description |
|------|-------------|
| `TestCreateThenParse` | Create a .torrent from test data, then parse it with `torrent.Parse`, verify all fields match: name, piece length, piece hashes, file list, trackers, url-list |
| `TestCreateThenParseMultiFile` | Same as above but with a multi-file directory |
| `TestCreateInfoHashStable` | Create the same torrent twice, verify info_hash is identical (deterministic encoding) |

#### Full integration test

| Test | Description |
|------|-------------|
| `TestCreateVerifyDownload` | Create .torrent from test data → compute expected hashes → parse .torrent → for each piece, read data and verify SHA-1 matches. (Does not require network — verifies local consistency.) |

### 5.2 CLI tests

| Test | Description |
|------|-------------|
| `TestCreateCLISingleFile` | Run `peer-pressure create <file> -t http://tracker`, verify .torrent file is created and parseable |
| `TestCreateCLIDirectory` | Run `peer-pressure create <dir> -t http://tracker`, verify multi-file torrent created |
| `TestCreateCLICustomPieceLength` | Run with `--piece-length 256K`, verify piece length in output is 262144 |
| `TestCreateCLIPrivateFlag` | Run with `--private`, verify private flag set in parsed torrent |
| `TestCreateCLIWebSeed` | Run with `--webseed http://example.com/`, verify url-list in parsed torrent |
| `TestCreateCLIOutput` | Run with `-o custom.torrent`, verify file is written to that path |
