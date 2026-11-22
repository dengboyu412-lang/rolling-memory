// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

// Package db implements SQLite connection, schema migration and small DB
// helpers.
package db

import (
	"context"
	"database/sql"
	_ "embed"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/rocareer/rolling-memory/internal/ids"
	"github.com/rocareer/rolling-memory/internal/version"
	"modernc.org/sqlite"
)

//go:embed schema_vault.sql
var schemaThinSQL string

// StorageProfile is a set of connection-level PRAGMAs.
type StorageProfile struct {
	Name              string
	Synchronous       string
	BusyTimeoutMS     int
	WALAutocheckpoint int
	// JournalSizeLimit bounds the on-disk WAL file (bytes): wal_autocheckpoint only
	// fires after a write commit and never truncates the file, so a long-lived
	// sibling reader (daemon / console) can pin the WAL into unbounded growth that
	// survives the reader. The limit makes every successful checkpoint truncate the
	// WAL back under the cap.
	JournalSizeLimit int64
	MmapSize         int64
	CacheSize        int
	TempStore        string
	ForeignKeys      bool
}

// LocalProfile is the v0.7.0 profile.
func LocalProfile() StorageProfile {
	return StorageProfile{
		Name:        "local",
		Synchronous: "NORMAL",
		// Multi-process WAL vaults: sibling writers (daemon, MCP, console, CLI) must absorb
		// multi-second FTS/checkpoint stalls without failing the caller. Matches USBProfile.
		BusyTimeoutMS:     15000,
		WALAutocheckpoint: 1000,
		// 64 MiB: the same resource-hygiene ceiling the live-machine verifier enforces
		// (WAL <= max(64MiB, 4x db)); checkpoints truncate the WAL file back under it.
		JournalSizeLimit: 64 << 20,
		TempStore:        "MEMORY",
		ForeignKeys:      true,
	}
}

// USBProfile is the design-document removable-media profile.
func USBProfile() StorageProfile {
	return StorageProfile{
		Name:              "usb",
		Synchronous:       "FULL",
		BusyTimeoutMS:     15000,
		WALAutocheckpoint: 256,
		JournalSizeLimit:  16 << 20,
		MmapSize:          0,
		CacheSize:         -8192,
		TempStore:         "MEMORY",
		ForeignKeys:       true,
	}
}

func (p StorageProfile) normalized() StorageProfile {
	if p.Name == "" && p.Synchronous == "" && p.BusyTimeoutMS == 0 &&
		p.WALAutocheckpoint == 0 && p.JournalSizeLimit == 0 && p.MmapSize == 0 && p.CacheSize == 0 &&
		p.TempStore == "" && !p.ForeignKeys {
		return LocalProfile()
	}
	if p.Synchronous == "" {
		p.Synchronous = "NORMAL"
	}
	if p.BusyTimeoutMS <= 0 {
		p.BusyTimeoutMS = 15000
	}
	if p.TempStore == "" {
		p.TempStore = "MEMORY"
	}
	return p
}

// Connect opens path with one connection, immediate transactions and the
// profile PRAGMAs. The profile rides in the DSN, so every physical connection
// the pool ever creates keeps it, not just the first one. A failed WAL switch
// degrades to the rollback journal.
func Connect(path string, profile StorageProfile) (*sql.DB, error) {
	return openSQLite(path, profile, nil)
}

// OpenCounting opens the same vault pool as Connect with a driver wrapper that counts the
// statements sent to SQLite. Measurement paths use it; the product opens through Connect so
// the counting wrapper never rides a user session.
func OpenCounting(path string, profile StorageProfile) (*sql.DB, *SQLStats, error) {
	stats := &SQLStats{}
	connection, err := openSQLite(path, profile, stats)
	if err != nil {
		return nil, nil, err
	}
	return connection, stats, nil
}

