package dht

import (
	"net"
	"sync"
	"testing"
	"time"

	"github.com/ihvo/peer-pressure/bencode"
)

// mockDHTNode creates a UDP endpoint that responds to ping and find_node.
type mockDHTNode struct {
	id   NodeID
	conn *net.UDPConn
	// findNodeResponse is the compact nodes to return for find_node queries.
	findNodeResponse []byte
	done             chan struct{}
}

func newMockDHTNode(t *testing.T, id NodeID, findNodeResp []byte) *mockDHTNode {
	t.Helper()
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatalf("listen mock: %v", err)
	}
	m := &mockDHTNode{
		id:               id,
		conn:             conn,
		findNodeResponse: findNodeResp,
		done:             make(chan struct{}),
	}
	go m.serve()
	return m
}

func (m *mockDHTNode) serve() {
	defer close(m.done)
	buf := make([]byte, 4096)
	for {
		n, addr, err := m.conn.ReadFromUDP(buf)
		if err != nil {
			return
		}
		msg, err := DecodeMessage(buf[:n])
		if err != nil {
			continue
		}

		var resp Message
		switch msg.Method {
		case "ping":
			resp = Message{
				TxnID: msg.TxnID,
				Type:  "r",
				Reply: bencode.Dict{"id": bencode.String(m.id[:])},
			}
		case "find_node":
			resp = Message{
				TxnID: msg.TxnID,
				Type:  "r",
				Reply: bencode.Dict{
					"id":    bencode.String(m.id[:]),
					"nodes": bencode.String(m.findNodeResponse),
				},
			}
		default:
			continue
		}
		m.conn.WriteToUDP(EncodeMessage(resp), addr)
	}
}

func (m *mockDHTNode) addr() *net.UDPAddr {
	return m.conn.LocalAddr().(*net.UDPAddr)
}

func (m *mockDHTNode) close() {
	m.conn.Close()
	<-m.done
}

func TestPing(t *testing.T) {
	serverID := NodeID{0xAA}
	mock := newMockDHTNode(t, serverID, nil)
	defer mock.close()

	conn, _ := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	dht := New(conn)
	defer dht.Transport.Close()
	go dht.Transport.Listen(nil)

	got, err := dht.Ping(mock.addr())
	if err != nil {
		t.Fatalf("Ping: %v", err)
	}
	if got != serverID {
		t.Errorf("Ping: got %x, want %x", got, serverID)
	}

	// Node should be in routing table now.
	if dht.Table.Len() != 1 {
		t.Errorf("table should have 1 node, got %d", dht.Table.Len())
	}
}

func TestCompactNodesRoundTrip(t *testing.T) {
	nodes := []Node{
		{ID: NodeID{0x01}, Addr: net.UDPAddr{IP: net.IPv4(10, 0, 0, 1), Port: 6881}},
		{ID: NodeID{0x02}, Addr: net.UDPAddr{IP: net.IPv4(192, 168, 1, 1), Port: 51413}},
		{ID: NodeID{0xFF}, Addr: net.UDPAddr{IP: net.IPv4(8, 8, 8, 8), Port: 80}},
	}

	encoded := EncodeCompactNodes(nodes)
	if len(encoded) != 78 { // 3 * 26
		t.Fatalf("encoded length: got %d, want 78", len(encoded))
	}

	decoded := DecodeCompactNodes(encoded)
	if len(decoded) != 3 {
		t.Fatalf("decoded length: got %d, want 3", len(decoded))
	}

	for i, n := range decoded {
		if n.ID != nodes[i].ID {
			t.Errorf("node %d ID: got %x, want %x", i, n.ID, nodes[i].ID)
		}
		if !n.Addr.IP.Equal(nodes[i].Addr.IP) || n.Addr.Port != nodes[i].Addr.Port {
			t.Errorf("node %d addr: got %v, want %v", i, n.Addr, nodes[i].Addr)
		}
	}
}

