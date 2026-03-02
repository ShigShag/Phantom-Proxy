package shell

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/chzyer/readline"

	"github.com/ShigShag/Phantom-Proxy/internal/proto"
	"github.com/ShigShag/Phantom-Proxy/internal/proxy"
	"github.com/ShigShag/Phantom-Proxy/internal/registry"
)

// Shell provides an interactive command-line interface for C&C operations.
type Shell struct {
	registry  *registry.Registry
	socks     *proxy.SOCKS5Server
	logger    *slog.Logger
	activeID  string
	startTime time.Time
	socksAddr string
	shutdown  func() // called on exit to terminate the server
}

// New creates a new Shell. The shutdown function is called when the user exits the shell.
func New(reg *registry.Registry, socks *proxy.SOCKS5Server, logger *slog.Logger, socksAddr string, shutdown func()) *Shell {
	return &Shell{
		registry:  reg,
		socks:     socks,
		logger:    logger,
		startTime: time.Now(),
		socksAddr: socksAddr,
		shutdown:  shutdown,
	}
}

// readlineResult holds the result of a Readline() call.
type readlineResult struct {
	line string
	err  error
}

// Run starts the interactive shell with readline support
// (line editing, history, tab completion).
func (s *Shell) Run(ctx context.Context) {
	rl, err := readline.NewEx(&readline.Config{
		Prompt:            s.getPrompt(),
		AutoComplete:      s.newCompleter(),
		InterruptPrompt:   "^C",
		EOFPrompt:         "exit",
		HistorySearchFold: true,
	})
	if err != nil {
		slog.Error("readline init", "error", err)
		return
	}
	defer rl.Close()

	// Redirect slog through readline so log output doesn't corrupt the prompt.
	level := detectLevel(slog.Default().Handler())
	prevLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(rl.Stderr(), &slog.HandlerOptions{Level: level})))
	defer slog.SetDefault(prevLogger)

	fmt.Fprintln(rl.Stdout(), "Type 'help' for commands. Tab for completion, \u2191/\u2193 for history.")

	// Run readline in a goroutine so we can select on wake notifications.
	lineCh := make(chan readlineResult, 1)
	readLine := func() {
		line, err := rl.Readline()
		lineCh <- readlineResult{line, err}
	}
	go readLine()

	for {
		select {
		case <-ctx.Done():
			return

		case ev := <-s.registry.WakeCh:
			s.handleWakeEvent(ev, rl)

		case res := <-lineCh:
			if res.err != nil {
				if res.err == readline.ErrInterrupt {
					rl.SetPrompt(s.getPrompt())
					go readLine()
					continue
				}
				return
			}

			line := strings.TrimSpace(res.line)
			if line == "" {
				rl.SetPrompt(s.getPrompt())
				go readLine()
				continue
			}

			if s.dispatch(line, rl.Stdout()) {
				return
			}
			rl.SetPrompt(s.getPrompt())
			go readLine()
		}
	}
}

// handleWakeEvent processes a wake notification, updates the prompt, and refreshes readline.
func (s *Shell) handleWakeEvent(ev registry.WakeEvent, rl *readline.Instance) {
	entry, ok := s.registry.Get(ev.ID)
	if !ok {
		return
	}
	hostname := ""
	if entry.Info != nil {
		hostname = entry.Info.Hostname
	}
	if s.activeID == "" {
		s.activateClient(ev.ID, entry, rl.Stdout())
		fmt.Fprintf(rl.Stdout(), "[notification] client %s (%s) checked in and was woken — auto-activated\n", ev.ID, hostname)
	} else {
		fmt.Fprintf(rl.Stdout(), "[notification] client %s (%s) checked in and was woken\n", ev.ID, hostname)
	}
	rl.SetPrompt(s.getPrompt())
	rl.Refresh()
}

// RunWithIO reads commands from r and writes output to w (for testing).
func (s *Shell) RunWithIO(ctx context.Context, r io.Reader, w io.Writer) {
	scanner := bufio.NewScanner(r)
	s.drainWakeNotifications(w)
	s.printPrompt(w)

	for scanner.Scan() {
		select {
		case <-ctx.Done():
			return
		default:
		}

		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			s.drainWakeNotifications(w)
			s.printPrompt(w)
			continue
		}

		if s.dispatch(line, w) {
			return
		}

		s.drainWakeNotifications(w)
		s.printPrompt(w)
	}
}

