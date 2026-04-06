package main

import (
	"context"
	"crypto/rand"
	"flag"
	"fmt"
	"os"

	"github.com/ihvo/peer-pressure/download"
	"github.com/ihvo/peer-pressure/torrent"
	"github.com/ihvo/peer-pressure/tracker"
)

const version = "0.1.0"

var peerID [20]byte

func init() {
	// -PP0001- prefix + 12 random bytes
	copy(peerID[:], "-PP0001-")
	rand.Read(peerID[8:])
}

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
	case "version", "--version", "-v":
		fmt.Printf("peer-pressure %s\n", version)
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
  version    Print version
  help       Show this help

Run 'peer-pressure <command> -h' for command-specific help.
`, version)
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
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: peer-pressure download [options] <file.torrent>\n\n")
		fmt.Fprintf(os.Stderr, "Download a torrent file.\n\n")
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

	outPath := *output
	if outPath == "" {
		outPath = t.Name
	}

	addrs := announceAll(t, uint16(*port))
	if len(addrs) == 0 {
		fatal("no peers found in swarm")
	}

	fmt.Printf("Found %d peers, downloading %s (%d pieces)...\n",
		len(addrs), t.Name, len(t.Pieces))

	err = download.File(context.Background(), download.Config{
		Torrent:    t,
		Peers:      addrs,
		OutputPath: outPath,
		PeerID:     peerID,
		MaxPeers:   *maxPeers,
		Quiet:      *quiet,
	})
	if err != nil {
		fatal("download: %v", err)
	}

	fmt.Printf("\nDone! Saved to %s\n", outPath)
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
