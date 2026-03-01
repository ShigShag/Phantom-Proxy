package tcp

import (
	"net"

	"github.com/ShigShag/Phantom-Proxy/internal/transport"
)

func init() {
	transport.Register(&TCP{})
}

// TCP implements the Transport interface for plain TCP connections.
type TCP struct{}

func (t *TCP) Name() string { return "tcp" }

func (t *TCP) Dial(addr string, _ *transport.Config) (net.Conn, error) {
	return net.Dial("tcp", addr)
}

func (t *TCP) Listen(addr string, _ *transport.Config) (net.Listener, error) {
	return net.Listen("tcp", addr)
}
