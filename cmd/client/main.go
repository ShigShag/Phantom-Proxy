package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"os"
	"os/signal"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/ShigShag/Phantom-Proxy/internal/buildcfg"
	"github.com/ShigShag/Phantom-Proxy/internal/mux"
	"github.com/ShigShag/Phantom-Proxy/internal/proto"
	"github.com/ShigShag/Phantom-Proxy/internal/proxy"
	"github.com/ShigShag/Phantom-Proxy/internal/transport"
	"github.com/ShigShag/Phantom-Proxy/pkg/config"

	// Register transports.
	_ "github.com/ShigShag/Phantom-Proxy/internal/transport/http"
	_ "github.com/ShigShag/Phantom-Proxy/internal/transport/ssh"
	_ "github.com/ShigShag/Phantom-Proxy/internal/transport/tcp"
	_ "github.com/ShigShag/Phantom-Proxy/internal/transport/tls"
)

// stringSlice implements flag.Value for repeatable string flags.
type stringSlice []string

func (s *stringSlice) String() string { return strings.Join(*s, ", ") }
func (s *stringSlice) Set(v string) error {
	*s = append(*s, v)
	return nil
}

func main() {
	var (
		serverAddr = flag.String("server", "localhost:4444", "server address")
		transportN = flag.String("transport", "tcp", "transport (tcp, tls, ssh, http)")
		secret     = flag.String("secret", "", "shared secret for auth")
		logLevel   = flag.String("log-level", "info", "log level (debug, info, warn, error)")
		reconnect  = flag.Bool("reconnect", true, "auto-reconnect on disconnect")

		// TLS flags
		certFile   = flag.String("cert", "", "TLS client certificate file")
		keyFile    = flag.String("certkey", "", "TLS client key file")
		caFile     = flag.String("tls-ca", "", "TLS CA certificate for server verification")
		skipVerify = flag.Bool("tls-skip-verify", false, "skip TLS certificate verification")

		// SSH flags
		clientKeyFile = flag.String("key", "", "SSH client key or TLS key file")

		// HTTP flags
		httpPath   = flag.String("http-path", buildcfg.DefaultWSPath, "WebSocket URL path")
		hostHeader = flag.String("http-host", "", "custom Host header")
		userAgent  = flag.String("http-ua", "", "custom User-Agent header")

		// Port forwarding
		remoteForwards stringSlice
	)
	flag.Var(&remoteForwards, "remote-forward", "remote port forward (bind:target), repeatable")

	flag.Parse()

	// Setup logging.
	var level slog.Level
	switch *logLevel {
	case "debug":
		level = slog.LevelDebug
	case "warn":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	default:
		level = slog.LevelInfo
	}
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))
	slog.SetDefault(logger)

	if *secret == "" {
		slog.Error("--secret is required")
		os.Exit(1)
	}

	// Resolve key file.
	if *clientKeyFile != "" && *keyFile == "" {
		*keyFile = *clientKeyFile
	}

	// Parse port forwards.
	var fwds []config.PortForward
	for _, s := range remoteForwards {
		pf, err := parseForward(s)
		if err != nil {
			slog.Error("parse remote-forward", "value", s, "error", err)
			os.Exit(1)
		}
		fwds = append(fwds, pf)
	}

	tr, err := transport.Get(*transportN)
	if err != nil {
		slog.Error("transport", "error", err)
		os.Exit(1)
	}

	cfg := &transport.Config{
		CertFile:      *certFile,
		KeyFile:       *keyFile,
		CAFile:        *caFile,
		SkipVerify:    *skipVerify,
		ClientKeyFile: *clientKeyFile,
		HTTPPath:      *httpPath,
		HostHeader:    *hostHeader,
		UserAgent:     *userAgent,
		UseTLS:        *caFile != "" || *certFile != "",
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Handle shutdown.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		slog.Info("shutting down")
		cancel()
	}()

	if *reconnect {
		runWithReconnect(ctx, tr, cfg, *serverAddr, *secret, fwds, logger)
	} else {
		if err := connect(ctx, tr, cfg, *serverAddr, *secret, fwds, logger); err != nil {
			slog.Error("fatal", "error", err)
			os.Exit(1)
		}
	}
}

func runWithReconnect(ctx context.Context, tr transport.Transport, cfg *transport.Config, serverAddr, secret string, fwds []config.PortForward, logger *slog.Logger) {
	backoff := time.Second
	const maxBackoff = 60 * time.Second

	for {
		err := connect(ctx, tr, cfg, serverAddr, secret, fwds, logger)
		if ctx.Err() != nil {
			return
		}

		slog.Warn("disconnected", "error", err)

		// Exponential backoff with jitter.
		jitter := time.Duration(rand.Int64N(int64(backoff) / 2))
		wait := backoff + jitter
		slog.Info("reconnecting", "wait", wait)

		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}

		backoff = min(backoff*2, maxBackoff)
	}
}

func connect(ctx context.Context, tr transport.Transport, cfg *transport.Config, serverAddr, secret string, fwds []config.PortForward, logger *slog.Logger) error {
	slog.Info("connecting", "server", serverAddr, "transport", tr.Name())

	conn, err := tr.Dial(serverAddr, cfg)
	if err != nil {
		return err
	}
	defer conn.Close()

	session, err := mux.ClientSession(conn)
	if err != nil {
		return err
	}
	defer session.Close()

	// Open control stream (stream 0).
	ctrl, err := session.Open()
	if err != nil {
		return err
	}
	defer ctrl.Close()

	// Authenticate.
	hostname, _ := os.Hostname()
	info := &proto.ClientInfoPayload{
		Hostname: hostname,
		OS:       runtime.GOOS,
		Arch:     runtime.GOARCH,
	}
	if err := proto.ClientHandshake(ctrl, secret, info); err != nil {
		return err
	}

	slog.Info("authenticated", "server", serverAddr)

	// Close session on context cancellation to unblock Accept().
	go func() {
		<-ctx.Done()
		session.Close()
	}()

	// Start keepalive on control stream.
	go proto.RunKeepalive(ctx, ctrl, logger)

	// Start remote port forwards (client listens, dials through session to server side).
	for _, fwd := range fwds {
		if err := proxy.RemoteForward(session, fwd.Bind, fwd.Target, logger); err != nil {
			slog.Error("remote forward", "bind", fwd.Bind, "target", fwd.Target, "error", err)
		}
	}

	// Accept streams from server and handle CONNECT requests (SOCKS5 proxy).
	return proxy.HandleStreams(ctx, session, logger)
}

// parseForward parses "bind:target" format, e.g. "127.0.0.1:3306:dbhost:3306".
func parseForward(s string) (config.PortForward, error) {
	parts := strings.SplitN(s, ":", 4)
	if len(parts) == 4 {
		return config.PortForward{
			Bind:   parts[0] + ":" + parts[1],
			Target: parts[2] + ":" + parts[3],
		}, nil
	}
	if len(parts) == 3 {
		return config.PortForward{
			Bind:   "0.0.0.0:" + parts[0],
			Target: parts[1] + ":" + parts[2],
		}, nil
	}
	return config.PortForward{}, fmt.Errorf("invalid forward format %q (expected bind:target)", s)
}
