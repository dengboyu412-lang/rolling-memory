// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package localvault_test

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rocareer/rolling-memory/internal/memory/localvault"
)

func newNoeticTestVault(t testing.TB) (*sql.DB, *localvault.Vault, *localvault.Client) {
	t.Helper()
	db, err := sql.Open("sqlite", vaultTestDSN(filepath.Join(t.TempDir(), "noetic-trace.db")))
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	vault, err := localvault.NewVault(db, []byte("noetic-trace-secret"))
	if err != nil {
		t.Fatalf("NewVault: %v", err)
	}
	return db, vault, localvault.NewClient(vault, nil)
}

// The evidence set is the recollection; the cue is only a handle. Two phrasings
// of the same evidence resolve to one trace, and each phrasing can still recall
// that trace with an exact match.
func TestNoeticTraceIdentityMergesCueVariants(t *testing.T) {
	db, _, client := newNoeticTestVault(t)
	if err := client.RememberNoetic("NT-A", "alpha beta cue", []string{"u2", "u1"}, []string{"evidence"}, "ans", 100); err != nil {
		t.Fatalf("RememberNoetic NT-A: %v", err)
	}
	if err := client.RememberNoetic("NT-B", "gamma delta cue", []string{"u1", "u2"}, []string{"evidence"}, "ans", 200); err != nil {
		t.Fatalf("RememberNoetic NT-B: %v", err)
	}

	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM rm_noetic`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("trace rows = %d err=%v, want one row per evidence set", count, err)
	}
	var id, primary, variantsJSON, traceKey string
	if err := db.QueryRow(`SELECT noetic_id, query_text, query_variants_json, trace_key FROM rm_noetic`).Scan(&id, &primary, &variantsJSON, &traceKey); err != nil {
		t.Fatalf("read trace: %v", err)
	}
	if id != "NT-A" || primary != "gamma delta cue" || !strings.Contains(variantsJSON, "alpha beta cue") {
		t.Fatalf("merged trace = id %q primary %q variants %q", id, primary, variantsJSON)
	}
	if want := localvault.NoeticTraceKey([]string{"u1", "u2"}); traceKey != want {
		t.Fatalf("trace key = %q, want %q", traceKey, want)
	}

	alpha, err := client.RecallNoetic("alpha beta cue", 10)
	if err != nil || len(alpha) != 1 || alpha[0].NoeticID != "NT-A" || alpha[0].MatchedCue != "alpha beta cue" {
		t.Fatalf("alpha recall = %+v err=%v", alpha, err)
	}
	gamma, err := client.RecallNoetic("gamma delta cue", 10)
	if err != nil || len(gamma) != 1 || gamma[0].NoeticID != "NT-A" || gamma[0].MatchedCue != "gamma delta cue" {
		t.Fatalf("gamma recall = %+v err=%v", gamma, err)
	}
}

// The handle set is bounded: the newest cue stays primary, the previous primary
// becomes a variant, and the oldest phrasings fall off the cap.
func TestNoeticTraceVariantCap(t *testing.T) {
	db, _, client := newNoeticTestVault(t)
	for i := 0; i < 8; i++ {
		cue := fmt.Sprintf("variant cue number %d", i)
		if err := client.RememberNoetic(fmt.Sprintf("NT-%d", i), cue, []string{"u1"}, []string{"evidence"}, "ans", int64(100+i)); err != nil {
			t.Fatalf("RememberNoetic %d: %v", i, err)
		}
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM rm_noetic`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("trace rows = %d err=%v, want 1", count, err)
	}
	var primary, variantsJSON string
	if err := db.QueryRow(`SELECT query_text, query_variants_json FROM rm_noetic`).Scan(&primary, &variantsJSON); err != nil {
		t.Fatalf("read trace: %v", err)
	}
	var variants []string
	if err := json.Unmarshal([]byte(variantsJSON), &variants); err != nil {
		t.Fatalf("variants json: %v", err)
	}
	if primary != "variant cue number 7" {
		t.Fatalf("primary = %q, want the newest cue", primary)
	}
	if len(variants) != 4 {
		t.Fatalf("variants = %v, want the 4 newest prior handles", variants)
	}
	recent, err := client.RecallNoetic("variant cue number 5", 10)
	if err != nil || len(recent) != 1 {
		t.Fatalf("retained variant recall = %+v err=%v", recent, err)
	}
	if strings.Contains(variantsJSON, "variant cue number 0") {
		t.Fatalf("oldest handle survived the cap: %s", variantsJSON)
	}
}

