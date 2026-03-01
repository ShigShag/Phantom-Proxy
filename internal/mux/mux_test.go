package mux

import (
	"bytes"
	"io"
	"net"
	"testing"
)

func TestSessionRoundTrip(t *testing.T) {
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()

	serverSess, err := ServerSession(a)
	if err != nil {
		t.Fatalf("ServerSession: %v", err)
	}
	defer serverSess.Close()

	clientSess, err := ClientSession(b)
	if err != nil {
		t.Fatalf("ClientSession: %v", err)
	}
	defer clientSess.Close()

	// Client opens a stream.
	stream, err := clientSess.Open()
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	// Server accepts the stream.
	accepted, err := serverSess.Accept()
	if err != nil {
		t.Fatalf("Accept: %v", err)
	}

	want := []byte("hello yamux")
	if _, err := stream.Write(want); err != nil {
		t.Fatalf("Write: %v", err)
	}
	stream.Close()

	got, err := io.ReadAll(accepted)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("got %q, want %q", got, want)
	}
	accepted.Close()
}

func TestRelay(t *testing.T) {
	// Use TCP connections instead of net.Pipe because Relay relies on
	// CloseWrite for half-close signaling, which net.Pipe doesn't support.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	// Pair 1: a1 <-> a2
	var a2 net.Conn
	acceptDone := make(chan struct{})
	go func() {
		var err error
		a2, err = ln.Accept()
		if err != nil {
			t.Error(err)
		}
		close(acceptDone)
	}()
	a1, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	<-acceptDone

	// Pair 2: b1 <-> b2
	go func() {
		var err error
		// reuse a2 var name scope — use b conn
		conn, err := ln.Accept()
		if err != nil {
			t.Error(err)
			return
		}
		// b1 is the accepted side, start relay
		Relay(a2, conn)
	}()
	b2, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}

	// Write from a1 side, read from b2 side.
	want := []byte("relay test data")
	go func() {
		a1.Write(want)
		a1.Close()
	}()

	got, err := io.ReadAll(b2)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("got %q, want %q", got, want)
	}
	b2.Close()
}
