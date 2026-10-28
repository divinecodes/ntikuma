import copy
import hashlib
import importlib.util
import io
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest
from unittest import mock

ROOT = Path(__file__).resolve().parents[2]
SPEC = importlib.util.spec_from_file_location("release_deb", ROOT / "scripts/release-deb.py")
RELEASE = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(RELEASE)


def sha(number):
    return f"{number:040x}"


def pull(number, label=None):
    return {"number": number, "merge_commit_sha": sha(number), "merged_at": "2026-10-28T12:00:00Z",
            "labels": [{"name": label}] if label else [],
            "base": {"ref": "master", "repo": {"full_name": "example/ntikuma"}}}


class FakeHistory:
    def __init__(self, count=10):
        self.cutoff = sha(count)
        self.positions = {sha(n): n for n in range(count + 1)}
        self.excluded = set()

    def eligible(self, value):
        return value in self.positions and value not in self.excluded


class FakeGitHub:
    repo = "example/ntikuma"

    def __init__(self, pulls=()):
        self.pulls = copy.deepcopy(list(pulls))
        self.refs, self.tags, self.releases, self.assets = {}, {}, {}, {}
        self.writes = []
        self.failure = None
        self.comparison = "identical"
        self.publish_after_upload = False
        self.push_access = True

    def all(self, path):
        if path.startswith("git/matching-refs/"):
            return copy.deepcopy(list(self.refs.values()))
        if path == "releases":
            return copy.deepcopy(list(self.releases.values()))
        if path.startswith("pulls?"):
            return copy.deepcopy(self.pulls)
        if path.startswith("releases/") and path.endswith("/assets"):
            return copy.deepcopy(list(self.assets.get(int(path.split("/")[1]), {}).values()))
        raise AssertionError(path)

    def request(self, method, path, body=None, **kwargs):
        if method == "GET":
            if not path:
                return {"default_branch": "master", "permissions": {"push": self.push_access}}
            if path == "branches/master":
                return {"commit": {"sha": sha(10)}}
            if path.startswith("compare/"):
                return {"status": self.comparison}
            if path.startswith("git/tags/"):
                return copy.deepcopy(self.tags[path.split("/")[-1]])
            if path.startswith("git/commits/"):
                return {"committer": {"date": "2026-10-28T12:00:00Z"}}
            if path.startswith("releases/"):
                return copy.deepcopy(self.releases[int(path.split("/")[1])])
            raise AssertionError(path)
        self.writes.append((method, path, copy.deepcopy(body)))
        if method == "POST" and path == "git/tags":
            key = hashlib.sha1(RELEASE.canonical(body).encode()).hexdigest()
            self.tags[key] = {"tag": body["tag"], "message": body["message"], "object": {"sha": body["object"], "type": body["type"]}}
            return {"sha": key}
        if method == "POST" and path == "git/refs":
            if body["ref"] in self.refs:
                raise RELEASE.ReleaseError("ref already exists")
            self.refs[body["ref"]] = {"ref": body["ref"], "object": {"type": "tag", "sha": body["sha"]}}
            self.fail_once("tag")
            return copy.deepcopy(self.refs[body["ref"]])
        if method == "POST" and path == "releases":
            key = len(self.releases) + 1
            self.releases[key] = {"id": key, **copy.deepcopy(body)}
            self.assets[key] = {}
            self.fail_once("release")
            return copy.deepcopy(self.releases[key])
        if method == "PATCH" and path.startswith("releases/"):
            key = int(path.split("/")[1])
            self.releases[key].update(copy.deepcopy(body))
            return copy.deepcopy(self.releases[key])
        if method == "DELETE" and path.startswith("releases/assets/"):
            key = int(path.split("/")[-1])
            for assets in self.assets.values():
                assets.pop(key, None)
            return None
        if method == "POST" and path == "dispatches":
            return None
        raise AssertionError((method, path, body))

    def fail_once(self, point):
        if self.failure == point:
            self.failure = None
            raise RELEASE.ReleaseError("fixture interrupted after " + point)

    def upload(self, release_id, path):
        key = max([0, *(i for assets in self.assets.values() for i in assets)]) + 1
        asset = {"id": key, "name": path.name, "size": path.stat().st_size, "state": "uploaded",
                 "digest": "sha256:" + RELEASE.digest(path)}
        self.assets[release_id][key] = asset
        self.writes.append(("UPLOAD", path.name, None))
        if self.publish_after_upload and len(self.assets[release_id]) == 3:
            self.releases[release_id]["draft"] = False
        self.fail_once("asset")
        return copy.deepcopy(asset)


