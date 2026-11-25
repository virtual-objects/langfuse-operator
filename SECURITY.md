# Security policy

## Reporting a vulnerability

Please **do not open a public issue** for security problems.

Report them privately through
[GitHub private vulnerability reporting](https://github.com/virtual-objects/langfuse-operator/security/advisories/new).
Include the affected version, a description, and steps to reproduce.

You will get an acknowledgement within 5 working days. Once the issue is confirmed, we
will agree on a disclosure date with you, release a fix, and publish an advisory that
credits you unless you prefer otherwise.

## Supported versions

Only the latest minor release receives security fixes.

## Scope notes

- The operator handles Langfuse project API keys read from Kubernetes Secrets. Issues
  that could expose those keys (logs, events, status, metrics) are in scope.
- Vulnerabilities in Langfuse itself should be reported to the
  [Langfuse project](https://github.com/langfuse/langfuse/security).
