package torrent

import (
	"crypto/sha1"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/ihvo/peer-pressure/bencode"
)

// CreateOpts controls torrent creation parameters.
type CreateOpts struct {
	Tracker     string   // primary announce URL (required)
	Trackers    []string // additional tracker URLs (optional, single tier)
	PieceLength int      // piece length in bytes (0 = auto)
	Private     bool     // BEP 27 private flag
	Comment     string   // free-text comment
	WebSeeds    []string // BEP 19 web seed URLs
	CreatedBy   string   // creator string (default: "Peer Pressure")
}

// Create builds a .torrent metainfo from the file or directory at path.
// Returns the raw bencoded bytes ready to write to disk.
func Create(path string, opts CreateOpts) ([]byte, error) {
	if opts.Tracker == "" {
		return nil, fmt.Errorf("tracker URL is required")
	}

	fi, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("stat %s: %w", path, err)
	}

	var info bencode.Dict
	if fi.IsDir() {
		info, err = buildMultiFileInfo(path, fi.Name(), opts)
	} else {
		info, err = buildSingleFileInfo(path, fi.Name(), fi.Size(), opts)
	}
	if err != nil {
		return nil, err
	}

	meta := bencode.Dict{
		"announce":      bencode.String(opts.Tracker),
		"creation date": bencode.Int(time.Now().Unix()),
		"info":          info,
	}

	if len(opts.Trackers) > 0 {
		tier := make(bencode.List, 0, len(opts.Trackers)+1)
		tier = append(tier, bencode.String(opts.Tracker))
		for _, t := range opts.Trackers {
			tier = append(tier, bencode.String(t))
		}
		meta["announce-list"] = bencode.List{tier}
	}

	if opts.Comment != "" {
		meta["comment"] = bencode.String(opts.Comment)
	}

	createdBy := opts.CreatedBy
	if createdBy == "" {
		createdBy = "Peer Pressure"
	}
	meta["created by"] = bencode.String(createdBy)

	if len(opts.WebSeeds) > 0 {
		urlList := make(bencode.List, len(opts.WebSeeds))
		for i, u := range opts.WebSeeds {
			urlList[i] = bencode.String(u)
		}
		meta["url-list"] = urlList
	}

	return bencode.Encode(meta), nil
}

func buildSingleFileInfo(path, name string, size int64, opts CreateOpts) (bencode.Dict, error) {
	pl := pieceLength(size, opts.PieceLength)

	pieces, err := hashFile(path, pl)
	if err != nil {
		return nil, err
	}

	info := bencode.Dict{
		"name":         bencode.String(name),
		"piece length": bencode.Int(pl),
		"pieces":       bencode.String(pieces),
		"length":       bencode.Int(size),
	}
	if opts.Private {
		info["private"] = bencode.Int(1)
	}
	return info, nil
}

type fileEntry struct {
	relPath string
	size    int64
}

func buildMultiFileInfo(dirPath, name string, opts CreateOpts) (bencode.Dict, error) {
	var files []fileEntry
	var totalSize int64

	err := filepath.Walk(dirPath, func(p string, fi os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if fi.IsDir() {
			if strings.HasPrefix(fi.Name(), ".") && p != dirPath {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasPrefix(fi.Name(), ".") || fi.Size() == 0 {
			return nil
		}
		rel, err := filepath.Rel(dirPath, p)
		if err != nil {
			return err
		}
		files = append(files, fileEntry{relPath: rel, size: fi.Size()})
		totalSize += fi.Size()
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("walk %s: %w", dirPath, err)
	}

	sort.Slice(files, func(i, j int) bool {
		return files[i].relPath < files[j].relPath
	})

	if len(files) == 0 {
		return nil, fmt.Errorf("no files found in %s", dirPath)
	}

	pl := pieceLength(totalSize, opts.PieceLength)

	// Hash all files concatenated, piece boundaries may cross files.
	pieces, err := hashMultiFiles(dirPath, files, pl)
	if err != nil {
		return nil, err
	}

	fileList := make(bencode.List, len(files))
	for i, f := range files {
		parts := strings.Split(f.relPath, string(filepath.Separator))
		pathList := make(bencode.List, len(parts))
		for j, p := range parts {
			pathList[j] = bencode.String(p)
		}
		fileList[i] = bencode.Dict{
			"length": bencode.Int(f.size),
			"path":   pathList,
		}
	}

	info := bencode.Dict{
		"name":         bencode.String(name),
		"piece length": bencode.Int(pl),
		"pieces":       bencode.String(pieces),
		"files":        fileList,
	}
	if opts.Private {
		info["private"] = bencode.Int(1)
	}
	return info, nil
}

func hashFile(path string, pieceLen int) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var pieces []byte
	buf := make([]byte, pieceLen)
	for {
		n, err := io.ReadFull(f, buf)
		if n > 0 {
			h := sha1.Sum(buf[:n])
			pieces = append(pieces, h[:]...)
		}
		if err == io.EOF || err == io.ErrUnexpectedEOF {
			break
		}
		if err != nil {
			return nil, err
		}
	}
	return pieces, nil
}

func hashMultiFiles(dirPath string, files []fileEntry, pieceLen int) ([]byte, error) {
	var pieces []byte
	h := sha1.New()
	remaining := pieceLen

	for _, f := range files {
		fp, err := os.Open(filepath.Join(dirPath, f.relPath))
		if err != nil {
			return nil, err
		}

		buf := make([]byte, 32*1024)
		for {
			n, err := fp.Read(buf)
			if n > 0 {
				data := buf[:n]
				for len(data) > 0 {
					take := remaining
					if take > len(data) {
						take = len(data)
					}
					h.Write(data[:take])
					data = data[take:]
					remaining -= take

					if remaining == 0 {
						sum := h.Sum(nil)
						pieces = append(pieces, sum...)
						h.Reset()
						remaining = pieceLen
					}
				}
			}
			if err == io.EOF {
				break
			}
			if err != nil {
				fp.Close()
				return nil, err
			}
		}
		fp.Close()
	}

	// Flush last partial piece.
	if remaining < pieceLen {
		sum := h.Sum(nil)
		pieces = append(pieces, sum...)
	}

	return pieces, nil
}

// pieceLength returns the piece size to use, auto-selecting if pl is 0.
func pieceLength(totalSize int64, pl int) int {
	if pl > 0 {
		return pl
	}

	target := totalSize / 1500
	result := 16 * 1024 // 16 KiB minimum
	for result < int(target) && result < 16*1024*1024 {
		result *= 2
	}
	return result
}
