import hashlib
import importlib.util
import os
from pathlib import Path
import struct
import subprocess
import tempfile
import unittest
from unittest import mock

ROOT = Path(__file__).resolve().parents[2]
SPEC = importlib.util.spec_from_file_location("package_deb", ROOT / "scripts/package-deb.py")
PACKAGE = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(PACKAGE)


class DebianPackagingTests(unittest.TestCase):
    def test_release_versions_cannot_inject_build_flags_or_use_noncanonical_numbers(self):
        for value in ["0.1.0", "1.20.300", "0.0.0"]:
            self.assertEqual(PACKAGE.release_version(value), value)
        for value in ["v0.1.0", "01.1.0", "0.01.0", "1.0", "0.1.0-dev", "0.1.0 -X main.other=value", "0.1.0\n"]:
            with self.subTest(value=value), self.assertRaises(ValueError):
                PACKAGE.release_version(value)

    def test_non_amd64_and_non_elf_inputs_are_rejected(self):
        with tempfile.TemporaryDirectory() as temporary:
            file = Path(temporary) / "helper"
            for header in [b"#!/bin/sh\nexit 0\n", b"\x7fELF\x02\x01" + bytes(12) + struct.pack("<H", 183)]:
                file.write_bytes(header)
                with self.assertRaises(ValueError):
                    PACKAGE.check_elf(file)

    def test_dependency_scan_includes_helpers_and_libraries_and_fails_closed(self):
        with tempfile.TemporaryDirectory() as temporary:
            stage = Path(temporary) / "debian/ntikuma"
            stage.mkdir(parents=True)
            binaries = [stage / name for name in ["usr/bin/ntikuma", "usr/lib/ntikuma/ntikuma-asr",
                                                  "usr/lib/ntikuma/asr-lib/libonnxruntime.so"]]
            with mock.patch.object(PACKAGE, "run", return_value="shlibs:Depends=libc6 (>= 2.38), libstdc++6 (>= 4.8)\n") as call:
                self.assertEqual(PACKAGE.shared_dependencies(stage, binaries), ["libc6 (>= 2.38)", "libstdc++6 (>= 4.8)"])
                args = call.call_args.args[0]
                for binary in binaries:
                    self.assertIn("-e" + str(binary), args)
                self.assertNotIn("--ignore-missing-info", args)
            (stage / "DEBIAN").rmdir()
            with mock.patch.object(PACKAGE, "run", return_value=""), self.assertRaises(ValueError):
                PACKAGE.shared_dependencies(stage, binaries)


