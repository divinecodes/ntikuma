#!/usr/bin/env python3
"""Local ASR evaluation. No application settings are changed."""

import argparse
import ctypes
import errno
import hashlib
import json
import math
import os
from pathlib import Path
import platform
import re
import random
import resource
import shutil
import signal
import socket
import statistics
import struct
import subprocess
import sys
import tarfile
import tempfile
import time
import unicodedata
import urllib.request
import wave

ROOT = Path(__file__).resolve().parents[1]
ASSETS = Path(__file__).resolve().parent / "asr-eval"
MODELS = json.loads((ASSETS / "models.json").read_text())
DEFAULT_WORK = ROOT / "build/asr-eval"
DEFAULT_MODELS = ["qwen", "parakeet"]
COHERE_LANGUAGES = ("en", "fr", "de", "it", "es", "pt", "el", "nl", "pl", "zh", "ja", "ko", "vi", "ar")


def sha256(path):
    with Path(path).open("rb") as source:
        return hashlib.file_digest(source, "sha256").hexdigest()


def write_json(path, value):
    path = Path(path)
    path.parent.mkdir(parents=True, exist_ok=True)
    temporary = path.with_suffix(path.suffix + ".tmp")
    temporary.write_text(json.dumps(value, indent=2, ensure_ascii=False) + "\n")
    temporary.replace(path)


def words(text):
    text = unicodedata.normalize("NFKC", text).casefold().replace("’", "'")
    return re.findall(r"\w+", text.replace("'", ""))


def score(reference, hypothesis):
    """Word edits with a stable tie-break: substitute, delete, then insert."""
    ref, hyp = words(reference), words(hypothesis)
    previous = [(i, 0, 0, i) for i in range(len(hyp) + 1)]
    for row, expected in enumerate(ref, 1):
        current = [(row, 0, row, 0)]
        for col, actual in enumerate(hyp, 1):
            if expected == actual:
                current.append(previous[col - 1])
                continue
            cost, substitutions, deletions, insertions = previous[col - 1]
            substitute = (cost + 1, substitutions + 1, deletions, insertions)
            cost, substitutions, deletions, insertions = previous[col]
            delete = (cost + 1, substitutions, deletions + 1, insertions)
            cost, substitutions, deletions, insertions = current[col - 1]
            insert = (cost + 1, substitutions, deletions, insertions + 1)
            current.append(min((substitute, delete, insert), key=lambda item: item[0]))
        previous = current
    errors, substitutions, deletions, insertions = previous[-1]
    return {"words": len(ref), "substitutions": substitutions, "deletions": deletions,
            "insertions": insertions, "errors": errors,
            "wer": errors / len(ref) if ref else None}


def wav_info(path):
    with wave.open(str(path), "rb") as audio:
        if (audio.getnchannels(), audio.getsampwidth(), audio.getframerate()) != (1, 2, 16000):
            raise ValueError(f"{path}: need 16 kHz mono signed 16-bit PCM WAV")
        duration = audio.getnframes() / 16000
        if not 0 < duration <= 30:
            raise ValueError(f"{path}: clips must be longer than zero and at most 30 seconds")
        return duration


def read_manifest(path):
    path = Path(path).resolve()
    manifest = json.loads(path.read_text())
    if manifest.get("schema_version") != 1 or not manifest.get("clips"):
        raise ValueError("Need schema_version 1 and a nonempty clips list")
    seen = set()
    for clip in manifest["clips"]:
        clip_id = clip["id"]
        if not re.fullmatch(r"[a-zA-Z0-9_-]+", clip_id) or clip_id in seen:
            raise ValueError(f"Invalid or repeated clip ID: {clip_id}")
        seen.add(clip_id)
        if clip.get("kind", "speech") not in ("speech", "silence"):
            raise ValueError(f"{clip_id}: kind must be speech or silence")
        reference = clip.get("reference")
        if not isinstance(reference, str):
            raise ValueError(f"{clip_id}: supply a reference transcript before evaluating")
        if clip.get("kind", "speech") == "speech" and not words(reference):
            raise ValueError(f"{clip_id}: speech needs a nonempty reference transcript")
        if clip.get("kind") == "silence" and reference:
            raise ValueError(f"{clip_id}: silence must have an empty reference")
        clip["wav"] = str((path.parent / clip["wav"]).resolve())
        clip["seconds"] = wav_info(clip["wav"])
        clip["audio_sha256"] = sha256(clip["wav"])
        clip.setdefault("group", "unspecified")
    return manifest


