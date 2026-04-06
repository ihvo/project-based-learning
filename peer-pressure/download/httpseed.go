package download

import (
	"context"
	"crypto/sha1"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/ihvo/peer-pressure/torrent"
)

// httpseedWorker downloads pieces from a BEP 17 HTTP seed (Hoffman style).
// Unlike BEP 19 (Range requests on the raw file URL), BEP 17 uses a script URL
// with query parameters: ?info_hash=<hash>&piece=<N>&ranges=<start>-<end>
type httpseedWorker struct {
	url     string
	torrent *torrent.Torrent
	picker  *Picker
	results chan<- pieceResult
	prog    *Progress
	client  *http.Client
	bytes   atomic.Int64
}

func newHTTPSeedWorker(seedURL string, t *torrent.Torrent, picker *Picker,
	results chan<- pieceResult, prog *Progress) *httpseedWorker {
	return &httpseedWorker{
		url:     seedURL,
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
func (w *httpseedWorker) run(ctx context.Context) {
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
			w.results <- pieceResult{index: idx, err: fmt.Errorf("httpseed %s: %w", w.url, err)}
			if w.prog != nil {
				w.prog.PieceFailed(idx)
			}

			// Handle 503 retry-after.
			if wait, ok := retryAfter(err); ok {
				select {
				case <-ctx.Done():
					return
				case <-time.After(wait):
				}
			} else {
				select {
				case <-ctx.Done():
					return
				case <-time.After(2 * time.Second):
				}
			}
			continue
		}

		hash := sha1.Sum(data)
		if hash != w.torrent.Pieces[idx] {
			w.picker.Abort(idx)
			w.results <- pieceResult{
				index: idx,
				err:   fmt.Errorf("httpseed piece %d hash mismatch", idx),
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

// fetchPiece downloads one piece via BEP 17 HTTP seed protocol.
// Request format: <url>?info_hash=<percent-encoded>&piece=<N>
func (w *httpseedWorker) fetchPiece(ctx context.Context, idx int) ([]byte, error) {
	reqURL := fmt.Sprintf("%s?info_hash=%s&piece=%d",
		w.url, percentEncodeInfoHash(w.torrent.InfoHash), idx)

	req, err := http.NewRequestWithContext(ctx, "GET", reqURL, nil)
	if err != nil {
		return nil, err
	}

	resp, err := w.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusServiceUnavailable {
		body, _ := io.ReadAll(resp.Body)
		secs, _ := strconv.Atoi(string(body))
		if secs <= 0 {
			secs = 30
		}
		return nil, &retryError{wait: time.Duration(secs) * time.Second}
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	pieceLen := w.torrent.PieceLen(idx)
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

// retryError signals that the server returned 503 with a wait time.
type retryError struct {
	wait time.Duration
}

func (e *retryError) Error() string {
	return fmt.Sprintf("503 retry after %s", e.wait)
}

// retryAfter extracts the wait duration from a retryError.
func retryAfter(err error) (time.Duration, bool) {
	if re, ok := err.(*retryError); ok {
		return re.wait, true
	}
	return 0, false
}

// percentEncodeInfoHash percent-encodes a 20-byte info hash for URL query use.
func percentEncodeInfoHash(hash [20]byte) string {
	var buf [60]byte // worst case: 20 × 3 = 60
	n := 0
	for _, b := range hash {
		if isUnreservedByte(b) {
			buf[n] = b
			n++
		} else {
			buf[n] = '%'
			buf[n+1] = hexChar(b >> 4)
			buf[n+2] = hexChar(b & 0x0f)
			n += 3
		}
	}
	return string(buf[:n])
}

func isUnreservedByte(c byte) bool {
	return (c >= 'A' && c <= 'Z') ||
		(c >= 'a' && c <= 'z') ||
		(c >= '0' && c <= '9') ||
		c == '-' || c == '_' || c == '.' || c == '~'
}

func hexChar(b byte) byte {
	if b < 10 {
		return '0' + b
	}
	return 'A' + b - 10
}
