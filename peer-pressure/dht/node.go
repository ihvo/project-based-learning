package dht

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"net"
	"sync"
	"time"

	"github.com/ihvo/peer-pressure/bencode"
)

const (
	alpha        = 3             // concurrency factor for iterative lookups
	queryTimeout = 5 * time.Second
)

// DefaultBootstrapNodes are well-known DHT bootstrap nodes.
var DefaultBootstrapNodes = []string{
	"router.bittorrent.com:6881",
	"dht.transmissionbt.com:6881",
	"router.utorrent.com:6881",
}

// DHT is a BitTorrent DHT node implementing BEP 5.
type DHT struct {
	ID        NodeID
	Table     *RoutingTable
	Transport *Transport
	ReadOnly  bool              // BEP 43: set ro=1 in all outgoing queries
	tokens    map[NodeID]string // cached tokens from get_peers responses
	tokensMu  sync.Mutex
}

// New creates a DHT node bound to the given UDP connection.
func New(conn *net.UDPConn) *DHT {
	id := RandomNodeID()
	return &DHT{
		ID:        id,
		Table:     NewRoutingTable(id),
		Transport: NewTransport(conn),
		tokens:    make(map[NodeID]string),
	}
}

// newQuery builds a KRPC query message, setting ro=1 if we're read-only (BEP 43).
func (d *DHT) newQuery(method string, args bencode.Dict) Message {
	return Message{
		Type:     "q",
		Method:   method,
		Args:     args,
		ReadOnly: d.ReadOnly,
	}
}

