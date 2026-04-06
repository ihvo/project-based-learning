package tracker

import (
	"fmt"
	"math/rand"
	"sync"
)

// TieredAnnouncer implements BEP 12 multi-tracker tier logic.
// It shuffles trackers within each tier, tries tier 0 first, cascades
// to lower tiers on failure, and promotes successful trackers to the
// front of their tier.
type TieredAnnouncer struct {
	mu    sync.Mutex
	tiers [][]string // each tier is a list of tracker URLs
}

// NewTieredAnnouncer creates a tiered announcer from announce-list tiers.
// Each tier is shuffled on creation per BEP 12. If tiers is empty and
// announce is non-empty, a single tier with the announce URL is used.
func NewTieredAnnouncer(tiers [][]string, announce string) *TieredAnnouncer {
	ta := &TieredAnnouncer{}

	if len(tiers) > 0 {
		// Deep copy and shuffle each tier.
		for _, tier := range tiers {
			copied := make([]string, len(tier))
			copy(copied, tier)
			rand.Shuffle(len(copied), func(i, j int) {
				copied[i], copied[j] = copied[j], copied[i]
			})
			if len(copied) > 0 {
				ta.tiers = append(ta.tiers, copied)
			}
		}
	} else if announce != "" {
		ta.tiers = [][]string{{announce}}
	}

	return ta
}

// Announce tries trackers in tier order. Within each tier, tries each
// tracker in order. On success, promotes the tracker to the front of
// its tier and returns the response. On failure of all trackers in a
// tier, cascades to the next tier. Returns the first successful response,
// or an error if all trackers fail.
func (ta *TieredAnnouncer) Announce(params AnnounceParams) (*Response, error) {
	ta.mu.Lock()
	tiers := make([][]string, len(ta.tiers))
	for i, tier := range ta.tiers {
		tiers[i] = make([]string, len(tier))
		copy(tiers[i], tier)
	}
	ta.mu.Unlock()

	var lastErr error

	for tierIdx, tier := range tiers {
		for trackerIdx, url := range tier {
			resp, err := Announce(url, params)
			if err != nil {
				lastErr = fmt.Errorf("tier %d tracker %s: %w", tierIdx, url, err)
				continue
			}

			// Success — promote this tracker to front of its tier.
			ta.mu.Lock()
			ta.promote(tierIdx, trackerIdx)
			ta.mu.Unlock()

			return resp, nil
		}
	}

	if lastErr != nil {
		return nil, fmt.Errorf("all trackers failed, last: %w", lastErr)
	}
	return nil, fmt.Errorf("no trackers available")
}

// promote moves the tracker at tierIdx/trackerIdx to position 0 in its tier.
func (ta *TieredAnnouncer) promote(tierIdx, trackerIdx int) {
	if tierIdx >= len(ta.tiers) || trackerIdx <= 0 {
		return
	}
	tier := ta.tiers[tierIdx]
	if trackerIdx >= len(tier) {
		return
	}
	url := tier[trackerIdx]
	copy(tier[1:trackerIdx+1], tier[:trackerIdx])
	tier[0] = url
}

// Tiers returns the current tier state (for testing/debugging).
func (ta *TieredAnnouncer) Tiers() [][]string {
	ta.mu.Lock()
	defer ta.mu.Unlock()
	result := make([][]string, len(ta.tiers))
	for i, tier := range ta.tiers {
		result[i] = make([]string, len(tier))
		copy(result[i], tier)
	}
	return result
}
