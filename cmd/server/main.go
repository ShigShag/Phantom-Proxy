package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/hashicorp/yamux"

	"github.com/ShigShag/Phantom-Proxy/internal/buildcfg"
	"github.com/ShigShag/Phantom-Proxy/internal/mux"
	"github.com/ShigShag/Phantom-Proxy/internal/proto"
	"github.com/ShigShag/Phantom-Proxy/internal/proxy"
	"github.com/ShigShag/Phantom-Proxy/internal/registry"
	"github.com/ShigShag/Phantom-Proxy/internal/shell"
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
		listenAddr = flag.String("listen", ":4444", "listen address")
		transportN = flag.String("transport", "tcp", "transport (tcp, tls, ssh, http)")
		secret     = flag.String("secret", "", "shared secret for auth")
		socks5Addr = flag.String("socks5", "127.0.0.1:1080", "SOCKS5 listen address")
		logLevel   = flag.String("log-level", "info", "log level (debug, info, warn, error)")

		// Interactive mode flags.
		interactive   = flag.Bool("interactive", false, "enable interactive C&C shell")
		sleepInterval = flag.Duration("sleep-interval", 30*time.Second, "default beacon interval for dormant clients")
		sleepJitter   = flag.Int("sleep-jitter", 0, "default sleep jitter percentage (0-100)")

		// TLS flags
		certFile = flag.String("cert", "", "TLS certificate file")
		keyFile  = flag.String("certkey", "", "TLS private key file")
		caFile   = flag.String("tls-ca", "", "TLS CA certificate for client auth (mTLS)")

		// SSH flags
		hostKeyFile = flag.String("key", "", "SSH host key or TLS key file")

		// HTTP flags
		httpPath = flag.String("http-path", buildcfg.DefaultWSPath, "WebSocket URL path")

		// Port forwarding
		localForwards stringSlice
	)
	flag.Var(&localForwards, "local-forward", "local port forward (bind:target), repeatable")

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

	// Resolve key file: --key is shared between SSH host key and TLS key.
	if *hostKeyFile != "" && *keyFile == "" {
		*keyFile = *hostKeyFile
	}

	// Parse port forwards.
	var fwds []config.PortForward
	for _, s := range localForwards {
		pf, err := parseForward(s)
		if err != nil {
			slog.Error("parse local-forward", "value", s, "error", err)
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
		CertFile:    *certFile,
		KeyFile:     *keyFile,
		CAFile:      *caFile,
		HostKeyFile: *hostKeyFile,
		HTTPPath:    *httpPath,
	}

	ln, err := tr.Listen(*listenAddr, cfg)
	if err != nil {
		slog.Error("listen", "error", err)
		os.Exit(1)
	}
	slog.Info("listening", "addr", ln.Addr(), "transport", tr.Name())

	// SOCKS5 server with session swapping.
	socksServer := proxy.NewSOCKS5Server(logger)

	// Start SOCKS5 listener.
	go func() {
		if err := socksServer.ListenAndServe(*socks5Addr); err != nil {
			slog.Error("socks5 listen", "error", err)
		}
	}()
	slog.Info("socks5 listening", "addr", *socks5Addr)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Handle shutdown.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		slog.Info("shutting down")
		cancel()
		ln.Close()
	}()

	// Default sleep configuration.
	defaultSleepCfg := proto.SleepCfgPayload{
		IntervalSec: int(sleepInterval.Seconds()),
		JitterPct:   *sleepJitter,
	}

	if *interactive {
		reg := registry.New()
		sh := shell.New(reg, socksServer, logger, *socks5Addr, func() {
			cancel()
			ln.Close()
		})
		go sh.Run(ctx)
		slog.Info("interactive mode enabled")

		for {
			conn, err := ln.Accept()
			if err != nil {
				select {
				case <-ctx.Done():
					return
				default:
					slog.Error("accept", "error", err)
					continue
				}
			}
			go handleClientInteractive(ctx, conn, *secret, reg, defaultSleepCfg, logger)
		}
	}

	// Non-interactive (legacy) mode.
	for {
		conn, err := ln.Accept()
		if err != nil {
			select {
			case <-ctx.Done():
				return
			default:
				slog.Error("accept", "error", err)
				continue
			}
		}
		go handleClient(ctx, conn, *secret, socksServer, fwds, logger)
	}
}

