# BEP 47 — Padding Files and Extended File Attributes

> Reference: <https://www.bittorrent.org/beps/bep_0047.html>

---

## 1. Summary

BEP 47 extends the info dictionary with two features: **padding files** and **extended file attributes**. Together they solve a practical problem with multi-file torrents: pieces that span file boundaries.

**The problem:** In standard BitTorrent, pieces are fixed-size chunks across the concatenated file data. A single piece often contains the tail of one file and the head of the next. This means:
- You can't download a single file without also downloading parts of adjacent files.
- You can't verify a file's integrity without having the full pieces at its boundaries.
- Deleting one file corrupts the pieces shared with neighboring files.

**Padding files** fix this by inserting zero-filled placeholder entries between real files so that each real file starts at a piece boundary. The padding file's length is chosen to round up the previous file's end to the next piece-length multiple.

**Extended file attributes** add an `attr` key to file entries, carrying flags like `p` (padding), `x` (executable), `h` (hidden), and `l` (symlink). This is also the foundation for BEP 52's `file tree` structure.

---

## 2. Protocol Specification

### 2.1 Padding Files

A padding file is a regular entry in the `files` list with the `attr` key containing the character `p`.

```
d
  ...
  4:infod
    5:filesld
      6:lengthi1048576e    ← real file: 1 MiB
      4:pathl9:video.mp4e
    ed
      6:lengthi0e          ← padding: 0 bytes (file was already aligned)
      4:pathl19:.pad/0e
      4:attr1:p
    ed
      6:lengthi524288e     ← real file: 512 KiB
      4:pathl9:audio.mp3e
    ed
      6:lengthi524288e     ← padding: 512 KiB to fill piece
      4:pathl19:.pad/524288e
      4:attr1:p
    ee
    ...
  e
e
```

**Rules for padding files:**

1. The `attr` key must contain the character `p`.
2. Padding files must be zero-filled. Clients **must not** write their data to disk.
3. The `path` is typically `.pad/<length>` but the path itself is irrelevant — only the `attr` flag matters for identification.
4. A padding file's length, when added to the preceding real file's length, must sum to a multiple of `piece length`. This ensures the next real file starts at a piece boundary.
5. A padding file with length 0 is valid (when the preceding file already ends at a piece boundary).
6. Padding files must not be the first or last entry in the file list.

**Byte layout example** (piece length = 256 KiB):

```
Piece boundaries:  |     0      |   256 KiB  |   512 KiB  |   768 KiB  |  1024 KiB  |
                   |            |            |            |            |            |
File data:         |◄── video.mp4 (300 KiB) ──►|◄pad 212K►|◄── audio.mp3 (512 KiB) ──►|
                   |            |            |            |            |            |
Pieces:            |  piece 0   |  piece 1   |  piece 2   |  piece 3   |
                   |            |   ▲        |            |            |
                   |            |   │ padding fills this gap           |
                   |            |   │ so audio.mp3 starts at piece 2  |
```

### 2.2 Extended File Attributes

Each file entry in the `files` list may contain an `attr` key with a string of flag characters:

| Flag | Meaning | Notes |
|---|---|---|
| `p` | Padding file | Zero-filled, not written to disk |
| `x` | Executable | Set the executable bit on Unix systems |
| `h` | Hidden | Set the hidden attribute on Windows / dot-prefix on Unix |
| `l` | Symlink | File is a symbolic link; target path in `symlink path` key |

**Multiple flags** can be combined in a single `attr` value. For example, `attr: "xh"` means executable and hidden.

#### Symlinks

When `attr` contains `l`, the file entry must also include a `symlink path` key:

```
d
  6:lengthi0e
  4:pathl7:link.txte
  4:attr1:l
  12:symlink pathl10:../real.txte
e
```

The `symlink path` value is a list of path components (same format as `path`). The symlink's `length` should be 0.

**Security consideration:** Clients must validate that symlink targets don't escape the torrent's root directory (no `..` traversal beyond root). Malicious torrents could use symlinks to overwrite arbitrary files.

### 2.3 Per-File SHA-1 Hash

BEP 47 introduces an optional `sha1` key per file entry containing the SHA-1 hash of the complete file contents (not the piece hashes):

