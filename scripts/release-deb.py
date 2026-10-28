#!/usr/bin/env python3
"""Plan, build and upload immutable, draft-only Debian releases after PR merges."""
import argparse
import hashlib
import json
import os
from pathlib import Path
import re
import stat
import subprocess
import sys
import tempfile
import urllib.error
import urllib.parse
import urllib.request

ROOT = Path(__file__).resolve().parents[1]
VERSION = r"(?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*)"
SHA = r"[0-9a-f]{40}"
TAG_HEADER = "Ntikuma release plan v1\n"
MARKER = re.compile(r"<!-- ntikuma-release (.*?) -->")
BATCH_SIZE = 3
BOT_EMAIL = "41898282+github-actions[bot]@users.noreply.github.com"
# PR planning needs merge_commit_sha, removed from the 2026-03-10 API.
API_VERSION = "2022-11-28"


class ReleaseError(Exception):
    pass


def canonical(value):
    return json.dumps(value, sort_keys=True, separators=(",", ":"))


def version_tuple(value):
    if not isinstance(value, str) or not re.fullmatch(VERSION, value):
        raise ReleaseError("release versions must be X.Y.Z without prefixes or suffixes")
    return tuple(map(int, value.split(".")))


def validate_plan(plan):
    if not isinstance(plan, dict) or set(plan) != {"schema", "version", "sha", "pr", "bump"}:
        raise ReleaseError("invalid release plan fields")
    version_tuple(plan["version"])
    if plan["schema"] != 1 or not isinstance(plan["sha"], str) or not re.fullmatch(SHA, plan["sha"]):
        raise ReleaseError("invalid release plan schema or commit")
    if type(plan["pr"]) is not int or plan["pr"] < 0 or plan["bump"] not in {"bootstrap", "initial", "patch", "minor", "major"}:
        raise ReleaseError("invalid release PR or bump")
    if (plan["pr"] == 0) != (plan["bump"] == "bootstrap"):
        raise ReleaseError("only bootstrap releases may omit a PR")
    return plan


def bump_version(previous, bump):
    if previous is None and bump in ("patch", "minor", "major"):
        if bump == "patch":
            return "0.1.0"
        previous = "0.1.0"
    major, minor, patch = version_tuple(previous)
    if bump == "major":
        return f"{major + 1}.0.0"
    if bump == "minor":
        return f"{major}.{minor + 1}.0"
    return f"{major}.{minor}.{patch + 1}"


def label_bump(pr):
    labels = [label["name"] for label in pr["labels"] if label["name"].startswith("release:")]
    if len(labels) > 1 or any(label not in {"release:patch", "release:minor", "release:major", "release:skip"} for label in labels):
        raise ReleaseError(f"PR #{pr['number']} needs at most one valid release: label")
    return labels[0].split(":")[1] if labels else "patch"


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, request, fp, code, message, headers, url):
        raise ReleaseError("GitHub redirected the request; use the canonical repository name")