// dispatch processes a single command line. Returns true if the shell should exit.
func (s *Shell) dispatch(line string, w io.Writer) bool {
	parts := strings.Fields(line)
	cmd := parts[0]
	args := parts[1:]

	switch cmd {
	case "help":
		s.cmdHelp(w)
	case "list", "ls":
		s.cmdList(w)
	case "use":
		s.cmdUse(w, args)
	case "sleep":
		s.cmdSleep(w, args)
	case "sleep-all":
		s.cmdSleepAll(w)
	case "kick":
		s.cmdKick(w, args)
	case "info":
		s.cmdInfo(w, args)
	case "interval":
		s.cmdInterval(w, args)
	case "status":
		s.cmdStatus(w)
	case "exit", "quit":
		s.cmdExit(w)
		return true
	default:
		fmt.Fprintf(w, "unknown command: %s. Type 'help' for available commands.\n", cmd)
	}
	return false
}

// getPrompt returns the current prompt string, clearing the active client if it went offline.
func (s *Shell) getPrompt() string {
	if s.activeID != "" {
		if e, ok := s.registry.Get(s.activeID); !ok || !e.Online {
			s.socks.SetSession(nil)
			s.activeID = ""
		}
	}
	if s.activeID != "" {
		return fmt.Sprintf("phantom [%s]> ", s.activeID)
	}
	return "phantom> "
}

func (s *Shell) printPrompt(w io.Writer) {
	fmt.Fprint(w, s.getPrompt())
}

// newCompleter builds the tab-completion tree.
func (s *Shell) newCompleter() *readline.PrefixCompleter {
	return readline.NewPrefixCompleter(
		readline.PcItem("help"),
		readline.PcItem("list"),
		readline.PcItem("ls"),
		readline.PcItem("use", readline.PcItemDynamic(s.clientIDCompleter)),
		readline.PcItem("sleep", readline.PcItemDynamic(s.clientIDCompleter)),
		readline.PcItem("sleep-all"),
		readline.PcItem("kick", readline.PcItemDynamic(s.clientIDCompleter)),
		readline.PcItem("info", readline.PcItemDynamic(s.clientIDCompleter)),
		readline.PcItem("interval", readline.PcItemDynamic(s.clientIDCompleter)),
		readline.PcItem("status"),
		readline.PcItem("exit"),
		readline.PcItem("quit"),
	)
}

// clientIDCompleter returns all known client IDs for tab completion.
func (s *Shell) clientIDCompleter(string) []string {
	entries := s.registry.List()
	ids := make([]string, 0, len(entries))
	for _, e := range entries {
		ids = append(ids, e.ID)
	}
	return ids
}

// detectLevel probes an slog.Handler to find the minimum enabled log level.
func detectLevel(h slog.Handler) slog.Level {
	ctx := context.Background()
	for _, l := range []slog.Level{slog.LevelDebug, slog.LevelInfo, slog.LevelWarn, slog.LevelError} {
		if h.Enabled(ctx, l) {
			return l
		}
	}
	return slog.LevelError
}

func (s *Shell) cmdHelp(w io.Writer) {
	fmt.Fprintln(w, "Commands:")
	fmt.Fprintln(w, "  help                          Show this help message")
	fmt.Fprintln(w, "  list, ls                      List all clients (online and offline)")
	fmt.Fprintln(w, "  use <id>                      Activate a client (route SOCKS5 through it)")
	fmt.Fprintln(w, "  sleep <id>                    Return a client to dormant state")
	fmt.Fprintln(w, "  sleep-all                     Sleep all active clients")
	fmt.Fprintln(w, "  kick <id>                     Disconnect and deregister a client")
	fmt.Fprintln(w, "  info <id>                     Show detailed client info")
	fmt.Fprintln(w, "  interval <id> <dur> [jitter%] Set beacon interval (e.g. interval a3f1 5m 30)")
	fmt.Fprintln(w, "  status                        Show server status")
	fmt.Fprintln(w, "  exit, quit                    Disconnect all clients and shutdown")
}

