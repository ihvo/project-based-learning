# BEP 53: Magnet URI Extension — Select Specific Files

## What It Does

BEP 53 adds an `so` (select-only) parameter to magnet URIs that specifies
which file indices to download from a multi-file torrent. This enables "deep
links" to specific files within a torrent.

### Format

```
magnet:?xt=urn:btih:HASH&so=0,2,4,6-8
```

- Indices are zero-based, comma-separated
- Dashes denote inclusive ranges: `6-8` means files 6, 7, 8
- The parameter is optional — absent means "download all"

### Use Case

A library torrent with 500 books — a website can link directly to book #47:

```
magnet:?xt=urn:btih:...&so=47
```

The client fetches metadata first (BEP 9), then only downloads the selected
file(s). Combined with BEP 47 padding files (which align files to piece
boundaries), this makes selective downloading efficient.

### What We Implemented

1. **`parseSelectOnly()`** — parses comma-separated indices and ranges
2. **`formatSelectOnly()`** — inverse: collapses consecutive indices into
   ranges for compact magnet URIs
3. **`Link.SelectOnly`** — the parsed indices, stored as `[]int`
4. **`Link.String()`** — includes `so=` when indices are present
5. **Round-trip**: parse → format → parse preserves indices exactly

## Go Idioms

### String Splitting with `strings.Split`

```go
for _, part := range strings.Split(s, ",") {
    if dash := strings.IndexByte(part, '-'); dash >= 0 {
        lo, _ := strconv.Atoi(part[:dash])
        hi, _ := strconv.Atoi(part[dash+1:])
```

`Split` handles the comma-separated list. `IndexByte` checks for a range
dash (cheaper than `Contains` since we need the position anyway). Slice
expressions `part[:dash]` and `part[dash+1:]` extract the range endpoints
without allocating new strings.

### Insertion Sort for Small Slices

```go
func sortInts(a []int) {
    for i := 1; i < len(a); i++ {
        key := a[i]
        j := i - 1
        for j >= 0 && a[j] > key {
            a[j+1] = a[j]
            j--
        }
        a[j+1] = key
    }
}
```

For `formatSelectOnly` we need sorted indices. Rather than importing `sort`
(or `slices`) for a single use, a 10-line insertion sort is perfectly adequate.
The typical select-only list has < 20 elements, where insertion sort's O(n²)
is faster than O(n log n) due to lower constant factors.

### Range Collapsing Pattern

```go
for i < len(sorted) {
    start := sorted[i]
    end := start
    for i+1 < len(sorted) && sorted[i+1] == end+1 {
        end = sorted[i+1]
        i++
    }
    // start..end is a maximal consecutive run
}
```

This greedy pattern extends a run as long as the next element is consecutive.
The inner loop advances `i`, so the outer loop skips past the entire run.
It produces the minimum number of range tokens (optimal compression).
