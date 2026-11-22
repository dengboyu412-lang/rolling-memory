// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package efficacy_test

import (
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/rocareer/rolling-memory/internal/efficacy"
)

func openFingerprintFixture(t *testing.T) *sql.DB {
	t.Helper()
	path := filepath.Join(t.TempDir(), "memory.sqlite")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(`
		CREATE TABLE db_meta (key TEXT PRIMARY KEY, value TEXT);
		CREATE TABLE rm_docs (unit_id TEXT PRIMARY KEY, occurred_at INTEGER);
		INSERT INTO db_meta VALUES ('created_at', '1795167633'), ('engine', 'rolling-memory');
		INSERT INTO rm_docs VALUES ('ep_oldest', 100);
	`); err != nil {
		t.Fatal(err)
	}
	return db
}

// TestStableFingerprintIgnoresCorpusChurn pins the identity contract: adding a
// background row with occurred_at = 0 changes what "oldest" means, but it must
// not change the vault fingerprint (that would rotate the ledger and hide its
// direct recall events).
func TestStableFingerprintIgnoresCorpusChurn(t *testing.T) {
	db := openFingerprintFixture(t)
	before := efficacy.ComputeDBFingerprint(db, "mem:self")
	if before == "" {
		t.Fatal("empty stable fingerprint")
	}
	if _, err := db.Exec(`INSERT INTO rm_docs VALUES ('bg_new#0', 0)`); err != nil {
		t.Fatal(err)
	}
	after := efficacy.ComputeDBFingerprint(db, "mem:self")
	if after != before {
		t.Fatalf("stable fingerprint changed after background insert: %s -> %s", before, after)
	}
	legacyAfter := efficacy.LegacyDBFingerprint(db)
	if legacyAfter == "" {
		t.Fatal("empty legacy fingerprint")
	}
	if legacyAfter == before {
		t.Fatalf("legacy fingerprint unexpectedly equals the stable one: %s", legacyAfter)
	}
}

// TestLegacyFingerprintRebindsInPlace pins the upgrade path: a ledger written by
// an older build keeps every event and is rebound to the stable identity instead
// of being rotated aside.
func TestLegacyFingerprintRebindsInPlace(t *testing.T) {
	db := openFingerprintFixture(t)
	stable := efficacy.ComputeDBFingerprint(db, "mem:self")
	legacy := efficacy.LegacyDBFingerprint(db)
	if stable == "" || legacy == "" || stable == legacy {
		t.Fatalf("fixture fingerprints: stable=%q legacy=%q", stable, legacy)
	}
	ledgerPath := filepath.Join(t.TempDir(), "ledger.sqlite")
	ledger, err := efficacy.OpenLedger(ledgerPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := ledger.BindDatabaseFingerprint(legacy, ""); err != nil {
		t.Fatal(err)
	}
	if err := ledger.Append(observedEvent("kept")); err != nil {
		t.Fatal(err)
	}
	if err := ledger.Close(); err != nil {
		t.Fatal(err)
	}

	ledger, err = efficacy.OpenBoundLedger(ledgerPath, stable, legacy)
	if err != nil {
		t.Fatalf("legacy ledger was not rebound: %v", err)
	}
	defer ledger.Close()
	events, err := ledger.Events(0, 10)
	if err != nil || len(events) != 1 || events[0].EventID != "kept" {
		t.Fatalf("events after rebind = %#v, %v", events, err)
	}

	check, err := sql.Open("sqlite", ledgerPath)
	if err != nil {
		t.Fatal(err)
	}
	defer check.Close()
	var stored string
	if err := check.QueryRow(`SELECT value FROM efficacy_meta WHERE key = 'db_fingerprint'`).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored != stable {
		t.Fatalf("stored fingerprint = %q, want %q", stored, stable)
	}
}
