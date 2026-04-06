# BEP 12 — Multitracker Metadata Extension (Tier Logic)

> **Specification:** <https://www.bittorrent.org/beps/bep_0012.html>
> **Status:** Partially implemented (parsing only, no tier logic)
> **Phase:** 4 — Peer Discovery

---

## 1. Summary

BEP 12 defines `announce-list`, a list-of-lists structure in the .torrent
metainfo file that allows a torrent to specify multiple trackers organized
into priority tiers.

**What we already have:** The `torrent` package parses `announce-list` into
`Torrent.AnnounceList [][]string`. The `Trackers()` method flattens all tiers
into a deduplicated list. The CLI announces to whichever tracker responds
first.

**What's missing:** The actual tier logic — the algorithm that determines the
order in which trackers are tried, how tiers cascade on failure, and how
successful trackers are promoted within their tier. Without this, we're not
BEP 12 compliant: we treat all trackers as equal rather than respecting the
priority ordering the torrent creator intended.

This matters because:
- Tier 1 trackers are typically the torrent creator's preferred/private trackers
- Falling back to tier 2+ should only happen when all tier 1 trackers fail
- Within a tier, shuffling and promotion avoids thundering-herd effects on any
  single tracker

---

## 2. Protocol Specification

### 2.1 Data Structure

The `announce-list` key in the .torrent metainfo is a list of lists of strings:

```
announce-list = [
  [tier1_tracker_a, tier1_tracker_b, tier1_tracker_c],  ← tier 0 (highest priority)
  [tier2_tracker_a],                                     ← tier 1
  [tier3_tracker_a, tier3_tracker_b],                    ← tier 2 (lowest priority)
]
```

Each inner list is a **tier**. Tiers are ordered by priority (index 0 is
highest). Trackers within a tier are considered equivalent in quality.

### 2.2 Algorithm

**Initial shuffle:** Before the first announce, each tier's tracker list is
shuffled randomly. This distributes load across trackers within a tier.

```
Before shuffle: [["http://a", "http://b", "http://c"], ["udp://d"]]
After shuffle:  [["http://c", "http://a", "http://b"], ["udp://d"]]
```

**Announce procedure:**

```
for each tier (in order, starting with tier 0):
    for each tracker in the tier (in current order):
        attempt announce
        if success:
            move this tracker to the front of its tier
            STOP — announce is complete
        if failure:
            continue to next tracker in tier
    if all trackers in tier failed:
        continue to next tier
if all tiers exhausted:
    announce failed entirely
```

**Key rules:**
1. Try tiers in order (0, 1, 2, ...)
2. Within a tier, try trackers in their current order
3. On success: move the successful tracker to index 0 of its tier, then stop
4. On failure: try the next tracker in the tier
5. If an entire tier fails, fall through to the next tier
6. On subsequent announces, the previously-successful tracker is tried first
   (because it was moved to the front)

### 2.3 `announce` vs `announce-list`

When `announce-list` is present in the metainfo:
- The standalone `announce` key is **ignored** (BEP 12 says `announce-list`
  takes priority)
- Some clients treat `announce` as a fallback if `announce-list` is present
  but all trackers fail — we follow this practice as a robustness measure

Our existing `Torrent.Trackers()` already handles this: it iterates
`AnnounceList` first, then falls back to `Announce`.

### 2.4 State Machine

```
┌──────────────────────────────────────────────────────┐
│                  Announce Cycle                       │
├──────────────────────────────────────────────────────┤
│                                                      │
│  ┌─── Tier 0 ────────────────────────────────────┐   │
│  │ tracker[0] ──fail──► tracker[1] ──fail──► ... │   │
│  │     │success                                  │   │
│  │     ▼                                         │   │
│  │  Promote to front. DONE.                      │   │
│  └──────────────────────────┬──all fail───────────┘   │
│                             ▼                        │
│  ┌─── Tier 1 ────────────────────────────────────┐   │
│  │ tracker[0] ──fail──► tracker[1] ──fail──► ... │   │
│  │     │success                                  │   │
│  │     ▼                                         │   │
│  │  Promote to front. DONE.                      │   │
│  └──────────────────────────┬──all fail───────────┘   │
│                             ▼                        │
│                          (etc.)                      │
│                             │                        │
│                      all tiers failed                │
│                             │                        │
│                      ┌──────▼──────┐                 │
│                      │  Fallback:  │                 │
│                      │  announce   │                 │
│                      │  key        │                 │
│                      └─────────────┘                 │
│                                                      │
└──────────────────────────────────────────────────────┘
```

### 2.5 Timing

BEP 12 does not change announce timing. The tracker's `interval` field from the
most recent successful response still governs when the next announce happens.
If different trackers return different intervals, use the interval from the
tracker that last succeeded.

---

