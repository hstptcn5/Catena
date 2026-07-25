package main

import (
	"fmt"
	"math/rand"
	"os"
	"sync"
	"testing"
	"time"
)

func TestDBConcurrency(t *testing.T) {
	// Use a temporary database file for the test
	dbFile := "test_concurrency.db"
	defer os.Remove(dbFile)

	// Keep track of write notifications
	var notifyCount int
	var notifyMu sync.Mutex
	onWrite := func(event WriteEvent) {
		notifyMu.Lock()
		defer notifyMu.Unlock()
		if event.Table == "users" {
			notifyCount++
		}
	}

	// Open DB
	db, err := OpenDB(dbFile, onWrite)
	if err != nil {
		t.Fatalf("Failed to open DB: %v", err)
	}
	defer db.Close()

	// Create test table
	_, err = db.Exec("CREATE TABLE IF NOT EXISTS users (id INTEGER PRIMARY KEY AUTOINCREMENT, name TEXT, age INTEGER);")
	if err != nil {
		t.Fatalf("Failed to create table: %v", err)
	}

	var wg sync.WaitGroup
	readers := 10
	writers := 3
	opsPerGoroutine := 50

	// Start reader goroutines
	for i := 0; i < readers; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for j := 0; j < opsPerGoroutine; j++ {
				// Execute select query
				res, err := db.Query("SELECT * FROM users LIMIT 5;")
				if err != nil {
					t.Errorf("Reader %d failed: %v", id, err)
					return
				}
				_ = res
				time.Sleep(time.Duration(rand.Intn(5)) * time.Millisecond)
			}
		}(i)
	}

	// Start writer goroutines
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for j := 0; j < opsPerGoroutine; j++ {
				// Execute insert/update query
				name := fmt.Sprintf("User_%d_%d", id, j)
				age := 20 + rand.Intn(30)
				res, err := db.Exec("INSERT INTO users (name, age) VALUES (?, ?);", name, age)
				if err != nil {
					t.Errorf("Writer %d failed: %v", id, err)
					return
				}
				if res.RowsAffected != 1 {
					t.Errorf("Writer %d: expected 1 row affected, got %d", id, res.RowsAffected)
				}
				time.Sleep(time.Duration(rand.Intn(5)) * time.Millisecond)
			}
		}(i)
	}

	// Wait for all goroutines to complete
	wg.Wait()

	// Ensure some updates were registered and notifications were triggered
	notifyMu.Lock()
	defer notifyMu.Unlock()
	t.Logf("Total successful writes: %d, notified: %d", writers*opsPerGoroutine, notifyCount)
	if notifyCount == 0 {
		t.Errorf("Expected write notifications to be triggered, but got 0")
	}
}

func TestTableParsing(t *testing.T) {
	tests := []struct {
		sql      string
		expected string
	}{
		{"INSERT INTO users (name) VALUES ('test')", "users"},
		{"insert into   `orders` (id) values (1)", "orders"},
		{"UPDATE users SET name = 'test' WHERE id = 1", "users"},
		{"update [products] set price = 10", "products"},
		{"DELETE FROM users WHERE id = 1", "users"},
		{"delete   from \"transactions\" where status = 0", "transactions"},
		{"SELECT * FROM users", ""},
	}

	for _, tc := range tests {
		actual := parseAffectedTable(tc.sql)
		if actual != tc.expected {
			t.Errorf("parseAffectedTable(%q) = %q; expected %q", tc.sql, actual, tc.expected)
		}
	}
}

func TestSQLClassification(t *testing.T) {
	tests := []struct {
		sql     string
		kind    SQLKind
		wantErr bool
	}{
		{"SELECT * FROM users", SQLRead, false},
		{"-- comment\nSELECT 1", SQLRead, false},
		{"PRAGMA table_info(users)", SQLRead, false},
		{"PRAGMA user_version", SQLRead, false},
		{"INSERT INTO users (name) VALUES (?)", SQLWrite, false},
		{"SELECT 1; SELECT 2", "", true},
		{"", "", true},
	}

	for _, tc := range tests {
		kind, err := ClassifySQL(tc.sql)
		if tc.wantErr {
			if err == nil {
				t.Fatalf("ClassifySQL(%q) expected error", tc.sql)
			}
			continue
		}
		if err != nil {
			t.Fatalf("ClassifySQL(%q) unexpected error: %v", tc.sql, err)
		}
		if kind != tc.kind {
			t.Fatalf("ClassifySQL(%q) = %q, expected %q", tc.sql, kind, tc.kind)
		}
	}
}
