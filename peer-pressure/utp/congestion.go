package utp

import (
	"time"
)

// Congestion control constants from BEP 29.
const (
	TargetDelay  = 100 * time.Millisecond // CCONTROL_TARGET
	MinTimeout   = 500 * time.Millisecond
	InitTimeout  = 1000 * time.Millisecond
	MinPacketSize = 150                    // bytes, floor for dynamic sizing
	MaxGain      = 3000                    // MAX_CWND_INCREASE_PACKETS_PER_RTT (bytes)
	BaseDelayWindow = 2 * time.Minute     // sliding window for base_delay
)

// CongestionController implements BEP 29 delay-based congestion control (LEDBAT).
type CongestionController struct {
	MaxWindow uint32 // send window in bytes
	CurWindow uint32 // bytes currently in flight

	rtt    int64 // smoothed RTT in microseconds
	rttVar int64 // RTT variance in microseconds

	Timeout time.Duration

	baseDelay     int64     // minimum observed one-way delay (µs)
	baseDelayTime time.Time // when base_delay was last set

	ourDelay int64 // latest delay sample (µs)
}

// NewCongestionController creates a controller with default initial values.
func NewCongestionController() *CongestionController {
	return &CongestionController{
		MaxWindow: 3 * 1400, // initial window: ~3 packets
		Timeout:   InitTimeout,
		baseDelay: -1, // sentinel: not yet set
	}
}

// OnAck updates RTT and congestion window when a packet is acknowledged.
// packetRTT is the measured round-trip time for this specific packet.
func (cc *CongestionController) OnAck(packetRTT time.Duration) {
	rttUs := packetRTT.Microseconds()

	if cc.rtt == 0 {
		cc.rtt = rttUs
		cc.rttVar = rttUs / 2
	} else {
		delta := cc.rtt - rttUs
		if delta < 0 {
			delta = -delta
		}
		cc.rttVar += (delta - cc.rttVar) / 4
		cc.rtt += (rttUs - cc.rtt) / 8
	}

	timeoutUs := cc.rtt + cc.rttVar*4
	cc.Timeout = time.Duration(timeoutUs) * time.Microsecond
	if cc.Timeout < MinTimeout {
		cc.Timeout = MinTimeout
	}
}

// OnDelaySample processes a one-way delay measurement and adjusts the window.
// delayUs is timestamp_difference_microseconds from the received packet.
func (cc *CongestionController) OnDelaySample(delayUs int64) {
	now := time.Now()

	// Reset base_delay if the window has expired.
	if cc.baseDelay < 0 || now.Sub(cc.baseDelayTime) > BaseDelayWindow {
		cc.baseDelay = delayUs
		cc.baseDelayTime = now
	} else if delayUs < cc.baseDelay {
		cc.baseDelay = delayUs
		cc.baseDelayTime = now
	}

	cc.ourDelay = delayUs - cc.baseDelay

	offTarget := int64(TargetDelay.Microseconds()) - cc.ourDelay

	var delayFactor float64
	if TargetDelay.Microseconds() != 0 {
		delayFactor = float64(offTarget) / float64(TargetDelay.Microseconds())
	}

	var windowFactor float64
	if cc.MaxWindow != 0 {
		windowFactor = float64(cc.CurWindow) / float64(cc.MaxWindow)
	}

	scaledGain := float64(MaxGain) * delayFactor * windowFactor
	newWindow := int64(cc.MaxWindow) + int64(scaledGain)
	if newWindow < 0 {
		newWindow = 0
	}
	cc.MaxWindow = uint32(newWindow)
}

// OnTimeout handles a packet timeout by resetting the window to minimum.
func (cc *CongestionController) OnTimeout() {
	cc.MaxWindow = uint32(MinPacketSize)
	cc.Timeout *= 2 // exponential backoff
}

// OnPacketLoss halves the window per BEP 29 loss handling.
func (cc *CongestionController) OnPacketLoss() {
	cc.MaxWindow /= 2
	if cc.MaxWindow < uint32(MinPacketSize) {
		cc.MaxWindow = uint32(MinPacketSize)
	}
}

// CanSend returns true if the socket is allowed to send packetSize bytes.
func (cc *CongestionController) CanSend(packetSize uint32, peerWndSize uint32) bool {
	effectiveWindow := cc.MaxWindow
	if peerWndSize < effectiveWindow {
		effectiveWindow = peerWndSize
	}
	return cc.CurWindow+packetSize <= effectiveWindow
}

// RTT returns the smoothed round-trip time.
func (cc *CongestionController) RTT() time.Duration {
	return time.Duration(cc.rtt) * time.Microsecond
}

// OurDelay returns the current buffering delay estimate.
func (cc *CongestionController) OurDelay() time.Duration {
	return time.Duration(cc.ourDelay) * time.Microsecond
}
