// Package pex implements BEP 11 — Peer Exchange.
//
// PEX lets connected peers share peer addresses with each other,
// reducing dependence on centralized trackers and DHT. Peers
// periodically exchange diffs of their known peer lists using the
// Extension Protocol (BEP 10).
//
// Reference: https://www.bittorrent.org/beps/bep_0011.html
package pex

import (
	"encoding/binary"
	"fmt"
	"net"

	"github.com/ihvo/peer-pressure/bencode"
)

// Flag bits for the added.f / added6.f byte per peer.
const (
	FlagEncryption uint8 = 0x01 // prefers encrypted connections
	FlagSeed       uint8 = 0x02 // peer is a seeder
	FlagUTP        uint8 = 0x04 // supports uTP (BEP 29)
	FlagHolepunch  uint8 = 0x08 // supports holepunch (BEP 55)
	FlagReachable  uint8 = 0x10 // connectable (not behind NAT)
)

// PeerEntry pairs a compact peer address with per-peer flags.
type PeerEntry struct {
	IP    net.IP
	Port  uint16
	Flags uint8
}

// Addr returns a "host:port" string.
func (e PeerEntry) Addr() string {
	return fmt.Sprintf("%s:%d", e.IP, e.Port)
}

// Message is a decoded PEX payload.
type Message struct {
	Added    []PeerEntry // IPv4 peers added since last message
	Dropped  []PeerEntry // IPv4 peers removed (flags ignored)
	Added6   []PeerEntry // IPv6 peers added
	Dropped6 []PeerEntry // IPv6 peers removed
}

// Encode serializes a PEX message into a bencoded byte slice suitable for
// embedding in a BEP 10 extended message payload.
func (m *Message) Encode() []byte {
	addedPeers, addedFlags := encodeCompactIPv4(m.Added)
	droppedPeers, _ := encodeCompactIPv4(m.Dropped)
	added6Peers, added6Flags := encodeCompactIPv6(m.Added6)
	dropped6Peers, _ := encodeCompactIPv6(m.Dropped6)

	d := bencode.Dict{
		"added":    bencode.String(addedPeers),
		"added.f":  bencode.String(addedFlags),
		"dropped":  bencode.String(droppedPeers),
		"added6":   bencode.String(added6Peers),
		"added6.f": bencode.String(added6Flags),
		"dropped6": bencode.String(dropped6Peers),
	}
	return bencode.Encode(d)
}

// Decode parses a bencoded PEX payload.
func Decode(data []byte) (*Message, error) {
	val, err := bencode.Decode(data)
	if err != nil {
		return nil, fmt.Errorf("decode PEX message: %w", err)
	}
	d, ok := val.(bencode.Dict)
	if !ok {
		return nil, fmt.Errorf("PEX message: expected dict, got %T", val)
	}

	msg := &Message{}

	if msg.Added, err = parseAdded(d, "added", "added.f", 4); err != nil {
		return nil, err
	}
	if msg.Dropped, err = parsePeers(d, "dropped", 4); err != nil {
		return nil, err
	}
	if msg.Added6, err = parseAdded(d, "added6", "added6.f", 16); err != nil {
		return nil, err
	}
	if msg.Dropped6, err = parsePeers(d, "dropped6", 16); err != nil {
		return nil, err
	}

	return msg, nil
}

// parseAdded parses a compact peer list with an associated flags list.
func parseAdded(d bencode.Dict, peersKey, flagsKey string, ipLen int) ([]PeerEntry, error) {
	peerData := getBytes(d, peersKey)
	flagData := getBytes(d, flagsKey)

	entryLen := ipLen + 2
	if len(peerData)%entryLen != 0 {
		return nil, fmt.Errorf("PEX %s: length %d not multiple of %d", peersKey, len(peerData), entryLen)
	}

	numPeers := len(peerData) / entryLen
	if len(flagData) > 0 && len(flagData) != numPeers {
		return nil, fmt.Errorf("PEX %s: %d peers but %d flag bytes", peersKey, numPeers, len(flagData))
	}

	entries := make([]PeerEntry, numPeers)
	for i := range numPeers {
		offset := i * entryLen
		ip := make(net.IP, ipLen)
		copy(ip, peerData[offset:offset+ipLen])
		port := binary.BigEndian.Uint16(peerData[offset+ipLen:])
		var flags uint8
		if i < len(flagData) {
			flags = flagData[i]
		}
		entries[i] = PeerEntry{IP: ip, Port: port, Flags: flags}
	}
	return entries, nil
}

// parsePeers parses a compact peer list (no flags).
func parsePeers(d bencode.Dict, key string, ipLen int) ([]PeerEntry, error) {
	data := getBytes(d, key)
	entryLen := ipLen + 2
	if len(data)%entryLen != 0 {
		return nil, fmt.Errorf("PEX %s: length %d not multiple of %d", key, len(data), entryLen)
	}

	numPeers := len(data) / entryLen
	entries := make([]PeerEntry, numPeers)
	for i := range numPeers {
		offset := i * entryLen
		ip := make(net.IP, ipLen)
		copy(ip, data[offset:offset+ipLen])
		port := binary.BigEndian.Uint16(data[offset+ipLen:])
		entries[i] = PeerEntry{IP: ip, Port: port}
	}
	return entries, nil
}

// encodeCompactIPv4 packs IPv4 peers into 6-byte-per-peer format and flags.
func encodeCompactIPv4(entries []PeerEntry) (peers, flags []byte) {
	peers = make([]byte, 0, len(entries)*6)
	flags = make([]byte, 0, len(entries))
	for _, e := range entries {
		ip := e.IP.To4()
		if ip == nil {
			continue
		}
		peers = append(peers, ip...)
		var portBuf [2]byte
		binary.BigEndian.PutUint16(portBuf[:], e.Port)
		peers = append(peers, portBuf[:]...)
		flags = append(flags, e.Flags)
	}
	return peers, flags
}

// encodeCompactIPv6 packs IPv6 peers into 18-byte-per-peer format and flags.
func encodeCompactIPv6(entries []PeerEntry) (peers, flags []byte) {
	peers = make([]byte, 0, len(entries)*18)
	flags = make([]byte, 0, len(entries))
	for _, e := range entries {
		ip := e.IP.To16()
		if ip == nil {
			continue
		}
		peers = append(peers, ip...)
		var portBuf [2]byte
		binary.BigEndian.PutUint16(portBuf[:], e.Port)
		peers = append(peers, portBuf[:]...)
		flags = append(flags, e.Flags)
	}
	return peers, flags
}

func getBytes(d bencode.Dict, key string) []byte {
	v, ok := d[key]
	if !ok {
		return nil
	}
	s, ok := v.(bencode.String)
	if !ok {
		return nil
	}
	return []byte(s)
}
