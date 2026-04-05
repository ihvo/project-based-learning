// Package torrent parses .torrent metainfo files (BEP 3).
//
// A .torrent file is a bencoded dictionary containing tracker URLs and an
// "info" dictionary that describes the file(s) to download. The SHA-1 hash
// of the raw bencoded info dictionary is the torrent's unique identity
// (the info_hash).
//
// Reference: https://www.bittorrent.org/beps/bep_0003.html
package torrent

import (
	"crypto/sha1"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/ihvo/peer-pressure/bencode"
)

const hashLen = 20 // SHA-1 produces 20-byte hashes

// Torrent holds the parsed contents of a .torrent metainfo file.
type Torrent struct {
	Announce     string        // primary tracker URL
	AnnounceList [][]string    // multi-tracker tiers (BEP 12); nil if absent
	InfoHash     [hashLen]byte // SHA-1 of the raw bencoded info dict
	Name         string        // suggested file/directory name
	PieceLength  int           // bytes per piece
	Pieces       [][hashLen]byte // SHA-1 hash for each piece
	Length       int           // total size in bytes (single-file mode)
	Files        []File        // file list (multi-file mode; empty for single-file)
}

// File represents one file in a multi-file torrent.
type File struct {
	Length int      // file size in bytes
	Path   []string // path components (e.g., ["dir", "subdir", "file.txt"])
}

// IsSingleFile reports whether this torrent describes a single file.
func (t *Torrent) IsSingleFile() bool {
	return len(t.Files) == 0
}

// Trackers returns a flat, deduplicated list of all tracker URLs.
// Includes announce-list tiers (if present) plus the primary announce URL.
func (t *Torrent) Trackers() []string {
	seen := make(map[string]bool)
	var result []string
	add := func(u string) {
		if u != "" && !seen[u] {
			seen[u] = true
			result = append(result, u)
		}
	}

	// Announce-list tiers first (BEP 12 says they take priority)
	for _, tier := range t.AnnounceList {
		for _, u := range tier {
			add(u)
		}
	}
	// Fall back to primary announce
	add(t.Announce)
	return result
}

// PieceLen returns the byte length of the piece at the given index.
// All pieces have length PieceLength except the last, which may be shorter.
func (t *Torrent) PieceLen(index int) int {
	start := index * t.PieceLength
	end := start + t.PieceLength
	total := t.TotalLength()
	if end > total {
		end = total
	}
	return end - start
}

// TotalLength returns the total size of all files in the torrent.
func (t *Torrent) TotalLength() int {
	if t.IsSingleFile() {
		return t.Length
	}
	total := 0
	for _, f := range t.Files {
		total += f.Length
	}
	return total
}

// String returns a human-readable summary of the torrent.
func (t *Torrent) String() string {
	var b strings.Builder
	fmt.Fprintf(&b, "Name:         %s\n", t.Name)
	fmt.Fprintf(&b, "Tracker:      %s\n", t.Announce)
	fmt.Fprintf(&b, "Info Hash:    %x\n", t.InfoHash)
	fmt.Fprintf(&b, "Piece Length: %d (%d KiB)\n", t.PieceLength, t.PieceLength/1024)
	fmt.Fprintf(&b, "Pieces:       %d\n", len(t.Pieces))

	if t.IsSingleFile() {
		fmt.Fprintf(&b, "Length:       %d (%s)\n", t.Length, formatSize(t.Length))
		fmt.Fprintf(&b, "Mode:         single-file\n")
	} else {
		fmt.Fprintf(&b, "Total Size:   %d (%s)\n", t.TotalLength(), formatSize(t.TotalLength()))
		fmt.Fprintf(&b, "Mode:         multi-file (%d files)\n", len(t.Files))
		for _, f := range t.Files {
			fmt.Fprintf(&b, "  %s — %s\n", strings.Join(f.Path, "/"), formatSize(f.Length))
		}
	}
	return b.String()
}

func formatSize(bytes int) string {
	switch {
	case bytes >= 1<<30:
		return fmt.Sprintf("%.2f GiB", float64(bytes)/float64(1<<30))
	case bytes >= 1<<20:
		return fmt.Sprintf("%.2f MiB", float64(bytes)/float64(1<<20))
	case bytes >= 1<<10:
		return fmt.Sprintf("%.2f KiB", float64(bytes)/float64(1<<10))
	default:
		return fmt.Sprintf("%d B", bytes)
	}
}

// Load parses a torrent from a file path or HTTP(S) URL.
func Load(pathOrURL string) (*Torrent, error) {
	if strings.HasPrefix(pathOrURL, "http://") || strings.HasPrefix(pathOrURL, "https://") {
		return fetchAndParse(pathOrURL)
	}
	return ParseFile(pathOrURL)
}

func fetchAndParse(url string) (*Torrent, error) {
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return nil, fmt.Errorf("fetch torrent: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetch torrent: HTTP %d", resp.StatusCode)
	}

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read torrent response: %w", err)
	}
	return Parse(data)
}

// ParseFile reads and parses a .torrent file from disk.
func ParseFile(path string) (*Torrent, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read torrent file: %w", err)
	}
	return Parse(data)
}