## 3. Implementation Plan

### 3.1 `tracker/tiered.go` — New File

This is the core new file. It manages the tier state machine:

```go
package tracker

import (
    "math/rand"
    "sync"
)

// TieredAnnouncer manages multi-tracker tier logic per BEP 12.
type TieredAnnouncer struct {
    mu    sync.Mutex
    tiers [][]string // tiers[i] is a shuffled list of tracker URLs

    // LastSuccessful tracks the URL and tier of the last successful announce.
    lastURL  string
    lastTier int
}

// NewTieredAnnouncer creates an announcer from the announce-list tiers.
// Each tier is shuffled randomly on creation.
func NewTieredAnnouncer(announceLists [][]string, fallback string) *TieredAnnouncer {
    ta := &TieredAnnouncer{
        tiers:    make([][]string, len(announceLists)),
        lastTier: -1,
    }

    // Deep copy and shuffle each tier.
    for i, tier := range announceLists {
        shuffled := make([]string, len(tier))
        copy(shuffled, tier)
        rand.Shuffle(len(shuffled), func(a, b int) {
            shuffled[a], shuffled[b] = shuffled[b], shuffled[a]
        })
        ta.tiers[i] = shuffled
    }

    // Add fallback announce URL as a last-resort tier if not already present.
    if fallback != "" && !ta.containsURL(fallback) {
        ta.tiers = append(ta.tiers, []string{fallback})
    }

    return ta
}

// Announce tries trackers in tier order, promoting the successful one.
// Returns the first successful response, or the last error if all fail.
func (ta *TieredAnnouncer) Announce(params AnnounceParams) (*Response, error) {
    ta.mu.Lock()
    tiers := ta.tiersCopy()
    ta.mu.Unlock()

    var lastErr error
    for tierIdx, tier := range tiers {
        for trackerIdx, url := range tier {
            resp, err := Announce(url, params)
            if err != nil {
                lastErr = err
                continue
            }
            // Success — promote this tracker to front of its tier.
            ta.mu.Lock()
            ta.promote(tierIdx, trackerIdx)
            ta.lastURL = url
            ta.lastTier = tierIdx
            ta.mu.Unlock()
            return resp, nil
        }
    }
    return nil, fmt.Errorf("all trackers failed: %w", lastErr)
}

// promote moves the tracker at index pos to the front of its tier.
func (ta *TieredAnnouncer) promote(tierIdx, pos int) {
    if pos == 0 {
        return
    }
    tier := ta.tiers[tierIdx]
    url := tier[pos]
    copy(tier[1:pos+1], tier[:pos])
    tier[0] = url
}

// tiersCopy returns a snapshot of the tiers (for iteration without holding lock).
func (ta *TieredAnnouncer) tiersCopy() [][]string {
    cp := make([][]string, len(ta.tiers))
    for i, tier := range ta.tiers {
        t := make([]string, len(tier))
        copy(t, tier)
        cp[i] = t
    }
    return cp
}

// containsURL reports whether any tier contains the given URL.
func (ta *TieredAnnouncer) containsURL(url string) bool {
    for _, tier := range ta.tiers {
        for _, u := range tier {
            if u == url {
                return true
            }
        }
    }
    return false
}

// Interval returns the announce interval from the last successful response,
// or a default of 1800 seconds if no successful announce has occurred.
func (ta *TieredAnnouncer) Interval() int {
    ta.mu.Lock()
    defer ta.mu.Unlock()
    return ta.lastInterval
}
```

### 3.2 `tracker/tracker.go` — No Changes to `Announce`

The existing `Announce(trackerURL string, params AnnounceParams)` function
stays as-is — it handles a single tracker URL (dispatching to HTTP or UDP).
`TieredAnnouncer.Announce` calls it in a loop.

### 3.3 `download/session.go` — Use `TieredAnnouncer`

Replace the current flat tracker iteration with `TieredAnnouncer`. In the
`Config` struct, replace the flat `Peers` field population logic:

```go
// Before download starts:
var announcer *tracker.TieredAnnouncer
if len(t.AnnounceList) > 0 {
    announcer = tracker.NewTieredAnnouncer(t.AnnounceList, t.Announce)
} else if t.Announce != "" {
    announcer = tracker.NewTieredAnnouncer([][]string{{t.Announce}}, "")
}
```

### 3.4 `cmd/peer-pressure/main.go` — Use `TieredAnnouncer`

In `runPeers()` and `runDownload()`, replace the direct `tracker.Announce`
call with `TieredAnnouncer`:

```go
// runPeers:
ta := tracker.NewTieredAnnouncer(t.AnnounceList, t.Announce)
resp, err := ta.Announce(tracker.AnnounceParams{
    InfoHash:   t.InfoHash,
    PeerID:     peerID,
    Port:       6881,
    Left:       int64(t.TotalLength()),
})
```

