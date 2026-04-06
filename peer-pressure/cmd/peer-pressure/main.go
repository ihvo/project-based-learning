package main

import (
	"context"
	"flag"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"

	"github.com/ihvo/peer-pressure/client"
	"github.com/ihvo/peer-pressure/dht"
	"github.com/ihvo/peer-pressure/download"
	"github.com/ihvo/peer-pressure/magnet"
	"github.com/ihvo/peer-pressure/peer"
	"github.com/ihvo/peer-pressure/seed"
	"github.com/ihvo/peer-pressure/torrent"
	"github.com/ihvo/peer-pressure/tracker"
)

var peerID = client.GeneratePeerID()

func main() {
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}

	cmd := os.Args[1]
	args := os.Args[2:]

	switch cmd {
	case "info":
		runInfo(args)
	case "peers":
		runPeers(args)
	case "download":
		runDownload(args)
	case "seed":
		runSeed(args)
	case "create":
		runCreate(args)
	case "version", "--version", "-v":
		fmt.Printf("peer-pressure %s\n", client.Version)
	case "help", "--help", "-h":
		printUsage()
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n\n", cmd)
		printUsage()
		os.Exit(1)
	}
}

func printUsage() {
	fmt.Fprintf(os.Stderr, `⚡ Peer Pressure — BitTorrent client v%s

Usage:
  peer-pressure <command> [options]

Commands:
  info       Parse and display .torrent file details
  peers      Announce to tracker and list peers in the swarm
  download   Download a torrent
  seed       Seed a torrent from local data
  create     Create a .torrent file from local data
  version    Print version
  help       Show this help

Run 'peer-pressure <command> -h' for command-specific help.
`, client.Version)
}

// --- info ---

func runInfo(args []string) {
	fs := flag.NewFlagSet("info", flag.ExitOnError)
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: peer-pressure info <file.torrent>\n\n")
		fmt.Fprintf(os.Stderr, "Parse and display .torrent file metadata.\n")
	}
	fs.Parse(args)

	if fs.NArg() < 1 {
		fs.Usage()
		os.Exit(1)
	}

	t, err := torrent.Load(fs.Arg(0))
	if err != nil {
		fatal("parse torrent: %v", err)
	}

	fmt.Print(t.String())
}

// --- peers ---

func runPeers(args []string) {
	fs := flag.NewFlagSet("peers", flag.ExitOnError)
	port := fs.Int("port", 6881, "port to announce")
	noDHT := fs.Bool("no-dht", false, "disable DHT peer discovery")
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: peer-pressure peers [options] <file.torrent>\n\n")
		fmt.Fprintf(os.Stderr, "Announce to the tracker and list peers.\n\n")
		fs.PrintDefaults()
	}
	fs.Parse(args)

	if fs.NArg() < 1 {
		fs.Usage()
		os.Exit(1)
	}

	t, err := torrent.Load(fs.Arg(0))
	if err != nil {
		fatal("parse torrent: %v", err)
	}

	peers := announceAll(t, uint16(*port))

	// Merge DHT peers (disabled for private torrents per BEP 27).
	if !*noDHT && !t.IsPrivate() {
		dhtPeers, node := discoverDHTPeers(t.InfoHash)
		if node != nil {
			node.Transport.Close()
		}
		seen := make(map[string]bool)
		for _, a := range peers {
			seen[a] = true
		}
		for _, a := range dhtPeers {
			if !seen[a] {
				seen[a] = true
				peers = append(peers, a)
			}
		}
	}

	fmt.Printf("Total unique peers: %d\n", len(peers))
	for _, addr := range peers {
		fmt.Printf("  %s\n", addr)
	}
}

// --- download ---

