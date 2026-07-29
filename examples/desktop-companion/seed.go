package main

import (
	"database/sql"
	"fmt"
	"os"

	_ "modernc.org/sqlite"
)

func main() {
	dbPath := "desktop-companion.db"
	if len(os.Args) > 1 {
		dbPath = os.Args[1]
	}

	db, err := sql.Open("sqlite", "file:"+dbPath+"?_journal_mode=WAL&_busy_timeout=5000")
	if err != nil {
		panic(err)
	}
	defer db.Close()

	statements := []string{
		`DROP TABLE IF EXISTS inventory;`,
		`CREATE TABLE inventory (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			sku TEXT NOT NULL UNIQUE,
			name TEXT NOT NULL,
			quantity INTEGER NOT NULL,
			updated_at TEXT DEFAULT CURRENT_TIMESTAMP
		);`,
		`INSERT INTO inventory (sku, name, quantity) VALUES
			('CAT-001', 'USB-C Cable', 12),
			('CAT-002', 'Dock Adapter', 4),
			('CAT-003', 'Spare Keyboard', 7);`,
	}

	for _, stmt := range statements {
		if _, err := db.Exec(stmt); err != nil {
			panic(err)
		}
	}

	fmt.Printf("Seeded %s\n", dbPath)
}
