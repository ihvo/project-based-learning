# BEP 47: Padding Files and Extended File Attributes

## What It Does

BEP 47 adds file-level metadata to multi-file torrents. Each file entry in
the `info` dictionary can carry an `attr` string whose characters encode
attributes:

| Character | Meaning    |
|-----------|------------|
| `p`       | Padding file — synthetic filler to align real files on piece boundaries |
| `l`       | Symlink (the `symlink path` key provides the target) |
| `x`       | Executable |
| `h`       | Hidden     |

### Why Padding Matters

In a multi-file torrent, pieces span across file boundaries. Without padding,
two unrelated files might share a piece, making selective downloading harder
and preventing deduplication across torrents that share files.

A padding file is inserted between real files so the *next* real file starts
at an exact piece boundary. For hash calculation, padding bytes are all zeros.
Clients aware of BEP 47 should:

1. **Not write** pad files to disk.
2. **Not request** pad-file regions from peers (fill with zeros locally).
3. **Still serve** pad-file data if a legacy peer requests it.

Conventional pad-file path: `[".pad", "N"]` where `N` is the length as a
decimal string.

### What We Implemented

- **Parsing**: the `attr` field is read from each file dict in the torrent
  metainfo. Unknown attributes are stored and ignored (forward-compatible).
- **`IsPad()` helper**: scans the attr string for `'p'`.
- **Display**: `[pad]` marker in the torrent info output so the user can see
  which files are padding.

We did *not* implement pad-file creation during `torrent create` — that's a
separate optimization for piece alignment (YAGNI for now).

## Go Idioms

### Iterating Over Runes

```go
func (f File) IsPad() bool {
    for _, c := range f.Attr {
        if c == 'p' {
            return true
        }
    }
    return false
}
```

`range` over a `string` yields `(index, rune)` pairs, correctly handling
multi-byte UTF-8. Since BEP 47 attributes are single ASCII characters, each
iteration is one byte, but the pattern is safe for any encoding.

An alternative is `strings.ContainsRune(f.Attr, 'p')`, but the explicit loop
is equally readable and avoids an import for a single use.

### Optional Field Parsing with Type Assertion

```go
if attrVal, ok := fd["attr"].(bencode.String); ok {
    t.Files[i].Attr = string(attrVal)
}
```

The two-value type assertion `x, ok := val.(Type)` is the idiomatic way to
handle optional bencoded fields. If the key is absent or the wrong type,
`ok` is false and we skip silently — exactly the right behavior for an
optional BEP extension.

### Zero Value as "Not Present"

The `Attr` field is a plain `string`. Its zero value (`""`) means "no
attributes", which is the correct default. No need for `*string` or a
separate `HasAttr bool` — Go's zero values align perfectly here.