```
d
  6:lengthi1048576e
  4:pathl9:video.mp4e
  4:sha120:<20 raw bytes>
e
```

This allows clients to verify individual file integrity without checking all pieces. Useful when resuming downloads or verifying selective downloads.

### 2.4 File Tree Structure

BEP 47 also defines `file tree` as an alternative to the flat `files` list. This is the structure used by BEP 52 (v2 torrents) and hybrid torrents.

```
d
  9:file treed
    9:video.mp4d
      0:d
        6:lengthi1048576e
        11:pieces root32:<32 bytes>
      e
    e
    9:audio.mp3d
      0:d
        6:lengthi524288e
        11:pieces root32:<32 bytes>
      e
    e
  e
e
```

**Structure rules:**
- Directories are represented as dicts with named keys for each child.
- Files are represented as dicts with a single empty-string key `""` whose value is a dict containing `length` and optionally `pieces root`, `attr`, etc.
- The tree is traversed depth-first to produce the canonical file ordering.
- File attributes (`attr`, `sha1`, `symlink path`) apply to the file's `""` dict.

### 2.5 Impact on Piece Selection

When downloading with padding-aware logic:

1. **Skip padding files during disk writes.** When assembling pieces to disk, zero out or skip the byte ranges corresponding to padding files.
2. **Selective download benefits.** Because each real file starts at a piece boundary, the client can download only the pieces for a specific file without pulling data from adjacent files.
3. **Piece verification is unchanged.** The SHA-1 piece hashes in the `pieces` field include the padding bytes. When verifying, the padding data (all zeros) must be included in the hash input.

---

## 3. Implementation Plan

### 3.1 Files to Modify

**`torrent/torrent.go`** — Extend the `File` struct and parsing logic:

```go
type File struct {
    Length      int      // file size in bytes
    Path        []string // path components
    Attr        string   // BEP 47 attribute flags ("p", "x", "h", "l", "xh", etc.)
    SHA1        [20]byte // optional per-file SHA-1 hash
    HasSHA1     bool     // true if SHA1 field is populated
    SymlinkPath []string // target path if Attr contains "l"
}

// IsPadding reports whether this file is a BEP 47 padding file.
func (f *File) IsPadding() bool {
    return strings.Contains(f.Attr, "p")
}

// IsExecutable reports whether this file has the executable attribute.
func (f *File) IsExecutable() bool {
    return strings.Contains(f.Attr, "x")
}

// IsHidden reports whether this file has the hidden attribute.
func (f *File) IsHidden() bool {
    return strings.Contains(f.Attr, "h")
}

// IsSymlink reports whether this file is a symbolic link.
func (f *File) IsSymlink() bool {
    return strings.Contains(f.Attr, "l")
}
```

Update `parseInfo()` to extract `attr`, `sha1`, and `symlink path` from each file entry dict.

Add a helper method to `Torrent`:

```go
// RealFiles returns only the non-padding files in order.
func (t *Torrent) RealFiles() []File

// PiecesForFile returns the piece index range [start, end) that covers the given
// file index. With BEP 47 padding, each real file maps to a clean piece range.
func (t *Torrent) PiecesForFile(fileIdx int) (start, end int)
```

**`download/session.go`** — When writing pieces to disk, skip byte ranges belonging to padding files.

**`download/piece.go`** — Add padding-awareness to piece-to-file offset mapping (the `BlockSize` and `BlockCount` logic already exists here; add file-boundary awareness).

### 3.2 Files to Create

**`torrent/filetree.go`** — Parser for the `file tree` dict structure:

```go
// ParseFileTree converts a BEP 47 / BEP 52 "file tree" dict into a flat
// list of File entries, ordered by depth-first traversal of the tree.
func ParseFileTree(tree bencode.Dict) ([]File, error)
```

**`torrent/filetree_test.go`** — Tests for file tree parsing.

### 3.3 Key Functions

