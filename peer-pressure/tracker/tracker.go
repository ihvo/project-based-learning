// Package tracker implements the BitTorrent HTTP tracker protocol (BEP 3).
//
// A tracker is an HTTP service that helps peers find each other. Clients send
// an "announce" request with their info_hash, peer_id, and stats. The tracker
// responds with a list of peers in the swarm.
//
// This package supports the compact peer format (BEP 23), which encodes each
// peer as 6 bytes (4 for IPv4 + 2 for port).
//
// Reference: https://www.bittorrent.org/beps/bep_0003.html
// Reference: https://www.bittorrent.org/beps/bep_0023.html
package tracker

import (
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/ihvo/peer-pressure/bencode"
)

// Peer represents a peer in the swarm.
type Peer struct {
	IP   net.IP
	Port uint16
}

func (p Peer) String() string {
	return fmt.Sprintf("%s:%d", p.IP, p.Port)
}

// Addr returns the peer as a TCP address string.
func (p Peer) Addr() string {
	return net.JoinHostPort(p.IP.String(), strconv.Itoa(int(p.Port)))
}

// Response holds the tracker's response to an announce request.
type Response struct {
	Interval int    // seconds between re-announces
	Peers    []Peer // peers in the swarm
	Complete int    // seeders (optional)
	Incomplete int  // leechers (optional)
}

// AnnounceParams holds the parameters for an announce request.
type AnnounceParams struct {
	InfoHash   [20]byte // torrent identity
	PeerID     [20]byte // our unique ID
	Port       uint16   // port we're listening on
	Uploaded   int64    // bytes uploaded so far
	Downloaded int64    // bytes downloaded so far
	Left       int64    // bytes remaining
	Event      string   // "started", "completed", "stopped", or "" for regular
	NumWant    int      // number of peers requested (0 = tracker default)
}

