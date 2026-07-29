package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

const pilotAPIKey = "pilot-dev-key"

func TestPilotJourneyEndToEnd(t *testing.T) {
	beforeGoroutines := runtime.NumGoroutine()
	tmp := t.TempDir()
	dbPath := filepath.Join(tmp, "pilot.db")
	backupDir := filepath.Join(tmp, "backups")

	db, err := OpenDB(dbPath, nil)
	if err != nil {
		t.Fatalf("OpenDB failed: %v", err)
	}
	if _, err := db.Exec(`CREATE TABLE inventory (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		sku TEXT NOT NULL UNIQUE,
		name TEXT NOT NULL,
		quantity INTEGER NOT NULL
	);`); err != nil {
		t.Fatalf("create schema failed: %v", err)
	}
	if _, err := db.Exec("INSERT INTO inventory (sku, name, quantity) VALUES (?, ?, ?)", "CAT-001", "Cable", 12); err != nil {
		t.Fatalf("seed failed: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close seed db failed: %v", err)
	}

	hub := NewHub()
	go hub.Run()
	catenaDB, err := OpenDB(dbPath, hub.Broadcast)
	if err != nil {
		t.Fatalf("OpenDB for server failed: %v", err)
	}
	server := NewServer(catenaDB, hub, ServerConfig{
		APIKey:       pilotAPIKey,
		BackupDir:    backupDir,
		MaxRows:      2,
		QueryTimeout: 5 * time.Second,
	})

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen failed: %v", err)
	}
	serverErr := make(chan error, 1)
	go func() {
		serverErr <- server.StartOnListener(listener)
	}()

	baseURL := "http://" + listener.Addr().String()
	waitForHealth(t, baseURL)

	t.Run("health", func(t *testing.T) {
		var payload map[string]string
		doJSON(t, http.MethodGet, baseURL+"/health", "", nil, &payload)
		if payload["status"] != "ok" || payload["version"] != appVersion {
			t.Fatalf("unexpected health payload: %+v", payload)
		}
	})

	t.Run("auth rejection", func(t *testing.T) {
		status, _ := doRaw(t, http.MethodPost, baseURL+"/query", "", []byte(`{"sql":"SELECT 1"}`))
		if status != http.StatusUnauthorized {
			t.Fatalf("expected missing credential 401, got %d", status)
		}
		status, _ = doRaw(t, http.MethodPost, baseURL+"/query", "wrong-key", []byte(`{"sql":"SELECT 1"}`))
		if status != http.StatusUnauthorized {
			t.Fatalf("expected invalid credential 401, got %d", status)
		}
	})

	var selectPayload QueryResponse
	doJSON(t, http.MethodPost, baseURL+"/query", pilotAPIKey, []byte(`{"sql":"SELECT sku, quantity FROM inventory WHERE sku = ?","params":["CAT-001"]}`), &selectPayload)
	if len(selectPayload.Rows) != 1 || selectPayload.Rows[0][0] != "CAT-001" {
		t.Fatalf("unexpected parameterized select payload: %+v", selectPayload)
	}

	wsURL := "ws://" + listener.Addr().String() + "/ws?token=" + pilotAPIKey
	ws, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("websocket dial failed: %v", err)
	}
	defer ws.Close()
	if err := ws.WriteJSON(WSMessage{Type: "subscribe", Table: "inventory"}); err != nil {
		t.Fatalf("websocket subscribe failed: %v", err)
	}
	var ack map[string]string
	if err := ws.ReadJSON(&ack); err != nil {
		t.Fatalf("websocket ack read failed: %v", err)
	}
	if ack["type"] != "subscribed" || ack["table"] != "inventory" {
		t.Fatalf("unexpected websocket ack: %+v", ack)
	}

	var writePayload QueryResponse
	doJSON(t, http.MethodPost, baseURL+"/query", pilotAPIKey, []byte(`{"sql":"INSERT INTO inventory (sku, name, quantity) VALUES (?, ?, ?)","params":["CAT-002","Adapter",5]}`), &writePayload)
	if writePayload.RowsAffected != 1 {
		t.Fatalf("expected one inserted row, got %+v", writePayload)
	}
	var event TableEvent
	if err := ws.ReadJSON(&event); err != nil {
		t.Fatalf("websocket event read failed: %v", err)
	}
	if event.Type != "update" || event.Table != "inventory" || event.Operation != "insert" || event.RowsAffected != 1 {
		t.Fatalf("unexpected websocket event: %+v", event)
	}

	var txPayload struct {
		Results []QueryResponse `json:"results"`
	}
	doJSON(t, http.MethodPost, baseURL+"/transaction", pilotAPIKey, []byte(`{
		"statements": [
			{ "sql": "INSERT INTO inventory (sku, name, quantity) VALUES (?, ?, ?)", "params": ["CAT-003", "Dock", 2] },
			{ "sql": "UPDATE inventory SET quantity = quantity + ? WHERE sku = ?", "params": [3, "CAT-001"] }
		]
	}`), &txPayload)
	if len(txPayload.Results) != 2 {
		t.Fatalf("expected two transaction results, got %+v", txPayload)
	}
	readUpdateEvent(t, ws)
	ws.Close()

	ws, _, err = websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("second websocket dial failed: %v", err)
	}
	defer ws.Close()
	if err := ws.WriteJSON(WSMessage{Type: "subscribe", Table: "inventory"}); err != nil {
		t.Fatalf("second websocket subscribe failed: %v", err)
	}
	ack = map[string]string{}
	if err := ws.ReadJSON(&ack); err != nil {
		t.Fatalf("second websocket ack read failed: %v", err)
	}
	if ack["type"] != "subscribed" || ack["table"] != "inventory" {
		t.Fatalf("unexpected second websocket ack: %+v", ack)
	}

	status, _ := doRaw(t, http.MethodPost, baseURL+"/transaction", pilotAPIKey, []byte(`{
		"statements": [
			{ "sql": "INSERT INTO inventory (sku, name, quantity) VALUES (?, ?, ?)", "params": ["CAT-004", "Stand", 4] },
			{ "sql": "INSERT INTO inventory (sku, name, quantity) VALUES (?, ?, ?)", "params": ["CAT-004", "Duplicate", 1] }
		]
	}`))
	if status != http.StatusInternalServerError {
		t.Fatalf("expected rollback transaction failure, got %d", status)
	}
	var rollbackCheck QueryResponse
	doJSON(t, http.MethodPost, baseURL+"/query", pilotAPIKey, []byte(`{"sql":"SELECT COUNT(*) FROM inventory WHERE sku = ?","params":["CAT-004"]}`), &rollbackCheck)
	if rollbackCheck.Rows[0][0] != float64(0) {
		t.Fatalf("rollback failed, row exists: %+v", rollbackCheck)
	}

	external, err := sql.Open("sqlite", "file:"+dbPath+"?_busy_timeout=5000")
	if err != nil {
		t.Fatalf("external open failed: %v", err)
	}
	if _, err := external.Exec("INSERT INTO inventory (sku, name, quantity) VALUES (?, ?, ?)", "EXT-001", "External write", 1); err != nil {
		t.Fatalf("external write failed: %v", err)
	}
	if err := external.Close(); err != nil {
		t.Fatalf("external close failed: %v", err)
	}
	if err := ws.SetReadDeadline(time.Now().Add(250 * time.Millisecond)); err != nil {
		t.Fatalf("set websocket deadline failed: %v", err)
	}
	if _, _, err := ws.ReadMessage(); err == nil {
		t.Fatalf("external SQLite write should not create a Catena websocket event")
	} else {
		var netErr net.Error
		if !errors.As(err, &netErr) || !netErr.Timeout() {
			t.Fatalf("expected websocket read timeout after external write, got %v", err)
		}
	}
	if err := ws.SetReadDeadline(time.Time{}); err != nil {
		t.Fatalf("clear websocket deadline failed: %v", err)
	}

	status, _ = doRaw(t, http.MethodPost, baseURL+"/query", pilotAPIKey, []byte(`{"sql":"SELECT * FROM inventory ORDER BY id"}`))
	if status != http.StatusUnprocessableEntity {
		t.Fatalf("expected row limit status %d, got %d", http.StatusUnprocessableEntity, status)
	}

	readOnlyDB, err := OpenDBReadOnly(dbPath)
	if err != nil {
		t.Fatalf("OpenDBReadOnly failed: %v", err)
	}
	if _, err := readOnlyDB.Query("SELECT sku FROM inventory ORDER BY sku LIMIT 1"); err != nil {
		t.Fatalf("read-only physical query failed: %v", err)
	}
	if _, err := readOnlyDB.Exec("INSERT INTO inventory (sku, name, quantity) VALUES (?, ?, ?)", "RO-001", "Blocked", 1); err == nil {
		t.Fatal("expected SQLite read-only connection to reject direct write")
	}
	readOnlyHub := NewHub()
	go readOnlyHub.Run()
	readOnlyServer := NewServer(readOnlyDB, readOnlyHub, ServerConfig{APIKey: pilotAPIKey, ReadOnly: true, MaxRows: 10})
	readOnlyHTTP := httptest.NewServer(readOnlyServer)
	readOnlyWriteStatus, _ := doRaw(t, http.MethodPost, readOnlyHTTP.URL+"/query", pilotAPIKey, []byte(`{"sql":"INSERT INTO inventory (sku, name, quantity) VALUES (?, ?, ?)","params":["RO-002","Blocked",1]}`))
	if readOnlyWriteStatus != http.StatusForbidden {
		t.Fatalf("expected read-only query write rejection, got %d", readOnlyWriteStatus)
	}
	readOnlyTxStatus, _ := doRaw(t, http.MethodPost, readOnlyHTTP.URL+"/transaction", pilotAPIKey, []byte(`{"statements":[{"sql":"INSERT INTO inventory (sku, name, quantity) VALUES (?, ?, ?)","params":["RO-003","Blocked",1]}]}`))
	if readOnlyTxStatus != http.StatusForbidden {
		t.Fatalf("expected read-only transaction rejection, got %d", readOnlyTxStatus)
	}
	var readOnlyQuery QueryResponse
	doJSON(t, http.MethodPost, readOnlyHTTP.URL+"/query", pilotAPIKey, []byte(`{"sql":"SELECT sku FROM inventory ORDER BY sku LIMIT 1"}`), &readOnlyQuery)
	if len(readOnlyQuery.Rows) != 1 {
		t.Fatalf("expected read-only query result, got %+v", readOnlyQuery)
	}
	readOnlyHTTP.Close()
	readOnlyHub.Stop()
	if err := readOnlyDB.Close(); err != nil {
		t.Fatalf("readonly db close failed: %v", err)
	}

	exportStatus, exportBody := doRaw(t, http.MethodGet, baseURL+"/export", pilotAPIKey, nil)
	if exportStatus != http.StatusOK || len(exportBody) == 0 {
		t.Fatalf("export failed: status=%d len=%d", exportStatus, len(exportBody))
	}
	exportPath := filepath.Join(tmp, "export.db")
	if err := os.WriteFile(exportPath, exportBody, 0644); err != nil {
		t.Fatalf("write export failed: %v", err)
	}
	assertSQLiteCount(t, exportPath, "inventory", 4)

	var backupPayload map[string]string
	doJSON(t, http.MethodPost, baseURL+"/backup", pilotAPIKey, nil, &backupPayload)
	if backupPayload["path"] == "" {
		t.Fatalf("missing backup path: %+v", backupPayload)
	}
	assertSQLiteCount(t, backupPayload["path"], "inventory", 4)

	ws.Close()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		t.Fatalf("shutdown failed: %v", err)
	}
	if err := <-serverErr; err != nil {
		t.Fatalf("server returned error: %v", err)
	}
	if err := catenaDB.Close(); err != nil {
		t.Fatalf("server db close failed: %v", err)
	}

	waitForNoLargeGoroutineGrowth(t, beforeGoroutines)
}