func openSQLite(path string, profile StorageProfile, stats *SQLStats) (*sql.DB, error) {
	profile = profile.normalized()
	dbPath := expandHome(path)
	memory := dbPath == ":memory:"
	if !memory {
		directory := filepath.Dir(dbPath)
		if err := os.MkdirAll(directory, 0o700); err != nil {
			return nil, err
		}
		file, err := os.OpenFile(dbPath, os.O_RDWR|os.O_CREATE, 0o600)
		if err != nil {
			return nil, err
		}
		if err := file.Chmod(0o600); err != nil {
			file.Close()
			return nil, err
		}
		if err := file.Close(); err != nil {
			return nil, err
		}
	}

	dsnPath := dbPath
	if strings.ContainsAny(dsnPath, "?#") {
		dsnPath = ids.SQLiteFileURI(dsnPath)
	}
	// The whole storage profile rides in the DSN, not in one-shot Execs on the first
	// connection: database/sql may replace a pooled connection at any time, and a fresh
	// connection with a zero busy timeout turns ordinary lock contention into instant
	// SQLITE_BUSY while a missing foreign_keys silently drops constraint enforcement.
	// recursive_triggers stays off so INSERT OR REPLACE keeps its implicit delete
	// silent: the canonical writer compensates docs_cnt for the rewrite, and enabling
	// recursion would fire the delete-side index, lineage and counter triggers as well.
	dsn := dsnPath + "?_txlock=immediate&_pragma=busy_timeout(" + strconv.Itoa(profile.BusyTimeoutMS) + ")" +
		"&_pragma=foreign_keys(1)" +
		"&_pragma=recursive_triggers(0)" +
		"&_pragma=synchronous(" + profile.Synchronous + ")" +
		"&_pragma=temp_store(" + profile.TempStore + ")"
	if profile.WALAutocheckpoint > 0 {
		dsn += "&_pragma=wal_autocheckpoint(" + strconv.Itoa(profile.WALAutocheckpoint) + ")"
	}
	if profile.JournalSizeLimit > 0 {
		dsn += "&_pragma=journal_size_limit(" + strconv.FormatInt(profile.JournalSizeLimit, 10) + ")"
	}
	if profile.Name == "usb" || profile.MmapSize != 0 {
		dsn += "&_pragma=mmap_size(" + strconv.FormatInt(profile.MmapSize, 10) + ")"
	}
	if profile.CacheSize != 0 {
		dsn += "&_pragma=cache_size(" + strconv.Itoa(profile.CacheSize) + ")"
	}
	var connection *sql.DB
	if stats != nil {
		connector, connectorErr := sqlite.NewConnector(dsn)
		if connectorErr != nil {
			return nil, connectorErr
		}
		connection = sql.OpenDB(&countingConnector{inner: connector, stats: stats})
	} else {
		var err error
		connection, err = sql.Open("sqlite", dsn)
		if err != nil {
			return nil, err
		}
	}
	connection.SetMaxOpenConns(1)
	connection.SetMaxIdleConns(1)
	connection.SetConnMaxLifetime(0)
	if err := connection.Ping(); err != nil {
		connection.Close()
		return nil, err
	}

	currentJournalMode, err := pragmaString(connection, "PRAGMA journal_mode")
	if err != nil {
		connection.Close()
		return nil, err
	}
	if !strings.EqualFold(currentJournalMode, "wal") {
		journalMode, err := pragmaString(connection, "PRAGMA journal_mode = WAL")
		if err != nil {
			connection.Close()
			return nil, err
		}
		if !strings.EqualFold(journalMode, "wal") {
			if _, err := pragmaString(connection, "PRAGMA journal_mode = DELETE"); err != nil {
				connection.Close()
				return nil, err
			}
		}
	}

	return connection, nil
}

// Migrate creates the bootstrap schema for a new database and writes db_meta rows.
func Migrate(connection *sql.DB) (int, error) {
	current, err := userVersion(connection)
	if err != nil {
		return 0, err
	}
	if current > version.SchemaVersion {
		return 0, fmt.Errorf(
			"database schema v%d is newer than this build (v%d); upgrade Rolling-Memory",
			current, version.SchemaVersion,
		)
	}
	if current == 0 {
		for _, statement := range splitSchemaStatements(schemaThinSQL) {
			if _, err := connection.Exec(statement); err != nil {
				return 0, err
			}
		}
		if _, err := connection.Exec(fmt.Sprintf("PRAGMA user_version = %d", version.SchemaVersion)); err != nil {
			return 0, err
		}
		meta := []struct{ key, value string }{
			{"schema_version", strconv.Itoa(version.SchemaVersion)},
			{"created_at", strconv.FormatInt(ids.UTCNow(), 10)},
			{"engine", "rolling-memory"},
		}
		for _, row := range meta {
			if _, err := connection.Exec(
				"INSERT OR REPLACE INTO db_meta(key, value) VALUES (?, ?)",
				row.key, row.value,
			); err != nil {
				return 0, err
			}
		}
	}
	return version.SchemaVersion, nil
}

