// Package throttle implements a token-bucket rate limiter for wrapping
// io.Reader and io.Writer with bandwidth constraints.
package throttle

import (
	"io"
	"sync"
	"time"
)

// Limiter implements a token-bucket rate limiter. A zero-value Limiter
// (or one created with rate=0) is unlimited and passes through without delay.
type Limiter struct {
	mu       sync.Mutex
	rate     float64   // bytes per second
	tokens   float64   // available tokens
	burst    int       // max tokens (1 second worth)
	lastTime time.Time // last token refill
}

// NewLimiter creates a rate limiter. If bytesPerSec is 0, the limiter
// is unlimited (no delay on any operation).
func NewLimiter(bytesPerSec int) *Limiter {
	if bytesPerSec <= 0 {
		return &Limiter{} // unlimited
	}
	return &Limiter{
		rate:     float64(bytesPerSec),
		tokens:   float64(bytesPerSec), // start with full burst
		burst:    bytesPerSec,
		lastTime: time.Now(),
	}
}

// Wait blocks until n tokens are available, consuming them.
// For an unlimited limiter, returns immediately.
func (l *Limiter) Wait(n int) {
	if l.rate == 0 {
		return
	}

	for {
		l.mu.Lock()
		l.refill()

		if l.tokens >= float64(n) {
			l.tokens -= float64(n)
			l.mu.Unlock()
			return
		}

		// How long to wait for enough tokens?
		deficit := float64(n) - l.tokens
		wait := time.Duration(deficit / l.rate * float64(time.Second))
		l.mu.Unlock()

		time.Sleep(wait)
	}
}

// refill adds tokens based on elapsed time. Caller must hold l.mu.
func (l *Limiter) refill() {
	now := time.Now()
	elapsed := now.Sub(l.lastTime).Seconds()
	l.lastTime = now
	l.tokens += elapsed * l.rate
	if l.tokens > float64(l.burst) {
		l.tokens = float64(l.burst)
	}
}

// Reader wraps an io.Reader with rate limiting.
type Reader struct {
	r       io.Reader
	limiter *Limiter
}

// WrapReader returns a rate-limited reader. If limiter is nil or
// unlimited, the original reader is returned as-is.
func WrapReader(r io.Reader, limiter *Limiter) io.Reader {
	if limiter == nil || limiter.rate == 0 {
		return r
	}
	return &Reader{r: r, limiter: limiter}
}

func (r *Reader) Read(p []byte) (int, error) {
	// Limit read size to burst to avoid long waits.
	max := len(p)
	if r.limiter.burst > 0 && max > r.limiter.burst {
		max = r.limiter.burst
	}
	n, err := r.r.Read(p[:max])
	if n > 0 {
		r.limiter.Wait(n)
	}
	return n, err
}

// Writer wraps an io.Writer with rate limiting.
type Writer struct {
	w       io.Writer
	limiter *Limiter
}

// WrapWriter returns a rate-limited writer. If limiter is nil or
// unlimited, the original writer is returned as-is.
func WrapWriter(w io.Writer, limiter *Limiter) io.Writer {
	if limiter == nil || limiter.rate == 0 {
		return w
	}
	return &Writer{w: w, limiter: limiter}
}

func (w *Writer) Write(p []byte) (int, error) {
	written := 0
	chunk := w.limiter.burst
	if chunk <= 0 {
		chunk = len(p)
	}
	for written < len(p) {
		end := written + chunk
		if end > len(p) {
			end = len(p)
		}
		w.limiter.Wait(end - written)
		n, err := w.w.Write(p[written:end])
		written += n
		if err != nil {
			return written, err
		}
	}
	return written, nil
}
