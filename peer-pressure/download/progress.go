package download

import (
	"fmt"
	"strings"
	"sync"
	"time"
)

// PieceState represents the download state of a single piece.
type PieceState byte

const (
	StateEmpty    PieceState = iota // no peer has this piece
	StatePending                    // at least one peer has it
	StateActive                     // currently downloading
	StateDone                       // verified and written
)

// PeerStats tracks download contribution from a single peer.
type PeerStats struct {
	Addr      string
	Bitfield  []byte
	HasPieces int // how many pieces the peer's bitfield advertises
	Pieces    int // completed pieces downloaded from this peer
	Blocks    int // blocks received from this peer
	Bytes     int64
}

// Progress tracks download state for terminal visualization.
// All methods are safe for concurrent use.
type Progress struct {
	mu sync.Mutex

	name       string
	totalBytes int64
	numPieces  int
	pieceLen   int

	pieces []PieceState
	peers  map[string]*PeerStats

	startTime  time.Time
	bytesDown  int64
	piecesDone int

	lastLines int // lines printed in last Render, for overwrite
}

// NewProgress creates a progress tracker for a download.
func NewProgress(name string, numPieces, pieceLen int, totalBytes int64) *Progress {
	return &Progress{
		name:       name,
		totalBytes: totalBytes,
		numPieces:  numPieces,
		pieceLen:   pieceLen,
		pieces:     make([]PieceState, numPieces),
		peers:      make(map[string]*PeerStats),
		startTime:  time.Now(),
	}
}

// PeerConnected registers a new peer with its bitfield.
func (p *Progress) PeerConnected(addr string, bitfield []byte) {
	p.mu.Lock()
	defer p.mu.Unlock()

	has := 0
	for i := range p.numPieces {
		if hasPiece(bitfield, i) {
			has++
			if p.pieces[i] == StateEmpty {
				p.pieces[i] = StatePending
			}
		}
	}

	p.peers[addr] = &PeerStats{
		Addr:      addr,
		Bitfield:  bitfield,
		HasPieces: has,
	}
}

// PeerDisconnected removes a peer.
func (p *Progress) PeerDisconnected(addr string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.peers, addr)
}

// PieceStarted marks a piece as actively downloading.
func (p *Progress) PieceStarted(index int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.pieces[index] = StateActive
}

// BlockReceived records a block received from a peer.
func (p *Progress) BlockReceived(addr string, blockBytes int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if ps, ok := p.peers[addr]; ok {
		ps.Blocks++
		ps.Bytes += int64(blockBytes)
	}
	p.bytesDown += int64(blockBytes)
}

// PieceDone marks a piece as completed and credits the peer.
func (p *Progress) PieceDone(index int, addr string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.pieces[index] = StateDone
	p.piecesDone++
	if ps, ok := p.peers[addr]; ok {
		ps.Pieces++
	}
}

// PieceFailed marks a piece back to pending.
func (p *Progress) PieceFailed(index int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.pieces[index] = StatePending
}

// Render returns the full terminal display as a string.
// Uses ANSI color codes for the piece map.
func (p *Progress) Render(width int) string {
	p.mu.Lock()
	defer p.mu.Unlock()

	if width <= 0 {
		width = 80
	}

	var b strings.Builder

	// Header
	pct := 0
	if p.numPieces > 0 {
		pct = p.piecesDone * 100 / p.numPieces
	}
	fmt.Fprintf(&b, "\033[1m⚡ Peer Pressure\033[0m — %s\n", p.name)
	fmt.Fprintf(&b, "   %s total, %s pieces, %d pcs × %s\n",
		formatBytes(p.totalBytes),
		formatBytes(int64(p.pieceLen)),
		p.numPieces,
		formatBytes(int64(BlockSize)),
	)
	b.WriteString("\n")

	// Overall progress bar
	barWidth := width - 30
	if barWidth < 20 {
		barWidth = 20
	}
	filled := 0
	if p.numPieces > 0 {
		filled = p.piecesDone * barWidth / p.numPieces
	}
	active := 0
	for _, s := range p.pieces {
		if s == StateActive {
			active++
		}
	}
	activeBar := 0
	if p.numPieces > 0 {
		activeBar = active * barWidth / p.numPieces
	}
	if activeBar+filled > barWidth {
		activeBar = barWidth - filled
	}

	fmt.Fprintf(&b, "  Progress \033[32m%s\033[33m%s\033[90m%s\033[0m %d%%  %d/%d pcs\n",
		strings.Repeat("█", filled),
		strings.Repeat("▓", activeBar),
		strings.Repeat("░", barWidth-filled-activeBar),
		pct,
		p.piecesDone,
		p.numPieces,
	)

	// Speed & ETA
	elapsed := time.Since(p.startTime).Seconds()
	speed := float64(0)
	if elapsed > 0 {
		speed = float64(p.bytesDown) / elapsed
	}
	remaining := p.totalBytes - p.bytesDown
	eta := ""
	if speed > 0 && remaining > 0 {
		secs := float64(remaining) / speed
		eta = formatDuration(time.Duration(secs * float64(time.Second)))
	} else if p.piecesDone == p.numPieces {
		eta = "done!"
	}
	fmt.Fprintf(&b, "  Speed: %s/s  Downloaded: %s  ETA: %s\n",
		formatBytes(int64(speed)),
		formatBytes(p.bytesDown),
		eta,
	)
	b.WriteString("\n")

	// Piece map
	b.WriteString("  \033[1mPiece Map\033[0m  \033[32m█\033[0m done  \033[33m▓\033[0m active  \033[90m░\033[0m pending  \033[90m·\033[0m empty\n")

	mapWidth := width - 4 // 2 spaces indent + margin
	if mapWidth < 20 {
		mapWidth = 20
	}
	maxRows := 8
	// If pieces fit in maxRows, show 1:1. Otherwise, compress.
	totalRows := (p.numPieces + mapWidth - 1) / mapWidth
	if totalRows > maxRows {
		// Each cell represents multiple pieces — show dominant state
		totalCells := maxRows * mapWidth
		for cell := range totalCells {
			if cell%mapWidth == 0 {
				b.WriteString("  ")
			}

			// Map cell to piece range
			startPiece := cell * p.numPieces / totalCells
			endPiece := (cell + 1) * p.numPieces / totalCells
			if endPiece > p.numPieces {
				endPiece = p.numPieces
			}

			// Find dominant state
			var counts [4]int
			for i := startPiece; i < endPiece; i++ {
				counts[p.pieces[i]]++
			}

			// Priority: done > active > pending > empty
			ch := pieceChar(dominantState(counts))
			b.WriteString(ch)

			if (cell+1)%mapWidth == 0 {
				b.WriteString("\n")
			}
		}
	} else {
		for i, s := range p.pieces {
			if i%mapWidth == 0 {
				b.WriteString("  ")
			}
			b.WriteString(pieceChar(s))
			if (i+1)%mapWidth == 0 || i == p.numPieces-1 {
				b.WriteString("\n")
			}
		}
	}
	b.WriteString("\n")

	// Peer table
	b.WriteString("  \033[1mPeers\033[0m\n")
	if len(p.peers) == 0 {
		b.WriteString("  (no peers connected)\n")
	}
	for _, ps := range p.peers {
		// Mini bitfield: compress to ~30 chars
		miniWidth := 30
		mini := renderMiniBitfield(ps.Bitfield, p.numPieces, miniWidth)

		fmt.Fprintf(&b, "  %-22s %s  has %d/%d  ↓ %d pcs %d blks %s\n",
			ps.Addr,
			mini,
			ps.HasPieces,
			p.numPieces,
			ps.Pieces,
			ps.Blocks,
			formatBytes(ps.Bytes),
		)
	}

	return b.String()
}

