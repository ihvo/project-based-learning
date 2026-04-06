package throttle

import (
	"bytes"
	"io"
	"testing"
	"time"
)

func TestLimiterUnlimited(t *testing.T) {
	l := NewLimiter(0)
	start := time.Now()
	l.Wait(1_000_000)
	if elapsed := time.Since(start); elapsed > 10*time.Millisecond {
		t.Errorf("unlimited limiter took %v", elapsed)
	}
}

func TestLimiterRate(t *testing.T) {
	// 50KB/s, write 100KB → should take ~2s (burst starts full so first 50KB is free).
	l := NewLimiter(50_000)
	start := time.Now()
	l.Wait(50_000) // consume burst
	l.Wait(50_000) // must wait ~1s
	elapsed := time.Since(start)

	if elapsed < 800*time.Millisecond {
		t.Errorf("expected ≥800ms, got %v", elapsed)
	}
	if elapsed > 3*time.Second {
		t.Errorf("expected ≤3s, got %v", elapsed)
	}
}

func TestLimiterBurst(t *testing.T) {
	l := NewLimiter(100_000)
	start := time.Now()
	l.Wait(100_000) // should consume burst immediately
	elapsed := time.Since(start)
	if elapsed > 50*time.Millisecond {
		t.Errorf("burst should be instant, took %v", elapsed)
	}
}

func TestWrapReaderPassthrough(t *testing.T) {
	data := []byte("hello world")
	r := WrapReader(bytes.NewReader(data), nil)
	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(out, data) {
		t.Errorf("got %q, want %q", out, data)
	}
}

func TestWrapWriterPassthrough(t *testing.T) {
	var buf bytes.Buffer
	w := WrapWriter(&buf, nil)
	data := []byte("hello world")
	n, err := w.Write(data)
	if err != nil || n != len(data) {
		t.Fatalf("write: %d, %v", n, err)
	}
	if !bytes.Equal(buf.Bytes(), data) {
		t.Errorf("got %q", buf.Bytes())
	}
}

func TestWrapReaderThrottled(t *testing.T) {
	// 10KB/s, read 20KB. Burst covers first 10KB, second 10KB takes ~1s.
	data := make([]byte, 20_000)
	for i := range data {
		data[i] = byte(i)
	}

	l := NewLimiter(10_000)
	r := WrapReader(bytes.NewReader(data), l)

	start := time.Now()
	out, err := io.ReadAll(r)
	elapsed := time.Since(start)

	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(out, data) {
		t.Error("data mismatch")
	}
	if elapsed < 500*time.Millisecond {
		t.Errorf("expected throttling, elapsed = %v", elapsed)
	}
}

func TestWrapWriterThrottled(t *testing.T) {
	var buf bytes.Buffer
	l := NewLimiter(10_000)
	w := WrapWriter(&buf, l)

	data := make([]byte, 20_000)
	start := time.Now()
	_, err := w.Write(data)
	elapsed := time.Since(start)

	if err != nil {
		t.Fatal(err)
	}
	if elapsed < 500*time.Millisecond {
		t.Errorf("expected throttling, elapsed = %v", elapsed)
	}
}