def block_network():
    """Linux seccomp denies new IPv4/IPv6 sockets in this worker and its threads."""
    class Comparison(ctypes.Structure):
        _fields_ = [("arg", ctypes.c_uint), ("op", ctypes.c_int),
                    ("a", ctypes.c_uint64), ("b", ctypes.c_uint64)]

    lib = ctypes.CDLL("libseccomp.so.2", use_errno=True)
    lib.seccomp_init.argtypes = [ctypes.c_uint32]
    lib.seccomp_init.restype = ctypes.c_void_p
    lib.seccomp_syscall_resolve_name.argtypes = [ctypes.c_char_p]
    lib.seccomp_syscall_resolve_name.restype = ctypes.c_int
    lib.seccomp_rule_add_array.argtypes = [ctypes.c_void_p, ctypes.c_uint32,
                                         ctypes.c_int, ctypes.c_uint,
                                         ctypes.POINTER(Comparison)]
    lib.seccomp_load.argtypes = [ctypes.c_void_p]
    lib.seccomp_release.argtypes = [ctypes.c_void_p]
    ctx = lib.seccomp_init(0x7FFF0000)  # SCMP_ACT_ALLOW
    if not ctx:
        raise RuntimeError("Cannot create the offline seccomp filter")
    try:
        syscall = lib.seccomp_syscall_resolve_name(b"socket")
        if syscall < 0:
            raise RuntimeError("Cannot resolve the socket syscall")
        for family in (socket.AF_INET, socket.AF_INET6):
            comparison = Comparison(0, 4, family, 0)  # SCMP_CMP_EQ
            if lib.seccomp_rule_add_array(ctx, 0x50000 | errno.EPERM, syscall, 1,
                                          ctypes.byref(comparison)) != 0:
                raise RuntimeError("Cannot configure offline socket restrictions")
        if lib.seccomp_load(ctx) != 0:
            raise RuntimeError("Cannot enforce offline inference on this system")
    finally:
        lib.seccomp_release(ctx)
    for family in (socket.AF_INET, socket.AF_INET6):
        try:
            with socket.socket(family, socket.SOCK_STREAM):
                raise RuntimeError("Offline socket restriction did not take effect")
        except PermissionError:
            pass
    return "Linux seccomp denied IPv4 and IPv6 socket creation; both probes passed"


def download_model(work, model):
    archive = work / model["archive"]
    partial = archive.with_suffix(archive.suffix + ".part")
    if not archive.exists():
        for attempt in range(3):
            try:
                offset = partial.stat().st_size if partial.exists() else 0
                headers = {"User-Agent": "Ntikuma-local-ASR-evaluation"}
                if offset:
                    headers["Range"] = f"bytes={offset}-"
                request = urllib.request.Request(model["url"], headers=headers)
                print(f"Downloading {model['name']} ({model['bytes'] / 1e6:.0f} MB)", flush=True)
                with urllib.request.urlopen(request, timeout=90) as response:
                    append = offset > 0 and response.status == 206
                    with partial.open("ab" if append else "wb") as output:
                        shutil.copyfileobj(response, output, length=1024 * 1024)
                partial.replace(archive)
                break
            except OSError:
                if attempt == 2:
                    raise
                time.sleep(2)
    if archive.stat().st_size != model["bytes"] or sha256(archive) != model["sha256"]:
        raise ValueError(f"Model archive checksum/size mismatch: {archive}")
    extras = [(asset, download_extra(work, asset)) for asset in model.get("extra_files", [])]
    destination = work / "models" / model["folder"]
    if not destination.exists():
        destination.parent.mkdir(parents=True, exist_ok=True)
        with tempfile.TemporaryDirectory(dir=destination.parent) as staging:
            with tarfile.open(archive, mode="r|bz2") as source:
                def checked_members():
                    for member in source:
                        if Path(member.name).parts[0] != model["folder"]:
                            raise ValueError(f"Unexpected archive root: {member.name}")
                        yield member

                source.extractall(staging, members=checked_members(), filter="data")
            (Path(staging) / model["folder"]).rename(destination)
    for asset, source in extras:
        target = destination / asset["name"]
        temporary = target.with_suffix(target.suffix + ".tmp")
        shutil.copyfile(source, temporary)
        temporary.replace(target)
    validate_model(work, model)
    print(f"Verified {model['name']}", flush=True)