func (s *Shell) cmdList(w io.Writer) {
	entries := s.registry.List()
	if len(entries) == 0 {
		fmt.Fprintln(w, "No connected clients.")
		return
	}

	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tHOSTNAME\tOS\tARCH\tSTATE\tONLINE\tREMOTE\tCONNECTED\tLAST_SEEN")
	for _, e := range entries {
		hostname := ""
		osName := ""
		arch := ""
		if e.Info != nil {
			hostname = e.Info.Hostname
			osName = e.Info.OS
			arch = e.Info.Arch
		}
		online := "offline"
		lastSeen := formatDuration(time.Since(e.LastSeen))
		if e.Online {
			online = "online"
			lastSeen = "now"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			e.ID,
			hostname,
			osName,
			arch,
			e.State.String(),
			online,
			e.RemoteAddr,
			formatDuration(time.Since(e.ConnectedAt)),
			lastSeen,
		)
	}
	tw.Flush()
}

func (s *Shell) cmdUse(w io.Writer, args []string) {
	if len(args) < 1 {
		fmt.Fprintln(w, "usage: use <id>")
		return
	}
	id := args[0]

	entry, ok := s.registry.Get(id)
	if !ok {
		fmt.Fprintf(w, "client not found: %s\n", id)
		return
	}

	// If another client is active, sleep it first.
	if s.activeID != "" && s.activeID != id {
		if prev, ok := s.registry.Get(s.activeID); ok && prev.Online {
			fmt.Fprintf(w, "sleeping previous active client %s...\n", s.activeID)
			if err := s.sleepClient(prev); err != nil {
				fmt.Fprintf(w, "warning: failed to sleep %s: %v\n", s.activeID, err)
			}
		}
		s.socks.SetSession(nil)
		s.activeID = ""
	}

	if !entry.Online {
		// Client is offline — queue wake for next check-in.
		s.registry.QueueCmd(id, &proto.Message{Type: proto.CmdWake})
		fmt.Fprintf(w, "client %s is offline — WAKE queued for next check-in\n", id)
		return
	}

	// Client is already active (non-dormant) — just route SOCKS5 through it.
	if entry.State == registry.StateActive {
		s.activateClient(id, entry, w)
		return
	}

	// Client is online but dormant (checking in) — send wake directly.
	if err := proto.WriteMessage(entry.Ctrl, &proto.Message{Type: proto.CmdWake}); err != nil {
		fmt.Fprintf(w, "failed to send WAKE to %s: %v\n", id, err)
		return
	}

	// Wait for ack.
	if err := s.waitForAck(entry, 5*time.Second); err != nil {
		fmt.Fprintf(w, "WAKE ack timeout for %s: %v\n", id, err)
		return
	}

	s.activateClient(id, entry, w)
}

func (s *Shell) cmdSleep(w io.Writer, args []string) {
	if len(args) < 1 {
		fmt.Fprintln(w, "usage: sleep <id>")
		return
	}
	id := args[0]

	entry, ok := s.registry.Get(id)
	if !ok {
		fmt.Fprintf(w, "client not found: %s\n", id)
		return
	}

	if !entry.Online {
		// Queue sleep for next check-in.
		s.registry.QueueCmd(id, &proto.Message{Type: proto.CmdSleep})
		if s.activeID == id {
			s.socks.SetSession(nil)
			s.activeID = ""
		}
		fmt.Fprintf(w, "client %s is offline — SLEEP queued for next check-in\n", id)
		return
	}

	// Non-dormant (legacy) clients don't support CmdSleep — just deactivate SOCKS5 routing.
	if entry.Info != nil && !entry.Info.Dormant {
		if s.activeID == id {
			s.socks.SetSession(nil)
			s.activeID = ""
		}
		fmt.Fprintf(w, "client %s deactivated\n", id)
		return
	}

	if err := s.sleepClient(entry); err != nil {
		fmt.Fprintf(w, "failed to sleep %s: %v\n", id, err)
		return
	}

	if s.activeID == id {
		s.socks.SetSession(nil)
		s.activeID = ""
	}

	fmt.Fprintf(w, "client %s is now dormant\n", id)
}

func (s *Shell) cmdSleepAll(w io.Writer) {
	entries := s.registry.List()
	count := 0
	for _, e := range entries {
		if e.State == registry.StateActive && e.Online {
			// Non-dormant clients: just deactivate (no CmdSleep).
			if e.Info != nil && !e.Info.Dormant {
				count++
				continue
			}
			if err := s.sleepClient(e); err != nil {
				fmt.Fprintf(w, "failed to sleep %s: %v\n", e.ID, err)
				continue
			}
			count++
		}
	}
	if s.activeID != "" {
		s.socks.SetSession(nil)
		s.activeID = ""
	}
	fmt.Fprintf(w, "slept %d client(s)\n", count)
}

