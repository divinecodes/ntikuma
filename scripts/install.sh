#!/usr/bin/env bash
# Install a built Ntikuma for the current user. Does not install system packages.
set -euo pipefail

ROOT=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)
APP_ID=dev.divinetettey.ntikuma
SRC="$ROOT/bin/ntikuma"
START=true
SHORTCUTS=true
AUTOSTART=true
SOURCE_SET=false
for arg in "$@"; do
  case "$arg" in
    --no-start) START=false ;;
    --no-shortcuts) SHORTCUTS=false ;;
    --no-autostart) AUTOSTART=false ;;
    --help|-h)
      echo "Usage: $0 [--no-start] [--no-shortcuts] [--no-autostart] [binary]"
      exit 0 ;;
    -*) echo "Unknown option: $arg" >&2; exit 2 ;;
    *) if $SOURCE_SET; then echo "Specify only one binary" >&2; exit 2; fi
       SRC=$arg; SOURCE_SET=true ;;
  esac
done

for tool in python3 install mktemp; do
  command -v "$tool" >/dev/null || { echo "Required tool missing: $tool" >&2; exit 1; }
done
[[ -f $SRC && -x $SRC ]] || { echo "No executable at $SRC. Run 'wails3 build' first." >&2; exit 1; }
if $SHORTCUTS; then
  command -v gsettings >/dev/null || { echo "gsettings is missing; use --no-shortcuts outside GNOME." >&2; exit 1; }
fi
BIN_DIR=${NTIKUMA_BIN_DIR:-$HOME/.local/bin}
DATA_DIR=${XDG_DATA_HOME:-$HOME/.local/share}
CONFIG_DIR=${XDG_CONFIG_HOME:-$HOME/.config}
CACHE_DIR=${XDG_CACHE_HOME:-$HOME/.cache}
for dir in "$BIN_DIR" "$DATA_DIR" "$CONFIG_DIR" "$CACHE_DIR"; do
  [[ $dir == /* ]] || { echo "Installation directories must be absolute: $dir" >&2; exit 1; }
done
DEST="$BIN_DIR/ntikuma"
[[ ! -d $DEST ]] || { echo "Binary destination is a directory: $DEST" >&2; exit 1; }
APPS="$DATA_DIR/applications"
ICONS="$DATA_DIR/icons/hicolor/scalable/apps"
mkdir -p "$BIN_DIR" "$APPS" "$ICONS" "$CONFIG_DIR/autostart" "$CACHE_DIR/ntikuma"
STAGE=$(mktemp -d "$BIN_DIR/.ntikuma-install.XXXXXX")
trap 'rm -rf -- "$STAGE"' EXIT

# Validate and stage everything before stopping the running instance.
install -m 0755 -- "$SRC" "$STAGE/ntikuma"
install -m 0644 -- "$ROOT/assets/ntikuma.svg" "$STAGE/icon.svg"
python3 "$ROOT/scripts/render-desktop.py" --binary "$DEST" --output "$STAGE/launcher.desktop"
python3 "$ROOT/scripts/render-desktop.py" --binary "$DEST" --kind autostart --output "$STAGE/autostart.desktop"
if command -v desktop-file-validate >/dev/null; then
  desktop-file-validate "$STAGE/launcher.desktop" "$STAGE/autostart.desktop"
fi
"$STAGE/ntikuma" quit || { echo "Could not ask the running instance to quit; installation stopped." >&2; exit 1; }

# Rename rather than overwrite an executing binary, so upgrades avoid ETXTBSY.
mv -f -- "$STAGE/ntikuma" "$DEST"
install -m 0644 -- "$STAGE/icon.svg" "$ICONS/$APP_ID.svg"
install -m 0644 -- "$STAGE/launcher.desktop" "$APPS/$APP_ID.desktop"
if $AUTOSTART; then
  install -m 0644 -- "$STAGE/autostart.desktop" "$CONFIG_DIR/autostart/$APP_ID.desktop"
else
  rm -f -- "$CONFIG_DIR/autostart/$APP_ID.desktop"
fi
if command -v update-desktop-database >/dev/null; then update-desktop-database "$APPS" || true; fi
if command -v gtk-update-icon-cache >/dev/null; then gtk-update-icon-cache -q -t "$DATA_DIR/icons/hicolor" || true; fi
if $SHORTCUTS; then NTIKUMA_BIN="$DEST" "$ROOT/scripts/install-shortcuts.sh"; fi

if $START; then
  python3 - "$DEST" "$CACHE_DIR/ntikuma/startup.log" <<'PY'
import os, subprocess, sys, time
binary, logfile = sys.argv[1:]
with open(logfile, "ab") as log:
    os.chmod(logfile, 0o600)
    # A quitting Wails instance may briefly retain its single-instance lock.
    for attempt in range(5):
        process = subprocess.Popen([binary, "start"], stdin=subprocess.DEVNULL,
                                   stdout=log, stderr=log, start_new_session=True)
        try:
            result = process.wait(timeout=1)
        except subprocess.TimeoutExpired:
            break
        if result != 0:
            sys.exit(f"Ntikuma failed to start. See {logfile}")
        time.sleep(0.2)
    else:
        sys.exit(f"Ntikuma did not stay running. See {logfile}")
PY
fi
echo "Installed Ntikuma at $DEST"
if $START; then echo "Ntikuma is running in the tray."; fi
if $AUTOSTART; then echo "Autostart is enabled for the next desktop login."; fi