def download_extra(work, asset):
    target = work / asset["name"]
    if not target.exists():
        partial = target.with_suffix(target.suffix + ".part")
        request = urllib.request.Request(asset["url"], headers={"User-Agent": "Ntikuma-local-ASR-evaluation"})
        with urllib.request.urlopen(request, timeout=90) as response, partial.open("wb") as output:
            shutil.copyfileobj(response, output, length=1024 * 1024)
        if partial.stat().st_size != asset["bytes"] or sha256(partial) != asset["sha256"]:
            raise ValueError(f"Speech detector checksum/size mismatch: {partial}")
        partial.replace(target)
    if target.stat().st_size != asset["bytes"] or sha256(target) != asset["sha256"]:
        raise ValueError(f"Speech detector checksum/size mismatch: {target}")
    return target


def validate_model(work, model):
    folder = work / "models" / model["folder"]
    for filename in model["files"]:
        if not (folder / filename).is_file() or (folder / filename).stat().st_size == 0:
            raise ValueError(f"Missing model file: {folder / filename}. Run prepare first.")
    return folder


def prepare(args):
    uv = shutil.which("uv")
    if not uv:
        raise ValueError("Install uv first: https://docs.astral.sh/uv/getting-started/installation/")
    work = args.work.resolve()
    work.mkdir(parents=True, exist_ok=True)
    env = {**os.environ, "UV_CACHE_DIR": str(work / "uv-cache"), "UV_PYTHON_DOWNLOADS": "never"}
    python = work / ".venv/bin/python"
    if not python.exists():
        subprocess.run([uv, "venv", "--python", args.python, str(work / ".venv")], env=env, check=True)
    subprocess.run([uv, "pip", "install", "--python", str(python), "-r", str(ASSETS / "requirements.txt")],
                   env=env, check=True)
    for name in args.models:
        download_model(work, MODELS[name])


def make_recognizer(name, work, threads, language="en"):
    import sherpa_onnx

    folder = validate_model(work, MODELS[name])
    if name == "qwen":
        return sherpa_onnx.OfflineRecognizer.from_qwen3_asr(
            conv_frontend=str(folder / "conv_frontend.onnx"),
            encoder=str(folder / "encoder.int8.onnx"), decoder=str(folder / "decoder.int8.onnx"),
            tokenizer=str(folder / "tokenizer"), num_threads=threads, provider="cpu",
            max_total_len=1024, max_new_tokens=512, temperature=1e-6, seed=42)
    if name == "cohere":
        return sherpa_onnx.OfflineRecognizer.from_cohere_transcribe(
            encoder=str(folder / "encoder.int8.onnx"), decoder=str(folder / "decoder.int8.onnx"),
            tokens=str(folder / "tokens.txt"), language=language, use_punct=True, use_itn=True,
            num_threads=threads, provider="cpu")
    return sherpa_onnx.OfflineRecognizer.from_transducer(
        encoder=str(folder / "encoder.int8.onnx"), decoder=str(folder / "decoder.int8.onnx"),
        joiner=str(folder / "joiner.int8.onnx"), tokens=str(folder / "tokens.txt"),
        model_type="nemo_transducer", num_threads=threads, provider="cpu")