func TestCompactPeersRoundTrip(t *testing.T) {
	addrs := []string{"10.0.0.1:6881", "192.168.1.1:51413"}
	encoded := EncodeCompactPeers(addrs)
	if len(encoded) != 12 { // 2 * 6
		t.Fatalf("encoded length: got %d, want 12", len(encoded))
	}
	decoded := DecodeCompactPeers(encoded)
	if len(decoded) != 2 {
		t.Fatalf("decoded length: got %d, want 2", len(decoded))
	}
	for i, a := range decoded {
		if a != addrs[i] {
			t.Errorf("peer %d: got %q, want %q", i, a, addrs[i])
		}
	}
}

func TestFindNodeIterative(t *testing.T) {
	// Build a 3-hop mock network: dht → nodeA → nodeB → nodeC
	// nodeC is closest to target.
	target := NodeID{0x01}

	nodeC := newMockDHTNode(t, NodeID{0x01, 0x01}, nil)
	defer nodeC.close()

	nodeBID := NodeID{0x02}
	nodeB := newMockDHTNode(t, nodeBID, EncodeCompactNodes([]Node{
		{ID: nodeC.id, Addr: *nodeC.addr()},
	}))
	defer nodeB.close()

	nodeAID := NodeID{0x04}
	nodeA := newMockDHTNode(t, nodeAID, EncodeCompactNodes([]Node{
		{ID: nodeB.id, Addr: *nodeB.addr()},
	}))
	defer nodeA.close()

	conn, _ := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	dht := New(conn)
	defer dht.Transport.Close()
	go dht.Transport.Listen(nil)

	// Seed the routing table with nodeA.
	dht.Table.Insert(Node{ID: nodeAID, Addr: *nodeA.addr()})

	result := dht.FindNode(target)
	if len(result) == 0 {
		t.Fatal("FindNode returned no nodes")
	}

	// nodeC should be in the results (closest to target).
	found := false
	for _, n := range result {
		if n.ID == nodeC.id {
			found = true
			break
		}
	}
	if !found {
		t.Error("nodeC not found in results")
	}
}

func TestFindNodeTimeout(t *testing.T) {
	// One responsive node, one dead node.
	goodID := NodeID{0x10}
	goodNode := newMockDHTNode(t, goodID, nil)
	defer goodNode.close()

	conn, _ := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	dht := New(conn)
	defer dht.Transport.Close()
	go dht.Transport.Listen(nil)

	// Insert the good node and a dead one (port 1 — won't respond).
	dht.Table.Insert(Node{ID: goodID, Addr: *goodNode.addr()})
	dht.Table.Insert(Node{ID: NodeID{0x20}, Addr: net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 1}})

	// Should still succeed with the good node.
	result := dht.FindNode(NodeID{0x15})
	if len(result) == 0 {
		t.Fatal("FindNode returned no nodes despite one good node")
	}
}

func TestFindNodeConcurrency(t *testing.T) {
	// Verify multiple concurrent FindNode lookups don't interfere.
	var mocks []*mockDHTNode
	for i := byte(1); i <= 5; i++ {
		m := newMockDHTNode(t, NodeID{i}, nil)
		defer m.close()
		mocks = append(mocks, m)
	}

	conn, _ := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	dht := New(conn)
	defer dht.Transport.Close()
	go dht.Transport.Listen(nil)

	for _, m := range mocks {
		dht.Table.Insert(Node{ID: m.id, Addr: *m.addr()})
	}

	var wg sync.WaitGroup
	for i := byte(0); i < 3; i++ {
		wg.Add(1)
		go func(target NodeID) {
			defer wg.Done()
			dht.FindNode(target)
		}(NodeID{0xA0 + i})
	}

	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()

	select {
	case <-done:
	case <-time.After(15 * time.Second):
		t.Fatal("concurrent FindNode timed out")
	}
}
