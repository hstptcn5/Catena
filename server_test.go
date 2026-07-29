package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
)

func newTestServer(t *testing.T, config ServerConfig) (*Server, func()) {
	t.Helper()
	dbFile := t.TempDir() + "/catena_test.db"
	db, err := OpenDB(dbFile, nil)
	if err != nil {
		t.Fatalf("OpenDB failed: %v", err)
	}
	srv := NewServer(db, NewHub(), config)
	return srv, func() {
		db.Close()
		os.Remove(dbFile)
	}
}

func TestAuthRequired(t *testing.T) {
	srv, cleanup := newTestServer(t, ServerConfig{APIKey: "secret"})
	defer cleanup()

	req := httptest.NewRequest(http.MethodPost, "/query", bytes.NewBufferString(`{"sql":"SELECT 1"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}
}

func TestHealthIncludesVersion(t *testing.T) {
	srv, cleanup := newTestServer(t, ServerConfig{})
	defer cleanup()

	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	var payload map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("failed to decode health response: %v", err)
	}
	if payload["version"] != appVersion {
		t.Fatalf("expected version %q, got %q", appVersion, payload["version"])
	}
}

func TestWebSocketOriginRejected(t *testing.T) {
	srv, cleanup := newTestServer(t, ServerConfig{CORSOrigin: "https://allowed.example"})
	defer cleanup()

	req := httptest.NewRequest(http.MethodGet, "/ws", nil)
	req.Header.Set("Origin", "https://blocked.example")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", rec.Code)
	}
}

func TestReadOnlyRejectsWrites(t *testing.T) {
	srv, cleanup := newTestServer(t, ServerConfig{ReadOnly: true})
	defer cleanup()

	req := httptest.NewRequest(http.MethodPost, "/query", bytes.NewBufferString(`{"sql":"CREATE TABLE users (id INTEGER)"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", rec.Code)
	}
}

func TestTransactionRollsBack(t *testing.T) {
	srv, cleanup := newTestServer(t, ServerConfig{})
	defer cleanup()

	createReq := httptest.NewRequest(http.MethodPost, "/query", bytes.NewBufferString(`{"sql":"CREATE TABLE users (id INTEGER PRIMARY KEY, name TEXT)"}`))
	createReq.Header.Set("Content-Type", "application/json")
	createRec := httptest.NewRecorder()
	srv.ServeHTTP(createRec, createReq)
	if createRec.Code != http.StatusOK {
		t.Fatalf("create table failed with status %d", createRec.Code)
	}

	body := `{
		"statements": [
			{ "sql": "INSERT INTO users (id, name) VALUES (?, ?)", "params": [1, "Alice"] },
			{ "sql": "INSERT INTO users (id, name) VALUES (?, ?)", "params": [1, "Bob"] }
		]
	}`
	txReq := httptest.NewRequest(http.MethodPost, "/transaction", bytes.NewBufferString(body))
	txReq.Header.Set("Content-Type", "application/json")
	txRec := httptest.NewRecorder()
	srv.ServeHTTP(txRec, txReq)
	if txRec.Code != http.StatusInternalServerError {
		t.Fatalf("expected transaction failure, got %d", txRec.Code)
	}

	selectReq := httptest.NewRequest(http.MethodPost, "/query", bytes.NewBufferString(`{"sql":"SELECT COUNT(*) AS count FROM users"}`))
	selectReq.Header.Set("Content-Type", "application/json")
	selectRec := httptest.NewRecorder()
	srv.ServeHTTP(selectRec, selectReq)
	if selectRec.Code != http.StatusOK {
		t.Fatalf("select failed with status %d", selectRec.Code)
	}

	var payload QueryResponse
	if err := json.Unmarshal(selectRec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if got := payload.Rows[0][0]; got != float64(0) {
		t.Fatalf("expected rollback to leave 0 rows, got %v", got)
	}
}

func TestExportBackupAndMetrics(t *testing.T) {
	backupDir := t.TempDir()
	srv, cleanup := newTestServer(t, ServerConfig{BackupDir: backupDir})
	defer cleanup()

	createReq := httptest.NewRequest(http.MethodPost, "/query", bytes.NewBufferString(`{"sql":"CREATE TABLE users (id INTEGER PRIMARY KEY, name TEXT)"}`))
	createReq.Header.Set("Content-Type", "application/json")
	createRec := httptest.NewRecorder()
	srv.ServeHTTP(createRec, createReq)
	if createRec.Code != http.StatusOK {
		t.Fatalf("create table failed with status %d", createRec.Code)
	}

	exportReq := httptest.NewRequest(http.MethodGet, "/export", nil)
	exportRec := httptest.NewRecorder()
	srv.ServeHTTP(exportRec, exportReq)
	if exportRec.Code != http.StatusOK {
		t.Fatalf("export failed with status %d", exportRec.Code)
	}
	if exportRec.Body.Len() == 0 {
		t.Fatalf("expected export body")
	}

	backupReq := httptest.NewRequest(http.MethodPost, "/backup", nil)
	backupRec := httptest.NewRecorder()
	srv.ServeHTTP(backupRec, backupReq)
	if backupRec.Code != http.StatusOK {
		t.Fatalf("backup failed with status %d", backupRec.Code)
	}
	var backupPayload map[string]string
	if err := json.Unmarshal(backupRec.Body.Bytes(), &backupPayload); err != nil {
		t.Fatalf("failed to decode backup response: %v", err)
	}
	if _, err := os.Stat(backupPayload["path"]); err != nil {
		t.Fatalf("expected backup file to exist: %v", err)
	}

	metricsReq := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	metricsRec := httptest.NewRecorder()
	srv.ServeHTTP(metricsRec, metricsReq)
	if metricsRec.Code != http.StatusOK {
		t.Fatalf("metrics failed with status %d", metricsRec.Code)
	}
	var metrics map[string]any
	if err := json.Unmarshal(metricsRec.Body.Bytes(), &metrics); err != nil {
		t.Fatalf("failed to decode metrics response: %v", err)
	}
	if metrics["export_total"] != float64(1) {
		t.Fatalf("expected export_total 1, got %v", metrics["export_total"])
	}
	if metrics["backup_total"] != float64(1) {
		t.Fatalf("expected backup_total 1, got %v", metrics["backup_total"])
	}
}

func TestQueryRowLimit(t *testing.T) {
	srv, cleanup := newTestServer(t, ServerConfig{MaxRows: 2})
	defer cleanup()

	createReq := httptest.NewRequest(http.MethodPost, "/query", bytes.NewBufferString(`{"sql":"CREATE TABLE items (id INTEGER);"}`))
	createReq.Header.Set("Content-Type", "application/json")
	createRec := httptest.NewRecorder()
	srv.ServeHTTP(createRec, createReq)
	if createRec.Code != http.StatusOK {
		t.Fatalf("create table failed with status %d", createRec.Code)
	}

	insertReq := httptest.NewRequest(http.MethodPost, "/transaction", bytes.NewBufferString(`{"statements":[{"sql":"INSERT INTO items VALUES (1)"},{"sql":"INSERT INTO items VALUES (2)"},{"sql":"INSERT INTO items VALUES (3)"}]}`))
	insertReq.Header.Set("Content-Type", "application/json")
	insertRec := httptest.NewRecorder()
	srv.ServeHTTP(insertRec, insertReq)
	if insertRec.Code != http.StatusOK {
		t.Fatalf("insert failed with status %d", insertRec.Code)
	}

	queryReq := httptest.NewRequest(http.MethodPost, "/query", bytes.NewBufferString(`{"sql":"SELECT id FROM items ORDER BY id"}`))
	queryReq.Header.Set("Content-Type", "application/json")
	queryRec := httptest.NewRecorder()
	srv.ServeHTTP(queryRec, queryReq)
	if queryRec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("expected 422 for row limit, got %d: %s", queryRec.Code, queryRec.Body.String())
	}
}

func TestQueryStringTokenOnlyAllowedForWebSocket(t *testing.T) {
	srv, cleanup := newTestServer(t, ServerConfig{APIKey: "secret"})
	defer cleanup()

	req := httptest.NewRequest(http.MethodPost, "/query?token=secret", bytes.NewBufferString(`{"sql":"SELECT 1"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected query-string token to be rejected for HTTP API, got %d", rec.Code)
	}
}
