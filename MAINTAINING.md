# Maintainer guide

How this repository is run day to day. It is written for maintainers who are new to
running an open-source project.

## One-time GitHub setup

1. **Settings → General**
   - Features: enable Issues and Discussions; disable Wiki (docs live in the repo).
   - Pull requests: allow **squash merging** only; enable "Automatically delete head
     branches".
2. **Settings → Branches → Add rule for `main`**
   - Require a pull request before merging, with 1 approval and CODEOWNERS review.
   - Require status checks: `Lint and verify`, `Unit and envtest`, `Helm chart`,
     `Langfuse 3.139`, `Langfuse 3`.
   - Do not allow force pushes or deletion.
3. **Settings → Code security**: enable Dependabot alerts and security updates, secret
   scanning with push protection, and **Private vulnerability reporting** (SECURITY.md
   relies on it).
4. **Settings → Actions → General**: workflow permissions "Read repository contents";
   the release workflow requests the extra permissions it needs.
5. After the first release, open **Packages → langfuse-operator → Package settings**
   and set visibility to **Public** (also for `charts/langfuse-operator`).
6. Add topics: `kubernetes`, `operator`, `langfuse`, `llm`, `observability`.
7. **Settings → General → Social preview**: upload `docs/assets/logo-512.png`.

## Handling issues

- Label new issues: `bug`, `enhancement`, `question`, `good first issue`,
  `help wanted`.
- Reply within about a week, even if only to say "thanks, will look".
- Ask for the YAML, `kubectl describe` output and versions if they are missing.
- Close issues that are fixed (reference them in the PR with `Fixes #N`) or that stay
  unanswered for 30 days after a question.

## Reviewing pull requests

- CI must be green.
- Check for tests, docs, and a CHANGELOG entry for user-visible changes.
- Check that `make manifests generate` output is committed (CI verifies this).
- Squash-merge with a Conventional Commit title.
- Dependabot PRs: merge when CI is green; read the release notes for major bumps.

## Releasing

1. Move the `Unreleased` entries in CHANGELOG.md under a new version heading, and
   update the version in `dist/chart/Chart.yaml` (`version`, `appVersion`), the image
   tag in `dist/chart/values.yaml` and `config/manager/kustomization.yaml`, and the
   install commands in README.md. Merge that PR.
2. Tag and push from an up-to-date `main`:
   ```sh
   git tag -a v0.2.0 -m "v0.2.0"
   git push origin v0.2.0
   ```
3. The Release workflow tests, builds and signs the image, pushes the Helm chart, and
   creates the GitHub release with `install.yaml`. Check the release notes it generated
   and edit them if needed.
4. Use pre-release tags such as `v0.2.0-rc.1` to try a release; they are marked as
   pre-releases automatically.

Versioning: follow [SemVer](https://semver.org/). While the API is `v1alpha1`, minor
versions may change it; say so clearly in the CHANGELOG. Breaking changes to stable
APIs later need a new API version and a deprecation period.

## Security reports

Private reports arrive under **Security → Advisories**. Acknowledge within 5 working
days, fix on a private fork created from the advisory, release, then publish the
advisory (GitHub can request a CVE for you).

## Getting the word out

- Announce releases in the Langfuse community (GitHub Discussions, Discord).
- Consider proposing a link from the Langfuse self-hosting docs once the project has
  users.
