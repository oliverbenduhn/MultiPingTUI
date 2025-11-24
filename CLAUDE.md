# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

`MultiPingTUI` is an enhanced TUI (Terminal User Interface) version of multiping written in Go. It monitors multiple network targets simultaneously using various probing methods (ICMP ping, TCP probing, or system ping). It provides an interactive Midnight Commander/Claude Code-style interface with keyboard navigation, live filtering, sorting, and detailed host statistics, along with optional JSON logging of state transitions.

## Build and Development Commands

### Building

```bash
# Standard build
go build -o mping

# Build with release script (cross-platform, includes version info)
./release.sh

# Build for specific platform (note: requires go mod vendor first)
go mod vendor
env CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -mod vendor
```

### Running

```bash
# TUI Mode (default) - Interactive interface
go run . localhost google.com 8.8.8.8

# With TCP probing
go run . tcp://google.com:443 tcp://[::1]:22

# With CIDR expansion (automatically expands subnets)
go run . 192.168.1.0/24

# Legacy display mode (non-interactive, pterm-based)
go run . -tui=false localhost google.com

# System ping mode
go run . -s localhost

# Quiet mode with logging (no display)
go run . -q -log transitions.json google.com

# Once mode (ping once and exit) - useful for scripting
go run . -once 192.168.1.0/24

# Legacy mode with filters: show only online hosts
go run . -tui=false -only-online 192.168.1.0/24

# Legacy mode with filters: show only offline hosts
go run . -tui=false -only-offline 192.168.1.0/24

# Once mode with filters (e.g., find all online hosts in subnet)
go run . -once -only-online 192.168.1.0/24

# TUI start with initial online-only filter
go run . -only-online localhost google.com
```

### TUI Keyboard Shortcuts

When running in TUI mode (default):
- `↑/↓` or `j/k` - Navigate through hosts
- `Enter` - Toggle detailed view for selected host
- `a` - Filter: show all hosts
- `o` - Filter: show only online hosts
- `f` - Filter: show only offline hosts
- `n` - Sort by name
- `s` - Sort by status
- `r` - Sort by RTT
- `Esc` - Back from detail view
- `q` or `Ctrl+C` - Quit

### Testing

No test files exist in the repository currently.

## Architecture

### Core Components

1. **main.go**: Entry point, CLI flag parsing, signal handling, and main loop orchestration
   - Handles CIDR expansion for subnet scanning (delegates to `ExpandCIDR()` in subnet.go)
   - Supports two modes: continuous monitoring (default) and once mode (`-once` flag)
   - Manages quiet vs live display modes
   - Coordinates between WrapperHolder and Display
   - Passes filter flags (`-only-online`, `-only-offline`) to Display and TUI for filtered output/initial view

2. **Ping Wrapper System** (Strategy Pattern)
   - `PingWrapperInterface`: Common interface for all ping implementations
   - `NewPingWrapper()`: Factory function that selects implementation based on host string pattern and options
   - Three implementations:
     - `ProbingWrapper` (pinger_probing.go): Pure Go ICMP using pro-bing library
     - `SystemPingWrapper` (pinger_system.go): Spawns OS's ping command as subprocess
     - `TCPPingWrapper` (pinger_tcp.go): TCP port probing using tcp-shaker

3. **State Management**
   - `PWStats` (pwstats.go): Tracks ping statistics and computes state transitions
   - `WrapperHolder` (wrapperholder.go): Manages collection of ping wrappers
   - `TransitionWriter` (transitionwriter.go): Thread-safe buffered JSON logger for state changes

4. **Display Layer (Two Modes)**
   - **TUI Mode** (tui.go): Interactive bubbletea-based TUI (default)
     - Full keyboard navigation with arrow keys and vim-style keys
     - Live filtering (all/online/offline) with `a`/`o`/`f` keys
     - Sorting by name, status, or RTT with `n`/`s`/`r` keys
     - Detail view with `Enter` key showing comprehensive host statistics
     - Styled with lipgloss for a modern terminal look
   - **Legacy Display Mode** (display.go): Non-interactive pterm-based display
     - Real-time updates with color-coded status (✅/❌)
     - Shows RTT, last loss information, and error messages
     - Supports filtering: `SetFilter()` allows showing only online or offline hosts
     - Enabled with `-tui=false` flag

5. **Subnet Expansion & Once Mode** (subnet.go)
   - `ExpandCIDR()`: Parses CIDR notation and expands to individual IP addresses
   - Removes network and broadcast addresses from results
   - `RunPingOnce()`: Concurrent one-time ping of multiple hosts
   - Uses semaphore pattern (limit 100 concurrent) to avoid file descriptor exhaustion
   - Supports filtering in once mode (`-only-online`, `-only-offline`)

### Host String Parsing & CIDR Expansion

**CIDR Expansion** (in main.go, before wrapper creation):
- Arguments are first checked if they match CIDR notation (e.g., `192.168.1.0/24`)
- If CIDR: `ExpandCIDR()` expands to individual IPs, excluding network/broadcast
- If not CIDR: treated as single host string

**Host String Parsing** (in `pingwrapper.go:18`):
- `ip://hostname` or bare hostname → ICMP ping
- `tcp://hostname:port` → TCP probing
- IPv4/IPv6 hints: `ip4://`, `ip6://`, `tcp4://`, `tcp6://`
- IPv6 addresses must use bracket notation: `tcp://[::1]:22`

### Concurrency Model

