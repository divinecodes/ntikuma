# GitHub security setup

Authenticate `gh` as an administrator of `divinecodes/ntikuma`. Keep the development archive private. These commands target the fresh repository at its canonical URL.

Enable dependency alerts and security fixes:

```bash
gh api --method PUT repos/divinecodes/ntikuma/vulnerability-alerts
gh api --method PUT repos/divinecodes/ntikuma/automated-security-fixes
```

After the repository is public, enable private reports, secret scanning and push protection:

```bash
gh api --method PUT repos/divinecodes/ntikuma/private-vulnerability-reporting
gh api --method PATCH repos/divinecodes/ntikuma --input .github/security-settings.json
```

Finish any approved history cleanup, push the reviewed workflow, and verify all three CI jobs pass before protecting `master`:

```bash
gh api --method PUT repos/divinecodes/ntikuma/branches/master/protection \
  --input .github/branch-protection.json
```

The policy requires Secrets scan, Linux checks and Native speech helper from the GitHub Actions app. Branches must be up to date, discussions resolved, and changes submitted through a PR. It blocks branch deletion and force pushes, including for administrators. Zero required approving reviews allows a solo maintainer to merge their own passing PR.

GitHub Free does not support protected private branches. Availability of secret protection and private reports also depends on repository visibility. Leave those settings pending until supported; these commands do not change visibility or enable a paid security product.

The CI workflow runs for pull requests, pushes to `master`, weekly, and on manual dispatch. Dependabot version updates begin once its configuration is on the default branch. The removed npm `minimum-release-age` setting did not enforce a delay with npm; the seven-day version-update cooldown is now configured in Dependabot.
