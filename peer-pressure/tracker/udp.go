// UDP tracker protocol implementation (BEP 15).
//
// UDP trackers replace HTTP announce with a lightweight binary protocol
// over UDP. Two round-trips: connect (anti-spoofing handshake) then
// announce (same data as HTTP, but binary-encoded).
//
// Retry uses exponential backoff: timeout = 15 × 2^n seconds per attempt.
//
// Reference: https://www.bittorrent.org/beps/bep_0015.html
package tracker

import (
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"net"
	"net/url"
	"time"
)

// UDP tracker protocol constants.
const (
	udpProtocolID uint64 = 0x41727101980 // magic constant per BEP 15

	actionConnect  uint32 = 0
	actionAnnounce uint32 = 1

	// BEP 15 event codes (different from HTTP string values).
	eventNone      uint32 = 0
	eventCompleted uint32 = 1
	eventStarted   uint32 = 2
	eventStopped   uint32 = 3

	// We cap individual attempt timeouts for usability. BEP 15's formula
	// (15 × 2^n) reaches 1920s at n=7 — far too long for interactive use.
	maxTimeout = 60 * time.Second
	maxRetries = 4 // 15s, 30s, 60s, 60s — then give up
)

// announceUDP performs a full UDP tracker announce: connect then announce.
// The URL must be udp://host:port/announce (path is ignored).
func announceUDP(rawURL string, params AnnounceParams) (*Response, error) {
	host, err := udpHostFromURL(rawURL)
	if err != nil {
		return nil, err
	}

	// Resolve DNS and dial a "connected" UDP socket. Connected means the OS
	// filters incoming packets to only this remote address, and we can use
	// simple Read/Write instead of ReadFrom/WriteTo.
	raddr, err := net.ResolveUDPAddr("udp", host)
	if err != nil {
		return nil, fmt.Errorf("resolve tracker: %w", err)
	}

	conn, err := net.DialUDP("udp", nil, raddr)
	if err != nil {
		return nil, fmt.Errorf("dial tracker: %w", err)
	}
	defer conn.Close()

	// Step 1: connect — get a connection_id to prove we're not IP-spoofing.
	connID, err := udpConnect(conn)
	if err != nil {
		return nil, fmt.Errorf("udp connect: %w", err)
	}

	// Step 2: announce — send our stats, get peers back.
	resp, err := udpAnnounce(conn, connID, params)
	if err != nil {
		return nil, fmt.Errorf("udp announce: %w", err)
	}

	return resp, nil
}

// udpConnect performs the BEP 15 connect handshake with retries.
//
//	Request:  [8B protocol_id] [4B action=0] [4B txn_id]       = 16 bytes
//	Response: [4B action=0]    [4B txn_id]   [8B connection_id] = 16 bytes
func udpConnect(conn *net.UDPConn) (uint64, error) {
	txnID := randUint32()

	// Build connect request: 16 bytes, fixed layout.
	var req [16]byte
	binary.BigEndian.PutUint64(req[0:8], udpProtocolID)
	binary.BigEndian.PutUint32(req[8:12], actionConnect)
	binary.BigEndian.PutUint32(req[12:16], txnID)

	resp, err := udpRoundTrip(conn, req[:], 16)
	if err != nil {
		return 0, err
	}

	// Parse connect response.
	action := binary.BigEndian.Uint32(resp[0:4])
	respTxn := binary.BigEndian.Uint32(resp[4:8])
	connID := binary.BigEndian.Uint64(resp[8:16])

	if action != actionConnect {
		return 0, fmt.Errorf("expected action=connect(0), got %d", action)
	}
	if respTxn != txnID {
		return 0, fmt.Errorf("transaction ID mismatch: sent %d, got %d", txnID, respTxn)
	}

	return connID, nil
}