func runDownload(args []string) {
	fs := flag.NewFlagSet("download", flag.ExitOnError)
	output := fs.String("o", "", "output file path (default: torrent name)")
	port := fs.Int("port", 6881, "port to announce")
	maxPeers := fs.Int("peers", 30, "max concurrent peer connections")
	quiet := fs.Bool("q", false, "suppress progress display")
	noDHT := fs.Bool("no-dht", false, "disable DHT peer discovery")
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: peer-pressure download [options] <file.torrent | magnet:?...>\n\n")
		fmt.Fprintf(os.Stderr, "Download a torrent file or magnet link.\n\n")
		fs.PrintDefaults()
	}
	fs.Parse(args)

	if fs.NArg() < 1 {
		fs.Usage()
		os.Exit(1)
	}

	arg := fs.Arg(0)

	var t *torrent.Torrent
	var addrs []string

	if strings.HasPrefix(arg, "magnet:?") {
		t, addrs = resolveMagnet(arg, uint16(*port))
	} else {
		var err error
		t, err = torrent.Load(arg)
		if err != nil {
			fatal("parse torrent: %v", err)
		}
		addrs = announceAll(t, uint16(*port))
	}

	// DHT peer discovery — runs concurrently so it doesn't block download start.
	type dhtResult struct {
		peers []string
		node  *dht.DHT
	}
	var dhtCh chan dhtResult
	if !*noDHT && !t.IsPrivate() {
		dhtCh = make(chan dhtResult, 1)
		go func() {
			peers, node := discoverDHTPeers(t.InfoHash)
			dhtCh <- dhtResult{peers, node}
		}()
	}

	if len(addrs) == 0 && dhtCh != nil && len(t.URLList) == 0 {
		// No tracker peers and no webseeds — must wait for DHT.
		fmt.Printf("No tracker peers, waiting for DHT...\n")
		r := <-dhtCh
		dhtCh = nil
		addrs = r.peers
		if r.node != nil {
			defer r.node.Transport.Close()
		}
	}

	if len(addrs) == 0 && len(t.URLList) == 0 {
		fatal("no peers found in swarm")
	}

	// Collect DHT result if it finished while we were setting up.
	var dhtNode *dht.DHT
	if dhtCh != nil {
		select {
		case r := <-dhtCh:
			dhtNode = r.node
			if dhtNode != nil {
				defer dhtNode.Transport.Close()
			}
			seen := make(map[string]bool)
			for _, a := range addrs {
				seen[a] = true
			}
			for _, a := range r.peers {
				if !seen[a] {
					addrs = append(addrs, a)
				}
			}
		default:
			// DHT still running — proceed with tracker peers.
		}
	}

	outPath := *output
	if outPath == "" {
		outPath = t.Name
	}

	if len(t.URLList) > 0 {
		fmt.Printf("Web seeds: %d\n", len(t.URLList))
	}

	fmt.Printf("Found %d peers, downloading %s (%d pieces)...\n",
		len(addrs), t.Name, len(t.Pieces))

	err := download.File(context.Background(), download.Config{
		Torrent:    t,
		Peers:      addrs,
		WebSeeds:   t.URLList,
		OutputPath: outPath,
		PeerID:     peerID,
		MaxPeers:   *maxPeers,
		Quiet:      *quiet,
	})
	if err != nil {
		fatal("download: %v", err)
	}

	// Announce completion to DHT.
	if dhtNode != nil {
		dhtNode.AnnouncePeer(t.InfoHash, *port)
	}

	fmt.Printf("\nDone! Saved to %s\n", outPath)
}

// discoverDHTPeers starts a DHT node, bootstraps, and looks up peers.
func discoverDHTPeers(infoHash [20]byte) ([]string, *dht.DHT) {
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4zero})
	if err != nil {
		fmt.Printf("DHT: failed to bind UDP: %v\n", err)
		return nil, nil
	}

	node := dht.New(conn)
	go node.Transport.Listen(nil)

	fmt.Printf("DHT: bootstrapping...\n")
	if err := node.Bootstrap(dht.DefaultBootstrapNodes); err != nil {
		fmt.Printf("DHT: bootstrap failed: %v\n", err)
		node.Transport.Close()
		return nil, nil
	}
	fmt.Printf("DHT: routing table has %d nodes\n", node.Table.Len())

	peers := node.GetPeers(infoHash)
	fmt.Printf("DHT: found %d peers\n", len(peers))
	return peers, node
}