// Announce sends an HTTP announce request to the tracker and returns the response.
func Announce(trackerURL string, params AnnounceParams) (*Response, error) {
	reqURL, err := buildAnnounceURL(trackerURL, params)
	if err != nil {
		return nil, fmt.Errorf("build announce URL: %w", err)
	}

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Get(reqURL)
	if err != nil {
		return nil, fmt.Errorf("tracker request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("tracker returned HTTP %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read tracker response: %w", err)
	}

	return parseResponse(body)
}

func buildAnnounceURL(trackerURL string, p AnnounceParams) (string, error) {
	base, err := url.Parse(trackerURL)
	if err != nil {
		return "", fmt.Errorf("parse tracker URL: %w", err)
	}

	// We build the query string manually because info_hash and peer_id are
	// raw binary and need byte-level percent-encoding, not Go's standard
	// url.Values encoding (which would encode spaces as "+" etc).
	params := base.Query()
	params.Set("port", strconv.Itoa(int(p.Port)))
	params.Set("uploaded", strconv.FormatInt(p.Uploaded, 10))
	params.Set("downloaded", strconv.FormatInt(p.Downloaded, 10))
	params.Set("left", strconv.FormatInt(p.Left, 10))
	params.Set("compact", "1")

	if p.Event != "" {
		params.Set("event", p.Event)
	}
	if p.NumWant > 0 {
		params.Set("numwant", strconv.Itoa(p.NumWant))
	}

	// Build the final URL with manually encoded binary fields
	base.RawQuery = params.Encode() +
		"&info_hash=" + percentEncodeBytes(p.InfoHash[:]) +
		"&peer_id=" + percentEncodeBytes(p.PeerID[:])

	return base.String(), nil
}

// percentEncodeBytes encodes raw bytes using percent-encoding.
// Unreserved characters (RFC 3986: A-Z, a-z, 0-9, -._~) pass through;
// everything else becomes %XX.
func percentEncodeBytes(b []byte) string {
	var buf strings.Builder
	buf.Grow(len(b) * 3) // worst case: every byte gets %XX
	for _, c := range b {
		if isUnreserved(c) {
			buf.WriteByte(c)
		} else {
			fmt.Fprintf(&buf, "%%%02X", c)
		}
	}
	return buf.String()
}

func isUnreserved(c byte) bool {
	return (c >= 'A' && c <= 'Z') ||
		(c >= 'a' && c <= 'z') ||
		(c >= '0' && c <= '9') ||
		c == '-' || c == '_' || c == '.' || c == '~'
}

// parseResponse decodes a bencoded tracker response.
func parseResponse(data []byte) (*Response, error) {
	val, err := bencode.Decode(data)
	if err != nil {
		return nil, fmt.Errorf("decode tracker response: %w", err)
	}

	d, ok := val.(bencode.Dict)
	if !ok {
		return nil, fmt.Errorf("tracker response is not a dict")
	}

	// Check for tracker error
	if failReason, ok := d["failure reason"]; ok {
		if s, ok := failReason.(bencode.String); ok {
			return nil, fmt.Errorf("tracker error: %s", string(s))
		}
	}

	r := &Response{}

	// interval
	if iv, ok := d["interval"].(bencode.Int); ok {
		r.Interval = int(iv)
	}

	// complete / incomplete (optional)
	if c, ok := d["complete"].(bencode.Int); ok {
		r.Complete = int(c)
	}
	if ic, ok := d["incomplete"].(bencode.Int); ok {
		r.Incomplete = int(ic)
	}

	// peers — compact format (byte string) or dictionary format (list)
	peersVal, ok := d["peers"]
	if !ok {
		return r, nil
	}

	switch p := peersVal.(type) {
	case bencode.String:
		// Compact format: 6 bytes per peer
		r.Peers, err = parseCompactPeers([]byte(p))
		if err != nil {
			return nil, fmt.Errorf("parse compact peers: %w", err)
		}
	case bencode.List:
		// Dictionary format: list of dicts with "ip" and "port" keys
		r.Peers, err = parseDictPeers(p)
		if err != nil {
			return nil, fmt.Errorf("parse dict peers: %w", err)
		}
	default:
		return nil, fmt.Errorf("unexpected type for 'peers': %T", peersVal)
	}

	return r, nil
}

const peerCompactLen = 6 // 4 bytes IP + 2 bytes port

// parseCompactPeers parses the compact peer format (BEP 23).
// Each peer is 6 bytes: 4 for IPv4 address + 2 for port, both big-endian.
func parseCompactPeers(data []byte) ([]Peer, error) {
	if len(data)%peerCompactLen != 0 {
		return nil, fmt.Errorf("compact peers length %d not a multiple of %d", len(data), peerCompactLen)
	}

	numPeers := len(data) / peerCompactLen
	peers := make([]Peer, numPeers)

	for i := range numPeers {
		offset := i * peerCompactLen
		// Make a copy of the IP bytes so we don't alias the input
		ip := make(net.IP, 4)
		copy(ip, data[offset:offset+4])
		peers[i] = Peer{
			IP:   ip,
			Port: binary.BigEndian.Uint16(data[offset+4 : offset+6]),
		}
	}

	return peers, nil
}

// parseDictPeers parses the dictionary peer format (original BEP 3).
// Each peer is a dict with "ip" (string) and "port" (int) keys.
func parseDictPeers(list bencode.List) ([]Peer, error) {
	peers := make([]Peer, 0, len(list))

	for i, item := range list {
		d, ok := item.(bencode.Dict)
		if !ok {
			return nil, fmt.Errorf("peer[%d] is not a dict", i)
		}

		ipVal, ok := d["ip"].(bencode.String)
		if !ok {
			return nil, fmt.Errorf("peer[%d]: missing or invalid 'ip'", i)
		}

		portVal, ok := d["port"].(bencode.Int)
		if !ok {
			return nil, fmt.Errorf("peer[%d]: missing or invalid 'port'", i)
		}

		ip := net.ParseIP(string(ipVal))
		if ip == nil {
			return nil, fmt.Errorf("peer[%d]: invalid IP %q", i, string(ipVal))
		}

		peers = append(peers, Peer{
			IP:   ip,
			Port: uint16(portVal),
		})
	}

	return peers, nil
}
