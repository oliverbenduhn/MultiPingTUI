# Troubleshooting

This document collects common failure modes and their fixes. It assumes
you have read [`README.md`](../README.md) and (if you're hacking on the
codebase) [`AGENTS.md`](../AGENTS.md).

---

## 1. Everything reports offline / "permission denied"

### Symptoms

- Every host in the list shows `❌ never had reply` or
  `❌ last reply … ago`.
- The wrapper has `error_message = "permission denied"` (visible in the
  detail view).

### Diagnosis

On Linux, raw ICMP requires either:

- root, or
- `CAP_NET_RAW` on the binary, or
- the `net.ipv4.ping_group_range` sysctl covers your UID.

```bash
sysctl net.ipv4.ping_group_range
# expect: net.ipv4.ping_group_range = 0 2147483647
# broken: net.ipv4.ping_group_range = 1 0
```

### Fix A — unprivileged ICMP (recommended)

```bash
sudo sysctl -w net.ipv4.ping_group_range="0 2147483647"
# To make it persistent:
echo 'net.ipv4.ping_group_range = 0 2147483647' | sudo tee /etc/sysctl.d/99-ping.conf
sudo sysctl --system
```

### Fix B — privileged ICMP

```bash
sudo setcap cap_net_raw=+ep /path/to/mping
./mping -privileged localhost
```

### Fix C — let `mping` fall back to system `ping`

`mping` detects this on startup and falls back automatically; you will
see:

```text
permission denied for raw ICMP; falling back to system ping (-s). Use -privileged or setcap to enable pure Go ping.
```

You can pin this behaviour by passing `-s` (or `-ping-options "..."`)
explicitly.

### Notes

- `mping` does **not** fall back if `-privileged` is passed — the
  caller is asking for pure Go and accepting the failure.
- Windows: the binary is always privileged by default (admin or not),
  so this issue does not apply.

---

## 2. Slow startup on a /24 (10–30 seconds)

### Symptoms

- `mping 192.168.1.0/24` takes a long time before the TUI appears.
- `-debug` output shows PTR lookups timing out.

### Cause

Reverse DNS for each IP. With 254 hosts and 500 ms per timeout, the
worst-case wait is **127 seconds**. Most PTR records on RFC 1918 ranges
do not exist, so lookups time out.

### Fix

- Add `-no-dns` to skip reverse DNS entirely.
- If you want DNS but faster startup, lower the DNS server's timeout
  for `in-addr.arpa`.
- Alternatively, switch to `-no-dns` and use the TUI's "Smart" filter
  to hide unreachable hosts.

```bash
mping -no-dns 192.168.1.0/24
```

### Why we don't lower the timeout

500 ms is a balance between responsiveness and reliability. Lowering
it further would cause false negatives for legitimate slow resolvers.

---

## 3. TUI hangs on first render / first interaction is laggy

### Symptoms

- The TUI appears but does not respond to keys for 1–2 seconds.
- The first `View()` is blank.

### Cause (historical)

The regression documented in `DIFF_ANALYSIS.md`: an empty
`statsCache` causes `CalcStats()` to be called for every wrapper during
the first render. On a /24 with subnet grouping, that's several
hundred `CalcStats` + sort operations on the render path.

### Current state

`TUIModel.getCachedStats` returns an **empty `PWStats`** on a cache
miss instead of calling `CalcStats`. The cache is filled by
`updateStatsCache()` on every tick. This regression is locked in by:

- `TestTUITickUpdatesStatsCacheOnce` (one CalcStats per tick per host)
- `TestStopBeforeStartDoesNotPanic`
- The contract documented in [`AGENTS.md`](../AGENTS.md) §2.2.4

### If you reproduce it

1. Run with `-debug` and check for "DEBUG: Starting wrapper".
2. Verify `updateStatsCache()` is being called (add a debug log line
   temporarily).
3. Verify the TUI tick interval: `100 ms * UpdateRate multiplier`.
4. Check the cache hit path in `getCachedStats`.

---

## 4. Webserver port already in use

### Symptoms

```text
status server error: listen tcp 127.0.0.1:8080: bind: address already in use
```

### Fix

```bash
mping -web-port 9090
```

To disable the webserver entirely:

```bash
mping -web-port 0
```

To find what is on `:8080`:

```bash
ss -ltnp | grep 8080    # or: lsof -iTCP:8080 -sTCP:LISTEN
```

---

## 5. TUI columns overlap / render garbage

### Symptoms

- After resizing the terminal, columns show garbled borders or
  `?` replacement chars.

### Cause

The column auto-shrink logic in `tui_list.go` cannot fit the minimum
width for all six columns. The status bar should show
"No hosts match the current filter" only when the host list is empty;
if columns are mis-rendered, the cause is usually a very narrow window
(< 40 cols).

### Fix

- Widen the terminal to ≥ 80 columns.
- Hide some columns with `1`–`6` keys until it fits.
- `q` and restart if you changed your terminal size during the
  session (the cache invalidates on resize but the visible-column
  logic uses a snapshot).

---

## 6. Self-update fails

### Symptoms

```text
permission denied for raw ICMP ...          ← unrelated to -update
```

or

```text
won't perform self update: ...
```

### Diagnosis

- **`won't perform self update`**: the binary is not writable by the
  current user. Re-run from a directory you own, or with `sudo` for
  `/usr/local/bin/mping`.
- **`net/http: connection refused`**: outbound HTTPS is blocked. Check
  `https://api.github.com` and `https://github.com` from the host.
- **`unable to upgrade: ...`**: the downloaded asset was corrupt. Try
  again; if it persists, download manually from the releases page.

### Workaround

Manually download from
<https://github.com/oliverbenduhn/MultiPingTUI/releases/latest>.

---

## 7. Traceroute not available