class GitHub:
    def __init__(self, repo, token, write=False):
        if not re.fullmatch(r"[A-Za-z0-9][A-Za-z0-9_.-]*/[A-Za-z0-9][A-Za-z0-9_.-]*", repo):
            raise ReleaseError("specify a canonical owner/repository")
        self.repo, self.token, self.write = repo, token, write
        self.opener = urllib.request.build_opener(NoRedirect())

    def _request(self, method, path, body=None, upload=None, missing=False):
        if method != "GET" and not self.write:
            raise ReleaseError("GitHub writes require publish --apply")
        url = path if path.startswith("https://") else f"https://api.github.com/repos/{self.repo}" + ("/" + path if path else "")
        if urllib.parse.urlsplit(url).netloc not in {"api.github.com", "uploads.github.com"}:
            raise ReleaseError("refusing to send a token outside the GitHub API")
        data = upload.read_bytes() if upload else (canonical(body).encode() if body is not None else None)
        headers = {"Accept": "application/vnd.github+json", "User-Agent": "ntikuma-releases",
                   "X-GitHub-Api-Version": API_VERSION}
        if self.token:
            headers["Authorization"] = "Bearer " + self.token
        if data is not None:
            headers["Content-Type"] = "application/octet-stream" if upload else "application/json"
        try:
            with self.opener.open(urllib.request.Request(url, data=data, headers=headers, method=method), timeout=120) as response:
                raw = response.read()
                links = response.headers.get("Link", "")
                next_url = next((part.split(">")[0].strip().lstrip("<") for part in links.split(",") if 'rel="next"' in part), None)
                return json.loads(raw) if raw else None, next_url
        except urllib.error.HTTPError as error:
            if missing and error.code == 404:
                return None, None
            raise ReleaseError(f"GitHub {method} failed with HTTP {error.code}; rerun after resolving the failure") from error

    def request(self, method, path, body=None, **kwargs):
        return self._request(method, path, body, **kwargs)[0]

    def all(self, path):
        path += ("&" if "?" in path else "?") + "per_page=100"
        result = []
        while path:
            rows, path = self._request("GET", path)
            if not isinstance(rows, list):
                raise ReleaseError("GitHub returned an unexpected list response")
            result.extend(rows)
        return result

    def upload(self, release_id, path):
        url = f"https://uploads.github.com/repos/{self.repo}/releases/{release_id}/assets?" + urllib.parse.urlencode({"name": path.name})
        return self.request("POST", url, upload=path)


def git(args, cwd=None):
    return subprocess.check_output(["git", *map(str, args)], cwd=cwd or ROOT, text=True).strip()


class History:
    def __init__(self, cutoff):
        if not re.fullmatch(SHA, cutoff):
            raise ReleaseError("cutoff must be a full commit SHA")
        self.cutoff = cutoff
        self.positions = {sha: index for index, sha in enumerate(git(["rev-list", "--first-parent", "--reverse", cutoff]).splitlines())}

    def eligible(self, sha):
        return sha in self.positions and subprocess.run(
            ["git", "cat-file", "-e", sha + ":.github/workflows/release.yml"], cwd=ROOT,
            stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL).returncode == 0


def marker(body):
    matches = MARKER.findall(body or "")
    if len(matches) != 1:
        raise ReleaseError("managed release marker missing or duplicated; preserve it when editing release notes")
    try:
        value = json.loads(matches[0])
        validate_plan(value["plan"])
        if value["state"] not in {"pending", "ready"}:
            raise ValueError()
        return value
    except (KeyError, TypeError, ValueError) as error:
        raise ReleaseError("invalid managed release marker") from error


def asset_names(plan):
    return {f"ntikuma_{plan['version']}_amd64.deb", "SHA256SUMS", "release.json"}


def asset_map(api, release):
    assets = api.all(f"releases/{release['id']}/assets")
    result = {a["name"]: a for a in assets}
    if len(result) != len(assets):
        raise ReleaseError("duplicate release asset names")
    return result


def is_ready(api, release, plan):
    data = marker(release.get("body"))
    if data["plan"] != plan:
        raise ReleaseError("release marker disagrees with its immutable tag")
    if data["state"] == "pending":
        if not release["draft"]:
            raise ReleaseError("an incomplete release was published; automatic repair is disabled")
        return False
    digests = data.get("digests", {})
    if set(digests) != asset_names(plan) or any(not re.fullmatch(r"[0-9a-f]{64}", digest) for digest in digests.values()):
        raise ReleaseError("ready release has invalid asset digests")
    assets = asset_map(api, release)
    for name, digest in digests.items():
        asset = assets.get(name, {})
        if asset.get("state") != "uploaded" or asset.get("digest") != "sha256:" + digest or asset.get("size", 0) <= 0:
            raise ReleaseError("a completed release asset changed; review it manually instead of overwriting it")
    return True


