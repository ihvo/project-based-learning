package download

import (
	"crypto/sha1"
	"fmt"
	"time"

	"github.com/ihvo/peer-pressure/peer"
	"github.com/ihvo/peer-pressure/torrent"
)

// pipelinedDownload keeps multiple pieces' requests in flight simultaneously
// so the TCP pipe stays full across piece boundaries.
//
// Architecture: two goroutines sharing one peer.Conn (safe — TCP is full-duplex,
// bufio.Reader and bufio.Writer operate on independent halves).
//
//	Sender goroutine: picks pieces → sends block requests → registers via jobCh
//	Reader (caller):  reads responses → assembles pieces → verifies hashes
//
// The jobCh buffer size equals maxActive, so the sender naturally blocks when
// that many pieces are in flight — bounding memory without explicit counting.
func pipelinedDownload(conn *peer.Conn, addr string, t *torrent.Torrent,
	picker *Picker, bitfield []byte, results chan<- pieceResult,
	prog *Progress, onBlock BlockCallback, maxActive int) int {

	type pieceJob struct {
		index, numBlocks int
		buf              []byte
		hash             [20]byte
		received         int
	}

	jobCh := make(chan pieceJob, maxActive)

	// Sender: picks pieces, sends all block requests per piece, then queues
	// the job for the reader. Closes jobCh when no more pieces are available
	// or on write error.
	go func() {
		defer close(jobCh)
		for {
			idx, ok := picker.Pick(bitfield)
			if !ok {
				return
			}

			pl := t.PieceLen(idx)
			nb := BlockCount(pl)
			if prog != nil {
				prog.PieceStarted(idx)
			}

			// Send all block requests for this piece (whole-piece burst).
			for i := range nb {
				begin := i * BlockSize
				length := BlockSize
				if begin+length > pl {
					length = pl - begin
				}
				if conn.WriteMessage(peer.NewRequest(uint32(idx), uint32(begin), uint32(length))) != nil {
					picker.Abort(idx)
					return
				}
			}
			if conn.Flush() != nil {
				picker.Abort(idx)
				return
			}

			jobCh <- pieceJob{
				index:    idx,
				numBlocks: nb,
				buf:      make([]byte, pl),
				hash:     t.Pieces[idx],
			}
		}
	}()

	// Reader state.
	active := make(map[int]*pieceJob, maxActive)
	pending := 0 // total blocks expected across all active pieces
	downloaded := 0
	senderDone := false

	// abortAll returns every reserved-but-unfinished piece to the picker.
	// Must be called on every error exit path.
	abortAll := func() {
		for _, j := range active {
			picker.Abort(j.index)
		}
		// Drain any jobs the sender queued after we stopped reading.
		for j := range jobCh {
			picker.Abort(j.index)
		}
	}

	// register adds a job to the active set.
	register := func(j pieceJob) {
		jj := j // copy so pointer is stable
		active[j.index] = &jj
		pending += j.numBlocks
	}

	for {
		// Ensure at least one active piece. Block if the sender hasn't
		// provided work yet (it's picking + sending requests concurrently).
		if len(active) == 0 {
			j, ok := <-jobCh
			if !ok {
				break // sender done, all pieces downloaded or unavailable
			}
			register(j)
		}

		// Non-blocking drain: accept any additional jobs the sender queued
		// while we were reading blocks.
	drain:
		for !senderDone {
			select {
			case j, ok := <-jobCh:
				if !ok {
					senderDone = true
					break drain
				}
				register(j)
			default:
				break drain
			}
		}

		// Read one message from the peer.
		conn.SetDeadline(time.Now().Add(30 * time.Second))
		msg, err := conn.ReadMessage()
		if err != nil {
			abortAll()
			return downloaded
		}
		if msg == nil {
			continue // keep-alive
		}

		switch msg.ID {
		case peer.MsgPiece:
			pp, err := peer.ParsePiece(msg.Payload)
			if err != nil {
				abortAll()
				return downloaded
			}

			j := active[int(pp.Index)]
			if j == nil {
				continue // block for unknown piece, ignore
			}
			if int(pp.Begin)+len(pp.Block) > len(j.buf) {
				continue // overflow, ignore
			}

			copy(j.buf[pp.Begin:], pp.Block)
			j.received++
			pending--

			if onBlock != nil {
				onBlock(j.index, int(pp.Begin), len(pp.Block))
			}

			// Piece complete?
			if j.received == j.numBlocks {
				actualHash := sha1.Sum(j.buf)
				if actualHash != j.hash {
					picker.Abort(j.index)
					results <- pieceResult{
						index: j.index,
						err:   fmt.Errorf("piece %d hash mismatch", j.index),
					}
					if prog != nil {
						prog.PieceFailed(j.index)
					}
				} else {
					picker.Finish(j.index)
					results <- pieceResult{
						index:    j.index,
						data:     j.buf,
						fromAddr: addr,
					}
					if prog != nil {
						prog.PieceDone(j.index, addr)
					}
					downloaded++
				}
				delete(active, j.index)
			}

		case peer.MsgChoke:
			abortAll()
			return downloaded
		}
	}

	conn.SetDeadline(time.Time{})
	return downloaded
}
