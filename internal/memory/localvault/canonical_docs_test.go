// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package localvault

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"reflect"
	"testing"

	core "github.com/rocareer/rolling-memory/internal/core"
)

func TestLoadChangedDocsMatchesStoreIndex(t *testing.T) {
	db, err := sql.Open("sqlite", vaultTestDSN(filepath.Join(t.TempDir(), "docs.sqlite")))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	v, err := NewVault(db, []byte("docs-key"))
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 80; i++ {
		text := fmt.Sprintf("unit %d records the alpha beta ledger for topic %d and the harbour decision.", i, i%7)
		if err := v.SaveLocalPlaintext(fmt.Sprintf("doc-%03d", i), 1700000000+int64(i)*3600, text, []string{text}, IngestResponse{Layer: "episodic"}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`INSERT INTO rm_citations(unit_id, cited_id, ord) VALUES (?, ?, 0)`, "doc-000", "doc-001"); err != nil {
		t.Fatal(err)
	}
	store, err := v.canonicalStore()
	if err != nil {
		t.Fatal(err)
	}
	idx, err := store.LoadIndex()
	if err != nil {
		t.Fatal(err)
	}
	docs, err := v.loadChangedDocs(context.Background(), idx.UnitIDs)
	if err != nil {
		t.Fatal(err)
	}
	if len(docs) != idx.N {
		t.Fatalf("hydrated %d docs, want %d", len(docs), idx.N)
	}
	rebuilt := core.BuildIndex(docs)
	if a, b := core.ComputeCorpusDigest(idx), core.ComputeCorpusDigest(rebuilt); a != b {
		t.Fatalf("corpus digest mismatch: store=%x hydrated=%x", a, b)
	}
	if idx.N != rebuilt.N || idx.MeanBits != rebuilt.MeanBits {
		t.Fatalf("index shape mismatch: store N=%d meanBits=%v, hydrated N=%d meanBits=%v",
			idx.N, idx.MeanBits, rebuilt.N, rebuilt.MeanBits)
	}
	if !reflect.DeepEqual(idx.UnitMeta, rebuilt.UnitMeta) {
		t.Fatalf("unit metadata mismatch")
	}
	for u := 0; u < idx.N; u++ {
		if !reflect.DeepEqual(idx.UnitBits[u], rebuilt.UnitBits[u]) ||
			!reflect.DeepEqual(idx.UnitFlags[u], rebuilt.UnitFlags[u]) ||
			!reflect.DeepEqual(idx.UnitRoleExt[u], rebuilt.UnitRoleExt[u]) ||
			!reflect.DeepEqual(idx.UnitExtFlags[u], rebuilt.UnitExtFlags[u]) ||
			!reflect.DeepEqual(idx.UnitGroups[u], rebuilt.UnitGroups[u]) ||
			!reflect.DeepEqual(idx.UnitPositions[u], rebuilt.UnitPositions[u]) {
			t.Fatalf("unit %d term maps mismatch", u)
		}
	}
}
