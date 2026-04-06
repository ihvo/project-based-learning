package tracker

import (
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/ihvo/peer-pressure/bencode"
)

// ScrapeResult holds the stats for a single torrent from a scrape response.
type ScrapeResult struct {
	InfoHash   [20]byte
	Complete   int // seeders
	Downloaded int // total completed downloads
	Incomplete int // leechers
}

// Scrape queries the tracker for torrent statistics without announcing.
func Scrape(trackerURL string, infoHashes [][20]byte) ([]ScrapeResult, error) {
	if strings.HasPrefix(trackerURL, "udp://") {
		return udpScrape(trackerURL, infoHashes)
	}
	return scrapeHTTP(trackerURL, infoHashes)
}

// scrapeURL derives the scrape URL from an announce URL.
func scrapeURL(announceURL string) (string, error) {
	u, err := url.Parse(announceURL)
	if err != nil {
		return "", fmt.Errorf("parse URL: %w", err)
	}

	idx := strings.LastIndex(u.Path, "/announce")
	if idx == -1 {
		return "", fmt.Errorf("tracker URL does not contain /announce: %s", announceURL)
	}

	u.Path = u.Path[:idx] + "/scrape" + u.Path[idx+len("/announce"):]
	return u.String(), nil
}

func scrapeHTTP(trackerURL string, infoHashes [][20]byte) ([]ScrapeResult, error) {
	sURL, err := scrapeURL(trackerURL)
	if err != nil {
		return nil, err
	}

	base, err := url.Parse(sURL)
	if err != nil {
		return nil, fmt.Errorf("parse scrape URL: %w", err)
	}

	var rawQuery strings.Builder
	if base.RawQuery != "" {
		rawQuery.WriteString(base.RawQuery)
	}
	for _, ih := range infoHashes {
		if rawQuery.Len() > 0 {
			rawQuery.WriteByte('&')
		}
		rawQuery.WriteString("info_hash=")
		rawQuery.WriteString(percentEncodeBytes(ih[:]))
	}
	base.RawQuery = rawQuery.String()

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Get(base.String())
	if err != nil {
		return nil, fmt.Errorf("scrape request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read scrape response: %w", err)
	}

	return parseScrapeResponse(body, infoHashes)
}

func parseScrapeResponse(data []byte, infoHashes [][20]byte) ([]ScrapeResult, error) {
	val, err := bencode.Decode(data)
	if err != nil {
		return nil, fmt.Errorf("decode scrape response: %w", err)
	}

	d, ok := val.(bencode.Dict)
	if !ok {
		return nil, fmt.Errorf("scrape response is not a dict")
	}

	if failReason, ok := d["failure reason"]; ok {
		if s, ok := failReason.(bencode.String); ok {
			return nil, fmt.Errorf("scrape error: %s", string(s))
		}
	}

	filesVal, ok := d["files"]
	if !ok {
		return nil, fmt.Errorf("scrape response missing 'files' key")
	}
	files, ok := filesVal.(bencode.Dict)
	if !ok {
		return nil, fmt.Errorf("scrape 'files' is not a dict")
	}

	results := make([]ScrapeResult, 0, len(infoHashes))
	for _, ih := range infoHashes {
		key := string(ih[:])
		entry, ok := files[key]
		if !ok {
			results = append(results, ScrapeResult{InfoHash: ih})
			continue
		}

		ed, ok := entry.(bencode.Dict)
		if !ok {
			results = append(results, ScrapeResult{InfoHash: ih})
			continue
		}

		r := ScrapeResult{InfoHash: ih}
		if v, ok := ed["complete"].(bencode.Int); ok {
			r.Complete = int(v)
		}
		if v, ok := ed["downloaded"].(bencode.Int); ok {
			r.Downloaded = int(v)
		}
		if v, ok := ed["incomplete"].(bencode.Int); ok {
			r.Incomplete = int(v)
		}
		results = append(results, r)
	}

	return results, nil
}

const actionScrape uint32 = 2

func udpScrape(rawURL string, infoHashes [][20]byte) ([]ScrapeResult, error) {
	host, _, err := udpParseURL(rawURL)
	if err != nil {
		return nil, err
	}

	raddr, err := net.ResolveUDPAddr("udp", host)
	if err != nil {
		return nil, fmt.Errorf("resolve tracker: %w", err)
	}

	conn, err := net.DialUDP("udp", nil, raddr)
	if err != nil {
		return nil, fmt.Errorf("dial tracker: %w", err)
	}
	defer conn.Close()

	connID, err := udpConnect(conn)
	if err != nil {
		return nil, fmt.Errorf("udp connect: %w", err)
	}

	return udpScrapeRequest(conn, connID, infoHashes)
}

func udpScrapeRequest(conn *net.UDPConn, connID uint64, infoHashes [][20]byte) ([]ScrapeResult, error) {
	txnID := randUint32()

	reqLen := 16 + 20*len(infoHashes)
	req := make([]byte, reqLen)
	binary.BigEndian.PutUint64(req[0:8], connID)
	binary.BigEndian.PutUint32(req[8:12], actionScrape)
	binary.BigEndian.PutUint32(req[12:16], txnID)
	for i, ih := range infoHashes {
		copy(req[16+i*20:16+(i+1)*20], ih[:])
	}

	minResp := 8 + 12*len(infoHashes)
	resp, err := udpRoundTrip(conn, req, minResp)
	if err != nil {
		return nil, err
	}

	action := binary.BigEndian.Uint32(resp[0:4])
	respTxn := binary.BigEndian.Uint32(resp[4:8])

	if action != actionScrape {
		return nil, fmt.Errorf("expected action=scrape(2), got %d", action)
	}
	if respTxn != txnID {
		return nil, fmt.Errorf("transaction ID mismatch: sent %d, got %d", txnID, respTxn)
	}

	results := make([]ScrapeResult, len(infoHashes))
	for i := range infoHashes {
		off := 8 + i*12
		results[i] = ScrapeResult{
			InfoHash:   infoHashes[i],
			Complete:   int(binary.BigEndian.Uint32(resp[off : off+4])),
			Downloaded: int(binary.BigEndian.Uint32(resp[off+4 : off+8])),
			Incomplete: int(binary.BigEndian.Uint32(resp[off+8 : off+12])),
		}
	}

	return results, nil
}
