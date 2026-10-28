import ast
import configparser
import ctypes
import hashlib
import json
import os
from pathlib import Path
import shlex
import shutil
import signal
import subprocess
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[2]
APP_ID = "dev.divinetettey.ntikuma"


def executable(path, content):
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(content)
    path.chmod(0o755)


class InstallTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.base = Path(self.temp.name)
        self.profile = self.base / "profile space 'quote %percent $cash"
        self.bin = self.profile / "bin"
        self.data = self.profile / "data"
        self.config = self.profile / "config"
        self.cache = self.profile / "cache"
        self.fakebin = self.base / "tools"
        self.log = self.base / "commands.log"
        self.settings = self.base / "gsettings.json"
        self.env = {
            **os.environ,
            "PATH": str(self.fakebin) + os.pathsep + os.environ["PATH"],
            "NTIKUMA_BIN_DIR": str(self.bin), "XDG_DATA_HOME": str(self.data),
            "XDG_CONFIG_HOME": str(self.config), "XDG_CACHE_HOME": str(self.cache),
            "NTIKUMA_TEST_LOG": str(self.log), "NTIKUMA_SETTINGS_FILE": str(self.settings),
        }
        self.source = self.base / "source binary"
        self.program = '''#!/usr/bin/env python3
import os, signal, sys
with open(os.environ["NTIKUMA_TEST_LOG"], "a") as log:
    log.write(f"app {sys.argv[1]} {os.getpid()}\\n")
if sys.argv[1] == "start":
    if os.environ.get("NTIKUMA_TEST_START_FAIL"):
        sys.exit(1)
    if os.environ.get("NTIKUMA_TEST_FORWARD_ONCE"):
        from pathlib import Path
        marker = Path(os.environ["NTIKUMA_TEST_FORWARD_ONCE"])
        if not marker.exists():
            marker.touch()
            sys.exit(0)
    signal.signal(signal.SIGTERM, lambda *_: sys.exit(0))
    while True:
        signal.pause()
'''
        executable(self.source, self.program)
        for tool in ["update-desktop-database", "gtk-update-icon-cache"]:
            executable(self.fakebin / tool, '#!/bin/sh\nprintf "%s\\n" "$0 $*" >> "$NTIKUMA_TEST_LOG"\n')
        executable(self.fakebin / "gsettings", '''#!/usr/bin/env python3
import ast, json, os, sys
from pathlib import Path
p = Path(os.environ["NTIKUMA_SETTINGS_FILE"])
state = json.loads(p.read_text()) if p.exists() else {}
op, schema, *rest = sys.argv[1:]
if ".custom-keybindings" in schema or (rest and rest[0] == "bindings"):
    sys.exit("Invalid custom shortcut schema/key")
if op == "get":
    print(repr(state.get(schema + "|" + rest[0], [])))
elif op == "set":
    value = rest[1]
    state[schema + "|" + rest[0]] = ast.literal_eval(value) if value.startswith(("[", "'", '"')) else value
elif op == "reset-recursively":
    state = {k:v for k,v in state.items() if not k.startswith(schema + "|")}
else:
    sys.exit("Unexpected gsettings operation")
p.write_text(json.dumps(state))
''')
        self.addCleanup(self.stop_fake_apps)

    def stop_fake_apps(self):
        if not self.log.exists():
            return
        for line in self.log.read_text().splitlines():
            if line.startswith("app start "):
                try:
                    pid = int(line.split()[-1])
                    command = Path(f"/proc/{pid}/cmdline").read_bytes()
                    if os.fsencode(self.bin / "ntikuma") in command:
                        os.kill(pid, signal.SIGTERM)
                except (ProcessLookupError, FileNotFoundError):
                    pass

    def run_script(self, name, *args, env=None, repo=ROOT):
        return subprocess.run([str(repo / "scripts" / name), *map(str, args)],
                              cwd=self.base, env=env or self.env, text=True,
                              capture_output=True, timeout=20)

    def install(self, *args, env=None):
        result = self.run_script("install.sh", *args, self.source, env=env)
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        return result

    def entry(self, path):
        parser = configparser.ConfigParser(interpolation=None)
        parser.read(path)
        return parser

    def test_install_entries_and_icon_in_xdg_paths_with_spaces(self):
        self.install("--no-start", "--no-shortcuts")
        launcher = self.data / "applications" / f"{APP_ID}.desktop"
        autostart = self.config / "autostart" / f"{APP_ID}.desktop"
        entry = self.entry(launcher)
        self.assertEqual(entry["Desktop Entry"]["StartupWMClass"], APP_ID)
        self.assertEqual(entry["Desktop Entry"]["Icon"], APP_ID)
        self.assertTrue(entry["Desktop Entry"]["Exec"].endswith(" show"))
        self.assertTrue(self.entry(autostart)["Desktop Entry"]["Exec"].endswith(" start"))
        self.assertEqual(self.entry(autostart)["Desktop Entry"]["NoDisplay"], "true")
        self.assertEqual((self.data / "icons/hicolor/scalable/apps" / f"{APP_ID}.svg").read_bytes(), (ROOT / "assets/ntikuma.svg").read_bytes())
        self.assertTrue(os.access(self.bin / "ntikuma", os.X_OK))
        subprocess.run(["desktop-file-validate", str(launcher), str(autostart)], check=True, capture_output=True)
        # Ask GLib to parse the actual desktop entry without launching it.
        gio = ctypes.CDLL("libgio-2.0.so.0")
        gio.g_desktop_app_info_new_from_filename.argtypes = [ctypes.c_char_p]
        gio.g_desktop_app_info_new_from_filename.restype = ctypes.c_void_p
        gio.g_app_info_get_commandline.argtypes = [ctypes.c_void_p]
        gio.g_app_info_get_commandline.restype = ctypes.c_char_p
        gio.g_object_unref.argtypes = [ctypes.c_void_p]
        app = gio.g_desktop_app_info_new_from_filename(os.fsencode(launcher))
        self.assertTrue(app, "GLib rejected the launcher")
        try:
            glib = ctypes.CDLL("libglib-2.0.so.0")
            argv = ctypes.POINTER(ctypes.c_char_p)()
            argc = ctypes.c_int()
            error = ctypes.c_void_p()
            glib.g_shell_parse_argv.argtypes = [ctypes.c_char_p, ctypes.POINTER(ctypes.c_int), ctypes.POINTER(ctypes.POINTER(ctypes.c_char_p)), ctypes.POINTER(ctypes.c_void_p)]
            command = gio.g_app_info_get_commandline(app).replace(b"%%", b"%")
            self.assertTrue(glib.g_shell_parse_argv(command, ctypes.byref(argc), ctypes.byref(argv), ctypes.byref(error)))
            try:
                self.assertEqual([argv[i].decode() for i in range(argc.value)], ["/usr/bin/env", str(self.bin / "ntikuma"), "show"])
            finally:
                glib.g_strfreev(argv)
        finally:
            gio.g_object_unref(app)
        self.assertFalse(self.settings.exists())

    def test_upgrade_preserves_data_and_can_disable_autostart(self):
        self.install("--no-start", "--no-shortcuts")
        cfg = self.config / "ntikuma/config.json"
        db = self.data / "ntikuma/ntikuma.db"
        for path in [cfg, db]:
            path.parent.mkdir(parents=True, exist_ok=True)
            path.write_text("keep this data")
        executable(self.source, self.program + "\n# version two\n")
        self.install("--no-start", "--no-shortcuts", "--no-autostart")
        self.assertEqual((self.bin / "ntikuma").read_text(), self.source.read_text())
        self.assertFalse((self.config / "autostart" / f"{APP_ID}.desktop").exists())
        self.assertEqual(cfg.read_text(), "keep this data")
        self.assertEqual(db.read_text(), "keep this data")
        self.assertFalse(list(self.bin.glob(".ntikuma-install.*")))

    def test_missing_binary_and_invalid_desktop_path_fail_before_quit(self):
        self.source.unlink()
        result = self.run_script("install.sh", "--no-start", "--no-shortcuts", self.source)
        self.assertNotEqual(result.returncode, 0)
        self.assertFalse(self.data.exists())
        executable(self.source, self.program)
        env = {**self.env, "NTIKUMA_BIN_DIR": str(self.base / "bad=path")}
        result = self.run_script("install.sh", "--no-start", "--no-shortcuts", self.source, env=env)
        self.assertNotEqual(result.returncode, 0)
        self.assertFalse(self.log.exists())

    def test_start_and_upgrade_lock_retry_use_only_fake_processes(self):
        env = {**self.env, "NTIKUMA_TEST_FORWARD_ONCE": str(self.base / "forwarded")}
        self.install("--no-shortcuts", env=env)
        self.assertGreaterEqual(self.log.read_text().count("app start"), 2)
        self.assertTrue((self.cache / "ntikuma/startup.log").exists())

    def test_start_failure_reports_log(self):
        env = {**self.env, "NTIKUMA_TEST_START_FAIL": "1"}
        result = self.run_script("install.sh", "--no-shortcuts", self.source, env=env)
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("startup.log", result.stderr)

    def test_shortcuts_preserve_unrelated_bindings_and_quote_binary(self):
        schema = "org.gnome.settings-daemon.plugins.media-keys"
        wm = "org.gnome.desktop.wm.keybindings"
        unrelated = "/org/gnome/settings-daemon/plugins/media-keys/custom-bindings/other/"
        self.settings.write_text(json.dumps({
            schema + "|custom-keybindings": [unrelated],
            wm + "|switch-input-source": ["<Super>space"],
            wm + "|switch-input-source-backward": ["<Shift><Super>space"],
        }))
        env = {**self.env, "NTIKUMA_BIN": str(self.bin / "ntikuma")}
        for _ in range(2):
            result = self.run_script("install-shortcuts.sh", "--yes", env=env)
            self.assertEqual(result.returncode, 0, result.stderr)
        state = json.loads(self.settings.read_text())
        self.assertEqual(len(state[schema + "|custom-keybindings"]), 3)
        self.assertIn(unrelated, state[schema + "|custom-keybindings"])
        self.assertEqual(state[wm + "|switch-input-source"], ["XF86Keyboard"])
        commands = [v for k, v in state.items() if k.endswith("|command")]
        self.assertEqual(sorted(shlex.split(value) for value in commands), sorted([[str(self.bin / "ntikuma"), "toggle"], [str(self.bin / "ntikuma"), "voice"]]))
        result = self.run_script("install-shortcuts.sh", "--remove", env=env)
        self.assertEqual(result.returncode, 0, result.stderr)
        state = json.loads(self.settings.read_text())
        self.assertEqual(state[schema + "|custom-keybindings"], [unrelated])
        self.assertEqual(state[wm + "|switch-input-source"], ["XF86Keyboard"])

    def test_installer_integrates_shortcuts_with_fake_gsettings(self):
        self.install("--no-start")
        self.assertTrue(self.settings.exists())
        self.assertEqual(len([k for k in json.loads(self.settings.read_text()) if k.endswith("|command")]), 2)

    def test_shortcuts_with_real_gsettings_in_isolated_keyfile_backend(self):
        real = Path("/usr/bin/gsettings")
        if not real.exists():
            self.skipTest("gsettings is not installed")
        env = {**self.env, "GSETTINGS_BACKEND": "keyfile", "NTIKUMA_BIN": str(self.bin / "ntikuma")}
        schemas = subprocess.run([str(real), "list-relocatable-schemas"], env=env,
                                 text=True, capture_output=True, check=True).stdout
        if "org.gnome.settings-daemon.plugins.media-keys.custom-keybinding" not in schemas:
            self.skipTest("GNOME media-key schemas are not installed")
        executable(self.fakebin / "gsettings", '#!/bin/sh\nexec /usr/bin/gsettings "$@"\n')
        result = self.run_script("install-shortcuts.sh", "--yes", env=env)
        self.assertEqual(result.returncode, 0, result.stderr)
        schema = "org.gnome.settings-daemon.plugins.media-keys.custom-keybinding:/org/gnome/settings-daemon/plugins/media-keys/custom-bindings/ntikuma-toggle/"
        value = subprocess.run([str(real), "get", schema, "command"], env=env,
                               text=True, capture_output=True, check=True).stdout
        self.assertEqual(shlex.split(ast.literal_eval(value)), [str(self.bin / "ntikuma"), "toggle"])

    def bootstrap_checkout(self, with_build=True):
        repo = self.base / "checkout"
        repo.mkdir()
        for directory in ["scripts", "assets", "packaging"]:
            shutil.copytree(ROOT / directory, repo / directory, ignore=shutil.ignore_patterns("__pycache__", "tests"))
        for name in ["Taskfile.yml", "go.mod"]:
            shutil.copy2(ROOT / name, repo / name)
        (repo / "frontend").mkdir()
        if with_build:
            for name in ["Taskfile.yml", "linux/Taskfile.yml", "config.yml"]:
                path = repo / "build" / name
                path.parent.mkdir(parents=True, exist_ok=True)
                path.write_text("preserve local " + name)
        return repo

    def test_bootstrap_preserves_existing_local_tooling_and_uses_pinned_cli(self):
        repo = self.bootstrap_checkout()
        executable(self.fakebin / "node", "#!/bin/sh\nexit 0\n")
        executable(self.fakebin / "npm", '#!/bin/sh\necho "npm $*" >> "$NTIKUMA_TEST_LOG"\n')
        cli = '#!/bin/sh\nif [ "$1" = version ]; then echo v3.0.0-beta.28 >&2; else echo "wails $*" >> "$NTIKUMA_TEST_LOG"; fi\n'
        executable(self.fakebin / "wails3", cli)
        executable(self.fakebin / "go", '#!/bin/sh\necho "go $*" >> "$NTIKUMA_TEST_LOG"\n')
        files = [repo / "Taskfile.yml", repo / "build/config.yml", repo / "build/linux/Taskfile.yml"]
        before = [hashlib.sha256(path.read_bytes()).digest() for path in files]
        result = self.run_script("bootstrap.sh", repo=repo)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(before, [hashlib.sha256(path.read_bytes()).digest() for path in files])
        log = self.log.read_text()
        self.assertIn("go mod download", log)
        self.assertIn("npm ci", log)
        self.assertIn("wails doctor", log)
        self.assertNotIn("wails init", log)

    def test_bootstrap_installs_matching_cli_when_existing_version_differs(self):
        repo = self.bootstrap_checkout()
        executable(self.fakebin / "node", "#!/bin/sh\nexit 0\n")
        executable(self.fakebin / "npm", "#!/bin/sh\nexit 0\n")
        executable(self.fakebin / "wails3", "#!/bin/sh\necho old-version\n")
        cli = self.base / "cli-template"
        executable(cli, '#!/bin/sh\nif [ "$1" = version ]; then echo v3.0.0-beta.28; fi\n')
        destination = self.base / "installed tools"
        executable(self.fakebin / "go", '''#!/bin/sh
if [ "$1" = install ]; then
    echo "$*" >> "$NTIKUMA_TEST_LOG"
    mkdir -p "$NTIKUMA_TEST_GOBIN"
    cp "$NTIKUMA_TEST_CLI_TEMPLATE" "$NTIKUMA_TEST_GOBIN/wails3"
elif [ "$1" = env ] && [ "$2" = GOBIN ]; then
    echo "$NTIKUMA_TEST_GOBIN"
fi
''')
        result = self.run_script("bootstrap.sh", repo=repo, env={**self.env,
            "NTIKUMA_TEST_GOBIN": str(destination), "NTIKUMA_TEST_CLI_TEMPLATE": str(cli)})
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertTrue((destination / "wails3").exists())
        self.assertIn("github.com/wailsapp/wails/v3/cmd/wails3@v3.0.0-beta.28", self.log.read_text())

    def test_bootstrap_regenerates_ignored_build_directory_without_touching_source(self):
        real_cli = shutil.which("wails3", path=os.environ["PATH"])
        if not real_cli:
            self.skipTest("Wails CLI is not installed")
        version = subprocess.run([real_cli, "version"], text=True, capture_output=True, check=True)
        if (version.stdout + version.stderr).strip() != "v3.0.0-beta.28":
            self.skipTest("The pinned Wails CLI is required for the regeneration test")
        repo = self.bootstrap_checkout(with_build=False)
        sentinel = repo / "application-source.txt"
        sentinel.write_text("keep application source")
        root_task = (repo / "Taskfile.yml").read_bytes()
        executable(self.fakebin / "node", "#!/bin/sh\nexit 0\n")
        executable(self.fakebin / "npm", "#!/bin/sh\nexit 0\n")
        executable(self.fakebin / "go", "#!/bin/sh\nexit 0\n")
        executable(self.fakebin / "wails3", '''#!/bin/sh
echo "wails $*" >> "$NTIKUMA_TEST_LOG"
if [ "$1" = doctor ]; then exit 0; fi
exec "$NTIKUMA_TEST_REAL_WAILS" "$@"
''')
        result = self.run_script("bootstrap.sh", repo=repo, env={**self.env, "NTIKUMA_TEST_REAL_WAILS": real_cli})
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertIn('productIdentifier: "' + APP_ID + '"', (repo / "build/config.yml").read_text())
        self.assertTrue((repo / "build/linux" / f"{APP_ID}.desktop").exists())
        self.assertIn(f"/usr/share/applications/{APP_ID}.desktop", (repo / "build/linux/nfpm/nfpm.yaml").read_text())
        self.assertEqual(sentinel.read_text(), "keep application source")
        self.assertEqual((repo / "Taskfile.yml").read_bytes(), root_task)


if __name__ == "__main__":
    unittest.main()
