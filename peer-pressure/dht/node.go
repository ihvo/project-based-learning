package dht

import (
	"encoding/binary"
	"fmt"
	"net"
	"sync"
	"time"

	"github.com/ihvo/peer-pressure/bencode"
)

const (
	alpha      = 3             // concurrency factor for iterative lookups
	queryTimeout = 5 * time.Second
)

// DHT is a BitTorrent DHT node implementing BEP 5.
type DHT struct {
	ID        NodeID
	Table     *RoutingTable
	Transport *Transport
}

// New creates a DHT node bound to the given UDP connection.
func New(conn *net.UDPConn) *DHT {
	id := RandomNodeID()
	return &DHT{
		ID:        id,
		Table:     NewRoutingTable(id),
		Transport: NewTransport(conn),
	}
}

// Ping sends a ping query and returns the remote node's ID.
func (d *DHT) Ping(addr *net.UDPAddr) (NodeID, error) {
	resp, err := d.Transport.Send(addr, Message{
		Type:   "q",
		Method: "ping",
		Args:   bencode.Dict{"id": bencode.String(d.ID[:])},
	}, queryTimeout)
	if err != nil {
		return NodeID{}, err
	}

	if resp.Type == "e" {
		return NodeID{}, fmt.Errorf("ping error: %v", resp.Error)
	}

	idStr, ok := resp.Reply["id"].(bencode.String)
	if !ok || len(idStr) != 20 {
		return NodeID{}, fmt.Errorf("ping: invalid id in response")
	}

	var id NodeID
	copy(id[:], idStr)

	d.Table.Insert(Node{ID: id, Addr: *addr})
	return id, nil
}

// FindNode performs an iterative find_node lookup for the target ID.
// Returns the closest nodes found across the network.
func (d *DHT) FindNode(target NodeID) []Node {
	return d.iterativeLookup(target, false)
}

// iterativeLookup is the core Kademlia iterative lookup algorithm.
// If getPeers is true, it also collects peer addresses along the way.
func (d *DHT) iterativeLookup(target NodeID, getPeers bool) []Node {
	// Seed with closest nodes from our routing table.
	seeds := d.Table.Closest(target, alpha)
	if len(seeds) == 0 {
		return nil
	}

	type queryResult struct {
		from  Node
		nodes []Node
	}

	queried := make(map[NodeID]bool)
	queried[d.ID] = true // don't query ourselves

	// Shortlist of closest nodes seen.
	shortlist := make(map[NodeID]Node)
	for _, n := range seeds {
		shortlist[n.ID] = n
	}

	for {
		// Pick up to alpha unqueried nodes from the shortlist, sorted by distance.
		var candidates []Node
		for _, n := range shortlist {
			if !queried[n.ID] {
				candidates = append(candidates, n)
			}
		}
		if len(candidates) == 0 {
			break
		}

		// Sort by distance and take alpha.
		sortByDistance(candidates, target)
		if len(candidates) > alpha {
			candidates = candidates[:alpha]
		}

		// Query them concurrently.
		results := make(chan queryResult, len(candidates))
		var wg sync.WaitGroup
		for _, c := range candidates {
			queried[c.ID] = true
			wg.Add(1)
			go func(n Node) {
				defer wg.Done()
				nodes, err := d.sendFindNode(n, target)
				if err != nil {
					return
				}
				results <- queryResult{from: n, nodes: nodes}
			}(c)
		}

		go func() { wg.Wait(); close(results) }()

		added := false
		for r := range results {
			d.Table.Insert(r.from)
			for _, n := range r.nodes {
				if _, exists := shortlist[n.ID]; !exists {
					shortlist[n.ID] = n
					d.Table.Insert(n)
					added = true
				}
			}
		}

		if !added {
			break // no closer nodes discovered
		}
	}

	// Return sorted shortlist.
	var result []Node
	for _, n := range shortlist {
		result = append(result, n)
	}
	sortByDistance(result, target)
	if len(result) > bucketSize {
		result = result[:bucketSize]
	}
	return result
}

// sendFindNode sends a find_node query to a single node.
func (d *DHT) sendFindNode(n Node, target NodeID) ([]Node, error) {
	resp, err := d.Transport.Send(&n.Addr, Message{
		Type:   "q",
		Method: "find_node",
		Args: bencode.Dict{
			"id":     bencode.String(d.ID[:]),
			"target": bencode.String(target[:]),
		},
	}, queryTimeout)
	if err != nil {
		return nil, err
	}

	if resp.Type == "e" {
		return nil, fmt.Errorf("find_node error: %v", resp.Error)
	}

	nodesStr, ok := resp.Reply["nodes"].(bencode.String)
	if !ok {
		return nil, fmt.Errorf("find_node: missing nodes in response")
	}

	return DecodeCompactNodes([]byte(nodesStr)), nil
}

// sortByDistance sorts nodes by XOR distance to target (ascending).
func sortByDistance(nodes []Node, target NodeID) {
	for i := 1; i < len(nodes); i++ {
		for j := i; j > 0; j-- {
			di := XOR(nodes[j].ID, target)
			dj := XOR(nodes[j-1].ID, target)
			if compareDist(di, dj) < 0 {
				nodes[j], nodes[j-1] = nodes[j-1], nodes[j]
			} else {
				break
			}
		}
	}
}

// EncodeCompactNodes encodes a list of nodes into the compact format.
// Each node: 20-byte ID + 4-byte IPv4 + 2-byte port = 26 bytes.
func EncodeCompactNodes(nodes []Node) []byte {
	buf := make([]byte, 26*len(nodes))
	for i, n := range nodes {
		off := i * 26
		copy(buf[off:], n.ID[:])
		ip4 := n.Addr.IP.To4()
		if ip4 != nil {
			copy(buf[off+20:], ip4)
		}
		binary.BigEndian.PutUint16(buf[off+24:], uint16(n.Addr.Port))
	}
	return buf
}

// DecodeCompactNodes parses the compact node format (26 bytes per node).
func DecodeCompactNodes(data []byte) []Node {
	var nodes []Node
	for len(data) >= 26 {
		var id NodeID
		copy(id[:], data[:20])
		ip := net.IPv4(data[20], data[21], data[22], data[23])
		port := binary.BigEndian.Uint16(data[24:26])
		nodes = append(nodes, Node{
			ID:   id,
			Addr: net.UDPAddr{IP: ip, Port: int(port)},
		})
		data = data[26:]
	}
	return nodes
}

// EncodeCompactPeers encodes peer addresses ("ip:port") into compact format.
// Each peer: 4-byte IPv4 + 2-byte port = 6 bytes.
func EncodeCompactPeers(addrs []string) []byte {
	var buf []byte
	for _, addr := range addrs {
		host, portStr, err := net.SplitHostPort(addr)
		if err != nil {
			continue
		}
		ip := net.ParseIP(host).To4()
		if ip == nil {
			continue
		}
		var portBuf [2]byte
		port := 0
		fmt.Sscanf(portStr, "%d", &port)
		binary.BigEndian.PutUint16(portBuf[:], uint16(port))
		buf = append(buf, ip...)
		buf = append(buf, portBuf[:]...)
	}
	return buf
}

// DecodeCompactPeers parses the compact peer format (6 bytes per peer).
func DecodeCompactPeers(data []byte) []string {
	var addrs []string
	for len(data) >= 6 {
		ip := net.IPv4(data[0], data[1], data[2], data[3])
		port := binary.BigEndian.Uint16(data[4:6])
		addrs = append(addrs, fmt.Sprintf("%s:%d", ip, port))
		data = data[6:]
	}
	return addrs
}
