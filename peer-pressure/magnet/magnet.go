package magnet

import (
	"encoding/hex"
	"fmt"
	"net/url"
	"strings"
)

// Link represents a parsed magnet URI.
//
// Format: magnet:?xt=urn:btih:<info_hash>&dn=<name>&tr=<tracker>...
type Link struct {
	InfoHash [20]byte // from xt=urn:btih:...
	Name     string   // from dn= (display name, optional)
	Trackers []string // from tr= (tracker URLs, optional)
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
	return "magnet:?" + params.Encode()
}
