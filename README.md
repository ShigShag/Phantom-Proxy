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

## Flags

### Server

| Flag             | Default          | Description                     |
| ---------------- | ---------------- | ------------------------------- |
| `-listen`        | `:4444`          | Listen address                  |
| `-transport`     | `tcp`            | Transport: tcp, tls, ssh, http  |
| `-secret`        | (required)       | Shared secret                   |
| `-socks5`        | `127.0.0.1:1080` | SOCKS5 listen address           |
| `-local-forward` |                  | Local port forward (repeatable) |
| `-cert`          | (auto-generated) | TLS certificate                 |
| `-certkey`       | (auto-generated) | TLS private key                 |
| `-tls-ca`        |                  | CA cert for mTLS client auth    |
| `-key`           | (auto-generated) | SSH host key                    |
| `-http-path`     | `/ws`            | WebSocket path                  |
| `-log-level`     | `info`           | debug, info, warn, error        |

### Client

| Flag               | Default          | Description                      |
| ------------------ | ---------------- | -------------------------------- |
| `-server`          | `localhost:4444` | Server address                   |
| `-transport`       | `tcp`            | Transport: tcp, tls, ssh, http   |
| `-secret`          | (required)       | Shared secret                    |
| `-reconnect`       | `true`           | Auto-reconnect with backoff      |
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
