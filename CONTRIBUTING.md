# Contributing

## Workflow

1. Branch off `master`.
2. Make the change. Keep the diff focused — bug fixes don't include style cleanups.
3. `go test -race -count=1 ./...` passes locally.
4. `go build ./...` clean.
5. Open a PR. CI runs the same checks on Linux.

## Code Style

Go standard `gofmt`. Imports grouped (stdlib, third-party, internal). No `golint`/`golangci-lint` configured — keep it readable by hand.

### Naming

- Exported names: `PascalCase`, doc-comment first line.
- Unexported names: `camelCase`. Test helpers `t.Helper()` first line.
- File names: `snake_case.go`. One type per file when the file would be <200 lines; bundle small related helpers.

### Comments

- Inline comments answer **why**, not what. The code shows what it does.
- Mark deliberate simplifications with `# ponytail: <ceiling>, <upgrade path>`. This is a grep target — future agents use it to find debt.
- No banner comments (`// ======= Section =======`).
- Doc comments on exported identifiers only.

## Testing

- One runnable check per non-trivial logic. A new branch / parser / state transition leaves exactly one test behind.
- For TUI code: a flow test in `tui_test.go`. See existing `TestFlow1..5` for the harness pattern.
- For `PingWrapperInterface` implementations: a unit test against the contract, not a real network probe.
- `go test -race` is mandatory before pushing.

### TUI testing pattern

```go
// 1. Build a fake wrapper satisfying PingWrapperInterface (see fakeWrapper).
// 2. Seed a MemoryHostRepository with the wrappers you want visible.
// 3. newTestModel(repo, gs, initialFilter) returns a configured TUIModel.
// 4. runProgram(t, m, w, h, []tea.Msg{...}, settleDuration) feeds keys and returns rendered output.
// 5. Assert on substrings of the rendered output.
```

Don't add mock libraries. Hand-rolled fakes. ~15 lines is enough.

### What to test

- Every new `case` in `Update()` keyboard handling.
- Every new branch in `getFilteredWrappers` or sort logic.
- Every new field in `PWStats.ComputeState`.
- Every new `PingWrapperInterface` method that does non-trivial work.

### What not to test

- Wrapper, frame, or marshaling code that has no logic.
- Re-exports of stdlib behavior.
- Cosmetic UI changes (verify manually).

## Pull Requests

- Commit message explains **why**, not what. The diff shows the what.
- One logical change per commit. Refactors + fixes in the same commit make bisecting painful.
- PR description: link to the issue, describe user-visible behavior, list the risks.
- If you touch `tui.go`, `tui_list.go`, `tui_components.go`, `pwstats.go`, `ping_service.go`, or `repository.go`: at least one test in `tui_test.go` must exercise the changed path.

## Adding a New Ping Backend

1. Implement `PingWrapperInterface` (`Start`, `Stop`, `Host`, `CalcStats`, `Stats`, `SetHostRepr`).
2. Add a branch to `NewPingWrapper()` in `pingwrapper.go` for the scheme prefix.
3. Write a unit test using a mock target (loopback for TCP, never-replied host for ICMP errors).
4. If the backend requires new privileges, document them in `docs/OPERATIONS.md`.

Don't add a new dependency unless:
- The feature can't be done with the existing 7 direct deps + Go stdlib.
- You write a sentence in the PR explaining why.

## Adding a New TUI View

1. Add the view mode constant to `viewMode` enum in `tui.go`.
2. Add the key binding to `keyMap` and a handler in `Update()`.
3. Add the render function (e.g. `renderXxxView()`).
4. Switch on `m.viewMode` in `View()` to call the renderer.
5. Update footer help text.
6. Add a sub-test to `TestFlow4_DetailSubViews` or write a new `TestFlowN_*`.

## Adding a New CLI Flag

1. Add the field to `Options` in `config.go`.
2. Bind the flag with `flag.BoolVar` / `StringVar` etc.
3. Use the value in `main.go` or `ping_service.go`.
4. Document in `README.md` flags table.
5. If it changes user-visible behavior, mention in `CHANGELOG.md`.

## Releases

Maintainer-only. Bump version with `./scripts/bump_version.sh <semver>`, commit, tag, push.

```bash
./scripts/bump_version.sh 1.2.0
git add main.go
git commit -m "chore: bump version to 1.2.0"
git tag v1.2.0
git push --follow-tags
```

GitHub Actions handles the rest (build + upload to releases).

## AI Agents

This repository is set up for AI-assisted development. The relevant docs:

- [CLAUDE.md](CLAUDE.md) — full context for Claude Code
- [AGENTS.md](AGENTS.md) — generic agent format (Aider, Continue, Cody)
- [.cursorrules](.cursorrules) — Cursor IDE shorthand

If you're an agent: read these before making changes. They define architectural boundaries and anti-patterns.