#!/usr/bin/env python3
"""Preview or apply the current user's transition from a source install to the .deb."""
import argparse
import ast
from contextlib import closing, contextmanager
from datetime import datetime, timezone
import fcntl
import hashlib
import json
import os
from pathlib import Path
import shlex
import shutil
import sqlite3
import stat
import subprocess
import sys
import tempfile
import time

APP_ID = "dev.divinetettey.ntikuma"
ROOT = Path(__file__).resolve().parents[1]
PACKAGE_BINARY = Path("/usr/bin/ntikuma")
PACKAGE_HELPER = Path("/usr/lib/ntikuma/ntikuma-asr")
MEDIA_KEYS = "org.gnome.settings-daemon.plugins.media-keys"
BINDING = MEDIA_KEYS + ".custom-keybinding"
ACTIONS = {"start", "toggle", "show", "voice", "history", "settings", "quit"}


class MigrationError(Exception):
    pass


def absolute(value):
    path = Path(value)
    if not path.is_absolute() or any(c in str(path) for c in "\0\n\r\t"):
        raise MigrationError("migration paths must be absolute and contain no control characters")
    return Path(os.path.abspath(path))


class Profile:
    def __init__(self):
        self.home = absolute(os.environ["HOME"])
        self.config = absolute(os.environ.get("XDG_CONFIG_HOME") or self.home / ".config")
        self.data = absolute(os.environ.get("XDG_DATA_HOME") or self.home / ".local/share")
        self.bin = absolute(os.environ.get("NTIKUMA_BIN_DIR") or self.home / ".local/bin")
        self.settings = self.config / "ntikuma/config.json"
        self.database = self.data / "ntikuma/ntikuma.db"
        self.backups = self.data / "ntikuma/migration-backups"

    def identity(self):
        return {key: str(getattr(self, key)) for key in ("home", "config", "data", "bin")}


def snapshot(path, allow_link=False):
    try:
        info = path.lstat()
    except FileNotFoundError:
        return None
    if info.st_uid != os.geteuid():
        raise MigrationError(f"not owned by the current user: {path}")
    if stat.S_ISLNK(info.st_mode) and allow_link:
        return {"kind": "link", "target": os.readlink(path)}
    if not stat.S_ISREG(info.st_mode):
        raise MigrationError(f"expected a regular file, without a symlink: {path}")
    with path.open("rb") as stream:
        digest = hashlib.file_digest(stream, "sha256").hexdigest()
    return {"kind": "file", "sha256": digest, "mode": stat.S_IMODE(info.st_mode)}


def after_snapshot(change):
    data = change["after"]
    return None if data is None else {"kind": "file", "sha256": hashlib.sha256(data).hexdigest(),
                                     "mode": change["before"]["mode"]}


def read_text(path):
    if path.stat().st_size > 4 * 1024 * 1024:
        raise MigrationError(f"unexpectedly large settings or desktop file: {path}")
    return path.read_text(encoding="utf-8")


def command(args):
    result = subprocess.run(list(map(str, args)), text=True, capture_output=True, timeout=15)
    if result.returncode:
        raise MigrationError(f"{args[0]} failed; no migration can be completed")
    return result.stdout.strip()


def setting(schema, key="command"):
    value = command(["gsettings", "get", schema, key])
    try:
        return ast.literal_eval(value.removeprefix("@as "))
    except (ValueError, SyntaxError) as error:
        raise MigrationError("could not parse GNOME shortcut settings") from error


def set_command(schema, value):
    command(["gsettings", "set", schema, "command", json.dumps(value, ensure_ascii=False)])
    if setting(schema) != value:
        raise MigrationError("GNOME did not save the shortcut command")


def desktop_prefixes(binary):
    # Match the quoting used by render-desktop.py, plus older unquoted paths.
    escaped = "".join("\\" + c if c in '\\"`$' else c for c in binary)
    escaped = escaped.replace("%", "%%").replace("\\", "\\\\")
    tokens = ['"' + escaped + '"']
    if not any(c.isspace() or c in '\\"`$%\'' for c in binary):
        tokens.append(binary)
    return [prefix + token for prefix in ("/usr/bin/env ", "") for token in tokens]


