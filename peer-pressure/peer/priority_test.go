package peer

import (
	"net"
	"testing"
)

func TestPrioritySpecVectors(t *testing.T) {
	// BEP 40 reference test vectors.
	tests := []struct {
		self, peer string
		want       uint32
	}{
		// Different subnets: masked to /24
		{"123.213.32.10", "98.76.54.32", 0xec2d7224},
		// Same subnet: use full IPs
		{"123.213.32.10", "123.213.32.234", 0x99568189},
	}

	for _, tt := range tests {
		got := Priority(net.ParseIP(tt.self), net.ParseIP(tt.peer))
		if got != tt.want {
			t.Errorf("Priority(%s, %s) = %#08x, want %#08x", tt.self, tt.peer, got, tt.want)
		}
	}
}

func TestPrioritySymmetric(t *testing.T) {
	a := net.IPv4(1, 2, 3, 4)
	b := net.IPv4(5, 6, 7, 8)
	if Priority(a, b) != Priority(b, a) {
		t.Error("priority should be symmetric")
	}
}

func TestPrioritySameSubnetDistinct(t *testing.T) {
	a := net.IPv4(10, 0, 0, 1)
	b := net.IPv4(10, 0, 0, 2)
	c := net.IPv4(10, 0, 0, 3)

	pab := Priority(a, b)
	pac := Priority(a, c)
	if pab == pac {
		t.Error("same-subnet peers should have distinct priorities")
	}
}

func TestPriorityIPv6(t *testing.T) {
	a := net.ParseIP("2001:db8:85a3::1")
	b := net.ParseIP("2001:db8:aaaa::1")
	p := Priority(a, b)
	if p == 0 {
		t.Error("expected non-zero priority for valid IPv6 pair")
	}

	// Symmetric
	if Priority(a, b) != Priority(b, a) {
		t.Error("IPv6 priority should be symmetric")
	}
}