def load_state(api, history):
    plans = {}
    for ref in api.all("git/matching-refs/tags/v"):
        name = ref["ref"].removeprefix("refs/tags/")
        if not re.fullmatch("v" + VERSION, name):
            continue
        if ref["object"]["type"] != "tag":
            raise ReleaseError(f"{name} is unmanaged; automatic releases will not adopt or move it")
        tag = api.request("GET", "git/tags/" + ref["object"]["sha"])
        if not tag["message"].startswith(TAG_HEADER):
            raise ReleaseError(f"{name} has no managed release plan")
        plan = validate_plan(json.loads(tag["message"][len(TAG_HEADER):]))
        if name != "v" + plan["version"] or tag["tag"] != name or tag["object"].get("type") != "commit" or tag["object"].get("sha") != plan["sha"]:
            raise ReleaseError("annotated release tag does not match its plan")
        if plan["sha"] not in history.positions:
            raise ReleaseError("release history is newer than or unrelated to this checkout; rerun on master")
        plans[name] = plan
    releases = {}
    for release in api.all("releases"):
        name = release["tag_name"]
        if not re.fullmatch("v" + VERSION, name):
            continue
        if name not in plans or name in releases:
            raise ReleaseError("unmanaged or duplicate versioned release; automatic releases stopped")
        releases[name] = release
    ordered = sorted(plans.values(), key=lambda p: version_tuple(p["version"]))
    positions = [history.positions[p["sha"]] for p in ordered]
    if positions != sorted(set(positions)) or len({p["pr"] for p in ordered}) != len(ordered):
        raise ReleaseError("release tags do not follow master history or repeat a PR")
    pending = [p for p in ordered if "v" + p["version"] not in releases or not is_ready(api, releases["v" + p["version"]], p)]
    return ordered, pending, releases


def plan_releases(api, history, bootstrap=False, limit=BATCH_SIZE):
    repo = api.request("GET", "")
    if repo["default_branch"] != "master":
        raise ReleaseError("release automation expects master as the default branch")
    # Repository role flags do not describe an Actions installation token's
    # granular access. The workflow grants contents: write for draft visibility;
    # let GitHub authorize the API requests rather than rejecting permissions.push.
    master = api.request("GET", "branches/master")["commit"]["sha"]
    comparison = api.request("GET", f"compare/{history.cutoff}...{master}")
    if comparison["status"] not in {"ahead", "identical"}:
        raise ReleaseError("release checkout is no longer on master history")
    ordered, pending, _ = load_state(api, history)
    if bootstrap:
        if ordered:
            if ordered[0]["pr"] != 0:
                raise ReleaseError("the first release already exists; use a normal catch-up run")
            items = pending[:limit]
            return {"schema": 1, "repo": api.repo, "cutoff": history.cutoff, "bootstrap": True,
                    "items": items, "more": len(pending) > limit}
        item = {"schema": 1, "version": "0.1.0", "sha": history.cutoff, "pr": 0, "bump": "bootstrap"}
        return {"schema": 1, "repo": api.repo, "cutoff": history.cutoff, "bootstrap": True, "items": [item], "more": False}
    baseline = history.positions[ordered[-1]["sha"]] if ordered else -1
    candidates = []
    for pr in api.all("pulls?state=closed&base=master&sort=updated&direction=desc"):
        if not pr.get("merged_at") or pr["base"]["ref"] != "master" or pr["base"]["repo"]["full_name"].lower() != api.repo.lower():
            continue
        sha = pr.get("merge_commit_sha")
        if not sha:
            raise ReleaseError(f"merged PR #{pr['number']} is missing merge_commit_sha; verify the GitHub API version before retrying")
        if sha not in history.positions or history.positions[sha] <= baseline or not history.eligible(sha):
            continue
        candidates.append(pr)
    candidates.sort(key=lambda p: history.positions[p["merge_commit_sha"]])
    previous = ordered[-1]["version"] if ordered else None
    items = list(pending)
    for pr in candidates:
        bump = label_bump(pr)
        if bump == "skip":
            continue
        version = bump_version(previous, bump)
        items.append({"schema": 1, "version": version, "sha": pr["merge_commit_sha"],
                      "pr": pr["number"], "bump": "initial" if previous is None and bump == "patch" else bump})
        previous = version
    return {"schema": 1, "repo": api.repo, "cutoff": history.cutoff, "bootstrap": False,
            "items": items[:limit], "more": len(items) > limit}


