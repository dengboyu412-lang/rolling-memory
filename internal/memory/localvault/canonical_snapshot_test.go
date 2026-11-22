// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package localvault

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The canonical snapshot builds from Store.LoadIndex, persists the .zcsr
// baseline, and serves later writes through the bounded delta overlay.
func TestCanonicalSnapshotUsesStoreIndexAndDelta(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "canonical_snapshot.sqlite")
	db, err := sql.Open("sqlite", vaultTestDSN(dbPath))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	v, err := NewVault(db, []byte("canonical-snapshot-key"))
	if err != nil {
		t.Fatal(err)
	}
	for i, text := range []string{
		"Alice met Bob in Paris on 2026-01-02.",
		"Carol booked a hotel near the harbour for the weekend.",
		"Dave paid fifty pounds for the boat tour ticket.",
	} {
		if err := v.SaveLocalPlaintext("doc-"+string(rune('a'+i)), 1700000000+int64(i), text, nil, IngestResponse{Layer: "episodic"}); err != nil {
			t.Fatal(err)
		}
	}
	snap, err := v.buildCorpusSnapshot(context.Background())
	if err != nil || snap == nil {
		t.Fatalf("build snapshot: %v", err)
	}
	if !strings.HasPrefix(snap.freshnessSignature, "v5|") {
		t.Fatalf("snapshot must carry the canonical v5 signature, got %q", snap.freshnessSignature)
	}
	eng := snap.ensureSnapshotCoreEngine(v)
	if eng == nil || eng.Index == nil || eng.Index.N != 3 {
		t.Fatalf("canonical engine must index three units, got %+v", eng)
	}
	if _, err := os.Stat(dbPath + ".zcsr"); err != nil {
		t.Fatalf(".zcsr baseline must be persisted: %v", err)
	}
	if _, err := os.Stat(dbPath + ".zcsr.units"); err != nil {
		t.Fatalf("unit revision sidecar must be persisted: %v", err)
	}

	if err := v.SaveLocalPlaintext("doc-d", 1700000010, "Erin took the train to Berlin in March.", nil, IngestResponse{Layer: "episodic"}); err != nil {
		t.Fatal(err)
	}
	next, err := v.buildCorpusSnapshot(context.Background())
	if err != nil || next == nil {
		t.Fatalf("build snapshot after write: %v", err)
	}
	nextEng := next.ensureSnapshotCoreEngine(v)
	if nextEng == nil {
		t.Fatal("delta engine must be available")
	}
	if !nextEng.HasDelta() || nextEng.DeltaDocCount() != 1 {
		t.Fatalf("expected a one-unit delta overlay, hasDelta=%v count=%d", nextEng.HasDelta(), nextEng.DeltaDocCount())
	}
}

// SQLite has no unsigned integers: the kernel persists its 64-bit document and
// block hashes as signed INTEGER, so a hash with the high bit set reads back as
// a negative int64. The snapshot metadata scan must widen through int64 instead
// of decoding the literal as uint64, otherwise those rows abort the scan and the
// published generation loses both its docs sidecar and every reader served from
// the canonical fallback.
func TestCanonicalSnapshotDocsReadHighBitHashes(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "high_bit_hashes.sqlite")
	db, err := sql.Open("sqlite", vaultTestDSN(dbPath))
	if err != nil {
		t.Fatal(err)
	}
	v, err := NewVault(db, []byte("high-bit-hash-key"))
	if err != nil {
		t.Fatal(err)
	}
	if err := v.SaveLocalPlaintext("doc-a", 1700000000, "Alice met Bob in Paris on 2026-01-02.", nil, IngestResponse{Layer: "episodic"}); err != nil {
		t.Fatal(err)
	}
	const (
		wantDocHash   = uint64(0x8000000000000001)
		wantBlockHash = uint64(0xffffffffffffffff)
	)
	storedDocHash, storedBlockHash := wantDocHash, wantBlockHash
	if _, err := db.Exec(`UPDATE rm_docs SET doc_hash = ?, block_hash = ? WHERE unit_id = 'doc-a'`,
		int64(storedDocHash), int64(storedBlockHash)); err != nil {
		t.Fatal(err)
	}

	docs, _, err := v.canonicalSnapshotDocs(ctx)
	if err != nil {
		t.Fatalf("canonicalSnapshotDocs: %v", err)
	}
	if len(docs) != 1 || docs[0].docHash != wantDocHash || docs[0].blockHash != wantBlockHash {
		t.Fatalf("canonicalSnapshotDocs lost the unsigned hashes: %+v", docs)
	}
	docsFor, ok := v.canonicalSnapshotDocsFor(ctx, []string{"doc-a"})
	if !ok || len(docsFor) != 1 || docsFor[0].docHash != wantDocHash || docsFor[0].blockHash != wantBlockHash {
		t.Fatalf("canonicalSnapshotDocsFor lost the unsigned hashes: ok=%v docs=%+v", ok, docsFor)
	}

	if _, err := v.RebuildCorpusSnapshot(ctx); err != nil {
		t.Fatalf("rebuild: %v", err)
	}
	docsPath, ok := PublishedSnapshotDocsPath(dbPath)
	if !ok {
		t.Fatal("rebuild did not publish a generation")
	}
	if _, err := os.Stat(docsPath); err != nil {
		t.Fatalf("published docs sidecar missing: %v", err)
	}
	_ = db.Close()

	coldDB, err := sql.Open("sqlite", vaultTestDSN(dbPath))
	if err != nil {
		t.Fatal(err)
	}
	defer coldDB.Close()
	cold, err := NewVault(coldDB, []byte("high-bit-hash-key"))
	if err != nil {
		t.Fatal(err)
	}
	cold.SetBuildAllowed(false)
	snap, ok := cold.loadPublishedSnapshot(ctx)
	if !ok || snap == nil || len(snap.docs) != 1 {
		t.Fatalf("published generation not readable: ok=%v snap=%v", ok, snap != nil)
	}
	if snap.docs[0].docHash != wantDocHash || snap.docs[0].blockHash != wantBlockHash {
		t.Fatalf("published docs lost the unsigned hashes: %+v", snap.docs[0])
	}
}
