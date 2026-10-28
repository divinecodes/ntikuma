#!/usr/bin/env bash
# Generate ignored build tooling in a temporary project; preserve local files.
set -euo pipefail
ROOT=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)
cd "$ROOT"
for tool in go node npm python3; do
  command -v "$tool" >/dev/null || { echo "Required tool missing: $tool. See README.md." >&2; exit 1; }
done
[[ -f Taskfile.yml ]] || { echo "Missing root Taskfile.yml. Restore it from Git." >&2; exit 1; }
WAILS_VERSION=$(awk '$1 == "github.com/wailsapp/wails/v3" { print $2; exit }' go.mod)
[[ -n $WAILS_VERSION ]] || { echo "Cannot determine the Wails version from go.mod" >&2; exit 1; }
if ! command -v wails3 >/dev/null || [[ $(wails3 version 2>&1) != "$WAILS_VERSION" ]]; then
  go install "github.com/wailsapp/wails/v3/cmd/wails3@$WAILS_VERSION"
  GO_BIN=$(go env GOBIN)
  if [[ -z $GO_BIN ]]; then GO_PATH=$(go env GOPATH); GO_BIN="${GO_PATH%%:*}/bin"; fi
  export PATH="$GO_BIN:$PATH"
fi
[[ $(wails3 version 2>&1) == "$WAILS_VERSION" ]] || { echo "Wails CLI version does not match $WAILS_VERSION" >&2; exit 1; }
if [[ ! -f build/Taskfile.yml || ! -f build/linux/Taskfile.yml || ! -f build/config.yml ]]; then
  BUILD_STAGE=$(mktemp -d)
  trap 'rm -rf -- "$BUILD_STAGE"' EXIT
  wails3 init -n ntikuma -t svelte -d "$BUILD_STAGE" -mod ntikuma -skipgomodtidy -q \
    -productname Ntikuma -productcompany "Divine Tettey" \
    -productidentifier dev.divinetettey.ntikuma \
    -productdescription "Ask an LLM from anywhere on your desktop" \
    -productcopyright "(c) 2026, Divine Tettey"
  python3 scripts/configure-build.py "$BUILD_STAGE/ntikuma/build"
  python3 - "$BUILD_STAGE/ntikuma/build" "$ROOT/build" <<'PY'
from pathlib import Path
import shutil, sys
source, destination = map(Path, sys.argv[1:])
destination.mkdir(parents=True, exist_ok=True)
for path in source.rglob('*'):
    target = destination / path.relative_to(source)
    if path.is_dir():
        target.mkdir(parents=True, exist_ok=True)
    elif not target.exists():
        shutil.copy2(path, target)
PY
  wails3 task common:generate:icons
fi
go mod download
(cd frontend && npm ci)
wails3 doctor
echo "Setup complete. Run 'wails3 dev', or 'wails3 build' then './scripts/install.sh'."
