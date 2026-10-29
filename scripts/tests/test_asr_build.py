import importlib.util
import os
from pathlib import Path
import re
import shutil
import subprocess
import tempfile
import unittest
from unittest import mock

ROOT = Path(__file__).resolve().parents[2]
SPEC = importlib.util.spec_from_file_location("asr_build", ROOT / "scripts/build-asr-helper.py")
BUILD = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(BUILD)


class GoPackageScanTests(unittest.TestCase):
    def test_generated_build_cache_is_excluded_from_the_app_module(self):
        go = os.environ.get("GO") or shutil.which("go")
        if not go:
            self.skipTest("Go 1.26+ is required for the package scan test")
        version = subprocess.check_output([go, "version"], text=True)
        match = re.search(r"go(\d+)\.(\d+)", version)
        if not match or tuple(map(int, match.groups())) < (1, 26):
            self.skipTest("Go 1.26+ is required for the package scan test")
        with tempfile.TemporaryDirectory() as temporary:
            repo = Path(temporary)
            shutil.copy2(ROOT / "go.mod", repo / "go.mod")
            (repo / "main.go").write_text("package main\nfunc main() {}\n")
            source = repo / "internal/example"
            source.mkdir(parents=True)
            (source / "example.go").write_text("package example\n")
            # Older dependencies lack go.mod files, so Go otherwise descends
            # into these versioned directories as part of the app module.
            cache = repo / "build/asr-native/gomodcache/example.com/legacy@v1.0.0"
            cache.mkdir(parents=True)
            (cache / "legacy.go").write_text("package legacy\n")
            (repo / ".gitignore").write_text("/build/\n")
            env = {**os.environ, "GOWORK": "off", "GOTOOLCHAIN": "local",
                   "GOMODCACHE": str(repo / ".modcache"), "GOCACHE": str(repo / ".gocache"),
                   "GOPROXY": "off", "GOSUMDB": "off"}
            tidy = subprocess.run([go, "mod", "tidy"], cwd=repo, env=env,
                                  text=True, capture_output=True, timeout=20)
            self.assertEqual(tidy.returncode, 0, tidy.stdout + tidy.stderr)
            packages = subprocess.check_output([go, "list", "-buildvcs=false", "./..."], cwd=repo, env=env,
                                               text=True, timeout=20).splitlines()
            self.assertEqual(packages, ["ntikuma", "ntikuma/internal/example"])


class NativeBuildTests(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        self.base = Path(temporary.name)
        self.sdk = self.base / "sdk with spaces"
        libraries = self.sdk / "lib/x86_64-unknown-linux-gnu"
        libraries.mkdir(parents=True)
        (libraries / "libsherpa-onnx-c-api.so").write_bytes(b"new native library")
        (libraries / "libonnxruntime.so").write_bytes(b"onnx runtime")
        (self.sdk / "LICENSE").write_text("test fixture license")
        (self.base / "tools/asr").mkdir(parents=True)
        self.go = self.base / "fake-go"
        self.go.write_text('''#!/usr/bin/env python3
import os, sys
from pathlib import Path
if sys.argv[1] == 'list':
    print(os.environ['NTIKUMA_FAKE_SDK'])
elif sys.argv[1] == 'build':
    if os.environ.get('NTIKUMA_FAIL_BUILD'):
        sys.exit(2)
    output = Path(sys.argv[sys.argv.index('-o') + 1])
    output.write_text("#!/bin/sh\\nprintf 'ntikuma-asr protocol=1 sherpa=2.0.0\\\\n'\\n")
    output.chmod(0o700)
''')
        self.go.chmod(0o700)
        self.env = {"GO": str(self.go), "NTIKUMA_FAKE_SDK": str(self.sdk)}

    def run_build(self, **extra):
        with mock.patch.object(BUILD, "ROOT", self.base), mock.patch.object(BUILD, "MODULE", self.base / "tools/asr"), \
             mock.patch.object(BUILD.platform, "system", return_value="Linux"), \
             mock.patch.object(BUILD.platform, "machine", return_value="x86_64"), \
             mock.patch.dict(os.environ, {**self.env, **extra}):
            BUILD.build()

    def test_rebuild_preserves_libraries_already_open_by_a_worker(self):
        libraries = self.base / "bin/asr-lib"
        libraries.mkdir(parents=True)
        destination = libraries / "libsherpa-onnx-c-api.so"
        destination.write_bytes(b"old mapped library")
        with destination.open("rb") as mapped:
            self.run_build()
            self.assertEqual(mapped.read(), b"old mapped library")
        self.assertEqual(destination.read_bytes(), b"new native library")
        self.assertTrue((self.base / "bin/ntikuma-asr").is_file())
        self.assertFalse(list((self.base / "bin").glob('.asr-build-*')))

    def test_failed_build_does_not_replace_a_working_runtime(self):
        libraries = self.base / "bin/asr-lib"
        libraries.mkdir(parents=True)
        (libraries / "libsherpa-onnx-c-api.so").write_bytes(b"working library")
        binary = self.base / "bin/ntikuma-asr"
        binary.write_bytes(b"working helper")
        with self.assertRaises(subprocess.CalledProcessError):
            self.run_build(NTIKUMA_FAIL_BUILD="1")
        self.assertEqual(binary.read_bytes(), b"working helper")
        self.assertEqual((libraries / "libsherpa-onnx-c-api.so").read_bytes(), b"working library")


if __name__ == "__main__":
    unittest.main()
