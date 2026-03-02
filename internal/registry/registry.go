package registry

import (
	"crypto/rand"
	"fmt"
	"net"
	"sort"
	"sync"
	"time"

	"github.com/ShigShag/Phantom-Proxy/internal/proto"
	"github.com/hashicorp/yamux"
)

// ClientState represents the current state of a connected client.
type ClientState int

const (
	StateDormant ClientState = iota
	StateActive
)

// maxPendingCmds caps the number of queued commands per offline client.
const maxPendingCmds = 100

// String returns a human-readable state name.
func (s ClientState) String() string {
	switch s {
	case StateDormant:
		return "dormant"
	case StateActive:
		return "active"
	default:
		return "unknown"
	}
}

// ClientEntry holds all metadata for a connected client.
type ClientEntry struct {
	ID          string
	Info        *proto.ClientInfoPayload
	Session     *yamux.Session
	Ctrl        net.Conn // control stream — used to send wake/sleep/cfg
	State       ClientState
	Online      bool
	ConnectedAt time.Time
	LastSeen    time.Time
	RemoteAddr  string
	SleepCfg    proto.SleepCfgPayload
	AckCh       chan *proto.Message // buffered channel, capacity 1

	mu          sync.Mutex      // protects PendingCmds
	PendingCmds []*proto.Message // commands queued while offline
}

// WakeEvent is sent on Registry.WakeCh when an offline client checks in and acks a queued wake.
type WakeEvent struct {
	ID string
}

// Registry is a thread-safe store of connected clients.
type Registry struct {
	mu      sync.RWMutex
	clients map[string]*ClientEntry
	WakeCh  chan WakeEvent // notifications for the shell
}

// New creates a new empty Registry.
func New() *Registry {
	return &Registry{
		clients: make(map[string]*ClientEntry),
		WakeCh:  make(chan WakeEvent, 16),
	}
}

// Register assigns a unique 4-char hex ID to the entry, stores it, and returns the ID.
func (r *Registry) Register(entry *ClientEntry) string {
	r.mu.Lock()
	defer r.mu.Unlock()

	for {
		id := randomHexID()
		if _, exists := r.clients[id]; !exists {
			entry.ID = id
			entry.AckCh = make(chan *proto.Message, 1)
			r.clients[id] = entry
			return id
		}
	}
}

// FindOrRegister looks up an existing entry by hostname for reconnecting dormant clients.
// If found, it updates Session/Ctrl/RemoteAddr/LastSeen/Online and returns (id, true).
// If not found, it registers as new and returns (id, false).
// If the existing entry has a stale session, it closes it.
func (r *Registry) FindOrRegister(info *proto.ClientInfoPayload, session *yamux.Session, ctrl net.Conn, remoteAddr string, defaultCfg proto.SleepCfgPayload) (string, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()

	// Try to find existing entry by hostname.
	for _, e := range r.clients {
		if e.Info != nil && e.Info.Hostname == info.Hostname {
			// Close stale session if still open.
			if e.Session != nil && e.Session != session {
				e.Session.Close()
			}
			e.Session = session
			e.Ctrl = ctrl
			e.RemoteAddr = remoteAddr
			e.ConnectedAt = time.Now()
			e.LastSeen = time.Now()
			e.Online = false
			e.Info = info
			// Re-create AckCh so stale acks don't leak through.
			e.AckCh = make(chan *proto.Message, 1)
			return e.ID, true
		}
	}

	// New client.
	for {
		id := randomHexID()
		if _, exists := r.clients[id]; !exists {
			entry := &ClientEntry{
				ID:          id,
				Info:        info,
				Session:     session,
				Ctrl:        ctrl,
				State:       StateDormant,
				Online:      false,
				ConnectedAt: time.Now(),
				LastSeen:    time.Now(),
				RemoteAddr:  remoteAddr,
				SleepCfg:    defaultCfg,
				AckCh:       make(chan *proto.Message, 1),
			}
			r.clients[id] = entry
			return id, false
		}
	}
}

// Deregister removes a client entry by ID.
func (r *Registry) Deregister(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.clients, id)
}

// Get looks up a client entry by ID.
func (r *Registry) Get(id string) (*ClientEntry, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	e, ok := r.clients[id]
	return e, ok
}

// List returns all entries sorted by ConnectedAt ascending.
func (r *Registry) List() []*ClientEntry {
	r.mu.RLock()
	defer r.mu.RUnlock()

	entries := make([]*ClientEntry, 0, len(r.clients))
	for _, e := range r.clients {
		entries = append(entries, e)
	}
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].ConnectedAt.Before(entries[j].ConnectedAt)
	})
	return entries
}

// FindByHostname returns the first client matching the given hostname.
func (r *Registry) FindByHostname(hostname string) (*ClientEntry, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, e := range r.clients {
		if e.Info != nil && e.Info.Hostname == hostname {
			return e, true
		}
	}
	return nil, false
}

// UpdateLastSeen sets LastSeen to the current time.
func (r *Registry) UpdateLastSeen(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if e, ok := r.clients[id]; ok {
		e.LastSeen = time.Now()
	}
}

// SetState updates the state of a client.
func (r *Registry) SetState(id string, state ClientState) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if e, ok := r.clients[id]; ok {
		e.State = state
	}
}

// SetOnline sets the Online field for a client.
func (r *Registry) SetOnline(id string, online bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if e, ok := r.clients[id]; ok {
		e.Online = online
		if online {
			e.LastSeen = time.Now()
		}
	}
}

// SetOfflineIfCtrl marks the client offline only if the given ctrl matches the current one.
// This prevents a new check-in handler from being clobbered by the old one's defer.
func (r *Registry) SetOfflineIfCtrl(id string, ctrl net.Conn) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if e, ok := r.clients[id]; ok {
		if e.Ctrl == ctrl {
			e.Online = false
		}
	}
}

// QueueCmd appends a command to the client's pending queue (for delivery at next check-in).
func (r *Registry) QueueCmd(id string, msg *proto.Message) bool {
	r.mu.RLock()
	e, ok := r.clients[id]
	r.mu.RUnlock()
	if !ok {
		return false
	}

	e.mu.Lock()
	defer e.mu.Unlock()
	if len(e.PendingCmds) >= maxPendingCmds {
		return false
	}
	e.PendingCmds = append(e.PendingCmds, msg)
	return true
}

// DrainPending returns and clears all pending commands for a client.
func (r *Registry) DrainPending(id string) []*proto.Message {
	r.mu.RLock()
	e, ok := r.clients[id]
	r.mu.RUnlock()
	if !ok {
		return nil
	}

	e.mu.Lock()
	defer e.mu.Unlock()
	cmds := e.PendingCmds
	e.PendingCmds = nil
	return cmds
}

// Count returns the number of registered clients.
func (r *Registry) Count() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.clients)
}

// randomHexID generates a 4-character hex string from 2 random bytes.
func randomHexID() string {
	b := make([]byte, 2)
	rand.Read(b)
	return fmt.Sprintf("%04x", b)
}
