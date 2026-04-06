package discovery

import (
	"context"
	"errors"
	"sort"
	"testing"
	"time"
)

// mockSource implements PeerSource for testing.
type mockSource struct {
	name  string
	peers []string
	err   error
	delay time.Duration
}

func (m *mockSource) Name() string { return m.name }

func (m *mockSource) Peers(ctx context.Context, infoHash [20]byte) ([]string, error) {
	if m.delay > 0 {
		select {
		case <-time.After(m.delay):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return m.peers, m.err
}

func TestManagerMergesSources(t *testing.T) {
	s1 := &mockSource{name: "tracker", peers: []string{"1.1.1.1:6881", "2.2.2.2:6881"}}
	s2 := &mockSource{name: "dht", peers: []string{"3.3.3.3:6881", "1.1.1.1:6881"}} // overlap
	s3 := &mockSource{name: "pex", peers: []string{"4.4.4.4:6881"}}

	mgr := NewManager(s1, s2, s3)
	ch := mgr.Discover(context.Background(), [20]byte{})

	var got []string
	for addr := range ch {
		got = append(got, addr)
	}

	sort.Strings(got)
	want := []string{"1.1.1.1:6881", "2.2.2.2:6881", "3.3.3.3:6881", "4.4.4.4:6881"}
	if len(got) != len(want) {
		t.Fatalf("got %d peers, want %d: %v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("got[%d] = %s, want %s", i, got[i], want[i])
		}
	}
}

func TestManagerDeduplicates(t *testing.T) {
	s1 := &mockSource{name: "a", peers: []string{"1.1.1.1:80", "2.2.2.2:80"}}
	s2 := &mockSource{name: "b", peers: []string{"1.1.1.1:80", "2.2.2.2:80"}}

	mgr := NewManager(s1, s2)
	ch := mgr.Discover(context.Background(), [20]byte{})

	var got []string
	for addr := range ch {
		got = append(got, addr)
	}

	if len(got) != 2 {
		t.Errorf("expected 2 unique peers, got %d: %v", len(got), got)
	}
}

func TestManagerOneSourceFails(t *testing.T) {
	good := &mockSource{name: "good", peers: []string{"1.1.1.1:80"}}
	bad := &mockSource{name: "bad", err: errors.New("connection refused")}

	mgr := NewManager(good, bad)
	ch := mgr.Discover(context.Background(), [20]byte{})

	var got []string
	for addr := range ch {
		got = append(got, addr)
	}

	if len(got) != 1 || got[0] != "1.1.1.1:80" {
		t.Errorf("expected 1 peer from good source, got %v", got)
	}
}

func TestManagerAllFail(t *testing.T) {
	bad1 := &mockSource{name: "a", err: errors.New("fail")}
	bad2 := &mockSource{name: "b", err: errors.New("fail")}

	mgr := NewManager(bad1, bad2)
	ch := mgr.Discover(context.Background(), [20]byte{})

	var got []string
	for addr := range ch {
		got = append(got, addr)
	}

	if len(got) != 0 {
		t.Errorf("expected 0 peers, got %d", len(got))
	}
}

func TestManagerContextCancellation(t *testing.T) {
	slow := &mockSource{name: "slow", peers: []string{"1.1.1.1:80"}, delay: 5 * time.Second}

	mgr := NewManager(slow)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	ch := mgr.Discover(ctx, [20]byte{})

	var got []string
	for addr := range ch {
		got = append(got, addr)
	}

	if len(got) != 0 {
		t.Errorf("expected 0 peers (cancelled before slow source), got %d", len(got))
	}
}

func TestManagerNoSources(t *testing.T) {
	mgr := NewManager()
	ch := mgr.Discover(context.Background(), [20]byte{})

	var got []string
	for addr := range ch {
		got = append(got, addr)
	}

	if len(got) != 0 {
		t.Errorf("expected 0 peers, got %d", len(got))
	}
}

func TestManagerCount(t *testing.T) {
	s := &mockSource{name: "x", peers: []string{"a:1", "b:2", "c:3"}}
	mgr := NewManager(s)

	ch := mgr.Discover(context.Background(), [20]byte{})
	for range ch {
	}

	if mgr.Count() != 3 {
		t.Errorf("Count = %d, want 3", mgr.Count())
	}
}

func TestManagerSeen(t *testing.T) {
	s := &mockSource{name: "x", peers: []string{"host:1"}}
	mgr := NewManager(s)

	ch := mgr.Discover(context.Background(), [20]byte{})
	for range ch {
	}

	if !mgr.Seen("host:1") {
		t.Error("host:1 should be seen")
	}
	if mgr.Seen("host:2") {
		t.Error("host:2 should not be seen")
	}
}

func TestManagerReset(t *testing.T) {
	s := &mockSource{name: "x", peers: []string{"a:1"}}
	mgr := NewManager(s)

	ch := mgr.Discover(context.Background(), [20]byte{})
	for range ch {
	}
	if mgr.Count() != 1 {
		t.Fatalf("count after first discover = %d", mgr.Count())
	}

	mgr.Reset()
	if mgr.Count() != 0 {
		t.Errorf("count after reset = %d", mgr.Count())
	}

	// Re-discover should yield peers again.
	ch = mgr.Discover(context.Background(), [20]byte{})
	var got []string
	for addr := range ch {
		got = append(got, addr)
	}
	if len(got) != 1 {
		t.Errorf("expected 1 peer after reset, got %d", len(got))
	}
}

func TestManagerConcurrentSources(t *testing.T) {
	// Multiple sources with slight delays to exercise concurrency.
	sources := make([]PeerSource, 10)
	for i := range 10 {
		peers := make([]string, 5)
		for j := range 5 {
			peers[j] = string(rune('A'+i)) + ":" + string(rune('0'+j))
		}
		sources[i] = &mockSource{
			name:  string(rune('A' + i)),
			peers: peers,
			delay: time.Duration(i) * time.Millisecond,
		}
	}

	mgr := NewManager(sources...)
	ch := mgr.Discover(context.Background(), [20]byte{})

	var count int
	for range ch {
		count++
	}

	// 10 sources × 5 peers each, all unique → 50 total.
	if count != 50 {
		t.Errorf("expected 50 peers, got %d", count)
	}
}
