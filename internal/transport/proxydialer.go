package transport

import (
	"bufio"
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"

	"golang.org/x/net/proxy"
)

// ProxyDial establishes a TCP connection to addr through the specified proxy.
// Supported schemes: http (HTTP CONNECT), socks5.
func ProxyDial(ctx context.Context, network, addr, proxyURL string) (net.Conn, error) {
	u, err := url.Parse(proxyURL)
	if err != nil {
		return nil, fmt.Errorf("parse proxy URL: %w", err)
	}

	switch u.Scheme {
	case "http", "https":
		return dialHTTPConnect(ctx, addr, u)
	case "socks5":
		return dialSOCKS5(ctx, network, addr, u)
	default:
		return nil, fmt.Errorf("unsupported proxy scheme: %q", u.Scheme)
	}
}

// DialRawTCP returns a TCP connection to addr, routing through the upstream
// proxy if cfg.ProxyURL is set.
func (cfg *Config) DialRawTCP(ctx context.Context, addr string) (net.Conn, error) {
	if cfg.ProxyURL != "" {
		return ProxyDial(ctx, "tcp", addr, cfg.ProxyURL)
	}
	var d net.Dialer
	return d.DialContext(ctx, "tcp", addr)
}

func dialHTTPConnect(ctx context.Context, addr string, u *url.URL) (net.Conn, error) {
	var d net.Dialer
	conn, err := d.DialContext(ctx, "tcp", u.Host)
	if err != nil {
		return nil, fmt.Errorf("dial proxy %s: %w", u.Host, err)
	}

	// Write the CONNECT request manually to avoid any automatic headers
	// that the Go HTTP library might add.
	reqBuf := "CONNECT " + addr + " HTTP/1.1\r\nHost: " + addr + "\r\n"
	if u.User != nil {
		user := u.User.Username()
		pass, _ := u.User.Password()
		cred := base64.StdEncoding.EncodeToString([]byte(user + ":" + pass))
		reqBuf += "Proxy-Authorization: Basic " + cred + "\r\n"
	}
	reqBuf += "\r\n"

	if _, err := io.WriteString(conn, reqBuf); err != nil {
		conn.Close()
		return nil, fmt.Errorf("write CONNECT request: %w", err)
	}

	// Read the response. Passing a CONNECT request tells ReadResponse
	// that a 2xx response has no body.
	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, &http.Request{Method: http.MethodConnect})
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("read CONNECT response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		conn.Close()
		return nil, fmt.Errorf("proxy CONNECT failed: %s", resp.Status)
	}

	// If the bufio.Reader consumed bytes past the HTTP response,
	// wrap conn so those bytes aren't lost.
	if br.Buffered() > 0 {
		return &bufConn{Conn: conn, br: br}, nil
	}
	return conn, nil
}

// bufConn wraps a net.Conn with a bufio.Reader to avoid losing
// data that was read-ahead during HTTP response parsing.
type bufConn struct {
	net.Conn
	br *bufio.Reader
}

func (c *bufConn) Read(p []byte) (int, error) {
	return c.br.Read(p)
}

func dialSOCKS5(ctx context.Context, network, addr string, u *url.URL) (net.Conn, error) {
	var auth *proxy.Auth
	if u.User != nil {
		user := u.User.Username()
		pass, _ := u.User.Password()
		auth = &proxy.Auth{User: user, Password: pass}
	}

	dialer, err := proxy.SOCKS5("tcp", u.Host, auth, proxy.Direct)
	if err != nil {
		return nil, fmt.Errorf("create SOCKS5 dialer: %w", err)
	}

	// Use context-aware dialing if supported.
	if cd, ok := dialer.(proxy.ContextDialer); ok {
		return cd.DialContext(ctx, network, addr)
	}
	return dialer.Dial(network, addr)
}
