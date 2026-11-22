// plugin: rolling-memory v0.0.58
package rollingmemory

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

func openTestSQLite(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// TestReadAdminLedgerFingerprint: missing files and unbound ledgers read as ""
// (never as a foreign identity), bound ledgers read their stored value.
func TestReadAdminLedgerFingerprint(t *testing.T) {
	dir := t.TempDir()
	missing := filepath.Join(dir, "missing.sqlite")
	if got := readAdminLedgerFingerprint(missing); got != "" {
		t.Fatalf("missing ledger fingerprint = %q, want empty", got)
	}
	unbound := filepath.Join(dir, "unbound.sqlite")
	db := openTestSQLite(t, unbound)
	if _, err := db.Exec(`CREATE TABLE efficacy_event(seq INTEGER PRIMARY KEY)`); err != nil {
		t.Fatal(err)
	}
	if got := readAdminLedgerFingerprint(unbound); got != "" {
		t.Fatalf("unbound ledger fingerprint = %q, want empty", got)
	}
	bound := filepath.Join(dir, "bound.sqlite")
	boundDB := openTestSQLite(t, bound)
	if _, err := boundDB.Exec(`CREATE TABLE efficacy_meta(key TEXT PRIMARY KEY, value TEXT NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	if _, err := boundDB.Exec(`INSERT INTO efficacy_meta(key, value) VALUES ('db_fingerprint', 'fp-123')`); err != nil {
		t.Fatal(err)
	}
	if got := readAdminLedgerFingerprint(bound); got != "fp-123" {
		t.Fatalf("bound ledger fingerprint = %q, want fp-123", got)
	}
}

// TestRotateAdminLedgerEpochArchives: rotation moves the ledger and its WAL/SHM
// companions beside themselves under .epoch-<unix> and never deletes data; a
// second rotation in the same second picks a fresh suffix.
func TestRotateAdminLedgerEpochArchives(t *testing.T) {
	dir := t.TempDir()
	ledgerPath := filepath.Join(dir, "memory.db.efficacy.sqlite")
	for _, p := range []string{ledgerPath, ledgerPath + "-wal", ledgerPath + "-shm"} {
		if err := os.WriteFile(p, []byte("data"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := rotateAdminLedgerEpoch(ledgerPath); err != nil {
		t.Fatalf("rotate: %v", err)
	}
	if _, err := os.Stat(ledgerPath); !os.IsNotExist(err) {
		t.Fatalf("original ledger still present: %v", err)
	}
	matches, err := filepath.Glob(ledgerPath + ".epoch-*")
	if err != nil {
		t.Fatal(err)
	}
	archived := 0
	for _, m := range matches {
		if strings.HasSuffix(m, "-wal") || strings.HasSuffix(m, "-shm") {
			continue
		}
		archived++
		if raw, err := os.ReadFile(m); err != nil || string(raw) != "data" {
			t.Fatalf("archived ledger content = %q, %v", string(raw), err)
		}
	}
	if archived != 1 {
		t.Fatalf("archived ledgers = %d, want 1", archived)
	}
	// A second rotation in the same second must not overwrite the first archive.
	if err := os.WriteFile(ledgerPath, []byte("data2"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := rotateAdminLedgerEpoch(ledgerPath); err != nil {
		t.Fatalf("second rotate: %v", err)
	}
	matches, _ = filepath.Glob(ledgerPath + ".epoch-*")
	archived = 0
	for _, m := range matches {
		if strings.HasSuffix(m, "-wal") || strings.HasSuffix(m, "-shm") {
			continue
		}
		archived++
	}
	if archived != 2 {
		t.Fatalf("archived ledgers after second rotation = %d, want 2", archived)
	}
}

func seedEfficacyEvent(t *testing.T, path, eventID string) {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS efficacy_event (
		seq INTEGER PRIMARY KEY AUTOINCREMENT,
		event_id TEXT NOT NULL UNIQUE,
		execution_id TEXT NOT NULL, session TEXT NOT NULL, scope TEXT NOT NULL,
		harness TEXT NOT NULL, hook TEXT NOT NULL, output_outcome TEXT NOT NULL,
		action TEXT NOT NULL, target_event_id TEXT NOT NULL, observed_at INTEGER NOT NULL,
		observation TEXT NOT NULL, text_sha256 TEXT NOT NULL, text_bytes INTEGER
	)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT OR IGNORE INTO efficacy_event(
		event_id, execution_id, session, scope, harness, hook, output_outcome,
		action, target_event_id, observed_at, observation, text_sha256, text_bytes
	) VALUES (?, 'exec', 'sess', 'mem:self', 'zcode', 'mcp_recall:tool', 'hits', 'recall', '', 1, 'observed', 'sha', 10)`, eventID); err != nil {
		t.Fatal(err)
	}
}

func bindTestLedgerFingerprint(t *testing.T, path, fp string) {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS efficacy_meta(key TEXT PRIMARY KEY, value TEXT NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT OR REPLACE INTO efficacy_meta(key, value) VALUES ('db_fingerprint', ?)`, fp); err != nil {
		t.Fatal(err)
	}
}

func ledgerHasEvent(t *testing.T, path, eventID string) bool {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM efficacy_event WHERE event_id = ?`, eventID).Scan(&count); err != nil {
		t.Fatalf("query events in %s: %v", path, err)
	}
	return count > 0
}

// TestSyncProductionRecallLedgerIdentityAndArchiveSafety pins both halves of the
// sync gate: a ledger bound to another vault is rotated aside (never merged back
// and never rewritten in place), a sidecar carrying a foreign fingerprint stays
// on disk untouched, and only a matching sidecar that merged successfully is
// removed.
func TestSyncProductionRecallLedgerIdentityAndArchiveSafety(t *testing.T) {
	home := t.TempDir()
	memoryPath := filepath.Join(home, ".local", "share", "rolling-memory", "memory.db")
	if err := os.MkdirAll(filepath.Dir(memoryPath), 0o755); err != nil {
		t.Fatal(err)
	}
	memory := openTestSQLite(t, memoryPath)
	if _, err := memory.Exec(`CREATE TABLE db_meta(key TEXT PRIMARY KEY, value TEXT NOT NULL);
		CREATE TABLE rm_units(doc_id TEXT, occurred_at INTEGER)`); err != nil {
		t.Fatal(err)
	}
	if _, err := memory.Exec(`INSERT INTO db_meta(key, value) VALUES ('created_at', '2026-01-01T00:00:00Z');
		INSERT INTO rm_units(doc_id, occurred_at) VALUES ('doc-1', 1)`); err != nil {
		t.Fatal(err)
	}
	fp := computeAdminDBFingerprint(memoryPath, UserScope)
	if fp == "" {
		t.Fatal("fingerprint of the synthetic vault is empty")
	}

	ledgerPath := memoryPath + ".efficacy.sqlite"
	// The current ledger belongs to another vault: it must be rotated aside.
	bindTestLedgerFingerprint(t, ledgerPath, "fp-foreign")
	seedEfficacyEvent(t, ledgerPath, "foreign-ledger-event")

	// A foreign sidecar and a matching sidecar.
	foreignSidecar := ledgerPath + ".epoch-100"
	bindTestLedgerFingerprint(t, foreignSidecar, "fp-foreign")
	seedEfficacyEvent(t, foreignSidecar, "foreign-sidecar-event")
	matchingSidecar := ledgerPath + ".epoch-101"
	bindTestLedgerFingerprint(t, matchingSidecar, fp)
	seedEfficacyEvent(t, matchingSidecar, "matching-sidecar-event")

	t.Setenv("ROLLING_MEMORY_DB", "")
	syncProductionRecallLedger(home, memoryPath)

	if got := readAdminLedgerFingerprint(ledgerPath); got != fp {
		t.Fatalf("ledger fingerprint = %q, want the current vault fingerprint", got)
	}
	if ledgerHasEvent(t, ledgerPath, "foreign-ledger-event") {
		t.Fatalf("foreign ledger events were merged into the current ledger")
	}
	if ledgerHasEvent(t, ledgerPath, "foreign-sidecar-event") {
		t.Fatalf("foreign sidecar events were merged into the current ledger")
	}
	if !ledgerHasEvent(t, ledgerPath, "matching-sidecar-event") {
		t.Fatalf("matching sidecar event was not merged")
	}
	if _, err := os.Stat(matchingSidecar); !os.IsNotExist(err) {
		t.Fatalf("merged sidecar was not removed: %v", err)
	}
	if _, err := os.Stat(foreignSidecar); err != nil {
		t.Fatalf("foreign sidecar must stay on disk: %v", err)
	}
	rotated, _ := filepath.Glob(ledgerPath + ".epoch-*")
	foundForeignLedger := false
	for _, m := range rotated {
		if strings.HasSuffix(m, "-wal") || strings.HasSuffix(m, "-shm") {
			continue
		}
		if readAdminLedgerFingerprint(m) == "fp-foreign" && ledgerHasEvent(t, m, "foreign-ledger-event") {
			foundForeignLedger = true
		}
	}
	if !foundForeignLedger {
		t.Fatalf("the foreign ledger was not archived aside: %v", rotated)
	}
}