def retarget_desktop(text, binaries, path):
    prefixes = sorted({p for binary in binaries for p in desktop_prefixes(binary)},
                      key=len, reverse=True)
    result = []
    target = '/usr/bin/env "' + str(PACKAGE_BINARY) + '"'
    for line in text.splitlines(keepends=True):
        if line.startswith("TryExec="):
            value = line[8:].rstrip("\r\n")
            if value not in binaries:
                raise MigrationError(f"unrecognized TryExec in {path}")
            result.append("TryExec=" + str(PACKAGE_BINARY) + ("\n" if line.endswith("\n") else ""))
        elif line.startswith("Exec="):
            body = line[5:].rstrip("\r\n")
            for prefix in prefixes:
                if body == prefix or body.startswith(prefix + " "):
                    suffix = body[len(prefix):]
                    result.append("Exec=" + target + suffix + ("\n" if line.endswith("\n") else ""))
                    break
            else:
                raise MigrationError(f"unrecognized launcher command in {path}; identify a checkout with --source-checkout or a user install with --source-bin")
        else:
            result.append(line)
    return "".join(result)


def plan(profile, source_bins=(), skip_shortcuts=False, source_checkouts=()):
    sources = {profile.bin / "ntikuma", profile.home / ".local/bin/ntikuma"}
    sources.update(absolute(p) for p in source_bins)
    if any(p.name != "ntikuma" or p == PACKAGE_BINARY for p in sources):
        raise MigrationError("source binaries must be user-installed paths named ntikuma")
    checkouts = {ROOT, *(absolute(p) for p in source_checkouts)}
    binaries = {str(p) for p in sources | {p / "bin/ntikuma" for p in checkouts} | {PACKAGE_BINARY}}
    binaries.add("ntikuma")
    changes, shortcuts, notes = [], [], []

    def edit(path, data, summary, allow_link=False):
        before = snapshot(path, allow_link)
        if before is None:
            return
        item = {"path": path, "before": before, "after": data, "summary": summary}
        if before != after_snapshot(item):
            changes.append(item)

    for path in sorted(sources):
        edit(path, None, "Retire source executable", allow_link=True)
    for path in (profile.data / f"applications/{APP_ID}.desktop",
                 profile.config / f"autostart/{APP_ID}.desktop"):
        if snapshot(path) is not None:
            data = retarget_desktop(read_text(path), binaries, path).encode()
            edit(path, data, "Retarget existing desktop entry")
    if snapshot(profile.settings) is not None:
        settings = json.loads(read_text(profile.settings))
        if not isinstance(settings, dict) or not isinstance(settings.get("voice", {}), dict):
            raise MigrationError("preferences must contain JSON objects")
        voice = settings.get("voice", {})
        helper = voice.get("nativeHelper", "")
        if not isinstance(helper, str):
            raise MigrationError("voice.nativeHelper must be a string")
        old_helpers = {p.parent / "ntikuma-asr" for p in sources}
        old_helpers.update(p / "bin/ntikuma-asr" for p in checkouts)
        old_helpers.add(profile.home / ".local/lib/ntikuma-asr/ntikuma-asr")
        expanded = str(profile.home) + helper[1:] if helper.startswith("~/") else helper
        if helper and Path(expanded) in old_helpers:
            voice["nativeHelper"] = str(PACKAGE_HELPER)
            edit(profile.settings, (json.dumps(settings, indent=2, ensure_ascii=False) + "\n").encode(),
                 "Use packaged speech helper")
        elif helper and helper != str(PACKAGE_HELPER):
            notes.append("Preserve custom speech runtime; check it in Preferences after migration.")
    if skip_shortcuts:
        notes.append("GNOME shortcuts explicitly skipped; any old shortcut commands remain unchanged.")
    else:
        if not shutil.which("gsettings"):
            raise MigrationError("gsettings is unavailable; use --skip-shortcuts only if you do not use GNOME shortcuts")
        paths = setting(MEDIA_KEYS, "custom-keybindings")
        if not isinstance(paths, list) or any(not isinstance(p, str) or not p.startswith("/") or not p.endswith("/") for p in paths):
            raise MigrationError("invalid GNOME custom shortcut paths")
        for path in dict.fromkeys(paths):
            schema = BINDING + ":" + path
            before = setting(schema)
            if not isinstance(before, str):
                raise MigrationError("invalid GNOME shortcut command")
            try:
                words = shlex.split(before)
            except ValueError:
                continue  # An unrelated malformed shortcut is not ours to edit.
            if words[:1] == ["/usr/bin/env"]:
                words = words[1:]
            if len(words) == 2 and words[0] in binaries and words[1] in ACTIONS:
                after = shlex.join([str(PACKAGE_BINARY), words[1]])
                if before != after:
                    shortcuts.append({"schema": schema, "before": before, "after": after})
            elif any(str(p) in text or shlex.quote(str(p)) in text
                     for p in sources for text in [before, *words]):
                raise MigrationError("an old Ntikuma shortcut uses a wrapper or extra arguments; update it manually before migration")
    return {"files": changes, "shortcuts": shortcuts, "notes": notes}


