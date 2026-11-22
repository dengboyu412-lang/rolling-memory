// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package localvault

import (
	"context"
	"os"
	"strings"
	"time"

	core "github.com/rocareer/rolling-memory/internal/core"
)

// cachedPublishedSnapshot returns the snapshot-only read generation when the
// active slot, codec key and purge epoch all still match.
func (v *Vault) cachedPublishedSnapshot(slot, codecKey string, purgeEpoch int64) (*vaultCorpusSnapshot, bool) {
	if v == nil || v.slotCacheMu == nil || v.snapRef == nil {
		return nil, false
	}
	v.slotCacheMu.RLock()
	snap := v.snapRef.publishedSnap
	slotOK := v.snapRef.publishedSlot == slot
	v.slotCacheMu.RUnlock()
	if snap != nil && slotOK && snap.episodicCodecKey == codecKey && snap.purgeEpoch == purgeEpoch {
		return snap, true
	}
	return nil, false
}

// loadPublishedSnapshot serves the active published generation and nothing
// newer. It is the snapshot-only recall path: writes stay in the change log
// until the background builder publishes the next slot, so a query never pays
// the delta-overlay cost and never triggers an in-process build.
//
// The docs sidecar is the generation's own metadata view. When it is missing or
// belongs to another signature, the live metadata view is paired with the
// published index instead; hits map by unit ID, so the index remains the
// authority for what is searchable.
func (v *Vault) loadPublishedSnapshot(ctx context.Context) (*vaultCorpusSnapshot, bool) {
	if v == nil || v.db == nil {
		return nil, false
	}
	if ctx == nil {
		ctx = context.Background()
	}
	dbPath := v.mainDBPath()
	if strings.TrimSpace(dbPath) == "" {
		return nil, false
	}
	csrPath, genPath, _, docsPath, slot, ok := resolvePublishedSnapshotPaths(dbPath)
	if !ok {
		return nil, false
	}
	codecKey, err := v.episodicCodecKey()
	if err != nil {
		return nil, false
	}
	var livePurge int64
	if err := v.db.QueryRowContext(ctx, `SELECT purge_epoch FROM rm_revision WHERE id = 1`).Scan(&livePurge); err != nil {
		return nil, false
	}
	if snap, ok := v.cachedPublishedSnapshot(slot, string(codecKey), livePurge); ok {
		return snap, true
	}
	if livePurge > 0 {
		built, known := v.snapshotBuiltPurgeEpoch(ctx)
		if !known || built != livePurge {
			return nil, false
		}
	}
	if v.snapRef != nil {
		v.snapRef.buildMu.Lock()
		defer v.snapRef.buildMu.Unlock()
	}
	// A publisher may have advanced CURRENT while this caller waited. Re-resolve
	// under the construction lock so the CSR and docs sidecar come from one slot.
	csrPath, genPath, _, docsPath, slot, ok = resolvePublishedSnapshotPaths(dbPath)
	if !ok {
		return nil, false
	}
	if snap, ok := v.cachedPublishedSnapshot(slot, string(codecKey), livePurge); ok {
		return snap, true
	}
	rawGen, err := os.ReadFile(genPath)
	if err != nil {
		return nil, false
	}
	genLine := strings.TrimSpace(string(rawGen))
	eng, err := core.OpenDiskClusterEngine(csrPath)
	if err != nil || eng == nil || eng.Index == nil || eng.Index.N <= 0 {
		return nil, false
	}
	eng.MaterializeClusterState()
	docs, header, ok := loadCorpusDocsSidecarAt(docsPath)
	if !ok || len(docs) == 0 || header.Count != len(docs) ||
		header.ChangeSeq <= 0 ||
		strings.TrimSpace(header.Signature) != genLine ||
		signatureCodecKey(header.Signature) != string(codecKey) {
		liveDocs, _, docsErr := v.canonicalSnapshotDocs(ctx)
		if docsErr != nil || len(liveDocs) == 0 {
			return nil, false
		}
		docs = liveDocs
		header = corpusDocsSidecarHeader{Signature: genLine, Count: len(liveDocs)}
	}
	state := canonicalCorpusState{
		docCnt:     int64(len(docs)),
		changeSeq:  header.ChangeSeq,
		purgeEpoch: livePurge,
		codecKey:   string(codecKey),
		signature:  genLine,
	}
	snap := canonicalSnapshotFromDocs(state, docs, eng)
	if snap == nil {
		return nil, false
	}
	snap.changeSeq = header.ChangeSeq
	if v.slotCacheMu != nil && v.snapRef != nil {
		v.slotCacheMu.Lock()
		v.snapRef.publishedSnap = snap
		v.snapRef.publishedSlot = slot
		v.snapRef.lastSnapTime = time.Now()
		v.slotCacheMu.Unlock()
	}
	return snap, true
}
