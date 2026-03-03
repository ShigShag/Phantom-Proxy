package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"net"
	"os"
	"os/signal"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/hashicorp/yamux"

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
		dormant    = flag.Bool("dormant", false, "start in dormant mode (disconnect between check-ins)")
		proxyURL   = flag.String("proxy", "", "upstream proxy URL (http://[user:pass@]host:port or socks5://[user:pass@]host:port)")

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
		ProxyURL:      *proxyURL,
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

	if *dormant {
		runCheckinLoop(ctx, tr, cfg, *serverAddr, *secret, fwds, logger)
		return
	}

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

	// Non-dormant mode: start keepalive and handle streams immediately.
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

// sleepState holds the beacon configuration, persisted across check-in cycles.
type sleepState struct {
	intervalSec int
	jitterPct   int
}

// runCheckinLoop is the outer loop for dormant clients.
// It connects, processes commands, disconnects, sleeps, and repeats.
func runCheckinLoop(ctx context.Context, tr transport.Transport, cfg *transport.Config, serverAddr, secret string, fwds []config.PortForward, logger *slog.Logger) {
	state := &sleepState{intervalSec: 30, jitterPct: 0}
	backoff := time.Second
	const maxBackoff = 60 * time.Second

	for {
		err := connectAndCheckin(ctx, tr, cfg, serverAddr, secret, fwds, state, logger)
		if ctx.Err() != nil {
			return
		}

		if err != nil {
			slog.Warn("check-in failed", "error", err)
			// Exponential backoff on connection errors.
			jitter := time.Duration(rand.Int64N(int64(backoff)/2 + 1))
			wait := backoff + jitter
			slog.Info("retrying", "wait", wait)
			select {
			case <-ctx.Done():
				return
			case <-time.After(wait):
			}
			backoff = min(backoff*2, maxBackoff)
			continue
		}

		// Success — reset backoff and sleep for configured interval.
		backoff = time.Second
		wait := computeSleepDuration(state.intervalSec, state.jitterPct)
		slog.Info("check-in complete, sleeping", "duration", wait)

		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
	}
}

// connectAndCheckin connects to the server, authenticates with Dormant flag,
// reads pending commands, and disconnects.
// If a CmdWake is received, it stays connected and enters active mode.
func connectAndCheckin(ctx context.Context, tr transport.Transport, cfg *transport.Config, serverAddr, secret string, fwds []config.PortForward, state *sleepState, logger *slog.Logger) error {
	slog.Info("checking in", "server", serverAddr, "transport", tr.Name())

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

	ctrl, err := session.Open()
	if err != nil {
		return err
	}
	defer ctrl.Close()

	hostname, _ := os.Hostname()
	info := &proto.ClientInfoPayload{
		Hostname: hostname,
		OS:       runtime.GOOS,
		Arch:     runtime.GOARCH,
		Dormant:  true,
	}
	if err := proto.ClientHandshake(ctrl, secret, info); err != nil {
		return err
	}

	slog.Info("authenticated", "server", serverAddr)

	// Close session on context cancellation.
	go func() {
		<-ctx.Done()
		session.Close()
	}()

	// Read commands from server until CmdCheckinDone or disconnect.
	// Use a 30s read timeout as a fallback for non-interactive servers.
	const readTimeout = 30 * time.Second
	for {
		select {
		case <-ctx.Done():
			proto.WriteMessage(ctrl, &proto.Message{Type: proto.Disconnect})
			return ctx.Err()
		default:
		}

		ctrl.SetReadDeadline(time.Now().Add(readTimeout))
		msg, err := proto.ReadMessage(ctrl)
		if err != nil {
			// Timeout or connection close — normal for non-interactive servers.
			if ne, ok := err.(net.Error); ok && ne.Timeout() {
				slog.Debug("read timeout during check-in, disconnecting")
				return nil
			}
			return fmt.Errorf("control stream closed: %w", err)
		}
		ctrl.SetReadDeadline(time.Time{})

		switch msg.Type {
		case proto.CmdSleepCfg:
			var cfg proto.SleepCfgPayload
			if err := json.Unmarshal(msg.Payload, &cfg); err != nil {
				logger.Error("decode sleep config", "error", err)
				proto.WriteCmdNack(ctrl, fmt.Sprintf("bad sleep config: %v", err))
				continue
			}
			logger.Info("received sleep config", "interval", cfg.IntervalSec, "jitter", cfg.JitterPct)
			state.intervalSec = cfg.IntervalSec
			state.jitterPct = cfg.JitterPct
			if err := proto.WriteCmdAck(ctrl); err != nil {
				return fmt.Errorf("send ack: %w", err)
			}

		case proto.CmdWake:
			logger.Info("received WAKE command")
			if err := proto.WriteCmdAck(ctrl); err != nil {
				return fmt.Errorf("send ack: %w", err)
			}
			// Stay connected and enter active mode.
			runActiveMode(ctx, ctrl, session, fwds, state, logger)
			return nil

		case proto.CmdSleep:
			logger.Info("received SLEEP command")
			if err := proto.WriteCmdAck(ctrl); err != nil {
				return fmt.Errorf("send ack: %w", err)
			}
			// Disconnect and return to check-in loop.
			return nil

		case proto.CmdCheckinDone:
			logger.Debug("server sent CHECKIN_DONE")
			return nil

		case proto.Disconnect:
			logger.Info("server sent DISCONNECT")
			return nil

		default:
			logger.Debug("unexpected control message", "type", fmt.Sprintf("0x%02x", msg.Type))
		}
	}
}

