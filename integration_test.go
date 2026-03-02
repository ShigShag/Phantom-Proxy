package phantom_proxy_test

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/net/proxy"
)

var (
	serverBin string
	clientBin string
)

func TestMain(m *testing.M) {
	tmp, err := os.MkdirTemp("", "phantom-test-*")
	if err != nil {
		fmt.Fprintf(os.Stderr, "mktemp: %v\n", err)
		os.Exit(1)
	}
	defer os.RemoveAll(tmp)

	serverBin = filepath.Join(tmp, "phantom-server")
	clientBin = filepath.Join(tmp, "phantom-client")

	// Build both binaries.
	for _, b := range []struct{ pkg, out string }{
		{"./cmd/server", serverBin},
		{"./cmd/client", clientBin},
	} {
		cmd := exec.Command("go", "build", "-o", b.out, b.pkg)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			fmt.Fprintf(os.Stderr, "build %s: %v\n", b.pkg, err)
			os.Exit(1)
		}
	}

	os.Exit(m.Run())
}

// waitForLog scans process stderr for a line containing marker.
// Returns the full line or an error on timeout.
func waitForLog(r io.Reader, marker string, timeout time.Duration) (string, error) {
	ch := make(chan string, 1)
	go func() {
		sc := bufio.NewScanner(r)
		for sc.Scan() {
			line := sc.Text()
			if strings.Contains(line, marker) {
				ch <- line
				return
			}
		}
	}()
	select {
	case line := <-ch:
		return line, nil
	case <-time.After(timeout):
		return "", fmt.Errorf("timeout waiting for %q", marker)
	}
}

// startServer starts the phantom-server with given args and waits for "listening".
// Returns the actual listen address, the process, and a stderr pipe reader.
func startServer(t *testing.T, args ...string) (addr string, cmd *exec.Cmd, stderr io.Reader) {
	t.Helper()
	cmd = exec.Command(serverBin, args...)
	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cmd.Process.Kill()
		cmd.Wait()
	})

	// Parse the listen address from log output.
	// Log line looks like: time=... level=INFO msg=listening addr=... transport=...
	line, err := waitForLog(stderrPipe, "msg=listening", 10*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	// Extract addr value.
	for _, field := range strings.Fields(line) {
		if strings.HasPrefix(field, "addr=") {
			addr = strings.TrimPrefix(field, "addr=")
			break
		}
	}
	if addr == "" {
		t.Fatalf("could not parse listen address from: %s", line)
	}

	return addr, cmd, stderrPipe
}

// startClient starts the phantom-client with given args and waits for "authenticated".
func startClient(t *testing.T, stderr io.Reader, args ...string) *exec.Cmd {
	t.Helper()
	cmd := exec.Command(clientBin, args...)
	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cmd.Process.Kill()
		cmd.Wait()
	})

	// Wait for client to authenticate.
	if _, err := waitForLog(stderrPipe, "authenticated", 10*time.Second); err != nil {
		t.Fatal(err)
	}

	return cmd
}

