package seed

import (
	"context"
	"math/rand"
	"sort"
	"time"

	"github.com/ihvo/peer-pressure/peer"
)

// Choker implements the tit-for-tat choking algorithm.
// Every evalInterval it ranks interested peers by upload speed and unchokes
// the top N. Every optimisticInterval it rotates one random optimistic
// unchoke slot.
type Choker struct {
	uploadSlots int
	optimistic  string // addr of the optimistically unchoked peer
}

// NewChoker creates a choker with the given number of regular unchoke slots.
func NewChoker(uploadSlots int) *Choker {
	if uploadSlots <= 0 {
		uploadSlots = 4
	}
	return &Choker{uploadSlots: uploadSlots}
}

// Run starts the periodic evaluation loop. Blocks until ctx is cancelled.
func (c *Choker) Run(ctx context.Context, getConns func() []*uploadConn) {
	evalTick := time.NewTicker(10 * time.Second)
	defer evalTick.Stop()
	optimisticTick := time.NewTicker(30 * time.Second)
	defer optimisticTick.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-evalTick.C:
			c.evaluate(getConns(), false)
		case <-optimisticTick.C:
			c.evaluate(getConns(), true)
		}
	}
}

func (c *Choker) evaluate(conns []*uploadConn, rotateOptimistic bool) {
	// Filter to interested peers.
	var interested []*uploadConn
	for _, uc := range conns {
		if uc.interested {
			interested = append(interested, uc)
		}
	}

	// Rank by upload bytes (higher = faster uploading to them).
	sort.Slice(interested, func(i, j int) bool {
		return interested[i].uploadBytes.Load() > interested[j].uploadBytes.Load()
	})

	// Pick the optimistic unchoke candidate.
	if rotateOptimistic && len(interested) > c.uploadSlots {
		candidates := interested[c.uploadSlots:]
		c.optimistic = candidates[rand.Intn(len(candidates))].addr
	}

	// Build unchoke set: top N + optimistic.
	unchokeSet := make(map[string]bool, c.uploadSlots+1)
	for i := range min(c.uploadSlots, len(interested)) {
		unchokeSet[interested[i].addr] = true
	}
	if c.optimistic != "" {
		unchokeSet[c.optimistic] = true
	}

	// Apply choke/unchoke decisions.
	for _, uc := range conns {
		shouldUnchoke := unchokeSet[uc.addr]
		if shouldUnchoke && uc.choked {
			uc.choked = false
			if uc.conn != nil {
				_ = uc.conn.WriteMessage(&peer.Message{ID: peer.MsgUnchoke})
				_ = uc.conn.Flush()
			}
		} else if !shouldUnchoke && !uc.choked {
			uc.choked = true
			if uc.conn != nil {
				_ = uc.conn.WriteMessage(&peer.Message{ID: peer.MsgChoke})
				_ = uc.conn.Flush()
			}
		}
	}
}
