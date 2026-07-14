# Architecture

Module map, data flow, and key state machines. Implementation detail belongs in source comments — this doc is for orientation.

## Module Overview

```mermaid
graph TB
    subgraph Entry
        Main[main.go]
        Config[config.go<br/>CLI flags]
        UserSettings[user_settings.go<br/>YAML persistence]
    end

    subgraph Core
        PingService[ping_service.go<br/>lifecycle, ReplaceHosts]
        Repository[repository.go<br/>MemoryHostRepository]
        PWStats[pwstats.go<br/>ComputeState, transitions]
    end

    subgraph Probes
        Factory[pingwrapper.go<br/>factory: scheme → impl]
        Probing[pinger_probing.go<br/>pro-bing ICMP]
        System[pinger_system.go<br/>ping subprocess]
        TCP[pinger_tcp.go<br/>tcp-shaker]
        Error[pinger_error.go<br/>validation failures]
    end

    subgraph Display
        TUI[tui.go + tui_list.go<br/>bubbletea model]
        Legacy[display.go<br/>pterm non-interactive]
        StatusServer[status_server.go<br/>HTTP sidecar]
    end

    subgraph I/O
        TransitionWriter[transitionwriter.go<br/>JSONL log]
        DNSUpdater[dns_updater.go<br/>reverse-DNS cache]
        HostDisplay[host_display.go<br/>hostname resolution]
    end

    Main --> Config
    Main --> PingService
    Main --> TUI
    Main --> Legacy
    Main --> TransitionWriter

    PingService --> Repository
    PingService --> Factory
    PingService --> DNSUpdater
    Factory --> Probing
    Factory --> System
    Factory --> TCP
    Factory --> Error
    Probing --> PWStats
    System --> PWStats
    TCP --> PWStats

    TUI --> Repository
    TUI --> StatusServer
    StatusServer --> Repository
    Legacy --> Repository
    Repository --> PWStats

    TransitionWriter -.->|writes JSONL| Disk[(disk)]
    DNSUpdater --> HostDisplay
    UserSettings -.->|reads YAML| ConfigHome[~/.config/mping/]
```

## Three Ping Backends (Strategy Pattern)

`PingWrapperInterface` is the contract. `NewPingWrapper()` selects the implementation by URL scheme.

```mermaid
graph LR
    Input["host string"] --> Factory{NewPingWrapper}
    Factory -->|tcp://, tcp4://, tcp6://| TCP[TCPPingWrapper<br/>SYN/ACK probe]
    Factory -->|system ping flag| System[SystemPingWrapper<br/>subprocess]
    Factory -->|default| Probing[ProbingWrapper<br/>pro-bing ICMP]
    Factory -.->|parse error| Error[ErrorWrapper<br/>wraps error]

    TCP --> Stats[PWStats]
    System --> Stats
    Probing --> Stats
    Error --> Stats
```

Add a new backend by implementing `PingWrapperInterface` and adding a branch to the factory. Do not add interfaces — the existing one already has three implementations.

## TUI Update Loop

The TUI is an Elm/Bubbletea model. State flows one direction: `Msg → Update → Model → View`.

```mermaid
sequenceDiagram
    participant User
    participant Program as tea.Program
    participant Model as TUIModel
    participant Repo as HostRepository
    participant Wrapper as PingWrapper

    User->>Program: key / tick (100ms) / window resize
    Program->>Model: Update(msg)

    alt KeyMsg
        Model->>Model: switch key → mutate hostList/header/viewMode
    else tickMsg
        Model->>Model: if elapsed >= updateRate<br/>updateStatsCache()
    else WindowSizeMsg
        Model->>Model: store width/height
    end

    Model-->>Program: (model, cmd)

    Program->>Model: View()
    Model->>Repo: GetAll()
    Model->>Model: getFilteredWrappers(<br/>cached stats, filter, sort)
    Model->>Model: render list / detail / dashboard
    Model-->>Program: styled string
    Program->>User: render to terminal

    Note over Wrapper: probes run on their own goroutines,<br/>independent of the TUI event loop
```

The probes run independently — they update `PWStats` (mutex-protected) and the TUI just reads the cache. There is no callback from probes into the TUI; the tick is the only sync point.

## PWStats State Machine

`ComputeState(timeout_ns)` is called once per ping result. It tracks three booleans and two timestamps.

```mermaid
stateDiagram-v2
    [*] --> Uninitialized: stats.state_initialized=false

    Uninitialized --> Online: receive within timeout<br/>has_ever_received=true<br/>has_ever_been_online=true
    Uninitialized --> Offline: timeout exceeded<br/>has_ever_received stays false

    Online --> Online: receive within timeout
    Online --> Offline: timeout exceeded<br/>last_loss_nano = now<br/>last_loss_duration computed<br/>transition up→down logged

    Offline --> Offline: timeout exceeded
    Offline --> Online: receive<br/>transition down→up logged<br/>highlight for one cycle

    note right of Offline
        last_loss_nano marks RECOVERY time,
        not the loss start. Compute the
        outage start as last_loss_nano - last_loss_duration.
    end note
```

The "highlight for one cycle" on down→up is implemented via `skip_next_up_highlight` to suppress double-firing when state flips rapidly.

## ViewState Propagation

The TUI and the HTTP server share the same source data. The HTTP server gets it via `StatsProvider(PingWrapperInterface) PWStats` — a function injected at startup. There is no shared mutable state between TUI and HTTP server; both read the `HostRepository` independently.

```
Wrapper → CalcStats → PWStats (per wrapper, mutex-protected)
                       ↓
            ┌──────────┴──────────┐
            ↓                     ↓
   TUI: statsCache            HTTP: StatsProvider func
   (refreshed per tick)       (called per request)
```

This means the HTTP view is always one tick behind the TUI at most. That's intentional — avoids blocking the TUI on a slow client.

## Repository Contract

`HostRepository` has exactly two methods: `GetAll()` returns a copy, `UpdateAll([]PingWrapperInterface)` replaces. The copy semantics in `GetAll()` are load-bearing — readers never see torn writes during `UpdateAll`. Do not add methods that return the underlying slice.

## DNS Resolution

`DNSUpdater` runs a background goroutine that periodically refreshes reverse-DNS for all hosts. It uses `hostDisplayName()` with a 500ms per-lookup timeout. `-no-dns` disables it entirely.

The TUI does not block on DNS. Hostnames may show as the IP for the first few hundred ms after startup; they fill in asynchronously. This is by design — sequential DNS would block large-subnet startup for minutes.

## Adaptive Ping Intervals

For subnets, hosts that have never been online are pinged every 10 seconds. Once a host replies, the interval drops to 1 second. The switch is driven by `PWStats.has_ever_been_online` and applied via `pinger.Interval` (pro-bing) or a ticker reset (TCP).

Auto-enabled when CIDR notation is detected in arguments. `-adaptive` forces it on.

## Concurrency Invariants

These must hold. Violations will manifest as data races (caught by `-race`) or stale UI.

- `MemoryHostRepository.mu` protects `wrappers` slice.
- `PWStats.mu` protects all PWStats fields.
- `TransitionWriter.mu` protects the buffered channel.
- `TUIModel.statsCacheMu` protects `statsCache` (separate from `repo.mu`).
- `StatusServer.viewMu` protects `ServerView`.
- `StatusServer.traceSem` is a counting semaphore (4 slots) for concurrent traceroutes.

No other shared state. New shared state needs a mutex named for the field it protects.