// dialSOCKS5 makes an HTTP GET through a SOCKS5 proxy.
func dialSOCKS5(t *testing.T, socksAddr, targetURL string) string {
	t.Helper()
	dialer, err := proxy.SOCKS5("tcp", socksAddr, nil, proxy.Direct)
	if err != nil {
		t.Fatalf("SOCKS5 dialer: %v", err)
	}
	httpClient := &http.Client{
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				return dialer.Dial(network, addr)
			},
		},
		Timeout: 5 * time.Second,
	}
	resp, err := httpClient.Get(targetURL)
	if err != nil {
		t.Fatalf("HTTP GET through SOCKS5: %v", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}

// getFreePort returns a free TCP port on localhost.
func getFreePort(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()
	return addr
}

func TestIntegration(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration tests in short mode")
	}

	// Start a target HTTP server.
	const body = "phantom-proxy-integration-test-ok"
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, body)
	}))
	t.Cleanup(target.Close)

	transports := []string{"tcp", "tls", "ssh", "http"}

	for _, tr := range transports {
		t.Run(tr, func(t *testing.T) {
			t.Parallel()

			socksAddr := getFreePort(t)

			serverAddr, _, serverStderr := startServer(t,
				"-listen", "127.0.0.1:0",
				"-transport", tr,
				"-secret", "integration-test-secret",
				"-socks5", socksAddr,
				"-log-level", "debug",
			)

			startClient(t, serverStderr,
				"-server", serverAddr,
				"-transport", tr,
				"-secret", "integration-test-secret",
				"-reconnect=false",
				"-log-level", "debug",
			)

			// Give the mux session a moment to stabilize.
			time.Sleep(200 * time.Millisecond)

			got := dialSOCKS5(t, socksAddr, target.URL)
			if got != body {
				t.Fatalf("body = %q, want %q", got, body)
			}
		})
	}

	t.Run("bad_secret", func(t *testing.T) {
		t.Parallel()

		socksAddr := getFreePort(t)

		serverAddr, _, _ := startServer(t,
			"-listen", "127.0.0.1:0",
			"-transport", "tcp",
			"-secret", "correct-secret",
			"-socks5", socksAddr,
			"-log-level", "debug",
		)

		// Client with wrong secret — should fail to authenticate.
		cmd := exec.Command(clientBin,
			"-server", serverAddr,
			"-transport", "tcp",
			"-secret", "wrong-secret",
			"-reconnect=false",
			"-log-level", "debug",
		)
		out, err := cmd.CombinedOutput()
		if err == nil {
			t.Fatal("expected client to exit with error")
		}
		if !strings.Contains(string(out), "authentication failed") {
			t.Fatalf("expected auth failure in output, got: %s", out)
		}
	})

	t.Run("dormant_checkin", func(t *testing.T) {
		t.Parallel()

		socksAddr := getFreePort(t)

		// Start server in interactive mode.
		serverAddr, _, serverStderr := startServer(t,
			"-listen", "127.0.0.1:0",
			"-transport", "tcp",
			"-secret", "interactive-test-secret",
			"-socks5", socksAddr,
			"-log-level", "debug",
			"-interactive",
			"-sleep-interval", "2s",
			"-sleep-jitter", "0",
		)

		// Start dormant client.
		clientCmd := exec.Command(clientBin,
			"-server", serverAddr,
			"-transport", "tcp",
			"-secret", "interactive-test-secret",
			"-log-level", "debug",
			"-dormant",
		)
		clientStderr, err := clientCmd.StderrPipe()
		if err != nil {
			t.Fatal(err)
		}
		if err := clientCmd.Start(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			clientCmd.Process.Kill()
			clientCmd.Wait()
		})

		// Wait for client to authenticate on first check-in.
		if _, err := waitForLog(clientStderr, "authenticated", 10*time.Second); err != nil {
			t.Fatal(err)
		}

		// Wait for client registration on server side.
		if _, err := waitForLog(serverStderr, "client registered", 10*time.Second); err != nil {
			t.Fatal(err)
		}

		// Wait for client to report check-in complete (disconnects and sleeps).
		if _, err := waitForLog(clientStderr, "check-in complete", 10*time.Second); err != nil {
			t.Fatal(err)
		}

		// Wait for the client to reconnect (second check-in).
		if _, err := waitForLog(clientStderr, "authenticated", 15*time.Second); err != nil {
			t.Fatal(err)
		}

		// Wait for server to see the reconnection.
		if _, err := waitForLog(serverStderr, "client reconnected", 10*time.Second); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("port_forward", func(t *testing.T) {
		t.Parallel()

		// Target address for port forwarding.
		targetAddr := strings.TrimPrefix(target.URL, "http://")
		fwdBind := getFreePort(t)
		socksAddr := getFreePort(t)

		serverAddr, _, serverStderr := startServer(t,
			"-listen", "127.0.0.1:0",
			"-transport", "tcp",
			"-secret", "fwd-test-secret",
			"-socks5", socksAddr,
			"-log-level", "debug",
			"-local-forward", fwdBind+":"+targetAddr,
		)

		startClient(t, serverStderr,
			"-server", serverAddr,
			"-transport", "tcp",
			"-secret", "fwd-test-secret",
			"-reconnect=false",
			"-log-level", "debug",
		)

		// Give port forward time to set up.
		time.Sleep(500 * time.Millisecond)

		// Connect directly to the forwarded port.
		resp, err := http.Get("http://" + fwdBind)
		if err != nil {
			t.Fatalf("HTTP GET through port forward: %v", err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		if string(b) != body {
			t.Fatalf("body = %q, want %q", string(b), body)
		}
	})
}
