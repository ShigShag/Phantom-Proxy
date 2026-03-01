package transport

import (
	"fmt"
	"net"
	"sync"
)

// Config holds transport-specific configuration.
type Config struct {
	// TLS
	CertFile   string
	KeyFile    string
	CAFile     string
	SkipVerify bool

	// SSH
	HostKeyFile   string
	ClientKeyFile string

	// HTTP/WebSocket
	HTTPPath  string
	HostHeader string
	UserAgent string
	UseTLS    bool
}

// Transport defines the interface every transport must implement.
type Transport interface {
	Dial(addr string, cfg *Config) (net.Conn, error)
	Listen(addr string, cfg *Config) (net.Listener, error)
	Name() string
}

var (
	mu       sync.RWMutex
	registry = make(map[string]Transport)
)

// Register adds a transport to the global registry.
func Register(t Transport) {
	mu.Lock()
	defer mu.Unlock()
	registry[t.Name()] = t
}

// Get retrieves a transport by name.
func Get(name string) (Transport, error) {
	mu.RLock()
	defer mu.RUnlock()
	t, ok := registry[name]
	if !ok {
		return nil, fmt.Errorf("unknown transport: %s", name)
	}
	return t, nil
}

// Available returns the names of all registered transports.
func Available() []string {
	mu.RLock()
	defer mu.RUnlock()
	names := make([]string, 0, len(registry))
	for name := range registry {
		names = append(names, name)
	}
	return names
}
