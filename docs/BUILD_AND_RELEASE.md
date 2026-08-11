# Build and Release

This document describes how to build `mping` locally, how the release
pipeline works, and how to bump the version.

---

## 1. Local build

### Single platform

```bash
go build -o mping
```

Output is `mping` (or `mping.exe` on Windows).

### Cross-compile

The repository vendors its dependencies (`go mod vendor` is committed),
so cross-compilation does not need network access once the vendor tree
is up to date.

```bash
env CGO_ENABLED=0 GOOS=linux  GOARCH=amd64 go build -mod vendor -o dist/mping-linux-amd64
env CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -mod vendor -o dist/mping-windows-amd64.exe
```

Supported target matrix (see `scripts/release.sh`):

| OS | Arch | Notes |
|---|---|---|
| linux | amd64 | Default release target. Packages as `.deb` and Arch PKGBUILD if the host tools exist. |
| windows | amd64 | Inno-Setup installer (`windows/mping.iss`). |
| darwin | amd64 | Builds; no installer pipeline. |
| freebsd | amd64 | Builds; no installer pipeline. |
| openbsd | amd64 | Builds; no installer pipeline. |
| (any) | arm / arm64 / 386 | Builds; no installer pipeline. |

### Version metadata

`main.go` declares:

```go
var Version = "v1.1.9"
var CommitHash = "dev"
var BuildTimestamp = "1970-01-01T00:00:00"
var Builder = "go version go1.xx.y os/platform"
```

These are overridable via `go build -ldflags`:

```bash
go build -ldflags="\
  -X 'main.Version=v1.2.0' \
  -X 'main.CommitHash=$(git rev-parse --short HEAD)' \
  -X 'main.BuildTimestamp=$(date -u +%Y-%m-%dT%H:%M:%SZ)' \
  -X 'main.Builder=$(go version)'" \
  -o mping
```

