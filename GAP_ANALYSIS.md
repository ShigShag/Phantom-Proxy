# Gap Analysis: Phantom Proxy vs Chisel & Ligolo-ng

## Feature Comparison Matrix

| Feature | Phantom Proxy | Chisel | Ligolo-ng |
|---------|:---:|:---:|:---:|
| **Transports** | | | |
| TCP | Yes | - | - |
| TLS | Yes | Yes (WSS) | Yes |
| SSH | Yes | - | - |
| WebSocket | Yes | Yes (primary) | Yes |
| **Tunneling** | | | |
| SOCKS5 (forward) | Yes | Yes | - |
| SOCKS5 (reverse) | - | Yes | - |
| Local port forward | Yes | Yes | - |
| Remote port forward | Yes | Yes | - |
| TUN interface | - | - | Yes |
| UDP tunneling | - | Yes | Yes (via TUN) |
| ICMP tunneling | - | - | Yes (via TUN) |
| stdio / ProxyCommand | - | Yes | - |
| **Authentication** | | | |
| Shared secret (HMAC) | Yes | - | - |
| Argon2id KDF | Yes | - | Yes (web UI) |
| User/pass credentials | - | Yes | Yes (web UI) |
| Multi-user authfile | - | Yes | Yes (config) |
| TLS fingerprint pinning | - | Yes | Yes |
| mTLS | Yes | Yes | - |
| Let's Encrypt autocert | - | Yes | Yes |
| **Stealth / Evasion** | | | |
| Binary obfuscation (garble) | Yes | - | - |
| Build-time string replacement | Yes | - | - |
| Dormant/beacon mode | Yes | - | - |
| Sleep/wake/jitter | Yes | - | - |
| Backend HTTP proxy (blending) | - | Yes | - |
| Custom HTTP headers | Partial (Host, UA) | Yes | Partial (UA) |
| SNI override / domain fronting | - | Yes | - |
| Upstream proxy support | - | Yes | Yes |
| **Multiplexing** | | | |
| yamux | Yes | - | - |
| SSH channels | - | Yes | - |
| Gvisor netstack | - | - | Yes |
| **Client Management** | | | |
| Interactive C2 shell | Yes | - | Yes |
| Multi-client support | Yes | - | Yes |
| Web UI | - | - | Yes |
| Dormant sleeper agents | Yes | - | - |
| Command queueing (offline) | Yes | - | - |
| Auto-reconnect | Yes | Yes | Yes |
| Client info reporting | Yes | - | Yes |
| **Pivoting** | | | |
| Double/multi-hop pivot | - | - | Yes |
| Listener on agent | - | - | Yes |
| **Platform** | | | |
| Linux (amd64, arm64) | Yes | Yes | Yes |
| macOS (amd64, arm64) | Yes | Yes | Yes |
| Windows (amd64, arm64) | Yes | Yes | Yes |
| MIPS / embedded | - | Yes | - |
| OpenBSD / FreeBSD | - | Yes | Yes |
| Single-binary (client+server) | - | Yes | - |
| Kali Linux package | - | Yes | Yes |

## Identified Gaps

### Gap 1: UDP Tunneling

**Present in:** Chisel (`/udp` suffix), Ligolo-ng (via TUN)
**Impact:** High — DNS, SNMP, NTP, and other UDP services are untunnelable today.

Chisel encapsulates UDP datagrams within the SSH tunnel. Ligolo-ng handles it transparently via TUN. Phantom Proxy only handles TCP streams through SOCKS5.

**Approach:** Add a UDP relay mode. The client listens on a local UDP port, encapsulates datagrams into framed messages over a yamux stream, and the server delivers them to the target UDP endpoint (and vice versa). This avoids needing a TUN interface while providing practical UDP tunneling.

---

### Gap 2: Reverse SOCKS5

**Present in:** Chisel (`R:socks`)
**Impact:** High — This is Chisel's most-used pentest feature. The server opens a SOCKS5 listener and routes traffic back through the client. Phantom Proxy's current SOCKS5 already works this way in headless mode, but there is no way for the _client_ to expose a SOCKS5 listener on its own network (true reverse SOCKS).

**Approach:** Allow the client to open a SOCKS5 listener (or any listener) that funnels connections back through the tunnel to the server side. This is the inverse of the current flow and enables pivoting from a compromised host's network.

---

### Gap 3: Upstream Proxy Support (HTTP CONNECT / SOCKS5 egress)

