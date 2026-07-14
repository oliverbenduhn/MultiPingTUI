# AGENTS.md — MultiPingTUI

Context for AI coding agents (Aider, Continue, Cody, custom tools). For Claude Code specifically, see [CLAUDE.md](CLAUDE.md) — it has the same content plus full dev-pattern documentation. For Cursor, see [.cursorrules](.cursorrules).

## Project

`MultiPingTUI` is a Go TUI (terminal user interface) for monitoring multiple network targets via ICMP ping, TCP probing, or system `ping`. Binary: `mping`. Tech stack: bubbletea v1.3.10, lipgloss, pro-bing, tcp-shaker.

## Build / Test

```bash
go build -o mping                    # dev build
go test -race -count=1 ./...          # full test suite (~4s)
go test -v -run TestFlow ./...        # TUI flow tests only
./scripts/release.sh                  # cross-build + .deb + Arch PKGBUILD
```

## Module map

| File | Role |
|---|---|
| `main.go` | Entry. CLI flags, mode selection (TUI / legacy / once / webserver), signal handling. |
| `tui.go` + `tui_list.go` + `tui_components.go` | Bubbletea model. List, detail, dashboard views. Filter/sort logic. |
| `ping_service.go` + `repository.go` | Lifecycle: start, stop, replace hosts. `MemoryHostRepository` is the single wrapper store. |
| `pingwrapper.go` + `pinger_*.go` | Three ping backends behind `PingWrapperInterface`. Factory selects by URL scheme. |
| `pwstats.go` | Per-host state machine. `ComputeState(timeout_ns)` is the only mutation entry point. |
| `status_server.go` | HTTP sidecar. JSON endpoints, HTML dashboard. Read-mostly. |
| `transitionwriter.go` | Thread-safe JSONL logger for state changes. |
| `dns_updater.go` + `host_display.go` | Reverse DNS with 500ms per-lookup timeout. |
| `subnet.go` | CIDR expansion + once-mode worker pool. |
| `selfupdate.go` | GitHub release updater. |

## Architectural boundaries (strict)

1. **Repository access in the TUI.** Use `m.repo` (the `HostRepository` interface) for any wrapper read/write. Do not reach into `m.ps` from a view or update handler.
2. **UI state vs. domain state.** UI handlers mutate `hostList.cursor`, `hostList.scrollOffset`, `viewMode`, `statusMessage`. Domain state (`PWStats`, wrapper lifecycle) is mutated only by `PingService` methods.
3. **Filter/sort logic.** `getFilteredWrappers` in `tui_list.go` is the single source of truth. New filters/sorts go there or in `tui_utils.go`. Do not branch on `m.filterMode` inside `renderListView`.
4. **Stats lifecycle.** Stats live in `statsCache` (written only by `updateStatsCache()`). Read via `getCachedStats()`. Do not compute stats in render code — runs every frame.
5. **No new dependencies without explicit justification.** Seven direct deps in `go.mod`. New ones need a one-line justification in the commit message.

## Anti-patterns (forbidden)

- Mocking frameworks (gomock, mockery, testify-mock). Hand-rolled fakes.
- Golden-file snapshots in the first pass. Substring assertions on rendered output.
- Pre-emptive interfaces (interface with one implementation).
- Global locks. Per-field mutex, named for what it protects.
- `time.Sleep > 1s` in tests. Force `UpdateRate100ms` in test models.
- Reading wrapper internals outside `CalcStats` / `Stats()`.
- Hard-coded editor in the `e` key handler. Honor `MPING_EDITOR` env first.

## Conventions

- **Trace the full call chain before editing.** A bug report names a symptom. Grep every caller of the function you're about to change. Fixing only the reported path leaves siblings broken.
- **One runnable check per non-trivial logic.** No frameworks. The smallest thing that fails if the logic breaks.
- **Inline comments answer "why", not "what".**
- **Mark deliberate simplifications** with `# ponytail: <ceiling>, <upgrade path>`. Future agents grep this to find debt.
- **Do not refactor on the way to a fix.** Bug fix diffs are bug fix diffs.
- **Don't add features the user didn't ask for.**

## TUI testing pattern

The repo uses a 40-line headless harness around `tea.NewProgram` instead of `teatest` (which is v2-only). The pattern is:

1. Build a `fakeWrapper` satisfying `PingWrapperInterface`.
2. Seed a `MemoryHostRepository`.
3. Call `newTestModel(repo, gs, initialFilter)` which builds a `TUIModel` with `UpdateRate100ms` forced.
4. Call `runProgram(t, m, w, h, []tea.Msg{...keySeq...}, settleDuration)` to feed keys and capture output.
5. Assert on substrings of the rendered output.

See `tui_test.go` for the full harness and five user-flow tests.

## When you're stuck

1. Read `CLAUDE.md` end-to-end. It's the densest reference.
2. Read the relevant module file completely before editing.
3. For TUI bugs: write a test in `tui_test.go` first that reproduces the issue, then fix.
4. For ping-backend bugs: write a unit test using `localhost` or a non-routable IP — never real network.
5. For HTTP server bugs: `curl` the endpoint and inspect the response shape from `docs/API.md`.