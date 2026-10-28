#!/usr/bin/env python3
"""Build an amd64 Debian package with its native speech runtime."""
import argparse
import ctypes
from datetime import datetime, timezone
import hashlib
import json
import os
from pathlib import Path
import platform
import re
import shutil
import struct
import subprocess
import sys
import tempfile

ROOT = Path(__file__).resolve().parents[1]
APP_ID = "dev.divinetettey.ntikuma"
VERSION_PATTERN = r"(?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*)"
LIBRARIES = ("libonnxruntime.so", "libsherpa-onnx-c-api.so", "libsherpa-onnx-cxx-api.so")


def release_version(value):
    if not re.fullmatch(VERSION_PATTERN, value):
        raise ValueError("version must be X.Y.Z without a prefix, suffix or leading zeroes")
    return value


def run(args, *, cwd=ROOT, env=None, capture=False):
    return subprocess.run(args, cwd=cwd, env=env, check=True, text=True,
                          stdout=subprocess.PIPE if capture else None).stdout


def check_elf(path):
    with path.open("rb") as stream:
        header = stream.read(20)
    if len(header) != 20 or header[:6] != b"\x7fELF\x02\x01" or struct.unpack("<H", header[18:20])[0] != 62:
        raise ValueError(f"expected a Linux amd64 ELF file: {path}")


def check_onnx_license(library):
    manifest = json.loads((ROOT / "packaging/licenses/onnxruntime.json").read_text())
    for name, info in manifest["files"].items():
        data = (ROOT / "packaging/licenses" / name).read_bytes()
        if hashlib.sha256(data).hexdigest() != info["sha256"]:
            raise ValueError(f"upstream notice checksum mismatch: {name}")

    class APIBase(ctypes.Structure):
        _fields_ = [("get_api", ctypes.c_void_p), ("version", ctypes.CFUNCTYPE(ctypes.c_char_p))]

    runtime = ctypes.CDLL(str(library))
    runtime.OrtGetApiBase.restype = ctypes.POINTER(APIBase)
    actual = runtime.OrtGetApiBase().contents.version().decode()
    if actual != manifest["version"]:
        raise ValueError(f"ONNX Runtime {actual} needs matching license notices; expected {manifest['version']}")


def shared_dependencies(stage, binaries):
    """Scan every shipped ELF, including the private libraries' own dependencies."""
    work = stage.parent.parent
    (work / "debian/control").write_text(
        "Source: ntikuma\nSection: utils\nPriority: optional\n"
        "Maintainer: Divine Tettey <14117975+divinecodes@users.noreply.github.com>\n\n"
        "Package: ntikuma\nArchitecture: amd64\nDescription: GNOME desktop assistant\n")
    (stage / "DEBIAN").mkdir()
    output = run(["dpkg-shlibdeps", "-O", "-l" + str(stage / "usr/lib/ntikuma/asr-lib"),
                  *["-e" + str(path) for path in binaries]], cwd=work, capture=True)
    lines = [line.removeprefix("shlibs:Depends=") for line in output.splitlines()
             if line.startswith("shlibs:Depends=")]
    if len(lines) != 1 or not lines[0].strip():
        raise ValueError("dpkg-shlibdeps did not produce runtime dependencies")
    return lines[0].split(", ")


def notice_files(directory):
    prefixes = ("license", "licence", "copying", "copyright", "notice", "thirdpartynotices", "patents")
    for path in sorted(directory.iterdir()):
        if path.name.lower().startswith(prefixes):
            if path.is_file():
                yield path
            elif path.is_dir():
                yield from sorted(p for p in path.rglob("*") if p.is_file())


def dependency_notices():
    sections = ["Ntikuma dependency notices\n\n"
                "The application is MIT licensed. Dependencies retain their upstream licenses.\n"
                "This inventory also includes installed frontend build dependencies.\n"]

    def append(label, directory, fallback=""):
        files = list(notice_files(directory))
        sections.append("\n" + "=" * 72 + "\n" + label + "\n")
        if not files:
            sections.append(f"Package license metadata: {fallback or 'see upstream source'}\n")
        for path in files:
            sections.append(f"\n--- {path.relative_to(directory)} ---\n" + path.read_text(errors="replace") + "\n")

    go = os.environ.get("GO", "go")
    append("Go standard library", Path(run([go, "env", "GOROOT"], capture=True).strip()))
    template = "{{if .Module}}{{.Module.Path}}|{{.Module.Version}}|{{.Module.Dir}}{{end}}"
    modules = {}
    for directory, env in [(ROOT, os.environ.copy()),
                           (ROOT / "tools/asr", {**os.environ, "GOMODCACHE": str(ROOT / "build/asr-native/gomodcache")})]:
        output = run([go, "list", "-deps", "-tags", "production", "-f", template, "."],
                     cwd=directory, env=env, capture=True)
        for line in output.splitlines():
            if line:
                name, version, location = line.split("|", 2)
                if version:
                    modules[(name, version)] = Path(location)
    for (name, version), directory in sorted(modules.items()):
        append(f"Go module {name} {version}", directory)
    lock = json.loads((ROOT / "frontend/package-lock.json").read_text())
    for name, info in sorted(lock["packages"].items()):
        directory = ROOT / "frontend" / name
        if name and directory.is_dir():
            append(f"npm package {name} {info.get('version', '')}", directory, str(info.get("license", "")))
    return "".join(sections)


