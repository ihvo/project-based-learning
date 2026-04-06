package seed

import (
	"crypto/sha1"
	"fmt"
	"io"
	"os"

	"github.com/ihvo/peer-pressure/torrent"
)

// VerifyResult holds the outcome of data integrity verification.
type VerifyResult struct {
	TotalPieces   int
	ValidPieces   int
	InvalidPieces []int
}

// VerifyData hashes every piece of the local data against the torrent's
// piece hashes. For single-file torrents, dataPath is the file. For
// multi-file torrents, dataPath is the root directory.
func VerifyData(t *torrent.Torrent, dataPath string) (*VerifyResult, error) {
	r, err := newDiskReader(t, dataPath)
	if err != nil {
		return nil, err
	}
	defer r.Close()

	result := &VerifyResult{TotalPieces: len(t.Pieces)}
	buf := make([]byte, t.PieceLength)

	for i := range t.Pieces {
		pieceLen := t.PieceLen(i)
		n, err := r.ReadPiece(i, buf[:pieceLen])
		if err != nil {
			result.InvalidPieces = append(result.InvalidPieces, i)
			continue
		}
		if n != pieceLen {
			result.InvalidPieces = append(result.InvalidPieces, i)
			continue
		}

		hash := sha1.Sum(buf[:pieceLen])
		if hash != t.Pieces[i] {
			result.InvalidPieces = append(result.InvalidPieces, i)
			continue
		}
		result.ValidPieces++
	}

	return result, nil
}

// diskReader handles reading piece data from single or multi-file torrents.
type diskReader struct {
	files   []*os.File
	layout  []fileSpan
	torrent *torrent.Torrent
}

type fileSpan struct {
	offset int64 // byte offset in the torrent's virtual address space
	length int64
}

func newDiskReader(t *torrent.Torrent, dataPath string) (*diskReader, error) {
	r := &diskReader{torrent: t}

	if len(t.Files) == 0 {
		// Single file
		f, err := os.Open(dataPath)
		if err != nil {
			return nil, fmt.Errorf("open data: %w", err)
		}
		r.files = []*os.File{f}
		r.layout = []fileSpan{{offset: 0, length: int64(t.Length)}}
		return r, nil
	}

	// Multi-file
	var offset int64
	for _, tf := range t.Files {
		path := dataPath
		for _, comp := range tf.Path {
			path += "/" + comp
		}
		f, err := os.Open(path)
		if err != nil {
			r.Close()
			return nil, fmt.Errorf("open %s: %w", path, err)
		}
		r.files = append(r.files, f)
		r.layout = append(r.layout, fileSpan{offset: offset, length: int64(tf.Length)})
		offset += int64(tf.Length)
	}

	return r, nil
}

// ReadPiece reads exactly one piece worth of data from the correct file(s).
func (r *diskReader) ReadPiece(index int, buf []byte) (int, error) {
	pieceOffset := int64(index) * int64(r.torrent.PieceLength)
	remaining := len(buf)
	pos := 0

	for i, span := range r.layout {
		if pieceOffset >= span.offset+span.length {
			continue
		}
		if remaining == 0 {
			break
		}

		fileOffset := pieceOffset - span.offset
		if fileOffset < 0 {
			fileOffset = 0
		}
		canRead := int(span.length - fileOffset)
		if canRead > remaining {
			canRead = remaining
		}

		n, err := r.files[i].ReadAt(buf[pos:pos+canRead], fileOffset)
		pos += n
		remaining -= n
		pieceOffset += int64(n)

		if err != nil && err != io.EOF {
			return pos, err
		}
	}

	if remaining > 0 {
		return pos, fmt.Errorf("short read: got %d, want %d", pos, len(buf))
	}
	return pos, nil
}

func (r *diskReader) Close() {
	for _, f := range r.files {
		f.Close()
	}
}
