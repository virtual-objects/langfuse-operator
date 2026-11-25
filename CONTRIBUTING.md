# Contributing

Thanks for helping! Bug reports, docs fixes, new pricing aliases and new resource
kinds are all welcome.

## Before you start

- For anything larger than a small fix, **open an issue first** so we can agree on the
  approach before you invest time.
- Be kind and constructive; see the [Code of Conduct](CODE_OF_CONDUCT.md).
- Never post credentials, API keys or private trace data in issues or pull requests.

## Development setup

Requirements: Go (version in `go.mod`), Docker, [kind](https://kind.sigs.k8s.io/), make.

```sh
git clone https://github.com/virtual-objects/langfuse-operator.git
cd langfuse-operator
make test          # unit tests and envtest (no cluster needed)
make lint
```

Run the operator against your current kube-context:

```sh
make install       # install CRDs
make run           # run the controller locally
```

End-to-end tests start a kind cluster and a real Langfuse in Docker:

```sh
make test-e2e-langfuse LANGFUSE_VERSION=3.139
```

## Making changes

1. Fork the repository and create a branch from `main`.
2. If you change `api/`, run `make manifests generate` and commit the generated files.
   If you change RBAC markers or CRDs, also run
   `kubebuilder edit --plugins=helm/v1-alpha` to refresh the Helm chart. That command
   also re-creates `.github/workflows/test-chart.yml`; delete it again, because the
   `chart` job in `ci.yml` covers it. Check that `dist/chart/values.yaml` and
   `Chart.yaml` kept their image and metadata.
3. Add or update tests. Bug fixes should come with a test that fails without the fix.
4. Run `make test lint` and make sure they pass.
5. Update the README or `docs/` for user-visible changes, and add a line under
   `Unreleased` in [CHANGELOG.md](CHANGELOG.md).
6. Open a pull request and fill in the template.

### Commit messages

Use [Conventional Commits](https://www.conventionalcommits.org/), for example:

```
feat(model): support pricePerThousand
fix(connection): report Unauthorized for 403 responses
docs: add External Secrets example
```

### Adding a new Langfuse resource kind

Follow [docs/extending.md](docs/extending.md).

## Review

A maintainer will review your pull request, usually within a week. CI (lint, unit,
envtest, chart install, and e2e against Langfuse 3.139 and the latest 3.x) must pass
before merge. Pull requests are squash-merged.

## License

By contributing you agree that your contributions are licensed under the
[Apache License 2.0](LICENSE), the license of this project. No separate contributor
license agreement is required.
