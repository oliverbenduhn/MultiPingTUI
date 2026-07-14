# MultiPingTUI

`MultiPingTUI` is an enhanced Terminal UI for monitoring multiple network targets simultaneously via ICMP ping, TCP probing, or the system `ping` command. Interactive, keyboard-driven, with live filtering, sorting, and per-host drill-down. Optional HTTP status mirror and JSON transition logging for automation.

**Binary:** `mping`

## Quickstart

```bash
# Build
go build -o mping

# TUI mode (default) — interactive
./mping localhost google.com 8.8.8.8

# TCP probing
./mping tcp://google.com:443 tcp://[::1]:22

# CIDR expansion (auto-discovers all hosts in subnet)
./mping 192.168.1.0/24

# Non-interactive legacy mode (pterm-based table)
./mping -notui localhost google.com

# One-shot for scripting
./mping -once -only-online 192.168.1.0/24

# Web status server (http://127.0.0.1:8080, JSON + dashboard)
./mping -webserver 8080 localhost google.com
```

## Features

- **Interactive TUI** — bubbletea/lipgloss, Midnight Commander–style navigation
- **Three ping backends** — pure-Go ICMP (`pro-bing`), system `ping` subprocess, TCP SYN/ACK (`tcp-shaker`)
- **Live filtering** — Smart (online or ever-replied), Online, Offline, All
- **Sorting** — name, status, RTT, last-seen, IP
- **Detail view** — per-host stats, traceroute, dashboard, history
- **CIDR expansion** — scan entire subnets, network/broadcast auto-excluded
- **Adaptive interval** — auto-enabled for subnets, idle hosts pinged every 10s instead of 1s
- **HTTP status mirror** — read-only JSON + dashboard at `127.0.0.1:<port>`
- **Transition logging** — JSONL log of every state change

## Keyboard (TUI mode)

| Key | Action |
|---|---|
| `↑/↓` or `j/k` | Navigate hosts (first keypress selects) |
| `Enter` | Toggle detail view |
| `f` | Cycle filter: Smart → Online → Offline → All |
| `s` | Cycle sort: Name → Status → RTT → LastSeen → IP |
| `r` | Cycle update rate: 100ms → 1s → 5s → 30s |
| `1`–`6` | Toggle column visibility |
| `d` | Toggle dashboard view |
| `t` | Traceroute (in detail view) |
| `e` | Edit config (`MPING_EDITOR` env overrides default editor) |
| `g` | Group by subnet |
| `del` | Hide selected host |
| `ins` | Show all hidden hosts |
| `pgup`/`pgdn` | Scroll page |
| `Esc` | Back / cancel |
| `q` or `Ctrl+C` | Quit |

## CLI flags

| Flag | Purpose |
|---|---|
| `-notui` | Legacy pterm display (non-interactive) |
| `-once` | Ping once and exit (scripting) |
| `-s` | Force system `ping` (fallback if ICMP unavailable) |
| `-no-dns` | Skip reverse DNS lookups (faster startup on large subnets) |
| `-only-online` / `-only-offline` | Initial filter for TUI and `-once` output |
| `-log <file>` | JSONL transition log |
| `-webserver <port>` | HTTP status mirror (binds `127.0.0.1`) |
| `-adaptive` | Force adaptive ping interval (auto-on for subnets) |
| `-debug` | Verbose startup progress |
| `-q` | Quiet — no display, log only |

## Build

```bash
# Standard
go build -o mping

# Cross-platform release (linux/amd64 + windows/amd64, .deb, Arch PKGBUILD)
./scripts/release.sh          # outputs in dist/
```

Cross-compilation requires `go mod vendor` first. Version info is injected via `-ldflags` (see `scripts/release.sh`). Single source of truth: `var Version = "v..."` in `main.go`. Bump with `./scripts/bump_version.sh <version>`.

## Installation

### From .deb

```bash
sudo dpkg -i mping_<version>_amd64.deb
sudo setcap cap_net_raw=+ep /usr/bin/mping   # for unprivileged ICMP
```

### From source

```bash
git clone https://github.com/oliverbenduhn/MultiPingTUI
cd MultiPingTUI
go build -o mping && sudo cp mping /usr/local/bin/
```

### Unprivileged ICMP (no root required)

```bash
sudo sysctl -w net.ipv4.ping_group_range="0 2147483647"
sudo setcap cap_net_raw=+ep /usr/bin/mping
```

Without this, ICMP falls back to the system `ping` binary (needs root or the same sysctl).

## Architecture

Three ping backends behind a `PingWrapperInterface` factory, a `WrapperHolder` for lifecycle, a bubbletea-based TUI as the only interactive surface, and an optional read-only HTTP status server.

Module overview and data-flow diagram: [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md)

HTTP endpoint reference: [docs/API.md](docs/API.md)

Operations (sysctl, setcap, self-update, logging): [docs/OPERATIONS.md](docs/OPERATIONS.md)

## Development

```bash
go test -race -count=1 ./...       # 29 tests, ~4s
go test -race -v -run TestFlow5    # size-matrix only
```

Test architecture and conventions: [CONTRIBUTING.md](CONTRIBUTING.md)

AI-agent context: [CLAUDE.md](CLAUDE.md) · [AGENTS.md](AGENTS.md) · [.cursorrules](.cursorrules)

## License

MIT — see [LICENSE](LICENSE).