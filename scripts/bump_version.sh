#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd -- "${SCRIPT_DIR}/.." && pwd)"
cd "${REPO_ROOT}"

usage() {
  cat <<'EOF'
Usage: ./scripts/bump_version.sh <version>

Updates the project version in:
  - main.go (var Version = "vX.Y.Z")
  - scripts/release.sh (VERSION=vX.Y.Z)
  - windows/mping.iss (#define MyAppVersion "X.Y.Z")

The <version> parameter must be in the form 1.2.3 (optionally prefixed with "v").
EOF
}

if [[ "${1:-}" == "-h" || "${1:-}" == "--help" ]]; then
  usage
  exit 0
fi

if [[ $# -ne 1 ]]; then
  usage >&2
  exit 2
fi

python3 - "$1" <<'PY'
import re
import sys
from pathlib import Path

new_arg = sys.argv[1].strip()
new = new_arg[1:] if new_arg.startswith("v") else new_arg

if not re.fullmatch(r"\d+\.\d+\.\d+", new):
    print(f"error: invalid version '{new_arg}' (expected 1.2.3 or v1.2.3)", file=sys.stderr)
    sys.exit(2)

new_v = f"v{new}"

def update_file(path: str, pattern: str, repl: str) -> int:
    p = Path(path)
    data = p.read_text(encoding="utf-8")
    updated, n = re.subn(pattern, repl, data, flags=re.M)
    if n != 1:
        raise SystemExit(f"error: expected 1 replacement in {path}, got {n}")
    if updated != data:
        p.write_text(updated, encoding="utf-8")
    return n

main_go = Path("main.go").read_text(encoding="utf-8")
m = re.search(r'^var Version = "(v\d+\.\d+\.\d+)"\s*$', main_go, flags=re.M)
if not m:
    print('error: could not find `var Version = "vX.Y.Z"` in main.go', file=sys.stderr)
    sys.exit(2)
old_v = m.group(1)

if old_v == new_v:
    print(f"no-op: version already {new_v}")
    sys.exit(0)

update_file("main.go", r'^var Version = "v\d+\.\d+\.\d+"\s*$', f'var Version = "{new_v}"')
update_file("scripts/release.sh", r'^VERSION=v\d+\.\d+\.\d+\s*$', f"VERSION={new_v}")
update_file("windows/mping.iss", r'^#define MyAppVersion "\d+\.\d+\.\d+"\s*$', f'#define MyAppVersion "{new}"')

def rg_like_find_version_hits(version: str) -> list[str]:
    hits: list[str] = []
    excludes = ("vendor/", "dist/", ".git/")
    for p in Path(".").rglob("*"):
        if not p.is_file():
            continue
        sp = p.as_posix()
        if sp.startswith(excludes) or sp in ("go.mod", "go.sum"):
            continue
        try:
            text = p.read_text(encoding="utf-8")
        except Exception:
            continue
        if version in text:
            hits.append(sp)
    return sorted(set(hits))

remaining = rg_like_find_version_hits(old_v)
if remaining:
    print(f"warning: old version string '{old_v}' still present in:")
    for f in remaining:
        print(f"  - {f}")

print(f"updated: {old_v} -> {new_v}")

# Quick local build
go build -o mping
PY
