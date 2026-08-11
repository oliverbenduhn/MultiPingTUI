# AGENTS.md — Rules for AI Coding Agents Working in This Repository

> This file is the single source of truth for AI agents (Claude Code, Cursor,
> local models, custom agents) operating in this repo. It supersedes any
> general-purpose coding rules. Read it fully before performing any non-trivial
> change.

---

## 1. Repository Snapshot

- **Name**: `MultiPingTUI` (binary: `mping`)
- **Language**: Go 1.24, vendored deps (`go mod vendor`).
- **Domain**: Multi-host network probing (ICMP / system `ping` / TCP) with a
  bubbletea TUI, a local HTTP status server, and a legacy `pterm` display.
- **Layout**: Single-package `main`. No sub-packages. All `.go` files share the
  same package.
- **Distribution**: GitHub Releases (`scripts/release.sh`), `.deb`/Arch
  packages, Windows Inno-Setup installer, self-update via GitHub API.

### Required reading before touching code

| Topic | Files to read in full |
|---|---|
| Build & flags | `main.go`, `config.go` |
| Wrapper contract | `pingwrapper.go`, `error_wrapper.go` |
| Stats state machine | `pwstats.go`, `pwstats_test.go` |
| Lifecycle | `ping_service.go`, `ping_service_test.go`, `repository.go`, `repository_test.go` |
| ICMP impl | `pinger_probing.go` |
| System-ping impl | `pinger_system.go` |
| TCP impl (unix) | `pinger_tcp.go` |
| TCP impl (windows) | `pinger_tcp_win.go` |
| TUI core | `tui.go`, `tui_list.go`, `tui_components.go`, `tui_utils.go` |
| Web server | `status_server.go` |
| DNS / Display / Once | `dns_updater.go`, `host_display.go`, `display.go`, `subnet.go` |
| Config persistence | `user_settings.go`, `config_editor_smidgen.go` |
| Lifecycle regressions | `lifecycle_regression_test.go` |

---

## 2. Hard Rules (Verboten / Pflicht)

### 2.1 Architecture boundaries

1. **Wrappers never reach into `HostRepository`.** Wrappers are pure stateless
   workers; the repository is owned by `PingService`. A wrapper that needs
   "sibling state" is a code smell — request it via the service.
2. **UI must not mutate wrapper state directly.** TUI and HTTP server consume
   snapshots through `CalcStats()` / `Stats()` only. Mutation paths go through
   `SetHostRepr()` or, for periodic DNS, through `DNSUpdater`.
