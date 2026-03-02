package mux

import (
	"io"
	"net"
	"sync"
	"time"

	"github.com/hashicorp/yamux"
)

// DefaultConfig returns a yamux config tuned for tunneling.
func DefaultConfig() *yamux.Config {
	cfg := yamux.DefaultConfig()
	cfg.EnableKeepAlive = true
	cfg.KeepAliveInterval = 30 * time.Second
	cfg.MaxStreamWindowSize = 256 * 1024
	cfg.LogOutput = io.Discard
	return cfg
}

// ServerSession creates a yamux server session over the given connection.
func ServerSession(conn net.Conn) (*yamux.Session, error) {
	return yamux.Server(conn, DefaultConfig())
}

// ClientSession creates a yamux client session over the given connection.
func ClientSession(conn net.Conn) (*yamux.Session, error) {
	return yamux.Client(conn, DefaultConfig())
}

// Relay copies data bidirectionally between two connections.
// It closes both connections when either direction finishes.
func Relay(a, b net.Conn) {
	var wg sync.WaitGroup
	wg.Add(2)

	copy := func(dst, src net.Conn) {
		defer wg.Done()
		io.Copy(dst, src)
		// Signal EOF to the other side.
		if tc, ok := dst.(interface{ CloseWrite() error }); ok {
			tc.CloseWrite()
		}
	}

	go copy(a, b)
	go copy(b, a)

	wg.Wait()
	a.Close()
	b.Close()
}