// Ping sends a ping query and returns the remote node's ID.
func (d *DHT) Ping(addr *net.UDPAddr) (NodeID, error) {
	resp, err := d.Transport.Send(addr, d.newQuery("ping", bencode.Dict{
		"id": bencode.String(d.ID[:]),
	}), queryTimeout)
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

// GetPeers performs an iterative get_peers lookup for the given info hash.
// Returns peer addresses (ip:port strings) discovered from the DHT.
func (d *DHT) GetPeers(infoHash [20]byte) []string {
	target := NodeID(infoHash)

	seeds := d.Table.Closest(target, alpha)
	if len(seeds) == 0 {
		return nil
	}

	type queryResult struct {
		from  Node
		nodes []Node
		peers []string
		token string
	}

	queried := make(map[NodeID]bool)
	queried[d.ID] = true

	shortlist := make(map[NodeID]Node)
	for _, n := range seeds {
		shortlist[n.ID] = n
	}

	seen := make(map[string]bool)
	var allPeers []string

	for {
		var candidates []Node
		for _, n := range shortlist {
			if !queried[n.ID] {
				candidates = append(candidates, n)
			}
		}
		if len(candidates) == 0 {
			break
		}

		sortByDistance(candidates, target)
		if len(candidates) > alpha {
			candidates = candidates[:alpha]
		}

		results := make(chan queryResult, len(candidates))
		var wg sync.WaitGroup
		for _, c := range candidates {
			queried[c.ID] = true
			wg.Add(1)
			go func(n Node) {
				defer wg.Done()
				r, err := d.sendGetPeers(n, infoHash)
				if err != nil {
					return
				}
				results <- r
			}(c)
		}
		go func() { wg.Wait(); close(results) }()

		added := false
		for r := range results {
			d.Table.Insert(r.from)

			if r.token != "" {
				d.tokensMu.Lock()
				d.tokens[r.from.ID] = r.token
				d.tokensMu.Unlock()
			}

			for _, p := range r.peers {
				if !seen[p] {
					seen[p] = true
					allPeers = append(allPeers, p)
				}
			}

			for _, n := range r.nodes {
				if _, exists := shortlist[n.ID]; !exists {
					shortlist[n.ID] = n
					d.Table.Insert(n)
					added = true
				}
			}
		}

		if !added && len(allPeers) > 0 {
			break // found peers, no new closer nodes
		}
		if !added {
			break
		}
	}

	return allPeers
}

// AnnouncePeer announces to DHT nodes that we are a peer for the given info hash.
// Uses tokens cached from previous GetPeers calls.
func (d *DHT) AnnouncePeer(infoHash [20]byte, port int) error {
	target := NodeID(infoHash)
	closest := d.Table.Closest(target, bucketSize)

	var announced int
	for _, n := range closest {
		d.tokensMu.Lock()
		token, ok := d.tokens[n.ID]
		d.tokensMu.Unlock()
		if !ok {
			continue
		}

		_, err := d.Transport.Send(&n.Addr, d.newQuery("announce_peer", bencode.Dict{
			"id":        bencode.String(d.ID[:]),
			"info_hash": bencode.String(infoHash[:]),
			"port":      bencode.Int(port),
			"token":     bencode.String(token),
		}), queryTimeout)
		if err == nil {
			announced++
		}
	}

	if announced == 0 && len(closest) > 0 {
		return fmt.Errorf("announce_peer: no nodes accepted (tried %d)", len(closest))
	}
	return nil
}

// sendGetPeers sends a get_peers query to a single node.
func (d *DHT) sendGetPeers(n Node, infoHash [20]byte) (struct {
	from  Node
	nodes []Node
	peers []string
	token string
}, error) {
	type result = struct {
		from  Node
		nodes []Node
		peers []string
		token string
	}

	resp, err := d.Transport.Send(&n.Addr, d.newQuery("get_peers", bencode.Dict{
		"id":        bencode.String(d.ID[:]),
		"info_hash": bencode.String(infoHash[:]),
	}), queryTimeout)
	if err != nil {
		return result{}, err
	}
	if resp.Type == "e" {
		return result{}, fmt.Errorf("get_peers error: %v", resp.Error)
	}

	r := result{from: n}

	// Extract token.
	if tok, ok := resp.Reply["token"].(bencode.String); ok {
		r.token = string(tok)
	}

	// Peers (values) — list of compact 6-byte peer addresses.
	if values, ok := resp.Reply["values"].(bencode.List); ok {
		for _, v := range values {
			if s, ok := v.(bencode.String); ok {
				for _, p := range DecodeCompactPeers([]byte(s)) {
					r.peers = append(r.peers, p)
				}
			}
		}
	}

	// BEP 32: IPv6 peers in values6 key.
	if values6, ok := resp.Reply["values6"].(bencode.List); ok {
		for _, v := range values6 {
			if s, ok := v.(bencode.String); ok {
				for _, p := range DecodeCompactPeers6([]byte(s)) {
					r.peers = append(r.peers, p)
				}
			}
		}
	}

	// Closer nodes.
	if nodesStr, ok := resp.Reply["nodes"].(bencode.String); ok {
		r.nodes = append(r.nodes, DecodeCompactNodes([]byte(nodesStr))...)
	}
	// BEP 32: IPv6 closer nodes.
	if nodes6Str, ok := resp.Reply["nodes6"].(bencode.String); ok {
		r.nodes = append(r.nodes, DecodeCompactNodes6([]byte(nodes6Str))...)
	}

	return r, nil
}

// iterativeLookup is the core Kademlia iterative lookup algorithm.
// Used by FindNode.
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
	resp, err := d.Transport.Send(&n.Addr, d.newQuery("find_node", bencode.Dict{
		"id":     bencode.String(d.ID[:]),
		"target": bencode.String(target[:]),
	}), queryTimeout)
	if err != nil {
		return nil, err
	}

	if resp.Type == "e" {
		return nil, fmt.Errorf("find_node error: %v", resp.Error)
	}

	var nodes []Node
	if nodesStr, ok := resp.Reply["nodes"].(bencode.String); ok {
		nodes = append(nodes, DecodeCompactNodes([]byte(nodesStr))...)
	}
	// BEP 32: IPv6 nodes in nodes6 key.
	if nodes6Str, ok := resp.Reply["nodes6"].(bencode.String); ok {
		nodes = append(nodes, DecodeCompactNodes6([]byte(nodes6Str))...)
	}
	if len(nodes) == 0 {
		return nil, fmt.Errorf("find_node: no nodes in response")
	}

	return nodes, nil
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

// Bootstrap resolves and pings well-known DHT nodes, then runs find_node
// on our own ID to populate the routing table with nearby nodes.
func (d *DHT) Bootstrap(addrs []string) error {
	type resolved struct {
		addr *net.UDPAddr
		id   NodeID
	}

	results := make(chan resolved, len(addrs))
	var wg sync.WaitGroup

	for _, addr := range addrs {
		wg.Add(1)
		go func(hostport string) {
			defer wg.Done()
			udpAddr, err := net.ResolveUDPAddr("udp4", hostport)
			if err != nil {
				return
			}
			id, err := d.Ping(udpAddr)
			if err != nil {
				return
			}
			d.Table.Insert(Node{ID: id, Addr: *udpAddr})
			results <- resolved{udpAddr, id}
		}(addr)
	}
	go func() { wg.Wait(); close(results) }()

	count := 0
	for range results {
		count++
	}
	if count == 0 {
		return fmt.Errorf("bootstrap: all %d nodes unreachable", len(addrs))
	}

	// Populate the table by finding nodes near ourselves.
	d.FindNode(d.ID)
	return nil
}

// Maintain periodically refreshes stale routing table buckets by running
// find_node with a random ID in each bucket's range. Cancel the context
// to stop.
func (d *DHT) Maintain(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			d.refreshBuckets()
		}
	}
}

// refreshBuckets runs find_node on a random target in each non-empty
// bucket's ID range to discover fresh nodes.
func (d *DHT) refreshBuckets() {
	d.Table.mu.RLock()
	var stale []int
	for i, b := range d.Table.buckets {
		if len(b) > 0 {
			stale = append(stale, i)
		}
	}
	d.Table.mu.RUnlock()

	for _, idx := range stale {
		target := randomIDInBucket(d.Table.own, idx)
		d.FindNode(target)
	}
}

