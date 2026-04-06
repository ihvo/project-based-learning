package download

import (
	"context"
	"crypto/sha1"
	"fmt"
	"io"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/ihvo/peer-pressure/torrent"
)

// webseedWorker downloads pieces from an HTTP seed (BEP 19).
// It uses HTTP Range requests to fetch exactly the bytes for each piece,
// verifies the SHA-1 hash, and sends results through the same channel
// as peer workers.
type webseedWorker struct {
	url     string
	torrent *torrent.Torrent
	picker  *Picker
	results chan<- pieceResult
	prog    *Progress
	client  *http.Client
	bytes   atomic.Int64 // total bytes downloaded, for speed tracking
}

func newWebseedWorker(url string, t *torrent.Torrent, picker *Picker,
	results chan<- pieceResult, prog *Progress) *webseedWorker {
	return &webseedWorker{
		url:     url,
		torrent: t,
		picker:  picker,
		results: results,
		prog:    prog,
		client: &http.Client{
			Timeout: 60 * time.Second,
		},
	}
}

// run picks and downloads pieces until ctx is canceled or no pieces remain.
func (w *webseedWorker) run(ctx context.Context) {
	// WebSeed has all pieces.
	bitfield := makeFullBitfield(len(w.torrent.Pieces))

	w.picker.AddPeer(bitfield)
	defer w.picker.RemovePeer(bitfield)

	if w.prog != nil {
		w.prog.PeerConnected(w.url, bitfield)
		defer w.prog.PeerDisconnected(w.url)
	}

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		idx, ok := w.picker.Pick(bitfield)
		if !ok {
			return
		}

		if w.prog != nil {
			w.prog.PieceStarted(idx)
		}

		data, err := w.fetchPiece(ctx, idx)
		if err != nil {
			w.picker.Abort(idx)
			w.results <- pieceResult{index: idx, err: fmt.Errorf("webseed %s: %w", w.url, err)}
			if w.prog != nil {
				w.prog.PieceFailed(idx)
			}
			// Back off on error to avoid hammering the server.
			select {
			case <-ctx.Done():
				return
			case <-time.After(2 * time.Second):
			}
			continue
		}

		// Verify hash.
		hash := sha1.Sum(data)
		if hash != w.torrent.Pieces[idx] {
			w.picker.Abort(idx)
			w.results <- pieceResult{
				index: idx,
				err:   fmt.Errorf("webseed piece %d hash mismatch", idx),
			}
			if w.prog != nil {
				w.prog.PieceFailed(idx)
			}
			continue
		}

		w.picker.Finish(idx)
		w.results <- pieceResult{index: idx, data: data, fromAddr: w.url}
		if w.prog != nil {
			w.prog.PieceDone(idx, w.url)
		}
	}
}

// fetchPiece downloads exactly one piece via HTTP Range request.
func (w *webseedWorker) fetchPiece(ctx context.Context, idx int) ([]byte, error) {
	pieceLen := w.torrent.PieceLen(idx)
	start := int64(idx) * int64(w.torrent.PieceLength)
	end := start + int64(pieceLen) - 1

	req, err := http.NewRequestWithContext(ctx, "GET", w.url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", start, end))

	resp, err := w.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusPartialContent && resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read body: %w", err)
	}
	if len(data) != pieceLen {
		return nil, fmt.Errorf("short read: got %d, want %d", len(data), pieceLen)
	}

	w.bytes.Add(int64(len(data)))
	if w.prog != nil {
		w.prog.BlockReceived(w.url, len(data))
	}
	return data, nil
}
