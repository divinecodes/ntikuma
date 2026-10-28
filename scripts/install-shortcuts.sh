#!/usr/bin/env bash
# Binds GNOME custom shortcuts to Ntikuma:
#   Super+Space        ntikuma toggle
#   Super+Shift+Space  ntikuma voice
#
# GNOME uses Super+Space (and Super+Shift+Space) to switch keyboard input
# sources. If those are bound, the script asks before taking them over; your
# XF86Keyboard key keeps switching layouts either way.
#
# Usage:
#   ./scripts/install-shortcuts.sh            install
#   ./scripts/install-shortcuts.sh --yes      install, take over Super+Space without asking
#   ./scripts/install-shortcuts.sh --remove   remove Ntikuma's shortcuts
#   NTIKUMA_BIN=/path/to/ntikuma ./scripts/install-shortcuts.sh
set -euo pipefail

SCHEMA=org.gnome.settings-daemon.plugins.media-keys
BASE=/org/gnome/settings-daemon/plugins/media-keys/custom-bindings
WM=org.gnome.desktop.wm.keybindings
IDS=(ntikuma-toggle ntikuma-voice)

BIN=${NTIKUMA_BIN:-$(command -v ntikuma || echo "$HOME/.local/bin/ntikuma")}
BIN_COMMAND=$(python3 - "$BIN" <<'PY'
import shlex, sys
print(shlex.quote(sys.argv[1]))
PY
)
YES=false
REMOVE=false
for arg in "$@"; do
  case $arg in
    --yes | -y) YES=true ;;
    --remove) REMOVE=true ;;
  *) echo "unknown option $arg" >&2; exit 2;;
 esac
done

command -v gsettings >/dev/null || { echo "gsettings not found; is this GNOME?" >&2; exit 1; }

# edits the list of custom keybindings. $1 = add|remove, rest=paths
edit_list() {
  local op=$1; shift
  local current
  current=$(gsettings get "$SCHEMA" custom-keybindings)
  python3 - "$op" "$current" "$@" <<'PY'
import ast,sys
op, current, paths = sys.argv[1], sys.argv[2],sys.argv[3:]
current = current.replace("@as ", "")
items = ast.literal_eval(current) if current.strip() else []
for p in paths:
    if op == "add" and p not in items:
        items.append(p)
    if op == "remove" and p in items:
        items.remove(p)
print("[" + ", ".join(repr(i) for i in items) +"]")
PY
}

paths=()
for id in "${IDS[@]}"; do paths+=("$BASE/$id/"); done

if $REMOVE; then
  gsettings set "$SCHEMA" custom-keybindings "$(edit_list remove "${paths[@]}")"
  for p in "${paths[@]}"; do
    gsettings reset-recursively "$SCHEMA.custom-keybinding:$p" 2>/dev/null || true
  done
  echo "Removed Ntikuma's shortcuts. (Input-source shortcuts are left as they are)"
  exit 0
fi

# Super+Space usually switches input sources. Offer to free it
fwd=$(gsettings get "$WM" switch-input-source)
back=$(gsettings get "$WM" switch-input-source-backward)
if [[ $fwd == *"<Super>space"* || $back == *"<Shift><Super>space"* ]]; then
  echo "GNOME uses Super+Space to switch keyboard layouts"
  take=$YES
  if ! $YES &&  [[ -t 0 ]]; then
    read -r -p "Move layout switching to the keyboard key only and give Super+Space to Ntikuma: " answer
    [[ $answer =~ ^[Yy] ]] && take=true
  fi
  if $take; then
    gsettings set "$WM" switch-input-source "['XF86Keyboard']"
    gsettings set "$WM" switch-input-source-backward "['<Shift>XF86Keyboard']"
    echo "Freed Super+Space. Undo with gsettings reset $WM switch-input-source; gsettings reset $WM switch-input-source-backward"
  else
    echo "Leaving layout switching alone. Pick other keys GNOME Settings > Keyboard > Custom Shortcuts"
  fi
fi

gsettings set "$SCHEMA" custom-keybindings "$(edit_list add "${paths[@]}")"

set_binding() {
  local id=$1 name=$2 command=$3  accel=$4
  local p="$SCHEMA.custom-keybinding:$BASE/$id/"
  gsettings set "$p" name "$name"
  # GSettings parses a GVariant string before GNOME parses the shell command.
  local value
  value=$(python3 - "$command" <<'PY'
import json, sys
print(json.dumps(sys.argv[1]))
PY
)
  gsettings set "$p" command "$value"
  gsettings set "$p" binding "$accel"
}
set_binding ntikuma-toggle "Ntikuma" "$BIN_COMMAND toggle" "<Super>space"
set_binding ntikuma-voice "Ntikuma dictation" "$BIN_COMMAND voice" "<Shift><Super>space"

echo "Bound Super+Space to '$BIN toggle' and Super+Shift+Space to '$BIN voice'."
[[ -x $BIN ]] || echo "Note: $BIN doesn't exist yet. Run ./scripts/install.sh after building."
