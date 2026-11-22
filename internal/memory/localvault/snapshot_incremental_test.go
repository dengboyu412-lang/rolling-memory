// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package localvault

import (
	"context"
	"database/sql"
	"encoding/gob"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"testing"

	core "github.com/rocareer/rolling-memory/internal/core"
)

type incrementalAuxProbe struct {
	Extended bool
}

func TestRebuildCorpusSnapshotUsesIncrementalGeneration(t *testing.T) {
	ctx := context.Background()
	db, err := sql.Open("sqlite", vaultTestDSN(filepath.Join(t.TempDir(), "incremental.sqlite")))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	v, err := NewVault(db, []byte("incremental-key"))
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 240; i++ {
		text := fmt.Sprintf("unit %d records the alpha beta ledger and the harbour decision unique%03d.", i, i)
		if err := v.SaveLocalPlaintext(fmt.Sprintf("doc-%03d", i), 1700000000+int64(i)*3600, text, nil, IngestResponse{Layer: "episodic"}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := v.RebuildCorpusSnapshot(ctx); err != nil {
		t.Fatal(err)
	}
	assertGenerationAux(t, v, false)

	if err := v.SaveLocalPlaintext("doc-005", 1700000900, "rewritten unit five now records updated005 and the harbour ledger.", nil, IngestResponse{Layer: "episodic"}); err != nil {
		t.Fatal(err)
	}
	for i := 240; i < 280; i++ {
		text := fmt.Sprintf("fresh unit %d records the alpha beta ledger and the harbour decision unique%03d.", i, i)
		if err := v.SaveLocalPlaintext(fmt.Sprintf("doc-%03d", i), 1700000000+int64(i)*3600, text, nil, IngestResponse{Layer: "episodic"}); err != nil {
			t.Fatal(err)
		}
	}
	deleteCanonicalUnit(t, db, "doc-010")
	if _, err := v.RebuildCorpusSnapshot(ctx); err != nil {
		t.Fatal(err)
	}
	assertGenerationAux(t, v, true)

	v.SetBuildAllowed(false)
	if _, err := v.loadCorpusSnapshot(ctx); err != nil {
		t.Fatal(err)
	}
	assertSearchVisibility(t, v, "unique240", "doc-240", true)
	assertSearchVisibility(t, v, "unique010", "doc-010", false)
	assertSearchVisibility(t, v, "updated005", "doc-005", true)
	assertPublishedMatchesFull(t, v, ctx)

	for i := 280; i < 300; i++ {
		text := fmt.Sprintf("second wave unit %d records the alpha beta ledger and unique%03d.", i, i)
		if err := v.SaveLocalPlaintext(fmt.Sprintf("doc-%03d", i), 1700000000+int64(i)*3600, text, nil, IngestResponse{Layer: "episodic"}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := v.RebuildCorpusSnapshot(ctx); err != nil {
		t.Fatal(err)
	}
	assertGenerationAux(t, v, true)
	assertSearchVisibility(t, v, "unique299", "doc-299", true)
	assertPublishedMatchesFull(t, v, ctx)
}

func deleteCanonicalUnit(t *testing.T, db *sql.DB, unitID string) {
	t.Helper()
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`DELETE FROM rm_terms WHERE unit_id = ?`, unitID); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if _, err := tx.Exec(`DELETE FROM rm_docs WHERE unit_id = ?`, unitID); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func assertGenerationAux(t *testing.T, v *Vault, wantExtended bool) {
	t.Helper()
	_, _, _, _, slot, ok := resolvePublishedSnapshotPaths(v.mainDBPath())
	if !ok || slot == "" {
		t.Fatal("no published generation")
	}
	auxPath := filepath.Join(snapshotSlotDir(v.mainDBPath(), slot), snapshotSlotAgg)
	f, err := os.Open(auxPath)
	if err != nil {
		t.Fatalf("open build aux: %v", err)
	}
	defer f.Close()
	var probe incrementalAuxProbe
	if err := gob.NewDecoder(f).Decode(&probe); err != nil {
		t.Fatalf("decode build aux: %v", err)
	}
	if probe.Extended != wantExtended {
		t.Fatalf("build aux extended=%v, want %v", probe.Extended, wantExtended)
	}
}

func assertSearchVisibility(t *testing.T, v *Vault, query, unitID string, want bool) {
	t.Helper()
	hits, err := v.SearchClientSDKDefault(query, 20)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, hit := range hits {
		if hit.UnitID == unitID {
			found = true
			break
		}
	}
	if found != want {
		t.Fatalf("query %q unit %s visible=%v, want %v (hits=%d)", query, unitID, found, want, len(hits))
	}
}

func assertPublishedMatchesFull(t *testing.T, v *Vault, ctx context.Context) {
	t.Helper()
	csrPath, _, _, _, _, ok := resolvePublishedSnapshotPaths(v.mainDBPath())
	if !ok {
		t.Fatal("no published generation")
	}
	published, err := core.OpenDiskClusterEngine(csrPath)
	if err != nil {
		t.Fatal(err)
	}
	defer published.Close()
	full := v.buildCorpusEngine(ctx)
	if full == nil {
		t.Fatal("full rebuild failed")
	}
	query := make(map[uint64]core.Term, 4)
	for u := 0; u < full.Index.N && len(query) < 4; u++ {
		for _, hash := range full.Index.DocTerms[u] {
			if _, exists := query[hash]; exists {
				continue
			}
			query[hash] = core.Term{Hash: hash}
			if len(query) >= 4 {
				break
			}
		}
	}
	want := full.Search(query, 20)
	got := published.Search(query, 20)
	if len(want) != len(got) {
		t.Fatalf("published hits=%d want=%d", len(got), len(want))
	}
	for i := range want {
		if want[i].UnitID != got[i].UnitID || math.Float64bits(want[i].Energy) != math.Float64bits(got[i].Energy) {
			t.Fatalf("hit %d mismatch: published=%+v full=%+v", i, got[i], want[i])
		}
	}
}
