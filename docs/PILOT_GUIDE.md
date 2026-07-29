# 10-Minute Catena Pilot Guide

This guide helps a qualified SQLite user verify Catena's primary value proposition quickly: connect Catena to an existing SQLite file, use it through HTTP and WebSocket, and confirm the security and realtime boundaries.

## 1. Who This Pilot Is For

This pilot is for you if:

- You already use SQLite on one machine.
- A trusted dashboard, script, service, or nearby device needs database access.
- You want to keep the original `.db` file.
- You do not want to build a custom API just for this database.
- You do not need replication, multi-tenancy, row-level permissions, or user accounts.

## 2. Requirements

- Go installed, if building from source.
- A Catena binary, if downloading from GitHub Releases.
- An SQLite `.db` file you can safely test with.
- A private or local network boundary.

The development API key in this guide is for local pilot testing only.

## 3. Download Or Build Catena

Build from source:

```bash
go build -o catena
```

Windows:

```powershell
go build -o catena.exe .
```

Or download a release binary from GitHub Releases.

## 4. Run Catena Against An Existing `.db` File

Unix:

```bash
./catena serve --db ./examples/desktop-companion/desktop-companion.db --host 127.0.0.1 --port 8080 --api-key pilot-dev-key --max-rows 100
```

Windows:

```powershell
.\catena.exe serve --db .\examples\desktop-companion\desktop-companion.db --host 127.0.0.1 --port 8080 --api-key pilot-dev-key --max-rows 100
```

If you do not have a safe test database, seed the bundled example first:

Unix:

```bash
go run ./examples/desktop-companion/seed.go ./examples/desktop-companion/desktop-companion.db
```

Windows:

```powershell
go run .\examples\desktop-companion\seed.go .\examples\desktop-companion\desktop-companion.db
```

## 5. Connect Through The Admin UI

Open:

```text
http://127.0.0.1:8080
```

Enter:

```text
pilot-dev-key
```

## 6. Execute The First Read

Unix:

```bash
curl -X POST http://127.0.0.1:8080/query \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer pilot-dev-key" \
  -d '{"sql":"SELECT sku, name, quantity FROM inventory ORDER BY sku","params":[]}'
```

Windows:

```powershell
Invoke-RestMethod http://127.0.0.1:8080/query `
  -Method Post `
  -Headers @{ Authorization = "Bearer pilot-dev-key" } `
  -ContentType "application/json" `
  -Body '{"sql":"SELECT sku, name, quantity FROM inventory ORDER BY sku","params":[]}'
```

## 7. Execute A Safe Parameterized Write

Unix:

```bash
curl -X POST http://127.0.0.1:8080/query \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer pilot-dev-key" \
  -d '{"sql":"UPDATE inventory SET quantity = quantity + ? WHERE sku = ?","params":[2,"CAT-001"]}'
```

Windows:

```powershell
Invoke-RestMethod http://127.0.0.1:8080/query `
  -Method Post `
  -Headers @{ Authorization = "Bearer pilot-dev-key" } `
  -ContentType "application/json" `
  -Body '{"sql":"UPDATE inventory SET quantity = quantity + ? WHERE sku = ?","params":[2,"CAT-001"]}'
```

## 8. Subscribe To WebSocket Events

In the admin UI, set realtime table to:

```text
inventory
```

Click `Connect WS`, then run the write command again.

Expected event:

```json
{
  "type": "update",
  "table": "inventory",
  "operation": "update",
  "rows_affected": 1,
  "timestamp": "..."
}
```

Important: Catena does not detect writes performed directly by unrelated SQLite connections. WebSocket events only cover writes performed through Catena.

## 9. Test Read-Only Mode

Stop Catena and restart with `--readonly`.

Unix:

```bash
./catena serve --db ./examples/desktop-companion/desktop-companion.db --host 127.0.0.1 --port 8080 --api-key pilot-dev-key --readonly
```

Windows:

```powershell
.\catena.exe serve --db .\examples\desktop-companion\desktop-companion.db --host 127.0.0.1 --port 8080 --api-key pilot-dev-key --readonly
```

Run the safe write again. Expected result: HTTP `403`.

## 10. Export And Back Up The Database

Export:

Unix:

```bash
curl -L http://127.0.0.1:8080/export \
  -H "Authorization: Bearer pilot-dev-key" \
  -o desktop-companion-export.db
```

Windows:

```powershell
Invoke-WebRequest http://127.0.0.1:8080/export `
  -Headers @{ Authorization = "Bearer pilot-dev-key" } `
  -OutFile desktop-companion-export.db
```

Backup:

Unix:

```bash
curl -X POST http://127.0.0.1:8080/backup \
  -H "Authorization: Bearer pilot-dev-key"
```

Windows:

```powershell
Invoke-RestMethod http://127.0.0.1:8080/backup `
  -Method Post `
  -Headers @{ Authorization = "Bearer pilot-dev-key" }
```

## 11. Production Boundary And Security Warnings

Catena exposes SQL by design. Use it only with trusted clients and a clear network boundary.

For production-like use:

- Use a strong API key.
- Bind to `127.0.0.1` behind a TLS reverse proxy, or protect the service with a firewall/VPN.
- Set strict `--cors-origin`.
- Enable `--rate-limit`.
- Use `--readonly` for public datasets.
- Back up regularly.
- Do not expose raw SQL to untrusted users.

See `docs/PRODUCTION.md` for more deployment detail.

## 12. Report Pilot Results

Please report:

- Whether you already had a workaround.
- What creates your SQLite file.
- How long it took to start Catena.
- Which features you actually used.
- Whether Catena is better than your current workaround.
- Any blocking errors.

Use `docs/PILOT_RESULTS.md` to record five qualified pilot results.