// runActiveMode runs HandleStreams + keepalive until a CmdSleep is received or disconnect.
func runActiveMode(ctx context.Context, ctrl net.Conn, session *yamux.Session, fwds []config.PortForward, state *sleepState, logger *slog.Logger) {
	activeCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	// Start keepalive.
	go proto.RunKeepalive(activeCtx, ctrl, logger)

	// Start remote port forwards.
	for _, fwd := range fwds {
		if err := proxy.RemoteForward(session, fwd.Bind, fwd.Target, logger); err != nil {
			logger.Error("remote forward", "bind", fwd.Bind, "target", fwd.Target, "error", err)
		}
	}

	// Start handling streams in background.
	go func() {
		if err := proxy.HandleStreams(activeCtx, session, logger); err != nil && activeCtx.Err() == nil {
			logger.Debug("handle streams ended", "error", err)
		}
	}()

	logger.Info("transitioned to active state")

	// Read control messages until sleep or disconnect.
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		msg, err := proto.ReadMessage(ctrl)
		if err != nil {
			logger.Debug("control stream closed in active mode", "error", err)
			return
		}

		switch msg.Type {
		case proto.CmdSleep:
			logger.Info("received SLEEP command, returning to dormant")
			proto.WriteCmdAck(ctrl)
			return

		case proto.CmdSleepCfg:
			var cfg proto.SleepCfgPayload
			if err := json.Unmarshal(msg.Payload, &cfg); err != nil {
				logger.Error("decode sleep config", "error", err)
				proto.WriteCmdNack(ctrl, fmt.Sprintf("bad sleep config: %v", err))
				continue
			}
			logger.Info("received sleep config", "interval", cfg.IntervalSec, "jitter", cfg.JitterPct)
			state.intervalSec = cfg.IntervalSec
			state.jitterPct = cfg.JitterPct
			proto.WriteCmdAck(ctrl)

		case proto.KeepaliveAck:
			logger.Debug("keepalive ack received")

		case proto.Disconnect:
			logger.Info("server sent DISCONNECT")
			return

		default:
			logger.Debug("unexpected control message", "type", fmt.Sprintf("0x%02x", msg.Type))
		}
	}
}

// computeSleepDuration calculates the sleep duration with jitter.
func computeSleepDuration(intervalSec, jitterPct int) time.Duration {
	base := time.Duration(intervalSec) * time.Second
	if base <= 0 {
		base = 30 * time.Second
	}
	if jitterPct <= 0 {
		return base
	}
	// jitter: ± (base * jitterPct / 200)
	maxJitter := int64(base) * int64(jitterPct) / 200
	if maxJitter > 0 {
		jitter := rand.Int64N(2*maxJitter+1) - maxJitter
		base += time.Duration(jitter)
	}
	return base
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