// A new evidence set for the same cue is the current answer; the older trace is
// retired but kept for audit, and can be revived by a later crystallization.
func TestNoeticSameCueSuccessionAndRevival(t *testing.T) {
	db, vault, client := newNoeticTestVault(t)
	const cue = "same question repeated"
	if err := client.RememberNoetic("NT-OLD", cue, []string{"u1"}, []string{"old"}, "old answer", 100); err != nil {
		t.Fatalf("RememberNoetic old: %v", err)
	}
	if err := client.RememberNoetic("NT-NEW", cue, []string{"u2"}, []string{"new"}, "new answer", 200); err != nil {
		t.Fatalf("RememberNoetic new: %v", err)
	}
	var status, supersededBy string
	if err := db.QueryRow(`SELECT status, superseded_by FROM rm_noetic WHERE noetic_id = 'NT-OLD'`).Scan(&status, &supersededBy); err != nil {
		t.Fatalf("read old trace: %v", err)
	}
	if status != "superseded" || supersededBy != "NT-NEW" {
		t.Fatalf("old trace = status %q by %q, want superseded by NT-NEW", status, supersededBy)
	}
	hits, err := client.RecallNoetic(cue, 10)
	if err != nil || len(hits) != 1 || hits[0].NoeticID != "NT-NEW" {
		t.Fatalf("recall after succession = %+v err=%v", hits, err)
	}
	if activated, err := vault.ActivateNoetics([]string{"NT-OLD"}, 300); err != nil || activated != 0 {
		t.Fatalf("superseded activation = %d err=%v, want 0", activated, err)
	}

	// Re-consolidating the old evidence revives its trace and retires the newer one.
	if err := client.RememberNoetic("NT-REVIVE", cue, []string{"u1"}, []string{"old"}, "old answer", 400); err != nil {
		t.Fatalf("RememberNoetic revive: %v", err)
	}
	if err := db.QueryRow(`SELECT status FROM rm_noetic WHERE noetic_id = 'NT-OLD'`).Scan(&status); err != nil {
		t.Fatalf("read revived trace: %v", err)
	}
	if status != "active" {
		t.Fatalf("revived trace status = %q, want active", status)
	}
	var newStatus string
	if err := db.QueryRow(`SELECT status FROM rm_noetic WHERE noetic_id = 'NT-NEW'`).Scan(&newStatus); err != nil {
		t.Fatalf("read retired trace: %v", err)
	}
	if newStatus != "superseded" {
		t.Fatalf("previous trace status = %q, want superseded", newStatus)
	}
	hits, err = client.RecallNoetic(cue, 10)
	if err != nil || len(hits) != 1 || hits[0].NoeticID != "NT-OLD" {
		t.Fatalf("recall after revival = %+v err=%v", hits, err)
	}
}

// Different cues over different evidence sets stay independent traces.
func TestNoeticDifferentCueKeepsTraces(t *testing.T) {
	db, _, client := newNoeticTestVault(t)
	if err := client.RememberNoetic("NT-X", "cue x", []string{"u1"}, []string{"x"}, "ans", 100); err != nil {
		t.Fatalf("RememberNoetic x: %v", err)
	}
	if err := client.RememberNoetic("NT-Y", "cue y", []string{"u2"}, []string{"y"}, "ans", 200); err != nil {
		t.Fatalf("RememberNoetic y: %v", err)
	}
	var active int
	if err := db.QueryRow(`SELECT COUNT(*) FROM rm_noetic WHERE status = 'active'`).Scan(&active); err != nil || active != 2 {
		t.Fatalf("active traces = %d err=%v, want 2", active, err)
	}
	xHits, err := client.RecallNoetic("cue x", 10)
	if err != nil || len(xHits) != 1 || xHits[0].NoeticID != "NT-X" {
		t.Fatalf("cue x recall = %+v err=%v", xHits, err)
	}
	yHits, err := client.RecallNoetic("cue y", 10)
	if err != nil || len(yHits) != 1 || yHits[0].NoeticID != "NT-Y" {
		t.Fatalf("cue y recall = %+v err=%v", yHits, err)
	}
}

