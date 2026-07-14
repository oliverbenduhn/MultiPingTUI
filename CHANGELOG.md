# Changelog

All notable changes to `mping` are documented here. Format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/), versioning follows [Semantic Versioning](https://semver.org/).

## [Unreleased]

## [1.1.4] - 2026-XX-XX

### Fixed
- **Filter unreachable on startup**: `FilterAll` was silently coerced to `FilterSmart` at init by `NewTUIModel`. The `-only-all` / config-driven "show all hosts" use case was impossible to reach. `NewTUIModel` now accepts `FilterAll` as a valid initial choice.
- **First-second blank render**: Smart filter showed "No hosts match" for ~1 second on every start because `statsCache` was empty until the first tick. `NewTUIModel` now pre-warms the cache synchronously.
- **Header/filter mismatch on startup**: `HeaderModel.filterMode` was never synced with `hostList.filterMode`, so the header label disagreed with the active filter at startup. Synced in `NewTUIModel`.
- **Vague help text for Smart filter**: Footer help now explains `Smart=online|seen → Online → Offline → All` so users know which hosts survive each filter.

### Added
- **`MPING_EDITOR` environment variable**: overrides the editor launched by the `e` key. Default falls back to the built-in `-edit-config` subprocess. Makes the editor pick user-configurable and the path mockable in tests.
- **TUI test suite** (`tui_test.go`): 5 user-flow tests (navigation, filter/sort cycle, edit-mode, detail sub-views, terminal-size matrix) using a 40-line headless harness around `tea.NewProgram`. No new dependencies.

### Changed
- `lifecycle_regression_test.go::TestTUITickUpdatesStatsCacheOnce` — now expects 1 pre-warm at init + 1 per tick (was: 1 total per tick). Reflects the new pre-warm semantics.

## [1.1.3] - 2026-01-XX

### Added
- Web status server (`-webserver <port>`): HTTP sidecar with JSON endpoints (`/json`, `/state`, `/view`, `/trace`) and HTML dashboard.
- Pure-Go ICMP via `pro-bing` as the default backend.
- TCP probing via `tcp-shaker` (SYN/ACK on Linux/BSD, full handshake on Windows/macOS).
- Adaptive ping intervals: hosts that never respond are probed every 10s instead of 1s. Auto-enabled for CIDR arguments.
- CIDR expansion in CLI args (`192.168.1.0/24` → individual hosts).
- Reverse DNS via `dns_updater.go` with 500ms per-lookup timeout.
- `-no-dns` flag to skip reverse DNS for faster startup.
- Staggered wrapper startup with 20-way concurrency to prevent ARP storms on large subnets.
- `-edit-config` subprocess mode for in-TUI YAML editing.

## [1.1.0] - 2025-XX-XX

### Added
- Initial release: TUI mode with filter/sort/edit, legacy pterm display mode, JSONL transition logging, self-update.

[Unreleased]: https://github.com/oliverbenduhn/MultiPingTUI/compare/v1.1.4...HEAD
[1.1.4]: https://github.com/oliverbenduhn/MultiPingTUI/compare/v1.1.3...v1.1.4
[1.1.3]: https://github.com/oliverbenduhn/MultiPingTUI/compare/v1.1.0...v1.1.3
[1.1.0]: https://github.com/oliverbenduhn/MultiPingTUI/releases/tag/v1.1.0