3. **`PingService` is the only owner of `Wrapper.Start()` / `Stop()` lifecycle.**
   Do not call these methods from anywhere else (not from TUI, not from tests
   that don't also call `Service.Stop()`).
4. **Global state is restricted to:**
   - `Version`, `CommitHash`, `BuildTimestamp`, `Builder` (ldflags-injected)
   - `DebugMode`, `SkipDNS` (toggles)
   - `TimeoutThresholdNS` (computed once during startup, read by all callers)
   - `nowFunc` (test seam for `time.Now` in `pwstats.go`)
   No other mutable globals. Anything new must be discussed before adding.

### 2.2 Concurrency invariants

1. **`PWStats` is read-write shared.** Every mutating method on a wrapper
   (`onSend`, `onRecv`, `onDuplicateRecv`, `SetHostRepr`, `Stats`, `CalcStats`,
   `Start`, `Stop`) **must** hold the wrapper's `sync.Mutex` (RWMutex preferred).
   `CalcStats` must return a **value copy** (`PWStats` is small; do not return a
   pointer to the internal field).
2. **`HostRepository.GetAll()` returns a defensive copy** of the slice. Callers
   may mutate the returned slice without locking.
3. **`UpdateAll` replaces the slice atomically under the write lock.** Old
   wrappers' `Stop()` is the caller's responsibility — service handles it.
4. **`TUIModel.updateStatsCache()` is the only place that calls `CalcStats()`
   per wrapper per tick.** UI rendering reads `statsCache` via
   `getCachedStats()`. Do not introduce other per-frame `CalcStats` paths.
5. **Stats cache miss must return an empty `PWStats`, not block.** See
   `TUIModel.getCachedStats` — this prevents the "first `View()` hangs on /24"
   regression documented in `DIFF_ANALYSIS.md`.
6. **`Stop()` methods must be idempotent** — use `sync.Once` (see
   `TCPPingWrapper.stopOnce`, `TransitionWriter.closeOnce`). It is legal and
   expected for `Stop()` to be called before `Start()`.
7. **DNS lookups in `Start()` are forbidden.** They block startup on large
   subnets. Use the `DNSUpdater` 3s-initial / 60s-periodic pattern.
8. **All goroutines started by wrappers must exit on `Stop()` within 1
   tick interval.** Use `context.WithCancel`, `chan struct{}`, or ticker stop.
   Use `runtime/debug.Stack()` in any goroutine panic guard.

### 2.3 PWStats state machine

`PWStats.ComputeState(timeout)` is the single point of truth for up/down state.
Never compute `state = lastrecv > 0 && delta < threshold` outside of it. The
fields `skip_next_up_highlight`, `loss_reference_recv`, `last_up_transition`,
`has_ever_been_online`, `last_loss_nano`, `last_loss_duration` form a coupled
state machine — change them only via `ComputeState`, never directly.

The transition log JSON written inside `ComputeState` uses the schema defined
in the package comment of `pwstats.go`. Do not add fields to it without
updating `README.md` and `docs/API.md` (`-log` format).

### 2.4 Configuration & persistence

1. The single source of truth for user-config is `~/.config/mping/config.yaml`
   (overridable via `MPING_CONFIG`). All writes go through `SaveUserSettings`
   which uses atomic temp-rename.
2. The single source of truth for the version literal is `var Version = "..."`
   in `main.go`. `scripts/release.sh` and `windows/mping.iss` are derived via
   `scripts/bump_version.sh`. Do not introduce a fourth location.
3. `ValidateUserSettings` must reject: empty hosts, unknown `FilterMode`/
   `SortMode`/`UpdateRate` values, duplicate or out-of-range column IDs
   (must be 1..6).
4. The YAML parser in `user_settings.go` is a deliberately tiny subset. Don't
   pull in `gopkg.in/yaml.v3` for one new key. Extend the existing parser
   instead.

### 2.5 Performance & resource limits

| Limit | Value | Where enforced |
|---|---|---|
| CIDR expansion | ≤ 65 536 hosts | `maxExpandedCIDRHosts` in `subnet.go` |
| Once-mode worker pool | 100 concurrent pings | `onceWorkerLimit` |
| Wrapper startup concurrency | 20 concurrent starts + 1 ms every 10 | `ping_service.go` startWrappers |
| DNS startup concurrency | 20 concurrent lookups | `dns_updater.go` `performDNSUpdates` |
| Trace semaphore | 2 concurrent traces | `status_server.go` `traceSem` |
| Trace-state map | 256 entries (FIFO prune) | `maxTraceStates` |
| Hidden-hosts view patch | 10 000 items, 512 chars/key | `maxHiddenViewItems`, `maxHiddenKeyLength` |
| View patch body | 1 MiB | `maxViewPatchBytes` |
| Reverse-DNS timeout | 500 ms per lookup | `host_display.go` |
| DNS-updater tick | 3 s initial, 60 s period | `dns_updater.go` `Start` |
| Positive DNS cache TTL | 1 hour | `dns_updater.go` `performDNSUpdates` |
| Negative DNS cache TTL | 5 minutes | `dns_updater.go` `performDNSUpdates` |
| Adaptive slow interval | 10 s | `PWStats.GetPingInterval` |
| Adaptive threshold override | 12 s (was 2/5 s) | `main.go` |
| Transition-writer flush | 500 ms | `transitionwriter.go` |
| TUI render tick | 100 ms | `tui.go` `tickCmd` |
| Stats cache tick | 100 ms / 1 s / 5 s / 30 s | `UpdateRate` enum |

If a change requires modifying any of these, document the rationale in the
commit message and update `docs/TROUBLESHOOTING.md` if it affects users.

### 2.6 Dependencies

- Do **not** add a new top-level dependency without first checking that the
  feature is not already available in:
  - `bubbletea`, `bubbles`, `lipgloss` (UI)
  - `pro-bing` (ICMP)
  - `tcp-shaker` (TCP)
  - `pterm` (legacy output)
  - `selfupdate`, `fastjson`, `xz` (update path)
  - `tview`/`smidgen` (config editor — already present)
- Vendoring is mandatory for cross-compilation in CI. After any `go.mod`
  change run `go mod vendor` and commit the `vendor/` diff.
- Pure-Go stdlib is preferred for: parsers, encoders, small utilities. The
  tiny YAML subset in `user_settings.go` is an example of this rule.

### 2.7 Error handling

1. **Never `panic` in wrapper goroutines** — use `defer recover()` with
   stderr logging (see `ping_service.go startWrappers`).
2. **DNS / parse errors must produce an `ErrorWrapper`**, not a fatal exit,
   unless the entire CLI is unusable. `mping` must always start with at least
   an empty host list.
3. **Webserver HTTP errors** must set `Cache-Control: no-store` and
   `Connection: close` to prevent stale streaming.
4. **`log.Fatalln` is allowed only in `selfUpdate` and unrecoverable CLI
   pre-flight**. Prefer `fmt.Fprintf(os.Stderr, ...)` + exit code.

---

## 3. Coding Conventions

### 3.1 Style

- Early returns over nested `if`s. See `PWStats.ComputeState` as the reference.
- Pre-existing patterns to follow exactly:
  - Field naming: `snake_case` in struct literals (matches `PWStats`).
  - Method receivers: short, `m *TUIModel`, `s *StatusServer`, `w *TCPPingWrapper`.
  - File header: no banner; `//go:build` lines on Windows-specific files
    (`pinger_tcp_win.go`).
  - Constants: `kConventionalName` for tunables, prefixed with the concept
    (`maxExpandedCIDRHosts`, `onceWorkerLimit`, `maxTraceStates`).
- Imports: stdlib first, then third-party. No internal "company" prefix exists
  because everything is package `main`.

### 3.2 Adding a new feature

- **New ping wrapper** (e.g. UDP, HTTP probe):
  1. Create `pinger_<name>.go` implementing `PingWrapperInterface`.
  2. Add dispatch in `NewPingWrapper()` factory after matching the host
     string regex or scheme.
  3. Mutex-protect all `PWStats` mutations.
  4. Idempotent `Stop()` with `sync.Once`.
  5. Add unit test in `lifecycle_regression_test.go` style.
  6. Update `README.md` "Probing Methods" section.

- **New TUI keybinding**:
  1. Add field to `keyMap` in `tui.go`.
  2. Add binding initialization in `var keys = keyMap{...}`.
  3. Handle in `Update()` switch on `key.Matches`.
  4. Update `FooterModel.View()` text.
  5. Update `README.md` keyboard shortcuts table.

- **New filter/sort mode**:
  1. Add enum value to `FilterMode`/`SortMode`.
  2. Update `nextFilterMode`/`nextSortMode` cycles.
  3. Update `HeaderModel.getFilterModeString`/`getSortModeString`.
  4. Update `validFilterMode`/`validSortMode` in `status_server.go`.
  5. Update `tui_list.go sortWrappersSliceGeneric`.
  6. Update `README.md`.

- **New HTTP endpoint**:
  1. Register in `StartStatusServer` `mux.HandleFunc`.
  2. Use `allowMethods(w, r, http.MethodGet[, …])` guard.
  3. Set `Cache-Control: no-store`, `Connection: close`.
  4. Document in `docs/API.md`.

- **New config key**:
  1. Add field to `UserViewSettings` in `user_settings.go`.
  2. Extend `parseUserSettings` and `marshalUserSettingsYAML`.
  3. Add validation to `ValidateUserSettings`.
  4. Update `applyUserSettingsToModel` in `tui.go` and
     `userSettingsFromModel`.
  5. Update `docs/CONFIGURATION.md`.

### 3.3 Testing

- `lifecycle_regression_test.go` is the canonical template for new tests.
- Use `MockWrapper`, `countingWrapper`, `MockPingWrapper` instead of
  inventing new mock types when possible.
- `httptest.NewRecorder` + `httptest.NewRequest` for handler tests.
- For time-dependent tests, **always** use `nowFunc` from `pwstats.go` (it is
  intentionally a package-level var to allow monkey-patching).
- For DNS-touching code paths, prefer constructing `PWStats` directly with
  seeded timestamps (see `NewErrorWrapper`) over real DNS queries.
- The TUI is tested only via lifecycle/stats-cache invariants. Do not try to
  unit-test `tea.Update` outputs.

### 3.4 Commit hygiene

- Conventional commits not enforced, but the message should describe:
  - the symptom or feature (what),
  - the root cause or approach (why),
  - the test that locks it in (if any).
- Do not mix refactors with behavioral changes in the same commit.

---

## 4. Platform-Specific Rules

| OS | Build tags | Ping default | TCP |
|---|---|---|---|
| Linux | (none) | Pure-Go, falls back to `-s` if raw ICMP denied | SYN/ACK via `tcp-shaker` |
| macOS | (none) | Pure-Go (privileged) | Full handshake (`pinger_tcp_win.go` compiled as `//go:build windows` is excluded; macOS uses the unix file) |
| FreeBSD/OpenBSD | (none) | Pure-Go | SYN/ACK via `tcp-shaker` |
| Windows | `//go:build windows` only on `pinger_tcp_win.go` | System `ping -t` | Full handshake |

`buildPureGoAllowed()` (a one-shot probe in `main.go`) is the auto-fallback
gate. Do not bypass it.

IPv6:
- Host strings: `tcp://[::1]:22`, `ip6://host`, `tcp6://host`.
- `SystemPingWrapper` tries `ping6` first for IPv6 targets.

---

## 5. Anti-Patterns (explicitly forbidden)

| Anti-pattern | Why |
|---|---|
| Adding DNS lookup to `wrapper.Start()` | Blocks startup; 254-host /24 becomes unusable |
| Returning `*w.stats` from `Stats()` | Race: caller may read fields while goroutine writes |
| Calling `CalcStats` from `View()` | One render = N×M mutex acquisitions on /24 |
| Bypassing `HostRepository` from anywhere | Bypasses the `ReplaceHosts` lifecycle |
| Using `panic` for control flow | Wrappers must be `Stop()`-safe even mid-start |
| Adding a goroutine that can't be cancelled | Goroutine leak per `ReplaceHosts` cycle |
| Embedding another struct's fields into `PWStats` | Breaks the value-copy contract |
| Writing host strings into HTML templates via `+` | XSS — see `TestDashboardScriptDoesNotInjectHostHTML` |
| `for { time.Sleep(...) ; pw.Stop() }` | Hangs `Stop`; use context cancellation |
| Hidden mutable package globals (besides the 5 listed) | Hides state from tests and races |
| Trusting `r.URL.Query().Get("key")` without `TrimSpace` | Trace endpoint will index keys with whitespace |
| Reading `cfg := LoadConfig()` outside `main()` | Flag state must stay local |
| Bumping deps in `vendor/modules.txt` manually | Always via `go mod vendor` |
| Calling `hostDisplayName` without 500 ms context | Hangs DNS indefinitely on bad resolvers |
| Forgetting `defer cancel()` on `context.WithCancel` | Leaks until parent is GC'd |

---

## 6. Quick Decision Tree

- "Should I add a new dep?" → No, check stdlib + existing deps first (§2.6).
- "Where does this goroutine start?" → Wrapper `Start` is the only entry;
  cancellation via `Stop`. §2.2.
- "Should I mutate `PWStats`?" → Only inside the wrapper's mutex, only via
  field-level writes; state-machine fields only inside `ComputeState`. §2.3.
- "Should I call DNS?" → Not in `Start`. Use `DNSUpdater` or `hostDisplayName`
  with 500 ms context. §2.2.7.
- "Should I add a keybinding?" → Update `keyMap`, `keys{}`, `Update` switch,
  `FooterModel.View`, `README`. §3.2.
- "Should I add an HTTP endpoint?" → Register in `StartStatusServer`,
  document in `docs/API.md`. §3.2.
- "Should I write a test?" → Yes; follow `lifecycle_regression_test.go`
  style. §3.3.