The release script does this for you; see [`scripts/release.sh`](#3-the-release-script).

---

## 2. Repository layout

```
MultiPingTUI/
├── main.go                      # entry point + Version vars
├── config.go                    # flag definitions
├── pingwrapper.go               # factory + interface
├── pinger_probing.go            # pure-Go ICMP
├── pinger_system.go             # system `ping` subprocess
├── pinger_tcp.go                # TCP probe (linux/freebsd/openbsd/darwin)
├── pinger_tcp_win.go            # TCP probe (windows: full handshake)
├── error_wrapper.go             # safe placeholder for unresolvable hosts
├── pwstats.go                   # state machine
├── ping_service.go              # lifecycle, parallel startup
├── repository.go                # in-memory host store
├── transitionwriter.go          # buffered JSON log
├── dns_updater.go               # periodic reverse DNS
├── host_display.go              # one-shot reverse DNS
├── subnet.go                    # CIDR + once-mode
├── display.go                   # legacy pterm output
├── user_settings.go             # config persistence + tiny YAML parser
├── config_editor_smidgen.go     # embedded editor
├── traceroute.go                # traceroute / tracepath / tracert
├── selfupdate.go                # GitHub release self-update
├── tui.go                       # bubbletea model, render
├── tui_list.go                  # list render, filter, sort, grouping
├── tui_components.go            # header / footer / HostListModel
├── tui_utils.go                 # cycles, hidden-hosts clone, IP-key
├── status_server.go             # HTTP status mirror
├── *_test.go                    # unit tests
├── scripts/
│   ├── release.sh               # cross-build + upload
│   └── bump_version.sh          # atomic version bump
├── windows/
│   └── mping.iss                # Inno-Setup template
├── .github/workflows/
│   ├── release.yml              # tag → linux/windows build + release
│   └── windows-installer.yml    # tag → Inno-Setup installer
├── go.mod / go.sum              # module definitions
└── vendor/                      # vendored deps (committed)
```

---

## 3. The release script

[`scripts/release.sh`](../scripts/release.sh) is a single bash entry point
that builds, packages, and (optionally) uploads a release.

```bash
./scripts/release.sh
```

What it does, in order:

1. **Resolve metadata**: read `VERSION` from `scripts/release.sh`
   (single source of truth, kept in sync via `bump_version.sh`), then
   fill `COMMIT_HASH`, `DIRTY`, `BUILD_TIMESTAMP`, `BUILDER` from `git`
   and `go version`.
2. **Clean `dist/`**: `rm -rf dist; mkdir dist`.
3. **Build targets**: for each `OS/ARCH` pair in the `TARGETS` variable
   (currently `linux/amd64 windows/amd64`):
   - `env CGO_ENABLED=0 GOOS=$OS GOARCH=$ARCH go build -ldflags=... -mod vendor -o dist/<TARGET>`
   - If Linux/amd64 and `dpkg-deb` exists: build a `.deb` package.
   - If Linux/amd64 and `makepkg` exists: build an Arch package;
     otherwise stage a `PKGBUILD` for manual build.
4. **Hashes**: append `sha256sum` entries to `mping.sha256sum`.
5. **Compress**: `xz` (and `zip` on Windows) — controlled by
   `NOCOMPRESS=1`.
6. **GitHub upload** (skipped with `SKIP_GH_UPLOAD=1`):
   - If `gh` is installed and `gh release view $VERSION` succeeds,
     `gh release upload $VERSION dist/* --clobber`.
   - Otherwise `gh release create $VERSION dist/* --title $VERSION --notes "Automated release for $VERSION"`.
   - The target repo defaults to `oliverbenduhn/MultiPingTUI` but can
     be overridden via `GH_REPO=owner/name`.
7. **Quick local build**: `go build -o mping` so the developer has a
   runnable binary after the script returns.

### Useful environment variables

| Variable | Default | Effect |
|---|---|---|
| `MAINTAINER` | `oliverbenduhn` | Maintainer field in `.deb`/PKGBUILD |
| `NOCOMPRESS` | (unset) | Skip `xz`/`zip` compression |
| `SKIP_GH_UPLOAD` | (unset) | Skip the `gh release upload` step |
| `GH_REPO` | derived from `git remote get-url origin` | Target repo for `gh` |

---

## 4. Version bumps

The version literal lives in **three files**, kept in sync by
`scripts/bump_version.sh`:

| File | Format | Field |
|---|---|---|
| `main.go` | `var Version = "vX.Y.Z"` | `Version` |
| `scripts/release.sh` | `VERSION=vX.Y.Z` | `VERSION` |
| `windows/mping.iss` | `#define MyAppVersion "X.Y.Z"` | `MyAppVersion` |

To bump:

```bash
./scripts/bump_version.sh 1.2.3
# or
./scripts/bump_version.sh v1.2.3
```

The script:

1. Validates the argument (`^\d+\.\d+\.\d+$` after stripping an
   optional `v`).
2. Replaces the three literals with `re.subn` (one replacement each,
   aborts on zero or more-than-one matches).
3. Scans the repo for any remaining occurrence of the **old** version
   string and prints warnings (excluding `vendor/`, `dist/`, `.git/`,
   `go.mod`, `go.sum`).
4. Runs a quick local `go build -o mping` to verify everything still
   compiles.

After a bump:

```bash
git add main.go scripts/release.sh windows/mping.iss
git commit -m "Bump version to v1.2.3"
git tag v1.2.3
git push origin master --tags
```

The CI workflows (next section) trigger on the `v*` tag.

---

## 5. CI workflows

### `.github/workflows/release.yml`

Triggered by any tag matching `v*`.

```yaml
on:
  push:
    tags:
      - "v*"
```

Steps:

1. `actions/checkout@v7`
2. `actions/setup-go@v6` with `go-version: '1.24.10'`
3. `./release.sh` — produces `dist/mping-linux-amd64`,
   `dist/mping-windows-amd64.exe` (+ `.xz`, `.zip`, `.deb`, Arch
   package if the runner has the tooling).
4. `marvinpinto/action-automatic-releases@latest` uploads everything in
   `dist/*` as release assets.

### `.github/workflows/windows-installer.yml`

Triggered by `v*` tags **or** manual `workflow_dispatch`.

Steps:

1. `actions/checkout@v7`
2. `actions/setup-go@v5` with `go-version: "1.24.10"`
3. `go test ./...` — runs the unit test suite on Windows.
4. Build `dist/mping-windows-amd64.exe` (no `-mod vendor` here, since
   `setup-go` already populates the module cache).
5. `choco install innosetup -y` and `iscc windows\\mping.iss` to
   produce `dist/mping-setup.exe`.
6. `actions/upload-artifact@v4` uploads the installer as
   `mping-installer`.

---

## 6. Installer template (`windows/mping.iss`)

Inno-Setup script. Key points:

- AppId is hard-coded to track upgrades: `{4E5F85B0-8B0E-4F50-9B8E-42CF5D4D3C72}`.
- Installs to `{pf}\mping` with binary name `mping.exe`.
- `LicenseFile=..\LICENSE` (relative to the script).
- Optional tasks: `path` (add to system `PATH`) and `desktopicon`,
  both unchecked by default.
- The `NeedsAddPath` helper avoids duplicating the install path in
  `PATH`.

To edit the version, run `scripts/bump_version.sh` (don't hand-edit
`#define MyAppVersion`).

---

## 7. Package templates

### `.deb` (`dpkg-deb`)

Produced by `scripts/release.sh` on Linux/amd64 when `dpkg-deb` is
installed:

```text
Package: mping
Version: 1.2.3
Section: net
Priority: optional
Architecture: amd64
Maintainer: oliverbenduhn
Description: MultiPingTUI CLI (mping) - multi-host ping/TCP probe TUI
```

The binary is staged at `usr/bin/mping`. Files land in `dist/`.

### Arch (`PKGBUILD`)

Produced as `dist/arch/PKGBUILD` and built by `makepkg -f` if
available:

```text
pkgname=mping
pkgver=1.2.3
pkgrel=1
pkgdesc="MultiPingTUI CLI - multi-host ping/TCP probe TUI"
arch=('x86_64')
url="https://github.com/oliverbenduhn/MultiPingTUI"
license=('MIT')
depends=('glibc')
source=("mping")
md5sums=('SKIP')

package() {
    install -Dm755 "${srcdir}/mping" "${pkgdir}/usr/bin/mping"
}
```

If `makepkg` is missing, the script stages the `PKGBUILD` for manual
build.

---

## 8. Self-update mechanism

`mping -update` (in `selfupdate.go`) does the following:

1. `GET https://api.github.com/repos/oliverbenduhn/MultiPingTUI/releases/latest`
2. Parses `name` with `fastjson`.
3. Compares with the embedded `Version` via `golang.org/x/mod/semver`:
   - `-1` → "newer than latest" warning, exit.
   - `0` → "already latest", exit.
   - `1` → proceed.
4. Computes the asset URL:
   `https://github.com/oliverbenduhn/MultiPingTUI/releases/download/<tag>/mping-<GOOS>-<GOARCH>.<ext>`
   - `ext = "xz"` on POSIX, `"exe.xz"` on Windows.
5. Calls `selfupdate.CheckPermissions()` — if the binary is not
   writable by the current user, prints a manual-download message and
   exits.
6. `GET <asset_url>` → `xz.NewReader` → `selfupdate.Apply`.

For `v0.0.0` (development build), the script requires an extra
`Enter` press before continuing, to avoid overwriting a developer
binary by accident.

---

## 9. Verifying a release locally

```bash
# Build
./scripts/release.sh SKIP_GH_UPLOAD=1

# Inspect
ls -la dist/
cat dist/mping.sha256sum | head

# Smoke-test the binary
./dist/mping-linux-amd64 -once localhost
./dist/mping-linux-amd64 -h
```

---

## 10. Common pitfalls

| Pitfall | Mitigation |
|---|---|
| `go: cannot find main module` | Run from the repo root; `go.mod` is at the top level. |
| `go build` fails on Windows because of CGO | Always pass `CGO_ENABLED=0` for cross-compilation. |
| `dpkg-deb: command not found` | Install `dpkg-dev` on the build host, or set `SKIP_GH_UPLOAD=1` and package manually. |
| `gh: command not found` | Same — the script still produces `dist/`, it just skips the upload step. |
| `release.sh` references the wrong version | Run `scripts/bump_version.sh <v>` and commit; the script reads `VERSION=` from itself. |
| New top-level dep causes `go mod vendor` to fail in CI | Run `go mod tidy && go mod vendor` locally, commit both `go.mod`/`go.sum` and `vendor/`. |