// WithImmediateTx runs fn inside BEGIN IMMEDIATE. The connection DSN uses
// modernc's _txlock=immediate, so BeginTx issues the same statement standard
// uses; database/sql's default DEFERRED mode is never selected.
func WithImmediateTx(connection *sql.DB, fn func(*sql.Tx) error) error {
	tx, err := connection.BeginTx(context.Background(), nil)
	if err != nil {
		return err
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()
	if err := fn(tx); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	committed = true
	return nil
}

// GetMeta returns db_meta[key] or defaultValue.
func GetMeta(connection *sql.DB, key, defaultValue string) (string, error) {
	var value string
	err := connection.QueryRow("SELECT value FROM db_meta WHERE key = ?", key).Scan(&value)
	if err == sql.ErrNoRows {
		return defaultValue, nil
	}
	if err != nil {
		return "", err
	}
	return value, nil
}

// DBSize sums the database, -wal and -shm files.
func DBSize(path string) int64 {
	base := expandHome(path)
	total := int64(0)
	for _, suffix := range []string{"", "-wal", "-shm"} {
		if info, err := os.Stat(base + suffix); err == nil {
			total += info.Size()
		}
	}
	return total
}

// IntegrityCheck runs integrity_check and foreign_key_check.
func IntegrityCheck(connection *sql.DB) ([]string, error) {
	issues := []string{}
	var pageCount int64
	_ = connection.QueryRow("PRAGMA page_count").Scan(&pageCount)
	if pageCount > 8192 {
		// On multi-hundred-MB production vaults (>32MB), full-database PRAGMA integrity_check
		// scans every FTS5 trigram posting block and 200MB+ of packed binary slot blobs (~4s).
		// Check each core relational metadata/document table's B-tree and indexes directly.
		for _, tbl := range []string{"db_meta", "rm_meta", "rm_docs", "rm_noetic"} {
			var exists int
			if connection.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?", tbl).Scan(&exists) != nil || exists == 0 {
				continue
			}
			var res string
			if err := connection.QueryRow("PRAGMA integrity_check(" + tbl + ")").Scan(&res); err != nil {
				return nil, err
			}
			if strings.ToLower(strings.TrimSpace(res)) != "ok" {
				issues = append(issues, "integrity_check("+tbl+"): "+res)
			}
		}
	} else {
		rows, err := connection.Query("PRAGMA integrity_check")
		if err != nil {
			return nil, err
		}
		if rows.Next() {
			var value any
			if err := rows.Scan(&value); err != nil {
				rows.Close()
				return nil, err
			}
			text := pyValue(value)
			if strings.ToLower(text) != "ok" {
				issues = append(issues, "integrity_check: "+text)
			}
		}
		if err := rows.Close(); err != nil {
			return nil, err
		}
	}

	foreignRows, err := connection.Query("PRAGMA foreign_key_check")
	if err != nil {
		return nil, err
	}
	defer foreignRows.Close()
	for foreignRows.Next() {
		var table, rowid, parent, fkid any
		if err := foreignRows.Scan(&table, &rowid, &parent, &fkid); err != nil {
			return nil, err
		}
		issues = append(issues, fmt.Sprintf(
			"foreign_key_check: table=%s rowid=%s parent=%s fkid=%s",
			pyValue(table), pyValue(rowid), pyValue(parent), pyValue(fkid),
		))
	}
	if err := foreignRows.Err(); err != nil {
		return nil, err
	}
	return issues, nil
}

// SQLiteVersion returns the runtime SQLite library version.
func SQLiteVersion(connection *sql.DB) (string, error) {
	var value string
	err := connection.QueryRow("SELECT sqlite_version()").Scan(&value)
	return value, err
}

// JSONExtractAvailable probes the JSON1 extension.
func JSONExtractAvailable() bool {
	connection, err := sql.Open("sqlite", ":memory:?_txlock=immediate")
	if err != nil {
		return false
	}
	defer connection.Close()
	var value int
	err = connection.QueryRow(`SELECT json_extract('{"a":1}', '$.a')`).Scan(&value)
	return err == nil && value == 1
}

func userVersion(connection *sql.DB) (int, error) {
	var value int
	err := connection.QueryRow("PRAGMA user_version").Scan(&value)
	return value, err
}

func pragmaString(connection *sql.DB, statement string) (string, error) {
	var value string
	err := connection.QueryRow(statement).Scan(&value)
	return value, err
}

func splitSchemaStatements(schema string) []string {
	var withoutComments strings.Builder
	for _, line := range strings.Split(schema, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "--") {
			continue
		}
		withoutComments.WriteString(line)
		withoutComments.WriteByte('\n')
	}
	statements := []string{}
	for _, statement := range strings.Split(withoutComments.String(), ";") {
		if trimmed := strings.TrimSpace(statement); trimmed != "" {
			statements = append(statements, trimmed)
		}
	}
	return statements
}

func pyValue(value any) string {
	switch typed := value.(type) {
	case nil:
		return "None"
	case []byte:
		return string(typed)
	case int64:
		return strconv.FormatInt(typed, 10)
	case int:
		return strconv.Itoa(typed)
	default:
		return fmt.Sprint(typed)
	}
}

func expandHome(path string) string {
	if path == "~" {
		if home, err := os.UserHomeDir(); err == nil {
			return home
		}
		return path
	}
	if strings.HasPrefix(path, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, path[2:])
		}
	}
	return path
}