func (s *Shell) cmdKick(w io.Writer, args []string) {
	if len(args) < 1 {
		fmt.Fprintln(w, "usage: kick <id>")
		return
	}
	id := args[0]

	entry, ok := s.registry.Get(id)
	if !ok {
		fmt.Fprintf(w, "client not found: %s\n", id)
		return
	}

	if entry.Online {
		proto.WriteMessage(entry.Ctrl, &proto.Message{Type: proto.Disconnect})
		if entry.Session != nil {
			entry.Session.Close()
		}
	}
	s.registry.Deregister(id)

	if s.activeID == id {
		s.socks.SetSession(nil)
		s.activeID = ""
	}

	fmt.Fprintf(w, "kicked client %s\n", id)
}

func (s *Shell) cmdInfo(w io.Writer, args []string) {
	if len(args) < 1 {
		fmt.Fprintln(w, "usage: info <id>")
		return
	}
	id := args[0]

	e, ok := s.registry.Get(id)
	if !ok {
		fmt.Fprintf(w, "client not found: %s\n", id)
		return
	}

	hostname := ""
	osName := ""
	arch := ""
	if e.Info != nil {
		hostname = e.Info.Hostname
		osName = e.Info.OS
		arch = e.Info.Arch
	}

	online := "no"
	if e.Online {
		online = "yes"
	}

	fmt.Fprintf(w, "ID:          %s\n", e.ID)
	fmt.Fprintf(w, "Hostname:    %s\n", hostname)
	fmt.Fprintf(w, "OS:          %s\n", osName)
	fmt.Fprintf(w, "Arch:        %s\n", arch)
	fmt.Fprintf(w, "State:       %s\n", e.State.String())
	fmt.Fprintf(w, "Online:      %s\n", online)
	fmt.Fprintf(w, "Remote:      %s\n", e.RemoteAddr)
	fmt.Fprintf(w, "Connected:   %s (%s)\n", e.ConnectedAt.Format(time.DateTime), formatDuration(time.Since(e.ConnectedAt)))
	if e.Online {
		fmt.Fprintf(w, "Last Seen:   now\n")
	} else {
		fmt.Fprintf(w, "Last Seen:   %s (%s)\n", e.LastSeen.Format(time.DateTime), formatDuration(time.Since(e.LastSeen)))
	}
	fmt.Fprintf(w, "Sleep Cfg:   interval=%ds, jitter=%d%%\n", e.SleepCfg.IntervalSec, e.SleepCfg.JitterPct)
}

func (s *Shell) cmdInterval(w io.Writer, args []string) {
	if len(args) < 2 {
		fmt.Fprintln(w, "usage: interval <id> <duration> [jitter%]")
		return
	}
	id := args[0]

	entry, ok := s.registry.Get(id)
	if !ok {
		fmt.Fprintf(w, "client not found: %s\n", id)
		return
	}

	dur, err := time.ParseDuration(args[1])
	if err != nil {
		fmt.Fprintf(w, "invalid duration: %v\n", err)
		return
	}

	jitter := 0
	if len(args) >= 3 {
		jitter, err = strconv.Atoi(args[2])
		if err != nil || jitter < 0 || jitter > 100 {
			fmt.Fprintln(w, "jitter must be 0-100")
			return
		}
	}

	cfg := proto.SleepCfgPayload{
		IntervalSec: int(dur.Seconds()),
		JitterPct:   jitter,
	}

	if !entry.Online {
		// Queue for next check-in.
		payload, _ := json.Marshal(cfg)
		s.registry.QueueCmd(id, &proto.Message{Type: proto.CmdSleepCfg, Payload: payload})
		entry.SleepCfg = cfg
		fmt.Fprintf(w, "client %s is offline — interval update queued for next check-in\n", id)
		return
	}

	// Non-dormant clients don't read C&C on ctrl — just store the config server-side.
	if entry.Info != nil && !entry.Info.Dormant {
		entry.SleepCfg = cfg
		fmt.Fprintf(w, "updated %s: interval=%s, jitter=%d%% (stored, will apply when client reconnects as dormant)\n", id, dur, jitter)
		return
	}

	if err := proto.WriteSleepCfg(entry.Ctrl, cfg); err != nil {
		fmt.Fprintf(w, "failed to send sleep config to %s: %v\n", id, err)
		return
	}

	if err := s.waitForAck(entry, 5*time.Second); err != nil {
		fmt.Fprintf(w, "ack timeout for %s: %v\n", id, err)
		return
	}

	entry.SleepCfg = cfg
	fmt.Fprintf(w, "updated %s: interval=%s, jitter=%d%%\n", id, dur, jitter)
}

