package peer

import (
	"bufio"
	"fmt"
	"net"
	"time"
)

// Conn wraps a TCP connection to a BitTorrent peer, providing buffered
// message-level read/write operations. Both reads and writes are buffered:
// reads via bufio.Reader (avoids many small Read syscalls when parsing
// length-prefixed messages), writes via bufio.Writer (batches multiple
// small Request messages into fewer TCP segments).
type Conn struct {
	conn   net.Conn
	reader *bufio.Reader
	writer *bufio.Writer
	// The peer's info from the handshake
	PeerID   [20]byte
	InfoHash [20]byte
	Reserved [8]byte // peer's reserved bytes from handshake

	// Extension protocol state (BEP 10).
	// Populated after ExchangeExtHandshake().
	PeerExtensions *ExtHandshake
}

// Dial connects to a peer, performs the handshake, and returns a Conn.
// Uses a 2-second connection timeout. Returns an error if the peer's
// info_hash doesn't match ours.
func Dial(addr string, infoHash, peerID [20]byte) (*Conn, error) {
	conn, err := net.DialTimeout("tcp", addr, 2*time.Second)
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

// Accept performs a server-side handshake on an incoming connection.
// It reads the peer's handshake first to learn the requested infohash,
// then calls lookupHash to validate it. If lookupHash returns false the
// connection is closed. Otherwise our handshake is sent back.
func Accept(conn net.Conn, peerID [20]byte, lookupHash func([20]byte) bool) (*Conn, error) {
	conn.SetDeadline(time.Now().Add(10 * time.Second))
	defer conn.SetDeadline(time.Time{})

	peerHS, err := ReadHandshake(conn)
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("read peer handshake: %w", err)
	}

	if !lookupHash(peerHS.InfoHash) {
		conn.Close()
		return nil, fmt.Errorf("unknown infohash %x", peerHS.InfoHash)
	}

	hs := &Handshake{InfoHash: peerHS.InfoHash, PeerID: peerID}
	if err := WriteHandshake(conn, hs); err != nil {
		conn.Close()
		return nil, fmt.Errorf("write handshake: %w", err)
	}

	return &Conn{
		conn:     conn,
		reader:   bufio.NewReaderSize(conn, 128*1024),
		writer:   bufio.NewWriterSize(conn, 128*1024),
		PeerID:   peerHS.PeerID,
		InfoHash: peerHS.InfoHash,
		Reserved: peerHS.Reserved,
	}, nil
}

func doHandshake(conn net.Conn, infoHash, peerID [20]byte) (*Conn, error) {
	hs := &Handshake{InfoHash: infoHash, PeerID: peerID}

	// Set a generous deadline for the handshake exchange.
	conn.SetDeadline(time.Now().Add(10 * time.Second))
	defer conn.SetDeadline(time.Time{}) // clear after handshake

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
		reader:   bufio.NewReaderSize(conn, 128*1024),
		writer:   bufio.NewWriterSize(conn, 128*1024),
		PeerID:   peerHS.PeerID,
		InfoHash: peerHS.InfoHash,
		Reserved: peerHS.Reserved,
	}, nil
}

// SetDeadline sets a read+write deadline on the underlying connection.
func (c *Conn) SetDeadline(d time.Time) error {
	return c.conn.SetDeadline(d)
}

// ReadMessage reads the next message from the peer. Returns nil for keep-alive.
func (c *Conn) ReadMessage() (*Message, error) {
	return ReadMessage(c.reader)
}

// WriteMessage writes a message to the buffered writer. The message is NOT
// immediately sent on the wire — call Flush() to push buffered data to the
// socket. This lets callers batch multiple small messages (e.g., a burst of
// Request messages) into fewer TCP segments.
func (c *Conn) WriteMessage(m *Message) error {
	return WriteMessage(c.writer, m)
}

// Flush pushes any buffered write data to the underlying TCP connection.
// Call this after writing a batch of messages to ensure they're sent.
func (c *Conn) Flush() error {
	return c.writer.Flush()
}

// Close closes the underlying TCP connection.
func (c *Conn) Close() error {
	return c.conn.Close()
}

// RemoteAddr returns the peer's network address.
func (c *Conn) RemoteAddr() net.Addr {
	return c.conn.RemoteAddr()
}

// SupportsExtensions returns true if the peer advertised BEP 10 support.
func (c *Conn) SupportsExtensions() bool {
	return c.Reserved[5]&0x10 != 0
}

// ExchangeExtHandshake sends our extension handshake and reads the peer's.
// Only call if SupportsExtensions() is true. Stores the result in PeerExtensions.
func (c *Conn) ExchangeExtHandshake(exts map[string]int, metadataSize int, clientVersion string) error {
	// Write and read concurrently — net.Pipe (and similar unbuffered
	// transports) deadlock if both sides flush before either reads.
	writeErr := make(chan error, 1)
	go func() {
		err := c.WriteMessage(NewExtHandshake(exts, metadataSize, clientVersion))
		if err == nil {
			err = c.Flush()
		}
		writeErr <- err
	}()

	// Read messages until we get the extension handshake.
	// The peer may send bitfield/have messages before it.
	for {
		c.SetDeadline(time.Now().Add(10 * time.Second))
		msg, err := c.ReadMessage()
		if err != nil {
			return fmt.Errorf("read ext handshake: %w", err)
		}
		if msg == nil {
			continue // keep-alive
		}
		if msg.ID != MsgExtended || len(msg.Payload) == 0 || msg.Payload[0] != 0 {
			continue // not an extension handshake, skip
		}

		if err := <-writeErr; err != nil {
			return fmt.Errorf("write ext handshake: %w", err)
		}

		ext, err := ParseExtHandshake(msg.Payload)
		if err != nil {
			return err
		}
		c.PeerExtensions = ext
		c.SetDeadline(time.Time{})
		return nil
	}
}
