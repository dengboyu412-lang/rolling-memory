// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package localvault

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strconv"

	core "github.com/rocareer/rolling-memory/internal/core"
)

// incrementalBuildResult describes the generation produced from the published
// slot plus the change log. The caller publishes it with these markers.
type incrementalBuildResult struct {
	eng        *core.Engine
	docsRev    int64
	changeSeq  int64
	purgeEpoch int64
}

// buildCorpusEngineIncremental reopens the published segment, applies the
// change-log delta and extends the cluster state in place. It is the adapter
// around the kernel capability: RM decides the base generation and the delta,
// the kernel performs the index and cluster-state update.
//
// It returns an error whenever the published slot cannot be proven to be the
// generation recorded in the build marker, or the delta snapshot is unusable.
// The caller then falls back to the full build.
func (v *Vault) buildCorpusEngineIncremental(ctx context.Context, previousChangeSeq int64) (*incrementalBuildResult, error) {
	if v == nil || v.db == nil {
		return nil, errors.New("localvault: nil vault")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if previousChangeSeq <= 0 {
		return nil, errors.New("localvault: no published change watermark")
	}
	dbPath := v.mainDBPath()
	if dbPath == "" {
		return nil, errors.New("localvault: incremental build requires a file-backed vault")
	}
	csrPath, _, _, _, slot, ok := resolvePublishedSnapshotPaths(dbPath)
	if !ok || slot == "" {
		return nil, errors.New("localvault: no published generation to extend")
	}
	if len(slot) < 16 {
		return nil, fmt.Errorf("localvault: malformed snapshot slot %q", slot)
	}
	baseSeq, err := strconv.ParseInt(slot[:16], 16, 64)
	if err != nil || baseSeq != previousChangeSeq {
		return nil, fmt.Errorf("localvault: published slot %s does not match the build marker %d", slot, previousChangeSeq)
	}

	tx, err := v.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	var docsRev, changeSeq, purgeEpoch int64
	if err := tx.QueryRowContext(ctx, `SELECT docs_rev, change_seq, purge_epoch FROM rm_revision WHERE id = 1`).
		Scan(&docsRev, &changeSeq, &purgeEpoch); err != nil {
		return nil, err
	}
	if changeSeq <= previousChangeSeq {
		return nil, errors.New("localvault: no changes since the published generation")
	}
	lastOp := make(map[string]string, 64)
	rows, err := tx.QueryContext(ctx, `
		SELECT unit_id, op FROM rm_change
		WHERE seq > ? AND seq <= ? ORDER BY seq`, previousChangeSeq, changeSeq)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var unitID, op string
		if err := rows.Scan(&unitID, &op); err != nil {
			rows.Close()
			return nil, err
		}
		if unitID != "" && (op == "upsert" || op == "delete") {
			lastOp[unitID] = op
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()
	deletes := make([]string, 0, len(lastOp))
	upsertIDs := make([]string, 0, len(lastOp))
	for id, op := range lastOp {
		if op == "delete" {
			deletes = append(deletes, id)
		} else {
			upsertIDs = append(upsertIDs, id)
		}
	}
	sort.Strings(deletes)
	sort.Strings(upsertIDs)
	docs, err := v.loadChangedDocsTx(ctx, tx, upsertIDs)
	if err != nil {
		return nil, err
	}
	_ = tx.Rollback()

	eng, err := core.OpenDiskClusterEngine(csrPath)
	if err != nil {
		return nil, err
	}
	if err := core.AttachClusterBuildAux(filepath.Join(filepath.Dir(csrPath), snapshotSlotAgg), eng); err == nil {
		// The sidecar is written by the kernel beside every published segment.
		// If it is missing or belongs to another generation, the engine closes
		// the base and materialises from the new index instead.
		eng.RetainBuildBase(true)
	}
	eng.ApplyDocs(deletes, docs)
	eng.MaterializeClusterState()
	if eng.Index == nil || eng.Index.N == 0 {
		_ = eng.Close()
		return nil, errors.New("localvault: incremental build produced an empty index")
	}
	return &incrementalBuildResult{eng: eng, docsRev: docsRev, changeSeq: changeSeq, purgeEpoch: purgeEpoch}, nil
}