```go
// ParseFileTree flattens a "file tree" dict into a sorted []File list.
func ParseFileTree(tree bencode.Dict) ([]File, error)

// ValidatePadding checks that padding files correctly align real files
// to piece boundaries. Returns an error describing the first violation.
func ValidatePadding(files []File, pieceLength int) error

// FileOffset returns the byte offset within the concatenated torrent data
// where the given file starts.
func (t *Torrent) FileOffset(fileIdx int) int64

// PiecesForFile returns the [start, end) piece range covering a file.
func (t *Torrent) PiecesForFile(fileIdx int) (start, end int)

// RealFiles returns the subset of files that are not padding.
func (t *Torrent) RealFiles() []File

// ValidateSymlinkTarget checks that a symlink path doesn't escape
// the torrent root directory. Returns an error if it does.
func ValidateSymlinkTarget(torrentRoot string, symlinkPath []string) error
```

### 3.4 Package Placement

All parsing and metadata logic lives in `torrent/`. The download-side changes (skipping padding on disk writes) go in `download/`.

---

## 4. Dependencies

| BEP | Relationship |
|---|---|
| **BEP 3** | Base protocol — defines the `files` list and `pieces` hash structure that BEP 47 extends |
| **BEP 52** | BitTorrent v2 — uses `file tree` from BEP 47 as its primary file structure, and makes padding implicit |
| **BEP 53** | File selection — BEP 47 padding makes per-file selection clean since files align to piece boundaries |
| **BEP 39** | Updating torrents — `file tree` can be used in feed-based torrent updates |

### Internal Dependencies

- `torrent.File` — the struct being extended with `Attr`, `SHA1`, `SymlinkPath`
- `torrent.parseInfo()` — the function that parses file entries from the info dict
- `bencode.Dict` / `bencode.List` — for parsing the `file tree` structure
- `download.File()` — needs to skip padding files during disk assembly

---

## 5. Testing Strategy

### 5.1 Parsing Tests (`torrent/torrent_test.go`)

**`TestParseFileAttrs`** — Construct bencoded file entries with `attr` keys:
- `attr: "p"` → `File.IsPadding() == true`
- `attr: "x"` → `File.IsExecutable() == true`
- `attr: "xh"` → both `IsExecutable()` and `IsHidden()` true
- `attr: "l"` with `symlink path` → `File.IsSymlink() == true`, `SymlinkPath` populated
- No `attr` key → all attribute methods return false

**`TestParseFileSHA1`** — File entry with `sha1` key:
- 20-byte SHA-1 present → `HasSHA1 == true`, `SHA1` matches
- No `sha1` key → `HasSHA1 == false`
- Wrong-length `sha1` → parse error

**`TestPaddingAlignment`** — Verify `ValidatePadding`:
- Correctly padded file list → no error
- Padding that doesn't align to piece boundary → error
- Padding file as first entry → error
- Padding file as last entry → error

### 5.2 File Tree Tests (`torrent/filetree_test.go`)

**`TestParseFileTreeFlat`** — Single level of files:
- Two files in a tree → produces a sorted `[]File` with correct paths

**`TestParseFileTreeNested`** — Directories with subdirectories:
- `dir/subdir/file.txt` → `File.Path == ["dir", "subdir", "file.txt"]`

**`TestParseFileTreeWithAttrs`** — File tree entries with `attr`, `sha1`:
- Attributes are correctly extracted from the `""` dict

**`TestParseFileTreeOrdering`** — Verify depth-first traversal produces deterministic ordering:
- Files are sorted lexicographically at each directory level

**`TestParseFileTreeEmpty`** — Empty file tree → empty `[]File`, no error

### 5.3 Functional Tests

**`TestPiecesForFileWithPadding`** — Given a padded multi-file torrent:
- `PiecesForFile(0)` returns piece range covering exactly the first real file
- `PiecesForFile(1)` returns the range for the second real file
- Ranges don't overlap (thanks to padding alignment)

**`TestRealFilesFilter`** — Verify `RealFiles()` excludes padding entries:
- 5-entry file list with 2 padding files → `RealFiles()` returns 3 entries

**`TestSymlinkValidation`** — Security tests for symlink targets:
- `["subdir", "file.txt"]` → valid
- `["..", "etc", "passwd"]` → rejected (escapes root)
- `["subdir", "..", "..", "escape"]` → rejected
- `["normal", "file"]` → valid

### 5.4 Download Integration

**`TestDownloadSkipsPadding`** — Verify that padding bytes are not written to real files:
- Create a mock 3-file torrent with padding
- Download it and verify that the output directory contains only the real files
- Verify file contents match the original data (no padding bytes mixed in)