def make_cohere_vad(folder):
    import sherpa_onnx

    config = sherpa_onnx.VadModelConfig()
    config.sample_rate = 16000
    config.num_threads = 1
    config.provider = "cpu"
    config.silero_vad.model = str(folder / "silero_vad.onnx")
    config.silero_vad.threshold = 0.5
    config.silero_vad.min_silence_duration = 0.3
    config.silero_vad.min_speech_duration = 0.1
    config.silero_vad.max_speech_duration = 25
    config.silero_vad.window_size = 512
    return sherpa_onnx.VoiceActivityDetector(config, buffer_size_in_seconds=30)


def speech_spans(vad, samples):
    """Use the app's Silero settings and 200/300 ms padding, merged once."""
    import numpy as np

    vad.reset()
    while not vad.empty():
        vad.pop()
    spans = []

    def drain():
        while not vad.empty():
            segment = vad.front
            start, end = max(0, segment.start - 3200), min(len(samples), segment.start + len(segment.samples) + 4800)
            if end > start:
                if spans and start <= spans[-1][1]:
                    spans[-1] = (spans[-1][0], max(end, spans[-1][1]))
                else:
                    spans.append((start, end))
            vad.pop()

    for offset in range(0, len(samples), 512):
        window = samples[offset:offset + 512]
        if len(window) < 512:
            window = np.pad(window, (0, 512 - len(window)))
        vad.accept_waveform(window)
        drain()
    vad.flush()
    drain()
    return spans


def worker(args):
    offline = block_network()  # Apply before importing native inference libraries.
    import numpy as np
    import sherpa_onnx

    manifest = read_manifest(args.manifest)
    model = MODELS[args.model]
    folder = validate_model(args.work, model)
    fingerprints = {name: sha256(folder / name) for name in model["files"]}
    started = time.perf_counter()
    recognizer = make_recognizer(args.model, args.work, args.threads, args.language)
    vad = make_cohere_vad(folder) if args.model == "cohere" else None
    load_seconds = time.perf_counter() - started
    result = {"model": model["name"], "bundle_sha256": model["sha256"],
              "model_files_sha256": fingerprints, "sherpa_onnx": sherpa_onnx.__version__,
              "numpy": np.__version__, "python": platform.python_version(),
              "threads": args.threads, "provider": "cpu", "language_hint": args.language if args.model == "cohere" else None,
              "offline": offline, "load_seconds": load_seconds, "clips": []}
    if args.model == "qwen":
        result["decoding"] = {"max_total_len": 1024, "max_new_tokens": 512,
                              "temperature": 1e-6, "top_p": 0.8, "seed": 42}
    elif args.model == "cohere":
        result["decoding"] = {"method": "greedy_search", "language": args.language, "use_punct": True,
                              "use_itn": True, "vad": "silero", "threshold": 0.5,
                              "min_speech_seconds": 0.1, "min_silence_seconds": 0.3,
                              "max_speech_seconds": 25, "padding_ms": [200, 300]}
    else:
        result["decoding"] = {"method": "greedy_search", "model_type": "nemo_transducer"}
    for clip in manifest["clips"]:
        with wave.open(clip["wav"], "rb") as audio:
            samples = np.frombuffer(audio.readframes(audio.getnframes()), dtype="<i2").astype(np.float32) / 32768
        elapsed, hypotheses = [], []
        for _ in range(args.repeat):
            started = time.perf_counter()
            spans = speech_spans(vad, samples) if vad is not None else [(0, len(samples))]
            parts = []
            for start, end in spans:
                stream = recognizer.create_stream()
                stream.accept_waveform(16000, samples[start:end])
                recognizer.decode_stream(stream)
                parts.append(stream.result.text.strip())
            elapsed.append(time.perf_counter() - started)
            hypotheses.append(" ".join(parts).strip())
        # The first decode in each fresh model process is excluded from warm timing.
        warm = elapsed[1:] if not result["clips"] else elapsed
        median = statistics.median(warm)
        row = {**clip, "hypothesis": hypotheses[0], "hypotheses": hypotheses,
               "repeat_agreement": len(set(hypotheses)) == 1, "decode_seconds": elapsed,
               "warm_median_seconds": median, "warm_rtf": median / clip["seconds"],
               "score": score(clip["reference"], hypotheses[0])}
        result["clips"].append(row)
        result["peak_rss_mib"] = resource.getrusage(resource.RUSAGE_SELF).ru_maxrss / 1024
        write_json(args.output, result)
        print(f"{args.model} {clip['id']}: {median:.3f}s, RTF {row['warm_rtf']:.3f}", flush=True)


