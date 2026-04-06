// Package dht implements the BitTorrent Distributed Hash Table (BEP 5).
//
// DHT enables trackerless peer discovery using a Kademlia-based distributed
// hash table. Peers announce which torrents they are sharing and can look up
// other peers for a given info_hash without relying on a central tracker.
//
// The protocol uses KRPC (a bencoded RPC protocol over UDP) with four
// operations: ping, find_node, get_peers, and announce_peer.
//
// Reference: https://www.bittorrent.org/beps/bep_0005.html
package dht

import (
	"crypto/rand"
	"fmt"
	"net"
	"sync"
	"time"

	"github.com/ihvo/peer-pressure/bencode"
)

// NodeID is a 160-bit identifier for a DHT node.
type NodeID [20]byte

// RandomNodeID generates a cryptographically random node ID.
func RandomNodeID() NodeID {
	var id NodeID
	rand.Read(id[:])
	return id
}

// Message represents a KRPC message (query, response, or error).
//
// BEP 5 format:
//
//	{"t": "<txn_id>", "y": "q|r|e", "q": "<method>", "a|r|e": {...}}
type Message struct {
	TxnID  string      // "t" — transaction ID matching request to response
	Type   string      // "y" — "q" (query), "r" (response), "e" (error)
	Method string      // "q" — query method name (only for queries)
	Args   bencode.Dict // "a" — query arguments (only for queries)
	Reply  bencode.Dict // "r" — response body (only for responses)
	Error  []any       // "e" — [code, message] (only for errors)
}

// EncodeMessage bencodes a KRPC message for transmission.
func EncodeMessage(msg Message) []byte {
	d := bencode.Dict{
		"t": bencode.String(msg.TxnID),
		"y": bencode.String(msg.Type),
	}
	switch msg.Type {
	case "q":
		d["q"] = bencode.String(msg.Method)
		d["a"] = msg.Args
	case "r":
		d["r"] = msg.Reply
	case "e":
		items := make(bencode.List, len(msg.Error))
		for i, v := range msg.Error {
			switch val := v.(type) {
			case int:
				items[i] = bencode.Int(val)
			case string:
				items[i] = bencode.String(val)
			}
		}
		d["e"] = items
	}
	return bencode.Encode(d)
}

// DecodeMessage parses a bencoded KRPC message.
func DecodeMessage(data []byte) (Message, error) {
	val, err := bencode.Decode(data)
	if err != nil {
		return Message{}, fmt.Errorf("decode krpc: %w", err)
	}
	d, ok := val.(bencode.Dict)
	if !ok {
		return Message{}, fmt.Errorf("krpc message is not a dict")
	}

	msg := Message{}

	// Transaction ID.
	tVal, ok := d["t"]
	if !ok {
		return msg, fmt.Errorf("krpc: missing 't' (transaction ID)")
	}
	if t, ok := tVal.(bencode.String); ok {
		msg.TxnID = string(t)
	}

	// Message type.
	yVal, ok := d["y"]
	if !ok {
		return msg, fmt.Errorf("krpc: missing 'y' (message type)")
	}
	if y, ok := yVal.(bencode.String); ok {
		msg.Type = string(y)
	}

	switch msg.Type {
	case "q":
		if q, ok := d["q"].(bencode.String); ok {
			msg.Method = string(q)
		}
		if a, ok := d["a"].(bencode.Dict); ok {
			msg.Args = a
		}
	case "r":
		if r, ok := d["r"].(bencode.Dict); ok {
			msg.Reply = r
		}
	case "e":
		if e, ok := d["e"].(bencode.List); ok {
			for _, item := range e {
				switch v := item.(type) {
				case bencode.Int:
					msg.Error = append(msg.Error, int(v))
				case bencode.String:
					msg.Error = append(msg.Error, string(v))
				}
			}
		}
	}

	return msg, nil
}

// Transport handles KRPC message exchange over UDP with transaction matching.
type Transport struct {
	conn *net.UDPConn
	mu   sync.Mutex
	txns map[string]chan Message // pending transaction ID → response channel
	txnN uint16                 // counter for generating transaction IDs
}

// NewTransport creates a Transport bound to the given UDP connection.
// Call Listen() in a goroutine to start receiving messages.
func NewTransport(conn *net.UDPConn) *Transport {
	return &Transport{
		conn: conn,
		txns: make(map[string]chan Message),
	}
}

// Listen reads incoming UDP packets, dispatching responses to pending
// transactions and passing queries to the handler function.
// Blocks until the connection is closed.
func (t *Transport) Listen(handler func(msg Message, addr *net.UDPAddr)) {
	buf := make([]byte, 4096) // KRPC messages are small
	for {
		n, addr, err := t.conn.ReadFromUDP(buf)
		if err != nil {
			return // connection closed
		}
		msg, err := DecodeMessage(buf[:n])
		if err != nil {
			continue
		}

		if msg.Type == "r" || msg.Type == "e" {
			t.mu.Lock()
			ch, ok := t.txns[msg.TxnID]
			if ok {
				delete(t.txns, msg.TxnID)
			}
			t.mu.Unlock()
			if ok {
				ch <- msg
			}
			continue
		}

		// Incoming query — pass to handler.
		if handler != nil {
			handler(msg, addr)
		}
	}
}

// Send sends a KRPC query and waits for the response (with timeout).
func (t *Transport) Send(addr *net.UDPAddr, msg Message, timeout time.Duration) (Message, error) {
	// Assign a transaction ID.
	t.mu.Lock()
	t.txnN++
	txnID := fmt.Sprintf("%02x", t.txnN)
	ch := make(chan Message, 1)
	t.txns[txnID] = ch
	t.mu.Unlock()

	msg.TxnID = txnID
	data := EncodeMessage(msg)

	if _, err := t.conn.WriteToUDP(data, addr); err != nil {
		t.mu.Lock()
		delete(t.txns, txnID)
		t.mu.Unlock()
		return Message{}, fmt.Errorf("send krpc: %w", err)
	}

	select {
	case resp := <-ch:
		return resp, nil
	case <-time.After(timeout):
		t.mu.Lock()
		delete(t.txns, txnID)
		t.mu.Unlock()
		return Message{}, fmt.Errorf("krpc timeout after %v", timeout)
	}
}

// Close closes the underlying UDP connection.
func (t *Transport) Close() error {
	return t.conn.Close()
}
