package proto

import (
	"bytes"
	"net"
	"strings"
	"testing"
)

func TestMessageRoundTrip(t *testing.T) {
	cases := []struct {
		name string
		msg  Message
	}{
		{"empty payload", Message{Type: Keepalive, Payload: nil}},
		{"small payload", Message{Type: AuthChallenge, Payload: []byte("hello")}},
		{"connect", Message{Type: Connect, Payload: []byte(`{"addr":"localhost:80"}`)}},
		{"binary payload", Message{Type: AuthResponse, Payload: bytes.Repeat([]byte{0xab}, 256)}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			if err := WriteMessage(&buf, &tc.msg); err != nil {
				t.Fatalf("write: %v", err)
			}
			got, err := ReadMessage(&buf)
			if err != nil {
				t.Fatalf("read: %v", err)
			}
			if got.Type != tc.msg.Type {
				t.Errorf("type = 0x%02x, want 0x%02x", got.Type, tc.msg.Type)
			}
			if !bytes.Equal(got.Payload, tc.msg.Payload) {
				t.Errorf("payload mismatch")
			}
		})
	}
}

func TestMessageTooLarge(t *testing.T) {
	// Craft a header claiming > 1 MiB payload.
	var buf bytes.Buffer
	msg := &Message{Type: 0x01, Payload: make([]byte, 0)}
	WriteMessage(&buf, msg)

	// Overwrite length field with 1 MiB + 1.
	raw := buf.Bytes()
	raw[1] = 0x00
	raw[2] = 0x10
	raw[3] = 0x00
	raw[4] = 0x01 // 1<<20 + 1 = 1048577

	_, err := ReadMessage(bytes.NewReader(raw))
	if err == nil {
		t.Fatal("expected error for oversized message")
	}
	if !strings.Contains(err.Error(), "too large") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestConnectRoundTrip(t *testing.T) {
	var buf bytes.Buffer
	addr := "example.com:443"
	if err := WriteConnect(&buf, addr); err != nil {
		t.Fatalf("WriteConnect: %v", err)
	}
	got, err := ReadConnect(&buf)
	if err != nil {
		t.Fatalf("ReadConnect: %v", err)
	}
	if got != addr {
		t.Fatalf("addr = %q, want %q", got, addr)
	}
}

func TestConnectAckRoundTrip(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		var buf bytes.Buffer
		if err := WriteConnectAck(&buf, true, ""); err != nil {
			t.Fatal(err)
		}
		ok, errMsg, err := ReadConnectAck(&buf)
		if err != nil {
			t.Fatal(err)
		}
		if !ok {
			t.Fatal("expected ok=true")
		}
		if errMsg != "" {
			t.Fatalf("unexpected error message: %q", errMsg)
		}
	})

	t.Run("failure", func(t *testing.T) {
		var buf bytes.Buffer
		if err := WriteConnectAck(&buf, false, "connection refused"); err != nil {
			t.Fatal(err)
		}
		ok, errMsg, err := ReadConnectAck(&buf)
		if err != nil {
			t.Fatal(err)
		}
		if ok {
			t.Fatal("expected ok=false")
		}
		if errMsg != "connection refused" {
			t.Fatalf("error = %q, want %q", errMsg, "connection refused")
		}
	})
}

func TestHandshakeSuccess(t *testing.T) {
	server, client := net.Pipe()
	defer server.Close()
	defer client.Close()

	secret := "test-secret"
	errCh := make(chan error, 2)
	var info *ClientInfoPayload

	go func() {
		var err error
		info, err = ServerHandshake(server, secret)
		errCh <- err
	}()

	go func() {
		errCh <- ClientHandshake(client, secret, &ClientInfoPayload{
			Hostname: "testhost",
			OS:       "linux",
			Arch:     "amd64",
		})
	}()

	for range 2 {
		if err := <-errCh; err != nil {
			t.Fatalf("handshake error: %v", err)
		}
	}

	if info.Hostname != "testhost" {
		t.Fatalf("hostname = %q, want testhost", info.Hostname)
	}
	if info.OS != "linux" {
		t.Fatalf("os = %q, want linux", info.OS)
	}
}

func TestHandshakeBadSecret(t *testing.T) {
	server, client := net.Pipe()
	defer server.Close()
	defer client.Close()

	errCh := make(chan error, 2)

	go func() {
		_, err := ServerHandshake(server, "correct-secret")
		errCh <- err
	}()

	go func() {
		errCh <- ClientHandshake(client, "wrong-secret", &ClientInfoPayload{
			Hostname: "testhost",
			OS:       "linux",
			Arch:     "amd64",
		})
	}()

	var serverErr, clientErr error
	for range 2 {
		err := <-errCh
		if err != nil {
			if strings.Contains(err.Error(), "authentication failed") {
				if strings.Contains(err.Error(), "bad secret") {
					clientErr = err
				} else {
					serverErr = err
				}
			}
		}
	}

	if serverErr == nil {
		t.Fatal("expected server to report auth failure")
	}
	if clientErr == nil {
		t.Fatal("expected client to report auth failure")
	}
}
