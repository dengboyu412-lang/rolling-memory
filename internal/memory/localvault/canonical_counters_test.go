// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package localvault

import (
	"database/sql"
	"path/filepath"
	"testing"
)

func readCanonicalCounters(t *testing.T, db *sql.DB) (docsCnt, liveDocs, termsCnt, liveTerms int64) {
	t.Helper()
	if err := db.QueryRow(`SELECT docs_cnt, terms_cnt FROM rm_revision WHERE id = 1`).Scan(&docsCnt, &termsCnt); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM rm_docs`).Scan(&liveDocs); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM rm_terms`).Scan(&liveTerms); err != nil {
		t.Fatal(err)
	}
	return docsCnt, liveDocs, termsCnt, liveTerms
}

// The canonical ingest rewrites a unit with INSERT OR REPLACE. SQLite resolves
// the primary-key conflict by deleting the old row without firing DELETE
// triggers, so the writer must compensate for the implicit delete; otherwise
// every rewrite leaks one phantom document. The rm_units view instead resolves
// its conflict with ON CONFLICT DO UPDATE and must leave the counter untouched.
func TestCanonicalDocCounterTracksReplacements(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "doc_counter.sqlite")
	db, err := sql.Open("sqlite", vaultTestDSN(dbPath))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	v, err := NewVault(db, []byte("doc-counter-key"))
	if err != nil {
		t.Fatal(err)
	}
	assertDocs := func(stage string, want int64) {
		t.Helper()
		docsCnt, liveDocs, _, _ := readCanonicalCounters(t, db)
		if docsCnt != want || liveDocs != want {
			t.Fatalf("%s: docs_cnt=%d live=%d, want both %d", stage, docsCnt, liveDocs, want)
		}
	}

	if err := v.SaveLocalPlaintext("doc-a", 1700000000, "Alice met Bob in Paris.", nil, IngestResponse{Layer: "episodic"}); err != nil {
		t.Fatal(err)
	}
	assertDocs("first insert", 1)

	if err := v.SaveLocalPlaintext("doc-a", 1700000100, "Alice met Carol in Berlin instead.", nil, IngestResponse{Layer: "episodic"}); err != nil {
		t.Fatal(err)
	}
	assertDocs("replacement", 1)

	if err := v.SaveLocalPlaintext("doc-b", 1700000200, "Dave paid fifty pounds for the boat tour.", nil, IngestResponse{Layer: "episodic"}); err != nil {
		t.Fatal(err)
	}
	assertDocs("second insert", 2)

	if _, err := db.Exec(`DELETE FROM rm_docs WHERE unit_id = 'doc-b'`); err != nil {
		t.Fatal(err)
	}
	assertDocs("hard delete", 1)

	// The rm_units view resolves its conflict with ON CONFLICT DO UPDATE, which
	// turns into a row update and must not change the document count.
	if _, err := db.Exec(`INSERT INTO rm_units(doc_id, layer, occurred_at, raw_text) VALUES ('doc-c', 'episodic', 1700000300, 'Carol took the train.')`); err != nil {
		t.Fatal(err)
	}
	assertDocs("view insert", 2)
	if _, err := db.Exec(`INSERT INTO rm_units(doc_id, layer, occurred_at, raw_text) VALUES ('doc-c', 'episodic', 1700000400, 'Carol took the boat instead.')`); err != nil {
		t.Fatal(err)
	}
	assertDocs("view upsert", 2)
}

// Vaults written before the compensating trigger keep an inflated docs_cnt.
// The v2 marker repairs the counters exactly once on the next open.
func TestCanonicalCountsBackfillRepairsDrift(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "counts_repair.sqlite")
	db, err := sql.Open("sqlite", vaultTestDSN(dbPath))
	if err != nil {
		t.Fatal(err)
	}
	v, err := NewVault(db, []byte("counts-repair-key"))
	if err != nil {
		t.Fatal(err)
	}
	if err := v.SaveLocalPlaintext("doc-a", 1700000000, "Alice met Bob in Paris.", nil, IngestResponse{Layer: "episodic"}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE rm_revision SET docs_cnt = 99, terms_cnt = 12345 WHERE id = 1`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DELETE FROM rm_migration WHERE key = 'canonical_counts_v2'`); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()

	reopened, err := sql.Open("sqlite", vaultTestDSN(dbPath))
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if _, err := NewVault(reopened, []byte("counts-repair-key")); err != nil {
		t.Fatal(err)
	}
	docsCnt, liveDocs, termsCnt, liveTerms := readCanonicalCounters(t, reopened)
	if docsCnt != liveDocs || termsCnt != liveTerms {
		t.Fatalf("counters not repaired: docs_cnt=%d live=%d terms_cnt=%d live=%d", docsCnt, liveDocs, termsCnt, liveTerms)
	}
	var marker int
	if err := reopened.QueryRow(`SELECT COUNT(*) FROM rm_migration WHERE key = 'canonical_counts_v2'`).Scan(&marker); err != nil || marker != 1 {
		t.Fatalf("v2 marker not recorded: count=%d err=%v", marker, err)
	}
	if _, err := reopened.Exec(`UPDATE rm_revision SET docs_cnt = 42 WHERE id = 1`); err != nil {
		t.Fatal(err)
	}
	_ = reopened.Close()

	again, err := sql.Open("sqlite", vaultTestDSN(dbPath))
	if err != nil {
		t.Fatal(err)
	}
	defer again.Close()
	if _, err := NewVault(again, []byte("counts-repair-key")); err != nil {
		t.Fatal(err)
	}
	if docsCnt, _, _, _ := readCanonicalCounters(t, again); docsCnt != 42 {
		t.Fatalf("v2 repair must run only once, docs_cnt=%d", docsCnt)
	}
}
