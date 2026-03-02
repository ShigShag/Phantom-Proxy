package shell

import (
	"bytes"
	"context"
	"io"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/ShigShag/Phantom-Proxy/internal/proto"
	"github.com/ShigShag/Phantom-Proxy/internal/proxy"
	"github.com/ShigShag/Phantom-Proxy/internal/registry"

	"log/slog"
)

func newTestShell(t *testing.T) (*Shell, *registry.Registry) {
	t.Helper()
	reg := registry.New()
	logger := slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))
	socks := proxy.NewSOCKS5Server(logger)
	sh := New(reg, socks, logger, "127.0.0.1:1080", nil)
	return sh, reg
}

// addTestClient adds an online client with a draining pipe (reads are consumed so writes don't block).
func addTestClient(t *testing.T, reg *registry.Registry, hostname string) (*registry.ClientEntry, net.Conn) {
	t.Helper()
	serverSide, clientSide := net.Pipe()
	t.Cleanup(func() {
		serverSide.Close()
		clientSide.Close()
	})

	// Drain the client side so writes to serverSide don't block.
	go io.Copy(io.Discard, clientSide)

	entry := &registry.ClientEntry{
		Info:        &proto.ClientInfoPayload{Hostname: hostname, OS: "linux", Arch: "amd64", Dormant: true},
		Ctrl:        serverSide,
		State:       registry.StateDormant,
		Online:      true,
		ConnectedAt: time.Now().Add(-1 * time.Hour),
		LastSeen:    time.Now(),
		RemoteAddr:  "192.168.1.1:4321",
		SleepCfg:    proto.SleepCfgPayload{IntervalSec: 30, JitterPct: 0},
	}
	reg.Register(entry)
	return entry, clientSide
}

// addOfflineClient adds an offline client (no active connection).
func addOfflineClient(t *testing.T, reg *registry.Registry, hostname string) *registry.ClientEntry {
	t.Helper()
	entry := &registry.ClientEntry{
		Info:        &proto.ClientInfoPayload{Hostname: hostname, OS: "linux", Arch: "amd64", Dormant: true},
		State:       registry.StateDormant,
		Online:      false,
		ConnectedAt: time.Now().Add(-1 * time.Hour),
		LastSeen:    time.Now().Add(-5 * time.Minute),
		RemoteAddr:  "192.168.1.1:4321",
		SleepCfg:    proto.SleepCfgPayload{IntervalSec: 30, JitterPct: 0},
	}
	reg.Register(entry)
	return entry
}

func runShellCommand(t *testing.T, sh *Shell, command string) string {
	t.Helper()
	var out bytes.Buffer
	input := strings.NewReader(command + "\n")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	sh.RunWithIO(ctx, input, &out)
	return out.String()
}

func TestHelpCommand(t *testing.T) {
	sh, _ := newTestShell(t)
	output := runShellCommand(t, sh, "help")
	if !strings.Contains(output, "Commands:") {
		t.Fatal("help should print command list")
	}
	if !strings.Contains(output, "list") {
		t.Fatal("help should mention list command")
	}
	if !strings.Contains(output, "use") {
		t.Fatal("help should mention use command")
	}
}

func TestListEmpty(t *testing.T) {
	sh, _ := newTestShell(t)
	output := runShellCommand(t, sh, "list")
	if !strings.Contains(output, "No connected clients") {
		t.Fatal("list should show no clients message")
	}
}

func TestListWithClients(t *testing.T) {
	sh, reg := newTestShell(t)
	addTestClient(t, reg, "web-prod")
	addTestClient(t, reg, "dev-box")

	output := runShellCommand(t, sh, "ls")
	if !strings.Contains(output, "web-prod") {
		t.Fatal("list should show web-prod")
	}
	if !strings.Contains(output, "dev-box") {
		t.Fatal("list should show dev-box")
	}
	if !strings.Contains(output, "HOSTNAME") {
		t.Fatal("list should show header")
	}
	if !strings.Contains(output, "ONLINE") {
		t.Fatal("list should show ONLINE column")
	}
}

