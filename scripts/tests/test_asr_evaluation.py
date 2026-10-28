import importlib.util
import io
import json
from pathlib import Path
import subprocess
import sys
import tarfile
import tempfile
import unittest
from unittest import mock
import wave

ROOT = Path(__file__).resolve().parents[2]
SCRIPT = ROOT / "scripts/evaluate-asr.py"
SPEC = importlib.util.spec_from_file_location("asr_evaluation", SCRIPT)
EVAL = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(EVAL)


class ASREvaluationTests(unittest.TestCase):
    def test_prepare_defaults_stay_small_and_cohere_is_an_explicit_choice(self):
        for argv, expected in ((["prepare"], ["qwen", "parakeet"]),
                               (["prepare", "--models", "cohere"], ["cohere"])):
            with self.subTest(argv=argv), mock.patch.object(sys, "argv", [str(SCRIPT), *argv]), \
                    mock.patch.object(EVAL, "prepare") as prepare:
                EVAL.main()
                self.assertEqual(prepare.call_args.args[0].models, expected)

    def test_cohere_speech_detector_checksum_is_checked_before_extraction(self):
        with tempfile.TemporaryDirectory() as directory:
            base = Path(directory)
            archive = base / "model.tar.bz2"
            archive.write_bytes(b"fixture")
            (base / "silero_vad.onnx").write_bytes(b"tampered")
            model = {"archive": archive.name, "bytes": 7, "sha256": EVAL.sha256(archive),
                     "extra_files": [{"name": "silero_vad.onnx", "bytes": 8, "sha256": "0" * 64}]}
            with self.assertRaisesRegex(ValueError, "detector checksum"):
                EVAL.download_model(base, model)
            self.assertFalse((base / "models").exists())

    def test_verified_speech_detector_is_installed_with_the_model(self):
        with tempfile.TemporaryDirectory() as directory:
            base = Path(directory)
            archive = base / "model.tar.bz2"
            with tarfile.open(archive, "w:bz2") as target:
                member = tarfile.TarInfo("bundle/encoder.onnx.data")
                member.size = 4
                target.addfile(member, io.BytesIO(b"test"))
            vad = base / "silero_vad.onnx"
            vad.write_bytes(b"verified detector")
            model = {"name": "Cohere fixture", "archive": archive.name, "bytes": archive.stat().st_size,
                     "sha256": EVAL.sha256(archive), "folder": "bundle",
                     "files": ["encoder.onnx.data", "silero_vad.onnx"],
                     "extra_files": [{"name": vad.name, "bytes": vad.stat().st_size, "sha256": EVAL.sha256(vad)}]}
            EVAL.download_model(base, model)
            self.assertEqual((base / "models/bundle/silero_vad.onnx").read_bytes(), vad.read_bytes())

    def test_word_errors_include_substitutions_deletions_and_insertions(self):
        result = EVAL.score("one two three four", "one changed four extra")
        self.assertEqual(result["words"], 4)
        self.assertEqual(result["errors"], 3)
        self.assertEqual(result["wer"], 0.75)
        for reference, hypothesis, field in (("one two", "one", "deletions"),
                                              ("one", "one two", "insertions"),
                                              ("one", "two", "substitutions")):
            self.assertEqual(EVAL.score(reference, hypothesis)[field], 1)

    def test_normalization_preserves_number_errors_and_handles_apostrophes(self):
        self.assertEqual(EVAL.score("DON’T stop, now!", "dont stop now")["errors"], 0)
        self.assertGreater(EVAL.score("fourteen", "14")["errors"], 0)
        self.assertEqual(EVAL.score("", "invented words")["insertions"], 2)
        self.assertIsNone(EVAL.score("", "invented words")["wer"])

    def test_summary_weights_by_word_count_and_counts_silence_separately(self):
        clips = [
            {"score": EVAL.score("one two three four", "one two three four"), "hypothesis": "one two three four"},
            {"score": EVAL.score("one", "wrong"), "hypothesis": "wrong"},
            {"kind": "silence", "score": EVAL.score("", "made up"), "hypothesis": "made up"},
        ]
        result = EVAL.totals(clips)
        self.assertEqual(result["wer"], 0.2)
        self.assertEqual(result["silence_with_text"], 1)
        self.assertEqual(result["silence_clips"], 1)

    def test_manifest_requires_references_unique_ids_and_the_recorders_format(self):
        with tempfile.TemporaryDirectory() as directory:
            base = Path(directory)
            with wave.open(str(base / "clip.wav"), "wb") as audio:
                audio.setparams((1, 2, 16000, 0, "NONE", "not compressed"))
                audio.writeframes(b"\0\0" * 160)
            clip = {"id": "clip", "wav": "clip.wav", "reference": "hello"}
            manifest = base / "manifest.json"
            manifest.write_text(json.dumps({"schema_version": 1, "clips": [clip]}))
            result = EVAL.read_manifest(manifest)
            self.assertEqual(result["clips"][0]["seconds"], 0.01)
            self.assertEqual(len(result["clips"][0]["audio_sha256"]), 64)
            manifest.write_text(json.dumps({"schema_version": 1, "clips": [clip, clip]}))
            with self.assertRaisesRegex(ValueError, "repeated"):
                EVAL.read_manifest(manifest)
            clip["reference"] = ""
            manifest.write_text(json.dumps({"schema_version": 1, "clips": [clip]}))
            with self.assertRaisesRegex(ValueError, "nonempty reference"):
                EVAL.read_manifest(manifest)
            with wave.open(str(base / "stereo.wav"), "wb") as audio:
                audio.setparams((2, 2, 16000, 0, "NONE", "not compressed"))
                audio.writeframes(b"\0\0\0\0" * 160)
            with self.assertRaisesRegex(ValueError, "mono"):
                EVAL.wav_info(base / "stereo.wav")

    def test_checksum_failure_prevents_model_extraction(self):
        with tempfile.TemporaryDirectory() as directory:
            base = Path(directory)
            (base / "bad.tar.bz2").write_bytes(b"not a trusted model")
            model = {"archive": "bad.tar.bz2", "bytes": 19, "sha256": "0" * 64}
            with self.assertRaisesRegex(ValueError, "checksum"):
                EVAL.download_model(base, model)
            self.assertFalse((base / "models").exists())

    def test_verified_archives_extract_safely_and_reject_other_roots(self):
        for member_name, valid in (("bundle/model.onnx", True), ("other/model.onnx", False),
                                   ("bundle/../../escape.onnx", False)):
            with self.subTest(member_name=member_name), tempfile.TemporaryDirectory() as directory:
                base = Path(directory)
                archive = base / "model.tar.bz2"
                with tarfile.open(archive, "w:bz2") as target:
                    member = tarfile.TarInfo(member_name)
                    member.size = 4
                    target.addfile(member, io.BytesIO(b"test"))
                model = {"archive": archive.name, "bytes": archive.stat().st_size,
                         "sha256": EVAL.sha256(archive), "folder": "bundle",
                         "files": ["model.onnx"], "name": "fixture"}
                if valid:
                    EVAL.download_model(base, model)
                    self.assertEqual((base / "models/bundle/model.onnx").read_bytes(), b"test")
                else:
                    with self.assertRaises((ValueError, tarfile.FilterError)):
                        EVAL.download_model(base, model)
                    self.assertFalse((base / "models/bundle").exists())
                    self.assertFalse((base / "escape.onnx").exists())

    @unittest.skipUnless(sys.platform == "linux", "Inference uses Linux seccomp")
    def test_offline_filter_denies_both_ip_socket_families_in_a_child(self):
        # The filter is irreversible for its process. Never apply it to the test runner.
        code = f"""
import importlib.util
spec = importlib.util.spec_from_file_location('asr_eval', {str(SCRIPT)!r})
evaluation = importlib.util.module_from_spec(spec)
spec.loader.exec_module(evaluation)
print(evaluation.block_network())
"""
        result = subprocess.run([sys.executable, "-c", code], text=True, capture_output=True, timeout=10)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn("both probes passed", result.stdout)

    def test_corpus_initialization_does_not_overwrite_recordings(self):
        with tempfile.TemporaryDirectory() as directory:
            target = Path(directory) / "personal"
            result = subprocess.run([sys.executable, str(SCRIPT), "init", "--directory", str(target)],
                                    text=True, capture_output=True, timeout=10)
            self.assertEqual(result.returncode, 0, result.stderr)
            manifest = target / "manifest.json"
            self.assertEqual(target.stat().st_mode & 0o777, 0o700)
            self.assertEqual(manifest.stat().st_mode & 0o777, 0o600)
            original = manifest.read_bytes()
            result = subprocess.run([sys.executable, str(SCRIPT), "init", "--directory", str(target)],
                                    text=True, capture_output=True, timeout=10)
            self.assertNotEqual(result.returncode, 0)
            self.assertEqual(manifest.read_bytes(), original)

    def test_recording_is_never_started_in_a_noninteractive_process(self):
        result = subprocess.run([sys.executable, str(SCRIPT), "record", "--manifest", "missing.json"],
                                input="", text=True, capture_output=True, timeout=10)
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("interactive terminal", result.stderr)


if __name__ == "__main__":
    unittest.main()