### 3.5 `torrent/torrent.go` — Deprecate `Trackers()`

The existing `Trackers()` method flattens all tiers into a deduplicated list,
losing tier information. It can remain for backward compatibility but should
be considered deprecated in favor of using `AnnounceList` directly with
`TieredAnnouncer`.

Add a doc comment noting this:

```go
// Trackers returns a flat, deduplicated list of all tracker URLs.
// For BEP 12 tier-aware announcing, use AnnounceList with
// tracker.NewTieredAnnouncer instead.
func (t *Torrent) Trackers() []string {
```

### 3.6 File Summary

| File                          | Change       | Description                                      |
|-------------------------------|--------------|--------------------------------------------------|
| `tracker/tiered.go`          | Create       | `TieredAnnouncer` with shuffle, cascade, promote |
| `cmd/peer-pressure/main.go`  | Modify       | Use `TieredAnnouncer` in `runPeers`/`runDownload` |
| `download/session.go`        | Modify       | Integrate `TieredAnnouncer` for peer discovery   |
| `torrent/torrent.go`         | Modify       | Add deprecation note to `Trackers()`             |

---

## 4. Dependencies

| BEP | Relationship | Notes |
|-----|-------------|-------|
| 3   | Requires    | `announce-list` extends the metainfo format defined by BEP 3 |
| 15  | Interacts   | UDP trackers are tried the same way as HTTP trackers — `Announce()` dispatches by URL scheme |
| 27  | Interacts   | Private torrents still use multi-tracker tiers (tracker is the only peer source) |

---

## 5. Testing Strategy

### 5.1 `tracker/tiered_test.go` — Shuffle

| Test Case | Description |
|-----------|-------------|
| `TestNewTieredAnnouncerShuffles` | Create a `TieredAnnouncer` with a 10-element tier. Run 100 times. Verify the order is not always the same (statistical test: at least 2 unique orderings seen). |
| `TestNewTieredAnnouncerPreservesAll` | After shuffling, verify no URLs are lost or duplicated. |
| `TestNewTieredAnnouncerFallback` | Create with `fallback="http://fallback"` not in any tier. Verify it appears as the last tier. |
| `TestNewTieredAnnouncerFallbackDuplicate` | Create with `fallback` already in tier 0. Verify no extra tier is added. |

### 5.2 `tracker/tiered_test.go` — Tier Cascade

These tests use a mock `Announce` function (swap the package-level function or
use an interface).

| Test Case | Description |
|-----------|-------------|
| `TestAnnounceFirstTierSuccess` | Tier 0 has 3 trackers, tracker[1] succeeds. Verify we get a response and tracker[1] is not tried before tracker[0]. |
| `TestAnnounceTierFallthrough` | Tier 0 all fail, tier 1 tracker[0] succeeds. Verify tier 1 is tried and returns success. |
| `TestAnnounceAllFail` | All tiers, all trackers fail. Verify error is returned wrapping the last failure. |

### 5.3 `tracker/tiered_test.go` — Promotion

| Test Case | Description |
|-----------|-------------|
| `TestPromoteMovesToFront` | Tier has `[A, B, C]`, tracker B succeeds. Verify tier becomes `[B, A, C]`. |
| `TestPromoteAlreadyFront` | Tier has `[A, B, C]`, tracker A succeeds. Verify tier stays `[A, B, C]`. |
| `TestPromoteSubsequentAnnounce` | After B is promoted to front, do another announce. Verify B is tried first. If B succeeds again, tier order is unchanged. If B fails and C succeeds, C moves to front. |

### 5.4 `tracker/tiered_test.go` — Concurrency

| Test Case | Description |
|-----------|-------------|
| `TestAnnounceConcurrent` | Launch 10 goroutines calling `Announce` simultaneously. Verify no data races (run with `-race`). |

### 5.5 `tracker/tiered_test.go` — Edge Cases

| Test Case | Description |
|-----------|-------------|
| `TestSingleTierSingleTracker` | One tier with one tracker. Verify it's tried and success/failure is returned. |
| `TestEmptyTiers` | `announce-list` is `[[], ["http://a"]]`. Verify empty tier is skipped, tier 1 is tried. |
| `TestNoAnnounceList` | `AnnounceList` is nil, `Announce` is set. Verify fallback creates a single tier. |

### 5.6 Integration

| Test Case | Description |
|-----------|-------------|
| `TestTieredAnnouncerWithHTTPMock` | Start two `httptest.Server` instances as mock trackers. Put them in different tiers. Kill tier 0's server. Verify tier 1 is used. |
| `TestDownloadWithTieredTracker` | End-to-end: parse a .torrent with multi-tier announce-list, run `download.File` with mock trackers. Verify the session uses tiered announcing. |
