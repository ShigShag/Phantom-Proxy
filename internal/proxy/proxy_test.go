package proxy

import (
	"bufio"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ShigShag/Phantom-Proxy/internal/mux"
	"github.com/ShigShag/Phantom-Proxy/internal/proto"
)

func TestHandleStream(t *testing.T) {
	// Use an HTTP server as the target. HTTP/1.1 with Connection: close
	// lets us read a complete response without needing yamux half-close.
	const body = "handle-stream-ok"
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, body)
	}))
	defer ts.Close()

	targetAddr := ts.Listener.Addr().String()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()

	serverSess, err := mux.ServerSession(a)
	if err != nil {
		t.Fatal(err)
	}
	defer serverSess.Close()

	clientSess, err := mux.ClientSession(b)
	if err != nil {
		t.Fatal(err)
	}
	defer clientSess.Close()

	go func() {
		stream, err := serverSess.Accept()
		if err != nil {
			return
		}
		handleStream(stream, logger)
	}()

	stream, err := clientSess.Open()
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()

	if err := proto.WriteConnect(stream, targetAddr); err != nil {
		t.Fatal(err)
	}

	ok, errMsg, err := proto.ReadConnectAck(stream)
	if err != nil {
		t.Fatalf("ReadConnectAck: %v", err)
	}
	if !ok {
		t.Fatalf("CONNECT rejected: %s", errMsg)
	}

	// Send an HTTP request through the tunnel.
	req, _ := http.NewRequest("GET", "http://"+targetAddr+"/", nil)
	req.Header.Set("Connection", "close")
	if err := req.Write(stream); err != nil {
		t.Fatal(err)
	}

	// Read the HTTP response using the HTTP parser, which reads exactly
	// the right amount of data based on Content-Length / chunked encoding.
	resp, err := http.ReadResponse(bufio.NewReader(stream), req)
	if err != nil {
		t.Fatalf("ReadResponse: %v", err)
	}
	defer resp.Body.Close()

	got, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("ReadAll body: %v", err)
	}
	if string(got) != body {
		t.Fatalf("got %q, want %q", string(got), body)
	}
}

func TestHandleStreamDialFailure(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()

	serverSess, err := mux.ServerSession(a)
	if err != nil {
		t.Fatal(err)
	}
	defer serverSess.Close()

	clientSess, err := mux.ClientSession(b)
	if err != nil {
		t.Fatal(err)
	}
	defer clientSess.Close()

	go func() {
		stream, err := serverSess.Accept()
		if err != nil {
			return
		}
		handleStream(stream, logger)
	}()

	stream, err := clientSess.Open()
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()

	// CONNECT to an unreachable address.
	if err := proto.WriteConnect(stream, "127.0.0.1:1"); err != nil {
		t.Fatal(err)
	}

	ok, errMsg, err := proto.ReadConnectAck(stream)
	if err != nil {
		t.Fatalf("ReadConnectAck: %v", err)
	}
	if ok {
		t.Fatal("expected CONNECT to be rejected")
	}
	if errMsg == "" {
		t.Fatal("expected non-empty error message")
	}
}
