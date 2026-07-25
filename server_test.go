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
