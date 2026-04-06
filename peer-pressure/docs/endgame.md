# Endgame Mode

> Prevents a single slow peer from bottlenecking the final seconds of a
> download by sending duplicate block requests to all available peers.

---

## 1. Summary

Endgame mode is a download optimisation that activates when the only pieces
remaining are already in-flight — there are no idle pieces left to request.
At this point, the client sends duplicate requests for every outstanding block
to every connected peer that has the piece, then cancels the duplicates as
blocks arrive.

Without endgame mode, if the last 3 pieces are each assigned to one peer and
one of those peers is slow, the download stalls waiting for that single slow
connection. Endgame mode breaks this bottleneck by racing multiple peers
against each other for the same blocks.

Properties:

- **Trigger**: no idle (unrequested, unfinished) pieces remain — every
  remaining piece is already assigned to at least one peer.
- **Behavior**: broadcast every outstanding block request to all peers that
  have the piece.
- **Cancellation**: when a block arrives from any peer, immediately send
  Cancel messages to all other peers that were asked for the same block.
- **Scope**: only active during the very tail of the download — typically the
  last few pieces.
- **Trade-off**: briefly increases upload bandwidth (peers read from disk to
  serve requests that will be cancelled), but the duration is short.

---

## 2. Design

### 2.1 When to enter endgame mode

Endgame mode activates when:

```
remaining_pieces = numPieces - done_pieces - failed_pieces
inflight_pieces  = count of pieces currently being downloaded

remaining_pieces > 0 AND remaining_pieces == inflight_pieces
```

In other words: every remaining piece has a worker actively downloading it,
and there are no pieces left to pick. The `Picker` detects this condition
because `Pick()` returns `false` (nothing to pick) but `Done()` also returns
`false` (not all pieces are verified).

### 2.2 Block-level tracking

Endgame mode works at the **block** level (16 KiB), not the piece level. A
piece is composed of multiple blocks:

```
blocks_per_piece = ceil(piece_length / 16384)
```

The picker must track which individual blocks are outstanding:

```go
type pendingBlock struct {
    Piece  int
    Begin  int  // byte offset within piece
    Length int  // block size (usually 16384, smaller for last block)
}
```

When entering endgame mode, the picker builds the list of all blocks that have
been requested but not yet received.

### 2.3 Duplicate request broadcasting

On entering endgame mode:

```
for each outstanding block B:
    for each connected peer P:
        if P has the piece containing B AND P is unchoked AND P != original requester:
            send Request(B.Piece, B.Begin, B.Length) to P
            record (B, P) in endgame tracking map
```

For new block requests issued during endgame (e.g. from a retry), broadcast
immediately to all eligible peers.

### 2.4 Cancel on receipt

When a block arrives from any peer:

```
for each peer P that was sent a duplicate request for this block:
    if P != the peer that delivered the block:
        send Cancel(piece, begin, length) to P
        remove (block, P) from tracking map
```

This limits wasted bandwidth — peers that receive Cancel should drop the
request from their send queue.

### 2.5 State machine

```
NORMAL ──────────────────────── ENDGAME
  │                                │
  │  Pick() returns false          │  Block received → cancel duplicates
  │  AND Done() is false           │  
  │  ──────────────────►           │  All pieces done?
  │                                │  ──────────────────► COMPLETE
  │                                │
  │  A piece fails verification    │
  │  (new idle piece available)    │
  │  ◄──────────────────           │
  │                                │
```

If a piece fails hash verification during endgame, its blocks go back to the
idle pool, and the picker may exit endgame mode (since there's now an idle
piece to pick normally).

### 2.6 Interaction with the pool

The existing `peerPool` in `download/pool.go` manages worker goroutines that
each download pieces from a single peer. Endgame mode changes the pool's
behavior:

- **Normal mode**: each worker asks the `Picker` for a piece, downloads all
  its blocks from one peer, verifies the hash, and reports the result.
- **Endgame mode**: when the picker signals endgame, the pool broadcasts
  outstanding block requests to all connected workers that have the relevant
  pieces.

The pool needs a channel or callback to distribute endgame requests to
workers, and workers need to handle Cancel messages they receive.

### 2.7 Bandwidth impact

Endgame mode is only active for the tail of the download. For a torrent with
1000 pieces, it might activate when 3-5 pieces remain. The duplicate request
overhead is:

```
extra_requests = remaining_blocks * (eligible_peers - 1)
```

For 3 remaining pieces of 256 KiB (16 blocks each) with 10 eligible peers:
`48 * 9 = 432` extra requests. Most will be cancelled quickly. The actual
extra data transferred is minimal since Cancel arrives before the data.

---

## 3. Implementation Plan

### 3.1 Package placement

Endgame mode is integrated into the existing `download/` package by modifying
`picker.go` and `pool.go`.

### 3.2 Modified files

| File | Changes |
|------|---------|
| `download/picker.go` | Add endgame detection, block-level tracking, duplicate request list, cancel tracking |
| `download/pool.go` | Broadcast requests in endgame, send cancels on block receipt |
| `download/pipeline.go` | Handle incoming Cancel messages; report block-level progress back to picker |
| `download/session.go` | No structural changes — endgame is transparent to the session |

### 3.3 New file

| File | Purpose |
|------|---------|
| `download/endgame.go` | Endgame state: pending blocks, duplicate tracking, cancel generation |
| `download/endgame_test.go` | Tests for endgame logic |

### 3.4 Key types

```go
// download/endgame.go

// block identifies a single 16 KiB block within a piece.
type block struct {
    Piece  int
    Begin  int
    Length int
}

// endgame tracks the state of endgame mode.
type endgame struct {
    mu        sync.Mutex
    active    bool
    pending   map[block]map[string]struct{} // block → set of peer addrs that have been asked
}
```