def totals(clips):
    speech = [clip for clip in clips if clip.get("kind", "speech") == "speech"]
    count = sum(clip["score"]["words"] for clip in speech)
    errors = sum(clip["score"]["errors"] for clip in speech)
    return {"words": count, "errors": errors, "wer": errors / count if count else None,
            "silence_clips": sum(clip.get("kind") == "silence" for clip in clips),
            "silence_with_text": sum(clip.get("kind") == "silence" and bool(clip["hypothesis"]) for clip in clips)}


def percentage(value):
    return f"{value:.1%}" if value is not None else "n/a"


def report_text(results):
    lines = ["# Local ASR comparison", "", "These results describe this corpus and machine only.", "",
             "| Model | Speech WER | Load seconds | First decode seconds | Peak RSS MiB | Silence with text |",
             "| --- | ---: | ---: | ---: | ---: | ---: |"]
    for result in results:
        overall = totals(result["clips"])
        first = result["clips"][0]["decode_seconds"][0]
        lines.append(f"| {result['model']} | {percentage(overall['wer'])} | {result['load_seconds']:.3f} | "
                     f"{first:.3f} | {result['peak_rss_mib']:.1f} | {overall['silence_with_text']}/{overall['silence_clips']} |")
    lines += ["", "Load time is model construction in a fresh process, with OS file caches uncontrolled.",
              "Peak RSS includes the Python worker and native runtime. Warm decode excludes file reading and model loading.",
              "The first decode is excluded from warm timing. Identical recordings are reused; no audio is uploaded.",
              "WER ignores case and punctuation, retains digits, and removes apostrophes. Numeric formatting differences count.", ""]
    for result in results:
        lines += [f"## {result['model']}", "", f"Offline check: {result['offline']}.", "",
                  "| Clip | Group | Seconds | Warm decode seconds | Warm RTF | WER | Stable repeats |",
                  "| --- | --- | ---: | ---: | ---: | ---: | --- |"]
        for clip in result["clips"]:
            lines.append(f"| {clip['id']} | {clip['group']} | {clip['seconds']:.2f} | "
                         f"{clip['warm_median_seconds']:.3f} | {clip['warm_rtf']:.3f} | "
                         f"{percentage(clip['score']['wer'])} | {clip['repeat_agreement']} |")
        lines += ["", "### Group results", ""]
        for field in ("group", "speaker", "accent"):
            for group in sorted({clip[field] for clip in result["clips"] if clip.get(field)}):
                total = totals([clip for clip in result["clips"] if clip.get(field) == group])
                lines.append(f"- {field} {group}: {percentage(total['wer'])} WER, {total['errors']}/{total['words']} word errors.")
        lines += ["", "### Transcripts", ""]
        for clip in result["clips"]:
            lines += [f"#### {clip['id']}", "", f"Reference: {clip['reference'] or '[silence]'}", "",
                      f"Recognized: {clip['hypothesis'] or '[empty]'}", ""]
    return "\n".join(lines)