// Activation is a counter of real use, with no time decay: an old row is still
// recalled today, and two activations raise the continuous gain.
func TestNoeticActivationHasNoDecay(t *testing.T) {
	db, vault, client := newNoeticTestVault(t)
	if err := client.RememberNoetic("NT-ANCIENT", "ancient cue", []string{"u1"}, []string{"old"}, "ans", 100); err != nil {
		t.Fatalf("RememberNoetic: %v", err)
	}
	if hits, err := client.RecallNoetic("ancient cue", 10); err != nil || len(hits) != 1 {
		t.Fatalf("old row recall = %+v err=%v, want it kept", hits, err)
	}
	for i := 0; i < 2; i++ {
		if activated, err := vault.ActivateNoetics([]string{"NT-ANCIENT"}, int64(1000+i)); err != nil || activated != 1 {
			t.Fatalf("ActivateNoetics %d = %d err=%v", i, activated, err)
		}
	}
	var count int
	var activatedAt int64
	if err := db.QueryRow(`SELECT activation_count, activated_at FROM rm_noetic WHERE noetic_id = 'NT-ANCIENT'`).Scan(&count, &activatedAt); err != nil {
		t.Fatalf("read activation: %v", err)
	}
	if count != 2 || activatedAt != 1001 {
		t.Fatalf("activation = count %d at %d, want 2 at 1001", count, activatedAt)
	}
	hits, err := client.RecallNoetic("ancient cue", 10)
	if err != nil || len(hits) != 1 || hits[0].ActivationGain() <= 1.6 {
		t.Fatalf("gain after two uses = %+v err=%v", hits, err)
	}
}

// A legacy vault without the identity columns upgrades in place: trace keys are
// backfilled from the cited ids and the row stays recallable.
func TestNoeticTraceMigrationBackfillsKeys(t *testing.T) {
	db, err := sql.Open("sqlite", vaultTestDSN(filepath.Join(t.TempDir(), "noetic-legacy.db")))
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	defer db.Close()
	for _, stmt := range []string{
		`CREATE TABLE rm_meta (k TEXT PRIMARY KEY, v BLOB NOT NULL)`,
		`CREATE TABLE rm_noetic (
			noetic_id TEXT PRIMARY KEY,
			occurred_at INTEGER NOT NULL DEFAULT 0,
			status TEXT NOT NULL DEFAULT 'active',
			query_text TEXT NOT NULL,
			answer_text TEXT NOT NULL,
			cited_unit_ids_json TEXT NOT NULL DEFAULT '[]',
			cited_sentences_json TEXT NOT NULL DEFAULT '[]'
		)`,
		`INSERT INTO rm_noetic(noetic_id, occurred_at, status, query_text, answer_text, cited_unit_ids_json, cited_sentences_json)
		 VALUES ('NT-LEGACY', 100, 'active', 'legacy cue', 'ans', '["u1","u2"]', '["evidence"]')`,
	} {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("seed legacy schema: %v", err)
		}
	}
	vault, err := localvault.NewVault(db, []byte("noetic-legacy-secret"))
	if err != nil {
		t.Fatalf("NewVault migration: %v", err)
	}
	var traceKey, variants string
	var activation int
	if err := db.QueryRow(`SELECT trace_key, query_variants_json, activation_count FROM rm_noetic WHERE noetic_id = 'NT-LEGACY'`).
		Scan(&traceKey, &variants, &activation); err != nil {
		t.Fatalf("read migrated row: %v", err)
	}
	if want := localvault.NoeticTraceKey([]string{"u1", "u2"}); traceKey != want {
		t.Fatalf("trace key = %q, want %q", traceKey, want)
	}
	if variants != "[]" || activation != 0 {
		t.Fatalf("migrated defaults = variants %q activation %d", variants, activation)
	}
	client := localvault.NewClient(vault, nil)
	hits, err := client.RecallNoetic("legacy cue", 10)
	if err != nil || len(hits) != 1 || hits[0].NoeticID != "NT-LEGACY" {
		t.Fatalf("legacy recall = %+v err=%v", hits, err)
	}
	// Second open is idempotent: the marker skips the backfill scan.
	if _, err := localvault.NewVault(db, []byte("noetic-legacy-secret")); err != nil {
		t.Fatalf("second NewVault: %v", err)
	}
}