func handleClient(ctx context.Context, conn net.Conn, secret string, socksServer *proxy.SOCKS5Server, fwds []config.PortForward, logger *slog.Logger) {
	defer conn.Close()

	session, err := mux.ServerSession(conn)
	if err != nil {
		slog.Error("yamux session", "error", err)
		return
	}
	defer session.Close()

	// Accept control stream (stream 0).
	ctrl, err := session.Accept()
	if err != nil {
		slog.Error("accept control stream", "error", err)
		return
	}
	defer ctrl.Close()

	// Authenticate.
	info, err := proto.ServerHandshake(ctrl, secret)
	if err != nil {
		slog.Error("handshake", "error", err)
		return
	}

	slog.Info("client connected",
		"hostname", info.Hostname,
		"os", info.OS,
		"arch", info.Arch,
		"remote", conn.RemoteAddr(),
	)

	// Provide session to SOCKS5 server.
	socksServer.SetSession(session)

	// Close session on context cancellation to unblock Accept().
	go func() {
		<-ctx.Done()
		session.Close()
	}()

	// Start keepalive handler on control stream.
	go proto.HandleKeepalive(ctx, ctrl, logger)

	// Start local port forwards (server listens, client dials).
	for _, fwd := range fwds {
		if err := proxy.LocalForward(session, fwd.Bind, fwd.Target, logger); err != nil {
			slog.Error("local forward", "bind", fwd.Bind, "target", fwd.Target, "error", err)
		}
	}

	// Handle incoming streams from client (remote port forwards + SOCKS5).
	proxy.HandleServerStreams(ctx, session, logger)
}

// handleClientInteractive handles a client in interactive mode.
// For dormant clients (info.Dormant == true), uses FindOrRegister for reconnection
// support. Drains pending commands, sends CmdCheckinDone if no wake pending,
// and keeps the entry alive in the registry after disconnect.
// For non-dormant (legacy) clients, uses the old Register/Deregister flow.
func handleClientInteractive(ctx context.Context, conn net.Conn, secret string, reg *registry.Registry, defaultCfg proto.SleepCfgPayload, logger *slog.Logger) {
	defer conn.Close()

	session, err := mux.ServerSession(conn)
	if err != nil {
		slog.Error("yamux session", "error", err)
		return
	}
	defer session.Close()

	// Accept control stream (stream 0).
	ctrl, err := session.Accept()
	if err != nil {
		slog.Error("accept control stream", "error", err)
		return
	}
	defer ctrl.Close()

	// Authenticate.
	info, err := proto.ServerHandshake(ctrl, secret)
	if err != nil {
		slog.Error("handshake", "error", err)
		return
	}

	// Close session on context cancellation.
	go func() {
		<-ctx.Done()
		session.Close()
	}()

	if info.Dormant {
		handleDormantClient(ctx, conn, ctrl, session, info, reg, defaultCfg, logger)
	} else {
		handleLegacyInteractiveClient(ctx, conn, ctrl, session, info, reg, defaultCfg, logger)
	}
}

// handleDormantClient handles a polling dormant client.
// Entry persists in registry across disconnects.
func handleDormantClient(ctx context.Context, conn net.Conn, ctrl net.Conn, session *yamux.Session, info *proto.ClientInfoPayload, reg *registry.Registry, defaultCfg proto.SleepCfgPayload, logger *slog.Logger) {
	id, isReconnect := reg.FindOrRegister(info, session, ctrl, conn.RemoteAddr().String(), defaultCfg)
	defer reg.SetOfflineIfCtrl(id, ctrl)

	entry, _ := reg.Get(id)

	if isReconnect {
		slog.Info("client reconnected",
			"id", id,
			"hostname", info.Hostname,
			"remote", conn.RemoteAddr(),
		)
	} else {
		slog.Info("client registered",
			"id", id,
			"hostname", info.Hostname,
			"os", info.OS,
			"arch", info.Arch,
			"remote", conn.RemoteAddr(),
		)
		// Send default sleep configuration to new clients.
		if err := proto.WriteSleepCfg(ctrl, defaultCfg); err != nil {
			slog.Error("send sleep config", "id", id, "error", err)
			return
		}
		// Wait for ack.
		if err := waitForClientAck(ctrl, entry, 5*time.Second); err != nil {
			slog.Error("sleep config ack", "id", id, "error", err)
			return
		}
	}

	// Drain and send pending commands.
	pending := reg.DrainPending(id)
	hasWake := false
	for _, msg := range pending {
		if msg.Type == proto.CmdWake {
			hasWake = true
		}
		if err := proto.WriteMessage(ctrl, msg); err != nil {
			slog.Error("send pending command", "id", id, "type", fmt.Sprintf("0x%02x", msg.Type), "error", err)
			return
		}
		// Wait for ack for each command.
		if err := waitForClientAck(ctrl, entry, 5*time.Second); err != nil {
			slog.Warn("pending command ack failed", "id", id, "error", err)
			return
		}
	}

	if hasWake {
		// Client was woken — set online+active and notify shell.
		reg.SetOnline(id, true)
		reg.SetState(id, registry.StateActive)
		select {
		case reg.WakeCh <- registry.WakeEvent{ID: id}:
		default:
		}
		slog.Info("client woken", "id", id)
		// Run active control loop until sleep or disconnect.
		runActiveControlLoop(ctx, ctrl, id, entry, reg, logger)
		return
	}

	// No wake pending — tell client to disconnect.
	proto.WriteMessage(ctrl, &proto.Message{Type: proto.CmdCheckinDone})
	slog.Debug("check-in complete, sent CHECKIN_DONE", "id", id)
}