### 3.5 Changes to Picker

```go
// download/picker.go — new fields on Picker

type Picker struct {
    // ... existing fields ...
    endgame  *endgame        // nil until endgame activates
}

// New methods:

// InEndgame returns true if endgame mode is active.
func (p *Picker) InEndgame() bool

// EnterEndgame activates endgame mode and returns the list of all
// outstanding blocks that should be broadcast to additional peers.
func (p *Picker) EnterEndgame(inflightBlocks []block) []block

// BlockReceived records that a block was received from a peer.
// Returns the list of (block, peer) pairs to send Cancel messages to.
func (p *Picker) BlockReceived(b block, fromAddr string) []cancelTarget

// ExitEndgame deactivates endgame mode (e.g. when a piece fails verification
// and goes back to idle).
func (p *Picker) ExitEndgame()

// cancelTarget identifies a Cancel message to send.
type cancelTarget struct {
    Block block
    Addr  string
}
```

### 3.6 Changes to Pool

```go
// download/pool.go — new behavior

// In the pool's main loop, after each piece result:
//
// 1. Check if picker.InEndgame() should activate:
//    if !picker.InEndgame() && picker cannot pick any more pieces && !picker.Done():
//        collect all in-flight blocks from active workers
//        picker.EnterEndgame(inflightBlocks)
//        broadcast duplicate requests to eligible workers
//
// 2. When a block arrives during endgame:
//    cancels := picker.BlockReceived(block, fromAddr)
//    for each cancel:
//        send Cancel message to cancel.Addr
//
// 3. When a piece fails verification during endgame:
//    picker.ExitEndgame()  // may re-enter on next evaluation
```

### 3.7 Changes to Pipeline

```go
// download/pipeline.go

// The reader goroutine needs to:
// 1. Report each received block (not just completed pieces) back to the pool
//    so the pool can trigger cancel broadcasts during endgame.
//
// 2. Handle Cancel messages received from the remote peer:
//    - Remove the cancelled block from our outgoing request queue
//    - (We send Cancels to others; we also receive Cancels from others
//      who are also in endgame mode)
```

---

## 4. Dependencies

| Dependency | Type | Notes |
|------------|------|-------|
| `download/picker.go` | Modified | Core endgame detection and block tracking |
| `download/pool.go` | Modified | Broadcast and cancel distribution |
| `download/pipeline.go` | Modified | Block-level reporting, Cancel handling |
| `download/piece.go` | Reference | `BlockSize` constant (16384), `BlockCount` function |
| `peer/message.go` | Required | `NewCancel()`, `NewRequest()`, `ParsePiece()` |

No new external dependencies.

---

## 5. Testing Strategy

### 5.1 Endgame detection tests (`download/endgame_test.go`)

| Test | Description |
|------|-------------|
| `TestEndgameNotTriggeredEarly` | 10 pieces, 3 done, 2 in-flight, 5 idle. `InEndgame()` returns false (idle pieces remain). |
| `TestEndgameTriggered` | 10 pieces, 7 done, 3 in-flight, 0 idle. `InEndgame()` returns true. |
| `TestEndgameNotTriggeredWhenDone` | All pieces done. `InEndgame()` returns false (download is complete, not in endgame). |
| `TestEndgameExitOnFailure` | Enter endgame with 2 in-flight pieces. One fails verification (goes back to idle). Verify `InEndgame()` returns false. |

### 5.2 Block tracking tests

| Test | Description |
|------|-------------|
| `TestEnterEndgameReturnsBlocks` | 2 in-flight pieces (piece 8: 16 blocks, piece 9: 10 blocks). `EnterEndgame` returns all 26 blocks. |
| `TestBlockReceivedReturnsCancels` | Block was sent to peers A, B, C. Block arrives from A. `BlockReceived` returns cancels for B and C. |
| `TestBlockReceivedNoCancelsForSolePeer` | Block was only sent to peer A. Block arrives from A. `BlockReceived` returns empty cancel list. |
| `TestBlockReceivedUnknownBlock` | Block was never tracked (edge case). `BlockReceived` returns empty cancel list, no panic. |
| `TestAllBlocksReceivedClearsPiece` | Receive all blocks of a piece. Verify the piece's blocks are removed from the pending map. |

### 5.3 Picker integration tests (`download/picker_test.go` additions)

| Test | Description |
|------|-------------|
| `TestPickerEndgameTransition` | Create picker with 5 pieces. Complete 3, set 2 in-flight. Call Pick() — returns false. Verify `InEndgame()` activates after `EnterEndgame`. |
| `TestPickerEndgamePickStillWorks` | In endgame, if a piece fails and goes idle, `Pick()` should return it and `ExitEndgame()` should be callable. |
| `TestPickerEndgameConcurrency` | 10 goroutines calling `BlockReceived` concurrently on different blocks. Verify no races (`go test -race`). |

### 5.4 Pool integration tests (`download/pool.go` test additions)

| Test | Description |
|------|-------------|
| `TestPoolEndgameBroadcast` | Mock 3 peers connected. Enter endgame with 2 outstanding blocks. Verify all 3 peers receive Request messages for both blocks. |
| `TestPoolEndgameCancelOnReceipt` | 3 peers in endgame. Peer A delivers a block. Verify Cancel sent to peers B and C for that block. |

### 5.5 Full integration test

| Test | Description |
|------|-------------|
| `TestEndgameFullDownload` | Start 3 mock seeders. One is artificially slow (100ms delay per block). Download a small torrent (5 pieces). Verify download completes in less time than the slow peer would take alone (endgame kicks in and the fast peers cover the slow one). |
