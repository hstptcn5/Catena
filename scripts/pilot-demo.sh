#!/usr/bin/env sh
set -eu

ROOT_DIR="$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)"
EXAMPLE_DIR="$ROOT_DIR/examples/desktop-companion"
DB_PATH="$EXAMPLE_DIR/desktop-companion.db"
BIN_PATH="$ROOT_DIR/catena"
API_KEY="pilot-dev-key"
HOST="127.0.0.1"
PORT="8080"
BASE_URL="http://$HOST:$PORT"
SERVER_PID=""

cleanup() {
  if [ -n "$SERVER_PID" ] && kill -0 "$SERVER_PID" 2>/dev/null; then
    kill "$SERVER_PID" 2>/dev/null || true
    wait "$SERVER_PID" 2>/dev/null || true
  fi
}
trap cleanup EXIT INT TERM

cd "$ROOT_DIR"

if curl -fsS "$BASE_URL/health" >/dev/null 2>&1; then
  echo "A service is already responding at $BASE_URL. Stop it or change PORT in this script before running the pilot."
  exit 1
fi

echo "Building Catena..."
go build -o "$BIN_PATH" .

echo "Seeding deterministic example database..."
go run ./examples/desktop-companion/seed.go "$DB_PATH"

echo "Starting Catena for local pilot testing..."
"$BIN_PATH" serve \
  --db "$DB_PATH" \
  --host "$HOST" \
  --port "$PORT" \
  --api-key "$API_KEY" \
  --max-rows 100 \
  --backup-dir "$EXAMPLE_DIR/backups" &
SERVER_PID="$!"

echo "Waiting for health readiness..."
ready=0
for _ in 1 2 3 4 5 6 7 8 9 10; do
  if curl -fsS "$BASE_URL/health" >/dev/null 2>&1; then
    ready=1
    break
  fi
  sleep 0.5
done

if [ "$ready" -ne 1 ]; then
  echo "Catena did not become ready at $BASE_URL"
  exit 1
fi

echo "Running authenticated query..."
curl -fsS -X POST "$BASE_URL/query" \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer $API_KEY" \
  -d '{"sql":"SELECT sku, name, quantity FROM inventory ORDER BY sku","params":[]}'
echo

cat <<EOF

Catena pilot is running.

Admin UI:
  $BASE_URL

Use this development API key only for the local pilot:
  $API_KEY

Try these next commands in another terminal:

  curl -X POST $BASE_URL/query \\
    -H "Content-Type: application/json" \\
    -H "Authorization: Bearer $API_KEY" \\
    -d '{"sql":"UPDATE inventory SET quantity = quantity + ? WHERE sku = ?","params":[2,"CAT-001"]}'

  curl -X POST $BASE_URL/transaction \\
    -H "Content-Type: application/json" \\
    -H "Authorization: Bearer $API_KEY" \\
    -d '{"statements":[{"sql":"INSERT INTO inventory (sku, name, quantity) VALUES (?, ?, ?)","params":["CAT-004","Mouse",5]},{"sql":"UPDATE inventory SET quantity = quantity - ? WHERE sku = ?","params":[1,"CAT-002"]}]}'

  curl -L $BASE_URL/export \\
    -H "Authorization: Bearer $API_KEY" \\
    -o desktop-companion-export.db

Press Ctrl+C here to stop Catena and clean up the server process.
EOF

wait "$SERVER_PID"
