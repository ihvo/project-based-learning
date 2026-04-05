package peer

import (
	"bufio"
	"fmt"
	"net"
	"time"
)

// Conn wraps a TCP connection to a BitTorrent peer, providing buffered
// message-level read/write operations.
type Conn struct {
	conn   net.Conn
	reader *bufio.Reader
	// The peer's info from the handshake
	PeerID   [20]byte
	InfoHash [20]byte
}

// Dial connects to a peer, performs the handshake, and returns a Conn.
// Uses a 5-second connection timeout. Returns an error if the peer's
// info_hash doesn't match ours.
func Dial(addr string, infoHash, peerID [20]byte) (*Conn, error) {
	conn, err := net.DialTimeout("tcp", addr, 5*time.Second)
	if err != nil {
		return nil, fmt.Errorf("connect to peer: %w", err)
	}

	pc, err := doHandshake(conn, infoHash, peerID)
	if err != nil {
		conn.Close()
		return nil, err
	}
	return pc, nil
}

// FromConn wraps an existing net.Conn (e.g., from a listener) and performs
// the server side of the handshake: read theirs first, then send ours.
func FromConn(conn net.Conn, infoHash, peerID [20]byte) (*Conn, error) {
	pc, err := doHandshake(conn, infoHash, peerID)
	if err != nil {
		conn.Close()
		return nil, err
	}
	return pc, nil
}

func doHandshake(conn net.Conn, infoHash, peerID [20]byte) (*Conn, error) {
	hs := &Handshake{InfoHash: infoHash, PeerID: peerID}

	// Write and read concurrently. This is necessary because net.Pipe (and
	// similar unbuffered transports) will deadlock if both sides write before
	// either reads. With real TCP sockets the kernel buffer absorbs the 68-byte
	// handshake, but being concurrent here is correct for all cases.
	writeErr := make(chan error, 1)
	go func() {
		writeErr <- WriteHandshake(conn, hs)
	}()

	peerHS, err := ReadHandshake(conn)
	if err != nil {
		return nil, fmt.Errorf("read peer handshake: %w", err)
	}

	if err := <-writeErr; err != nil {
		return nil, fmt.Errorf("write handshake: %w", err)
	}

	if peerHS.InfoHash != infoHash {
		return nil, fmt.Errorf("info hash mismatch: got %x, want %x", peerHS.InfoHash, infoHash)
	}

	return &Conn{
		conn:     conn,
		reader:   bufio.NewReader(conn),
		PeerID:   peerHS.PeerID,
		InfoHash: peerHS.InfoHash,
	}, nil
}

// ReadMessage reads the next message from the peer. Returns nil for keep-alive.
func (c *Conn) ReadMessage() (*Message, error) {
	return ReadMessage(c.reader)
}

// WriteMessage sends a message to the peer. Pass nil for keep-alive.
func (c *Conn) WriteMessage(m *Message) error {
	return WriteMessage(c.conn, m)
}

// Close closes the underlying TCP connection.
func (c *Conn) Close() error {
	return c.conn.Close()
}

// RemoteAddr returns the peer's network address.
func (c *Conn) RemoteAddr() net.Addr {
	return c.conn.RemoteAddr()
}
