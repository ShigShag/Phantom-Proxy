# CLAUDE.md

## Project Overview

Phantom Proxy is a Go tunneling & proxy tool with swappable transports (TCP, TLS, SSH, WebSocket). The server runs on the operator machine, the client connects out from the target. Authentication uses HMAC-SHA256 over a shared secret with Argon2id key derivation. Streams are multiplexed over yamux.

The server supports two modes:
- **Headless (default)**: Single-client mode, SOCKS5 routes through the connected client automatically.
- **Interactive (`-interactive`)**: Multi-client C&C mode with a command shell. Clients connect as dormant sleeper agents and are activated on demand.

## Build & Run

```bash
make build          # -> build/phantom-server, build/phantom-client
make stealth        # obfuscated build via garble -> build/svchost, build/svcmgr
go test ./...       # run all tests
go test -race ./... # with race detector
go test -short ./...# skip integration tests
```

## Project Structure

```
cmd/server/main.go           # Server entry point (flags, accept loop, handleClient, handleClientInteractive)
cmd/client/main.go           # Client entry point (flags, reconnect loop, connect, check-in cycle for dormant)
internal/buildcfg/           # Injectable string vars for stealth builds (ldflags -X)
internal/crypto/             # Argon2id key derivation, HMAC-SHA256, nonce generation, cert/key generation
internal/proto/              # Wire protocol: framed messages [1B type][4B len][payload], handshake, keepalive, C&C commands
internal/transport/          # Transport interface + registry (Register/Get/Available)
internal/transport/tcp/      # Plain TCP transport
internal/transport/tls/      # TLS transport (auto-generates ephemeral P-256 certs)
internal/transport/ssh/      # SSH channel transport (auto-generates ED25519 keys)
internal/transport/http/     # WebSocket transport (coder/websocket library)
internal/mux/                # Yamux session wrappers (ServerSession, ClientSession, Relay)
internal/proxy/              # SOCKS5 server, stream handling (HandleStreams), port forwarding
internal/registry/           # Thread-safe client registry for interactive mode (Register, FindOrRegister, QueueCmd, etc.)
internal/shell/              # Interactive C&C shell with readline (line editing, history, tab completion)
pkg/config/                  # ServerConfig, ClientConfig, PortForward types
```

## Key Architecture

- **Connection flow**: Client dials server via transport -> yamux session -> stream 0 is control (auth + keepalive) -> streams 1+ for data (SOCKS5 / port forwards)
- **Auth**: Server sends random 32-byte nonce, client computes HMAC-SHA256(DeriveKey(secret, DeterministicSalt("phantom-proxy")), nonce), server verifies
- **Message framing**: `[1B type][4B big-endian length][payload]`, max 1 MiB per message
- **Transport registry**: Transports self-register via `init()` functions; imported with blank identifier `_ "..."`
- **Stealth build**: `make stealth` uses `garble -literals -tiny` + `-ldflags -X` to replace all identifiable strings in `internal/buildcfg` with innocuous values and obfuscate symbols. Normal `make build` is unaffected.
- **Interactive mode**: Server maintains a `Registry` of connected clients. Dormant clients disconnect between check-ins (polling model). The shell uses `chzyer/readline` for line editing, history, and tab completion of commands + client IDs. `slog` output is redirected through readline to prevent prompt corruption. Shell has two entry points: `Run()` (readline, real terminal) and `RunWithIO()` (bufio.Scanner, for tests). The shell sends commands directly to online clients or queues them for offline clients. The registry tracks online/offline state and pending commands per client.
- **Dormant check-in cycle**: Client connects → authenticates (with `Dormant: true`) → server drains pending commands → if CmdWake is pending, client stays connected in active mode; otherwise server sends `CmdCheckinDone` and client disconnects → sleeps for interval±jitter → reconnects. `FindOrRegister` matches by hostname for reconnections, preserving the client's registry entry and ID.
- **Sleep/wake protocol**: CmdWake tells the client to start `HandleStreams`; CmdSleep cancels it. CmdSleepCfg adjusts the beacon interval with jitter. CmdCheckinDone tells a dormant client to disconnect (no pending commands).

## Testing Conventions

- All tests use Go stdlib `testing` package, no external test dependencies
- Unit tests use `net.Pipe()` for protocol-level tests and `bytes.Buffer` for message framing
- `net.Pipe()` does NOT support `CloseWrite()` — use TCP connections or `http.ReadResponse` when half-close is needed
- Integration tests (`integration_test.go`) build real binaries in `TestMain`, use ephemeral ports (`:0`), parse listen addresses from log output
- Integration tests use `httptest.Server` as target — no external network calls
- Integration tests cover: tcp, tls, ssh, http transports, bad_secret, port_forward, dormant_checkin
- All tests pass with `-race` flag

## Code Style

- Go 1.25, uses range-over-int (`for range 2`)
- `slog` for structured logging throughout
- Transport implementations follow the `transport.Transport` interface: `Dial`, `Listen`, `Name`
- Error wrapping with `fmt.Errorf("context: %w", err)`
- No external test dependencies — stdlib only

## Maintenance

- On major changes or feature additions, update both `README.md` and `CLAUDE.md` to reflect the new state of the project