// resolveMagnet parses a magnet URI, finds peers, fetches metadata, and
// returns a fully populated Torrent and peer list.
func resolveMagnet(uri string, port uint16) (*torrent.Torrent, []string) {
	link, err := magnet.Parse(uri)
	if err != nil {
		fatal("parse magnet: %v", err)
	}

	fmt.Printf("Magnet: %s\n", link.Name)
	fmt.Printf("Info hash: %x\n", link.InfoHash)

	// Announce to trackers from the magnet link to find peers.
	// If the magnet has no trackers, try well-known public ones.
	trackers := link.Trackers
	if len(trackers) == 0 {
		trackers = []string{
			"udp://tracker.opentrackr.org:1337/announce",
			"udp://open.stealth.si:80/announce",
			"udp://tracker.openbittorrent.com:6969/announce",
			"udp://exodus.desync.com:6969/announce",
		}
		fmt.Printf("No trackers in magnet, trying %d public trackers\n", len(trackers))
	}
	addrs := announceAllTrackers(trackers, link.InfoHash, port)
	if len(addrs) == 0 {
		fatal("no peers found (magnet had %d trackers)", len(link.Trackers))
	}

	// Fetch metadata from the first peer that supports ut_metadata.
	fmt.Printf("Fetching metadata from peers...\n")
	metadata := fetchMetadataFromPeers(addrs, link.InfoHash)
	if metadata == nil {
		fatal("could not fetch metadata from any peer")
	}

	// Parse the info dict into a Torrent.
	t, err := torrent.FromInfoDict(metadata, link.InfoHash, trackers)
	if err != nil {
		fatal("parse metadata: %v", err)
	}

	fmt.Printf("Got metadata: %s (%d pieces)\n", t.Name, len(t.Pieces))
	return t, addrs
}

// fetchMetadataFromPeers tries each peer until one provides valid metadata.
func fetchMetadataFromPeers(addrs []string, infoHash [20]byte) []byte {
	for _, addr := range addrs {
		conn, err := peer.Dial(addr, infoHash, peerID)
		if err != nil {
			continue
		}

		if !conn.SupportsExtensions() {
			conn.Close()
			continue
		}

		err = conn.ExchangeExtHandshake(
			map[string]int{"ut_metadata": 1}, 0, client.UserAgent())
		if err != nil {
			conn.Close()
			continue
		}

		if conn.PeerExtensions.M["ut_metadata"] == 0 {
			conn.Close()
			continue
		}

		data, err := magnet.FetchMetadata(conn, infoHash)
		conn.Close()
		if err != nil {
			fmt.Printf("  metadata from %s: %v\n", addr, err)
			continue
		}

		fmt.Printf("  got metadata from %s (%d bytes)\n", addr, len(data))
		return data
	}
	return nil
}

// announceAllTrackers queries trackers using an info hash directly.
func announceAllTrackers(trackers []string, infoHash [20]byte, port uint16) []string {
	type result struct {
		resp *tracker.Response
		err  error
	}
	ch := make(chan result, len(trackers))

	for _, trackerURL := range trackers {
		go func(url string) {
			fmt.Printf("Announcing to %s...\n", url)
			resp, err := tracker.Announce(url, tracker.AnnounceParams{
				InfoHash: infoHash,
				PeerID:   peerID,
				Port:     port,
				Left:     0,
				Event:    "started",
				NumWant:  200,
			})
			ch <- result{resp, err}
		}(trackerURL)
	}

	seen := make(map[string]bool)
	var addrs []string
	for range len(trackers) {
		r := <-ch
		if r.err != nil {
			fmt.Printf("  tracker error: %v\n", r.err)
			continue
		}
		fmt.Printf("  got %d peers\n", len(r.resp.Peers))
		for _, p := range r.resp.Peers {
			a := p.Addr()
			if !seen[a] {
				seen[a] = true
				addrs = append(addrs, a)
			}
		}
	}
	return addrs
}

// announceAll queries every tracker in the torrent concurrently and merges peers.
func announceAll(t *torrent.Torrent, port uint16) []string {
	trackers := t.Trackers()

	type result struct {
		url   string
		resp  *tracker.Response
		err   error
	}
	ch := make(chan result, len(trackers))

	for _, trackerURL := range trackers {
		go func(url string) {
			fmt.Printf("Announcing to %s...\n", url)
			resp, err := tracker.Announce(url, tracker.AnnounceParams{
				InfoHash: t.InfoHash,
				PeerID:   peerID,
				Port:     port,
				Left:     int64(t.TotalLength()),
				Event:    "started",
				NumWant:  200,
			})
			ch <- result{url, resp, err}
		}(trackerURL)
	}

	seen := make(map[string]bool)
	var addrs []string

	for range len(trackers) {
		r := <-ch
		if r.err != nil {
			fmt.Printf("  tracker error: %v\n", r.err)
			continue
		}
		fmt.Printf("  got %d peers (seeders: %d, leechers: %d)\n",
			len(r.resp.Peers), r.resp.Complete, r.resp.Incomplete)
		for _, p := range r.resp.Peers {
			addr := p.Addr()
			if !seen[addr] {
				seen[addr] = true
				addrs = append(addrs, addr)
			}
		}
	}
	return addrs
}

