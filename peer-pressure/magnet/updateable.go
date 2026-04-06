package magnet

import (
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"net/url"
	"strings"
)

// UpdateableLink represents a BEP 46 magnet link for updateable torrents.
// The link uses a public key (xs=urn:btpk:<hex>) instead of an infohash.
type UpdateableLink struct {
	PublicKey [32]byte // ed25519 public key
	Salt      string   // optional salt
	Name      string   // from dn= (optional)
}

// ParseUpdateable parses a BEP 46 magnet URI with xs=urn:btpk:... scheme.
func ParseUpdateable(uri string) (*UpdateableLink, error) {
	if !strings.HasPrefix(uri, "magnet:?") {
		return nil, fmt.Errorf("not a magnet URI: %q", uri)
	}

	params, err := url.ParseQuery(uri[len("magnet:?"):])
	if err != nil {
		return nil, fmt.Errorf("parse magnet params: %w", err)
	}

	xs := params.Get("xs")
	if xs == "" {
		return nil, fmt.Errorf("magnet URI missing xs parameter")
	}
	if !strings.HasPrefix(xs, "urn:btpk:") {
		return nil, fmt.Errorf("unsupported xs scheme: %q (want urn:btpk:)", xs)
	}

	keyHex := xs[len("urn:btpk:"):]
	if len(keyHex) != 64 {
		return nil, fmt.Errorf("public key has unexpected length %d (want 64 hex chars)", len(keyHex))
	}

	decoded, err := hex.DecodeString(keyHex)
	if err != nil {
		return nil, fmt.Errorf("decode public key: %w", err)
	}

	var key [32]byte
	copy(key[:], decoded)

	link := &UpdateableLink{
		PublicKey: key,
		Name:     params.Get("dn"),
	}

	// Salt is hex-encoded in the magnet link.
	if s := params.Get("s"); s != "" {
		saltBytes, err := hex.DecodeString(s)
		if err != nil {
			return nil, fmt.Errorf("decode salt: %w", err)
		}
		link.Salt = string(saltBytes)
	}

	return link, nil
}

// TargetID computes the DHT target for this updateable link: SHA-1(pubkey + salt).
func (l *UpdateableLink) TargetID() [20]byte {
	h := sha1.New()
	h.Write(l.PublicKey[:])
	h.Write([]byte(l.Salt))
	var target [20]byte
	copy(target[:], h.Sum(nil))
	return target
}

// String returns the BEP 46 magnet URI.
func (l *UpdateableLink) String() string {
	params := url.Values{}
	params.Set("xs", "urn:btpk:"+hex.EncodeToString(l.PublicKey[:]))
	if l.Salt != "" {
		params.Set("s", hex.EncodeToString([]byte(l.Salt)))
	}
	if l.Name != "" {
		params.Set("dn", l.Name)
	}
	return "magnet:?" + params.Encode()
}
