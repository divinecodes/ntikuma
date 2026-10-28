#!/usr/bin/env python3
"""Build the optional native Linux speech helper beside bin/ntikuma."""
import os
from pathlib import Path
import platform
import shutil
import subprocess
import tempfile

ROOT = Path(__file__).resolve().parents[1]
MODULE = ROOT / "tools/asr"


def build():
    if platform.system() != "Linux":
        raise SystemExit("The native speech helper currently supports Linux")
    target = {"x86_64": "x86_64-unknown-linux-gnu", "aarch64": "aarch64-unknown-linux-gnu"}.get(platform.machine())
    if not target:
        raise SystemExit("The native speech helper requires Linux x86_64 or aarch64")
    go = os.environ.get("GO", "go")
    output = ROOT / "bin"
    output.mkdir(exist_ok=True)
    env = {**os.environ, "CGO_ENABLED": "1", "GOMODCACHE": str(ROOT / "build/asr-native/gomodcache")}
    subprocess.run([go, "mod", "download"], cwd=MODULE, env=env, check=True)
    package = "github.com/k2-fsa/sherpa-onnx-go-linux/v2"
    directory = Path(subprocess.check_output([go, "list", "-m", "-f", "{{.Dir}}", package], cwd=MODULE, env=env, text=True).strip())
    with tempfile.TemporaryDirectory(prefix=".asr-build-", dir=output) as temporary:
        stage = Path(temporary)
        staged_libraries = stage / "asr-lib"
        staged_libraries.mkdir()
        for source in (directory / "lib" / target).glob("*.so*"):
            shutil.copy2(source, staged_libraries / source.name)
        if not all((staged_libraries / name).is_file() for name in ("libsherpa-onnx-c-api.so", "libonnxruntime.so")):
            raise RuntimeError("The downloaded speech runtime library bundle is incomplete")
        # The executable and libraries can move together. Prefer this relative
        # search path over the SDK's additional module-cache search path.
        env["CGO_LDFLAGS"] = "-Wl,-rpath,$ORIGIN/asr-lib " + env.get("CGO_LDFLAGS", "")
        subprocess.run([go, "build", "-trimpath", "-buildvcs=false", "-o", str(stage / "ntikuma-asr"), "."], cwd=MODULE, env=env, check=True)
        shutil.copy2(directory / "LICENSE", staged_libraries / "sherpa-onnx-LICENSE")
        subprocess.run([str(stage / "ntikuma-asr"), "--version"], check=True)
        libraries = output / "asr-lib"
        libraries.mkdir(exist_ok=True)
        # Rename each file so rebuilding never truncates a library mapped by
        # a running worker. A failed build leaves the working runtime intact.
        for source in staged_libraries.iterdir():
            source.replace(libraries / source.name)
        (stage / "ntikuma-asr").replace(output / "ntikuma-asr")
    print(f"Native speech helper built at {output / 'ntikuma-asr'}")


if __name__ == "__main__":
    build()
