# Release Checklist

Use this checklist for each public Catena release.

## Before Tagging

- Run `go test ./...`.
- Run `go build .`.
- Run `.\catena.exe version`.
- Run `.\catena.exe inspect --db dev.db` against a known local database.
- Start Catena locally with an API key and test `/health`, `/query`, `/transaction`, `/metrics`, `/backup`, `/export`, and `/ws`.
- Confirm the embedded admin UI can run a query and show a SELECT result table.
- Confirm `README.md` and the release notes mention new user-facing behavior.

## Build Artifacts Locally

```powershell
.\scripts\build-release.ps1 -Version 0.3.0
```

This is useful for local verification. GitHub Actions builds and uploads the public release artifacts automatically when the tag is pushed.

Local artifacts are written to `dist/`:

- `catena-<version>-windows-amd64.exe`
- `catena-<version>-linux-amd64`
- `catena-<version>-darwin-amd64`
- `catena-<version>-darwin-arm64`
- `SHA256SUMS.txt`

## Tagging

```powershell
git tag -a v0.3.0 -m "Catena v0.3.0"
git push origin main
git push origin v0.3.0
```

## GitHub Release

After `git push origin v0.3.0`, the `Release` GitHub Actions workflow will:

- Build Windows, Linux, and macOS artifacts.
- Create `SHA256SUMS.txt`.
- Create or update the GitHub Release.
- Use `docs/releases/v0.3.0.md` as the release body when present.

Only create a release manually if the workflow fails or you need to replace the generated release.
