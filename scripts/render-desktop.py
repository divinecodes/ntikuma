#!/usr/bin/env python3
"""Render the shared desktop templates for user installs and Linux packages."""
import argparse
from pathlib import Path

APP_ID = "dev.divinetettey.ntikuma"


def render(binary, kind="launcher"):
    if not binary or any(c in binary for c in "\n\r\t="):
        raise ValueError("desktop executable paths cannot be empty or contain tabs, newlines, or '='")
    # Exec quoting is applied after the desktop string's backslash unescaping.
    escaped = "".join("\\" + c if c in '\\"`$' else c for c in binary)
    escaped = escaped.replace("%", "%%").replace("\\", "\\\\")
    # The fixed env executable also avoids GLib testing a %% path before its
    # field-code expansion. env execs the binary directly without a shell.
    executable = '"' + escaped + '"'
    name = f"{APP_ID}.autostart.desktop" if kind == "autostart" else f"{APP_ID}.desktop"
    template = Path(__file__).resolve().parent.parent / "packaging" / name
    return template.read_text().replace("@BIN@", executable)


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--binary", required=True)
    parser.add_argument("--kind", choices=["launcher", "autostart"], default="launcher")
    parser.add_argument("--output", required=True, type=Path)
    args = parser.parse_args()
    try:
        content = render(args.binary, args.kind)
    except ValueError as error:
        parser.error(str(error))
    args.output.parent.mkdir(parents=True, exist_ok=True)
    args.output.write_text(content)
    args.output.chmod(0o644)