func TestInfoCommand(t *testing.T) {
	sh, reg := newTestShell(t)
	entry, _ := addTestClient(t, reg, "test-host")

	output := runShellCommand(t, sh, "info "+entry.ID)
	if !strings.Contains(output, entry.ID) {
		t.Fatal("info should show ID")
	}
	if !strings.Contains(output, "test-host") {
		t.Fatal("info should show hostname")
	}
	if !strings.Contains(output, "linux") {
		t.Fatal("info should show OS")
	}
	if !strings.Contains(output, "dormant") {
		t.Fatal("info should show state")
	}
	if !strings.Contains(output, "Online:") {
		t.Fatal("info should show online field")
	}
}

func TestInfoNotFound(t *testing.T) {
	sh, _ := newTestShell(t)
	output := runShellCommand(t, sh, "info zzzz")
	if !strings.Contains(output, "client not found") {
		t.Fatal("info should report not found")
	}
}

func TestStatusCommand(t *testing.T) {
	sh, reg := newTestShell(t)
	addTestClient(t, reg, "host-a")

	output := runShellCommand(t, sh, "status")
	if !strings.Contains(output, "1 total") {
		t.Fatalf("status should show client count, got: %s", output)
	}
	if !strings.Contains(output, "1 online") {
		t.Fatalf("status should show online count, got: %s", output)
	}
	if !strings.Contains(output, "(none)") {
		t.Fatal("status should show no active client")
	}
	if !strings.Contains(output, "127.0.0.1:1080") {
		t.Fatal("status should show socks address")
	}
}

func TestUnknownCommand(t *testing.T) {
	sh, _ := newTestShell(t)
	output := runShellCommand(t, sh, "foobar")
	if !strings.Contains(output, "unknown command: foobar") {
		t.Fatal("should report unknown command")
	}
}

func TestPromptDefault(t *testing.T) {
	sh, _ := newTestShell(t)
	output := runShellCommand(t, sh, "help")
	if !strings.Contains(output, "phantom> ") {
		t.Fatal("should show default prompt")
	}
}

func TestKickCommand(t *testing.T) {
	sh, reg := newTestShell(t)
	entry, _ := addTestClient(t, reg, "kickme")

	output := runShellCommand(t, sh, "kick "+entry.ID)
	if !strings.Contains(output, "kicked client") {
		t.Fatalf("kick should confirm: %s", output)
	}
	if reg.Count() != 0 {
		t.Fatal("client should be deregistered after kick")
	}
}

func TestKickOfflineClient(t *testing.T) {
	sh, reg := newTestShell(t)
	entry := addOfflineClient(t, reg, "offline-kick")

	output := runShellCommand(t, sh, "kick "+entry.ID)
	if !strings.Contains(output, "kicked client") {
		t.Fatalf("kick should confirm: %s", output)
	}
	if reg.Count() != 0 {
		t.Fatal("client should be deregistered after kick")
	}
}

func TestSleepCommandNotFound(t *testing.T) {
	sh, _ := newTestShell(t)
	output := runShellCommand(t, sh, "sleep zzzz")
	if !strings.Contains(output, "client not found") {
		t.Fatal("sleep should report not found")
	}
}

func TestUseCommandNotFound(t *testing.T) {
	sh, _ := newTestShell(t)
	output := runShellCommand(t, sh, "use zzzz")
	if !strings.Contains(output, "client not found") {
		t.Fatal("use should report not found")
	}
}

func TestUsageMissing(t *testing.T) {
	sh, _ := newTestShell(t)

	for _, cmd := range []string{"use", "sleep", "kick", "info", "interval"} {
		output := runShellCommand(t, sh, cmd)
		if !strings.Contains(output, "usage:") {
			t.Fatalf("%s without args should show usage", cmd)
		}
	}
}

func TestIntervalInvalidDuration(t *testing.T) {
	sh, reg := newTestShell(t)
	entry, _ := addTestClient(t, reg, "test-host")

	output := runShellCommand(t, sh, "interval "+entry.ID+" notaduration")
	if !strings.Contains(output, "invalid duration") {
		t.Fatal("should report invalid duration")
	}
}

func TestIntervalInvalidJitter(t *testing.T) {
	sh, reg := newTestShell(t)
	entry, _ := addTestClient(t, reg, "test-host")

	output := runShellCommand(t, sh, "interval "+entry.ID+" 5m 200")
	if !strings.Contains(output, "jitter must be 0-100") {
		t.Fatal("should reject jitter > 100")
	}
}