@unittest.skipUnless(os.environ.get("NTIKUMA_TEST_DEB_PATH"), "set NTIKUMA_TEST_DEB_PATH to validate a built package")
class BuiltDebianPackageTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.package = Path(os.environ["NTIKUMA_TEST_DEB_PATH"]).resolve()
        cls.temporary = tempfile.TemporaryDirectory()
        cls.addClassCleanup(cls.temporary.cleanup)
        cls.root = Path(cls.temporary.name)
        subprocess.run(["dpkg-deb", "-x", str(cls.package), str(cls.root)], check=True)
        cls.version = subprocess.check_output(["dpkg-deb", "-f", str(cls.package), "Version"], text=True).strip()

    def test_version_maintainer_and_dependencies(self):
        actual = subprocess.check_output([str(self.root / "usr/bin/ntikuma"), "version"], text=True).strip()
        self.assertEqual(actual, "ntikuma " + self.version)
        maintainer = subprocess.check_output(["dpkg-deb", "-f", str(self.package), "Maintainer"], text=True)
        self.assertEqual(maintainer.strip(), "Divine Tettey <14117975+divinecodes@users.noreply.github.com>")
        dependencies = subprocess.check_output(["dpkg-deb", "-f", str(self.package), "Depends"], text=True)
        for name in ["libc6", "libstdc++6", "libgtk-4-1", "libwebkitgtk-6.0-4", "pipewire-bin | pulseaudio-utils"]:
            self.assertIn(name, dependencies)

    def test_relocated_helper_has_no_build_machine_fallback(self):
        helper = self.root / "usr/lib/ntikuma/ntikuma-asr"
        env = {key: value for key, value in os.environ.items() if key not in {"LD_LIBRARY_PATH", "LD_PRELOAD"}}
        result = subprocess.check_output([str(helper), "--version"], text=True, env=env)
        self.assertIn("cohere-transcribe", result)
        self.assertEqual(subprocess.check_output(["patchelf", "--print-rpath", str(helper)], text=True).strip(), "$ORIGIN/asr-lib")
        libraries = helper.parent / "asr-lib"
        moved = []
        try:
            for path in libraries.glob("*.so"):
                hidden = path.with_suffix(".hidden")
                path.rename(hidden)
                moved.append((hidden, path))
            failed = subprocess.run([str(helper), "--version"], text=True, capture_output=True, env=env)
            self.assertNotEqual(failed.returncode, 0, "helper unexpectedly found unpackaged SDK libraries")
        finally:
            for hidden, path in moved:
                hidden.rename(path)

    def test_executables_do_not_embed_local_build_paths(self):
        private_paths = {os.fsencode(str(ROOT) + os.sep), os.fsencode(str(Path.home()) + os.sep)}
        for relative in ["usr/bin/ntikuma", "usr/lib/ntikuma/ntikuma-asr"]:
            data = (self.root / relative).read_bytes()
            for private_path in private_paths:
                with self.subTest(binary=relative):
                    self.assertFalse(private_path in data, f"{relative} contains a local build path")

    def test_desktop_permissions_licenses_and_checksum(self):
        launcher = self.root / "usr/share/applications/dev.divinetettey.ntikuma.desktop"
        subprocess.run(["desktop-file-validate", str(launcher)], check=True)
        self.assertIn('"/usr/bin/ntikuma" show', launcher.read_text())
        self.assertEqual((self.root / "usr/bin/ntikuma").stat().st_mode & 0o777, 0o755)
        docs = self.root / "usr/share/doc/ntikuma"
        for name in ["README.md", "INSTALLATION.md", "SECURITY.md", "PRIVACY.md",
                     "copyright", "THIRD_PARTY_NOTICES.txt", "licenses/sherpa-onnx-LICENSE",
                     "licenses/onnxruntime-LICENSE", "licenses/onnxruntime-ThirdPartyNotices.txt", "licenses/Inter-OFL.txt"]:
            self.assertGreater((docs / name).stat().st_size, 100)
        self.assertFalse((self.root / "etc/xdg/autostart").exists())
        self.assertFalse((self.root / "home").exists())
        digest = hashlib.sha256(self.package.read_bytes()).hexdigest()
        self.assertIn(digest + "  " + self.package.name, (self.package.parent / "SHA256SUMS").read_text())

    def test_migration_command_is_shipped_and_preview_isolated_from_user_data(self):
        wrapper = self.root / "usr/bin/ntikuma-migrate-deb"
        script = self.root / "usr/lib/ntikuma/migrate-deb.py"
        self.assertEqual(wrapper.stat().st_mode & 0o777, 0o755)
        self.assertEqual(script.stat().st_mode & 0o777, 0o755)
        self.assertIn('exec python3 /usr/lib/ntikuma/migrate-deb.py "$@"', wrapper.read_text())
        with tempfile.TemporaryDirectory() as temporary:
            profile = Path(temporary) / "profile"
            profile.mkdir()
            env = {**os.environ, "HOME": str(profile), "XDG_DATA_HOME": str(profile / "data"),
                   "XDG_CONFIG_HOME": str(profile / "config"), "NTIKUMA_BIN_DIR": str(profile / "bin")}
            result = subprocess.run(["python3", str(script), "--skip-shortcuts"], env=env,
                                    check=True, text=True, capture_output=True)
            self.assertIn("Preview only", result.stdout)
            self.assertEqual(list(profile.iterdir()), [])