def digest(path):
    with path.open("rb") as stream:
        return hashlib.file_digest(stream, "sha256").hexdigest()


def write_provenance(directory, plan):
    package = directory / f"ntikuma_{plan['version']}_amd64.deb"
    (directory / "release.json").write_text(json.dumps({"plan": plan, "deb_sha256": digest(package)}, sort_keys=True, indent=2) + "\n")
    names = sorted([package.name, "release.json"])
    (directory / "SHA256SUMS").write_text("".join(f"{digest(directory / name)}  {name}\n" for name in names))


def verify_artifacts(directory, plan):
    validate_plan(plan)
    if directory.is_symlink() or set(p.name for p in directory.iterdir()) != asset_names(plan):
        raise ReleaseError("release directory must contain only its .deb, SHA256SUMS and release.json")
    for path in directory.iterdir():
        if not stat.S_ISREG(path.lstat().st_mode) or path.stat().st_size == 0:
            raise ReleaseError("release assets must be nonempty regular files")
    provenance = json.loads((directory / "release.json").read_text())
    package = directory / f"ntikuma_{plan['version']}_amd64.deb"
    if provenance != {"plan": plan, "deb_sha256": digest(package)}:
        raise ReleaseError("release provenance does not match the plan and package")
    expected = "".join(f"{digest(directory / name)}  {name}\n" for name in sorted([package.name, "release.json"]))
    if (directory / "SHA256SUMS").read_text() != expected:
        raise ReleaseError("release checksums do not match")
    fields = subprocess.check_output(["dpkg-deb", "-f", str(package), "Package", "Version", "Architecture"], text=True)
    if fields.splitlines() != ["Package: ntikuma", "Version: " + plan["version"], "Architecture: amd64"]:
        raise ReleaseError("Debian package identity, version or architecture differs from the release")
    return {name: digest(directory / name) for name in asset_names(plan)}


def run(args, cwd, env):
    subprocess.run(list(map(str, args)), cwd=cwd, env=env, check=True)


def build_releases(bundle, destination):
    destination.mkdir(parents=True, exist_ok=False)
    for plan in bundle["items"]:
        validate_plan(plan)
        output = destination / ("v" + plan["version"])
        output.mkdir()
        work_base = ROOT / "build/release-worktrees"
        work_base.mkdir(parents=True, exist_ok=True)
        with tempfile.TemporaryDirectory(dir=work_base) as temporary:
            checkout = Path(temporary) / "source"
            git(["worktree", "add", "--detach", checkout, plan["sha"]])
            try:
                env = {k: v for k, v in os.environ.items() if k not in {"GH_TOKEN", "GITHUB_TOKEN"}}
                run(["bash", "scripts/bootstrap.sh"], checkout, env)
                module = re.search(r"github.com/wailsapp/wails/v3\s+v(\S+)", (checkout / "go.mod").read_text()).group(1)
                runtime = json.loads((checkout / "frontend/package.json").read_text())["dependencies"]["@wailsio/runtime"]
                if module != runtime:
                    raise ReleaseError("update Wails Go and JavaScript dependencies together")
                run([sys.executable, "scripts/package-deb.py", "--version", plan["version"], "--output-dir", output], checkout, env)
                for args in (["npm", "run", "check"], ["npm", "test"], ["npm", "audit", "--omit=dev", "--audit-level=low"], ["npm", "audit", "--audit-level=moderate"]):
                    run(args, checkout / "frontend", env)
                for args in (["go", "test", "-race", "-count=1", "./..."], ["go", "vet", "./..."], ["govulncheck", "./..."]):
                    run(args, checkout, env)
                    helper_env = {**env, "GOMODCACHE": str(checkout / "build/asr-native/gomodcache")}
                    run(args, checkout / "tools/asr", helper_env)
                test_env = {**env, "NTIKUMA_TEST_DEB_PATH": str(output / f"ntikuma_{plan['version']}_amd64.deb")}
                run([sys.executable, "-m", "unittest", "discover", "-s", "scripts/tests", "-v"], checkout, test_env)
                run(["git", "diff", "--exit-code", "--", "go.mod", "go.sum", "frontend/package.json", "frontend/package-lock.json"], checkout, env)
                write_provenance(output, plan)
                verify_artifacts(output, plan)
            finally:
                # Go module caches contain read-only directories. Make only
                # this disposable worktree's directories removable first.
                for directory, _, _ in os.walk(checkout, followlinks=False):
                    path = Path(directory)
                    if not path.is_symlink():
                        path.chmod(path.stat().st_mode | 0o700)
                git(["worktree", "remove", "--force", checkout])
    (destination / "plan.json").write_text(json.dumps(bundle, indent=2) + "\n")