// BenchmarkRecallNoeticCueVariants measures the matching cost when every trace
// keeps the full complement of cue handles.
func BenchmarkRecallNoeticCueVariants(b *testing.B) {
	db, _, client := newNoeticTestVault(b)
	tx, err := db.Begin()
	if err != nil {
		b.Fatalf("begin: %v", err)
	}
	stmt, err := tx.Prepare(`INSERT INTO rm_noetic(
		noetic_id, occurred_at, inserted_at, status, query_text, answer_text, cited_unit_ids_json, cited_sentences_json,
		activation_count, trace_key, query_variants_json
	) VALUES (?, ?, ?, 'active', ?, 'ans', ?, '["evidence"]', ?, ?, ?)`)
	if err != nil {
		b.Fatalf("prepare: %v", err)
	}
	for i := 0; i < 500; i++ {
		unit := fmt.Sprintf("u%d", i)
		primary := fmt.Sprintf("benchmark cue %d alpha", i)
		variants := []string{
			fmt.Sprintf("benchmark cue %d beta", i),
			fmt.Sprintf("benchmark cue %d gamma", i),
			fmt.Sprintf("benchmark cue %d delta", i),
			fmt.Sprintf("benchmark cue %d epsilon", i),
		}
		variantsJSON, _ := json.Marshal(variants)
		unitsJSON, _ := json.Marshal([]string{unit})
		if _, err := stmt.Exec(fmt.Sprintf("nt_bench_%d", i), int64(1000+i), int64(1000+i), primary,
			string(unitsJSON), i%3, localvault.NoeticTraceKey([]string{unit}), string(variantsJSON)); err != nil {
			b.Fatalf("insert %d: %v", i, err)
		}
	}
	_ = stmt.Close()
	if err := tx.Commit(); err != nil {
		b.Fatalf("commit: %v", err)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := client.RecallNoetic("benchmark cue 250 alpha", 10); err != nil {
			b.Fatalf("RecallNoetic: %v", err)
		}
	}
}

// TestNewVaultFastPathRestoresNoeticTraceColumns locks the upgrade path: a vault
// that already stores its secret key takes the short open path, which must still
// add the L3 lifecycle columns an older schema lacks.
func TestNewVaultFastPathRestoresNoeticTraceColumns(t *testing.T) {
	db, err := sql.Open("sqlite", vaultTestDSN(filepath.Join(t.TempDir(), "noetic-fast-path.db")))
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := localvault.NewVault(db, []byte("fast-path-secret")); err != nil {
		t.Fatalf("NewVault slow path: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO rm_meta(k, v) VALUES ('user_secret_key', ?)`, bytes.Repeat([]byte{7}, 32)); err != nil {
		t.Fatalf("store key: %v", err)
	}
	if _, err := db.Exec(`DROP INDEX IF EXISTS idx_rm_noetic_trace`); err != nil {
		t.Fatalf("drop trace index: %v", err)
	}
	for _, column := range []string{"query_variants_json", "trace_key", "superseded_by"} {
		if _, err := db.Exec(`ALTER TABLE rm_noetic DROP COLUMN ` + column); err != nil {
			t.Fatalf("drop %s: %v", column, err)
		}
	}
	if _, err := localvault.NewVault(db, nil); err != nil {
		t.Fatalf("NewVault fast path: %v", err)
	}
	rows, err := db.Query(`PRAGMA table_info(rm_noetic)`)
	if err != nil {
		t.Fatalf("table_info: %v", err)
	}
	defer rows.Close()
	columns := map[string]bool{}
	for rows.Next() {
		var cid, notNull, pk int
		var name, kind string
		var dflt any
		if err := rows.Scan(&cid, &name, &kind, &notNull, &dflt, &pk); err != nil {
			t.Fatalf("scan: %v", err)
		}
		columns[name] = true
	}
	for _, want := range []string{"query_variants_json", "trace_key", "superseded_by"} {
		if !columns[want] {
			t.Fatalf("fast path left rm_noetic without %s (columns=%v)", want, columns)
		}
	}
}
