# Security Policy

## Supported Versions

Security fixes are applied to the latest release and the default branch.

## Reporting a Vulnerability

Please do not open a public issue for a suspected vulnerability.

Use GitHub's private vulnerability reporting for this repository. Include:

- the affected Catena version or commit;
- the deployment configuration;
- steps to reproduce;
- the expected and observed behavior;
- any proof of concept that does not expose real credentials or data.

You should receive an acknowledgement within seven days. Please allow time for a fix and coordinated disclosure before publishing details.

## Deployment Boundary

Catena exposes raw SQL and is intended for trusted clients and controlled networks. An API key is not a substitute for row-level authorization or tenant isolation.

For production deployments:

- bind Catena to a private interface or place it behind a TLS reverse proxy;
- configure a strong API key and a strict CORS origin;
- use read-only mode for public datasets;
- enable rate limiting;
- never place API keys in logs, issue reports, or examples based on real systems;
- keep backups outside the served application directory.

See [docs/PRODUCTION.md](docs/PRODUCTION.md) for the full checklist.
