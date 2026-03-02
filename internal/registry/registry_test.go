package registry

import (
	"net"
	"sync"
	"testing"
	"time"

	"github.com/ShigShag/Phantom-Proxy/internal/proto"
)

func newTestEntry(hostname string) *ClientEntry {
	return &ClientEntry{
		Info:        &proto.ClientInfoPayload{Hostname: hostname, OS: "linux", Arch: "amd64"},
		ConnectedAt: time.Now(),
		LastSeen:    time.Now(),
		RemoteAddr:  "127.0.0.1:1234",
		State:       StateDormant,
	}
}

func TestRegisterAndGet(t *testing.T) {
	r := New()

	id := r.Register(newTestEntry("host1"))
	if len(id) != 4 {
		t.Fatalf("expected 4-char ID, got %q", id)
	}

	e, ok := r.Get(id)
	if !ok {
		t.Fatal("expected to find entry")
	}
	if e.Info.Hostname != "host1" {
		t.Fatalf("hostname = %q, want host1", e.Info.Hostname)
	}
	if e.ID != id {
		t.Fatalf("entry ID = %q, want %q", e.ID, id)
	}
	if e.AckCh == nil {
		t.Fatal("AckCh should be initialized")
	}
}

func TestDeregister(t *testing.T) {
	r := New()
	id := r.Register(newTestEntry("host1"))

	r.Deregister(id)
	if _, ok := r.Get(id); ok {
		t.Fatal("entry should be gone after deregister")
	}
	if r.Count() != 0 {
		t.Fatalf("count = %d, want 0", r.Count())
	}
}

func TestList(t *testing.T) {
	r := New()

	e1 := newTestEntry("host-a")
	e1.ConnectedAt = time.Now().Add(-2 * time.Hour)
	r.Register(e1)

	e2 := newTestEntry("host-b")
	e2.ConnectedAt = time.Now().Add(-1 * time.Hour)
	r.Register(e2)

	entries := r.List()
	if len(entries) != 2 {
		t.Fatalf("len = %d, want 2", len(entries))
	}
	if entries[0].Info.Hostname != "host-a" {
		t.Fatalf("first entry = %q, want host-a", entries[0].Info.Hostname)
	}
	if entries[1].Info.Hostname != "host-b" {
		t.Fatalf("second entry = %q, want host-b", entries[1].Info.Hostname)
	}
}

func TestFindByHostname(t *testing.T) {
	r := New()
	r.Register(newTestEntry("web-prod"))
	r.Register(newTestEntry("dev-box"))

	e, ok := r.FindByHostname("dev-box")
	if !ok {
		t.Fatal("expected to find dev-box")
	}
	if e.Info.Hostname != "dev-box" {
		t.Fatalf("hostname = %q, want dev-box", e.Info.Hostname)
	}

	_, ok = r.FindByHostname("nonexistent")
	if ok {
		t.Fatal("should not find nonexistent host")
	}
}

func TestUpdateLastSeen(t *testing.T) {
	r := New()
	e := newTestEntry("host1")
	e.LastSeen = time.Now().Add(-1 * time.Hour)
	id := r.Register(e)

	before := e.LastSeen
	r.UpdateLastSeen(id)

	got, _ := r.Get(id)
	if !got.LastSeen.After(before) {
		t.Fatal("LastSeen should have been updated")
	}
}

func TestSetState(t *testing.T) {
	r := New()
	id := r.Register(newTestEntry("host1"))

	r.SetState(id, StateActive)
	e, _ := r.Get(id)
	if e.State != StateActive {
		t.Fatalf("state = %v, want StateActive", e.State)
	}

	r.SetState(id, StateDormant)
	e, _ = r.Get(id)
	if e.State != StateDormant {
		t.Fatalf("state = %v, want StateDormant", e.State)
	}
}

func TestCount(t *testing.T) {
	r := New()
	if r.Count() != 0 {
		t.Fatal("expected 0")
	}
	r.Register(newTestEntry("a"))
	r.Register(newTestEntry("b"))
	if r.Count() != 2 {
		t.Fatalf("count = %d, want 2", r.Count())
	}
}

func TestUniqueIDs(t *testing.T) {
	r := New()
	ids := make(map[string]bool)
	for range 100 {
		id := r.Register(newTestEntry("host"))
		if ids[id] {
			t.Fatalf("duplicate ID: %s", id)
		}
		ids[id] = true
	}
}

func TestConcurrentAccess(t *testing.T) {
	r := New()

	var wg sync.WaitGroup
	// Concurrent registrations.
	for range 50 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			id := r.Register(newTestEntry("concurrent"))
			r.UpdateLastSeen(id)
			r.SetState(id, StateActive)
			r.Get(id)
			r.List()
			r.FindByHostname("concurrent")
			r.Count()
		}()
	}
	wg.Wait()

	if r.Count() != 50 {
		t.Fatalf("count = %d, want 50", r.Count())
	}

	// Concurrent deregistrations.
	entries := r.List()
	for _, e := range entries {
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			r.Deregister(id)
		}(e.ID)
	}
	wg.Wait()

	if r.Count() != 0 {
		t.Fatalf("count after deregister = %d, want 0", r.Count())
	}
}

func TestStateString(t *testing.T) {
	if StateDormant.String() != "dormant" {
		t.Fatalf("dormant string = %q", StateDormant.String())
	}
	if StateActive.String() != "active" {
		t.Fatalf("active string = %q", StateActive.String())
	}
}

