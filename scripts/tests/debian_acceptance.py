#!/usr/bin/env python3
"""Install, launch, upgrade and remove two packages inside a disposable container.

Requires root in a container with the package dependencies, Xvfb, xauth,
dbus-run-session and a normal ntikuma-test user. Never run on the host.
"""
import argparse
import hashlib
import json
import os
from pathlib import Path
import pwd
import sqlite3
import subprocess


def run(args, capture=False, env=None):
    return subprocess.run(args, check=True, text=True, env=env,
                          stdout=subprocess.PIPE if capture else None, timeout=120).stdout


def gui_smoke(home):
    # Disable WebKit's nested sandbox only in this network-isolated Docker
    # fixture. Production application settings are unchanged.
    env = {**os.environ, "HOME": str(home), "XDG_CONFIG_HOME": str(home / ".config"),
           "XDG_DATA_HOME": str(home / ".local/share"), "XDG_CACHE_HOME": str(home / ".cache"),
           "XDG_RUNTIME_DIR": str(home / "runtime"), "GDK_BACKEND": "x11", "GSK_RENDERER": "cairo",
           "WEBKIT_DISABLE_SANDBOX_THIS_IS_DANGEROUS": "1"}
    script = home / "launch.py"
    script.write_text('''import subprocess,time
with open("/tmp/ntikuma-gui.log", "w") as log:
 p=subprocess.Popen(["/usr/bin/ntikuma","show"],stdout=log,stderr=log)
 try:
  time.sleep(3)
  assert p.poll() is None,"app exited during launch"
  subprocess.run(["/usr/bin/ntikuma","show"],check=True,timeout=15)
  time.sleep(2)
  assert p.poll() is None,"app exited after show"
  subprocess.run(["/usr/bin/ntikuma","quit"],check=True,timeout=15)
  assert p.wait(timeout=20)==0,"app failed to quit cleanly"
 finally:
  if p.poll() is None:p.terminate();p.wait(timeout=10)
''')
    run(["runuser", "-u", "ntikuma-test", "--", "dbus-run-session", "--", "xvfb-run", "-a",
         "python3", str(script)], env=env)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--initial", type=Path, required=True)
    parser.add_argument("--upgrade", type=Path, required=True)
    parser.add_argument("--schema", type=Path, required=True)
    args = parser.parse_args()
    if os.geteuid() != 0 or not any(Path(p).exists() for p in ["/.dockerenv", "/run/.containerenv"]):
        parser.error("this acceptance check runs only as root in a disposable container")
    home = Path(pwd.getpwnam("ntikuma-test").pw_dir)
    config = home / ".config/ntikuma/config.json"
    database = home / ".local/share/ntikuma/ntikuma.db"
    model = home / "models/keep-model.txt"
    autostart = home / ".config/autostart/dev.divinetettey.ntikuma.desktop"
    keyring = home / ".local/share/keyrings/login.keyring"
    files = {config: '{"attachClipboard":false,"attachSelection":false,"offerScreenshot":false}',
             model: "fixture model data", autostart: "[Desktop Entry]\nHidden=true\nExec=/custom/ntikuma start\n",
             keyring: "fixture keyring sentinel"}
    for path, text in files.items():
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text(text)
    database.parent.mkdir(parents=True, exist_ok=True)
    with sqlite3.connect(database) as db:
        db.executescript(args.schema.read_text())
        db.execute("ALTER TABLE messages ADD COLUMN meta TEXT")
        db.execute("PRAGMA user_version=2")
        db.execute("INSERT INTO chats VALUES ('fixture-chat','Keep this chat',1,1)")
        db.execute("INSERT INTO messages (id,chat_id,role,content,created_at) VALUES ('fixture-message','fixture-chat','assistant','Keep this answer',1)")
    (home / "runtime").mkdir(mode=0o700, exist_ok=True)
    run(["chown", "-R", "ntikuma-test:ntikuma-test", str(home)])
    expected = {str(path): hashlib.sha256(path.read_bytes()).hexdigest() for path in files}

    def preserved():
        assert expected == {str(path): hashlib.sha256(path.read_bytes()).hexdigest() for path in files}
        with sqlite3.connect(database) as db:
            assert db.execute("SELECT title FROM chats").fetchall() == [("Keep this chat",)]
            assert db.execute("SELECT content FROM messages").fetchall() == [("Keep this answer",)]

    versions = []
    for package in [args.initial.resolve(), args.upgrade.resolve()]:
        version = run(["dpkg-deb", "-f", str(package), "Version"], capture=True).strip()
        run(["apt-get", "install", "-y", "--no-install-recommends", str(package)])
        assert run(["/usr/bin/ntikuma", "version"], capture=True).strip() == "ntikuma " + version
        helper = "/usr/lib/ntikuma/ntikuma-asr"
        assert "cohere-transcribe" in run([helper, "--version"], capture=True)
        assert run(["patchelf", "--print-rpath", helper], capture=True).strip() == "$ORIGIN/asr-lib"
        run(["desktop-file-validate", "/usr/share/applications/dev.divinetettey.ntikuma.desktop"])
        assert not Path("/etc/xdg/autostart/dev.divinetettey.ntikuma.desktop").exists()
        gui_smoke(home)
        preserved()
        versions.append(version)
    run(["apt-get", "remove", "-y", "ntikuma"])
    assert not Path("/usr/bin/ntikuma").exists()
    assert not Path("/usr/lib/ntikuma/ntikuma-asr").exists()
    preserved()
    run(["dpkg", "--purge", "ntikuma"])
    preserved()
    result = {"versions": versions, "install_upgrade_remove_purge": True,
              "native_launch_show_quit": True, "bundled_helper": True, "profile_preserved": True,
              "physical_desktop_acceptance": False,
              "fixture_limits": "Xvfb and private D-Bus; WebKit nested sandbox disabled only in the isolated test environment"}
    Path("/tmp/ntikuma-debian-acceptance.json").write_text(json.dumps(result, indent=2) + "\n")
    print(json.dumps(result))


if __name__ == "__main__":
    main()