**Present in:** Chisel (`--proxy`), Ligolo-ng (`--socks`)
**Impact:** High — Corporate networks often require traversing an outbound proxy. Without this, the client cannot connect out from many restricted environments.

**Approach:** Add a `--proxy` flag to the client that routes the initial transport connection through an upstream HTTP CONNECT or SOCKS5 proxy. This wraps the dialer, so it works with any transport.

---

### Gap 4: TUN Interface Mode

**Present in:** Ligolo-ng (core feature)
**Impact:** Medium-High — Eliminates the need for `proxychains`. Tools like nmap, Impacket, and Rubeus work natively. However, it requires root/admin on the operator machine.

**Approach:** Add an optional TUN mode on the server side using a userland netstack (e.g., `gvisor.dev/gvisor/pkg/tcpip`). When enabled, the server creates a TUN interface and translates IP packets to yamux stream operations through the client. This is a large feature but a major differentiator vs Chisel.

---

### Gap 5: SNI Override / Domain Fronting

**Present in:** Chisel (`--sni`)
**Impact:** Medium-High — Allows the TLS ClientHello to advertise a different hostname than the actual server, enabling CDN-based domain fronting and bypassing SNI-based filtering.

**Approach:** Add a `--tls-sni` flag to the client that overrides the `ServerName` in the TLS config. Straightforward to implement for the TLS and HTTP (WSS) transports.

---

### Gap 6: Backend HTTP Proxy (Blending)

**Present in:** Chisel (`--backend`)
**Impact:** Medium-High — Makes the server look like a normal web server. Non-WebSocket HTTP requests are reverse-proxied to a legitimate backend. Only WebSocket upgrades are handled by the tunnel. Defeats casual inspection and port scanning.

**Approach:** For the HTTP/WebSocket transport, add a `--backend` flag to the server that proxies non-upgrade HTTP requests to a specified URL using `httputil.ReverseProxy`.

---

### Gap 7: Let's Encrypt Autocert

**Present in:** Chisel (`--tls-domain`), Ligolo-ng (`-autocert`)
**Impact:** Medium — Valid CA-signed certificates reduce TLS inspection suspicion and eliminate the need for `-tls-skip-verify` or fingerprint pinning on the client.

**Approach:** Integrate `golang.org/x/crypto/acme/autocert` into the TLS and HTTP transports. Add a `--tls-domain` server flag. Requires port 443 for the ACME challenge.

---

### Gap 8: Multi-hop / Double Pivoting

**Present in:** Ligolo-ng (listener chaining)
**Impact:** Medium — Enables reaching networks that are multiple hops deep (e.g., DMZ -> internal -> database segment).

**Approach:** Allow the client to open listeners (`listener_add`) that accept new agent connections and relay them back through the existing tunnel to the server. The server sees these as additional clients. Alternatively, implement a `--chain` mode where a client can act as a relay for another client.

---

### Gap 9: stdio / ProxyCommand Mode

**Present in:** Chisel (`stdio:host:port`)
**Impact:** Medium — Enables transparent SSH proxying via `ssh -o ProxyCommand='phantom-client stdio ...'`. Useful for operators who want seamless SSH access through the tunnel.

**Approach:** Add a `stdio` mode to the client that connects stdin/stdout directly to a single tunneled connection instead of running as a long-lived daemon.

---

### Gap 10: Single Binary

**Present in:** Chisel (server + client in one binary)
**Impact:** Medium — Reduces operational complexity. One file to transfer and manage.

**Approach:** Merge `cmd/server` and `cmd/client` into a single `main.go` with subcommands (`phantom server ...`, `phantom client ...`). Use `os.Args[1]` dispatch or a library like `cobra` (though stdlib flag parsing with subcommands is fine).

---

### Gap 11: Multi-user Authentication with ACLs

**Present in:** Chisel (`--authfile` with regex ACLs), Ligolo-ng (web UI users)
**Impact:** Medium-Low — Useful for team operations where different operators have different tunnel access levels.

**Approach:** Add an `--authfile` flag accepting a JSON/YAML file mapping `user:secret` pairs to allowed tunnel patterns.

---

### Gap 12: Web UI

**Present in:** Ligolo-ng (web interface with multiplayer)
**Impact:** Low-Medium — Nice for team operations and visibility, but the interactive shell already covers core management.

**Approach:** Embed a small web server (e.g., on `--web :8080`) serving a single-page app that mirrors shell commands via a REST/WebSocket API.

---