func TestFindOrRegisterNew(t *testing.T) {
	r := New()
	info := &proto.ClientInfoPayload{Hostname: "new-host", OS: "linux", Arch: "amd64"}
	cfg := proto.SleepCfgPayload{IntervalSec: 30, JitterPct: 10}

	id, isReconnect := r.FindOrRegister(info, nil, nil, "10.0.0.1:1234", cfg)
	if isReconnect {
		t.Fatal("expected new registration, got reconnect")
	}
	if len(id) != 4 {
		t.Fatalf("expected 4-char ID, got %q", id)
	}
	e, ok := r.Get(id)
	if !ok {
		t.Fatal("expected to find entry")
	}
	if e.Online {
		t.Fatal("new entry should be offline during check-in")
	}
	if e.SleepCfg.IntervalSec != 30 {
		t.Fatalf("SleepCfg.IntervalSec = %d, want 30", e.SleepCfg.IntervalSec)
	}
}

func TestFindOrRegisterReconnect(t *testing.T) {
	r := New()
	info := &proto.ClientInfoPayload{Hostname: "recon-host", OS: "linux", Arch: "amd64"}
	cfg := proto.SleepCfgPayload{IntervalSec: 60, JitterPct: 20}

	// First registration.
	id1, _ := r.FindOrRegister(info, nil, nil, "10.0.0.1:1111", cfg)
	r.SetOnline(id1, false)

	// Reconnect with same hostname.
	info2 := &proto.ClientInfoPayload{Hostname: "recon-host", OS: "linux", Arch: "amd64"}
	id2, isReconnect := r.FindOrRegister(info2, nil, nil, "10.0.0.1:2222", cfg)
	if !isReconnect {
		t.Fatal("expected reconnect, got new")
	}
	if id1 != id2 {
		t.Fatalf("ID changed: %q -> %q", id1, id2)
	}
	e, _ := r.Get(id2)
	if e.RemoteAddr != "10.0.0.1:2222" {
		t.Fatalf("RemoteAddr = %q, want 10.0.0.1:2222", e.RemoteAddr)
	}
	if e.Online {
		t.Fatal("reconnected entry should be offline during check-in")
	}
}

func TestQueueCmdAndDrain(t *testing.T) {
	r := New()
	id := r.Register(newTestEntry("host1"))

	msg1 := &proto.Message{Type: proto.CmdWake}
	msg2 := &proto.Message{Type: proto.CmdSleepCfg, Payload: []byte("cfg")}

	if !r.QueueCmd(id, msg1) {
		t.Fatal("QueueCmd should succeed")
	}
	if !r.QueueCmd(id, msg2) {
		t.Fatal("QueueCmd should succeed")
	}

	cmds := r.DrainPending(id)
	if len(cmds) != 2 {
		t.Fatalf("DrainPending returned %d cmds, want 2", len(cmds))
	}
	if cmds[0].Type != proto.CmdWake {
		t.Fatalf("first cmd type = 0x%02x, want CmdWake", cmds[0].Type)
	}

	// Drain again should be empty.
	cmds = r.DrainPending(id)
	if len(cmds) != 0 {
		t.Fatalf("DrainPending should return 0 after drain, got %d", len(cmds))
	}
}

func TestQueueCmdCap(t *testing.T) {
	r := New()
	id := r.Register(newTestEntry("host1"))

	for range maxPendingCmds {
		r.QueueCmd(id, &proto.Message{Type: proto.CmdSleepCfg})
	}
	if r.QueueCmd(id, &proto.Message{Type: proto.CmdSleepCfg}) {
		t.Fatal("QueueCmd should fail when at capacity")
	}
}

func TestSetOnline(t *testing.T) {
	r := New()
	id := r.Register(newTestEntry("host1"))

	r.SetOnline(id, true)
	e, _ := r.Get(id)
	if !e.Online {
		t.Fatal("expected online=true")
	}

	r.SetOnline(id, false)
	e, _ = r.Get(id)
	if e.Online {
		t.Fatal("expected online=false")
	}
}

func TestSetOfflineIfCtrl(t *testing.T) {
	r := New()
	entry := newTestEntry("host1")

	ctrl1a, ctrl1b := net.Pipe()
	ctrl2a, ctrl2b := net.Pipe()
	t.Cleanup(func() { ctrl1a.Close(); ctrl1b.Close(); ctrl2a.Close(); ctrl2b.Close() })

	entry.Ctrl = ctrl1a
	entry.Online = true
	id := r.Register(entry)

	// Should NOT mark offline because ctrl doesn't match.
	r.SetOfflineIfCtrl(id, ctrl2a)
	e, _ := r.Get(id)
	if !e.Online {
		t.Fatal("should still be online (ctrl mismatch)")
	}

	// Should mark offline because ctrl matches.
	r.SetOfflineIfCtrl(id, ctrl1a)
	e, _ = r.Get(id)
	if e.Online {
		t.Fatal("should be offline (ctrl match)")
	}
}

func TestQueueCmdNotFound(t *testing.T) {
	r := New()
	if r.QueueCmd("nonexistent", &proto.Message{Type: proto.CmdWake}) {
		t.Fatal("QueueCmd should return false for unknown ID")
	}
}

func TestDrainPendingNotFound(t *testing.T) {
	r := New()
	cmds := r.DrainPending("nonexistent")
	if cmds != nil {
		t.Fatal("DrainPending should return nil for unknown ID")
	}
}
