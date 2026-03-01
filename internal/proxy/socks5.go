package proxy

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"sync"

	"github.com/hashicorp/yamux"
	"github.com/things-go/go-socks5"

	"github.com/ShigShag/Phantom-Proxy/internal/proto"
)

// SOCKS5Server wraps a go-socks5 server that routes connections through a yamux session.
type SOCKS5Server struct {
	mu      sync.RWMutex
	session *yamux.Session
	logger  *slog.Logger
}

// NewSOCKS5Server creates a new SOCKS5Server.
func NewSOCKS5Server(logger *slog.Logger) *SOCKS5Server {
	return &SOCKS5Server{logger: logger}
}

// SetSession replaces the current yamux session (used on reconnect).
func (s *SOCKS5Server) SetSession(sess *yamux.Session) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.session = sess
	s.logger.Info("socks5 session updated")
}

func (s *SOCKS5Server) getSession() *yamux.Session {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.session
}

// ListenAndServe starts the SOCKS5 listener on the given address.
func (s *SOCKS5Server) ListenAndServe(addr string) error {
	server := socks5.NewServer(
		socks5.WithDial(s.dialThroughTunnel),
	)

	s.logger.Info("socks5 server starting", "addr", addr)
	return server.ListenAndServe("tcp", addr)
}

// dialThroughTunnel opens a yamux stream, sends CONNECT, and returns the stream as a net.Conn.
func (s *SOCKS5Server) dialThroughTunnel(ctx context.Context, network, addr string) (net.Conn, error) {
	sess := s.getSession()
	if sess == nil {
		return nil, fmt.Errorf("no active session")
	}

	stream, err := sess.Open()
	if err != nil {
		return nil, fmt.Errorf("open stream: %w", err)
	}

	// Send CONNECT request.
	if err := proto.WriteConnect(stream, addr); err != nil {
		stream.Close()
		return nil, fmt.Errorf("send CONNECT: %w", err)
	}

	// Read CONNECT_ACK.
	ok, errMsg, err := proto.ReadConnectAck(stream)
	if err != nil {
		stream.Close()
		return nil, fmt.Errorf("read CONNECT_ACK: %w", err)
	}
	if !ok {
		stream.Close()
		return nil, fmt.Errorf("CONNECT rejected: %s", errMsg)
	}

	return stream, nil
}

// HandleStreams runs the client-side stream accept loop.
// For each incoming stream, it reads a CONNECT, dials the target, and relays data.
func HandleStreams(ctx context.Context, session *yamux.Session, logger *slog.Logger) error {
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		stream, err := session.Accept()
		if err != nil {
			return fmt.Errorf("accept stream: %w", err)
		}

		go handleStream(stream, logger)
	}
}

func handleStream(stream net.Conn, logger *slog.Logger) {
	defer stream.Close()

	// Read CONNECT request.
	addr, err := proto.ReadConnect(stream)
	if err != nil {
		logger.Debug("read CONNECT", "error", err)
		return
	}

	logger.Debug("CONNECT", "target", addr)

	// Dial the target.
	target, err := net.Dial("tcp", addr)
	if err != nil {
		logger.Debug("dial target", "addr", addr, "error", err)
		proto.WriteConnectAck(stream, false, err.Error())
		return
	}
	defer target.Close()

	// Send ACK.
	if err := proto.WriteConnectAck(stream, true, ""); err != nil {
		logger.Debug("send CONNECT_ACK", "error", err)
		return
	}

	// Relay data bidirectionally.
	relay(stream, target)
}

// HandleServerStreams runs the server-side stream accept loop.
// Handles streams opened by the client (e.g., for remote port forwarding).
func HandleServerStreams(ctx context.Context, session *yamux.Session, logger *slog.Logger) {
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		stream, err := session.Accept()
		if err != nil {
			logger.Info("session closed", "error", err)
			return
		}

		go handleStream(stream, logger)
	}
}

// relay copies data bidirectionally between two connections.
func relay(a, b net.Conn) {
	var wg sync.WaitGroup
	wg.Add(2)

	cp := func(dst, src net.Conn) {
		defer wg.Done()
		io.Copy(dst, src)
		if tc, ok := dst.(interface{ CloseWrite() error }); ok {
			tc.CloseWrite()
		}
	}

	go cp(a, b)
	go cp(b, a)

	wg.Wait()
}
