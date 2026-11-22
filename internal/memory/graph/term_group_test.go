// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package graph

import (
	"context"
	"database/sql"
	"fmt"
	"testing"

	core "github.com/rocareer/rolling-memory/internal/core"
	"github.com/rocareer/rolling-memory/internal/memory/localvault"
	_ "modernc.org/sqlite"
)

// TestSnapshotTermGroupMatchesEngineCanonicalGroup locks the projection
// contract: the snapshot-level term→group map must resolve the same canonical
// word-family container the engine's cluster state reports. View labels then
// need no per-term energy-profile scan, which is O(phrase keys) per lookup.
func TestSnapshotTermGroupMatchesEngineCanonicalGroup(t *testing.T) {
	db, err := sql.Open("sqlite", "file:term_group_map?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	vault, err := localvault.NewVault(db, []byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatal(err)
	}
	texts := []string{
		"alpha project graph memory cluster phrase unit citation evidence time",
		"记忆 图谱 检索 证据 时间 单元 词族 短语 知识 关系",
		"database schema index query planner storage engine btree page",
		"图谱 载入 速度 缓存 快照 检索 证据 时间 单元 词族",
	}
	for i := 0; i < 160; i++ {
		text := fmt.Sprintf("%s session %d", texts[i%len(texts)], i)
		layer := "episodic"
		if i%11 == 0 {
			layer = "noetic"
		}
		if saveErr := vault.SaveLocalPlaintext(fmt.Sprintf("ep_%04d", i), 1700000000+int64(i)*60, text, nil, localvault.IngestResponse{Layer: layer}); saveErr != nil {
			t.Fatal(saveErr)
		}
	}
	builder := NewBuilder(vault)
	snap, err := builder.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if snap.engine == nil || snap.engine.Index == nil {
		t.Fatal("snapshot has no engine")
	}
	checked := 0
	seen := map[uint64]struct{}{}
	for u := 0; u < snap.engine.Index.N; u++ {
		snap.engine.ForEachDocEntry(u, func(entry core.DocEntryView) {
			if entry.ID == 0 {
				return
			}
			if _, dup := seen[entry.ID]; dup {
				return
			}
			seen[entry.ID] = struct{}{}
			checked++
			got := snap.TermGroup[entry.ID]
			if got == 0 {
				got = entry.ID
			}
			want := entry.ID
			if profile, ok := snap.engine.TermEnergyVector(entry.ID); ok && profile.GroupID != 0 {
				want = profile.GroupID
			}
			if got != want {
				t.Errorf("term %x group = %x, engine canonical group = %x", entry.ID, got, want)
			}
		})
	}
	if checked == 0 {
		t.Fatal("no indexed terms checked")
	}
}
