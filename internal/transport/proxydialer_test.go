package transport_test

import (
	"bufio"
	"context"
	"encoding/base64"
	"io"
	"net"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	socks5 "github.com/things-go/go-socks5"

	"github.com/ShigShag/Phantom-Proxy/internal/transport"
)

func parseProxyAuth(header string) (user, pass string, ok bool) {
	if !strings.HasPrefix(header, "Basic ") {
		return "", "", false
	}
	decoded, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(header, "Basic "))
	if err != nil {
		return "", "", false
	}
	parts := strings.SplitN(string(decoded), ":", 2)
	if len(parts) != 2 {
		return "", "", false
	}
	return parts[0], parts[1], true
}

// startRawCONNECTProxy starts a raw TCP server that handles HTTP CONNECT.
func startRawCONNECTProxy(t *testing.T) (string, *atomic.Int64) {
	t.Helper()
	var count atomic.Int64

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })

	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go handleCONNECT(c, &count)
		}
	}()

	return ln.Addr().String(), &count
}

func handleCONNECT(c net.Conn, count *atomic.Int64) {
	defer c.Close()

	req, err := http.ReadRequest(bufio.NewReader(c))
	if err != nil || req.Method != http.MethodConnect {
		c.Write([]byte("HTTP/1.1 400 Bad Request\r\n\r\n"))
		return
	}

	dest, err := net.Dial("tcp", req.Host)
	if err != nil {
		c.Write([]byte("HTTP/1.1 502 Bad Gateway\r\n\r\n"))
		return
	}
	defer dest.Close()

	count.Add(1)
	c.Write([]byte("HTTP/1.1 200 Connection Established\r\n\r\n"))

	go io.Copy(dest, c)
	io.Copy(c, dest)
}

// startRawCONNECTProxyWithAuth starts a CONNECT proxy that requires auth.
func startRawCONNECTProxyWithAuth(t *testing.T, wantUser, wantPass string) (string, *atomic.Int64) {
	t.Helper()
	var count atomic.Int64

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })

	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go handleCONNECTWithAuth(c, wantUser, wantPass, &count)
		}
	}()

	return ln.Addr().String(), &count
}

func handleCONNECTWithAuth(c net.Conn, wantUser, wantPass string, count *atomic.Int64) {
	defer c.Close()

	req, err := http.ReadRequest(bufio.NewReader(c))
	if err != nil || req.Method != http.MethodConnect {
		c.Write([]byte("HTTP/1.1 400 Bad Request\r\n\r\n"))
		return
	}

	user, pass, ok := parseProxyAuth(req.Header.Get("Proxy-Authorization"))
	if !ok || user != wantUser || pass != wantPass {
		c.Write([]byte("HTTP/1.1 407 Proxy Authentication Required\r\n\r\n"))
		return
	}

	dest, err := net.Dial("tcp", req.Host)
	if err != nil {
		c.Write([]byte("HTTP/1.1 502 Bad Gateway\r\n\r\n"))
		return
	}
	defer dest.Close()

	count.Add(1)
	c.Write([]byte("HTTP/1.1 200 Connection Established\r\n\r\n"))

	go io.Copy(dest, c)
	io.Copy(c, dest)
}

// startEchoServer starts a TCP echo server.
func startEchoServer(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				io.Copy(c, c)
			}()
		}
	}()
	return ln.Addr().String()
}

func TestProxyDial_HTTPConnect(t *testing.T) {
	echoAddr := startEchoServer(t)
	proxyAddr, count := startRawCONNECTProxy(t)

	conn, err := transport.ProxyDial(context.Background(), "tcp", echoAddr, "http://"+proxyAddr)
	if err != nil {
		t.Fatalf("ProxyDial: %v", err)
	}
	defer conn.Close()

	msg := "hello proxy"
	if _, err := conn.Write([]byte(msg)); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, len(msg))
	if _, err := io.ReadFull(conn, buf); err != nil {
		t.Fatal(err)
	}
	if string(buf) != msg {
		t.Fatalf("got %q, want %q", buf, msg)
	}

	if count.Load() != 1 {
		t.Fatalf("proxy CONNECT count = %d, want 1", count.Load())
	}
}

func TestProxyDial_HTTPConnectAuth(t *testing.T) {
	echoAddr := startEchoServer(t)
	proxyAddr, count := startRawCONNECTProxyWithAuth(t, "testuser", "testpass")

	// Correct credentials.
	conn, err := transport.ProxyDial(context.Background(), "tcp", echoAddr, "http://testuser:testpass@"+proxyAddr)
	if err != nil {
		t.Fatalf("ProxyDial with auth: %v", err)
	}
	conn.Close()

	if count.Load() != 1 {
		t.Fatalf("auth proxy count = %d, want 1", count.Load())
	}

	// Wrong credentials.
	_, err = transport.ProxyDial(context.Background(), "tcp", echoAddr, "http://wrong:creds@"+proxyAddr)
	if err == nil {
		t.Fatal("expected error with wrong credentials")
	}
}

func TestProxyDial_SOCKS5(t *testing.T) {
	echoAddr := startEchoServer(t)

	srv := socks5.NewServer()
	socksLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { socksLn.Close() })
	go srv.Serve(socksLn)

	conn, err := transport.ProxyDial(context.Background(), "tcp", echoAddr, "socks5://"+socksLn.Addr().String())
	if err != nil {
		t.Fatalf("ProxyDial SOCKS5: %v", err)
	}
	defer conn.Close()

	msg := "hello socks5"
	if _, err := conn.Write([]byte(msg)); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, len(msg))
	if _, err := io.ReadFull(conn, buf); err != nil {
		t.Fatal(err)
	}
	if string(buf) != msg {
		t.Fatalf("got %q, want %q", buf, msg)
	}
}

func TestProxyDial_BadScheme(t *testing.T) {
	_, err := transport.ProxyDial(context.Background(), "tcp", "localhost:1234", "ftp://proxy:3128")
	if err == nil {
		t.Fatal("expected error for unsupported scheme")
	}
	if !strings.Contains(err.Error(), "unsupported proxy scheme") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestDialRawTCP_NoProxy(t *testing.T) {
	echoAddr := startEchoServer(t)

	cfg := &transport.Config{}
	conn, err := cfg.DialRawTCP(context.Background(), echoAddr)
	if err != nil {
		t.Fatalf("DialRawTCP: %v", err)
	}
	defer conn.Close()

	msg := "direct"
	if _, err := conn.Write([]byte(msg)); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, len(msg))
	if _, err := io.ReadFull(conn, buf); err != nil {
		t.Fatal(err)
	}
	if string(buf) != msg {
		t.Fatalf("got %q, want %q", buf, msg)
	}
}
