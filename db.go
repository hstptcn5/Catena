package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode"

	_ "modernc.org/sqlite"
)

// DB represents a wrapper around sql.DB with concurrent read/write isolation
type DB struct {
	sqliteDB *sql.DB
	writeMu  sync.Mutex
	filePath string
	onWrite  func(WriteEvent)
}

// WriteEvent describes a successful mutating SQL statement.
type WriteEvent struct {
	Table        string
	Operation    string
	RowsAffected int64
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

// ExecStatement is one statement in a batch transaction.
type ExecStatement struct {
	SQL    string
	Params []any
}

// DBInfo summarizes a SQLite database for CLI inspection.
type DBInfo struct {
	Path        string   `json:"path"`
	SizeBytes   int64    `json:"size_bytes"`
	JournalMode string   `json:"journal_mode"`
	UserVersion int64    `json:"user_version"`
	TableCount  int      `json:"table_count"`
	Tables      []string `json:"tables"`
}

// OpenDB initializes a writable connection to the SQLite database and enables WAL mode.
func OpenDB(path string, onWrite func(WriteEvent)) (*DB, error) {
	return openDB(path, onWrite, false)
}

// OpenDBReadOnly opens an existing SQLite database using SQLite's read-only mode.
// This is the enforcement boundary for --readonly; SQL classification is defense in depth.
func OpenDBReadOnly(path string) (*DB, error) {
	return openDB(path, nil, true)
}

func openDB(path string, onWrite func(WriteEvent), readOnly bool) (*DB, error) {
	connStr := fmt.Sprintf("file:%s?_journal_mode=WAL&_busy_timeout=5000", path)
	if readOnly {
		connStr = fmt.Sprintf("file:%s?mode=ro&_busy_timeout=5000", path)
	}
	db, err := sql.Open("sqlite", connStr)
	if err != nil {
		return nil, fmt.Errorf("failed to open sqlite database: %w", err)
	}

	// Set connection pool limits
	db.SetMaxOpenConns(100)
	db.SetMaxIdleConns(10)

	// Verify the connection. A read-only connection must not execute mutating PRAGMAs.
	if readOnly {
		if err := db.Ping(); err != nil {
			db.Close()
			return nil, fmt.Errorf("failed to open sqlite database read-only: %w", err)
		}
	} else {
		var journalMode string
		if err := db.QueryRow("PRAGMA journal_mode=WAL;").Scan(&journalMode); err != nil {
			db.Close()
			return nil, fmt.Errorf("failed to enable WAL mode: %w", err)
		}
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

// Inspect returns basic metadata about the database file and schema.
func (d *DB) Inspect(ctx context.Context) (*DBInfo, error) {
	info := &DBInfo{Path: d.filePath}
	if stat, err := os.Stat(d.filePath); err == nil {
		info.SizeBytes = stat.Size()
	}

	if err := d.sqliteDB.QueryRowContext(ctx, "PRAGMA journal_mode;").Scan(&info.JournalMode); err != nil {
		return nil, err
	}
	if err := d.sqliteDB.QueryRowContext(ctx, "PRAGMA user_version;").Scan(&info.UserVersion); err != nil {
		return nil, err
	}

	rows, err := d.sqliteDB.QueryContext(ctx, "SELECT name FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%' ORDER BY name;")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	for rows.Next() {
		var table string
		if err := rows.Scan(&table); err != nil {
			return nil, err
		}
		info.Tables = append(info.Tables, table)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	info.TableCount = len(info.Tables)
	return info, nil
}

// Export copies a checkpointed SQLite database file to the writer.
func (d *DB) Export(ctx context.Context, w io.Writer) error {
	d.writeMu.Lock()
	defer d.writeMu.Unlock()

	if _, err := d.sqliteDB.ExecContext(ctx, "PRAGMA wal_checkpoint(TRUNCATE);"); err != nil {
		return err
	}

	file, err := os.Open(d.filePath)
	if err != nil {
		return err
	}
	defer file.Close()

	_, err = io.Copy(w, file)
	return err
}

// Backup writes a checkpointed copy of the SQLite database into targetDir.
func (d *DB) Backup(ctx context.Context, targetDir string) (string, error) {
	if targetDir == "" {
		targetDir = "."
	}
	if err := os.MkdirAll(targetDir, 0755); err != nil {
		return "", err
	}

	base := strings.TrimSuffix(filepath.Base(d.filePath), filepath.Ext(d.filePath))
	if base == "" || base == "." {
		base = "catena"
	}
	target := filepath.Join(targetDir, fmt.Sprintf("%s-%s.db", base, time.Now().UTC().Format("20060102T150405Z")))

	file, err := os.Create(target)
	if err != nil {
		return "", err
	}
	defer file.Close()

	if err := d.Export(ctx, file); err != nil {
		os.Remove(target)
		return "", err
	}
	return target, nil
}

// Query performs concurrent read queries (SELECT)
func (d *DB) Query(sqlStr string, args ...any) (*QueryResult, error) {
	return d.QueryContext(context.Background(), sqlStr, args...)
}

// QueryContext performs concurrent read queries (SELECT).
func (d *DB) QueryContext(ctx context.Context, sqlStr string, args ...any) (*QueryResult, error) {
	return d.QueryContextLimit(ctx, sqlStr, 0, args...)
}

// QueryContextLimit performs a read query and aborts if it exceeds maxRows.
// A non-positive maxRows disables the limit for trusted internal callers.
func (d *DB) QueryContextLimit(ctx context.Context, sqlStr string, maxRows int, args ...any) (*QueryResult, error) {
	rows, err := d.sqliteDB.QueryContext(ctx, sqlStr, args...)
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
		if maxRows > 0 && len(resultRows) >= maxRows {
			return nil, ErrRowLimitExceeded
		}
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
	return d.ExecContext(context.Background(), sqlStr, args...)
}

// ExecContext performs a serialized write query (INSERT/UPDATE/DELETE/CREATE/etc).
func (d *DB) ExecContext(ctx context.Context, sqlStr string, args ...any) (*ExecResult, error) {
	d.writeMu.Lock()
	defer d.writeMu.Unlock()

	res, err := d.sqliteDB.ExecContext(ctx, sqlStr, args...)
	if err != nil {
		return nil, err
	}

	lastID, _ := res.LastInsertId()
	rowsAff, _ := res.RowsAffected()

	if d.onWrite != nil {
		table := parseAffectedTable(sqlStr)
		if table != "" {
			d.onWrite(WriteEvent{
				Table:        table,
				Operation:    parseOperation(sqlStr),
				RowsAffected: rowsAff,
			})
		}
	}

	return &ExecResult{
		LastInsertID: lastID,
		RowsAffected: rowsAff,
	}, nil
}

// ExecBatchContext runs multiple write statements in a single transaction.
func (d *DB) ExecBatchContext(ctx context.Context, statements []ExecStatement) ([]ExecResult, error) {
	d.writeMu.Lock()
	defer d.writeMu.Unlock()

	tx, err := d.sqliteDB.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}

	results := make([]ExecResult, 0, len(statements))
	events := make([]WriteEvent, 0, len(statements))
	for _, stmt := range statements {
		res, err := tx.ExecContext(ctx, stmt.SQL, stmt.Params...)
		if err != nil {
			tx.Rollback()
			return nil, err
		}
		lastID, _ := res.LastInsertId()
		rowsAff, _ := res.RowsAffected()
		results = append(results, ExecResult{LastInsertID: lastID, RowsAffected: rowsAff})
		if table := parseAffectedTable(stmt.SQL); table != "" {
			events = append(events, WriteEvent{
				Table:        table,
				Operation:    parseOperation(stmt.SQL),
				RowsAffected: rowsAff,
			})
		}
	}

	if err := tx.Commit(); err != nil {
		return nil, err
	}
	if d.onWrite != nil {
		for _, event := range events {
			d.onWrite(event)
		}
	}
	return results, nil
}

var (
	ErrEmptySQL       = errors.New("SQL statement is required")
	ErrRowLimitExceeded = errors.New("query result exceeds the configured row limit")
	ErrMultiStatement = errors.New("multiple SQL statements are disabled")
)

// SQLKind describes how Catena should execute a SQL statement.
type SQLKind string

const (
	SQLRead  SQLKind = "read"
	SQLWrite SQLKind = "write"
)

// ClassifySQL validates and classifies a single SQL statement.
func ClassifySQL(sqlStr string) (SQLKind, error) {
	trimmed := stripLeadingComments(strings.TrimSpace(sqlStr))
	if trimmed == "" {
		return "", ErrEmptySQL
	}
	if hasMultipleStatements(trimmed) {
		return "", ErrMultiStatement
	}
	if IsReadQuery(trimmed) {
		return SQLRead, nil
	}
	return SQLWrite, nil
}

// IsReadQuery returns true if the SQL command is a read-only query
func IsReadQuery(sqlStr string) bool {
	s := strings.ToLower(stripLeadingComments(strings.TrimSpace(sqlStr)))
	// Commonly query statements
	if strings.HasPrefix(s, "select") || strings.HasPrefix(s, "explain") {
		return true
	}
	if strings.HasPrefix(s, "pragma") {
		return isReadOnlyPragma(s)
	}
	return false
}

// parseAffectedTable tries to extract table name from simple write operations
func parseAffectedTable(sqlStr string) string {
	s := strings.ToLower(stripLeadingComments(strings.TrimSpace(sqlStr)))

	// INSERT INTO <table> ...
	if strings.HasPrefix(s, "insert ") || strings.HasPrefix(s, "replace ") {
		fields := strings.Fields(s)
		for i, field := range fields {
			if field == "into" && i+1 < len(fields) {
				return cleanTableName(fields[i+1])
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

func parseOperation(sqlStr string) string {
	s := strings.ToLower(stripLeadingComments(strings.TrimSpace(sqlStr)))
	fields := strings.Fields(s)
	if len(fields) == 0 {
		return "write"
	}
	operation := fields[0]
	if operation == "insert" && len(fields) > 1 && fields[1] == "or" {
		return "insert"
	}
	switch operation {
	case "insert", "update", "delete", "create", "drop", "alter", "replace":
		return operation
	default:
		return "write"
	}
}

func stripLeadingComments(s string) string {
	for {
		s = strings.TrimSpace(s)
		if strings.HasPrefix(s, "--") {
			if idx := strings.IndexByte(s, '\n'); idx >= 0 {
				s = s[idx+1:]
				continue
			}
			return ""
		}
		if strings.HasPrefix(s, "/*") {
			if idx := strings.Index(s, "*/"); idx >= 0 {
				s = s[idx+2:]
				continue
			}
			return ""
		}
		return s
	}
}

func hasMultipleStatements(s string) bool {
	inSingle := false
	inDouble := false
	inLineComment := false
	inBlockComment := false
	seenTerminator := false

	for i, r := range s {
		var next rune
		if i+1 < len(s) {
			next = rune(s[i+1])
		}

		if inLineComment {
			if r == '\n' {
				inLineComment = false
			}
			continue
		}
		if inBlockComment {
			if r == '*' && next == '/' {
				inBlockComment = false
			}
			continue
		}
		if inSingle {
			if r == '\'' {
				if next == '\'' {
					continue
				}
				inSingle = false
			}
			continue
		}
		if inDouble {
			if r == '"' {
				inDouble = false
			}
			continue
		}

		if r == '-' && next == '-' {
			inLineComment = true
			continue
		}
		if r == '/' && next == '*' {
			inBlockComment = true
			continue
		}
		if r == '\'' {
			inSingle = true
			continue
		}
		if r == '"' {
			inDouble = true
			continue
		}
		if r == ';' {
			seenTerminator = true
			continue
		}
		if seenTerminator && !unicode.IsSpace(r) {
			return true
		}
	}
	return false
}

func isReadOnlyPragma(s string) bool {
	if strings.Contains(s, "=") {
		return false
	}
	fields := strings.Fields(strings.TrimSuffix(s, ";"))
	if len(fields) < 2 {
		return false
	}
	name := strings.Trim(fields[1], "`\"[]")
	name = strings.Split(name, "(")[0]
	switch name {
	case "analysis_limit", "application_id", "auto_vacuum", "busy_timeout", "cache_size",
		"cache_spill", "cell_size_check", "checkpoint_fullfsync", "foreign_keys",
		"journal_mode", "locking_mode", "mmap_size", "optimize", "page_size",
		"recursive_triggers", "secure_delete", "synchronous", "temp_store",
		"user_version", "wal_autocheckpoint", "wal_checkpoint":
		if len(fields) == 2 && !strings.Contains(fields[1], "(") {
			return true
		}
		return false
	}
	return len(fields) >= 2
}

// cleanTableName removes common SQL delimiters like quotes, backticks or brackets
func cleanTableName(name string) string {
	name = strings.TrimSpace(name)
	name = strings.Trim(name, "`\"[]")
	return name
}
