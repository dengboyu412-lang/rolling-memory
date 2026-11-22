// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package localvault

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"
)

func TestPublishedSnapshotIsAsync(t *testing.T) {
	ctx := context.Background()
	db, err := sql.Open("sqlite", vaultTestDSN(filepath.Join(t.TempDir(), "async.sqlite")))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	v, err := NewVault(db, []byte("async-key"))
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 60; i++ {
		text := fmt.Sprintf("unit %d records the alpha beta ledger and unique%03d.", i, i)
		if err := v.SaveLocalPlaintext(fmt.Sprintf("doc-%03d", i), 1700000000+int64(i)*3600, text, nil, IngestResponse{Layer: "episodic"}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := v.RebuildCorpusSnapshot(ctx); err != nil {
		t.Fatal(err)
	}
	v.SetBuildAllowed(false)
	if err := v.SaveLocalPlaintext("doc-060", 1700002000, "fresh unit sixty records the alpha ledger and unique060.", nil, IngestResponse{Layer: "episodic"}); err != nil {
		t.Fatal(err)
	}

	before, ok := v.loadPublishedSnapshot(ctx)
	if !ok || before == nil {
		t.Fatal("published generation not loaded")
	}
	assertSearchVisibility(t, v, "unique000", "doc-000", true)
	assertSearchVisibility(t, v, "unique060", "doc-060", false)

	afterWrite, ok := v.loadPublishedSnapshot(ctx)
	if !ok || afterWrite != before {
		t.Fatal("an unpublished write rebuilt the published snapshot")
	}
	if _, err := v.RebuildCorpusSnapshot(ctx); err != nil {
		t.Fatal(err)
	}
	assertSearchVisibility(t, v, "unique060", "doc-060", true)
	afterPublish, ok := v.loadPublishedSnapshot(ctx)
	if !ok || afterPublish == before {
		t.Fatal("a published generation was not picked up")
	}

	if err := v.SaveLocalPlaintext("doc-000", 1700003000, "rewritten unit zero records zqxwvuqwerty and the harbour ledger.", nil, IngestResponse{Layer: "episodic"}); err != nil {
		t.Fatal(err)
	}
	assertSearchVisibility(t, v, "zqxwvuqwerty", "doc-000", false)
	if _, err := v.RebuildCorpusSnapshot(ctx); err != nil {
		t.Fatal(err)
	}
	assertSearchVisibility(t, v, "zqxwvuqwerty", "doc-000", true)
}
