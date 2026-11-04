package speechserve

import (
	"net"
	"sync"
)

// Server-side close occurs after handleConn's drain and release defers. Observe
// that boundary without probing (and claiming) the bridge's single client slot.
type observedListener struct{ net.Listener }

func (l observedListener) Accept() (net.Conn, error) {
	conn, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	return &observedConn{Conn: conn, closed: make(chan struct{})}, nil
}

type observedConn struct {
	net.Conn
	closed chan struct{}
	once   sync.Once
}

func (c *observedConn) Close() error {
	err := c.Conn.Close()
	c.once.Do(func() { close(c.closed) })
	return err
}
