// Extension protocol (BEP 10) support.
//
// The extension protocol lets peers negotiate additional capabilities
// beyond the base wire protocol. Each side advertises which extensions
// it supports (e.g., ut_metadata for BEP 9) via a bencoded handshake
// carried inside MsgExtended (ID 20) with sub-ID 0.
//
// Reference: https://www.bittorrent.org/beps/bep_0010.html
package peer

import (
	"fmt"

	"github.com/ihvo/peer-pressure/bencode"
)

// ExtHandshake holds the fields of a BEP 10 extension handshake.
type ExtHandshake struct {
	// M maps extension names to the message IDs the sender will use.
	// e.g. {"ut_metadata": 1, "ut_pex": 2}
	M map[string]int

	// MetadataSize is the total size of the info dictionary in bytes.
	// Only meaningful when the peer supports ut_metadata (BEP 9).
	MetadataSize int

	// V is the client name/version string (optional, informational).
	V string

	// UploadOnly indicates the peer is a partial seed (BEP 21).
	UploadOnly bool
}

// NewExtHandshake creates an extension handshake message (sub-ID 0)
// advertising our supported extensions.
func NewExtHandshake(exts map[string]int, metadataSize int, clientVersion string) *Message {
	m := bencode.Dict{}
	for name, id := range exts {
		m[name] = bencode.Int(id)
	}

	d := bencode.Dict{
		"m": m,
		"v": bencode.String(clientVersion),
	}
	if metadataSize > 0 {
		d["metadata_size"] = bencode.Int(metadataSize)
	}

	data := bencode.Encode(d)

	// Extended message: [sub-ID byte] [bencoded payload]
	payload := make([]byte, 1+len(data))
	payload[0] = 0 // sub-ID 0 = handshake
	copy(payload[1:], data)

	return &Message{ID: MsgExtended, Payload: payload}
}

// ParseExtHandshake decodes a BEP 10 extension handshake from the payload
// of a MsgExtended message. The caller must verify payload[0] == 0 (sub-ID).
func ParseExtHandshake(payload []byte) (*ExtHandshake, error) {
	if len(payload) < 2 {
		return nil, fmt.Errorf("extension handshake too short: %d bytes", len(payload))
	}
	if payload[0] != 0 {
		return nil, fmt.Errorf("expected sub-ID 0, got %d", payload[0])
	}

	val, err := bencode.Decode(payload[1:])
	if err != nil {
		return nil, fmt.Errorf("decode extension handshake: %w", err)
	}

	d, ok := val.(bencode.Dict)
	if !ok {
		return nil, fmt.Errorf("extension handshake: expected dict, got %T", val)
	}

	hs := &ExtHandshake{
		M: make(map[string]int),
	}

	// Parse "m" — extension name → ID mapping
	if mVal, ok := d["m"]; ok {
		if mDict, ok := mVal.(bencode.Dict); ok {
			for name, idVal := range mDict {
				if id, ok := idVal.(bencode.Int); ok {
					hs.M[name] = int(id)
				}
			}
		}
	}

	// Parse "metadata_size"
	if msVal, ok := d["metadata_size"]; ok {
		if ms, ok := msVal.(bencode.Int); ok {
			hs.MetadataSize = int(ms)
		}
	}

	// Parse "v"
	if vVal, ok := d["v"]; ok {
		if v, ok := vVal.(bencode.String); ok {
			hs.V = string(v)
		}
	}

	// BEP 21: parse top-level "upload_only" state
	if uoVal, ok := d["upload_only"]; ok {
		if uo, ok := uoVal.(bencode.Int); ok {
			hs.UploadOnly = uo != 0
		}
	}

	return hs, nil
}

// NewExtMessage creates an extended message with the given sub-ID and payload.
func NewExtMessage(subID uint8, data []byte) *Message {
	payload := make([]byte, 1+len(data))
	payload[0] = subID
	copy(payload[1:], data)
	return &Message{ID: MsgExtended, Payload: payload}
}
