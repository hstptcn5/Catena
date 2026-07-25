# Catena

Catena is a lightweight single-binary server that exposes any SQLite database file over HTTP and WebSockets.

It is designed for small apps, internal tools, local-first products, edge devices, and prototypes that need a simple network API in front of a normal SQLite file.

## Features

- Pure Go SQLite driver via `modernc.org/sqlite`
- WAL mode for concurrent reads
- Serialized writes to avoid writer contention
- `POST /query` for parameterized SQL
- `POST /transaction` for atomic write batches
- `GET /ws` for realtime table update events
- Optional API key authentication
- Optional read-only mode
- Configurable CORS, request body limit, query timeout, and rate limit
- Embedded admin UI at `/`
- OpenAPI document at `/openapi.json`
- Minimal JavaScript and Python clients in `sdk/`

## Quick Start

Build and run:

```bash
go build -o catena
./catena serve --db mydb.db --port 8080
```

With an API key:

```bash
./catena serve --db mydb.db --api-key "dev-secret"
```

Open the admin UI:

```text
http://localhost:8080
```

## Configuration

CLI flags:

```text
--db              SQLite database path
--host            interface to bind to
--port            HTTP port
--api-key         require an API key for /query, /transaction and /ws
--readonly        reject write statements
--cors-origin     Access-Control-Allow-Origin value
--body-limit      maximum JSON request body size in bytes
--query-timeout   maximum query duration
--rate-limit      per-client requests per minute; 0 disables rate limiting
```

Example `catena.yaml`:

```yaml
db: "production.db"
host: "0.0.0.0"
port: 8080
api_key: "change-me"
readonly: false
cors_origin: "https://example.com"
body_limit: 1048576
query_timeout: "30s"
rate_limit: 120
```

Environment variables use the `CATENA_` prefix:

```bash
CATENA_DB=production.db
CATENA_API_KEY=change-me
CATENA_PORT=8080
```

## HTTP API

Health:

```bash
curl http://localhost:8080/health
```

Read query:

```bash
curl -X POST http://localhost:8080/query \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer dev-secret" \
  -d '{"sql":"SELECT name FROM sqlite_master WHERE type = ?","params":["table"]}'
```

Write query:

```bash
curl -X POST http://localhost:8080/query \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer dev-secret" \
  -d '{"sql":"INSERT INTO users (name) VALUES (?)","params":["Alice"]}'
```

Atomic write batch:

```bash
curl -X POST http://localhost:8080/transaction \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer dev-secret" \
  -d '{
    "statements": [
      { "sql": "INSERT INTO users (name) VALUES (?)", "params": ["A"] },
      { "sql": "INSERT INTO users (name) VALUES (?)", "params": ["B"] }
    ]
  }'
```

Error responses use a stable shape:

```json
{
  "code": "invalid_sql",
  "message": "multiple SQL statements are disabled",
  "details": ""
}
```

## WebSocket

Connect to:

```text
ws://localhost:8080/ws?token=dev-secret
```

Subscribe to one table:

```json
{ "type": "subscribe", "table": "users" }
```

Subscribe to every table:

```json
{ "type": "subscribe", "table": "*" }
```

Update event:

```json
{
  "type": "update",
  "table": "users",
  "operation": "insert",
  "rows_affected": 1,
  "timestamp": "2026-07-25T12:00:00Z"
}
```

## SDKs

JavaScript:

```js
import { CatenaClient } from "./sdk/catena.js";

const catena = new CatenaClient("http://localhost:8080", { apiKey: "dev-secret" });
const rows = await catena.query("SELECT * FROM users WHERE role = ?", ["admin"]);
catena.subscribe("users", console.log);
```

Python:

```python
from sdk.catena import CatenaClient

catena = CatenaClient("http://localhost:8080", api_key="dev-secret")
print(catena.query("SELECT * FROM users"))
```

## Docker

```bash
docker build -t catena .
docker run --rm -p 8080:8080 -v ./data:/app/data catena
```

Or:

```bash
docker compose up -d
```

## Current Scope

Catena intentionally stays small. It is not a full backend framework, an ORM, or a distributed database. It exposes SQLite safely enough for controlled environments, but public deployments should use API keys, strict CORS, rate limits, backups, and a reverse proxy with TLS.

## License

MIT
