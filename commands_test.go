package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

const testCommandsYAML = `
version: 1
commands:
  create_sale:
    version: 1
    description: Create a sale and emit a completion event.
    permission: sales.create
    idempotency:
      required: true
      ttl: 168h
    input:
      sale_id:
        type: string
        required: true
      customer_id:
        type: string
      total_cents:
        type: integer
        required: true
        minimum: 1
    statements:
      - sql: |
          INSERT INTO sales(id,customer_id,total_cents)
          VALUES(:sale_id,:customer_id,:total_cents)
    result:
      sql: |
        SELECT id,customer_id,total_cents FROM sales WHERE id=:sale_id
    emit:
      - event_id: "evt_:command_id"
        topic: sale.completed
        destination: accounting
        payload:
          sale_id: :sale_id
          customer_id: :customer_id
          total_cents: :total_cents
          command_id: :command_id
`

func newCommandFixture(t *testing.T) (*DB, *CommandExecutor, CommandActor) {
	t.Helper()
	db, err := OpenDB(t.TempDir()+"/commands.db", nil)
	if err != nil {
		t.Fatalf("OpenDB: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	schema := `
	CREATE TABLE sales(
		id TEXT PRIMARY KEY,
		customer_id TEXT,
		total_cents INTEGER NOT NULL
	);
	CREATE TABLE anetac_outbox (
		id TEXT PRIMARY KEY,
		topic TEXT NOT NULL,
		destination TEXT NOT NULL,
		payload TEXT NOT NULL,
		status TEXT NOT NULL DEFAULT 'pending',
		generation INTEGER NOT NULL DEFAULT 0,
		attempts INTEGER NOT NULL DEFAULT 0,
		created_at_ms INTEGER NOT NULL,
		available_at_ms INTEGER NOT NULL,
		lease_owner TEXT,
		lease_until_ms INTEGER,
		delivered_at_ms INTEGER,
		last_http_status INTEGER,
		last_error_code TEXT,
		last_error TEXT,
		command_id TEXT,
		correlation_id TEXT,
		causation_id TEXT,
		updated_at_ms INTEGER NOT NULL
	);`
	if _, err := db.sqliteDB.Exec(schema); err != nil {
		t.Fatalf("create fixture schema: %v", err)
	}
	registry, err := ParseCommandRegistry([]byte(testCommandsYAML))
	if err != nil {
		t.Fatalf("ParseCommandRegistry: %v", err)
	}
	executor, err := NewCommandExecutor(db, registry, 256<<10, 1<<20)
	if err != nil {
		t.Fatalf("NewCommandExecutor: %v", err)
	}
	actor := CommandActor{
		ID:          "mobile-app",
		Permissions: map[string]struct{}{"sales.create": {}, "receipts.read": {}},
	}
	return db, executor, actor
}

func TestCommandExecuteIdempotencyAndReceipt(t *testing.T) {
	db, executor, actor := newCommandFixture(t)
	input := map[string]any{
		"sale_id":     "sale-1",
		"customer_id": "customer-1",
		"total_cents": json.Number("125000"),
	}
	request := ExecuteCommandRequest{
		CommandName: "create_sale", IdempotencyKey: "mobile-request-1",
		CorrelationID: "checkout-1", Actor: actor, Input: input,
	}
	first, err := executor.Execute(context.Background(), request)
	if err != nil {
		t.Fatalf("first Execute: %v", err)
	}
	if first.DatabaseStatus != "committed" || first.DeliveryStatus != "pending" || first.Complete {
		t.Fatalf("unexpected first receipt: %+v", first)
	}
	if len(first.Events) != 1 || first.Events[0].CorrelationID != "checkout-1" {
		t.Fatalf("unexpected first event: %+v", first.Events)
	}

	second, err := executor.Execute(context.Background(), request)
	if err != nil {
		t.Fatalf("idempotent Execute: %v", err)
	}
	if second.CommandID != first.CommandID || !second.IdempotentReplay {
		t.Fatalf("expected stored receipt replay: first=%+v second=%+v", first, second)
	}
	for table, expected := range map[string]int{"sales": 1, "anetac_outbox": 1, "catena_command_receipts": 1} {
		var count int
		if err := db.sqliteDB.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&count); err != nil {
			t.Fatalf("count %s: %v", table, err)
		}
		if count != expected {
			t.Fatalf("expected %d row in %s, got %d", expected, table, count)
		}
	}

	conflicting := request
	conflicting.Input = map[string]any{
		"sale_id": "sale-2", "customer_id": "customer-1", "total_cents": json.Number("500"),
	}
	if _, err := executor.Execute(context.Background(), conflicting); !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("expected idempotency conflict, got %v", err)
	}
}

func TestCommandTransactionRollsBackEveryIntegratedRecord(t *testing.T) {
	db, _, actor := newCommandFixture(t)
	registry, err := ParseCommandRegistry([]byte(`
version: 1
commands:
  broken_sale:
    permission: sales.create
    idempotency:
      required: true
    input:
      sale_id: {type: string, required: true}
    statements:
      - sql: INSERT INTO sales(id,total_cents) VALUES(:sale_id,100)
      - sql: INSERT INTO table_that_does_not_exist(id) VALUES(:sale_id)
`))
	if err != nil {
		t.Fatalf("ParseCommandRegistry: %v", err)
	}
	executor, err := NewCommandExecutor(db, registry, 256<<10, 1<<20)
	if err != nil {
		t.Fatalf("NewCommandExecutor: %v", err)
	}
	_, err = executor.Execute(context.Background(), ExecuteCommandRequest{
		CommandName: "broken_sale", IdempotencyKey: "broken-1", Actor: actor,
		Input: map[string]any{"sale_id": "sale-broken"},
	})
	if err == nil {
		t.Fatal("expected command failure")
	}
	for _, table := range []string{"sales", "catena_command_receipts", "anetac_outbox"} {
		var count int
		if err := db.sqliteDB.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&count); err != nil {
			t.Fatalf("count %s: %v", table, err)
		}
		if count != 0 {
			t.Fatalf("expected rollback to leave %s empty, got %d", table, count)
		}
	}
}

func TestReceiptAggregatesDeliveredAndDeadStates(t *testing.T) {
	db, executor, actor := newCommandFixture(t)
	receipt, err := executor.Execute(context.Background(), ExecuteCommandRequest{
		CommandName: "create_sale", IdempotencyKey: "state-1", Actor: actor,
		Input: map[string]any{
			"sale_id": "sale-state", "customer_id": nil, "total_cents": json.Number("100"),
		},
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	eventID := receipt.Events[0].EventID
	if _, err := db.sqliteDB.Exec(`
		UPDATE anetac_outbox SET status='delivered',attempts=2,delivered_at_ms=updated_at_ms+1000,updated_at_ms=updated_at_ms+1000
		WHERE id=?`, eventID); err != nil {
		t.Fatalf("mark delivered: %v", err)
	}
	delivered, err := executor.GetReceipt(context.Background(), receipt.CommandID)
	if err != nil {
		t.Fatalf("GetReceipt: %v", err)
	}
	if delivered.DeliveryStatus != "delivered" || !delivered.Complete || delivered.Events[0].Attempts != 2 {
		t.Fatalf("unexpected delivered receipt: %+v", delivered)
	}

	if _, err := db.sqliteDB.Exec(`
		UPDATE anetac_outbox SET status='dead',delivered_at_ms=NULL,last_error_code='http_4xx',updated_at_ms=updated_at_ms+1000
		WHERE id=?`, eventID); err != nil {
		t.Fatalf("mark dead: %v", err)
	}
	dead, err := executor.GetReceipt(context.Background(), receipt.CommandID)
	if err != nil {
		t.Fatalf("GetReceipt dead: %v", err)
	}
	if dead.DeliveryStatus != "dead" || !dead.Complete || dead.Events[0].LastErrorCode == nil {
		t.Fatalf("unexpected dead receipt: %+v", dead)
	}
}

func TestCommandHTTPAuthPermissionAndConflict(t *testing.T) {
	_, executor, _ := newCommandFixture(t)
	authorizer := &CommandAuthorizer{credentials: []commandCredential{{
		token: "mobile-secret",
		actor: CommandActor{
			ID: "mobile-app", Permissions: map[string]struct{}{"sales.create": {}, "receipts.read": {}},
		},
	}}}
	server := NewServer(executor.db, NewHub(), ServerConfig{
		CommandExecutor: executor, CommandAuth: authorizer, BodyLimitBytes: 1 << 20,
		DisableRawSQL: true,
	})
	body := `{"sale_id":"sale-http","customer_id":"customer-http","total_cents":125000}`
	request := func(token, key, payload string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/v1/commands/create_sale", bytes.NewBufferString(payload))
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Idempotency-Key", key)
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		server.ServeHTTP(rec, req)
		return rec
	}
	if got := request("wrong", "http-1", body).Code; got != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", got)
	}
	first := request("mobile-secret", "http-1", body)
	if first.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d: %s", first.Code, first.Body.String())
	}
	replay := request("mobile-secret", "http-1", body)
	if replay.Code != http.StatusOK {
		t.Fatalf("expected 200 replay, got %d: %s", replay.Code, replay.Body.String())
	}
	conflict := request("mobile-secret", "http-1", `{"sale_id":"different","customer_id":"x","total_cents":1}`)
	if conflict.Code != http.StatusConflict {
		t.Fatalf("expected 409, got %d: %s", conflict.Code, conflict.Body.String())
	}

	raw := httptest.NewRequest(http.MethodPost, "/query", bytes.NewBufferString(`{"sql":"SELECT 1"}`))
	rawRec := httptest.NewRecorder()
	server.ServeHTTP(rawRec, raw)
	if rawRec.Code != http.StatusNotFound {
		t.Fatalf("expected raw SQL to be disabled, got %d", rawRec.Code)
	}
}

func TestCommandConfigurationFailsClosed(t *testing.T) {
	_, err := ParseCommandRegistry([]byte(`
version: 1
commands:
  unsafe:
    permission: sales.create
    unknown_field: true
    idempotency: {required: true}
    statements:
      - sql: INSERT INTO sales(id) VALUES(:id)
`))
	if err == nil {
		t.Fatal("expected unknown command field to fail")
	}
	_, err = ParseCommandAuthorizer([]byte(`
version: 1
tokens:
  mobile:
    token_env: MISSING_TOKEN
    actor_id: mobile
    permissions: [sales.create]
`), func(string) (string, bool) { return "", false })
	if err == nil {
		t.Fatal("expected missing configured token to fail")
	}
}