### Symptoms

Pressing `t` in the detail view fails with:

```text
traceroute/tracepath not found in PATH
```

### Fix

Install one of:

| OS | Tool |
|---|---|
| Debian/Ubuntu | `apt install traceroute` |
| Fedora/RHEL | `dnf install traceroute` |
| Arch | `pacman -S traceroute` |
| Alpine | `apk add traceroute` |
| Windows | Built-in `tracert.exe` |
| macOS | Built-in (no install needed) |

If only `tracepath` is available, `mping` falls back to it automatically.

---

## 8. ARP storm / packet loss when scanning large subnets

### Symptoms

- Local network slows down while `mping` is starting.
- Switch's MAC table fills up.

### Cause

254 simultaneous ARP requests + ICMP echo requests within milliseconds
saturate small switches and home routers.

### Mitigations already in place

- Startup parallelism is capped at 20 concurrent wrappers
  (`ping_service.go` `startWrappers`).
- A 1 ms pause every 10 hosts (between host 10 and `len-1`) throttles
  the burst.

### If it's still too aggressive

- Use `-adaptive` (auto-enabled for CIDRs). Never-seen hosts are
  pinged every 10 s, drastically reducing the steady-state load.
- Run from a workstation connected to a managed switch that rate-limits
  ARP per port.

---

## 9. TCP probing always reports offline

### Symptoms

- `tcp://example.com:443` shows offline even when `curl` works.

### Diagnosis

- **Wrong port**: `mping` cannot tell you the port is wrong; it just
  reports offline. Try a known-open port like 80 or 443 on a
  well-known host.
- **Middlebox / SYN proxy**: on networks with stateful firewalls, the
  SYN/ACK may come from the firewall itself (linux/freebsd/openbsd
  code path). The TCP probe may report online even when the real
  server is down.
- **Full-handshake mode** (macOS/Windows): some servers rate-limit or
  drop SYN+ACK responses that complete a connection but don't follow
  up. Try with `-privileged` ICMP instead.

### Fix

Switch to `-s` (system `ping`) to rule out a TCP-specific issue, or to
ICMP if the host answers ICMP at all.

---

## 10. `error_message` is set but the host still appears online

### Cause

`PWStats.error_message` is a free-form string set by the wrapper when
its `Start()` fails (e.g. `pro-bing` couldn't bind a socket). The state
machine in `ComputeState` does **not** override `state` based on
`error_message`. The TUI treats the host as offline when
`state == false || error_message != ""`, so the host shows as offline
even if the wrapper somehow managed to record a `lastrecv`.

### Fix

This is a defensive measure: when the wrapper reports an error, treat
the host as offline regardless of recent state. If you see this for a
specific host, it means the wrapper failed to initialize but the TUI
has cached older stats. Restart `mping` (or trigger `ReplaceHosts`
via the config editor) to clear the error.

---

## 11. DNS updater keeps resolving the same name

### Symptoms

- The host display flickers between IP and reverse-DNS name.
- Log shows repeated DNS lookups for the same IP.

### Cause

The DNS cache (`DNSUpdater.dnsCache`) is in-memory and cleared when
`mping` restarts. On the first run, every online host is looked up.
After 1 hour (positive TTL) or 5 minutes (negative TTL), the lookup
repeats.

### Fix

This is expected behaviour. To suppress it:

```bash
mping -no-dns
```

To inspect the cache, run with `-debug`. The debug log shows
`DEBUG DNS: <ip> -> <name>` for each lookup.

---

## 12. `go test ./...` fails on Windows

### Symptoms

```text
FAIL: TestExpandCIDRRejectsHugeNetworks (or similar)
```

### Fix

The tests assume a Unix environment for some shell-style checks.
Run them on Linux/macOS or in CI:

```yaml
# .github/workflows/windows-installer.yml
- run: go test ./...
```

If the failure is reproducible only on Windows and unrelated to file
paths, please open an issue with the test output.

---

## 13. Logs filling the disk (`-log`)

### Symptoms

`transitions.json` grows without bound.

### Fix

Rotate the file with `logrotate` (Linux) or a scheduled task. Example
logrotate config:

```text
/var/log/mping/transitions.json {
    daily
    rotate 7
    compress
    missingok
    notifempty
    copytruncate
}
```

`copytruncate` is required because `mping` holds an open file handle.

---

## 14. The "Smart" filter hides a host I want to see

### Symptom

A host that is offline and has never been online is hidden under the
default filter (`Smart`).

### Fix

- Press `f` until the filter says "All".
- Or pass `-only-online=false -only-offline=false` and pick a filter in
  the TUI.
- Or edit `~/.config/mping/config.yaml` and set `view.filter: 0`.

---

## 15. `mping` is slower than expected

### Profile

```bash
mping -pprof localhost:6060 github.com example.com
# Then:
go tool pprof http://localhost:6060/debug/pprof/profile
```

Common hotspots:

- `pwstats.ComputeState` — many calls per tick per host. Expected.
- `dns_updater.performDNSUpdates` — only every 60 s; if you see it hot,
  you have a flaky DNS server. Try `-no-dns`.
- `status_server.htmlHandler` — large host list with frequent polling.
  Reduce polling or shrink the page.

---

## 16. Reporting a new issue

When opening a GitHub issue, include:

1. `mping --version` (or `mping` first line of output).
2. Output of `mping -debug <repro> 2>&1 | head -200`.
3. OS and arch (`uname -a` on Linux/macOS, `ver` on Windows).
4. `go version` if you built from source.
5. The relevant slice of `-log transitions.json` if transitions are
   involved.
6. If the TUI renders incorrectly: terminal type (`echo $TERM`), width,
   height.

See [`AGENTS.md`](../AGENTS.md) §6 for the quick-decision tree.