func (s *Shell) cmdStatus(w io.Writer) {
	entries := s.registry.List()
	total := len(entries)
	onlineCount := 0
	activeCount := 0
	offlineCount := 0
	for _, e := range entries {
		if e.Online {
			onlineCount++
		} else {
			offlineCount++
		}
		if e.State == registry.StateActive {
			activeCount++
		}
	}

	fmt.Fprintf(w, "Clients:     %d total, %d online, %d offline, %d active\n", total, onlineCount, offlineCount, activeCount)
	if s.activeID != "" {
		fmt.Fprintf(w, "Active:      %s\n", s.activeID)
	} else {
		fmt.Fprintln(w, "Active:      (none)")
	}
	fmt.Fprintf(w, "SOCKS5:      %s\n", s.socksAddr)
	fmt.Fprintf(w, "Uptime:      %s\n", formatDuration(time.Since(s.startTime)))
}

func (s *Shell) cmdExit(w io.Writer) {
	entries := s.registry.List()
	for _, e := range entries {
		if e.Online {
			proto.WriteMessage(e.Ctrl, &proto.Message{Type: proto.Disconnect})
			if e.Session != nil {
				e.Session.Close()
			}
		}
	}
	fmt.Fprintf(w, "sent DISCONNECT to %d client(s), shutting down\n", len(entries))
	if s.shutdown != nil {
		s.shutdown()
	}
}

// activateClient sets up the SOCKS5 session and activeID for a woken client.
func (s *Shell) activateClient(id string, entry *registry.ClientEntry, w io.Writer) {
	s.registry.SetState(id, registry.StateActive)
	s.socks.SetSession(entry.Session)
	s.activeID = id

	hostname := ""
	if entry.Info != nil {
		hostname = entry.Info.Hostname
	}
	fmt.Fprintf(w, "activated client %s (%s) — SOCKS5 traffic now routes through this client\n", id, hostname)
}

// drainWakeNotifications reads pending wake notifications from the registry and
// auto-activates the client if no other client is active.
func (s *Shell) drainWakeNotifications(w io.Writer) {
	for {
		select {
		case ev := <-s.registry.WakeCh:
			entry, ok := s.registry.Get(ev.ID)
			if !ok {
				continue
			}
			hostname := ""
			if entry.Info != nil {
				hostname = entry.Info.Hostname
			}
			if s.activeID == "" {
				s.activateClient(ev.ID, entry, w)
				fmt.Fprintf(w, "[notification] client %s (%s) checked in and was woken — auto-activated\n", ev.ID, hostname)
			} else {
				fmt.Fprintf(w, "[notification] client %s (%s) checked in and was woken\n", ev.ID, hostname)
			}
		default:
			return
		}
	}
}

// sleepClient sends CmdSleep and waits for ack.
func (s *Shell) sleepClient(entry *registry.ClientEntry) error {
	if err := proto.WriteMessage(entry.Ctrl, &proto.Message{Type: proto.CmdSleep}); err != nil {
		return fmt.Errorf("send SLEEP: %w", err)
	}
	if err := s.waitForAck(entry, 5*time.Second); err != nil {
		return fmt.Errorf("SLEEP ack: %w", err)
	}
	s.registry.SetState(entry.ID, registry.StateDormant)
	return nil
}

// waitForAck waits for a CmdAck or CmdNack on the entry's AckCh with a timeout.
func (s *Shell) waitForAck(entry *registry.ClientEntry, timeout time.Duration) error {
	select {
	case msg := <-entry.AckCh:
		if msg.Type == proto.CmdNack {
			return fmt.Errorf("NACK: %s", string(msg.Payload))
		}
		return nil
	case <-time.After(timeout):
		return fmt.Errorf("timeout")
	}
}

// formatDuration formats a duration as a human-readable string.
func formatDuration(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%ds ago", int(d.Seconds()))
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	}
	if d < 24*time.Hour {
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	}
	return fmt.Sprintf("%dd ago", int(d.Hours()/24))
}