def ensure_stopped():
    for process in Path("/proc").iterdir():
        if not process.name.isdigit():
            continue
        try:
            if process.stat().st_uid == os.geteuid():
                name = os.readlink(process / "exe").removesuffix(" (deleted)")
                if Path(name).name == "ntikuma":
                    raise MigrationError("quit Ntikuma before applying or restoring migration; no running app is stopped automatically")
        except (FileNotFoundError, PermissionError, ProcessLookupError):
            continue


def check_package():
    if command(["dpkg-query", "-W", "-f=${Status}", "ntikuma"]) != "install ok installed":
        raise MigrationError("install the Ntikuma .deb before applying migration")
    for path in (PACKAGE_BINARY, PACKAGE_HELPER):
        if not path.is_file() or not os.access(path, os.X_OK):
            raise MigrationError(f"packaged executable missing: {path}")
    if not command([PACKAGE_BINARY, "version"]).startswith("ntikuma "):
        raise MigrationError("packaged application did not report its version")
    version = command([PACKAGE_HELPER, "--version"])
    if "ntikuma-asr protocol=1 sherpa=2.0.0" not in version or "cohere-transcribe" not in version:
        raise MigrationError("packaged speech runtime is incompatible")


def atomic_write(path, data, mode):
    fd, temporary = tempfile.mkstemp(prefix=".ntikuma-migration-", dir=path.parent)
    try:
        with os.fdopen(fd, "wb") as stream:
            stream.write(data)
            os.fchmod(stream.fileno(), mode)
            stream.flush()
            os.fsync(stream.fileno())
        os.replace(temporary, path)
        sync_directory(path.parent)
    finally:
        Path(temporary).unlink(missing_ok=True)


def sync_directory(path):
    fd = os.open(path, os.O_RDONLY | os.O_DIRECTORY)
    try:
        os.fsync(fd)
    finally:
        os.close(fd)


def sync_file(path):
    with path.open("rb") as stream:
        os.fsync(stream.fileno())


def save_manifest(directory, manifest):
    atomic_write(directory / "manifest.json", (json.dumps(manifest, indent=2) + "\n").encode(), 0o600)


def backup_database(source, destination):
    snapshot(source)  # Refuse symlinks and nonregular database files.
    destination.touch(mode=0o600, exist_ok=False)
    deadline = time.monotonic() + 15

    def progress(*_):
        if time.monotonic() > deadline:
            raise MigrationError("database backup timed out; no migration changes were made")

    with closing(sqlite3.connect(source.as_uri() + "?mode=ro", uri=True)) as original:
        with closing(sqlite3.connect(destination)) as copy:
            original.backup(copy, pages=128, progress=progress, sleep=0.05)
            if copy.execute("PRAGMA quick_check").fetchone() != ("ok",):
                raise MigrationError("database backup failed its integrity check")