def make_deb(directory, version):
    directory.mkdir(parents=True, exist_ok=True)
    with tempfile.TemporaryDirectory() as temporary:
        root = Path(temporary)
        (root / "DEBIAN").mkdir()
        (root / "DEBIAN").chmod(0o755)
        (root / "DEBIAN/control").write_text(
            f"Package: ntikuma\nVersion: {version}\nArchitecture: amd64\n"
            "Maintainer: Fixture <fixture@example.invalid>\nDescription: Release fixture\n")
        (root / "DEBIAN/control").chmod(0o644)
        target = directory / f"ntikuma_{version}_amd64.deb"
        result = subprocess.run(["dpkg-deb", "--build", "--root-owner-group", str(root), str(target)], text=True, capture_output=True)
        if result.returncode:
            raise AssertionError("Debian fixture build failed: " + result.stderr)


class ReleaseTests(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        self.root = Path(temporary.name)
        self.api = FakeGitHub([pull(1), pull(2), pull(3)])
        self.history = FakeHistory()

    def plan(self, **kwargs):
        return RELEASE.plan_releases(self.api, self.history, **kwargs)

    def artifacts(self, plan):
        path = self.root / ("v" + plan["version"])
        make_deb(path, plan["version"])
        RELEASE.write_provenance(path, plan)
        return path

    def publish(self, plan):
        RELEASE.publish_one(self.api, self.history, plan, self.artifacts(plan))

    def test_first_version_defaults_and_numeric_bumps(self):
        self.assertEqual([p["version"] for p in self.plan()["items"]], ["0.1.0", "0.1.1", "0.1.2"])
        self.api.pulls = [pull(1, "release:minor"), pull(2, "release:major"), pull(3, "release:patch")]
        self.assertEqual([p["version"] for p in self.plan()["items"]], ["0.2.0", "1.0.0", "1.0.1"])
        self.assertEqual(RELEASE.bump_version("1.9.99", "minor"), "1.10.0")
        self.assertEqual(RELEASE.bump_version("1.10.99", "patch"), "1.10.100")
        self.assertEqual(self.api.writes, [])

    def test_planning_rejects_tokens_that_cannot_see_drafts(self):
        self.api.push_access = False
        with self.assertRaisesRegex(RELEASE.ReleaseError, "push access to see all existing drafts"):
            self.plan()
        self.assertEqual(self.api.writes, [])

    def test_skipped_unmerged_other_branch_and_pre_workflow_prs_do_not_consume_versions(self):
        self.api.pulls = [pull(6), pull(5), pull(4), pull(3), pull(2), pull(1, "release:skip")]
        self.api.pulls[1]["base"]["ref"] = "other"
        self.api.pulls[2]["merged_at"] = None
        self.history.excluded.add(sha(3))
        self.assertEqual([(p["pr"], p["version"]) for p in self.plan()["items"]], [(2, "0.1.0"), (6, "0.1.1")])

    def test_conflicting_or_unknown_release_labels_fail_without_writes(self):
        for labels in (["release:minor", "release:major"], ["release:skip", "release:patch"], ["release:typo"]):
            self.api.pulls = [pull(1)]
            self.api.pulls[0]["labels"] = [{"name": value} for value in labels]
            with self.subTest(labels=labels), self.assertRaisesRegex(RELEASE.ReleaseError, "at most one valid"):
                self.plan()
        self.assertEqual(self.api.writes, [])

    def test_batches_catch_up_in_master_order_including_drafts(self):
        self.api.pulls = [pull(n) for n in (6, 2, 5, 1, 4, 3)]
        first = self.plan()
        self.assertTrue(first["more"])
        self.assertEqual([p["pr"] for p in first["items"]], [1, 2, 3])
        for item in first["items"]:
            self.publish(item)
        second = self.plan()
        self.assertEqual([p["pr"] for p in second["items"]], [4, 5, 6])
        self.assertEqual([p["version"] for p in second["items"]], ["0.1.3", "0.1.4", "0.1.5"])
        for item in second["items"]:
            self.publish(item)
        self.assertEqual(self.plan()["items"], [])
        self.assertTrue(all(release["draft"] for release in self.api.releases.values()))

    def test_retry_recovers_tag_release_and_upload_failures_without_new_versions(self):
        for failure in ("tag", "release", "asset"):
            with self.subTest(failure=failure):
                self.api = FakeGitHub([pull(1)])
                plan = self.plan()["items"][0]
                self.api.failure = failure
                with self.assertRaisesRegex(RELEASE.ReleaseError, "fixture interrupted"):
                    self.publish(plan)
                self.assertEqual(self.plan()["items"], [plan])
                if self.api.releases:
                    self.api.releases[1]["body"] += "\nMaintainer review notes stay here.\n"
                RELEASE.publish_one(self.api, self.history, plan, self.root / "v0.1.0")
                self.assertEqual(len(self.api.refs), 1)
                self.assertEqual(len(self.api.releases), 1)
                self.assertEqual(len(self.api.assets[1]), 3)
                self.assertEqual(self.plan()["items"], [])
                if failure != "tag":
                    self.assertIn("Maintainer review notes", self.api.releases[1]["body"])

    def test_pending_reservation_ignores_later_label_changes_and_stops_older_skips_reappearing(self):
        plan = self.plan()["items"][0]
        self.api.failure = "tag"
        with self.assertRaises(RELEASE.ReleaseError):
            self.publish(plan)
        self.api.pulls[0]["labels"] = [{"name": "release:major"}]
        self.assertEqual(self.plan()["items"][0], plan)
        RELEASE.publish_one(self.api, self.history, plan, self.root / "v0.1.0")
        self.api.pulls = [pull(1, "release:skip"), pull(2)]
        self.publish(self.plan()["items"][0])
        self.api.pulls[0]["labels"] = []
        self.assertEqual(self.plan()["items"], [])

    def test_ready_drafts_and_published_assets_are_never_overwritten(self):
        self.api.pulls = [pull(1)]
        plan = self.plan()["items"][0]
        self.publish(plan)
        self.api.releases[1]["body"] += "\nEdited by the maintainer.\n"
        for published in (False, True):
            self.api.releases[1]["draft"] = not published
            self.api.writes.clear()
            RELEASE.publish_one(self.api, self.history, plan, self.root / "v0.1.0")
            self.assertEqual(self.api.writes, [])
            self.assertIn("Edited by the maintainer", self.api.releases[1]["body"])

    def test_completed_asset_tampering_or_marker_removal_requires_manual_review(self):
        self.api.pulls = [pull(1)]
        self.publish(self.plan()["items"][0])
        original = copy.deepcopy(self.api.releases[1])
        self.api.releases[1]["body"] = "Missing marker"
        with self.assertRaisesRegex(RELEASE.ReleaseError, "marker missing"):
            self.plan()
        self.api.releases[1] = original
        next(iter(self.api.assets[1].values()))["digest"] = "sha256:" + "0" * 64
        with self.assertRaisesRegex(RELEASE.ReleaseError, "completed release asset changed"):
            self.plan()

    def test_published_pending_release_and_mid_upload_publication_are_not_redrafted(self):
        plan = self.plan()["items"][0]
        self.api.publish_after_upload = True
        with self.assertRaisesRegex(RELEASE.ReleaseError, "changed during upload"):
            self.publish(plan)
        self.assertFalse(self.api.releases[1]["draft"])
        self.assertFalse(any(method == "PATCH" for method, _, _ in self.api.writes))
        with self.assertRaisesRegex(RELEASE.ReleaseError, "incomplete release was published"):
            self.plan()

    def test_unmanaged_tags_and_releases_are_not_adopted(self):
        self.api.refs["refs/tags/v9.0.0"] = {"ref": "refs/tags/v9.0.0", "object": {"type": "commit", "sha": sha(1)}}
        with self.assertRaisesRegex(RELEASE.ReleaseError, "unmanaged"):
            self.plan()
        self.api.refs.clear()
        self.api.releases[1] = {"id": 1, "tag_name": "v9.0.0", "draft": True, "body": "manual"}
        with self.assertRaisesRegex(RELEASE.ReleaseError, "unmanaged"):
            self.plan()
        self.assertEqual(self.api.writes, [])

    def test_bootstrap_is_fixed_at_zero_one_zero_and_retry_safe(self):
        self.api.pulls = []
        plan = self.plan(bootstrap=True)["items"][0]
        self.assertEqual((plan["version"], plan["pr"], plan["sha"]), ("0.1.0", 0, self.history.cutoff))
        self.api.failure = "release"
        with self.assertRaises(RELEASE.ReleaseError):
            self.publish(plan)
        self.assertEqual(self.plan(bootstrap=True)["items"], [plan])
        RELEASE.publish_one(self.api, self.history, plan, self.root / "v0.1.0")
        self.assertEqual(self.plan(bootstrap=True)["items"], [])

    def test_label_change_before_tag_reservation_invalidates_build_plan(self):
        plan = self.plan()["items"][0]
        self.api.pulls[0]["labels"] = [{"name": "release:major"}]
        with self.assertRaisesRegex(RELEASE.ReleaseError, "plan changed while building"):
            self.publish(plan)
        self.assertEqual(self.api.writes, [])

    def test_annotated_tag_uses_bot_noreply_and_always_points_to_exact_commit(self):
        plan = self.plan()["items"][0]
        self.publish(plan)
        tag_body = next(body for method, path, body in self.api.writes if path == "git/tags")
        self.assertEqual(tag_body["object"], plan["sha"])
        self.assertEqual(tag_body["tagger"]["email"], RELEASE.BOT_EMAIL)
        self.assertTrue(RELEASE.BOT_EMAIL.endswith("@users.noreply.github.com"))
        self.assertTrue(next(body for _, path, body in self.api.writes if path == "releases")["draft"])
        self.assertFalse(any(body and body.get("draft") is False for _, _, body in self.api.writes))

    def test_artifact_checksums_provenance_metadata_and_symlinks_are_checked(self):
        plan = self.plan()["items"][0]
        directory = self.artifacts(plan)
        RELEASE.verify_artifacts(directory, plan)
        metadata = directory / "release.json"
        original = metadata.read_bytes()
        metadata.write_text("{}")
        with self.assertRaisesRegex(RELEASE.ReleaseError, "provenance"):
            RELEASE.verify_artifacts(directory, plan)
        metadata.write_bytes(original)
        (directory / "SHA256SUMS").write_text("bad checksums")
        with self.assertRaisesRegex(RELEASE.ReleaseError, "checksums"):
            RELEASE.verify_artifacts(directory, plan)
        RELEASE.write_provenance(directory, plan)
        metadata.unlink()
        metadata.symlink_to(self.root / "outside")
        with self.assertRaisesRegex(RELEASE.ReleaseError, "regular files"):
            RELEASE.verify_artifacts(directory, plan)

    def test_version_paths_and_unrelated_history_are_rejected(self):
        for value in ("v0.1.0", "01.0.0", "0.1.0-dev", "../../secret", "0.1.0\n"):
            with self.subTest(value=value), self.assertRaises(RELEASE.ReleaseError):
                RELEASE.version_tuple(value)
        self.api.comparison = "diverged"
        with self.assertRaisesRegex(RELEASE.ReleaseError, "no longer on master"):
            self.plan()

    def test_wrong_package_version_and_additional_assets_are_rejected(self):
        plan = self.plan()["items"][0]
        directory = self.artifacts(plan)
        package = directory / "ntikuma_0.1.0_amd64.deb"
        make_deb(directory, "0.9.0")
        (directory / "ntikuma_0.9.0_amd64.deb").replace(package)
        RELEASE.write_provenance(directory, plan)
        with self.assertRaisesRegex(RELEASE.ReleaseError, "identity, version or architecture"):
            RELEASE.verify_artifacts(directory, plan)
        (directory / "unexpected.txt").write_text("not a release asset")
        with self.assertRaisesRegex(RELEASE.ReleaseError, "contain only"):
            RELEASE.verify_artifacts(directory, plan)

    def test_plan_and_batch_validation_rejects_foreign_or_unsafe_inputs(self):
        bundle = self.plan()
        RELEASE.validate_bundle(bundle, self.api.repo)
        with self.assertRaises(RELEASE.ReleaseError):
            RELEASE.validate_bundle(bundle, "other/repo")
        for field, value in (("version", "../../escape"), ("sha", "master"), ("pr", True), ("bump", "custom")):
            altered = copy.deepcopy(bundle)
            altered["items"][0][field] = value
            with self.subTest(field=field), self.assertRaises(RELEASE.ReleaseError):
                RELEASE.validate_bundle(altered, self.api.repo)
        altered = copy.deepcopy(bundle)
        altered["items"] *= 2
        with self.assertRaisesRegex(RELEASE.ReleaseError, "batch size"):
            RELEASE.validate_bundle(altered, self.api.repo)

    def test_replaced_or_reordered_tag_source_is_rejected(self):
        plan = self.plan()["items"][0]
        self.publish(plan)
        tag = next(iter(self.api.tags.values()))
        tag["object"]["sha"] = sha(2)
        with self.assertRaisesRegex(RELEASE.ReleaseError, "tag does not match"):
            self.plan()

    def test_cli_preview_verifies_downloaded_artifacts_without_local_git_history(self):
        bundle = self.plan()
        bundle["items"] = bundle["items"][:1]
        self.artifacts(bundle["items"][0])
        (self.root / "plan.json").write_text(json.dumps(bundle))
        result = subprocess.run([sys.executable, str(ROOT / "scripts/release-deb.py"), "--repo", self.api.repo,
                                 "publish", "--artifacts", str(self.root)], cwd=self.root,
                                text=True, capture_output=True, timeout=15)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn("Preview only", result.stdout)


class BuildTests(unittest.TestCase):
    def test_build_uses_exact_source_worktree_and_no_api_token_in_children(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary) / "repo"
            root.mkdir()
            subprocess.run(["git", "init", "-b", "master", str(root)], check=True, capture_output=True)
            (root / "frontend").mkdir()
            (root / "go.mod").write_text("module fixture\ngo 1.26.0\nrequire github.com/wailsapp/wails/v3 v3.0.0-beta.28\n")
            (root / "frontend/package.json").write_text(json.dumps({"dependencies": {"@wailsio/runtime": "3.0.0-beta.28"}}))
            (root / ".gitignore").write_text("build/\n")
            (root / "source.txt").write_text("reviewed source")
            env = {**os.environ, "GIT_AUTHOR_NAME": "Fixture", "GIT_AUTHOR_EMAIL": "fixture@users.noreply.github.com",
                   "GIT_COMMITTER_NAME": "Fixture", "GIT_COMMITTER_EMAIL": "fixture@users.noreply.github.com"}
            subprocess.run(["git", "add", "."], cwd=root, check=True)
            subprocess.run(["git", "commit", "-m", "fixture one"], cwd=root, env=env, check=True, capture_output=True)
            old = subprocess.check_output(["git", "rev-parse", "HEAD"], cwd=root, text=True).strip()
            (root / "source.txt").write_text("later source")
            subprocess.run(["git", "commit", "-am", "fixture two"], cwd=root, env=env, check=True, capture_output=True)
            plan = {"schema": 1, "version": "0.1.0", "sha": old, "pr": 1, "bump": "initial"}
            bundle = {"schema": 1, "repo": "example/ntikuma", "cutoff": old, "bootstrap": False, "items": [plan], "more": False}
            output = Path(temporary) / "artifacts"
            calls = []

            def run(args, cwd, child_env):
                self.assertNotIn("GH_TOKEN", child_env)
                self.assertNotIn("GITHUB_TOKEN", child_env)
                source = cwd if (cwd / "source.txt").exists() else cwd.parent if cwd.name == "frontend" else cwd.parents[1]
                self.assertEqual((source / "source.txt").read_text(), "reviewed source")
                calls.append(args)
                if args == ["bash", "scripts/bootstrap.sh"]:
                    cache = source / "build/readonly-module-cache"
                    cache.mkdir(parents=True)
                    (cache / "module.txt").write_text("fixture module cache")
                    cache.chmod(0o555)
                if "scripts/package-deb.py" in args:
                    make_deb(output / "v0.1.0", "0.1.0")

            with mock.patch.object(RELEASE, "ROOT", root), mock.patch.object(RELEASE, "run", side_effect=run), mock.patch.dict(os.environ, {"GH_TOKEN": "fixture-token", "GITHUB_TOKEN": "fixture-token"}):
                RELEASE.build_releases(bundle, output)
            self.assertEqual((root / "source.txt").read_text(), "later source")
            self.assertEqual(json.loads((output / "plan.json").read_text()), bundle)
            self.assertTrue(any(args[:3] == ["go", "test", "-race"] for args in calls))
            self.assertTrue(any("unittest" in args for args in calls))
            self.assertEqual(subprocess.check_output(["git", "worktree", "list", "--porcelain"], cwd=root, text=True).count("worktree "), 1)


class APIClientTests(unittest.TestCase):
    def response(self, data, link=""):
        response = mock.MagicMock()
        response.__enter__.return_value = response
        response.read.return_value = json.dumps(data).encode()
        response.headers = {"Link": link}
        return response

    def test_pagination_uses_link_headers_and_canonical_repository_url(self):
        api = RELEASE.GitHub("example/ntikuma", "fixture-token")
        api.opener = mock.Mock()
        api.opener.open.side_effect = [self.response({"id": 1}), self.response([1], '<https://api.github.com/repos/example/ntikuma/releases?page=2>; rel="next"'), self.response([2])]
        api.request("GET", "")
        self.assertEqual(api.opener.open.call_args.args[0].full_url, "https://api.github.com/repos/example/ntikuma")
        self.assertEqual(api.all("releases"), [1, 2])
        self.assertEqual(api.opener.open.call_count, 3)

    def test_read_only_clients_cannot_write_or_send_credentials_elsewhere(self):
        api = RELEASE.GitHub("example/ntikuma", "fixture-token")
        api.opener = mock.Mock()
        with self.assertRaisesRegex(RELEASE.ReleaseError, "require publish --apply"):
            api.request("POST", "releases", {})
        with self.assertRaisesRegex(RELEASE.ReleaseError, "outside the GitHub API"):
            api.request("GET", "https://example.invalid/collect")
        self.assertFalse(api.opener.open.called)
        with self.assertRaises(RELEASE.ReleaseError):
            RELEASE.NoRedirect().redirect_request(None, None, 302, "redirect", {}, "https://example.invalid")


if __name__ == "__main__":
    unittest.main()
