# Operations

Setup, capabilities, deployment, logging. Everything you need to run `mping` in production.

## Privileges

`mping` probes targets via raw ICMP sockets by default. Raw sockets require `CAP_NET_RAW` (Linux) or root. Three options, in order of preference:

### 1. Unprivileged ICMP (recommended)

Linux allows unprivileged ICMP if the user is in the `ping_group_range`:

```bash
sudo sysctl -w net.ipv4.ping_group_range="0 2147483647"
sudo setcap cap_net_raw=+ep /usr/bin/mping
```

This survives reboots if you persist the sysctl in `/etc/sysctl.d/`.

### 2. Capability only (no sysctl change)

```bash
sudo setcap cap_net_raw=+ep /usr/bin/mping
```

Works if your user is already in the ping group. Check with `cat /proc/sys/net/ipv4/ping_group_range`.

### 3. Fallback to system `ping`

If neither works, `mping` automatically falls back to spawning the OS `ping` binary (pass `-s` to force this from the start). Requires the same privilege as `ping` itself — usually setuid root or the sysctl above.

## Firewall considerations

`mping` sends **outbound** ICMP echo requests and receives the replies. No inbound ports need to be opened. For TCP probing, the target's port must be reachable — that's the test.

If `mping` runs in a container with restricted capabilities, you'll see `permission denied for raw ICMP`. Use `--cap-add=NET_RAW` (Docker) or equivalent.

## Performance Tuning

### Large subnet scans

Scanning `/16` or larger subnets? Three knobs to tune:

```bash
mping -no-dns 192.168.0.0/16         # skip reverse DNS, saves ~10s startup
mping -adaptive 192.168.0.0/16       # idle hosts ping every 10s, not 1s
```

`-adaptive` is auto-enabled for CIDR arguments. The default rate (1s) is correct for small lists; for hundreds of hosts the CPU and network savings are substantial.

### Update rate

In the TUI, `r` cycles the update rate: 100ms → 1s → 5s → 30s. Lower rates = more CPU but snappier UI. 100ms is fine for ≤50 hosts.

## Logging

### Transition log

```bash
mping -log transitions.jsonl google.com 8.8.8.8
```

Append-only JSONL. Each line is a transition event:

```json
{"Timestamp":"2026-01-15T12:00:00Z","UnixNano":1736942400000000000,"Host":"google.com","Ip":"142.250.74.206","Transition":"up to down","State":false}
```

Useful for alerting: pipe through `jq` to filter for `State: false` transitions.

### Self-update

The binary can update itself from GitHub releases:

```bash
mping -update                    # updates to latest stable
mping -update -prerelease        # includes RCs/betas
```

Implemented in `selfupdate.go`. Uses `minio/selfupdate`. The update replaces the running binary in-place.

## Deployment

### .deb package

Built by `./scripts/release.sh` for `linux/amd64`:

```bash
./scripts/release.sh
sudo dpkg -i dist/mping_<version>_amd64.deb
```

The package installs only the binary at `/usr/bin/mping`. No service file, no init script — `mping` is meant to be run interactively or supervised manually.

For unattended monitoring with logging, use:

```bash
mping -q -log /var/log/mping/transitions.jsonl 192.168.1.0/24
```

Wrap in systemd or supervisord if you want restart-on-fail.

### Arch Linux

`scripts/release.sh` stages an Arch `PKGBUILD` at `dist/arch/`. Build locally:

```bash
cd dist/arch
makepkg -si
```

### Cross-compile from source

```bash
go mod vendor
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -mod vendor -o mping-linux-arm64
```

`CGO_ENABLED=0` is required because `pro-bing` and `tcp-shaker` are pure-Go but the build system still wants it set explicitly for cross-compile.

### Static binary

`CGO_ENABLED=0` produces a static binary that runs in `scratch` containers:

```dockerfile
FROM scratch
COPY mping /mping
ENTRYPOINT ["/mping"]
```

Add `--cap-add=NET_RAW` if running in Kubernetes or Docker with restricted caps.

## GitHub Actions (release pipeline)

`.github/workflows/release.yml` builds on every `v*` tag push:

1. `actions/checkout@v7`, `actions/setup-go@v6` (Go 1.24.10)
2. `./release.sh` — produces dist artifacts
3. `marvinpinto/action-automatic-releases` uploads `dist/*`

Cross-build targets: `linux/amd64`, `windows/amd64`. Output artifacts: `.deb` (linux/amd64 only), `.xz`-compressed binaries, Arch PKGBUILD directory.

Bumping version:

```bash
./scripts/bump_version.sh 1.2.0    # updates main.go
git tag v1.2.0 && git push --tags  # triggers release
```

## Configuration

`mping` reads optional YAML from `~/.config/mping/config.yaml`. Managed in-TUI via the `e` key. Override path with `MPING_CONFIG` env var. Override the editor with `MPING_EDITOR`.

The config holds the host list and view settings (filter, sort, hidden hosts, columns). The CLI flags `-only-online` / `-only-offline` take precedence on first launch.

## Troubleshooting

| Symptom | Likely cause |
|---|---|
| `permission denied for raw ICMP` | Need `setcap` or `ping_group_range` sysctl. Falls back to system ping automatically. |
| Slow startup on subnets | Reverse DNS timing out. Use `-no-dns`. |
| "No hosts match the current filter" on startup | First tick hasn't fired yet — fixed in latest version; ensure pre-warm is active. |
| Traceroute returns 429 | 4 concurrent traceroutes already running. Wait and retry. |
| `setcap` doesn't survive `dpkg -i` | Debian policy strips capabilities during install. Re-apply `setcap` after install. |
| HTTP server returns empty | TUI not running with `-webserver`. Or the TUI exited — server lifecycle is tied to the TUI. |