// handleLegacyInteractiveClient handles a non-dormant client in interactive mode
// using the original Register/Deregister flow.
func handleLegacyInteractiveClient(ctx context.Context, conn net.Conn, ctrl net.Conn, session *yamux.Session, info *proto.ClientInfoPayload, reg *registry.Registry, defaultCfg proto.SleepCfgPayload, logger *slog.Logger) {
	now := time.Now()
	entry := &registry.ClientEntry{
		Info:        info,
		Session:     session,
		Ctrl:        ctrl,
		State:       registry.StateActive,
		Online:      true,
		ConnectedAt: now,
		LastSeen:    now,
		RemoteAddr:  conn.RemoteAddr().String(),
		SleepCfg:    defaultCfg,
	}

	id := reg.Register(entry)
	defer reg.Deregister(id)

	slog.Info("client registered",
		"id", id,
		"hostname", info.Hostname,
		"os", info.OS,
		"arch", info.Arch,
		"remote", conn.RemoteAddr(),
	)

	runActiveControlLoop(ctx, ctrl, id, entry, reg, logger)
}

// runActiveControlLoop reads messages from the client until disconnect.
// Handles keepalive, ack/nack, and disconnect messages.
func runActiveControlLoop(ctx context.Context, ctrl net.Conn, id string, entry *registry.ClientEntry, reg *registry.Registry, logger *slog.Logger) {
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		msg, err := proto.ReadMessage(ctrl)
		if err != nil {
			slog.Info("client disconnected", "id", id, "error", err)
			return
		}

		switch msg.Type {
		case proto.Keepalive:
			proto.WriteMessage(ctrl, &proto.Message{Type: proto.KeepaliveAck})
			reg.UpdateLastSeen(id)

		case proto.Disconnect:
			slog.Info("client sent DISCONNECT", "id", id)
			return

		case proto.CmdAck:
			select {
			case entry.AckCh <- msg:
			default:
			}

		case proto.CmdNack:
			slog.Warn("client NACK", "id", id, "error", string(msg.Payload))
			select {
			case entry.AckCh <- msg:
			default:
			}

		default:
			slog.Debug("unexpected control message", "id", id, "type", fmt.Sprintf("0x%02x", msg.Type))
		}
	}
}

// waitForClientAck reads a single message from ctrl expecting CmdAck or CmdNack.
// Also forwards the ack to entry.AckCh for the shell.
func waitForClientAck(ctrl net.Conn, entry *registry.ClientEntry, timeout time.Duration) error {
	ctrl.SetReadDeadline(time.Now().Add(timeout))
	msg, err := proto.ReadMessage(ctrl)
	ctrl.SetReadDeadline(time.Time{})
	if err != nil {
		return fmt.Errorf("read ack: %w", err)
	}
	switch msg.Type {
	case proto.CmdAck:
		return nil
	case proto.CmdNack:
		return fmt.Errorf("NACK: %s", string(msg.Payload))
	default:
		return fmt.Errorf("unexpected message type 0x%02x waiting for ack", msg.Type)
	}
}

// parseForward parses "bind:target" format, e.g. "127.0.0.1:3306:dbhost:3306".
// Format: bindHost:bindPort:targetHost:targetPort
func parseForward(s string) (config.PortForward, error) {
	parts := strings.SplitN(s, ":", 4)
	if len(parts) == 4 {
		return config.PortForward{
			Bind:   parts[0] + ":" + parts[1],
			Target: parts[2] + ":" + parts[3],
		}, nil
	}
	// Also support "bindPort:targetHost:targetPort" (bind on all interfaces).
	if len(parts) == 3 {
		return config.PortForward{
			Bind:   "0.0.0.0:" + parts[0],
			Target: parts[1] + ":" + parts[2],
		}, nil
	}
	return config.PortForward{}, fmt.Errorf("invalid forward format %q (expected bind:target)", s)
}