### Gap 13: Custom HTTP Headers

**Present in:** Chisel (`--header`, repeatable)
**Impact:** Low-Medium — Already partially supported (Host, User-Agent for WebSocket transport). Full arbitrary header support would be a small addition.

**Approach:** Add a repeatable `--http-header "Name: Value"` flag to the client's HTTP transport dialer.

---

### Gap 14: ICMP Tunneling

**Present in:** Ligolo-ng (via TUN)
**Impact:** Low — Useful for ping-based connectivity checks through the tunnel, but rarely critical.

**Approach:** Only practical with TUN mode (Gap 4). If TUN is implemented, ICMP comes along naturally.

---

## Recommended Implementation Order

Prioritized by **operator impact**, **competitive parity**, and **implementation effort**.

| Priority | Gap | Effort | Rationale |
|:---:|------|:---:|-----------|
| **1** | **Upstream proxy support** | Small | Unlocks corporate/restricted environments. Simple dialer wrapper. |
| **2** | **SNI override** | Small | One-line TLS config change. Big evasion win. |
| **3** | **Custom HTTP headers** | Small | Completes the HTTP transport's evasion surface. Tiny change. |
| **4** | **Backend HTTP proxy** | Small-Medium | Major stealth feature. `httputil.ReverseProxy` does the heavy lifting. |
| **5** | **Reverse SOCKS5** | Medium | Chisel's killer feature for pentesting. Reuses existing SOCKS5 code. |
| **6** | **UDP tunneling** | Medium | Opens up DNS tunneling and more. New framing type + relay logic. |
| **7** | **Let's Encrypt autocert** | Small | Drop-in `autocert` integration. Reduces operator setup friction. |
| **8** | **stdio / ProxyCommand** | Small | Thin wrapper around a single tunneled stream. High operator convenience. |
| **9** | **Single binary** | Small-Medium | Merge entry points. Quality-of-life improvement for deployments. |
| **10** | **Multi-hop pivoting** | Medium-Large | Listener-on-agent model. Requires new protocol messages and relay logic. |
| **11** | **TUN interface mode** | Large | Gvisor netstack integration. Major differentiator but significant work. |
| **12** | **Multi-user auth + ACLs** | Medium | Authfile with regex patterns. Useful for team ops. |
| **13** | **Web UI** | Large | Full frontend + API. Low priority given the existing shell. |
| **14** | **ICMP tunneling** | Large | Depends on TUN mode. Niche use case. |

### Suggested Phases

**Phase 1 — Quick Wins (1-2 days each)**
Gaps 1-4: Upstream proxy, SNI override, custom headers, backend proxy. These are small changes with outsized impact on evasion and usability in restricted networks.

**Phase 2 — Competitive Parity with Chisel (3-5 days each)**
Gaps 5-9: Reverse SOCKS5, UDP tunneling, autocert, stdio mode, single binary. After this phase, Phantom Proxy matches or exceeds Chisel's feature set while retaining its unique C2 capabilities.

**Phase 3 — Ligolo-ng Parity (1-2 weeks each)**
Gaps 10-11: Multi-hop pivoting and TUN interface. These are the big-ticket items that would make Phantom Proxy a superset of both competitors.

**Phase 4 — Team Operations (optional)**
Gaps 12-14: Multi-user auth, web UI, ICMP. Nice-to-haves for larger team deployments.

---

## Phantom Proxy's Existing Advantages

Features that **neither** Chisel nor Ligolo-ng offer:

| Feature | Description |
|---------|-------------|
| **Dormant sleeper agents** | Polling-based check-in with configurable interval + jitter. No persistent connection to detect. |
| **Command queueing** | Queue wake/sleep/config commands for offline agents. |
| **Sleep/wake protocol** | Dynamically activate and deactivate agents on demand. |
| **Binary obfuscation** | `garble -literals -tiny` + ldflags string replacement. Neither competitor has this built in. |
| **4 swappable transports** | TCP, TLS, SSH, and WebSocket with a pluggable registry. Chisel is WebSocket-only; Ligolo-ng is TLS/WebSocket-only. |
| **Build-time stealth config** | All identifiable strings replaceable via ldflags for stealth builds. |
| **HMAC-SHA256 + Argon2id auth** | Cryptographically strong challenge-response auth. Chisel uses simple user:pass; Ligolo-ng uses TLS pinning. |

These features position Phantom Proxy uniquely as a **C2-aware tunneling tool** — a niche that neither competitor occupies.
