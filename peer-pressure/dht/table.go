package dht

import (
	"math/bits"
	"net"
	"sort"
	"sync"
)

const bucketSize = 8 // k = 8 per Kademlia / BEP 5

// Node represents a DHT peer with its ID and UDP address.
type Node struct {
	ID   NodeID
	Addr net.UDPAddr
}

// XOR returns the bitwise XOR of two node IDs (Kademlia distance metric).
func XOR(a, b NodeID) NodeID {
	var result NodeID
	for i := range 20 {
		result[i] = a[i] ^ b[i]
	}
	return result
}

// BucketIndex returns the routing table bucket index for a given distance.
// The index is the position of the highest set bit (0–159).
// A distance of zero returns -1 (same node).
func BucketIndex(distance NodeID) int {
	for i := range 20 {
		if distance[i] != 0 {
			return 159 - (i*8 + bits.LeadingZeros8(distance[i]))
		}
	}
	return -1 // identical nodes
}

// RoutingTable is a Kademlia routing table with 160 k-buckets.
type RoutingTable struct {
	own     NodeID
	buckets [160][]Node
	mu      sync.RWMutex
}

// NewRoutingTable creates a routing table for the given local node ID.
func NewRoutingTable(own NodeID) *RoutingTable {
	return &RoutingTable{own: own}
}

// Insert adds a node to the routing table.
// Returns true if inserted, false if the bucket is full.
func (rt *RoutingTable) Insert(n Node) bool {
	idx := BucketIndex(XOR(rt.own, n.ID))
	if idx < 0 {
		return false // don't insert ourselves
	}

	rt.mu.Lock()
	defer rt.mu.Unlock()

	bucket := rt.buckets[idx]

	// Check if already present — move to end (most recently seen).
	for i, existing := range bucket {
		if existing.ID == n.ID {
			bucket = append(bucket[:i], bucket[i+1:]...)
			bucket = append(bucket, n)
			rt.buckets[idx] = bucket
			return true
		}
	}

	if len(bucket) >= bucketSize {
		return false // bucket full
	}

	rt.buckets[idx] = append(bucket, n)
	return true
}

// Remove deletes a node from the routing table by ID.
func (rt *RoutingTable) Remove(id NodeID) {
	idx := BucketIndex(XOR(rt.own, id))
	if idx < 0 {
		return
	}

	rt.mu.Lock()
	defer rt.mu.Unlock()

	bucket := rt.buckets[idx]
	for i, n := range bucket {
		if n.ID == id {
			rt.buckets[idx] = append(bucket[:i], bucket[i+1:]...)
			return
		}
	}
}

// Closest returns the n closest nodes to the target, sorted by XOR distance.
func (rt *RoutingTable) Closest(target NodeID, n int) []Node {
	rt.mu.RLock()
	defer rt.mu.RUnlock()

	// Collect all nodes.
	var all []Node
	for _, bucket := range rt.buckets {
		all = append(all, bucket...)
	}

	// Sort by distance to target.
	sort.Slice(all, func(i, j int) bool {
		di := XOR(all[i].ID, target)
		dj := XOR(all[j].ID, target)
		return compareDist(di, dj) < 0
	})

	if len(all) > n {
		all = all[:n]
	}
	return all
}

// Len returns the total number of nodes in the routing table.
func (rt *RoutingTable) Len() int {
	rt.mu.RLock()
	defer rt.mu.RUnlock()
	total := 0
	for _, b := range rt.buckets {
		total += len(b)
	}
	return total
}

// compareDist returns -1/0/+1 comparing two XOR distances as big-endian bytes.
func compareDist(a, b NodeID) int {
	for i := range 20 {
		if a[i] < b[i] {
			return -1
		}
		if a[i] > b[i] {
			return 1
		}
	}
	return 0
}