def run(args):
    read_manifest(args.manifest)  # Validate all audio before starting either model.
    python = args.work / ".venv/bin/python"
    if not python.exists():
        raise ValueError("Run prepare first to create the isolated runtime")
    for name in args.models:
        validate_model(args.work, MODELS[name])
    output = args.output.resolve()
    output.mkdir(parents=True, exist_ok=False)  # Never overwrite an earlier comparison.
    write_json(output / "machine.json", {"platform": platform.platform(), "cpu_count": os.cpu_count(),
                                        "cpu": Path("/proc/cpuinfo").read_text().split("model name", 1)[-1].splitlines()[0],
                                        "load_average": os.getloadavg(),
                                        "meminfo": Path("/proc/meminfo").read_text(),
                                        "manifest_sha256": sha256(args.manifest)})
    results = []
    for name in args.models:
        print(f"Evaluating {MODELS[name]['name']}...", flush=True)
        command = [str(python), str(Path(__file__).resolve()), "--work", str(args.work), "_worker",
                   "--model", name, "--manifest", str(args.manifest.resolve()), "--threads", str(args.threads),
                   "--repeat", str(args.repeat), "--language", args.language, "--output", str(output / f"{name}.json")]
        log_path = output / f"{name}.log"
        with log_path.open("w") as log:
            try:
                subprocess.run(command, check=True, stdout=log, stderr=subprocess.STDOUT, timeout=args.timeout)
            except subprocess.SubprocessError as error:
                raise RuntimeError(f"{name} failed or timed out; see {log_path}") from error
        results.append(json.loads((output / f"{name}.json").read_text()))
        (output / "report.md").write_text(report_text(results))
    print(f"Report: {output / 'report.md'}", flush=True)


def init_corpus(args):
    directory = args.directory.resolve()
    directory.mkdir(parents=True, exist_ok=False)
    shutil.copyfile(ASSETS / "prompts.json", directory / "manifest.json")
    print(f"Created {directory / 'manifest.json'}. Use record, then review each reference before run.")


def smoke_corpus(args):
    """Generate a reproducible technical check, not an accent-accuracy dataset."""
    converter = shutil.which("gst-launch-1.0")
    if not converter:
        raise ValueError("The public smoke corpus needs gst-launch-1.0 for 24 kHz to 16 kHz conversion")
    source = validate_model(args.work, MODELS["parakeet"]) / "test_wavs/en.wav"
    directory = args.directory.resolve()
    directory.mkdir(parents=True, exist_ok=False)
    clean = directory / "clean.wav"
    subprocess.run([converter, "-q", "filesrc", f"location={source}", "!", "wavparse", "!",
                    "audioconvert", "!", "audioresample", "!",
                    "audio/x-raw,format=S16LE,channels=1,rate=16000", "!", "wavenc", "!",
                    "filesink", f"location={clean}"], check=True, timeout=30)
    with wave.open(str(clean), "rb") as audio:
        samples = struct.unpack(f"<{audio.getnframes()}h", audio.readframes(audio.getnframes()))
    rng = random.Random(42)
    rms = math.sqrt(sum(sample * sample for sample in samples) / len(samples))
    noisy = [max(-32768, min(32767, round(sample + rng.gauss(0, rms / math.sqrt(10))))) for sample in samples]
    for filename, data in (("quiet.wav", [round(sample * 0.1) for sample in samples]),
                           ("noise.wav", noisy), ("silence-2.wav", [0] * 32000),
                           ("silence-5.wav", [0] * 80000)):
        with wave.open(str(directory / filename), "wb") as output:
            output.setparams((1, 2, 16000, 0, "NONE", "not compressed"))
            output.writeframes(struct.pack(f"<{len(data)}h", *data))
    reference = "Ask not what your country can do for you, ask what you can do for your country."
    clips = [{"id": name, "wav": f"{name}.wav", "group": name, "reference": reference}
             for name in ("clean", "quiet", "noise")]
    clips += [{"id": f"silence-{seconds}", "wav": f"silence-{seconds}.wav", "group": "silence",
               "kind": "silence", "reference": ""} for seconds in (2, 5)]
    write_json(directory / "manifest.json", {"schema_version": 1,
               "name": "Technical smoke check: one public utterance, derivatives, and digital silence",
               "source": "https://k2-fsa.github.io/sherpa/onnx/pretrained_models/offline-transducer/nemo-transducer-models.html",
               "source_wav_sha256": sha256(source), "noise": "Seed 42 Gaussian noise, target 10 dB SNR before PCM clipping",
               "clips": clips})
    print(f"Created {directory / 'manifest.json'}. This corpus cannot establish accent accuracy.")