func TestFormatDuration(t *testing.T) {
	tests := []struct {
		d    time.Duration
		want string
	}{
		{5 * time.Second, "5s ago"},
		{90 * time.Second, "1m ago"},
		{2 * time.Hour, "2h ago"},
		{48 * time.Hour, "2d ago"},
	}
	for _, tc := range tests {
		got := formatDuration(tc.d)
		if got != tc.want {
			t.Errorf("formatDuration(%v) = %q, want %q", tc.d, got, tc.want)
		}
	}
}

func TestSleepAllEmpty(t *testing.T) {
	sh, _ := newTestShell(t)
	output := runShellCommand(t, sh, "sleep-all")
	if !strings.Contains(output, "slept 0 client(s)") {
		t.Fatal("sleep-all with no clients should report 0")
	}
}

func TestUseOfflineQueuesWake(t *testing.T) {
	sh, reg := newTestShell(t)
	entry := addOfflineClient(t, reg, "offline-host")

	output := runShellCommand(t, sh, "use "+entry.ID)
	if !strings.Contains(output, "offline") {
		t.Fatalf("use on offline client should report offline, got: %s", output)
	}
	if !strings.Contains(output, "queued") {
		t.Fatalf("use on offline client should queue wake, got: %s", output)
	}

	cmds := reg.DrainPending(entry.ID)
	if len(cmds) != 1 || cmds[0].Type != proto.CmdWake {
		t.Fatalf("expected 1 CmdWake queued, got %d cmds", len(cmds))
	}
}

func TestSleepOfflineQueuesSleep(t *testing.T) {
	sh, reg := newTestShell(t)
	entry := addOfflineClient(t, reg, "offline-host")

	output := runShellCommand(t, sh, "sleep "+entry.ID)
	if !strings.Contains(output, "offline") {
		t.Fatalf("sleep on offline client should report offline, got: %s", output)
	}
	if !strings.Contains(output, "queued") {
		t.Fatalf("sleep on offline client should queue sleep, got: %s", output)
	}

	cmds := reg.DrainPending(entry.ID)
	if len(cmds) != 1 || cmds[0].Type != proto.CmdSleep {
		t.Fatalf("expected 1 CmdSleep queued, got %d cmds", len(cmds))
	}
}

func TestIntervalOfflineQueues(t *testing.T) {
	sh, reg := newTestShell(t)
	entry := addOfflineClient(t, reg, "offline-host")

	output := runShellCommand(t, sh, "interval "+entry.ID+" 5m 30")
	if !strings.Contains(output, "offline") {
		t.Fatalf("interval on offline client should report offline, got: %s", output)
	}
	if !strings.Contains(output, "queued") {
		t.Fatalf("interval on offline client should queue, got: %s", output)
	}

	cmds := reg.DrainPending(entry.ID)
	if len(cmds) != 1 || cmds[0].Type != proto.CmdSleepCfg {
		t.Fatalf("expected 1 CmdSleepCfg queued, got %d cmds", len(cmds))
	}
}

func TestListShowsOnlineColumn(t *testing.T) {
	sh, reg := newTestShell(t)
	addTestClient(t, reg, "online-host")
	addOfflineClient(t, reg, "offline-host")

	output := runShellCommand(t, sh, "ls")
	if !strings.Contains(output, "online") {
		t.Fatal("list should show online status")
	}
	if !strings.Contains(output, "offline") {
		t.Fatal("list should show offline status")
	}
}

func TestStatusShowsOnlineOffline(t *testing.T) {
	sh, reg := newTestShell(t)
	addTestClient(t, reg, "online-host")
	addOfflineClient(t, reg, "offline-host")

	output := runShellCommand(t, sh, "status")
	if !strings.Contains(output, "2 total") {
		t.Fatalf("status should show 2 total, got: %s", output)
	}
	if !strings.Contains(output, "1 online") {
		t.Fatalf("status should show 1 online, got: %s", output)
	}
	if !strings.Contains(output, "1 offline") {
		t.Fatalf("status should show 1 offline, got: %s", output)
	}
}
