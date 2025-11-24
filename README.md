# MultiPingTUI

[![GitHub](https://img.shields.io/github/license/oliverbenduhn/MultiPingTUI)](https://github.com/oliverbenduhn/MultiPingTUI/blob/master/LICENSE)

`MultiPingTUI` is an enhanced TUI (Terminal User Interface) version of multiping with interactive navigation, filtering, and detailed host statistics. It monitors multiple network targets simultaneously using pings or TCP probing with optional logging of state transitions. The CLI command/binary is `mping`.

**Key Features:**
- 🎨 **Interactive TUI** - Midnight Commander/Claude Code inspired interface
- ⌨️  **Keyboard Navigation** - Arrow keys, vim-style (j/k), and shortcuts
- 🔍 **Live Filtering** - Filter by online/offline status on the fly
- 📊 **Detailed View** - Press Enter for detailed statistics per host
- 🔀 **Sorting** - Sort by name, status, or RTT
- 🌐 **CIDR Support** - Scan entire subnets (192.168.1.0/24)
- 📝 **Transition Logging** - JSON log of all state changes

## Demo

**TUI Mode (default):**
```bash
mping localhost google.com 8.8.8.8
```

**Keyboard Shortcuts:**
- `↑/↓` or `j/k` - Navigate through hosts
- `Enter` - Show detailed view for selected host
- `a` - Show all hosts
- `o` - Filter: show only online hosts
- `f` - Filter: show only offline hosts
- `n` - Sort by name
- `s` - Sort by status
- `r` - Sort by RTT
- `Esc` - Back from detail view
- `q` or `Ctrl+C` - Quit

**Subnet Scanning:**
```bash
mping 192.168.1.0/24
```

**Legacy Display Mode:**
```bash
# Use -tui=false to disable TUI mode
mping -tui=false localhost google.com
```

## Documentation

See `mping -h` for detailed information.

### Modes

**TUI Mode (Default)**
Interactive terminal UI with keyboard navigation, filtering, and detailed host views. This is the default mode and provides the best user experience.

**Legacy Display Mode** (`-tui=false`)
Simple non-interactive display mode compatible with the original multiping. Updates every 100ms.

**Once Mode** (`-once`)
Ping each target once and exit. Useful for scripting.

**Quiet Mode** (`-q`)
Disables all display output. Useful with `-log` for background monitoring.

### Probing Methods

Available probing means are:
- pure go ping (pro-bing, default)
- OS's ping command, via background process (`-s`)
- tcp (partial (S/SA/R tcp-shaker) or full handshake depending on the OS)

### ping

Pure Go is the default option but for unprivileged users ([see linux notes](#linux-notes-on-pure-go-ping)), OS/system's ping command (usually available on OS with specific cap or setuid) can be used with a background spawn model with `-s` flag. Privileged mode (default when user is root or on windows) can be forcefully enabled with `-privileged`.

On pure Go implementation, ICMP packet size can be specified using `-size` option, note that do-not-fragment bit is set only for linux platform (kind of defeat the purpose of `-size` on other platforms :/). Given size doesn't account for the 28 bytes header (note for usual limits: 1472 or 8972). This has no effect on system's ping, refer to system's manual and use `-ping-options`.

Hint can be given about address family resolution using `ip<family>://`, `ip://` is the default, `ip4://` to force IPv4 and `ip6://` to force IPv6, example:
 - `google.com` is equivalent to `ip://google.com`
 - `ip4://google.com` forces resolution of google.com as ipv4
 - `ip6://google.com` forces resolution of google.com as ipv6

### TCP probing

For tcp probing, on linux, freebsd and openbsd, S/SA/R pattern is used. This allows to probe tcp ports without really triggering an accept on the listening app. Issue is if a device in between perform syn proxying, the result might not reflect reality.
On darwin and windows due to limitations, complete handshake is performed.

tcp probing example syntax:
- `tcp://google.com:80`
- `tcp://192.168.0.1:443`
- `tcp://[::1]:22`

As for `ip://`, `tcp://` can also have hint of address family:
- `tcp4://google.com:80` forces resolution of google.com as ipv4
- `tcp6://google.com:80` forces resolution of google.com as ipv6

### Transition logging

Transition logging can be enabled using `-log filename`.
Log format is pretty self explanatory:

* Timestamp (string): timestamp
* UnixNano (int64): timestamp in nano seconds
* Host (string): the host provided as arg (inc. proto)
* Ip (string): the resolved host
* State (bool): true if alive, false if timeout
* Transition (string): "down to up" or "up to down"

### CIDR subnet scanning

`mping` automatically detects and expands CIDR notation (e.g., `192.168.1.0/24`) to ping all hosts in the subnet (excluding network and broadcast addresses).

Example:
```bash
mping 192.168.1.0/24
```

Use filtering (`o` key) in TUI mode to quickly see which hosts are online.

### Once mode

Use `-once` to ping each target once and exit, useful for scripting:

```bash
mping -once 192.168.1.0/24
```

### Display filtering

Filter the display to show only specific host states:
- `-only-online`: Show only hosts that are reachable
- `-only-offline`: Show only hosts that are unreachable

These work in both continuous and once mode:
```bash
# Find all online hosts in subnet
mping -tui=false -once -only-online 192.168.1.0/24

# Monitor only offline hosts continuously
mping -tui=false -only-offline 192.168.1.1 192.168.1.2 192.168.1.3
```

In TUI mode these flags set the initial filter when the UI opens; you can still toggle filters dynamically with `a`/`o`/`f`.

## Linux notes on pure go ping

If run unprivileged, you might need to allow groups to perform "unprivileged" ping via UDP with the following sysctl:
```bash
sysctl -w net.ipv4.ping_group_range="0 2147483647"
```

You can also add net raw cap to the binary to use it with `-privileged` mode
```bash
setcap cap_net_raw=+ep /path/to/your/compiled/binary
```

## Source

Github repository: https://github.com/oliverbenduhn/MultiPingTUI

Based on: https://github.com/babs/multiping

### libs used

* https://github.com/charmbracelet/bubbletea - TUI framework
* https://github.com/charmbracelet/lipgloss - Terminal styling
* https://github.com/pterm/pterm - Terminal UI (legacy mode)
* https://github.com/prometheus-community/pro-bing - Pure Go ping
* https://github.com/tevino/tcp-shaker - TCP probing
* https://github.com/valyala/fastjson - JSON parsing
* https://github.com/minio/selfupdate - Self-update mechanism
* https://github.com/ulikunitz/xz - Compression

## Building

```bash
# Standard build
go build -o mping

# Build with release script (cross-platform, includes version info)
./release.sh
```

## License

See [LICENSE](LICENSE) file.