func waitForHealth(t *testing.T, baseURL string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := http.Get(baseURL + "/health")
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return
			}
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("server did not become healthy")
}

func doJSON(t *testing.T, method, url, apiKey string, body []byte, target any) {
	t.Helper()
	status, responseBody := doRaw(t, method, url, apiKey, body)
	if status < 200 || status >= 300 {
		t.Fatalf("%s %s failed with status %d: %s", method, url, status, string(responseBody))
	}
	if err := json.Unmarshal(responseBody, target); err != nil {
		t.Fatalf("decode response failed: %v: %s", err, string(responseBody))
	}
}

func doRaw(t *testing.T, method, url, apiKey string, body []byte) (int, []byte) {
	t.Helper()
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequest(method, url, reader)
	if err != nil {
		t.Fatalf("request creation failed: %v", err)
	}
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s failed: %v", method, url, err)
	}
	defer resp.Body.Close()
	responseBody, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read response failed: %v", err)
	}
	return resp.StatusCode, responseBody
}

func readUpdateEvent(t *testing.T, ws *websocket.Conn) {
	t.Helper()
	var event TableEvent
	if err := ws.ReadJSON(&event); err != nil {
		t.Fatalf("websocket event read failed: %v", err)
	}
	if event.Type != "update" {
		t.Fatalf("unexpected websocket event: %+v", event)
	}
}

func assertSQLiteCount(t *testing.T, dbPath, table string, expected int) {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+dbPath+"?_busy_timeout=5000")
	if err != nil {
		t.Fatalf("open sqlite database failed: %v", err)
	}
	defer db.Close()
	var count int
	if err := db.QueryRow(fmt.Sprintf("SELECT COUNT(*) FROM %s", table)).Scan(&count); err != nil {
		t.Fatalf("count query failed: %v", err)
	}
	if count != expected {
		t.Fatalf("expected %d rows in %s, got %d", expected, dbPath, count)
	}
}

func waitForNoLargeGoroutineGrowth(t *testing.T, before int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		after := runtime.NumGoroutine()
		if after <= before+4 {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("possible goroutine leak: before=%d after=%d", before, runtime.NumGoroutine())
}
