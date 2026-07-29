# Pilot Readiness Audit

Date: 2026-07-29

## Implemented Product Promise

- Catena starts as a single binary and serves an existing SQLite database file.
- `/health` reports server readiness and version.
- `/query` executes one parameterized SQL statement.
- `/transaction` executes atomic write batches and rolls back on failure.
- API key authentication protects `/query`, `/transaction`, `/ws`, `/export`, `/backup`, and `/metrics`.
- Read-only mode rejects write statements.
- WebSocket clients can subscribe to table update events for writes performed through Catena.
- `/export` downloads a checkpointed database copy.
- `/backup` creates a server-side backup file.
- `/metrics` exposes lightweight process-local counters.
- The embedded admin UI supports query execution, realtime events, backup, export, and metrics.
- GitHub Actions provide CI and tag-based release builds.

## Documented But Previously Not Tested End To End

- Full first-user journey from a seeded existing database through health, auth, read, write, transaction, WebSocket event, export, backup, and shutdown.
- WebSocket limitation that direct unrelated SQLite writes do not create Catena events.
- Query row limit behavior.
- Export and backup validity as readable SQLite files.
- Graceful shutdown of a running server and WebSocket hub.

## Pilot Blockers Found

- The original pilot branch was based on an older `main` and conflicted with later security hardening.
- `go test -race ./...` could not run in the local Windows environment because CGO race builds require `gcc`, which is not installed.
- The WebSocket hub had no stop path, making shutdown/leak testing incomplete.
- There was no canonical pilot example or 10-minute pilot guide.

## Fixes In This Branch

- Rebased the pilot work onto current `main` and preserved the existing hardening boundaries.
- Added a canonical desktop companion example.
- Added deterministic pilot scripts for Windows and Unix.
- Added end-to-end pilot test coverage.
- Verified query row limit coverage using the existing `--max-rows` / `max_rows` / `MaxRows` contract.
- Added stoppable WebSocket hub lifecycle.
- Added auth regression coverage for Bearer, `X-API-Key`, invalid keys, WebSocket query token auth, and HTTP query token rejection.
- Added read-only E2E coverage for both SQLite physical read-only mode and HTTP write rejection.
- Added pilot documentation and validation record.
