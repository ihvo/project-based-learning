package utp

import (
	"testing"
	"time"
)

func TestCongestionInitialState(t *testing.T) {
	cc := NewCongestionController()
	if cc.MaxWindow == 0 {
		t.Error("initial MaxWindow should be non-zero")
	}
	if cc.Timeout != InitTimeout {
		t.Errorf("initial Timeout = %v, want %v", cc.Timeout, InitTimeout)
	}
}

func TestCongestionOnAckUpdatesRTT(t *testing.T) {
	cc := NewCongestionController()

	cc.OnAck(100 * time.Millisecond)
	rtt := cc.RTT()
	if rtt < 50*time.Millisecond || rtt > 150*time.Millisecond {
		t.Errorf("RTT after first sample = %v, expected ~100ms", rtt)
	}

	// Second sample smooths the RTT.
	cc.OnAck(80 * time.Millisecond)
	rtt2 := cc.RTT()
	if rtt2 < 70*time.Millisecond || rtt2 > 110*time.Millisecond {
		t.Errorf("RTT after second sample = %v, expected ~90ms", rtt2)
	}
}

func TestCongestionTimeoutMinimum(t *testing.T) {
	cc := NewCongestionController()

	// Very small RTT should not bring timeout below MinTimeout.
	cc.OnAck(1 * time.Millisecond)
	if cc.Timeout < MinTimeout {
		t.Errorf("Timeout = %v, should not be less than %v", cc.Timeout, MinTimeout)
	}
}

func TestCongestionOnDelaySampleGrows(t *testing.T) {
	cc := NewCongestionController()
	cc.CurWindow = 1000

	// Under target delay → window should grow.
	initial := cc.MaxWindow
	cc.OnDelaySample(0) // zero delay = way under target
	if cc.MaxWindow <= initial {
		t.Errorf("window should grow when delay is 0: was %d, now %d", initial, cc.MaxWindow)
	}
}

func TestCongestionOnDelaySampleShrinks(t *testing.T) {
	cc := NewCongestionController()
	cc.MaxWindow = 100000
	cc.CurWindow = 80000

	// Set base delay first.
	cc.OnDelaySample(1000)

	// Way over target: 200ms over baseline vs 100ms target → should shrink.
	before := cc.MaxWindow
	cc.OnDelaySample(201000) // 200ms above base
	if cc.MaxWindow >= before {
		t.Errorf("window should shrink when delay exceeds target: was %d, now %d", before, cc.MaxWindow)
	}
}

func TestCongestionOnTimeout(t *testing.T) {
	cc := NewCongestionController()
	cc.MaxWindow = 50000
	prevTimeout := cc.Timeout

	cc.OnTimeout()

	if cc.MaxWindow != uint32(MinPacketSize) {
		t.Errorf("MaxWindow after timeout = %d, want %d", cc.MaxWindow, MinPacketSize)
	}
	if cc.Timeout != prevTimeout*2 {
		t.Errorf("Timeout after timeout = %v, want %v (doubled)", cc.Timeout, prevTimeout*2)
	}
}

func TestCongestionOnPacketLoss(t *testing.T) {
	cc := NewCongestionController()
	cc.MaxWindow = 10000

	cc.OnPacketLoss()
	if cc.MaxWindow != 5000 {
		t.Errorf("MaxWindow after loss = %d, want 5000", cc.MaxWindow)
	}

	// Multiple losses should floor at MinPacketSize.
	for range 20 {
		cc.OnPacketLoss()
	}
	if cc.MaxWindow < uint32(MinPacketSize) {
		t.Errorf("MaxWindow = %d, should not drop below %d", cc.MaxWindow, MinPacketSize)
	}
}

func TestCongestionCanSend(t *testing.T) {
	cc := NewCongestionController()
	cc.MaxWindow = 5000
	cc.CurWindow = 4000

	if !cc.CanSend(1000, 10000) {
		t.Error("should be able to send 1000 when curWnd=4000, maxWnd=5000")
	}
	if cc.CanSend(1001, 10000) {
		t.Error("should NOT be able to send 1001 when curWnd=4000, maxWnd=5000")
	}
}

func TestCongestionCanSendPeerWindow(t *testing.T) {
	cc := NewCongestionController()
	cc.MaxWindow = 10000
	cc.CurWindow = 0

	// Peer advertises small window.
	if cc.CanSend(3000, 2000) {
		t.Error("should NOT exceed peer window size")
	}
	if !cc.CanSend(2000, 2000) {
		t.Error("should be able to send up to peer window")
	}
}

func TestCongestionBaseDelayReset(t *testing.T) {
	cc := NewCongestionController()
	cc.CurWindow = 1000

	// First sample sets base delay.
	cc.OnDelaySample(50000) // 50ms
	if cc.OurDelay() != 0 {
		t.Errorf("ourDelay should be 0 when first sample = base_delay, got %v", cc.OurDelay())
	}

	// Lower sample updates base delay.
	cc.OnDelaySample(30000) // 30ms
	if cc.OurDelay() != 0 {
		t.Errorf("ourDelay should be 0 after lower base_delay, got %v", cc.OurDelay())
	}

	// Higher sample shows queuing delay.
	cc.OnDelaySample(80000) // 80ms → 50ms above base
	want := 50 * time.Millisecond
	if cc.OurDelay() != want {
		t.Errorf("ourDelay = %v, want %v", cc.OurDelay(), want)
	}
}
