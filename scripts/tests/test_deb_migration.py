import copy
from contextlib import closing
import importlib.util
import json
import os
from pathlib import Path
import shutil
import shlex
import sqlite3
import subprocess
import sys
import tempfile
import unittest
from unittest import mock

ROOT = Path(__file__).resolve().parents[2]
SPEC = importlib.util.spec_from_file_location("migrate_deb", ROOT / "scripts/migrate-deb.py")
MIGRATION = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(MIGRATION)
RENDER_SPEC = importlib.util.spec_from_file_location("render_desktop", ROOT / "scripts/render-desktop.py")
RENDER = importlib.util.module_from_spec(RENDER_SPEC)
RENDER_SPEC.loader.exec_module(RENDER)
ENSURE_STOPPED = MIGRATION.ensure_stopped
SETTING = MIGRATION.setting
SET_COMMAND = MIGRATION.set_command
COMMAND = MIGRATION.command
CHECK_PACKAGE = MIGRATION.check_package


class DebianMigrationTests(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        self.root = Path(temporary.name)
        home = self.root / "home space 'quote %percent $cash"
        home.mkdir()
        self.env = {**os.environ, "HOME": str(home), "XDG_CONFIG_HOME": str(home / "config"),
                    "XDG_DATA_HOME": str(home / "data"), "NTIKUMA_BIN_DIR": str(home / "bin")}
        env = mock.patch.dict(os.environ, self.env)
        env.start()
        self.addCleanup(env.stop)
        self.profile = MIGRATION.Profile()
        self.old_binary = self.profile.bin / "ntikuma"
        self.old_binary.parent.mkdir()
        self.old_binary.write_bytes(b"old source executable")
        self.old_binary.chmod(0o755)
        self.launcher = self.profile.data / f"applications/{MIGRATION.APP_ID}.desktop"
        self.autostart = self.profile.config / f"autostart/{MIGRATION.APP_ID}.desktop"
        for path, kind in [(self.launcher, "launcher"), (self.autostart, "autostart")]:
            path.parent.mkdir(parents=True, exist_ok=True)
            text = RENDER.render(str(self.old_binary), kind)
            if kind == "autostart":
                text = text.replace("X-GNOME-Autostart-enabled=true", "X-GNOME-Autostart-enabled=false") + "Hidden=true\n"
            path.write_text(text)
        self.profile.settings.parent.mkdir(parents=True)
        self.config = {"defaultModel": "openai/example", "keepChatsDays": 0,
                       "futureSetting": {"keep": True}, "voice": {
                           "engine": "parakeet", "nativeHelper": str(self.profile.home / ".local/lib/ntikuma-asr/ntikuma-asr"),
                           "qwen": {"modelDir": "/keep/qwen", "threads": 4},
                           "parakeet": {"modelDir": "/keep/parakeet", "threads": 3},
                           "whisperUrl": "http://127.0.0.1:8178", "whisperModelPath": "/keep/whisper"}}
        self.profile.settings.write_text(json.dumps(self.config))
        self.profile.database.parent.mkdir(parents=True, exist_ok=True)
        self.db = sqlite3.connect(self.profile.database)
        self.addCleanup(self.db.close)
        self.db.execute("PRAGMA journal_mode=WAL")
        self.db.execute("PRAGMA wal_autocheckpoint=0")
        self.db.execute("PRAGMA user_version=2")
        self.db.execute("CREATE TABLE chats (id TEXT, title TEXT)")
        self.db.execute("INSERT INTO chats VALUES ('chat-one', 'Keep my chat')")
        self.db.commit()
        self.keyring = self.profile.data / "keyrings/login.keyring"
        self.model = self.profile.data / "ntikuma/models/weights.onnx"
        for path in (self.keyring, self.model):
            path.parent.mkdir(parents=True, exist_ok=True)
            path.write_text("unchanged private data")
        self.shortcut = MIGRATION.BINDING + ":/org/gnome/settings-daemon/plugins/media-keys/custom-bindings/ntikuma-toggle/"
        self.other = MIGRATION.BINDING + ":/org/gnome/settings-daemon/plugins/media-keys/custom-bindings/other/"
        self.settings = {self.shortcut: shlex.join([str(self.old_binary), "toggle"]),
                         self.other: "other-app --keep"}
        self.original_shortcuts = copy.deepcopy(self.settings)
        self.saved = {p: p.read_bytes() for p in (self.old_binary, self.launcher, self.autostart, self.profile.settings, self.keyring, self.model)}
        for name, replacement in (("ensure_stopped", None), ("check_package", None)):
            patch = mock.patch.object(MIGRATION, name, return_value=replacement)
            patch.start()
            self.addCleanup(patch.stop)
        for name, function in (("setting", self.setting), ("set_command", self.set_command)):
            patch = mock.patch.object(MIGRATION, name, side_effect=function)
            patch.start()
            self.addCleanup(patch.stop)
        patch = mock.patch.object(MIGRATION, "command", return_value="true")
        patch.start()
        self.addCleanup(patch.stop)

    def setting(self, schema, key="command"):
        if schema == MIGRATION.MEDIA_KEYS and key == "custom-keybindings":
            return [self.shortcut.split(":", 1)[1], self.other.split(":", 1)[1]]
        return self.settings[schema]

    def set_command(self, schema, value):
        self.settings[schema] = value

    def plan(self, **kwargs):
        with mock.patch.object(MIGRATION.shutil, "which", return_value="/usr/bin/gsettings"):
            return MIGRATION.plan(self.profile, **kwargs)

    def assert_original_files(self):
        for path, data in self.saved.items():
            self.assertEqual(path.read_bytes(), data, str(path))

    def test_preview_is_read_only_even_before_package_installation(self):
        before = sorted(str(p.relative_to(self.profile.home)) for p in self.profile.home.rglob("*"))
        changes = self.plan()
        self.assertEqual(len(changes["files"]), 4)
        self.assertEqual(len(changes["shortcuts"]), 1)
        self.assert_original_files()
        self.assertEqual(self.settings, self.original_shortcuts)
        self.assertEqual(before, sorted(str(p.relative_to(self.profile.home)) for p in self.profile.home.rglob("*")))
        result = subprocess.run([sys.executable, str(ROOT / "scripts/migrate-deb.py"), "--skip-shortcuts"],
                                env=self.env, capture_output=True, text=True, timeout=20)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn("Preview only", result.stdout)
        self.assertNotIn("openai/example", result.stdout)
        self.assertFalse(self.profile.backups.exists())

    def test_apply_preserves_profile_and_wal_data_with_private_backups_and_restore(self):
        database_before = self.profile.database.read_bytes()
        backup = MIGRATION.apply(self.profile, self.plan())
        self.assertFalse(self.old_binary.exists())
        for path in (self.launcher, self.autostart):
            self.assertIn('"/usr/bin/ntikuma"', path.read_text())
            self.assertNotIn(str(self.old_binary), path.read_text())
            subprocess.run(["desktop-file-validate", str(path)], check=True, capture_output=True)
        self.assertIn("Hidden=true", self.autostart.read_text())
        self.assertIn("X-GNOME-Autostart-enabled=false", self.autostart.read_text())
        expected = copy.deepcopy(self.config)
        expected["voice"]["nativeHelper"] = str(MIGRATION.PACKAGE_HELPER)
        self.assertEqual(json.loads(self.profile.settings.read_text()), expected)
        self.assertEqual(self.settings[self.shortcut], "/usr/bin/ntikuma toggle")
        self.assertEqual(self.settings[self.other], self.original_shortcuts[self.other])
        for path in (self.keyring, self.model):
            self.assertEqual(path.read_bytes(), self.saved[path])
        self.assertEqual(self.profile.database.read_bytes(), database_before)
        with closing(sqlite3.connect(backup / "chats.sqlite3")) as db:
            self.assertEqual(db.execute("SELECT * FROM chats").fetchall(), [("chat-one", "Keep my chat")])
            self.assertEqual(db.execute("PRAGMA user_version").fetchone(), (2,))
        for path in backup.rglob("*"):
            self.assertEqual(path.stat().st_mode & 0o777, 0o700 if path.is_dir() else 0o600)
        self.assertEqual(backup.stat().st_mode & 0o777, 0o700)
        self.assertEqual(self.profile.backups.stat().st_mode & 0o777, 0o700)
        self.assertIsNone(MIGRATION.apply(self.profile, self.plan()), "second apply should be a no-op")
        self.db.execute("INSERT INTO chats VALUES ('chat-two', 'Keep newer chat')")
        self.db.commit()
        MIGRATION.restore_backup(self.profile, backup)
        self.assert_original_files()
        self.assertEqual(self.settings, self.original_shortcuts)
        self.assertEqual(self.old_binary.stat().st_mode & 0o777, 0o755)
        self.assertEqual(self.db.execute("SELECT count(*) FROM chats").fetchone(), (2,))
        MIGRATION.restore_backup(self.profile, backup)
        self.assertEqual(json.loads((backup / "manifest.json").read_text())["state"], "restored")

    def test_absent_autostart_and_custom_helper_are_preserved(self):
        self.autostart.unlink()
        self.config["voice"]["nativeHelper"] = "/opt/custom-asr/ntikuma-asr"
        self.profile.settings.write_text(json.dumps(self.config))
        before = self.profile.settings.read_bytes()
        changes = self.plan()
        self.assertIn("custom speech runtime", " ".join(changes["notes"]))
        MIGRATION.apply(self.profile, changes)
        self.assertFalse(self.autostart.exists())
        self.assertEqual(self.profile.settings.read_bytes(), before)

    def test_explicit_checkout_retargets_references_without_deleting_its_binaries(self):
        checkout = self.profile.home / "old checkout"
        app = checkout / "bin/ntikuma"
        helper = checkout / "bin/ntikuma-asr"
        app.parent.mkdir(parents=True)
        app.write_text("keep checkout app")
        helper.write_text("keep checkout runtime")
        self.launcher.write_text(RENDER.render(str(app)))
        self.config["voice"]["nativeHelper"] = str(helper)
        self.profile.settings.write_text(json.dumps(self.config))
        MIGRATION.apply(self.profile, self.plan(source_checkouts=[checkout]))
        self.assertIn('"/usr/bin/ntikuma" show', self.launcher.read_text())
        self.assertEqual(json.loads(self.profile.settings.read_text())["voice"]["nativeHelper"], str(MIGRATION.PACKAGE_HELPER))
        self.assertEqual(app.read_text(), "keep checkout app")
        self.assertEqual(helper.read_text(), "keep checkout runtime")

    def test_custom_source_and_symlink_only_retire_the_installed_alias(self):
        target = self.root / "checkout/bin/ntikuma"
        target.parent.mkdir(parents=True)
        target.write_text("keep the source checkout")
        self.old_binary.unlink()
        self.old_binary.symlink_to(target)
        extra = self.profile.home / "custom/bin/ntikuma"
        extra.parent.mkdir(parents=True)
        extra.write_text("additional old install")
        backup = MIGRATION.apply(self.profile, self.plan(source_bins=[extra]))
        self.assertFalse(self.old_binary.is_symlink())
        self.assertFalse(extra.exists())
        self.assertEqual(target.read_text(), "keep the source checkout")
        MIGRATION.restore_backup(self.profile, backup)
        self.assertTrue(self.old_binary.is_symlink())
        self.assertEqual(os.readlink(self.old_binary), str(target))
        self.assertEqual(extra.read_text(), "additional old install")

    def test_locked_shortcut_and_file_changes_abort_before_any_edits(self):
        changes = self.plan()
        with mock.patch.object(MIGRATION, "command", return_value="false"), self.assertRaisesRegex(MIGRATION.MigrationError, "locked"):
            MIGRATION.apply(self.profile, changes)
        self.assert_original_files()
        self.assertEqual(self.settings, self.original_shortcuts)
        self.old_binary.write_text("new source upgrade")
        with self.assertRaisesRegex(MIGRATION.MigrationError, "changed since preview"):
            MIGRATION.apply(self.profile, changes)
        self.assertEqual(self.old_binary.read_text(), "new source upgrade")

    def test_shortcut_failure_rolls_back_files_and_already_changed_shortcuts(self):
        second = MIGRATION.BINDING + ":/org/gnome/settings-daemon/plugins/media-keys/custom-bindings/voice/"
        changes = self.plan()
        before = shlex.join([str(self.old_binary), "voice"])
        self.settings[second] = before
        changes["shortcuts"].append({"schema": second, "before": before, "after": "/usr/bin/ntikuma voice"})

        def failing(schema, value):
            if schema == second and value == "/usr/bin/ntikuma voice":
                raise MIGRATION.MigrationError("fixture write failure")
            self.settings[schema] = value

        with mock.patch.object(MIGRATION, "set_command", side_effect=failing), mock.patch.object(MIGRATION, "command", return_value="true"):
            with self.assertRaisesRegex(MIGRATION.MigrationError, "fixture write failure"):
                MIGRATION.apply(self.profile, changes)
        self.assert_original_files()
        self.assertEqual(self.settings[self.shortcut], self.original_shortcuts[self.shortcut])
        self.assertEqual(self.settings[second], before)

    def test_restore_refuses_later_preferences_or_shortcut_edits_before_restoring_anything(self):
        for kind in ("preferences", "shortcut"):
            with self.subTest(kind=kind):
                with mock.patch.object(MIGRATION, "command", return_value="true"):
                    backup = MIGRATION.apply(self.profile, self.plan())
                after = self.profile.settings.read_bytes()
                if kind == "preferences":
                    self.profile.settings.write_text('{"new":"settings"}')
                else:
                    self.settings[self.shortcut] = "custom-new-command"
                with self.assertRaisesRegex(MIGRATION.MigrationError, "later"):
                    MIGRATION.restore_backup(self.profile, backup)
                self.assertFalse(self.old_binary.exists())
                self.profile.settings.write_bytes(after)
                self.settings[self.shortcut] = "/usr/bin/ntikuma toggle"
                MIGRATION.restore_backup(self.profile, backup)

    def test_corrupt_database_or_backup_prevents_migration_or_restore(self):
        self.db.close()
        self.profile.database.unlink()
        Path(str(self.profile.database) + "-wal").unlink(missing_ok=True)
        Path(str(self.profile.database) + "-shm").unlink(missing_ok=True)
        self.profile.database.write_text("not SQLite")
        with self.assertRaises(sqlite3.DatabaseError):
            MIGRATION.apply(self.profile, self.plan())
        self.assert_original_files()
        self.profile.database.unlink()
        with mock.patch.object(MIGRATION, "command", return_value="true"):
            backup = MIGRATION.apply(self.profile, self.plan())
        (backup / "file-0").write_text("corrupt backup")
        with self.assertRaisesRegex(MIGRATION.MigrationError, "missing or changed"):
            MIGRATION.restore_backup(self.profile, backup)
        self.assertFalse(self.old_binary.exists())

    def test_malformed_settings_unknown_launchers_and_symlinks_are_not_modified(self):
        original = self.profile.settings.read_bytes()
        for value in ("{broken", "[]", '{"voice":null}', '{"voice":{"nativeHelper":42}}'):
            self.profile.settings.write_text(value)
            with self.assertRaises((MIGRATION.MigrationError, ValueError)):
                self.plan()
            self.assertTrue(self.old_binary.exists())
        self.profile.settings.write_bytes(original)
        self.launcher.write_text("[Desktop Entry]\nExec=/custom/wrapper show\n")
        with self.assertRaisesRegex(MIGRATION.MigrationError, "unrecognized launcher"):
            self.plan()
        self.launcher.unlink()
        self.launcher.symlink_to(self.keyring)
        with self.assertRaisesRegex(MIGRATION.MigrationError, "without a symlink"):
            self.plan()
        self.assertEqual(self.keyring.read_bytes(), self.saved[self.keyring])

    def test_relative_directories_system_source_and_insecure_backup_locations_are_rejected(self):
        with mock.patch.dict(os.environ, {"XDG_DATA_HOME": "relative"}), self.assertRaises(MIGRATION.MigrationError):
            MIGRATION.Profile()
        with self.assertRaises(MIGRATION.MigrationError):
            self.plan(source_bins=[MIGRATION.PACKAGE_BINARY])
        self.profile.backups.mkdir(parents=True, mode=0o755)
        self.profile.backups.chmod(0o755)
        with self.assertRaisesRegex(MIGRATION.MigrationError, "0700"):
            MIGRATION.apply(self.profile, self.plan())
        self.assert_original_files()

    def test_running_instance_guard_does_not_stop_the_process(self):
        binary = self.root / "running/ntikuma"
        binary.parent.mkdir()
        # Multicall coreutils select their program from argv[0], so copying
        # sleep under the app name is not a portable process fixture.
        subprocess.run(["cc", "-x", "c", "-o", str(binary), "-"], input="#include <unistd.h>\nint main(void) { for (;;) pause(); }\n",
                       text=True, capture_output=True, check=True)
        process = subprocess.Popen([str(binary)])
        try:
            with self.assertRaisesRegex(MIGRATION.MigrationError, "quit Ntikuma"):
                ENSURE_STOPPED()
            self.assertIsNone(process.poll())
        finally:
            process.terminate()
            process.wait(timeout=5)

    def test_tryexec_and_existing_action_lines_are_updated_without_changing_autostart_flags(self):
        self.launcher.write_text(self.launcher.read_text() + "\n[Desktop Entry]\nTryExec=" + str(self.old_binary) + "\n")
        changes = self.plan()
        MIGRATION.apply(self.profile, changes)
        self.assertIn("TryExec=/usr/bin/ntikuma", self.launcher.read_text())
        self.assertEqual(self.launcher.read_text().count('Exec=/usr/bin/env "/usr/bin/ntikuma"'), 5)

    def test_missing_package_broken_helper_and_missing_executable_are_rejected(self):
        for status in ("deinstall ok config-files", "unknown ok not-installed"):
            with mock.patch.object(MIGRATION, "command", return_value=status), self.assertRaisesRegex(MIGRATION.MigrationError, "install the Ntikuma"):
                CHECK_PACKAGE()
        with mock.patch.object(MIGRATION, "command", return_value="install ok installed"), mock.patch.object(MIGRATION, "PACKAGE_BINARY", self.root / "missing"):
            with self.assertRaisesRegex(MIGRATION.MigrationError, "executable missing"):
                CHECK_PACKAGE()
        with mock.patch.object(MIGRATION, "command", side_effect=["install ok installed", "ntikuma 0.1.0", "incompatible runtime"]), mock.patch.object(MIGRATION, "PACKAGE_BINARY", Path(sys.executable)), mock.patch.object(MIGRATION, "PACKAGE_HELPER", Path(sys.executable)):
            with self.assertRaisesRegex(MIGRATION.MigrationError, "incompatible"):
                CHECK_PACKAGE()

    def test_gsettings_write_is_verified_and_locked_migrations_do_not_overlap(self):
        with mock.patch.object(MIGRATION, "command", return_value=""), mock.patch.object(MIGRATION, "setting", return_value="old"):
            with self.assertRaisesRegex(MIGRATION.MigrationError, "did not save"):
                SET_COMMAND(self.shortcut, "/usr/bin/ntikuma toggle")
        with MIGRATION.migration_lock(self.profile):
            with self.assertRaises(BlockingIOError):
                MIGRATION.apply(self.profile, self.plan())
        self.assert_original_files()

    def test_wrapped_old_shortcuts_block_retiring_their_executable(self):
        for value in (shlex.join([str(self.old_binary), "toggle", "--custom"]),
                      shlex.join(["bash", "-c", shlex.join([str(self.old_binary), "toggle"])])):
            self.settings[self.shortcut] = value
            with self.assertRaisesRegex(MIGRATION.MigrationError, "wrapper or extra arguments"):
                self.plan()
            self.assert_original_files()

    def test_real_gsettings_keyfile_backend_preserves_bindings_lists_and_layout_switching(self):
        if not Path("/usr/bin/gsettings").exists():
            self.skipTest("gsettings is not installed")
        with mock.patch.dict(os.environ, {"GSETTINGS_BACKEND": "keyfile"}), mock.patch.object(MIGRATION, "command", side_effect=COMMAND), mock.patch.object(MIGRATION, "setting", side_effect=SETTING), mock.patch.object(MIGRATION, "set_command", side_effect=SET_COMMAND):
            schemas = COMMAND(["gsettings", "list-relocatable-schemas"])
            if MIGRATION.BINDING not in schemas:
                self.skipTest("GNOME custom-keybinding schema is not installed")
            paths = [self.shortcut.split(":", 1)[1], self.other.split(":", 1)[1]]
            COMMAND(["gsettings", "set", MIGRATION.MEDIA_KEYS, "custom-keybindings", repr(paths)])
            for schema in (self.shortcut, self.other):
                SET_COMMAND(schema, self.original_shortcuts[schema])
                COMMAND(["gsettings", "set", schema, "binding", "'<Super>space'"])
            wm = "org.gnome.desktop.wm.keybindings"
            COMMAND(["gsettings", "set", wm, "switch-input-source", "['<Super>space']"])
            backup = MIGRATION.apply(self.profile, self.plan())
            self.assertEqual(SETTING(self.shortcut), "/usr/bin/ntikuma toggle")
            self.assertEqual(SETTING(self.other), self.original_shortcuts[self.other])
            self.assertEqual(SETTING(MIGRATION.MEDIA_KEYS, "custom-keybindings"), paths)
            self.assertEqual(SETTING(self.shortcut, "binding"), "<Super>space")
            self.assertEqual(SETTING(wm, "switch-input-source"), ["<Super>space"])
            MIGRATION.restore_backup(self.profile, backup)
            self.assertEqual(SETTING(self.shortcut), self.original_shortcuts[self.shortcut])
            self.assert_original_files()


if __name__ == "__main__":
    unittest.main()