def package(version, output, skip_build=False):
    version = release_version(version)
    if platform.system() != "Linux" or platform.machine() != "x86_64":
        raise ValueError("Debian releases currently require a native Linux amd64 build")
    required = ["wails3", "dpkg-shlibdeps", "dpkg-deb", "patchelf", "readelf", "desktop-file-validate"]
    for tool in required:
        if not shutil.which(tool):
            raise ValueError(f"missing build tool: {tool}; see INSTALLATION.md")
    expected = re.search(r"github.com/wailsapp/wails/v3\s+(\S+)", (ROOT / "go.mod").read_text()).group(1)
    check = subprocess.run(["wails3", "version"], check=True, text=True, capture_output=True)
    if (check.stdout + check.stderr).strip() != expected:
        raise ValueError("Wails CLI must match go.mod; run scripts/bootstrap.sh")
    env = {**os.environ, "GOOS": "linux", "GOARCH": "amd64", "CGO_ENABLED": "1", "NTIKUMA_VERSION": version}
    if not skip_build:
        run(["wails3", "build"], env=env)
        # Relink explicitly: older ignored Taskfiles do not inject the release
        # version, and their task-local BUILD_FLAGS override CLI variables.
        run([os.environ.get("GO", "go"), "build", "-tags", "production", "-trimpath", "-buildvcs=false",
             "-ldflags", f"-w -s -X main.version={version}", "-o", str(ROOT / "bin/ntikuma"), "."], env=env)
        run([sys.executable, str(ROOT / "scripts/build-asr-helper.py")], env=env)
    if run([str(ROOT / "bin/ntikuma"), "version"], capture=True).strip() != "ntikuma " + version:
        raise ValueError("application version does not match the requested package version")
    check_onnx_license(ROOT / "bin/asr-lib/libonnxruntime.so")
    output = output.resolve()
    output.mkdir(parents=True, exist_ok=True)
    workdir = ROOT / "build"
    workdir.mkdir(exist_ok=True)
    with tempfile.TemporaryDirectory(prefix="deb-", dir=workdir) as temporary:
        work = Path(temporary)
        stage = work / "debian/ntikuma"
        stage.mkdir(parents=True)
        config = json.loads((ROOT / "packaging/deb.json").read_text())
        config["version"] = version
        config["contents"] = []
        epoch = int(os.environ.get("SOURCE_DATE_EPOCH") or run(["git", "log", "-1", "--format=%ct"], capture=True).strip())
        timestamp = datetime.fromtimestamp(epoch, timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ")

        def add(source, destination, mode=0o644):
            target = stage / destination.lstrip("/")
            target.parent.mkdir(parents=True, exist_ok=True)
            shutil.copyfile(source, target)
            target.chmod(mode)
            config["contents"].append({"src": str(target), "dst": destination,
                                       "file_info": {"mode": mode, "owner": "root", "group": "root", "mtime": timestamp}})
            return target

        app = add(ROOT / "bin/ntikuma", "/usr/bin/ntikuma", 0o755)
        helper = add(ROOT / "bin/ntikuma-asr", "/usr/lib/ntikuma/ntikuma-asr", 0o755)
        libraries = [add(ROOT / "bin/asr-lib" / name, "/usr/lib/ntikuma/asr-lib/" + name) for name in LIBRARIES]
        binaries = [app, helper, *libraries]
        for path in binaries:
            check_elf(path)
        run(["patchelf", "--set-rpath", "$ORIGIN/asr-lib", str(helper)])
        clean_env = {key: value for key, value in os.environ.items() if key not in {"LD_LIBRARY_PATH", "LD_PRELOAD"}}
        helper_version = run([str(helper), "--version"], env=clean_env, capture=True)
        if "ntikuma-asr protocol=1 sherpa=2.0.0" not in helper_version or "cohere-transcribe" not in helper_version:
            raise ValueError("packaged speech helper is incompatible")
        runpath = run(["patchelf", "--print-rpath", str(helper)], capture=True).strip()
        if runpath != "$ORIGIN/asr-lib":
            raise ValueError("packaged helper retains a build-machine library path")

        desktop = work / "launcher.desktop"
        run([sys.executable, str(ROOT / "scripts/render-desktop.py"), "--binary", "/usr/bin/ntikuma", "--output", str(desktop)])
        run(["desktop-file-validate", str(desktop)])
        add(desktop, f"/usr/share/applications/{APP_ID}.desktop")
        add(ROOT / "assets/ntikuma.svg", f"/usr/share/icons/hicolor/scalable/apps/{APP_ID}.svg")
        autostart = work / "autostart.desktop"
        run([sys.executable, str(ROOT / "scripts/render-desktop.py"), "--kind", "autostart", "--binary", "/usr/bin/ntikuma", "--output", str(autostart)])
        add(autostart, "/usr/share/ntikuma/autostart.desktop")
        add(ROOT / "scripts/install-shortcuts.sh", "/usr/lib/ntikuma/install-shortcuts.sh", 0o755)
        wrapper = work / "shortcuts"
        wrapper.write_text('#!/bin/sh\nexport NTIKUMA_BIN=/usr/bin/ntikuma\nexec /usr/lib/ntikuma/install-shortcuts.sh "$@"\n')
        add(wrapper, "/usr/bin/ntikuma-install-shortcuts", 0o755)
        add(ROOT / "scripts/migrate-deb.py", "/usr/lib/ntikuma/migrate-deb.py", 0o755)
        migration = work / "migrate-deb"
        migration.write_text('#!/bin/sh\nexec python3 /usr/lib/ntikuma/migrate-deb.py "$@"\n')
        add(migration, "/usr/bin/ntikuma-migrate-deb", 0o755)
        for name in ["README.md", "INSTALLATION.md", "SECURITY.md", "PRIVACY.md"]:
            add(ROOT / name, "/usr/share/doc/ntikuma/" + name)
        add(ROOT / "LICENSE", "/usr/share/doc/ntikuma/copyright")
        add(ROOT / "bin/asr-lib/sherpa-onnx-LICENSE", "/usr/share/doc/ntikuma/licenses/sherpa-onnx-LICENSE")
        for name in json.loads((ROOT / "packaging/licenses/onnxruntime.json").read_text())["files"]:
            add(ROOT / "packaging/licenses" / name, "/usr/share/doc/ntikuma/licenses/" + name)
        add(ROOT / "packaging/licenses/Inter-OFL.txt", "/usr/share/doc/ntikuma/licenses/Inter-OFL.txt")
        notices = work / "THIRD_PARTY_NOTICES.txt"
        notices.write_text(dependency_notices())
        add(notices, "/usr/share/doc/ntikuma/THIRD_PARTY_NOTICES.txt")
        config["depends"] = sorted(set(config["depends"] + shared_dependencies(stage, binaries)))
        config["scripts"] = {"postinstall": str(ROOT / "packaging/deb-postinst.sh"),
                             "postremove": str(ROOT / "packaging/deb-postrm.sh")}
        config_path = work / "nfpm.json"
        config_path.write_text(json.dumps(config, indent=2) + "\n")
        run(["wails3", "tool", "package", "-name", "ntikuma", "-format", "deb", "-config", str(config_path), "-out", str(work)])
        result = work / "ntikuma.deb"
        if run(["dpkg-deb", "-f", str(result), "Version"], capture=True).strip() != version:
            raise ValueError("generated Debian metadata has the wrong version")
        destination = output / f"ntikuma_{version}_amd64.deb"
        staged = output / ("." + destination.name + ".tmp")
        shutil.copyfile(result, staged)
        staged.replace(destination)
        digest = hashlib.sha256(destination.read_bytes()).hexdigest()
        checksum = output / ".SHA256SUMS.tmp"
        checksum.write_text(f"{digest}  {destination.name}\n")
        checksum.replace(output / "SHA256SUMS")
        print(f"Package: {destination}\nSHA256: {digest}\nDepends: {', '.join(config['depends'])}")
        return destination


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--version", default=os.environ.get("NTIKUMA_VERSION", "0.1.0"))
    parser.add_argument("--output-dir", type=Path)
    parser.add_argument("--skip-build", action="store_true", help="package existing binaries after validating their version and runtime")
    args = parser.parse_args()
    try:
        version = release_version(args.version)
        package(version, args.output_dir or ROOT / "bin/packages" / version, args.skip_build)
    except (ValueError, OSError, subprocess.CalledProcessError) as error:
        parser.exit(1, f"Packaging failed: {error}\n")


if __name__ == "__main__":
    main()
