package seed

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"sync"
	"sync/atomic"

	"github.com/ihvo/peer-pressure/peer"
	"github.com/ihvo/peer-pressure/torrent"
)

// Config holds seeder configuration.
type Config struct {
	Torrent     *torrent.Torrent
	DataPath    string   // path to the file or directory containing torrent data
	PeerID      [20]byte
	ListenAddr  string   // e.g. ":6881"
	MaxConns    int      // max simultaneous connections (default 50)
	UploadSlots int      // regular unchoke slots (default 4)
}

// Seeder manages the accept loop, active connections, and choking.
type Seeder struct {
	cfg      Config
	listener net.Listener
	reader   *diskReader
	choker   *Choker

	conns   map[string]*uploadConn
	connsMu sync.Mutex

	uploaded atomic.Int64
}

// New creates a Seeder. Verifies data integrity before returning.
func New(cfg Config) (*Seeder, error) {
	if cfg.MaxConns <= 0 {
		cfg.MaxConns = 50
	}
	if cfg.UploadSlots <= 0 {
		cfg.UploadSlots = 4
	}
	if cfg.ListenAddr == "" {
		cfg.ListenAddr = ":6881"
	}

	r, err := newDiskReader(cfg.Torrent, cfg.DataPath)
	if err != nil {
		return nil, fmt.Errorf("open data: %w", err)
	}

	return &Seeder{
		cfg:    cfg,
		reader: r,
		choker: NewChoker(cfg.UploadSlots),
		conns:  make(map[string]*uploadConn),
	}, nil
}

// Run starts the accept loop and choker. Blocks until ctx is cancelled.
func (s *Seeder) Run(ctx context.Context) error {
	ln, err := net.Listen("tcp", s.cfg.ListenAddr)
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}
	s.listener = ln

	slog.Info("seeding", "torrent", s.cfg.Torrent.Name, "addr", ln.Addr(),
		"pieces", len(s.cfg.Torrent.Pieces))

	// Start choker in background.
	go s.choker.Run(ctx, s.getConns)

	// Accept loop.
	go func() {
		<-ctx.Done()
		ln.Close()
	}()

	for {
		conn, err := ln.Accept()
		if err != nil {
			select {
			case <-ctx.Done():
				return nil
			default:
				return fmt.Errorf("accept: %w", err)
			}
		}

		s.connsMu.Lock()
		if len(s.conns) >= s.cfg.MaxConns {
			s.connsMu.Unlock()
			conn.Close()
			continue
		}
		s.connsMu.Unlock()

		go s.handleConn(ctx, conn)
	}
}

// Stats returns current seeding statistics.
func (s *Seeder) Stats() (conns int, uploaded int64) {
	s.connsMu.Lock()
	conns = len(s.conns)
	s.connsMu.Unlock()
	return conns, s.uploaded.Load()
}

func (s *Seeder) getConns() []*uploadConn {
	s.connsMu.Lock()
	defer s.connsMu.Unlock()
	result := make([]*uploadConn, 0, len(s.conns))
	for _, c := range s.conns {
		result = append(result, c)
	}
	return result
}

func (s *Seeder) handleConn(ctx context.Context, raw net.Conn) {
	infoHash := s.cfg.Torrent.InfoHash

	pc, err := peer.Accept(raw, s.cfg.PeerID, func(h [20]byte) bool {
		return h == infoHash
	})
	if err != nil {
		return
	}
	defer pc.Close()

	addr := pc.RemoteAddr().String()

	// Send bitfield — we have all pieces.
	bf := makeFullBitfield(len(s.cfg.Torrent.Pieces))
	if err := pc.WriteMessage(&peer.Message{ID: peer.MsgBitfield, Payload: bf}); err != nil {
		return
	}
	if err := pc.Flush(); err != nil {
		return
	}

	uc := &uploadConn{
		conn:       pc,
		addr:       addr,
		choked:     true,
		interested: false,
	}

	s.connsMu.Lock()
	s.conns[addr] = uc
	s.connsMu.Unlock()

	defer func() {
		s.connsMu.Lock()
		delete(s.conns, addr)
		s.connsMu.Unlock()
	}()

	s.messageLoop(ctx, uc)
}

func (s *Seeder) messageLoop(ctx context.Context, uc *uploadConn) {
	t := s.cfg.Torrent
	numPieces := len(t.Pieces)
	buf := make([]byte, t.PieceLength)

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		msg, err := uc.conn.ReadMessage()
		if err != nil {
			return
		}
		if msg == nil {
			continue // keep-alive
		}

		switch msg.ID {
		case peer.MsgInterested:
			uc.interested = true
		case peer.MsgNotInterested:
			uc.interested = false
		case peer.MsgRequest:
			if uc.choked {
				continue // silently ignore requests when choked
			}
			if len(msg.Payload) < 12 {
				return
			}
			index := int(msg.Payload[0])<<24 | int(msg.Payload[1])<<16 | int(msg.Payload[2])<<8 | int(msg.Payload[3])
			begin := int(msg.Payload[4])<<24 | int(msg.Payload[5])<<16 | int(msg.Payload[6])<<8 | int(msg.Payload[7])
			length := int(msg.Payload[8])<<24 | int(msg.Payload[9])<<16 | int(msg.Payload[10])<<8 | int(msg.Payload[11])

			if index < 0 || index >= numPieces || length > 16384 || length <= 0 {
				return
			}
			pieceLen := t.PieceLen(index)
			if begin+length > pieceLen {
				return
			}

			n, err := s.reader.ReadPiece(index, buf[:pieceLen])
			if err != nil || n != pieceLen {
				return
			}

			// Build piece message: index(4) + begin(4) + block
			payload := make([]byte, 8+length)
			payload[0] = byte(index >> 24)
			payload[1] = byte(index >> 16)
			payload[2] = byte(index >> 8)
			payload[3] = byte(index)
			payload[4] = byte(begin >> 24)
			payload[5] = byte(begin >> 16)
			payload[6] = byte(begin >> 8)
			payload[7] = byte(begin)
			copy(payload[8:], buf[begin:begin+length])

			if err := uc.conn.WriteMessage(&peer.Message{ID: peer.MsgPiece, Payload: payload}); err != nil {
				return
			}
			if err := uc.conn.Flush(); err != nil {
				return
			}

			uc.uploadBytes.Add(int64(length))
			s.uploaded.Add(int64(length))

		case peer.MsgHave, peer.MsgBitfield:
			// Track peer's bitfield (informational for choking)
		}
	}
}

// makeFullBitfield creates a bitfield with all pieces marked as having.
func makeFullBitfield(numPieces int) []byte {
	n := (numPieces + 7) / 8
	bf := make([]byte, n)
	for i := range bf {
		bf[i] = 0xFF
	}
	// Clear spare bits in last byte.
	spare := n*8 - numPieces
	if spare > 0 {
		bf[n-1] &= 0xFF << spare
	}
	return bf
}
