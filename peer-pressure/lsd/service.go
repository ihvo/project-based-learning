package lsd

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"math/big"
	"net"
	"sync"
	"time"
)

const (
	announceInterval = 5 * time.Minute
	announceJitter   = 30 * time.Second
	multicastTTL     = 1
	maxDatagram      = 512
)

// Peer represents a peer discovered via LSD.
type Peer struct {
	Addr     string // "ip:port"
	Infohash [20]byte
}

// Service manages LSD multicast announcing and listening.
type Service struct {
	listenPort uint16
	cookie     string
	active     map[[20]byte]bool
	mu         sync.RWMutex
	peers      chan<- Peer
}

// New creates a new LSD service. Discovered peers are sent to the peers channel.
func New(listenPort uint16, peers chan<- Peer) *Service {
	return &Service{
		listenPort: listenPort,
		cookie:     generateCookie(),
		active:     make(map[[20]byte]bool),
		peers:      peers,
	}
}

// AddInfohash registers a torrent for LSD announcing and discovery.
func (s *Service) AddInfohash(ih [20]byte) {
	s.mu.Lock()
	s.active[ih] = true
	s.mu.Unlock()
}

// RemoveInfohash stops announcing a torrent.
func (s *Service) RemoveInfohash(ih [20]byte) {
	s.mu.Lock()
	delete(s.active, ih)
	s.mu.Unlock()
}

// Run starts the multicast listener and announcer. Blocks until ctx is cancelled.
func (s *Service) Run(ctx context.Context) error {
	mcastAddr, err := net.ResolveUDPAddr("udp4", fmt.Sprintf("%s:%d", IPv4Multicast, Port))
	if err != nil {
		return fmt.Errorf("lsd: resolve multicast: %w", err)
	}

	listenConn, err := net.ListenMulticastUDP("udp4", nil, mcastAddr)
	if err != nil {
		return fmt.Errorf("lsd: join multicast: %w", err)
	}
	defer listenConn.Close()
	listenConn.SetReadBuffer(maxDatagram * 4)

	var wg sync.WaitGroup

	// Listener goroutine
	wg.Add(1)
	go func() {
		defer wg.Done()
		s.listen(ctx, listenConn)
	}()

	// Announcer goroutine
	wg.Add(1)
	go func() {
		defer wg.Done()
		s.announce(ctx, mcastAddr)
	}()

	<-ctx.Done()
	listenConn.Close() // unblocks ReadFromUDP
	wg.Wait()
	return nil
}

func (s *Service) listen(ctx context.Context, conn *net.UDPConn) {
	buf := make([]byte, maxDatagram)
	for {
		n, src, err := conn.ReadFromUDP(buf)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			slog.Debug("lsd: read error", "err", err)
			continue
		}

		a, err := ParseAnnounce(buf[:n])
		if err != nil {
			continue
		}

		// Filter own announcements
		if a.Cookie == s.cookie {
			continue
		}

		// Filter inactive infohashes
		s.mu.RLock()
		interested := s.active[a.Infohash]
		s.mu.RUnlock()
		if !interested {
			continue
		}

		addr := net.JoinHostPort(src.IP.String(), fmt.Sprintf("%d", a.Port))
		select {
		case s.peers <- Peer{Addr: addr, Infohash: a.Infohash}:
		case <-ctx.Done():
			return
		}
	}
}

func (s *Service) announce(ctx context.Context, mcastAddr *net.UDPAddr) {
	// Immediate first announce
	s.sendAnnounces(mcastAddr)

	for {
		jitter := jitterDuration(announceJitter)
		select {
		case <-time.After(announceInterval + jitter):
			s.sendAnnounces(mcastAddr)
		case <-ctx.Done():
			return
		}
	}
}

func (s *Service) sendAnnounces(mcastAddr *net.UDPAddr) {
	conn, err := net.DialUDP("udp4", nil, mcastAddr)
	if err != nil {
		slog.Debug("lsd: dial multicast", "err", err)
		return
	}
	defer conn.Close()

	s.mu.RLock()
	hashes := make([][20]byte, 0, len(s.active))
	for ih := range s.active {
		hashes = append(hashes, ih)
	}
	s.mu.RUnlock()

	host := fmt.Sprintf("%s:%d", IPv4Multicast, Port)
	for _, ih := range hashes {
		a := &Announce{
			Host:     host,
			Port:     s.listenPort,
			Infohash: ih,
			Cookie:   s.cookie,
		}
		data := FormatAnnounce(a)
		if _, err := conn.Write(data); err != nil {
			slog.Debug("lsd: send announce", "err", err)
		}
		// Stagger slightly between torrents to avoid packet loss
		time.Sleep(10 * time.Millisecond)
	}
}

func generateCookie() string {
	b := make([]byte, 8)
	rand.Read(b)
	return "pp-" + hex.EncodeToString(b)
}

func jitterDuration(max time.Duration) time.Duration {
	n, _ := rand.Int(rand.Reader, big.NewInt(int64(2*max)))
	return time.Duration(n.Int64()) - max
}