def make_backup(profile, changes):
    directory = Path(tempfile.mkdtemp(prefix=datetime.now(timezone.utc).strftime("%Y%m%dT%H%M%SZ-"),
                                      dir=profile.backups))
    sync_directory(profile.backups)
    manifest = {"version": 1, "uid": os.geteuid(), "profile": profile.identity(),
                "state": "prepared", "files": [], "shortcuts": changes["shortcuts"]}
    for index, item in enumerate(changes["files"]):
        path = item["path"]
        if snapshot(path, allow_link=True) != item["before"]:
            raise MigrationError(f"file changed since preview: {path}")
        entry = {"path": str(path), "before": item["before"], "after": after_snapshot(item)}
        if item["before"]["kind"] == "file":
            name = f"file-{index}"
            shutil.copyfile(path, directory / name)
            (directory / name).chmod(0o600)
            if snapshot(directory / name)["sha256"] != item["before"]["sha256"]:
                raise MigrationError("backup did not match its source; no migration changes were made")
            sync_file(directory / name)
            entry["backup"] = name
        manifest["files"].append(entry)
    if snapshot(profile.settings) is not None:
        shutil.copyfile(profile.settings, directory / "preferences.json")
        (directory / "preferences.json").chmod(0o600)
        sync_file(directory / "preferences.json")
    if profile.database.exists() or profile.database.is_symlink():
        backup_database(profile.database, directory / "chats.sqlite3")
        sync_file(directory / "chats.sqlite3")
    save_manifest(directory, manifest)
    return directory, manifest


def verify_state(manifest):
    for item in manifest["files"]:
        if snapshot(Path(item["path"]), allow_link=True) != item["before"]:
            raise MigrationError(f"file changed during migration: {item['path']}")
    for item in manifest["shortcuts"]:
        if setting(item["schema"]) != item["before"]:
            raise MigrationError("a shortcut changed during migration")
        if command(["gsettings", "writable", item["schema"], "command"]) != "true":
            raise MigrationError("a GNOME shortcut is locked by policy")


def restore(directory, manifest):
    # Validate every conflict and backup before restoring anything. Never roll
    # back new chats or settings/shortcuts edited after the migration.
    for item in manifest["files"]:
        if snapshot(Path(item["path"]), allow_link=True) not in (item["before"], item["after"]):
            raise MigrationError(f"restore would overwrite a later change: {item['path']}")
        if item["before"]["kind"] == "file":
            name = item["backup"]
            if Path(name).name != name:
                raise MigrationError("invalid backup filename")
            saved = snapshot(directory / name)
            if saved is None or saved["sha256"] != item["before"]["sha256"]:
                raise MigrationError("backup file is missing or changed")
    for item in manifest["shortcuts"]:
        if setting(item["schema"]) not in (item["before"], item["after"]):
            raise MigrationError("restore would overwrite a later shortcut change")
    for item in reversed(manifest["shortcuts"]):
        if setting(item["schema"]) != item["before"]:
            set_command(item["schema"], item["before"])
    for item in reversed(manifest["files"]):
        path, before = Path(item["path"]), item["before"]
        if snapshot(path, allow_link=True) == before:
            continue
        if before["kind"] == "link":
            path.symlink_to(before["target"])
            sync_directory(path.parent)
        else:
            atomic_write(path, (directory / item["backup"]).read_bytes(), before["mode"])
    manifest["state"] = "restored"
    save_manifest(directory, manifest)


def private_directory(path, create=True):
    if create:
        path.mkdir(mode=0o700, parents=True, exist_ok=True)
    info = path.lstat()
    if not stat.S_ISDIR(info.st_mode) or info.st_uid != os.geteuid() or info.st_mode & 0o077:
        raise MigrationError(f"backup directory must be owned by you with mode 0700: {path}")


