package unified

import (
	"fmt"
	"github.com/panaudia/panaudia/core/common"
	"net"
	"sync"
)

// Demuxer peeks at the first byte of each TCP connection and routes it:
//   - 0x16 (TLS ClientHello) → TLS listener (HTTPS/WebSocket)
//   - anything else           → STUN listener (pion ICE TCP)
type Demuxer struct {
	listener  net.Listener
	tlsConns  chan net.Conn
	stunConns chan net.Conn
	once      sync.Once
	done      chan struct{}
}

// NewDemuxer wraps a TCP listener with byte-peeking routing.
func NewDemuxer(listener net.Listener) *Demuxer {
	return &Demuxer{
		listener:  listener,
		tlsConns:  make(chan net.Conn, 64),
		stunConns: make(chan net.Conn, 64),
		done:      make(chan struct{}),
	}
}

// TLSListener returns a net.Listener that yields connections whose first byte is 0x16.
func (d *Demuxer) TLSListener() net.Listener {
	return &virtualListener{
		conns: d.tlsConns,
		addr:  d.listener.Addr(),
		done:  d.done,
	}
}

// STUNListener returns a net.Listener that yields all other connections (STUN/ICE).
func (d *Demuxer) STUNListener() net.Listener {
	return &virtualListener{
		conns: d.stunConns,
		addr:  d.listener.Addr(),
		done:  d.done,
	}
}

// Run accepts connections from the underlying listener and dispatches them.
// It blocks until the listener is closed or Stop() is called.
func (d *Demuxer) Run() {
	for {
		conn, err := d.listener.Accept()
		if err != nil {
			select {
			case <-d.done:
				return
			default:
			}
			common.LogError("Demuxer accept error: %v", err)
			return
		}

		go d.route(conn)
	}
}

func (d *Demuxer) route(conn net.Conn) {
	// Peek at the first byte
	buf := make([]byte, 1)
	n, err := conn.Read(buf)
	if err != nil || n == 0 {
		conn.Close()
		return
	}

	wrapped := &prefixConn{Conn: conn, prefix: buf[:n]}

	if buf[0] == 0x16 {
		// TLS ClientHello
		select {
		case d.tlsConns <- wrapped:
		case <-d.done:
			conn.Close()
		}
	} else {
		// STUN or other
		select {
		case d.stunConns <- wrapped:
		case <-d.done:
			conn.Close()
		}
	}
}

// Stop shuts down the demuxer.
func (d *Demuxer) Stop() {
	d.once.Do(func() {
		close(d.done)
		d.listener.Close()
	})
}

// virtualListener implements net.Listener backed by a channel of connections.
type virtualListener struct {
	conns chan net.Conn
	addr  net.Addr
	done  chan struct{}
}

func (vl *virtualListener) Accept() (net.Conn, error) {
	select {
	case conn, ok := <-vl.conns:
		if !ok {
			return nil, fmt.Errorf("listener closed")
		}
		return conn, nil
	case <-vl.done:
		return nil, fmt.Errorf("listener closed")
	}
}

func (vl *virtualListener) Close() error {
	return nil // lifecycle managed by Demuxer
}

func (vl *virtualListener) Addr() net.Addr {
	return vl.addr
}

// prefixConn replays the peeked byte(s) on the first Read, then delegates to the underlying conn.
type prefixConn struct {
	net.Conn
	prefix []byte
	read   bool
}

func (pc *prefixConn) Read(b []byte) (int, error) {
	if !pc.read && len(pc.prefix) > 0 {
		pc.read = true
		n := copy(b, pc.prefix)
		if n < len(b) {
			// Fill the rest from the real connection
			m, err := pc.Conn.Read(b[n:])
			return n + m, err
		}
		return n, nil
	}
	return pc.Conn.Read(b)
}
