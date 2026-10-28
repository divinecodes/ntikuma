#!/usr/bin/env python3
"""Apply Ntikuma metadata and Linux packaging to freshly generated Wails tooling."""
from pathlib import Path
import re
import shutil
import subprocess
import sys

ROOT = Path(__file__).resolve().parent.parent
APP_ID = "dev.divinetettey.ntikuma"


def configure(build):
    for path in build.rglob("*"):
        if not path.is_file() or path.suffix not in {".plist", ".xml", ".json", ".storyboard", ".pbxproj", ".nsi", ".nsh", ".manifest", ".yml"}:
            continue
        text = path.read_text()
        updated = text.replace("My Product Name", "Ntikuma").replace("My Product", "Ntikuma").replace("My Company", "Divine Tettey").replace("com.example.ntikuma", APP_ID).replace("This is a comment", "Ask an LLM from anywhere on your desktop")
        path.write_text(updated)
    config = build / "config.yml"
    text = config.read_text()
    for key, value in {
        "companyName": "Divine Tettey", "productName": "Ntikuma", "productIdentifier": APP_ID,
        "description": "Ask an LLM from anywhere on your desktop", "copyright": "(c) 2026, Divine Tettey",
        "comments": "", "version": "0.1.0",
    }.items():
        text = re.sub(rf'^  {key}:.*$', f'  {key}: "{value}"', text, count=1, flags=re.M)
    config.write_text(text)
    shutil.copy2(ROOT / "assets/appicon.png", build / "appicon.png")

    linux = build / "linux/Taskfile.yml"
    text = linux.read_text().replace("vars:\n", f"vars:\n  APP_ID: {APP_ID}\n", 1)
    text = text.replace('cp ../../appicon.png "{{.APP_NAME}}.png"', 'cp ../../appicon.png "{{.APP_ID}}.png"')
    text = text.replace("ICON: '{{.APP_NAME}}.png'", "ICON: '{{.APP_ID}}.png'")
    text = text.replace("DESKTOP_FILE: '../{{.APP_NAME}}.desktop'", "DESKTOP_FILE: '../{{.APP_ID}}.desktop'")
    text = text.replace('-ldflags="-w -s"', '-ldflags="-w -s -X main.version={{.RELEASE_VERSION}}"')
    for task, command in {
        "create:deb": 'python3 "{{.ROOT_DIR}}/scripts/package-deb.py" --version "{{.RELEASE_VERSION}}"',
        "generate:deb": "task: create:deb",
    }.items():
        entry = f"      - {command}" if command.startswith("task:") else f"      - '{command}'"
        replacement = f"  {task}:\n    summary: Creates a complete Debian package\n    cmds:\n{entry}\n\n"
        text, count = re.subn(rf"(?ms)^  {task}:\n.*?(?=^  [a-z])", lambda _: replacement, text, count=1)
        if count != 1:
            raise ValueError(f"The pinned Wails Linux template changed; cannot configure {task}")
    replacement = '''  generate:dotdesktop:
    summary: Generates the Ntikuma desktop entry
    cmds:
      - python3 "{{.ROOT_DIR}}/scripts/render-desktop.py" --binary "ntikuma" --output "{{.ROOT_DIR}}/build/linux/dev.divinetettey.ntikuma.desktop"

'''
    text, count = re.subn(r"(?ms)^  generate:dotdesktop:\n.*?(?=^  run:)", lambda _: replacement, text, count=1)
    if count != 1:
        raise ValueError("The pinned Wails Linux template changed; cannot configure desktop generation")
    linux.write_text(text)

    nfpm = build / "linux/nfpm/nfpm.yaml"
    text = nfpm.read_text().replace('"/usr/local/bin/ntikuma"', '"/usr/bin/ntikuma"')
    text = text.replace('"./build/appicon.png"', '"./assets/ntikuma.svg"')
    text = text.replace('"/usr/share/icons/hicolor/128x128/apps/ntikuma.png"', f'"/usr/share/icons/hicolor/scalable/apps/{APP_ID}.svg"')
    text = text.replace('"./build/linux/ntikuma.desktop"', f'"./build/linux/{APP_ID}.desktop"')
    text = text.replace('"/usr/share/applications/ntikuma.desktop"', f'"/usr/share/applications/{APP_ID}.desktop"')
    text = text.replace('homepage: "https://wails.io"\n', "")
    text = text.replace('  # postremove: "./build/linux/nfpm/scripts/postremove.sh"', '  postremove: "./build/linux/nfpm/scripts/postremove.sh"')
    nfpm.write_text(text)
    for name in ["postinstall", "postremove"]:
        hook = build / f"linux/nfpm/scripts/{name}.sh"
        hook.write_text('''#!/bin/sh
if command -v update-desktop-database >/dev/null 2>&1; then
  update-desktop-database -q /usr/share/applications || true
fi
if command -v gtk-update-icon-cache >/dev/null 2>&1; then
  gtk-update-icon-cache -q -t /usr/share/icons/hicolor || true
fi
exit 0
''')
        hook.chmod(0o755)
    for name, binary in [(f"{APP_ID}.desktop", "ntikuma"), ("desktop", "/usr/bin/ntikuma")]:
        subprocess.run([sys.executable, str(ROOT / "scripts/render-desktop.py"), "--binary", binary,
                        "--output", str(build / "linux" / name)], check=True)
    (build / "linux/ntikuma.desktop").unlink(missing_ok=True)
    android = build / "android/app/build.gradle"
    if android.exists():
        android.write_text(android.read_text().replace('applicationId "com.wails.app"', f'applicationId "{APP_ID}"'))
    android_task = build / "android/Taskfile.yml"
    if android_task.exists():
        android_task.write_text(android_task.read_text().replace('default "com.wails.app"', f'default "{APP_ID}"'))


if __name__ == "__main__":
    configure(Path(sys.argv[1]).resolve())
