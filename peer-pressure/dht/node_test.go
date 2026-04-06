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
	// getPeersValues is a list of compact 6-byte peer strings for get_peers.
	getPeersValues []string
	// getPeersToken is the token to return for get_peers.
	getPeersToken string
	// announceReceived tracks announce_peer calls.
	announceReceived []announceCall
	announceMu       sync.Mutex
	done             chan struct{}
}

type announceCall struct {
	InfoHash [20]byte
	Port     int
	Token    string
}

type mockOpts struct {
	findNodeResponse []byte
	getPeersValues   []string
	getPeersToken    string
}

func newMockDHTNode(t *testing.T, id NodeID, opts *mockOpts) *mockDHTNode {
	t.Helper()
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatalf("listen mock: %v", err)
	}
	m := &mockDHTNode{
		id:   id,
		conn: conn,
		done: make(chan struct{}),
	}
	if opts != nil {
		m.findNodeResponse = opts.findNodeResponse
		m.getPeersToken = opts.getPeersToken
		m.getPeersValues = opts.getPeersValues
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
		case "get_peers":
			reply := bencode.Dict{
				"id":    bencode.String(m.id[:]),
				"token": bencode.String(m.getPeersToken),
			}
			if len(m.getPeersValues) > 0 {
				vals := make(bencode.List, len(m.getPeersValues))
				for i, v := range m.getPeersValues {
					vals[i] = bencode.String(v)
				}
				reply["values"] = vals
			} else {
				reply["nodes"] = bencode.String(m.findNodeResponse)
			}
			resp = Message{TxnID: msg.TxnID, Type: "r", Reply: reply}
		case "announce_peer":
			var ih [20]byte
			if s, ok := msg.Args["info_hash"].(bencode.String); ok {
				copy(ih[:], s)
			}
			port := 0
			if p, ok := msg.Args["port"].(bencode.Int); ok {
				port = int(p)
			}
			tok := ""
			if t, ok := msg.Args["token"].(bencode.String); ok {
				tok = string(t)
			}
			m.announceMu.Lock()
			m.announceReceived = append(m.announceReceived, announceCall{ih, port, tok})
			m.announceMu.Unlock()
			resp = Message{
				TxnID: msg.TxnID,
				Type:  "r",
				Reply: bencode.Dict{"id": bencode.String(m.id[:])},
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
	nodeB := newMockDHTNode(t, nodeBID, &mockOpts{
		findNodeResponse: EncodeCompactNodes([]Node{
			{ID: nodeC.id, Addr: *nodeC.addr()},
		}),
	})
	defer nodeB.close()

	nodeAID := NodeID{0x04}
	nodeA := newMockDHTNode(t, nodeAID, &mockOpts{
		findNodeResponse: EncodeCompactNodes([]Node{
			{ID: nodeB.id, Addr: *nodeB.addr()},
		}),
	})
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

func TestGetPeers(t *testing.T) {
	infoHash := [20]byte{0xAB, 0xCD}

	// Mock node that returns peers for the info hash.
	mock := newMockDHTNode(t, NodeID{0x10}, &mockOpts{
		getPeersToken: "tok123",
		getPeersValues: []string{
			string(EncodeCompactPeers([]string{"10.0.0.1:6881"})),
			string(EncodeCompactPeers([]string{"10.0.0.2:6882"})),
		},
	})
	defer mock.close()

	conn, _ := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	dht := New(conn)
	defer dht.Transport.Close()
	go dht.Transport.Listen(nil)

	dht.Table.Insert(Node{ID: mock.id, Addr: *mock.addr()})

	peers := dht.GetPeers(infoHash)
	if len(peers) != 2 {
		t.Fatalf("GetPeers: got %d peers, want 2", len(peers))
	}

	// Check that token was cached.
	dht.tokensMu.Lock()
	tok, ok := dht.tokens[mock.id]
	dht.tokensMu.Unlock()
	if !ok || tok != "tok123" {
		t.Errorf("token not cached: got %q, ok=%v", tok, ok)
	}
}

func TestGetPeersNoResults(t *testing.T) {
	// Mock node returns nodes but no peers.
	mock := newMockDHTNode(t, NodeID{0x10}, &mockOpts{getPeersToken: "tok"})
	defer mock.close()

	conn, _ := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	dht := New(conn)
	defer dht.Transport.Close()
	go dht.Transport.Listen(nil)

	dht.Table.Insert(Node{ID: mock.id, Addr: *mock.addr()})

	peers := dht.GetPeers([20]byte{0xFF})
	if len(peers) != 0 {
		t.Errorf("expected 0 peers, got %d", len(peers))
	}
}

func TestAnnouncePeer(t *testing.T) {
	infoHash := [20]byte{0xAB}
	mock := newMockDHTNode(t, NodeID{0x10}, &mockOpts{
		getPeersToken: "secret_token",
		getPeersValues: []string{
			string(EncodeCompactPeers([]string{"10.0.0.1:6881"})),
		},
	})
	defer mock.close()

	conn, _ := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	dht := New(conn)
	defer dht.Transport.Close()
	go dht.Transport.Listen(nil)

	dht.Table.Insert(Node{ID: mock.id, Addr: *mock.addr()})

	// First get_peers to cache the token.
	dht.GetPeers(infoHash)

	// Now announce.
	err := dht.AnnouncePeer(infoHash, 6881)
	if err != nil {
		t.Fatalf("AnnouncePeer: %v", err)
	}

	// Give the mock a moment to process.
	time.Sleep(100 * time.Millisecond)

	mock.announceMu.Lock()
	calls := mock.announceReceived
	mock.announceMu.Unlock()

	if len(calls) != 1 {
		t.Fatalf("expected 1 announce call, got %d", len(calls))
	}
	if calls[0].InfoHash != infoHash {
		t.Errorf("announce info_hash mismatch")
	}
	if calls[0].Port != 6881 {
		t.Errorf("announce port: got %d, want 6881", calls[0].Port)
	}
	if calls[0].Token != "secret_token" {
		t.Errorf("announce token: got %q, want %q", calls[0].Token, "secret_token")
	}
}

func TestTokenCaching(t *testing.T) {
	// Two mock nodes with different tokens — all config at construction to avoid races.
	mock1 := newMockDHTNode(t, NodeID{0x10}, &mockOpts{
		getPeersToken:  "token_a",
		getPeersValues: []string{string(EncodeCompactPeers([]string{"1.2.3.4:80"}))},
	})
	defer mock1.close()

	mock2 := newMockDHTNode(t, NodeID{0x20}, &mockOpts{
		getPeersToken:  "token_b",
		getPeersValues: []string{string(EncodeCompactPeers([]string{"5.6.7.8:80"}))},
	})
	defer mock2.close()

	conn, _ := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	dht := New(conn)
	defer dht.Transport.Close()
	go dht.Transport.Listen(nil)

	dht.Table.Insert(Node{ID: mock1.id, Addr: *mock1.addr()})
	dht.Table.Insert(Node{ID: mock2.id, Addr: *mock2.addr()})

	dht.GetPeers([20]byte{0x15})

	dht.tokensMu.Lock()
	numTokens := len(dht.tokens)
	tokenSet := make(map[string]bool)
	for _, tok := range dht.tokens {
		tokenSet[tok] = true
	}
	dht.tokensMu.Unlock()

	if numTokens < 2 {
		t.Errorf("expected 2 cached tokens, got %d", numTokens)
	}
	if !tokenSet["token_a"] || !tokenSet["token_b"] {
		t.Errorf("expected tokens {token_a, token_b}, got %v", tokenSet)
	}
}