// udpAnnounce sends an announce request and parses the peer list.
//
//	Request:  98 bytes (connection_id + action + txn + info_hash + peer_id + stats + port)
//	Response: 20 bytes header + N×6 bytes peers
func udpAnnounce(conn *net.UDPConn, connID uint64, p AnnounceParams) (*Response, error) {
	txnID := randUint32()

	// Build announce request: 98 bytes, fixed layout.
	var req [98]byte
	binary.BigEndian.PutUint64(req[0:8], connID)
	binary.BigEndian.PutUint32(req[8:12], actionAnnounce)
	binary.BigEndian.PutUint32(req[12:16], txnID)
	copy(req[16:36], p.InfoHash[:])
	copy(req[36:56], p.PeerID[:])
	binary.BigEndian.PutUint64(req[56:64], uint64(p.Downloaded))
	binary.BigEndian.PutUint64(req[64:72], uint64(p.Left))
	binary.BigEndian.PutUint64(req[72:80], uint64(p.Uploaded))
	binary.BigEndian.PutUint32(req[80:84], eventCode(p.Event))
	binary.BigEndian.PutUint32(req[84:88], 0) // IP = 0 (use source)
	binary.BigEndian.PutUint32(req[88:92], randUint32()) // key
	numWant := int32(-1) // default
	if p.NumWant > 0 {
		numWant = int32(p.NumWant)
	}
	binary.BigEndian.PutUint32(req[92:96], uint32(numWant))
	binary.BigEndian.PutUint16(req[96:98], p.Port)

	// Response: 20-byte header + variable-length compact peers.
	// Max UDP packet is ~65535 bytes, but trackers typically fit in one packet.
	resp, err := udpRoundTrip(conn, req[:], 20)
	if err != nil {
		return nil, err
	}

	// Parse announce response header.
	action := binary.BigEndian.Uint32(resp[0:4])
	respTxn := binary.BigEndian.Uint32(resp[4:8])

	if action != actionAnnounce {
		return nil, fmt.Errorf("expected action=announce(1), got %d", action)
	}
	if respTxn != txnID {
		return nil, fmt.Errorf("transaction ID mismatch: sent %d, got %d", txnID, respTxn)
	}

	interval := binary.BigEndian.Uint32(resp[8:12])
	leechers := binary.BigEndian.Uint32(resp[12:16])
	seeders := binary.BigEndian.Uint32(resp[16:20])

	// Remaining bytes are compact peers (6 bytes each, same format as HTTP compact).
	peerData := resp[20:]
	peers, err := parseCompactPeers(peerData)
	if err != nil {
		return nil, fmt.Errorf("parse peers: %w", err)
	}

	return &Response{
		Interval:   int(interval),
		Peers:      peers,
		Complete:   int(seeders),
		Incomplete: int(leechers),
	}, nil
}

// udpRoundTrip sends a request and waits for a response with BEP 15 retries.
// The timeout formula is 15 × 2^n seconds, capped at maxTimeout.
// minResp is the minimum valid response size in bytes.
func udpRoundTrip(conn *net.UDPConn, req []byte, minResp int) ([]byte, error) {
	buf := make([]byte, 4096) // plenty for any tracker response

	for n := range maxRetries {
		// Linear backoff: 2s, 3s, 4s, 5s.
		timeout := time.Duration(2+n) * time.Second

		if _, err := conn.Write(req); err != nil {
			return nil, fmt.Errorf("send: %w", err)
		}

		conn.SetReadDeadline(time.Now().Add(timeout))
		nRead, err := conn.Read(buf)
		if err != nil {
			// Timeout — retry with longer deadline.
			if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
				continue
			}
			return nil, fmt.Errorf("read (attempt %d): %w", n+1, err)
		}

		if nRead < minResp {
			// Check for error response first (action=3, at least 8 bytes).
			if nRead >= 8 {
				action := binary.BigEndian.Uint32(buf[0:4])
				if action == 3 {
					msg := string(buf[8:nRead])
					return nil, fmt.Errorf("tracker error: %s", msg)
				}
			}
			return nil, fmt.Errorf("response too short: got %d bytes, need >= %d", nRead, minResp)
		}

		// Check for error response (action=3).
		if nRead >= 8 {
			action := binary.BigEndian.Uint32(buf[0:4])
			if action == 3 {
				msg := string(buf[8:nRead])
				return nil, fmt.Errorf("tracker error: %s", msg)
			}
		}

		return buf[:nRead], nil
	}

	return nil, fmt.Errorf("no response after %d attempts", maxRetries)
}

// udpHostFromURL extracts host:port from a udp:// tracker URL.
func udpHostFromURL(rawURL string) (string, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return "", fmt.Errorf("parse URL: %w", err)
	}
	if u.Scheme != "udp" {
		return "", fmt.Errorf("expected udp:// scheme, got %s://", u.Scheme)
	}
	host := u.Host
	if _, _, err := net.SplitHostPort(host); err != nil {
		// No port specified — unlikely for UDP trackers, but handle it.
		host = net.JoinHostPort(host, "6969")
	}
	return host, nil
}

// eventCode converts the string event name (used by HTTP trackers) to the
// numeric code used by UDP trackers.
func eventCode(event string) uint32 {
	switch event {
	case "completed":
		return eventCompleted
	case "started":
		return eventStarted
	case "stopped":
		return eventStopped
	default:
		return eventNone
	}
}

// randUint32 generates a cryptographically random uint32 for transaction IDs.
// Crypto/rand prevents other clients from predicting our transaction IDs
// and injecting fake tracker responses.
func randUint32() uint32 {
	var b [4]byte
	rand.Read(b[:])
	return binary.BigEndian.Uint32(b[:])
}