**Continuous Monitoring Mode:**
- Each ping wrapper runs in its own goroutine
- System ping wrappers read stdout line-by-line in separate goroutines
- TCP ping spawns a checker goroutine every second
- TransitionWriter has a flush goroutine running every 500ms
- Main loop updates display every 100ms

**Once Mode** (`-once` flag):
- `RunPingOnce()` uses worker pool pattern with WaitGroup
- Semaphore limits concurrent pingers to 100 (prevents FD exhaustion)
- Each host gets its own goroutine for parallel execution
- Results collected via buffered channel
- Timeout: 1 second per ping, count: 1

### Build System

- Uses Go modules with vendored dependencies (`go.mod`, vendor/)
- Version info injected via ldflags in `release.sh`
- Cross-compiles for: linux, openbsd, freebsd, windows, darwin across amd64/arm/arm64/386
- Outputs compressed (.xz) binaries for distribution
- Self-update functionality via GitHub releases (selfupdate.go)

## Key Dependencies

- `github.com/charmbracelet/bubbletea`: TUI framework (Elm architecture)
- `github.com/charmbracelet/lipgloss`: Terminal styling
- `github.com/charmbracelet/bubbles`: TUI components (key bindings)
- `github.com/prometheus-community/pro-bing`: Pure Go ICMP implementation
- `github.com/tevino/tcp-shaker`: TCP SYN/ACK probing (non-Windows)
- `github.com/pterm/pterm`: Terminal UI library (legacy mode)
- `github.com/valyala/fastjson`: JSON parsing for GitHub API
- `github.com/minio/selfupdate`: Self-update mechanism

## Platform-Specific Notes

### Linux Privileges
- Unprivileged mode requires: `sysctl -w net.ipv4.ping_group_range="0 2147483647"`
- Privileged mode requires CAP_NET_RAW: `setcap cap_net_raw=+ep mping`

### TCP Probing
- Linux/FreeBSD/OpenBSD: Uses SYN/ACK probing (tcp-shaker)
- Darwin/Windows: Full TCP handshake (pinger_tcp_win.go)

### IPv6
- System ping mode attempts `ping6` first, falls back to `ping`

## Development Patterns

### Adding a New Ping Implementation

1. Create new type implementing `PingWrapperInterface` in new file
2. Add factory logic to `NewPingWrapper()` in pingwrapper.go
3. Implement Start(), Stop(), Host(), and CalcStats() methods
4. Ensure PWStats state is updated correctly

### State Transition Logic

State transitions are computed in `PWStats.ComputeState()`:
- Uses timeout threshold (default 2s) to determine up/down
- Tracks `last_loss_nano` and `last_loss_duration` for display
- Writes JSON transition logs when state changes
- Format: `{"Timestamp":"...","UnixNano":123,"Host":"...","Ip":"...","Transition":"up to down","State":false}`

### Display Filtering

The Display layer supports filtering visible hosts:
- Filter is set via `SetFilter(onlyOnline, onlyOffline bool)`
- Filter logic in `Update()` checks: `isOnline := stats.state && stats.error_message == ""`
- If `onlyOnline=true`: skip hosts that are offline or have errors
- If `onlyOffline=true`: skip hosts that are online
- Useful for monitoring large subnets and focusing on specific states

## Development Patterns (TUI-Specific)

### TUI Architecture (tui.go)

The TUI follows the Elm/Bubbletea architecture:

1. **Model** (`TUIModel`): Holds all application state
   - Current cursor position
   - Filter mode (All/Online/Offline)
   - Sort mode (Name/Status/RTT)
   - Detail view toggle
   - Reference to `WrapperHolder` for ping data

2. **Update** (`Update(msg tea.Msg)`): Handles all messages
   - `tea.KeyMsg`: Keyboard input
   - `tickMsg`: 100ms tick for updating stats
   - `tea.WindowSizeMsg`: Terminal resize
   - Returns updated model and commands

3. **View** (`View()`): Renders the current state
   - Title with version
   - Header with filter/sort info
   - List view or detail view (based on `showDetails`)
   - Help text at bottom

### Adding New TUI Features

To add a new keyboard shortcut:
1. Add key binding to `keyMap` struct in tui.go
2. Handle it in the `Update()` function's switch statement
3. Update `View()` help text
4. Update README.md keyboard shortcuts section

To add a new filter or sort mode:
1. Add enum value to `FilterMode` or `SortMode`
2. Implement logic in `getFilteredWrappers()`
3. Add key binding and update handler
4. Update display strings in `getFilterModeString()` or `getSortModeString()`

## File Structure

```
MultiPingTUI/
├── main.go                    # Entry point, CLI, mode selection
├── tui.go                     # Interactive TUI (bubbletea)
├── display.go                 # Legacy terminal UI (pterm)
├── pingwrapper.go             # Factory and interface
├── pinger_probing.go          # Pure Go ICMP
├── pinger_system.go           # System ping subprocess
├── pinger_tcp.go              # TCP probing (non-Windows)
├── pinger_tcp_win.go          # TCP probing (Windows)
├── pwstats.go                 # Statistics and state tracking
├── wrapperholder.go           # Collection manager
├── transitionwriter.go        # JSON logger
├── subnet.go                  # CIDR expansion and once mode
├── selfupdate.go              # GitHub release updater
├── release.sh                 # Cross-platform build script
├── go.mod / go.sum           # Dependencies
└── vendor/                    # Vendored dependencies
```
