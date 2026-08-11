# MultiPingTUI

[![GitHub license](https://img.shields.io/github/license/oliverbenduhn/MultiPingTUI)](./LICENSE)
[![Go version](https://img.shields.io/github/go-mod/go-version/oliverbenduhn/MultiPingTUI)](./go.mod)
[![Release](https://img.shields.io/github/v/release/oliverbenduhn/MultiPingTUI)](https://github.com/oliverbenduhn/MultiPingTUI/releases/latest)

`MultiPingTUI` is an enhanced TUI (Terminal User Interface) version of
[`multiping`](https://github.com/babs/multiping), written in Go. It monitors
multiple network targets simultaneously via ICMP ping, TCP probing, or the
operating system's `ping` command, and ships with an interactive bubbletea
interface, a local HTTP status mirror, and optional JSON logging of state
transitions. The binary is called `mping`.

---

## Features at a glance

- 🎨 **Interactive TUI** — Midnight Commander / Claude Code-inspired interface.
- ⌨️  **Keyboard-first navigation** — arrow keys, vim-style (`j`/`k`), shortcuts.
- 🔍 **Live filtering** — Smart / Online / Offline / All.
- 🔀 **Live sorting** — Name / Status / RTT / Last Seen / IP.
- 👁️ **Toggleable columns** — six columns, on/off via number keys `1`–`6`.
- 📊 **Detail view** — per-host RTT history, uptime, last loss, error info.
- 📈 **Dashboard view** — totals, RTT distribution, top offline / top RTT.
- 🛰️ **Traceroute on demand** — from the detail view (`t`).
- 🌐 **CIDR subnet scanning** — `192.168.1.0/24` expands automatically (up to
  65 536 hosts, with adaptive ping intervals).
- ⚡ **Adaptive intervals** — hosts never seen online are probed every 10 s;
  as soon as one replies, it switches to 1 s. Auto-enabled for any CIDR.
- 📝 **Transition logging** — append-only NDJSON log of every up/down event.
- 🌐 **Web status mirror** — local HTTP server mirrors the TUI live view
  (`/`, `/dashboard`, `/api/dashboard`, `/json`, `/text`, `/state`,
  `/view`, `/trace`). The web UI and TUI share filter / sort / columns.
- 🧠 **Periodic reverse DNS** — PTR lookups refresh every 60 s with 500 ms
  per-lookup timeout and a 1-hour positive / 5-minute negative cache.
- 🛠️ **In-terminal config editor** — `e` opens `~/.config/mping/config.yaml`
  with syntax highlighting; `Ctrl+S` validates & saves.
- 🛡️ **Auto-fallback** — if raw ICMP is denied, `mping` automatically falls
  back to the system `ping` instead of marking every host offline.
- 🔄 **Self-update** — `mping -update` downloads the latest GitHub release
  binary for your OS/arch.

---

## Table of contents

1. [Prerequisites](#prerequisites)
2. [Quickstart](#quickstart)
3. [Usage](#usage)
4. [TUI keyboard shortcuts](#tui-keyboard-shortcuts)
5. [Probing methods](#probing-methods)
6. [Configuration](#configuration)
7. [Web status server](#web-status-server)
8. [Transition logging](#transition-logging)
9. [Adaptive ping intervals](#adaptive-ping-intervals)
10. [Modes](#modes)
11. [Building & releasing](#building--releasing)
12. [Troubleshooting](#troubleshooting)
13. [Acknowledgements](#acknowledgements)
14. [License](#license)

---

## Prerequisites

- **Go 1.22 or newer** to build from source. CI builds with `1.24.10`.
- For ICMP raw sockets on Linux as an unprivileged user:
  ```bash
  sudo sysctl -w net.ipv4.ping_group_range="0 2147483647"
  ```
- For TCP probing on Linux/FreeBSD/OpenBSD: nothing extra (uses raw SYN).
- For the traceroute feature (`t` in the detail view): the system
  `traceroute` (preferred), `tracepath`, or `tracert` (Windows).
- For self-update: outbound HTTPS to `api.github.com` and `github.com`.

> If raw ICMP is denied, `mping` detects this on startup and falls back to
> the system `ping` command automatically. See [Auto-fallback](#auto-fallback).

---

## Quickstart

```bash
# 1. Build
git clone https://github.com/oliverbenduhn/MultiPingTUI.git
cd MultiPingTUI
go build -o mping

# 2. Run (TUI mode is default)
./mping localhost github.com 8.8.8.8

# 3. Inside the TUI:
#    f      cycle filter
#    s      cycle sort
#    e      edit config (opens ~/.config/mping/config.yaml)
#    d      toggle dashboard
#    Enter  show host details
#    t      run traceroute from detail view
#    q      quit (saves view state)
```

The next launch starts with the same hosts and view state, because the
config file is loaded automatically.

---

## Usage

```text
Usage: mping [flags] host [hosts...]
```

### All flags

| Flag | Default | Description |
|---|---|---|
| `-privileged` | `false` | Switch to privileged ICMP mode (auto-enabled for root / Windows). Ignored with `-s`. |
| `-size` | `24` | Pure-Go ICMP payload size in bytes (28-byte header not included; total = size + 28). Common MTU-safe values: `1472`, `8972`. |
| `-s` | `false` | Use the system `ping` instead of pure-Go. |
| `-ping-options` | `""` | Quoted extra args passed to system `ping`. Implies `-s`. |
| `-q` | `false` | Quiet mode — no display. Pair with `-log`. |
| `-log FILE` | `""` | Append NDJSON transition log to `FILE`. |
| `-update` | `false` | Self-update from latest GitHub release. |
| `-tui` | `true` | Use the TUI (deprecated — prefer `-notui`). |
| `-notui` | `false` | Disable TUI, use legacy `pterm` display. |
| `-hostfile FILE` | `""` | Read hosts (one per line, CIDR allowed). |
| `-web-port PORT` | `8080` | Port for the local web status server. `0` disables it. |
| `-pprof ADDR` | `""` | Enable `net/http/pprof` at the given addr (e.g. `localhost:6060`). |
| `-once` | `false` | Ping each target once, then exit (scripting). |
| `-only-online` | `false` | Initial filter: online hosts only. |
| `-only-offline` | `false` | Initial filter: offline hosts only. |
| `-debug` | `false` | Print debug info to stderr during startup. |
| `-no-dns` | `false` | Skip reverse-DNS lookups (fast startup on big subnets). |
| `-edit-config` | `false` | Open the config editor and exit. |
| `-adaptive` | `false` | Force adaptive ping intervals (auto-enabled for CIDRs). |
| `-webserver` | `false` | Run in pure webserver mode — no TUI, only the HTTP status server in the background. Requires `-web-port > 0`. |
| `-h` | | Show the full help text. |

### Host strings

| Form | Method |
|---|---|
| `example.com` / `8.8.8.8` | ICMP, default address family |
| `ip://example.com` | ICMP, default address family |
| `ip4://example.com` | ICMP, force IPv4 |
| `ip6://example.com` | ICMP, force IPv6 |
| `tcp://example.com:443` | TCP probe |
| `tcp://[::1]:22` | TCP probe, IPv6 (bracket notation required) |
| `tcp4://example.com:80` | TCP probe, force IPv4 |
| `tcp6://example.com:80` | TCP probe, force IPv6 |
| `192.168.1.0/24` | CIDR — expands to every usable host |

---

## TUI keyboard shortcuts

The TUI is the default. `mping -h` documents all flags; this table covers the
keys.

| Key | Action |
|---|---|
| `↑` / `k` | Move cursor up |
| `↓` / `j` | Move cursor down |
| `pgup` / `pgdn` | Page scroll (or scroll traceroute output in detail view) |
| `Enter` | Open / close detail view for selected host |
| `t` | Run traceroute (only in detail view) |
| `d` | Toggle dashboard summary |
| `e` | Edit `~/.config/mping/config.yaml` in an embedded editor |
| `f` | Cycle filter: Smart → Online → Offline → All |
| `s` | Cycle sort: Name → Status → RTT → Last Seen → IP |
| `r` | Cycle update rate: 100 ms → 1 s → 5 s → 30 s |
| `g` | Toggle "group by subnet" |
| `1`–`6` | Toggle column visibility (1: Status, 2: Name, 3: IP, 4: RTT, 5: Last Reply, 6: Last Loss) |
| `delete` (Del) | Hide host under cursor (toggleable filter) |
| `insert` (Ins) | Show all hosts (un-hide everything) |
| `Esc` | Back to list / cancel |
| `q` / `Ctrl+C` | Quit (saves view state) |

The cursor starts unselected (`-1`); the first navigation key selects the
first host. The viewport auto-scrolls and shows a `[start-end/total]`
indicator when the list is longer than the screen.

---

## Probing methods

### Pure-Go ICMP (default)

Uses [`prometheus-community/pro-bing`](https://github.com/prometheus-community/pro-bing).
On Linux the Do-Not-Fragment bit is set. On Windows or when running as root
the wrapper switches to privileged mode automatically.

If a one-shot probe on `127.0.0.1` fails with a `permission denied` error,
`mping` prints a notice and **falls back to the system `ping`** so the
session keeps working. To force privileged pure-Go mode regardless, use
`-privileged` together with `setcap`:

```bash
sudo setcap cap_net_raw=+ep ./mping
```

### System `ping` (`-s`)

Spawns the OS `ping` and parses its stdout. Slower startup, irregular
intervals under parallel load, so `TimeoutThresholdNS` is raised to 5 s
when this mode is active. `ping-options` lets you pass extra flags to the
system command, e.g. `-ping-options "-Q 2"`.

IPv6 targets first try `ping6` then fall back to `ping`.

### TCP probing (`tcp://host:port`)

- **Linux / FreeBSD / OpenBSD**: SYN/ACK via
  [`tevino/tcp-shaker`](https://github.com/tevino/tcp-shaker) — does not
  trigger an `accept()` on the listening application. Caveat: middleboxes
  performing SYN proxying may give false positives.
- **macOS / Windows**: full three-way handshake.

---

## Configuration

`mping` reads / writes `~/.config/mping/config.yaml`. Override the path via
the `MPING_CONFIG` environment variable. The file is created automatically
on first run with sane defaults if it doesn't exist.

Example:

```yaml
# mping config
# Override the location via MPING_CONFIG

hosts:
  - "localhost"
  - "github.com"
  - "192.168.1.0/24"

view:
  # 0=All, 1=Smart (online or ever-seen), 2=Online, 3=Offline
  filter: 1
  # 0=Name, 1=Status, 2=RTT, 3=Last Seen, 4=IP
  sort: 4
  # 0=100ms, 1=1s, 2=5s, 3=30s
  rate: 1
  # Visible columns (1..6): 1=Status 2=Name 3=IP 4=RTT 5=Last Reply 6=Last Loss
  cols: [1, 2, 3, 4, 5, 6]
  group_by_subnet: true
  hidden: {}
```

State (filter, sort, visible columns, hidden hosts, rate, last host list) is
written back when you quit the TUI. Pressing `e` opens the file in the
embedded `smidgen` editor with monokai syntax highlighting; `Ctrl+S`
validates before saving, `Esc` closes without saving.

See [`docs/CONFIGURATION.md`](./docs/CONFIGURATION.md) for the full schema,
validation rules, and edge cases.

---

## Web status server

In TUI mode a small local HTTP server is started on `127.0.0.1:8080` by
default. It mirrors the live TUI view: changing filter / sort / columns /
hidden hosts in either UI updates the other.

| Path | Method | Returns |
|---|---|---|
| `/` | GET | Live HTML view |
| `/dashboard` | GET | Dashboard summary page |
| `/api/dashboard` | GET | JSON dashboard (totals, RTT dist, top offline / RTT, recent transitions) |
| `/text` | GET | Plain text summary |
| `/json` | GET | JSON array of host states |
| `/state` | GET | JSON object `{ view, statuses, updated }` |
| `/view` | GET | Current `ServerView` |
| `/view` | POST | Patch `ServerView` (filter / sort / rate / hidden / cols / group_by_subnet) |
| `/trace` | GET | Returns trace result for `?key=…` |
| `/trace` | POST | Starts a traceroute for `{ "key": "…" }` |

Customize the port with `-web-port <port>`. Use `-web-port 0` to disable
the server entirely. In pure webserver mode (`-webserver`) the CLI runs only
the HTTP server with no terminal UI.

See [`docs/API.md`](./docs/API.md) for full request/response shapes and
limits.

---

## Transition logging

`mping -log transitions.json` writes one JSON line per state transition.
Format:

```json
{"Timestamp":"2024-05-08T12:34:56.789Z","UnixNano":1715169296789000000,"Host":"google.com","Ip":"142.250.190.46","Transition":"down to up","State":true}
```

| Field | Type | Meaning |
|---|---|---|
| `Timestamp` | string | RFC3339Nano UTC timestamp |
| `UnixNano` | int64 | Same instant in nanoseconds |
| `Host` | string | The host string as supplied (incl. scheme) |
| `Ip` | string | The resolved IP |
| `Transition` | string | `up to down` or `down to up` |
| `State` | bool | `true` if alive, `false` if timed out |

---

## Adaptive ping intervals

Adaptive mode is **automatically enabled** whenever a CIDR subnet is in the
host list. It can also be enabled explicitly with `-adaptive`.

| Host history | Ping interval |
|---|---|
| Never seen online | 10 s |
| Has been online at least once | 1 s |

When a never-seen host responds, it immediately switches to 1 s. With
adaptive mode active, the global timeout threshold is raised to 12 s
(because a 10 s interval + slow first reply can otherwise cross the 2 s
threshold and produce false offline flapping).

---

## Modes

| Mode | Trigger | Behavior |
|---|---|---|
| **TUI** (default) | no flags | Interactive bubbletea interface with web mirror. |
| **TUI + quiet** | `-q -tui` | TUI runs headless; useful with `-log` to log transitions without rendering. |
| **Pure webserver** | `-webserver` | No TUI; only the HTTP status server. Press `Ctrl+C` to stop. |
| **Legacy display** | `-notui` | `pterm`-based scrolling list, updates every 100 ms. |
| **Once** | `-once` | One ping per target, then exit. Machine-readable JSON output via `-log`. Suitable for shell scripts and Ansible inventories. |

### Once mode example

```bash
mping -once 192.168.1.0/24
# 254 hosts probed in parallel with a 1 s timeout
```

Combine with `-only-online` / `-only-offline` to filter the output (and the
`-log` JSON).

---

## Building & releasing

### Local build

```bash
go build -o mping
```

### Cross-compile

```bash
env CGO_ENABLED=0 GOOS=linux  GOARCH=amd64 go build -mod vendor -o dist/mping-linux-amd64
env CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -mod vendor -o dist/mping-windows-amd64.exe
```

### Full release

```bash
./scripts/release.sh
```

Builds for `linux/amd64` and `windows/amd64`, packages as `.deb` (Linux,
if `dpkg-deb` is available) and an Arch PKGBUILD (built if `makepkg` is
present, otherwise staged for manual build), compresses with `xz`,
and uploads to GitHub Releases via `gh`.

### Bumping the version

```bash
./scripts/bump_version.sh 1.2.3
```

Updates `main.go`, `scripts/release.sh`, and `windows/mping.iss` in one
atomic change.

See [`docs/BUILD_AND_RELEASE.md`](./docs/BUILD_AND_RELEASE.md) for the CI
workflows (`.github/workflows/release.yml`,
`.github/workflows/windows-installer.yml`) and the Inno-Setup installer
template (`windows/mping.iss`).

---

## Troubleshooting

| Symptom | Likely cause | Fix |
|---|---|---|
| All hosts show "permission denied" / offline on Linux | Unprivileged ICMP denied by kernel | `sudo sysctl -w net.ipv4.ping_group_range="0 2147483647"` or pass `-s` |
| Pure-Go ICMP fails with EPERM | Same as above | `mping` auto-falls back to `-s`. To force pure Go: `sudo setcap cap_net_raw=+ep mping` and run with `-privileged`. |
| Startup on /24 takes > 10 s | Reverse DNS lookups time out (no PTR records) | Add `-no-dns` |
| TUI hangs / first render is blank | Should not happen — guarded by `updateStatsCache`. If it does, run with `-debug` and report. |
| Web UI shows different columns than TUI | View state is shared — close/reopen the TUI after editing the config to sync. |
| Webserver port already in use | Another process on `:8080` | `mping -web-port 9090` |
| `mping -update` fails with "won't perform self update" | Binary is not writable by current user | Re-run from a directory you own, or `sudo mping -update`. |

---

## Auto-fallback

On startup, `mping` runs a single probe to `127.0.0.1` with the pure-Go
pinger. If the error message contains `permission` or `operation not
permitted`, it prints a one-line notice to stderr and switches to
`-s` (system `ping`) for the whole session. This avoids the
"everything looks offline on a fresh laptop" footgun.

Disable the auto-fallback by passing `-privileged` together with the
appropriate capability / root, or `-s` to commit to system ping explicitly.

---

## Acknowledgements

- [`prometheus-community/pro-bing`](https://github.com/prometheus-community/pro-bing) — pure-Go ICMP
- [`tevino/tcp-shaker`](https://github.com/tevino/tcp-shaker) — TCP SYN/ACK probing
- [`charmbracelet/bubbletea`](https://github.com/charmbracelet/bubbletea) — TUI framework
- [`charmbracelet/lipgloss`](https://github.com/charmbracelet/lipgloss) — terminal styling
- [`pterm/pterm`](https://github.com/pterm/pterm) — legacy display
- [`minio/selfupdate`](https://github.com/minio/selfupdate) — self-update mechanism
- [`valyala/fastjson`](https://github.com/valyala/fastjson) — GitHub API parsing
- [`ulikunitz/xz`](https://github.com/ulikunitz/xz) — release compression
- [`rivo/tview`](https://github.com/rivo/tview) + [`sedwards2009/smidgen`](https://github.com/sedwards2009/smidgen) — embedded config editor
- Original inspiration: [`babs/multiping`](https://github.com/babs/multiping)

---

## License

[MIT](./LICENSE) © oliverbenduhn