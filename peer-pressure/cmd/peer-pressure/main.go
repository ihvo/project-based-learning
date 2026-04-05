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

	t, err := torrent.ParseFile(fs.Arg(0))
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

	t, err := torrent.ParseFile(fs.Arg(0))
	if err != nil {
		fatal("parse torrent: %v", err)
	}

	resp, err := tracker.Announce(t.Announce, tracker.AnnounceParams{
		InfoHash: t.InfoHash,
		PeerID:   peerID,
		Port:     uint16(*port),
		Left:     int64(t.TotalLength()),
	})
	if err != nil {
		fatal("tracker announce: %v", err)
	}

	fmt.Printf("Tracker: %s\n", t.Announce)
	fmt.Printf("Interval: %ds\n", resp.Interval)
	fmt.Printf("Seeders: %d  Leechers: %d\n", resp.Complete, resp.Incomplete)
	fmt.Printf("Peers (%d):\n", len(resp.Peers))
	for _, p := range resp.Peers {
		fmt.Printf("  %s\n", p.Addr())
	}
}

// --- download ---

func runDownload(args []string) {
	fs := flag.NewFlagSet("download", flag.ExitOnError)
	output := fs.String("o", "", "output file path (default: torrent name)")
	port := fs.Int("port", 6881, "port to announce")
	maxPeers := fs.Int("peers", 5, "max concurrent peer connections")
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

	t, err := torrent.ParseFile(fs.Arg(0))
	if err != nil {
		fatal("parse torrent: %v", err)
	}

	// Resolve output path
	outPath := *output
	if outPath == "" {
		outPath = t.Name
	}

	// Announce to get peers
	fmt.Printf("Announcing to %s...\n", t.Announce)
	resp, err := tracker.Announce(t.Announce, tracker.AnnounceParams{
		InfoHash: t.InfoHash,
		PeerID:   peerID,
		Port:     uint16(*port),
		Left:     int64(t.TotalLength()),
	})
	if err != nil {
		fatal("tracker announce: %v", err)
	}

	if len(resp.Peers) == 0 {
		fatal("no peers found in swarm")
	}

	addrs := make([]string, len(resp.Peers))
	for i, p := range resp.Peers {
		addrs[i] = p.Addr()
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

func fatal(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "error: "+format+"\n", args...)
	os.Exit(1)
}
