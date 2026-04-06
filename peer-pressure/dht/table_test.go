package dht

import (
	"net"
	"testing"
)

func TestXORDistance(t *testing.T) {
	var zeros, ones NodeID
	for i := range 20 {
		ones[i] = 0xFF
	}
	got := XOR(zeros, ones)
	if got != ones {
		t.Errorf("XOR(zeros, ones): got %x, want %x", got, ones)
	}
	got = XOR(ones, ones)
	if got != zeros {
		t.Errorf("XOR(ones, ones): got %x, want zeros", got)
	}
}

func TestBucketIndex(t *testing.T) {
	tests := []struct {
		name string
		dist NodeID
		want int
	}{
		{"highest bit", func() NodeID { var d NodeID; d[0] = 0x80; return d }(), 159},
		{"second bit", func() NodeID { var d NodeID; d[0] = 0x40; return d }(), 158},
		{"lowest bit", func() NodeID { var d NodeID; d[19] = 0x01; return d }(), 0},
		{"mid byte", func() NodeID { var d NodeID; d[10] = 0x04; return d }(), 74},
		{"zero distance", NodeID{}, -1},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := BucketIndex(tc.dist)
			if got != tc.want {
				t.Errorf("BucketIndex(%x): got %d, want %d", tc.dist, got, tc.want)
			}
		})
	}
}

func makeNode(b byte) Node {
	var id NodeID
	id[0] = b
	return Node{ID: id, Addr: net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: int(b) + 1000}}
}

func TestInsertAndClosest(t *testing.T) {
	own := NodeID{} // all zeros
	rt := NewRoutingTable(own)

	// Insert 20 nodes with different IDs.
	for i := byte(1); i <= 20; i++ {
		rt.Insert(makeNode(i))
	}

	if rt.Len() != 20 {
		t.Fatalf("Len: got %d, want 20", rt.Len())
	}

	// Target = 0x05: closest should be 0x05 itself (distance 0x05), then nearby.
	target := NodeID{0x05}
	closest := rt.Closest(target, 8)
	if len(closest) != 8 {
		t.Fatalf("Closest(8): got %d nodes", len(closest))
	}

	// First result should be 0x05 (distance = 0x05 XOR 0x05 = 0)... wait,
	// own is 0x00, target is 0x05, node IDs are 0x01-0x14.
	// XOR(node, target): node 0x05 → 0x00, node 0x04 → 0x01, node 0x07 → 0x02...
	if closest[0].ID[0] != 0x05 {
		t.Errorf("closest[0]: got %x, want 0x05", closest[0].ID[0])
	}

	// Verify sorted by distance.
	for i := 1; i < len(closest); i++ {
		di := XOR(closest[i-1].ID, target)
		dj := XOR(closest[i].ID, target)
		if compareDist(di, dj) > 0 {
			t.Errorf("not sorted at index %d: %x > %x", i, di, dj)
		}
	}
}

func TestBucketFull(t *testing.T) {
	own := NodeID{} // all zeros
	rt := NewRoutingTable(own)

	// All these nodes have ID[0] with the high bit set → same bucket (159).
	for i := byte(0); i < 9; i++ {
		var id NodeID
		id[0] = 0x80 | i
		n := Node{ID: id, Addr: net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 1000 + int(i)}}
		ok := rt.Insert(n)
		if i < 8 && !ok {
			t.Errorf("insert %d: should have succeeded", i)
		}
		if i == 8 && ok {
			t.Error("insert 9th: should have been rejected (bucket full)")
		}
	}
}

func TestInsertDuplicate(t *testing.T) {
	own := NodeID{}
	rt := NewRoutingTable(own)

	n := makeNode(0x42)
	rt.Insert(n)
	rt.Insert(n) // duplicate

	if rt.Len() != 1 {
		t.Errorf("Len after duplicate: got %d, want 1", rt.Len())
	}
}

func TestRemove(t *testing.T) {
	own := NodeID{}
	rt := NewRoutingTable(own)

	n := makeNode(0x42)
	rt.Insert(n)
	if rt.Len() != 1 {
		t.Fatalf("Len after insert: got %d", rt.Len())
	}

	rt.Remove(n.ID)
	if rt.Len() != 0 {
		t.Errorf("Len after remove: got %d, want 0", rt.Len())
	}
}

func TestClosestOrder(t *testing.T) {
	own := NodeID{}
	rt := NewRoutingTable(own)

	// Insert nodes at varying distances.
	nodes := []byte{0x10, 0x08, 0x04, 0x02, 0x01, 0x20, 0x40, 0x80}
	for _, b := range nodes {
		rt.Insert(makeNode(b))
	}

	target := NodeID{0x03} // closest to 0x02 (dist=1), 0x01 (dist=2), 0x04(dist=7)...

	closest := rt.Closest(target, len(nodes))
	for i := 1; i < len(closest); i++ {
		di := XOR(closest[i-1].ID, target)
		dj := XOR(closest[i].ID, target)
		if compareDist(di, dj) > 0 {
			t.Errorf("not sorted at %d: dist(%x)=%x > dist(%x)=%x",
				i, closest[i-1].ID, di, closest[i].ID, dj)
		}
	}
}
