# Changelog

All notable changes are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and the project uses
[Semantic Versioning](https://semver.org/). Until 1.0, minor releases may contain
breaking API changes; they are called out below.

## [Unreleased]

### Added

- `LangfuseCostAlert`: Slack alerts for one agent's LLM cost with `budget`, `change`
  (spike/drop, e.g. week-over-week), `costPerTrace` and `unpricedUsage` rules,
  repeat/resolved notifications, ingestion delay, and Prometheus metrics.
- `LangfuseConnection.spec.uiURL` for links in notifications.

- Runbook for adding, repricing and removing model definitions (`docs/runbook-model-definitions.md`).
- `hack/bedrock-prices.py`: prints Amazon Bedrock token prices from the public AWS price list.

## [0.1.0]

### Added

- `LangfuseConnection`: a Langfuse host plus a project API key from a Secret. Verifies
  the key and records the project.
- `LangfuseModel`: project-scoped model definitions with pricing tiers, price aliases,
  per-million or per-unit prices, adoption of existing models, drift correction,
  conflict detection and `deletionPolicy`.
- Helm chart, `install.yaml`, and signed multi-arch images on ghcr.io.
- End-to-end tests against real Langfuse 3.139 and latest 3.x.
