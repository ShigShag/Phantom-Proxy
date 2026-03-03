package http

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/coder/websocket"

	"github.com/ShigShag/Phantom-Proxy/internal/buildcfg"
	"github.com/ShigShag/Phantom-Proxy/internal/transport"
)

func init() {
	transport.Register(&HTTP{})
}

// HTTP implements the Transport interface using WebSocket connections.
type HTTP struct{}

func (h *HTTP) Name() string { return "http" }

// Dial connects to a WebSocket server and returns the underlying net.Conn.
func (h *HTTP) Dial(addr string, cfg *transport.Config) (net.Conn, error) {
	scheme := "ws"
	if cfg.UseTLS {
		scheme = "wss"
	}

	path := cfg.HTTPPath
	if path == "" {
		path = buildcfg.DefaultWSPath
	}

	url := fmt.Sprintf("%s://%s%s", scheme, addr, path)

	opts := &websocket.DialOptions{
		HTTPHeader: http.Header{},
	}

	if cfg.HostHeader != "" {
		opts.HTTPHeader.Set("Host", cfg.HostHeader)
	}
	if cfg.UserAgent != "" {
		opts.HTTPHeader.Set("User-Agent", cfg.UserAgent)
	}

	// Build custom HTTP transport for proxy and/or TLS support.
	httpTransport := &http.Transport{}
	customTransport := false

	if cfg.ProxyURL != "" {
		httpTransport.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
			return transport.ProxyDial(ctx, network, addr, cfg.ProxyURL)
		}
		customTransport = true
	}

	if cfg.UseTLS {
		tlsCfg := &tls.Config{
			InsecureSkipVerify: cfg.SkipVerify,
		}
		if cfg.CAFile != "" {
			caCert, err := os.ReadFile(cfg.CAFile)
			if err != nil {
				return nil, fmt.Errorf("read CA cert: %w", err)
			}
			pool := x509.NewCertPool()
			if !pool.AppendCertsFromPEM(caCert) {
				return nil, fmt.Errorf("failed to parse CA cert")
			}
			tlsCfg.RootCAs = pool
		}
		if cfg.CertFile != "" && cfg.KeyFile != "" {
			cert, err := tls.LoadX509KeyPair(cfg.CertFile, cfg.KeyFile)
			if err != nil {
				return nil, fmt.Errorf("load client cert: %w", err)
			}
			tlsCfg.Certificates = []tls.Certificate{cert}
		}
		httpTransport.TLSClientConfig = tlsCfg
		customTransport = true
	}

	if customTransport {
		opts.HTTPClient = &http.Client{Transport: httpTransport}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	ws, _, err := websocket.Dial(ctx, url, opts)
	if err != nil {
		return nil, fmt.Errorf("websocket dial: %w", err)
	}

	ws.SetReadLimit(-1)
	return websocket.NetConn(context.Background(), ws, websocket.MessageBinary), nil
}

// Listen starts an HTTP server with WebSocket upgrade and bridges connections to net.Listener.
func (h *HTTP) Listen(addr string, cfg *transport.Config) (net.Listener, error) {
	path := cfg.HTTPPath
	if path == "" {
		path = buildcfg.DefaultWSPath
	}

	ln := &wsListener{
		connCh: make(chan net.Conn, 16),
		done:   make(chan struct{}),
	}

	mux := http.NewServeMux()
	mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
		ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{
			InsecureSkipVerify: true,
		})
		if err != nil {
			return
		}
		ws.SetReadLimit(-1)
		// Use a background context, not r.Context(), so the conn
		// outlives the HTTP handler.
		conn := websocket.NetConn(context.Background(), ws, websocket.MessageBinary)

		select {
		case ln.connCh <- conn:
		case <-ln.done:
			conn.Close()
			return
		}

		// Block until the listener is closed so the HTTP handler
		// doesn't return and cancel the underlying connection.
		<-ln.done
	})

	// Start TCP listener.
	tcpLn, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, err
	}
	ln.tcpAddr = tcpLn.Addr()

	// Optionally wrap with TLS.
	if cfg.CertFile != "" && cfg.KeyFile != "" {
		cert, err := tls.LoadX509KeyPair(cfg.CertFile, cfg.KeyFile)
		if err != nil {
			tcpLn.Close()
			return nil, fmt.Errorf("load server cert: %w", err)
		}
		tlsCfg := &tls.Config{
			Certificates: []tls.Certificate{cert},
		}
		if cfg.CAFile != "" {
			caCert, err := os.ReadFile(cfg.CAFile)
			if err != nil {
				tcpLn.Close()
				return nil, fmt.Errorf("read CA cert: %w", err)
			}
			pool := x509.NewCertPool()
			if !pool.AppendCertsFromPEM(caCert) {
				tcpLn.Close()
				return nil, fmt.Errorf("failed to parse CA cert")
			}
			tlsCfg.ClientCAs = pool
			tlsCfg.ClientAuth = tls.RequireAndVerifyClientCert
		}
		tcpLn = tls.NewListener(tcpLn, tlsCfg)
	}

	srv := &http.Server{Handler: mux}
	ln.srv = srv

	go func() {
		if err := srv.Serve(tcpLn); err != nil && !errors.Is(err, http.ErrServerClosed) {
			// Log error in production; for now silent.
		}
	}()

	return ln, nil
}

// wsListener bridges WebSocket connections to the net.Listener interface.
type wsListener struct {
	connCh  chan net.Conn
	done    chan struct{}
	once    sync.Once
	srv     *http.Server
	tcpAddr net.Addr
}

func (l *wsListener) Accept() (net.Conn, error) {
	select {
	case conn := <-l.connCh:
		return conn, nil
	case <-l.done:
		return nil, errors.New("listener closed")
	}
}

func (l *wsListener) Close() error {
	l.once.Do(func() { close(l.done) })
	if l.srv != nil {
		return l.srv.Close()
	}
	return nil
}

func (l *wsListener) Addr() net.Addr {
	return l.tcpAddr
}