func fatal(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "error: "+format+"\n", args...)
	os.Exit(1)
}

// --- seed ---

func runSeed(args []string) {
	fs := flag.NewFlagSet("seed", flag.ExitOnError)
	listen := fs.String("listen", ":6881", "listen address")
	maxConns := fs.Int("max-conns", 50, "max concurrent connections")
	uploadSlots := fs.Int("upload-slots", 4, "number of unchoke slots")
	fs.Parse(args)

	if fs.NArg() < 2 {
		fmt.Fprintln(os.Stderr, "usage: peer-pressure seed <file.torrent> <data_path> [--listen :6881]")
		os.Exit(1)
	}

	torrentPath := fs.Arg(0)
	dataPath := fs.Arg(1)

	// Parse .torrent file.
	raw, err := os.ReadFile(torrentPath)
	if err != nil {
		fatal("read torrent: %v", err)
	}
	t, err := torrent.Parse(raw)
	if err != nil {
		fatal("parse torrent: %v", err)
	}

	fmt.Printf("⚡ Peer Pressure — Seeding\n")
	fmt.Printf("  Torrent: %s\n", t.Name)
	fmt.Printf("  Pieces:  %d × %d bytes\n", len(t.Pieces), t.PieceLength)
	fmt.Printf("  Listen:  %s\n\n", *listen)

	// Verify data integrity.
	fmt.Print("Verifying data... ")
	result, err := seed.VerifyData(t, dataPath)
	if err != nil {
		fatal("verify: %v", err)
	}
	fmt.Printf("%d/%d pieces valid\n", result.ValidPieces, result.TotalPieces)
	if len(result.InvalidPieces) > 0 {
		fatal("%d invalid pieces — cannot seed", len(result.InvalidPieces))
	}

	// Create and run seeder.
	seeder, err := seed.New(seed.Config{
		Torrent:     t,
		DataPath:    dataPath,
		PeerID:      peerID,
		ListenAddr:  *listen,
		MaxConns:    *maxConns,
		UploadSlots: *uploadSlots,
	})
	if err != nil {
		fatal("create seeder: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Announce to tracker.
	if t.Announce != "" {
		go func() {
			_, _ = tracker.Announce(t.Announce, tracker.AnnounceParams{
				InfoHash:   t.InfoHash,
				PeerID:     peerID,
				Port:       6881,
				Uploaded:   0,
				Downloaded: 0,
				Left:       0,
				Event:      "started",
			})
		}()
	}

	if err := seeder.Run(ctx); err != nil {
		fatal("seeder: %v", err)
	}
}

// --- create ---

func runCreate(args []string) {
	fs := flag.NewFlagSet("create", flag.ExitOnError)
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: peer-pressure create [options] <path>\n\n")
		fmt.Fprintf(os.Stderr, "Create a .torrent file from a file or directory.\n\n")
		fs.PrintDefaults()
	}
	tracker := fs.String("t", "", "tracker announce URL (required)")
	output := fs.String("o", "", "output .torrent path (default: <name>.torrent)")
	pieceLen := fs.Int("piece-size", 0, "piece length in bytes (default: auto)")
	private := fs.Bool("private", false, "set BEP 27 private flag")
	comment := fs.String("comment", "", "free-text comment")
	fs.Parse(args)

	if fs.NArg() < 1 {
		fs.Usage()
		os.Exit(1)
	}
	if *tracker == "" {
		fatal("tracker URL is required (-t)")
	}

	path := fs.Arg(0)
	raw, err := torrent.Create(path, torrent.CreateOpts{
		Tracker:     *tracker,
		PieceLength: *pieceLen,
		Private:     *private,
		Comment:     *comment,
	})
	if err != nil {
		fatal("create: %v", err)
	}

	outPath := *output
	if outPath == "" {
		outPath = filepath.Base(path) + ".torrent"
	}
	if err := os.WriteFile(outPath, raw, 0o644); err != nil {
		fatal("write: %v", err)
	}

	t, _ := torrent.Parse(raw)
	fmt.Printf("Created %s\n", outPath)
	fmt.Printf("  Info hash: %x\n", t.InfoHash)
	fmt.Printf("  Pieces:    %d\n", len(t.Pieces))
	fmt.Printf("  Size:      %d bytes\n", t.TotalLength())
}
