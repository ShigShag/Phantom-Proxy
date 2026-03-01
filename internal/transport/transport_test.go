package transport

import (
	"net"
	"slices"
	"testing"
)

// mockTransport implements Transport for testing.
type mockTransport struct {
	name string
}

func (m *mockTransport) Name() string                                    { return m.name }
func (m *mockTransport) Dial(string, *Config) (net.Conn, error)         { return nil, nil }
func (m *mockTransport) Listen(string, *Config) (net.Listener, error)   { return nil, nil }

func TestRegisterAndGet(t *testing.T) {
	// Clean up after test to avoid polluting other tests.
	defer func() {
		mu.Lock()
		delete(registry, "mock-test")
		mu.Unlock()
	}()

	Register(&mockTransport{name: "mock-test"})

	tr, err := Get("mock-test")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if tr.Name() != "mock-test" {
		t.Fatalf("name = %q, want mock-test", tr.Name())
	}
}

func TestGetUnknown(t *testing.T) {
	_, err := Get("nonexistent-transport")
	if err == nil {
		t.Fatal("expected error for unknown transport")
	}
}

func TestAvailable(t *testing.T) {
	defer func() {
		mu.Lock()
		delete(registry, "avail-a")
		delete(registry, "avail-b")
		mu.Unlock()
	}()

	Register(&mockTransport{name: "avail-a"})
	Register(&mockTransport{name: "avail-b"})

	names := Available()
	if !slices.Contains(names, "avail-a") {
		t.Fatal("missing avail-a")
	}
	if !slices.Contains(names, "avail-b") {
		t.Fatal("missing avail-b")
	}
}
