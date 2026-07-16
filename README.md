# Catena

Catena (Latin for *"chain"*) is a lightweight, zero-dependency, single-binary database server that exposes any SQLite database file over HTTP and WebSockets. 

By leveraging SQLite's **WAL (Write-Ahead Logging)** mode coupled with a Go-level **serialized write queue**, Catena enables high-performance concurrent reads while safely executing writes without database locks or `SQLITE_BUSY` errors.

---

## Features

- ⚡ **Zero-Dependency & Pure Go**: Powered by `modernc.org/sqlite`. No CGO, no GCC requirements, and compiles easily for any platform.
- 🔄 **WAL-Powered Concurrency**: Unlimited parallel reads, with writes serialized under a Go Mutex to ensure lock-free operations.
- 🌐 **Clean HTTP JSON API**: Execute raw SQL queries (parameterized to prevent SQL injection) via a single POST endpoint.
- 📡 **Real-time Pub/Sub WebSockets**: Subscribe to table updates and receive instant notifications whenever writes are executed on monitored tables.
- 🛠️ **CLI-Driven Configuration**: Fully configurable via CLI flags (Cobra) or YAML files (Viper).
- 🛡️ **Graceful Shutdown**: Automatically flushes SQLite transactions and closes connection pools safely upon receiving interruption signals.

---

## Quick Start

### Option A: Running the Native Binary

#### 1. Build the Binary
Clone the repository and compile the single executable binary:
```bash
go build -o catena
```

#### 2. Run the Server
Launch the server specifying the database path and port:
```bash
# Serves the database 'mydb.db' on port 8080
./catena serve -d mydb.db -p 8080
```

### Option B: Running with Docker

#### 1. Build & Run the Container
You can build and run Catena in an isolated Docker container:
```bash
# Build the image locally
docker build -t catena .

# Run the container mapping port 8080 and mounting a database volume
docker run -d --name catena_db -p 8080:8080 -v ./data:/app/data catena
```
This will automatically create and serve `catena.db` inside your local `./data` directory.

### Option C: Running with Docker Compose

Simply run:
```bash
docker-compose up -d
```
The database will be served at `http://localhost:8080` and the file will be persisted in `./data/catena.db` on your host filesystem.

---

## API Reference

### 1. Health Check
Checks if the server is running.
- **URL**: `/health`
- **Method**: `GET`
- **Response**:
  ```json
  {
    "status": "ok"
  }
  ```

### 2. Execute Queries
Runs read or write SQL statements. The server automatically routes statements starting with `SELECT`/`PRAGMA`/`EXPLAIN` to a concurrent read pool, and routes all other mutating statements to a serialized write queue.
- **URL**: `/query`
- **Method**: `POST`
- **Headers**: `Content-Type: application/json`

#### A. Read Query Example (SELECT)
- **Request Body**:
  ```json
  {
    "sql": "SELECT id, name, role FROM users WHERE role = ?",
    "params": ["developer"]
  }
  ```
- **Response**:
  ```json
  {
    "columns": ["id", "name", "role"],
    "rows": [
      [1, "Alice", "developer"],
      [2, "Bob", "developer"]
    ]
  }
  ```

#### B. Write Query Example (INSERT/UPDATE/DELETE/CREATE)
- **Request Body**:
  ```json
  {
    "sql": "INSERT INTO users (name, role) VALUES (?, ?)",
    "params": ["Charlie", "manager"]
  }
  ```
- **Response**:
  ```json
  {
    "last_insert_id": 3,
    "rows_affected": 1
  }
  ```

### 3. Real-Time WebSockets
Establish a WebSocket connection to subscribe to table-level modifications.
- **URL**: `/ws`
- **Method**: `GET` (upgrades connection)

#### Subscribe to a Table
Send the following JSON message through the WebSocket to receive updates when the `users` table changes:
```json
{
  "type": "subscribe",
  "table": "users"
}
```

#### Unsubscribe from a Table
Send this message to stop receiving updates for the `users` table:
```json
{
  "type": "unsubscribe",
  "table": "users"
}
```

#### Update Notification
Whenever a successful write statement (e.g. `INSERT/UPDATE/DELETE`) is performed on the subscribed table, the server broadcasts this event:
```json
{
  "type": "update",
  "table": "users"
}
```

---

## Configuration

You can configure the server using a `catena.yaml` file in the working directory:
```yaml
db: "production.db"
port: 8080
host: "0.0.0.0"
debug: true
```
CLI arguments will automatically override file configurations. Run `.\catena.exe serve --help` to view all CLI flags.

---

## License
This project is licensed under the MIT License.