def record(args):
    if not sys.stdin.isatty():
        raise ValueError("Recording requires an interactive terminal")
    manifest_path = args.manifest.resolve()
    manifest = json.loads(manifest_path.read_text())
    recorder = shutil.which("pw-record")
    if not recorder:
        raise ValueError("Install pw-record to use this recording workflow")
    print("Record at your normal speaking pace. Edit references to match what you actually say before running the comparison.")
    for clip in manifest["clips"]:
        audio = manifest_path.parent / clip["wav"]
        if audio.exists():
            print(f"Keeping existing recording: {clip['id']}")
            continue
        print(f"\n{clip['id']} [{clip['group']}]\n{clip['reference'] or 'Stay quiet for five seconds.'}")
        if input("Enter to record, or q to stop: ").strip().lower() == "q":
            return
        audio.parent.mkdir(parents=True, exist_ok=True)
        process = subprocess.Popen([recorder, "--rate", "16000", "--channels", "1", "--format", "s16", str(audio)])
        completed = False
        try:
            input("Recording. Press Enter to finish: ")
            completed = True
        finally:
            if process.poll() is None:
                process.send_signal(signal.SIGINT)
            try:
                process.wait(timeout=3)
            except subprocess.TimeoutExpired:
                process.kill()
                process.wait()
            if not completed:
                audio.unlink(missing_ok=True)
        wav_info(audio)
    print(f"Recordings ready. Review the references in {manifest_path} before evaluating.")


def main():
    os.umask(0o077)
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--work", type=Path, default=DEFAULT_WORK)
    commands = parser.add_subparsers(dest="command", required=True, metavar="{prepare,init,smoke,record,run}")
    setup = commands.add_parser("prepare", help="Install the isolated runtime and download/verify selected model bundles")
    setup.add_argument("--python", default="3.12", help="Existing Python 3.12+ interpreter for uv")
    setup.add_argument("--models", nargs="+", choices=MODELS, default=DEFAULT_MODELS)
    setup.set_defaults(action=prepare)
    corpus = commands.add_parser("init", help="Create a corpus with 20 dictation prompts")
    corpus.add_argument("--directory", type=Path)
    corpus.set_defaults(action=init_corpus)
    smoke = commands.add_parser("smoke", help="Create a public-sample technical smoke corpus, without microphone access")
    smoke.add_argument("--directory", type=Path)
    smoke.set_defaults(action=smoke_corpus)
    capture = commands.add_parser("record", help="Interactively record each missing WAV from a manifest")
    capture.add_argument("--manifest", type=Path, required=True)
    capture.set_defaults(action=record)
    for name, action in (("run", run), ("_worker", worker)):
        options = {"help": "Compare selected models offline"} if name == "run" else {}
        sub = commands.add_parser(name, **options)
        sub.add_argument("--manifest", type=Path, required=True)
        sub.add_argument("--output", type=Path, required=True)
        sub.add_argument("--threads", type=int, default=4)
        sub.add_argument("--repeat", type=int, default=3)
        sub.add_argument("--language", choices=COHERE_LANGUAGES, default="en", help="Cohere spoken language")
        if name == "run":
            sub.add_argument("--models", nargs="+", choices=MODELS, default=DEFAULT_MODELS)
            sub.add_argument("--timeout", type=float, default=1800, help="Seconds allowed per model")
        else:
            sub.add_argument("--model", choices=MODELS, required=True)
        sub.set_defaults(action=action)
    args = parser.parse_args()
    args.work = args.work.resolve()
    if args.command in ("init", "smoke") and args.directory is None:
        args.directory = args.work / ("personal" if args.command == "init" else "smoke")
    if hasattr(args, "repeat") and (args.repeat < 2 or args.threads < 1):
        parser.error("Use at least two repeats and one CPU thread")
    try:
        args.action(args)
    except (OSError, ValueError, RuntimeError, subprocess.SubprocessError) as error:
        parser.exit(1, f"ASR evaluation failed: {error}\n")
    except (KeyboardInterrupt, EOFError):
        parser.exit(130, "ASR evaluation interrupted. Completed recordings and results were kept.\n")


if __name__ == "__main__":
    main()
