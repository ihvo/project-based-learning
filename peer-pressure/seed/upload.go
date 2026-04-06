package seed

import (
	"sync/atomic"

	"github.com/ihvo/peer-pressure/peer"
)

// uploadConn tracks state for a single connected peer.
type uploadConn struct {
	conn        *peer.Conn
	addr        string
	interested  bool
	choked      bool // we are choking this peer (true by default)
	uploadBytes atomic.Int64
}
