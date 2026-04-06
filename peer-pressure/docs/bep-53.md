# BEP 53 — Magnet URI Extension — Select Specific Files

> Reference: <https://www.bittorrent.org/beps/bep_0053.html>

---

## 1. Summary

BEP 53 adds a file selection parameter to magnet URIs, letting users specify which files from a multi-file torrent they want to download — before the metadata is even fetched.

**The problem it solves:** A user clicks a magnet link for a 50 GiB torrent but only wants one 700 MiB video file inside it. Without BEP 53, the client has to fetch the full metadata via BEP 9, then the user manually deselects files in the UI. With BEP 53, the intent is encoded directly in the URI.

**Format:** The `so` (select only) parameter is a comma-separated list of file indices and inclusive ranges:

```
magnet:?xt=urn:btih:HASH&so=0,2,4-6
```

This means: download file indices 0, 2, 4, 5, and 6 from the torrent's file list.

---

## 2. Protocol Specification

### 2.1 Parameter Format

The `so` parameter value is a string containing a comma-separated list of items. Each item is either:

- A single non-negative integer: `N` — selects file at index N
- An inclusive range: `N-M` — selects files at indices N, N+1, ..., M (where N ≤ M)

**Grammar:**

```
so       = item ("," item)*
item     = index | range
index    = DIGIT+
range    = index "-" index
DIGIT    = "0" | "1" | ... | "9"
```

**Examples:**

| `so` value | Selected indices |
|---|---|
| `0` | {0} |
| `0,2,4` | {0, 2, 4} |
| `1-5` | {1, 2, 3, 4, 5} |
| `0,3-5,8` | {0, 3, 4, 5, 8} |
| `0-0` | {0} (valid — range where start == end) |

### 2.2 File Index Mapping

File indices correspond to the order of files in the `files` list of the info dictionary (BEP 3), **zero-indexed**.

For a torrent with:
```
files = [
    {path: ["video.mp4"], length: 700000000},    // index 0
    {path: ["subs.srt"],  length: 50000},         // index 1
    {path: ["cover.jpg"], length: 200000},         // index 2
    {path: ["nfo.txt"],   length: 1000},           // index 3
]
```

`so=0,1` selects `video.mp4` and `subs.srt`.