def release_notes(repo, plan):
    change = f"Changes: [PR #{plan['pr']}](https://github.com/{repo}/pull/{plan['pr']})." if plan["pr"] else "Initial release snapshot."
    body = (f"Linux amd64 package with the native speech helper and libraries. Models are separate downloads.\n\n{change}\n\n"
            f"Source: [`{plan['sha'][:12]}`](https://github.com/{repo}/commit/{plan['sha']}).\n\n"
            f"See [installation and migration](https://github.com/{repo}/blob/{plan['sha']}/INSTALLATION.md). "
            "Download the .deb, SHA256SUMS and release.json; verify with `sha256sum -c SHA256SUMS`.\n\n"
            "This draft is ready for review after all three assets are uploaded and verified.\n\n")
    return body + "<!-- ntikuma-release " + canonical({"plan": plan, "state": "pending"}) + " -->\n"


def publish_one(api, history, plan, directory):
    digests = verify_artifacts(directory, plan)
    ordered, _, releases = load_state(api, history)
    tag_name = "v" + plan["version"]
    existing = next((p for p in ordered if p["version"] == plan["version"]), None)
    if existing is not None and existing != plan:
        raise ReleaseError("version already reserved for another release; no tag will be moved")
    if existing is None:
        expected = plan_releases(api, history, bootstrap=plan["pr"] == 0, limit=1)["items"]
        if expected != [plan]:
            raise ReleaseError("release plan changed while building; rerun to review current labels and versions")
        date = api.request("GET", "git/commits/" + plan["sha"])["committer"]["date"]
        tag = api.request("POST", "git/tags", {"tag": tag_name, "message": TAG_HEADER + canonical(plan),
                          "object": plan["sha"], "type": "commit",
                          "tagger": {"name": "github-actions[bot]", "email": BOT_EMAIL, "date": date}})
        api.request("POST", "git/refs", {"ref": "refs/tags/" + tag_name, "sha": tag["sha"]})
    release = releases.get(tag_name)
    if release and is_ready(api, release, plan):
        return  # Published or reviewed artifacts are immutable, including on reruns.
    if release is None:
        release = api.request("POST", "releases", {"tag_name": tag_name, "target_commitish": plan["sha"],
                              "name": "Ntikuma " + tag_name, "draft": True, "prerelease": False,
                              "body": release_notes(api.repo, plan)})
    if not release["draft"]:
        raise ReleaseError("refusing to change a published release")
    if marker(release["body"])["plan"] != plan:
        raise ReleaseError("release plan mismatch")
    assets = asset_map(api, release)
    for name in sorted(digests):
        current = assets.get(name)
        if current and current.get("state") == "uploaded" and current.get("digest") == "sha256:" + digests[name]:
            continue
        if current:
            api.request("DELETE", f"releases/assets/{current['id']}")
        uploaded = api.upload(release["id"], directory / name)
        if uploaded.get("state") != "uploaded" or uploaded.get("digest") != "sha256:" + digests[name]:
            raise ReleaseError("GitHub did not confirm the uploaded asset digest; rerun the workflow")
    current = api.request("GET", f"releases/{release['id']}")
    if not current["draft"] or marker(current["body"])["plan"] != plan:
        raise ReleaseError("release changed during upload; no publication or overwrite attempted")
    value = {"plan": plan, "state": "ready", "digests": digests}
    body = MARKER.sub(lambda _: "<!-- ntikuma-release " + canonical(value) + " -->", current["body"])
    api.request("PATCH", f"releases/{release['id']}", {"body": body})
    updated = api.request("GET", f"releases/{release['id']}")
    if not is_ready(api, updated, plan):
        raise ReleaseError("release assets were not fully verified")


