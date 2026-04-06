package magnet

import (
	"encoding/hex"
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

// Link represents a parsed magnet URI.
//
// Format: magnet:?xt=urn:btih:<info_hash>&dn=<name>&tr=<tracker>&so=<indices>...
type Link struct {
	InfoHash    [20]byte // from xt=urn:btih:...
	Name        string   // from dn= (display name, optional)
	Trackers    []string // from tr= (tracker URLs, optional)
	SelectOnly  []int    // BEP 53: from so= (file indices, optional)
}

// Parse parses a magnet URI string into a Link.
func Parse(uri string) (*Link, error) {
	if !strings.HasPrefix(uri, "magnet:?") {
		return nil, fmt.Errorf("not a magnet URI: %q", uri)
	}

	params, err := url.ParseQuery(uri[len("magnet:?"):])
	if err != nil {
		return nil, fmt.Errorf("parse magnet params: %w", err)
	}

	xt := params.Get("xt")
	if xt == "" {
		return nil, fmt.Errorf("magnet URI missing xt parameter")
	}

	// xt must be urn:btih:<hex or base32 info hash>
	if !strings.HasPrefix(xt, "urn:btih:") {
		return nil, fmt.Errorf("unsupported xt scheme: %q", xt)
	}

	hashStr := xt[len("urn:btih:"):]

	var infoHash [20]byte
	switch len(hashStr) {
	case 40: // hex encoded
		decoded, err := hex.DecodeString(hashStr)
		if err != nil {
			return nil, fmt.Errorf("decode hex info_hash: %w", err)
		}
		copy(infoHash[:], decoded)
	case 32: // base32 encoded
		decoded, err := decodeBase32(hashStr)
		if err != nil {
			return nil, fmt.Errorf("decode base32 info_hash: %w", err)
		}
		copy(infoHash[:], decoded)
	default:
		return nil, fmt.Errorf("info_hash has unexpected length %d (want 40 hex or 32 base32)", len(hashStr))
	}

	link := &Link{
		InfoHash: infoHash,
		Name:     params.Get("dn"),
		Trackers: params["tr"],
	}

	// BEP 53: select-only file indices.
	if so := params.Get("so"); so != "" {
		indices, err := parseSelectOnly(so)
		if err != nil {
			return nil, fmt.Errorf("parse so parameter: %w", err)
		}
		link.SelectOnly = indices
	}

	return link, nil
}

// decodeBase32 decodes a base32-encoded string (RFC 4648, no padding).
func decodeBase32(s string) ([]byte, error) {
	s = strings.ToUpper(s)
	const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZ234567"

	bits := 0
	acc := 0
	var out []byte

	for _, c := range s {
		idx := strings.IndexRune(alphabet, c)
		if idx < 0 {
			return nil, fmt.Errorf("invalid base32 character: %c", c)
		}
		acc = (acc << 5) | idx
		bits += 5
		if bits >= 8 {
			bits -= 8
			out = append(out, byte(acc>>bits))
			acc &= (1 << bits) - 1
		}
	}

	return out, nil
}

// String returns the magnet URI as a string.
func (l *Link) String() string {
	params := url.Values{}
	params.Set("xt", "urn:btih:"+hex.EncodeToString(l.InfoHash[:]))
	if l.Name != "" {
		params.Set("dn", l.Name)
	}
	for _, tr := range l.Trackers {
		params.Add("tr", tr)
	}
	if len(l.SelectOnly) > 0 {
		params.Set("so", formatSelectOnly(l.SelectOnly))
	}
	return "magnet:?" + params.Encode()
}

// parseSelectOnly parses a BEP 53 select-only string like "0,2,4,6-8".
func parseSelectOnly(s string) ([]int, error) {
	var indices []int
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if dash := strings.IndexByte(part, '-'); dash >= 0 {
			lo, err := strconv.Atoi(part[:dash])
			if err != nil {
				return nil, fmt.Errorf("invalid range start %q: %w", part[:dash], err)
			}
			hi, err := strconv.Atoi(part[dash+1:])
			if err != nil {
				return nil, fmt.Errorf("invalid range end %q: %w", part[dash+1:], err)
			}
			if lo > hi {
				return nil, fmt.Errorf("invalid range %d-%d: start > end", lo, hi)
			}
			for i := lo; i <= hi; i++ {
				indices = append(indices, i)
			}
		} else {
			idx, err := strconv.Atoi(part)
			if err != nil {
				return nil, fmt.Errorf("invalid index %q: %w", part, err)
			}
			indices = append(indices, idx)
		}
	}
	return indices, nil
}

// formatSelectOnly formats file indices as a BEP 53 string, collapsing
// consecutive sequences into ranges.
func formatSelectOnly(indices []int) string {
	if len(indices) == 0 {
		return ""
	}

	sorted := make([]int, len(indices))
	copy(sorted, indices)
	sortInts(sorted)

	var parts []string
	i := 0
	for i < len(sorted) {
		start := sorted[i]
		end := start
		for i+1 < len(sorted) && sorted[i+1] == end+1 {
			end = sorted[i+1]
			i++
		}
		if start == end {
			parts = append(parts, strconv.Itoa(start))
		} else {
			parts = append(parts, strconv.Itoa(start)+"-"+strconv.Itoa(end))
		}
		i++
	}
	return strings.Join(parts, ",")
}

// sortInts sorts a small int slice in-place (insertion sort, no import needed).
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