// randomIDInBucket generates a random node ID that falls into the given
// bucket index relative to our own ID.
func randomIDInBucket(own NodeID, bucketIdx int) NodeID {
	var target NodeID
	rand.Read(target[:])

	// The bucket index means XOR(own, target) has its highest bit at position bucketIdx.
	// We need: bit bucketIdx set, all higher bits cleared.
	dist := XOR(own, target)

	byteIdx := (159 - bucketIdx) / 8
	bitIdx := uint(7 - (159-bucketIdx)%8)

	// Clear all bytes before the target byte.
	for i := 0; i < byteIdx; i++ {
		dist[i] = 0
	}
	// In the target byte: set the specific bit, clear higher bits.
	dist[byteIdx] = dist[byteIdx] & ((1 << (bitIdx + 1)) - 1)
	dist[byteIdx] |= 1 << bitIdx

	// XOR back to get the actual target ID.
	for i := range 20 {
		target[i] = own[i] ^ dist[i]
	}
	return target
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

// EncodeCompactNodes6 encodes nodes into the BEP 32 IPv6 compact format.
// Each node: 20-byte ID + 16-byte IPv6 + 2-byte port = 38 bytes.
func EncodeCompactNodes6(nodes []Node) []byte {
	buf := make([]byte, 38*len(nodes))
	for i, n := range nodes {
		off := i * 38
		copy(buf[off:], n.ID[:])
		ip6 := n.Addr.IP.To16()
		if ip6 != nil {
			copy(buf[off+20:], ip6)
		}
		binary.BigEndian.PutUint16(buf[off+36:], uint16(n.Addr.Port))
	}
	return buf
}

// DecodeCompactNodes6 parses the BEP 32 IPv6 compact node format (38 bytes per node).
func DecodeCompactNodes6(data []byte) []Node {
	var nodes []Node
	for len(data) >= 38 {
		var id NodeID
		copy(id[:], data[:20])
		ip := make(net.IP, net.IPv6len)
		copy(ip, data[20:36])
		port := binary.BigEndian.Uint16(data[36:38])
		nodes = append(nodes, Node{
			ID:   id,
			Addr: net.UDPAddr{IP: ip, Port: int(port)},
		})
		data = data[38:]
	}
	return nodes
}

// DecodeCompactPeers6 parses the BEP 32 IPv6 compact peer format (18 bytes per peer).
func DecodeCompactPeers6(data []byte) []string {
	var addrs []string
	for len(data) >= 18 {
		ip := make(net.IP, net.IPv6len)
		copy(ip, data[:16])
		port := binary.BigEndian.Uint16(data[16:18])
		addrs = append(addrs, net.JoinHostPort(ip.String(), fmt.Sprintf("%d", port)))
		data = data[18:]
	}
	return addrs
}

// SampleInfohashesResult holds the response to a BEP 51 sample_infohashes query.
type SampleInfohashesResult struct {
	Samples  [][20]byte // sampled infohash keys
	Num      int        // total number of infohashes in storage
	Interval int        // seconds until a new sample is available
	Nodes    []Node     // close nodes for iterative traversal
}

// SampleInfohashes sends a BEP 51 sample_infohashes query to the given node.
func (d *DHT) SampleInfohashes(addr *net.UDPAddr, target NodeID) (*SampleInfohashesResult, error) {
	resp, err := d.Transport.Send(addr, d.newQuery("sample_infohashes", bencode.Dict{
		"id":     bencode.String(d.ID[:]),
		"target": bencode.String(target[:]),
	}), queryTimeout)
	if err != nil {
		return nil, err
	}
	if resp.Type == "e" {
		return nil, fmt.Errorf("sample_infohashes error: %v", resp.Error)
	}

	result := &SampleInfohashesResult{}

	// Parse samples (concatenated 20-byte hashes).
	if samplesRaw, ok := resp.Reply["samples"].(bencode.String); ok {
		data := []byte(samplesRaw)
		for len(data) >= 20 {
			var h [20]byte
			copy(h[:], data[:20])
			result.Samples = append(result.Samples, h)
			data = data[20:]
		}
	}

	// Parse num (total count).
	if numVal, ok := resp.Reply["num"].(bencode.Int); ok {
		result.Num = int(numVal)
	}

	// Parse interval.
	if ivVal, ok := resp.Reply["interval"].(bencode.Int); ok {
		result.Interval = int(ivVal)
	}

	// Parse nodes for iterative traversal.
	if nodesRaw, ok := resp.Reply["nodes"].(bencode.String); ok {
		result.Nodes = DecodeCompactNodes([]byte(nodesRaw))
	}
	if nodes6Raw, ok := resp.Reply["nodes6"].(bencode.String); ok {
		result.Nodes = append(result.Nodes, DecodeCompactNodes6([]byte(nodes6Raw))...)
	}

	return result, nil
}

// DecodeSamples parses concatenated 20-byte infohashes from a BEP 51 samples field.
func DecodeSamples(data []byte) [][20]byte {
	var hashes [][20]byte
	for len(data) >= 20 {
		var h [20]byte
		copy(h[:], data[:20])
		hashes = append(hashes, h)
		data = data[20:]
	}
	return hashes
}

// EncodeSamples concatenates infohashes into a BEP 51 samples field.
func EncodeSamples(hashes [][20]byte) []byte {
	buf := make([]byte, 0, len(hashes)*20)
	for _, h := range hashes {
		buf = append(buf, h[:]...)
	}
	return buf
}
