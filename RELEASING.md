# Debian releases

Ntikuma releases are Linux amd64 `.deb` files with the native speech helper and libraries. Models remain separate downloads. Every automatic release is a GitHub draft for maintainer review.

## Versions and labels

The first eligible PR merged into `master` creates `v0.1.0` by default. Later merges increment the patch version, including versions already reserved by drafts. Set at most one label before merging:

| PR label | Effect |
| --- | --- |
| None or `release:patch` | Patch bump |
| `release:minor` | Increment minor and reset patch |
| `release:major` | Increment major and reset minor/patch |
| `release:skip` | No release for this PR |

An explicit minor or major override on the first PR starts at `0.2.0` or `1.0.0`. Unknown or conflicting `release:` labels stop the run. Once a tag reserves a version, retries retain that version and source commit even if labels change. A skipped PR already included in a later release does not receive a retroactive release if its label is removed.

The [release workflow](.github/workflows/release.yml) runs after merged PRs, including fork PRs. It builds protected `master` history, never an unmerged PR head. Queue processing also finds earlier unreleased merged PRs, in master history order. PRs merged before the release workflow existed are included in the initial source snapshot rather than released individually.

## Build and draft preparation

Planning uses only GET requests, but its isolated job needs `contents: write` because GitHub lists draft releases only to callers with push access. It transfers a validated plan to the build job and executes no candidate code.

The build job has read-only repository permissions. It checks out each exact source commit in a separate worktree, bootstraps pinned tooling, builds the versioned package and runs frontend checks/tests, both Go race/vet/advisory suites, npm audits, and script/package tests. The package version and executable version must match.

A separate job receives only the validated candidates and has permission to create tags/releases. It rechecks plans and hashes, creates an annotated `vX.Y.Z` tag pointing to the exact source commit, and uploads:

- `ntikuma_X.Y.Z_amd64.deb`
- `release.json`, recording the version, source SHA, PR and package hash
- `SHA256SUMS`, covering the package and `release.json`

Tags use the GitHub Actions bot's noreply identity. The workflow creates no version-bump commits and never moves an existing tag. It verifies GitHub's SHA-256 digest for every upload before marking the draft ready. Keep the hidden `ntikuma-release` comment when editing release notes; automation uses it to distinguish pending builds from completed drafts.

Published releases and completed drafts are not overwritten. Interrupted pending uploads can be retried; only incomplete managed draft assets may be replaced. An unmanaged version tag/release, changed completed asset, or missing metadata stops automation for manual review.

Runs are serialized with GitHub concurrency. GitHub can replace pending runs, so each run reads unreleased PR history rather than relying only on its triggering PR. It handles up to three versions, then dispatches another catch-up run if needed. This bounds build time without dropping older merges. The workflow uses only `GITHUB_TOKEN`; no PAT or publishing secret is required.

## First release and recovery

After this workflow is merged, its first automatic run prepares `0.1.0` unless the PR has an override label. If no automatic release exists, an administrator can initialize a draft from the current `master`:

```bash
gh workflow run release.yml --repo divinecodes/ntikuma --ref master -f bootstrap=true
```

For a failed or missed run, rerun it in Actions, or request a catch-up without bootstrap:

```bash
gh workflow run release.yml --repo divinecodes/ntikuma --ref master
```

Artifact names include the producing run attempt. Rerunning all jobs produces fresh transfer artifacts; rerunning only a failed publishing job reuses the successful build's artifact. These temporary artifacts expire after seven days. After expiry, rerun all jobs to rebuild.

Use an account with access to the repository. Drafts count when allocating the next version. A failed upload after tag creation keeps its reserved version; do not delete the tag to force a new version. Fix conflicting labels or the reported problem and rerun. If a build fails before publication begins, no tag or draft is created. If master history was rewritten after planning, rerun from current master.

The controller has read-only planning and local artifact-verification commands for troubleshooting:

```bash
python3 scripts/release-deb.py --repo divinecodes/ntikuma plan --output /tmp/ntikuma-release-plan.json
python3 scripts/release-deb.py --repo divinecodes/ntikuma publish --artifacts /absolute/path/to/candidates
```

API planning uses `GH_TOKEN` or `GITHUB_TOKEN` with push access so it can include existing drafts. Planning sends no write requests. `publish` changes GitHub only with `--apply`; normally let the workflow run it under its shared concurrency lock.

## Review and publish

Open the [GitHub releases page](https://github.com/divinecodes/ntikuma/releases), inspect the draft's source commit and PR, and confirm it has all three assets. Download them together and run `sha256sum -c SHA256SUMS`. Follow [installation and migration](INSTALLATION.md#debian-package) to check the candidate on your desktop.

Edit the release notes, then publish the reviewed draft manually. The automation does not publish drafts, change repository visibility, install packages, migrate user data or download models. Repository visibility and private-reporting/secret-protection activation remain separate publication steps in the [security setup guide](.github/security-setup.md).
