package lsd

import (
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

const (
	IPv4Multicast = "239.192.152.143"
	IPv6Multicast = "ff15::efc0:988f"
	Port          = 6771

	requestLine = "BT-SEARCH * HTTP/1.1"
)

var (
	ErrBadRequestLine = errors.New("lsd: not a BT-SEARCH message")
	ErrMissingPort    = errors.New("lsd: missing Port header")
	ErrMissingHash    = errors.New("lsd: missing Infohash header")
	ErrBadInfohash    = errors.New("lsd: invalid infohash")
)

// Announce represents a parsed LSD announcement.
type Announce struct {
	Host     string
	Port     uint16
	Infohash [20]byte
	Cookie   string
}

// FormatAnnounce serializes an Announce into the BT-SEARCH wire format.
func FormatAnnounce(a *Announce) []byte {
	ih := hex.EncodeToString(a.Infohash[:])
	msg := fmt.Sprintf("%s\r\nHost: %s\r\nPort: %d\r\nInfohash: %s\r\ncookie: %s\r\n\r\n",
		requestLine, a.Host, a.Port, ih, a.Cookie)
	return []byte(msg)
}

// ParseAnnounce parses a raw UDP datagram into an Announce.
func ParseAnnounce(data []byte) (*Announce, error) {
	lines := strings.Split(string(data), "\r\n")
	if len(lines) == 0 || lines[0] != requestLine {
		return nil, ErrBadRequestLine
	}

	var a Announce
	var hasPort, hasHash bool

	for _, line := range lines[1:] {
		if line == "" {
			continue
		}
		idx := strings.IndexByte(line, ':')
		if idx < 0 {
			continue
		}
		key := strings.ToLower(strings.TrimSpace(line[:idx]))
		val := strings.TrimSpace(line[idx+1:])

		switch key {
		case "host":
			a.Host = val
		case "port":
			p, err := strconv.ParseUint(val, 10, 16)
			if err != nil {
				return nil, fmt.Errorf("lsd: bad port %q: %w", val, err)
			}
			a.Port = uint16(p)
			hasPort = true
		case "infohash":
			if len(val) != 40 {
				return nil, fmt.Errorf("%w: length %d", ErrBadInfohash, len(val))
			}
			b, err := hex.DecodeString(val)
			if err != nil {
				return nil, fmt.Errorf("%w: %v", ErrBadInfohash, err)
			}
			copy(a.Infohash[:], b)
			hasHash = true
		case "cookie":
			a.Cookie = val
		}
	}

	if !hasPort {
		return nil, ErrMissingPort
	}
	if !hasHash {
		return nil, ErrMissingHash
	}
	return &a, nil
}