// PrintOver renders and prints, overwriting the previous output.
func (p *Progress) PrintOver(width int) {
	p.mu.Lock()
	lines := p.lastLines
	p.mu.Unlock()

	// Move cursor up to overwrite previous output
	if lines > 0 {
		fmt.Printf("\033[%dA", lines)
	}

	output := p.Render(width)

	// Count lines
	newLines := strings.Count(output, "\n")

	fmt.Print(output)

	// Clear any leftover lines from previous render
	if newLines < lines {
		for range lines - newLines {
			fmt.Print("\033[K\n")
		}
		fmt.Printf("\033[%dA", lines-newLines)
	}

	p.mu.Lock()
	p.lastLines = newLines
	p.mu.Unlock()
}

func pieceChar(s PieceState) string {
	switch s {
	case StateDone:
		return "\033[32m█\033[0m"
	case StateActive:
		return "\033[33m▓\033[0m"
	case StatePending:
		return "\033[90m░\033[0m"
	default:
		return "\033[90m·\033[0m"
	}
}

func dominantState(counts [4]int) PieceState {
	// Priority: done > active > pending > empty
	if counts[StateDone] > 0 {
		return StateDone
	}
	if counts[StateActive] > 0 {
		return StateActive
	}
	if counts[StatePending] > 0 {
		return StatePending
	}
	return StateEmpty
}

func renderMiniBitfield(bitfield []byte, numPieces, width int) string {
	if numPieces == 0 {
		return strings.Repeat("·", width)
	}

	var b strings.Builder
	b.WriteString("\033[36m")
	for col := range width {
		start := col * numPieces / width
		end := (col + 1) * numPieces / width
		if end > numPieces {
			end = numPieces
		}
		has := 0
		total := end - start
		if total == 0 {
			total = 1
		}
		for i := start; i < end; i++ {
			if hasPiece(bitfield, i) {
				has++
			}
		}
		ratio := float64(has) / float64(total)
		switch {
		case ratio >= 0.75:
			b.WriteString("█")
		case ratio >= 0.5:
			b.WriteString("▓")
		case ratio >= 0.25:
			b.WriteString("▒")
		case ratio > 0:
			b.WriteString("░")
		default:
			b.WriteString("\033[90m·\033[36m")
		}
	}
	b.WriteString("\033[0m")
	return b.String()
}

func formatBytes(b int64) string {
	switch {
	case b >= 1<<30:
		return fmt.Sprintf("%.1f GiB", float64(b)/float64(1<<30))
	case b >= 1<<20:
		return fmt.Sprintf("%.1f MiB", float64(b)/float64(1<<20))
	case b >= 1<<10:
		return fmt.Sprintf("%.1f KiB", float64(b)/float64(1<<10))
	default:
		return fmt.Sprintf("%d B", b)
	}
}

func formatDuration(d time.Duration) string {
	if d >= time.Hour {
		return fmt.Sprintf("%dh %dm", int(d.Hours()), int(d.Minutes())%60)
	}
	if d >= time.Minute {
		return fmt.Sprintf("%dm %ds", int(d.Minutes()), int(d.Seconds())%60)
	}
	return fmt.Sprintf("%ds", int(d.Seconds()))
}