@contextmanager
def migration_lock(profile):
    private_directory(profile.backups)
    fd = os.open(profile.backups / ".lock", os.O_CREAT | os.O_RDWR | os.O_NOFOLLOW, 0o600)
    with os.fdopen(fd, "r+") as lock:
        info = os.fstat(lock.fileno())
        if not stat.S_ISREG(info.st_mode) or info.st_uid != os.geteuid():
            raise MigrationError("migration lock is not a regular file owned by you")
        os.fchmod(lock.fileno(), 0o600)
        fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
        yield


def apply(profile, changes):
    if not changes["files"] and not changes["shortcuts"]:
        return None
    ensure_stopped()
    check_package()
    with migration_lock(profile):
        ensure_stopped()
        directory, manifest = make_backup(profile, changes)
        print(f"Private backup: {directory}", flush=True)
        verify_state(manifest)
        try:
            for change in changes["files"]:
                if change["after"] is None:
                    change["path"].unlink()
                    sync_directory(change["path"].parent)
                else:
                    atomic_write(change["path"], change["after"], change["before"]["mode"])
            for item in changes["shortcuts"]:
                set_command(item["schema"], item["after"])
            manifest["state"] = "complete"
            save_manifest(directory, manifest)
        except (Exception, KeyboardInterrupt):
            try:
                restore(directory, manifest)
            except Exception as error:
                raise MigrationError(f"migration interrupted; restore from {directory} after resolving the conflict") from error
            raise
        return directory


def restore_backup(profile, directory):
    ensure_stopped()
    directory = absolute(directory)
    private_directory(directory, create=False)
    snapshot(directory / "manifest.json")
    manifest = json.loads(read_text(directory / "manifest.json"))
    if manifest.get("version") != 1 or manifest.get("uid") != os.geteuid() or manifest.get("profile") != profile.identity():
        raise MigrationError("backup belongs to another profile or an unsupported migration version")
    with migration_lock(profile):
        ensure_stopped()
        restore(directory, manifest)


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    mode = parser.add_mutually_exclusive_group()
    mode.add_argument("--apply", action="store_true", help="apply the preview after installing the .deb and quitting Ntikuma")
    mode.add_argument("--restore", type=Path, metavar="BACKUP", help="undo migration changes, preserving later chat data")
    parser.add_argument("--source-bin", action="append", default=[], type=Path,
                        help="additional user-installed executable to retire, named ntikuma")
    parser.add_argument("--source-checkout", action="append", default=[], type=Path,
                        help="recognize binaries and helpers from this checkout without deleting them")
    parser.add_argument("--skip-shortcuts", action="store_true", help="leave GNOME shortcuts unchanged")
    args = parser.parse_args(argv)
    try:
        if os.geteuid() == 0:
            raise MigrationError("run this command as your desktop user, without sudo")
        profile = Profile()
        if args.restore:
            restore_backup(profile, args.restore)
            print("Migration changes restored. Chats and model files were left in place.")
            return
        changes = plan(profile, args.source_bin, args.skip_shortcuts, args.source_checkout)
        for item in changes["files"]:
            print(f"{item['summary']}: {item['path']}")
        for item in changes["shortcuts"]:
            print(f"Retarget shortcut: {item['schema'].split(':', 1)[1]}")
        for note in changes["notes"]:
            print("Note: " + note)
        print("Chats stay in place; apply backs up SQLite and preferences privately. Models and keyring are not copied or changed.")
        if args.apply:
            backup = apply(profile, changes)
            print("Migration complete. Run /usr/bin/ntikuma show." if backup else "No migration changes needed.")
        else:
            print("Preview only; no files or settings changed. Install the .deb, quit Ntikuma, then repeat with --apply.")
    except (MigrationError, OSError, ValueError, sqlite3.Error, subprocess.TimeoutExpired) as error:
        parser.exit(1, f"Migration stopped: {error}\n")


if __name__ == "__main__":
    main()