**Edge cases:**
- Indices beyond the file count are silently ignored (not an error).
- Duplicate indices (e.g., `0,0` or `0-2,1`) result in the file being selected once.
- An empty `so` value or missing `so` parameter means download all files.
- For single-file torrents, `so` is ignored (there's only one file, index 0).

### 2.3 BEP 47 Padding File Interaction

When the torrent uses BEP 47 padding files, the padding entries **occupy file indices** in the `files` list. However:

- Clients should skip padding files when presenting indices to the user.
- The `so` indices in the magnet URI refer to the raw file list (including padding entries).
- In practice, padding files are small and selecting an adjacent real file implicitly requires downloading the padding anyway (it's part of the piece).

**Recommendation:** When generating `so` parameters, use the indices from the raw file list. When displaying to users, show only real file indices.

### 2.4 Piece Selection from File Selection

Once the metadata is fetched and the file list is known, the client maps selected file indices to the pieces that need downloading:

**Algorithm:**

```
function piecesForSelectedFiles(files, selectedIndices, pieceLength):
    neededPieces = empty set

    for each idx in selectedIndices:
        file = files[idx]
        fileStart = sum of lengths of files[0..idx-1]
        fileEnd = fileStart + file.length - 1

        firstPiece = fileStart / pieceLength
        lastPiece = fileEnd / pieceLength

        for p = firstPiece to lastPiece:
            neededPieces.add(p)

    return neededPieces
```

**Boundary pieces:** When a piece spans a selected file and a non-selected file, the **entire piece must be downloaded** (pieces are the atomic unit of verification). However, only the bytes belonging to selected files are written to disk.

```
Piece boundary example (piece length = 256 KiB):

  File 0 (selected)       File 1 (not selected)     File 2 (selected)
  ├────── 400 KiB ──────┤├────── 200 KiB ──────────┤├── 300 KiB ──┤
  |  piece 0  |  piece 1       |  piece 2  | piece 3       |
  |  256 KiB  |  256 KiB       |  256 KiB  | 132 KiB       |
              ▲                 ▲
              │                 │
          This piece contains   This piece contains
          end of File 0 AND     end of File 1 AND
          start of File 1.      start of File 2.
          Must download it      Must download it
          (has File 0 data).    (has File 2 data).
```

In this example, pieces 0, 1, 2, 3 are all needed (pieces 1 and 2 are boundary pieces). Only the File 0 and File 2 byte ranges are written to disk.

### 2.5 Magnet URI Roundtrip

The `so` parameter must survive URI encoding. Since it contains only digits, commas, and hyphens, no percent-encoding is needed in practice. However, the parser should handle percent-encoded forms (e.g., `%2C` for comma).

**Full magnet URI example:**

```
magnet:?xt=urn:btih:d2474e86c95b19b8bcfdb92bc12c9d44667571a5&dn=My+Torrent&so=0,2,4-6&tr=http%3A%2F%2Ftracker.example.com%2Fannounce
```

---

## 3. Implementation Plan

### 3.1 Files to Modify

**`magnet/magnet.go`** — Add `so` parameter parsing to `Link`:

```go
type Link struct {
    InfoHash   [20]byte
    Name       string
    Trackers   []string
    SelectOnly []int // BEP 53: selected file indices (nil = all files)
}
```

Update `Parse()` to extract and expand the `so` parameter:

```go
// In Parse():
if soStr := params.Get("so"); soStr != "" {
    selected, err := parseSelectOnly(soStr)
    if err != nil {
        return nil, fmt.Errorf("parse so parameter: %w", err)
    }
    link.SelectOnly = selected
}
```

Update `String()` to serialize `so` back into the URI:

```go
// In String():
if len(l.SelectOnly) > 0 {
    params.Set("so", formatSelectOnly(l.SelectOnly))
}
```

**`download/session.go`** — Add `SelectOnly []int` to `Config`. When set, compute the needed piece set and pass it to the `Picker`:

```go
type Config struct {
    // ... existing fields ...
    SelectOnly []int // BEP 53: file indices to download (nil = all)
}
```

**`download/picker.go`** — Add the concept of a "needed" piece set. Pieces not in the set are pre-marked as done:

```go
// NewPickerWithSelection creates a picker that only downloads the specified pieces.
// Pieces not in the neededPieces set are immediately marked as done.
func NewPickerWithSelection(numPieces int, neededPieces map[int]bool) *Picker
```

**`torrent/torrent.go`** — Add a helper to map file indices to piece ranges:

```go
// PiecesForFiles returns the set of piece indices needed to download the
// given file indices. Includes boundary pieces that partially overlap.
func (t *Torrent) PiecesForFiles(fileIndices []int) map[int]bool
```

### 3.2 Files to Create

**`magnet/selectonly.go`** — Parsing and formatting logic for the `so` parameter:

```go
// parseSelectOnly parses a BEP 53 "so" parameter value into a sorted,
// deduplicated list of file indices.
func parseSelectOnly(so string) ([]int, error)

// formatSelectOnly converts a list of file indices back to a compact "so"
// string, collapsing consecutive indices into ranges.
func formatSelectOnly(indices []int) string
```

**`magnet/selectonly_test.go`** — Tests for parsing and formatting.

### 3.3 Key Functions

```go
// parseSelectOnly parses "0,2,4-6" into [0, 2, 4, 5, 6].
// Returns a sorted, deduplicated slice.
func parseSelectOnly(so string) ([]int, error)

// formatSelectOnly converts [0, 2, 4, 5, 6] back to "0,2,4-6".
// Collapses runs of 3+ consecutive indices into ranges.
func formatSelectOnly(indices []int) string

// PiecesForFiles computes the set of piece indices that overlap with
// the given file indices. Handles boundary pieces.
func (t *Torrent) PiecesForFiles(fileIndices []int) map[int]bool

// NewPickerWithSelection creates a picker that only considers the
// specified pieces. All other pieces are pre-marked as done.
func NewPickerWithSelection(numPieces int, neededPieces map[int]bool) *Picker

// FileRangeInPiece returns the byte range within a piece that belongs
// to a specific file. Used when writing boundary pieces to disk.
func (t *Torrent) FileRangeInPiece(pieceIdx, fileIdx int) (offset, length int)
```

### 3.4 Package Placement

- `magnet/` — URI parsing and `so` parameter handling
- `torrent/` — file-to-piece mapping helpers
- `download/` — selection-aware picker and disk write logic

---

## 4. Dependencies

| BEP | Relationship |
|---|---|
| **BEP 3** | Base protocol — defines the `files` list and piece structure that BEP 53 indexes into |
| **BEP 9** | Metadata exchange — the client fetches metadata via BEP 9 before it can map `so` indices to actual files and pieces |
| **BEP 47** | Padding files — padding entries occupy file indices; BEP 47-padded torrents make piece selection cleaner |
| **BEP 52** | v2 torrents — per-file pieces eliminate boundary-piece complications for selective download |
| **BEP 21** | Partial seeds — a client doing selective download becomes a partial seed once it has its selected files |

### Internal Dependencies

- `magnet.Link` — extended with `SelectOnly` field
- `magnet.Parse()` / `magnet.String()` — roundtrip support for `so`
- `torrent.Torrent` — file list and `PiecesForFiles()` helper
- `download.Picker` — selection-aware variant
- `download.Config` — new `SelectOnly` field

---

## 5. Testing Strategy

### 5.1 Parsing Tests (`magnet/selectonly_test.go`)

**`TestParseSelectOnlySingle`** — Single index:
- `"0"` → `[0]`
- `"5"` → `[5]`

**`TestParseSelectOnlyMultiple`** — Comma-separated indices:
- `"0,2,4"` → `[0, 2, 4]`

**`TestParseSelectOnlyRange`** — Inclusive ranges:
- `"1-5"` → `[1, 2, 3, 4, 5]`
- `"0-0"` → `[0]`

**`TestParseSelectOnlyMixed`** — Indices and ranges combined:
- `"0,3-5,8"` → `[0, 3, 4, 5, 8]`
- `"0,2,4-6,10"` → `[0, 2, 4, 5, 6, 10]`

**`TestParseSelectOnlyDeduplicate`** — Overlapping entries:
- `"0,0"` → `[0]`
- `"0-2,1-3"` → `[0, 1, 2, 3]`
- `"5,3-7"` → `[3, 4, 5, 6, 7]`

**`TestParseSelectOnlyErrors`** — Invalid input:
- `""` → error (empty)
- `"-1"` → error (negative)
- `"abc"` → error (non-numeric)
- `"5-3"` → error (reversed range)

**`TestFormatSelectOnly`** — Compact serialization:
- `[0, 2, 4, 5, 6]` → `"0,2,4-6"`
- `[0]` → `"0"`
- `[1, 2, 3]` → `"1-3"`
- `[1, 3, 5]` → `"1,3,5"` (no consecutive runs)
- `[0, 1]` → `"0,1"` (run of 2 stays expanded — only 3+ collapses)

**`TestSelectOnlyRoundTrip`** — Parse then format, verify equivalence for various inputs.

### 5.2 Magnet URI Tests (`magnet/magnet_test.go`)

**`TestParseMagnetWithSO`** — Full magnet URI with `so`:
- `magnet:?xt=urn:btih:...&so=0,2,4-6` → `Link.SelectOnly == [0, 2, 4, 5, 6]`

**`TestParseMagnetWithoutSO`** — Standard magnet:
- `Link.SelectOnly == nil`

**`TestMagnetStringWithSO`** — Roundtrip:
- Parse a magnet with `so`, call `String()`, parse again → same `SelectOnly`

### 5.3 Piece Mapping Tests (`torrent/torrent_test.go`)

**`TestPiecesForFilesSingleFile`** — Single-file torrent:
- `PiecesForFiles([0])` returns all pieces

**`TestPiecesForFilesExact`** — Multi-file torrent where files align to piece boundaries:
- Selecting file 0 returns only the pieces covering file 0
- Selecting file 1 returns only its pieces
- No overlap between the two sets

**`TestPiecesForFilesBoundary`** — Files that don't align to piece boundaries:
- Selecting file 0 includes the boundary piece shared with file 1
- Selecting file 1 also includes that same boundary piece
- Selecting both returns the union (boundary piece counted once)

**`TestPiecesForFilesOutOfRange`** — Index beyond file count:
- Silently ignored, returns pieces for valid indices only

### 5.4 Picker Tests (`download/picker_test.go`)

**`TestPickerWithSelection`** — Verify selection-aware picker:
- Create a picker for 10 pieces, selecting only pieces {2, 5, 7}
- `Pick()` should never return pieces outside {2, 5, 7}
- After finishing pieces 2, 5, 7 → `Done()` returns true
- `Remaining()` reflects only the selected set

### 5.5 Integration Test

**`TestSelectiveDownload`** — End-to-end:
- Create a 4-file torrent with known data
- Download with `SelectOnly=[1, 3]` (files 1 and 3)
- Verify that only files 1 and 3 are written to disk
- Verify their contents match the original data
- Verify that files 0 and 2 are not created on disk
