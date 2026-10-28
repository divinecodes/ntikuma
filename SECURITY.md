# Security policy

## Supported versions

Security fixes target `master` and the latest published release. Older releases are not maintained. Before the first release, use the current `master` branch when reproducing a report.

## Reporting a vulnerability

Use GitHub's private [Report a vulnerability](https://github.com/divinecodes/ntikuma/security/advisories/new) form. If the form is unavailable, open an issue requesting a private reporting channel without posting the vulnerability details.

Include the affected version or commit, operating system, reproduction steps, and expected versus observed behavior. Use synthetic examples. Leave out API keys, personal recordings, screenshots of private content, and chat databases. Keep exploit details private until a fix or coordinated disclosure is agreed.

## Security boundaries

Local file tools and shell commands default to off. File tools are limited to configured folders. A shell command requires approval of the exact command, runs with your account's permissions, and can access files outside those folders. Shell execution is not sandboxed. Report permission, approval, path-containment, or output-rendering bypasses.

Model responses, web pages, tool results, and Markdown are untrusted content. Local transcription does not make chat processing local. See [Privacy](PRIVACY.md) for data flows, storage and user controls.

## Maintainer checks

CI scans Git history with redacted Gitleaks output, checks reachable vulnerabilities in both Go modules, and audits frontend dependencies. Every production npm advisory blocks CI. Moderate-or-higher development advisories block CI; low development advisories are reported for review. Scanner errors also fail the checks. Scans cannot establish that the application is vulnerability-free.

The pre-release audit identified [GHSA-gcqx-w48m-w2h4](https://github.com/advisories/GHSA-gcqx-w48m-w2h4) in the development-only `deepmerge` dependency of Svelte's Vite plugin. The full npm audit reports this under the low-severity development policy.

Dependabot checks npm, both Go modules, and GitHub Actions weekly. npm/Go version updates have a seven-day cooldown; security updates are not delayed by that setting. Changes require review. Update the Wails Go module and `@wailsio/runtime` together, keep the CI Go minor aligned with both `go.mod` files, and update native-runtime notices when its bundled libraries change.

See [GitHub security setup](https://github.com/divinecodes/ntikuma/blob/master/.github/security-setup.md) for maintainer settings and the controls enabled after publication.