// Parse decodes a .torrent file from raw bytes.
func Parse(data []byte) (*Torrent, error) {
	// Decode the top-level dict with raw bytes preserved per key,
	// so we can hash the raw "info" dict bytes for the info_hash.
	entries, err := bencode.DecodeDictRaw(data)
	if err != nil {
		return nil, fmt.Errorf("decode torrent: %w", err)
	}

	t := &Torrent{}

	// --- announce ---
	announceRaw, ok := entries["announce"]
	if !ok {
		return nil, fmt.Errorf("torrent: missing 'announce' key")
	}
	announce, ok := announceRaw.Val.(bencode.String)
	if !ok {
		return nil, fmt.Errorf("torrent: 'announce' is not a string")
	}
	t.Announce = string(announce)

	// --- announce-list (BEP 12, optional) ---
	if alRaw, ok := entries["announce-list"]; ok {
		if alList, ok := alRaw.Val.(bencode.List); ok {
			for _, tier := range alList {
				if tl, ok := tier.(bencode.List); ok {
					var urls []string
					for _, u := range tl {
						if s, ok := u.(bencode.String); ok {
							urls = append(urls, string(s))
						}
					}
					if len(urls) > 0 {
						t.AnnounceList = append(t.AnnounceList, urls)
					}
				}
			}
		}
	}

	// --- info dict ---
	infoRaw, ok := entries["info"]
	if !ok {
		return nil, fmt.Errorf("torrent: missing 'info' key")
	}
	// The info_hash is SHA-1 of the original bencoded info bytes
	t.InfoHash = sha1.Sum(infoRaw.Raw)

	infoDict, ok := infoRaw.Val.(bencode.Dict)
	if !ok {
		return nil, fmt.Errorf("torrent: 'info' is not a dict")
	}

	if err := parseInfo(t, infoDict); err != nil {
		return nil, err
	}

	return t, nil
}

func parseInfo(t *Torrent, info bencode.Dict) error {
	// --- name ---
	name, err := dictString(info, "name")
	if err != nil {
		return fmt.Errorf("info: %w", err)
	}
	t.Name = name

	// --- piece length ---
	pl, err := dictInt(info, "piece length")
	if err != nil {
		return fmt.Errorf("info: %w", err)
	}
	t.PieceLength = int(pl)

	// --- pieces (concatenated SHA-1 hashes) ---
	piecesVal, ok := info["pieces"]
	if !ok {
		return fmt.Errorf("info: missing 'pieces' key")
	}
	piecesBytes, ok := piecesVal.(bencode.String)
	if !ok {
		return fmt.Errorf("info: 'pieces' is not a string")
	}
	if len(piecesBytes)%hashLen != 0 {
		return fmt.Errorf("info: 'pieces' length %d is not a multiple of %d", len(piecesBytes), hashLen)
	}
	numPieces := len(piecesBytes) / hashLen
	t.Pieces = make([][hashLen]byte, numPieces)
	for i := range numPieces {
		copy(t.Pieces[i][:], piecesBytes[i*hashLen:(i+1)*hashLen])
	}

	// --- single-file vs multi-file ---
	_, hasLength := info["length"]
	_, hasFiles := info["files"]

	switch {
	case hasLength && hasFiles:
		return fmt.Errorf("info: has both 'length' and 'files'")
	case hasLength:
		length, err := dictInt(info, "length")
		if err != nil {
			return fmt.Errorf("info: %w", err)
		}
		t.Length = int(length)
	case hasFiles:
		filesList, ok := info["files"].(bencode.List)
		if !ok {
			return fmt.Errorf("info: 'files' is not a list")
		}
		t.Files = make([]File, len(filesList))
		for i, fv := range filesList {
			fd, ok := fv.(bencode.Dict)
			if !ok {
				return fmt.Errorf("info: files[%d] is not a dict", i)
			}
			fl, err := dictInt(fd, "length")
			if err != nil {
				return fmt.Errorf("info: files[%d]: %w", i, err)
			}
			pathList, ok := fd["path"].(bencode.List)
			if !ok {
				return fmt.Errorf("info: files[%d]: 'path' is not a list", i)
			}
			path := make([]string, len(pathList))
			for j, pv := range pathList {
				ps, ok := pv.(bencode.String)
				if !ok {
					return fmt.Errorf("info: files[%d]: path[%d] is not a string", i, j)
				}
				path[j] = string(ps)
			}
			t.Files[i] = File{Length: int(fl), Path: path}
		}
	default:
		return fmt.Errorf("info: missing both 'length' and 'files'")
	}

	return nil
}

// --- dict helper functions ---
// These extract typed values from a bencode.Dict with clear error messages.

func dictString(d bencode.Dict, key string) (string, error) {
	v, ok := d[key]
	if !ok {
		return "", fmt.Errorf("missing '%s' key", key)
	}
	s, ok := v.(bencode.String)
	if !ok {
		return "", fmt.Errorf("'%s' is not a string", key)
	}
	return string(s), nil
}

func dictInt(d bencode.Dict, key string) (int64, error) {
	v, ok := d[key]
	if !ok {
		return 0, fmt.Errorf("missing '%s' key", key)
	}
	n, ok := v.(bencode.Int)
	if !ok {
		return 0, fmt.Errorf("'%s' is not an integer", key)
	}
	return int64(n), nil
}
