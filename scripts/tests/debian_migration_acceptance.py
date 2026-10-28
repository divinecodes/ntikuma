#!/usr/bin/env python3
"""Exercise installed migration in a disposable container, never on the host.

Requires root in a container, the package's runtime dependencies, GNOME schemas,
and a normal ntikuma-test user. Uses a private GSettings keyfile backend.
"""
import argparse
import ast
from contextlib import closing
import hashlib
import json
import os
from pathlib import Path
import pwd
import shlex
import shutil
import sqlite3
import subprocess

APP_ID = "dev.divinetettey.ntikuma"
MEDIA_KEYS = "org.gnome.settings-daemon.plugins.media-keys"
SCHEMA = MEDIA_KEYS + ".custom-keybinding:"


def run(args, env=None):
    return subprocess.run(list(map(str, args)), env=env, check=True, text=True,
                          capture_output=True, timeout=120).stdout.strip()


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--package", required=True, type=Path)
    parser.add_argument("--schema", required=True, type=Path)
    args = parser.parse_args()
    if os.geteuid() != 0 or not any(Path(p).exists() for p in ("/.dockerenv", "/run/.containerenv")):
        parser.error("run only as root in a disposable container")
    home = Path(pwd.getpwnam("ntikuma-test").pw_dir)
    env = {**os.environ, "HOME": str(home), "XDG_CONFIG_HOME": str(home / ".config"),
           "XDG_DATA_HOME": str(home / ".local/share"), "XDG_CACHE_HOME": str(home / ".cache"),
           "NTIKUMA_BIN_DIR": str(home / ".local/bin"), "GSETTINGS_BACKEND": "keyfile"}

    def user(*command):
        return run(["runuser", "-u", "ntikuma-test", "--", *command], env)

    run(["apt-get", "install", "-y", "--no-install-recommends", args.package.resolve()])
    binary = home / ".local/bin/ntikuma"
    launcher = home / f".local/share/applications/{APP_ID}.desktop"
    autostart = home / f".config/autostart/{APP_ID}.desktop"
    config = home / ".config/ntikuma/config.json"
    database = home / ".local/share/ntikuma/ntikuma.db"
    old_helper = home / ".local/lib/ntikuma-asr/ntikuma-asr"
    model = home / "models/keep.onnx"
    keyring = home / ".local/share/keyrings/login.keyring"
    for path in (binary, launcher, autostart, config, database, old_helper, model, keyring):
        path.parent.mkdir(parents=True, exist_ok=True)
    shutil.copy2("/usr/bin/ntikuma", binary)
    launcher.write_text(Path(f"/usr/share/applications/{APP_ID}.desktop").read_text().replace("/usr/bin/ntikuma", str(binary)))
    autostart.write_text(Path("/usr/share/ntikuma/autostart.desktop").read_text().replace("/usr/bin/ntikuma", str(binary)) + "Hidden=true\n")
    settings = {"keepChatsDays": 0, "voice": {"engine": "parakeet", "nativeHelper": str(old_helper),
                                             "parakeet": {"modelDir": str(model.parent), "threads": 3}}}
    config.write_text(json.dumps(settings))
    for path in (old_helper, model, keyring):
        path.write_text("preserve fixture data")
    with closing(sqlite3.connect(database)) as db:
        db.execute("PRAGMA journal_mode=WAL")
        db.execute("PRAGMA wal_autocheckpoint=0")
        db.executescript(args.schema.read_text())
        db.execute("ALTER TABLE messages ADD COLUMN meta TEXT")
        db.execute("PRAGMA user_version=2")
        db.execute("INSERT INTO chats VALUES ('one','Keep this chat',1,1)")
        db.execute("INSERT INTO messages (id,chat_id,role,content,created_at) VALUES ('answer','one','assistant','Keep this answer',1)")
        db.commit()
        run(["chown", "-R", "ntikuma-test:ntikuma-test", home])
        paths = ["/org/gnome/settings-daemon/plugins/media-keys/custom-bindings/" + name + "/"
                 for name in ("ntikuma-toggle", "ntikuma-voice", "other")]
        user("gsettings", "set", MEDIA_KEYS, "custom-keybindings", repr(paths))
        commands = [shlex.join([str(binary), "toggle"]), shlex.join([str(binary), "voice"]), "other-app --keep"]
        for path, command in zip(paths, commands):
            user("gsettings", "set", SCHEMA + path, "command", json.dumps(command))
            user("gsettings", "set", SCHEMA + path, "binding", "'<Super>space'")
        wm = "org.gnome.desktop.wm.keybindings"
        user("gsettings", "set", wm, "switch-input-source", "['<Super>space']")
        originals = {path: path.read_bytes() for path in (binary, launcher, autostart, config, old_helper, model, keyring)}
        database_hash = hashlib.sha256(database.read_bytes()).hexdigest()
        preview = user("/usr/bin/ntikuma-migrate-deb")
        assert "Preview only" in preview
        assert all(path.read_bytes() == value for path, value in originals.items())
        backups = home / ".local/share/ntikuma/migration-backups"
        assert not backups.exists()
        applied = user("/usr/bin/ntikuma-migrate-deb", "--apply")
        assert "Migration complete" in applied
        backup, = [p for p in backups.iterdir() if p.is_dir()]
        assert not binary.exists()
        assert '"/usr/bin/ntikuma" show' in launcher.read_text()
        assert '"/usr/bin/ntikuma" start' in autostart.read_text()
        assert "Hidden=true" in autostart.read_text()
        expected = json.loads(json.dumps(settings))
        expected["voice"]["nativeHelper"] = "/usr/lib/ntikuma/ntikuma-asr"
        assert json.loads(config.read_text()) == expected
        assert ast.literal_eval(user("gsettings", "get", SCHEMA + paths[0], "command")) == "/usr/bin/ntikuma toggle"
        assert ast.literal_eval(user("gsettings", "get", SCHEMA + paths[1], "command")) == "/usr/bin/ntikuma voice"
        assert ast.literal_eval(user("gsettings", "get", SCHEMA + paths[2], "command")) == commands[2]
        assert ast.literal_eval(user("gsettings", "get", SCHEMA + paths[0], "binding")) == "<Super>space"
        assert ast.literal_eval(user("gsettings", "get", MEDIA_KEYS, "custom-keybindings")) == paths
        assert ast.literal_eval(user("gsettings", "get", wm, "switch-input-source")) == ["<Super>space"]
        assert hashlib.sha256(database.read_bytes()).hexdigest() == database_hash
        with closing(sqlite3.connect(backup / "chats.sqlite3")) as saved:
            assert saved.execute("SELECT title FROM chats").fetchall() == [("Keep this chat",)]
            assert saved.execute("SELECT content FROM messages").fetchall() == [("Keep this answer",)]
            assert saved.execute("PRAGMA user_version").fetchone() == (2,)
        for path in backup.rglob("*"):
            assert path.stat().st_uid == pwd.getpwnam("ntikuma-test").pw_uid
            assert path.stat().st_mode & 0o777 == (0o700 if path.is_dir() else 0o600)
        assert backup.stat().st_mode & 0o777 == 0o700
        assert "No migration changes needed" in user("/usr/bin/ntikuma-migrate-deb", "--apply")
        db.execute("INSERT INTO chats VALUES ('two','Keep newer chat',2,2)")
        db.commit()
        user("/usr/bin/ntikuma-migrate-deb", "--restore", backup)
        assert all(path.read_bytes() == value for path, value in originals.items())
        assert db.execute("SELECT count(*) FROM chats").fetchone() == (2,)
        assert ast.literal_eval(user("gsettings", "get", SCHEMA + paths[0], "command")) == commands[0]
    print(json.dumps({"installed_preview_apply_restore": True, "consistent_wal_backup": True,
                      "newer_chats_preserved_on_restore": True, "idempotent_apply": True,
                      "real_gsettings_keyfile_backend": True, "shortcut_and_autostart_choices_preserved": True,
                      "models_keyring_source_runtime_preserved": True, "physical_desktop_acceptance": False}))


if __name__ == "__main__":
    main()
