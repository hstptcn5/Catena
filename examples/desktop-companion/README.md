# Desktop Companion Example

This example shows Catena as a sidecar for a desktop or local application that already owns an SQLite file.

The scenario uses an `inventory` table. Catena exposes the database to a trusted dashboard or automation script so the user can inspect tables, read rows, perform authorized writes, subscribe to WebSocket updates, and export or back up the database.

Important limitation: Catena emits WebSocket events only for writes performed through Catena. Writes made directly by unrelated SQLite connections are not observed.

## Seed Database

From the repository root:

```bash
go run ./examples/desktop-companion/seed.go ./examples/desktop-companion/desktop-companion.db
```

## Start Catena

```bash
go build -o catena
./catena serve --db ./examples/desktop-companion/desktop-companion.db --host 127.0.0.1 --port 8080 --api-key pilot-dev-key --max-rows 100
```

On Windows:

```powershell
go build -o catena.exe .
.\catena.exe serve --db .\examples\desktop-companion\desktop-companion.db --host 127.0.0.1 --port 8080 --api-key pilot-dev-key --max-rows 100
```

The API key above is for local pilot testing only. Do not use it in production.

## Inspect Tables

```bash
./catena inspect --db ./examples/desktop-companion/desktop-companion.db
```

## Read Rows

```bash
curl -X POST http://127.0.0.1:8080/query \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer pilot-dev-key" \
  -d '{"sql":"SELECT sku, name, quantity FROM inventory ORDER BY sku","params":[]}'
```

## Parameterized Write

```bash
curl -X POST http://127.0.0.1:8080/query \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer pilot-dev-key" \
  -d '{"sql":"UPDATE inventory SET quantity = quantity + ? WHERE sku = ?","params":[2,"CAT-001"]}'
```

## Atomic Transaction

```bash
curl -X POST http://127.0.0.1:8080/transaction \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer pilot-dev-key" \
  -d '{
    "statements": [
      { "sql": "INSERT INTO inventory (sku, name, quantity) VALUES (?, ?, ?)", "params": ["CAT-004", "Mouse", 5] },
      { "sql": "UPDATE inventory SET quantity = quantity - ? WHERE sku = ?", "params": [1, "CAT-002"] }
    ]
  }'
```

## WebSocket Subscription

Open the embedded admin UI:

```text
http://127.0.0.1:8080
```

Enter `pilot-dev-key`, set the realtime table to `inventory`, and click `Connect WS`.

Alternatively, open `examples/desktop-companion/ws-watch.html` in a browser and click `Connect`.

Run the parameterized write again. The WebSocket log should show an update event for `inventory`.

Direct writes made by a separate SQLite tool will update the database file, but they will not create Catena WebSocket events.

## Read-Only Mode

Stop Catena and restart it in read-only mode:

```bash
./catena serve --db ./examples/desktop-companion/desktop-companion.db --host 127.0.0.1 --port 8080 --api-key pilot-dev-key --readonly
```

Writes through `/query` and `/transaction` now return `403`.

## Export and Backup

Export:

```bash
curl -L http://127.0.0.1:8080/export \
  -H "Authorization: Bearer pilot-dev-key" \
  -o desktop-companion-export.db
```

Backup:

```bash
curl -X POST http://127.0.0.1:8080/backup \
  -H "Authorization: Bearer pilot-dev-key"
```