def validate_bundle(bundle, repo):
    if not isinstance(bundle, dict) or set(bundle) != {"schema", "repo", "cutoff", "bootstrap", "items", "more"}:
        raise ReleaseError("invalid release batch fields")
    if bundle["schema"] != 1 or bundle["repo"] != repo or not re.fullmatch(SHA, bundle["cutoff"]):
        raise ReleaseError("release batch belongs to another repository or commit")
    if type(bundle["bootstrap"]) is not bool or type(bundle["more"]) is not bool or not isinstance(bundle["items"], list) or len(bundle["items"]) > BATCH_SIZE:
        raise ReleaseError("invalid release batch size or flags")
    for plan in bundle["items"]:
        validate_plan(plan)
    return bundle


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--repo", default=os.environ.get("GITHUB_REPOSITORY"), required=not os.environ.get("GITHUB_REPOSITORY"))
    sub = parser.add_subparsers(dest="mode", required=True)
    planning = sub.add_parser("plan", help="read-only plan; defaults to merged PR catch-up")
    planning.add_argument("--bootstrap", action="store_true")
    planning.add_argument("--output", type=Path, required=True)
    building = sub.add_parser("build", help="build and test exact source commits without GitHub writes")
    building.add_argument("--plan", type=Path, required=True)
    building.add_argument("--output", type=Path, required=True)
    publishing = sub.add_parser("publish", help="validate artifacts; GitHub writes require --apply")
    publishing.add_argument("--artifacts", type=Path, required=True)
    publishing.add_argument("--apply", action="store_true")
    args = parser.parse_args()
    try:
        api = GitHub(args.repo, os.environ.get("GH_TOKEN") or os.environ.get("GITHUB_TOKEN"), write=args.mode == "publish" and args.apply)
        if args.mode == "plan":
            bundle = plan_releases(api, History(git(["rev-parse", "HEAD"])), args.bootstrap)
            args.output.parent.mkdir(parents=True, exist_ok=True)
            args.output.write_text(json.dumps(bundle, indent=2) + "\n")
            if os.environ.get("GITHUB_OUTPUT"):
                with open(os.environ["GITHUB_OUTPUT"], "a") as stream:
                    stream.write("work=" + str(bool(bundle["items"])).lower() + "\n")
            print(json.dumps(bundle, indent=2))
        elif args.mode == "build":
            bundle = validate_bundle(json.loads(args.plan.read_text()), args.repo)
            build_releases(bundle, args.output.resolve())
        else:
            bundle = validate_bundle(json.loads((args.artifacts / "plan.json").read_text()), args.repo)
            for plan in bundle["items"]:
                verify_artifacts(args.artifacts / ("v" + plan["version"]), plan)
            if not args.apply:
                print("Artifacts verified. Preview only; no GitHub tags, releases or assets changed.")
                return
            history = History(bundle["cutoff"])
            for plan in bundle["items"]:
                publish_one(api, history, plan, args.artifacts / ("v" + plan["version"]))
                print("Draft ready for review: v" + plan["version"])
            if bundle["more"]:
                api.request("POST", "dispatches", {"event_type": "ntikuma-release-catch-up"})
    except (ReleaseError, OSError, ValueError, KeyError, TypeError, subprocess.CalledProcessError) as error:
        parser.exit(1, f"Release stopped: {error}\n")


if __name__ == "__main__":
    main()
