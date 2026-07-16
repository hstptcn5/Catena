package main

import (
	"database/sql"
	"fmt"
	"strings"
	"sync"

	_ "modernc.org/sqlite"
)

// DB represents a wrapper around sql.DB with concurrent read/write isolation
type DB struct {
	sqliteDB *sql.DB
	writeMu  sync.Mutex
	filePath string
	onWrite  func(tableName string)
}

// QueryResult represents the format of SELECT results
type QueryResult struct {
	Columns []string `json:"columns"`
	Rows    [][]any  `json:"rows"`
}

// ExecResult represents the format of INSERT/UPDATE/DELETE results
type ExecResult struct {
	LastInsertID int64 `json:"last_insert_id"`
	RowsAffected int64 `json:"rows_affected"`
}

// OpenDB initializes connection to the SQLite database and sets WAL mode
func OpenDB(path string, onWrite func(tableName string)) (*DB, error) {
	// Enable WAL and busy_timeout in connection string
	connStr := fmt.Sprintf("file:%s?_journal_mode=WAL&_busy_timeout=5000", path)
	db, err := sql.Open("sqlite", connStr)
	if err != nil {
		return nil, fmt.Errorf("failed to open sqlite database: %w", err)
	}

	// Set connection pool limits
	db.SetMaxOpenConns(100)
	db.SetMaxIdleConns(10)

	// Verify connection and explicitly set journal mode to WAL to be safe
	var journalMode string
	err = db.QueryRow("PRAGMA journal_mode=WAL;").Scan(&journalMode)
	if err != nil {
		db.Close()
		return nil, fmt.Errorf("failed to enable WAL mode: %w", err)
	}

	return &DB{
		sqliteDB: db,
		filePath: path,
		onWrite:  onWrite,
	}, nil
}

// Close closes the database connection pool
func (d *DB) Close() error {
	return d.sqliteDB.Close()
}

// Query performs concurrent read queries (SELECT)
func (d *DB) Query(sqlStr string, args ...any) (*QueryResult, error) {
	rows, err := d.sqliteDB.Query(sqlStr, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	cols, err := rows.Columns()
	if err != nil {
		return nil, err
	}

	resultRows := [][]any{}
	for rows.Next() {
		scanArgs := make([]any, len(cols))
		values := make([]any, len(cols))
		for i := range values {
			scanArgs[i] = &values[i]
		}

		if err := rows.Scan(scanArgs...); err != nil {
			return nil, err
		}

		rowVals := make([]any, len(cols))
		for i, val := range values {
			switch v := val.(type) {
			case []byte:
				// Convert raw bytes to string so JSON encoding produces text rather than base64
				rowVals[i] = string(v)
			default:
				rowVals[i] = v
			}
		}
		resultRows = append(resultRows, rowVals)
	}

	if err = rows.Err(); err != nil {
		return nil, err
	}

	return &QueryResult{
		Columns: cols,
		Rows:    resultRows,
	}, nil
}

// Exec performs a serialized write query (INSERT/UPDATE/DELETE/CREATE/etc)
func (d *DB) Exec(sqlStr string, args ...any) (*ExecResult, error) {
	d.writeMu.Lock()
	defer d.writeMu.Unlock()

	res, err := d.sqliteDB.Exec(sqlStr, args...)
	if err != nil {
		return nil, err
	}

	lastID, _ := res.LastInsertId()
	rowsAff, _ := res.RowsAffected()

	if d.onWrite != nil {
		table := parseAffectedTable(sqlStr)
		if table != "" {
			d.onWrite(table)
		}
	}

	return &ExecResult{
		LastInsertID: lastID,
		RowsAffected: rowsAff,
	}, nil
}

// IsReadQuery returns true if the SQL command is a read-only query
func IsReadQuery(sqlStr string) bool {
	s := strings.ToLower(strings.TrimSpace(sqlStr))
	// Commonly query statements
	return strings.HasPrefix(s, "select") || strings.HasPrefix(s, "pragma") || strings.HasPrefix(s, "explain")
}

// parseAffectedTable tries to extract table name from simple write operations
func parseAffectedTable(sqlStr string) string {
	s := strings.ToLower(strings.TrimSpace(sqlStr))

	// INSERT INTO <table> ...
	if strings.HasPrefix(s, "insert ") {
		parts := strings.Split(s, "into")
		if len(parts) > 1 {
			fields := strings.Fields(parts[1])
			if len(fields) > 0 {
				return cleanTableName(fields[0])
			}
		}
	}

	// UPDATE <table> ...
	if strings.HasPrefix(s, "update ") {
		fields := strings.Fields(s)
		if len(fields) > 1 {
			return cleanTableName(fields[1])
		}
	}

	// DELETE FROM <table> ...
	if strings.HasPrefix(s, "delete ") {
		parts := strings.Split(s, "from")
		if len(parts) > 1 {
			fields := strings.Fields(parts[1])
			if len(fields) > 0 {
				return cleanTableName(fields[0])
			}
		}
	}

	return ""
}

// cleanTableName removes common SQL delimiters like quotes, backticks or brackets
func cleanTableName(name string) string {
	name = strings.TrimSpace(name)
	name = strings.Trim(name, "`\"[]")
	return name
}
