// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

// 本件钉住 ApplyDocs 的顺序等价性与单次结算：混合批（删除+追加）必须与
// DeleteDocs 后 AppendDocs 在索引、簇状态与查询三层逐位一致，且 Phase-3
// 外挂只同步一次（生产 drain 不再付两遍全量重建）。
package core

import (
	"path/filepath"
	"testing"
)

// syncCountingPlugin 记录 Phase-3 同步次数与末次同步看到的单元数。
type syncCountingPlugin struct {
	syncs int
	lastN int
}

func (p *syncCountingPlugin) SyncRMEngine(eng *Engine) {
	p.syncs++
	if eng != nil && eng.Index != nil {
		p.lastN = eng.Index.N
	}
}

func (p *syncCountingPlugin) AugmentRMStage2(*Index, map[uint64]Term, map[uint64]float64, float64, []Stage3Candidate) []Stage3Delta {
	return nil
}

func applyDocsProbeBatch(docs []Doc) (drops []string, upserts []Doc, want []Doc) {
	const victim = "probe-00087"
	const readded = "probe-00121"
	batch := appendProbeDocs(len(docs) + 10)[len(docs):]
	readd := batch[0]
	readd.UnitID = readded
	readd.OccurredAt += 7
	upserts = append(batch, readd)
	drops = []string{victim, readded, "probe-99999"}

	kept := make([]Doc, 0, len(docs)+len(upserts))
	for _, d := range docs {
		if d.UnitID == victim || d.UnitID == readded {
			continue
		}
		kept = append(kept, d)
	}
	want = append(kept, upserts...)
	return drops, upserts, want
}

func TestApplyDocsMatchesSequentialAndFullRebuild(t *testing.T) {
	docs := appendProbeDocs(300)
	drops, upserts, wantDocs := applyDocsProbeBatch(docs)

	full := NewEngine(BuildIndex(wantDocs))
	full.ensureClusterState()
	wantDigest := clusterStateCanonicalDigest(full.ensureClusterState())
	wantCorpus := ComputeCorpusDigest(full.Index)
	q := digestProbeQuery(wantDocs)

	seq := NewEngine(BuildIndex(docs))
	seq.ensureClusterState()
	if removed := seq.DeleteDocs(drops...); removed != 2 {
		t.Fatalf("sequential DeleteDocs removed %d, want 2 resident IDs", removed)
	}
	seq.AppendDocs(upserts)

	comb := NewEngine(BuildIndex(docs))
	comb.ensureClusterState()
	if removed := comb.ApplyDocs(drops, upserts); removed != 2 {
		t.Fatalf("ApplyDocs removed %d, want 2 resident IDs", removed)
	}

	if got := ComputeCorpusDigest(seq.Index); got != wantCorpus {
		t.Fatalf("sequential corpus digest mismatch: full=%x seq=%x", wantCorpus, got)
	}
	if got := clusterStateCanonicalDigest(seq.ensureClusterState()); got != wantDigest {
		t.Fatalf("sequential cluster digest mismatch:\n full=%s\n seq =%s", wantDigest, got)
	}
	if got := clusterStateCanonicalDigest(comb.ensureClusterState()); got != wantDigest {
		t.Fatalf("ApplyDocs cluster digest mismatch:\n full=%s\n comb=%s", wantDigest, got)
	}
	if got := ComputeCorpusDigest(comb.Index); got != wantCorpus {
		t.Fatalf("ApplyDocs corpus digest mismatch: full=%x comb=%x", wantCorpus, got)
	}
	assertSameQuery(t, seq, comb, q)
	assertSameQuery(t, full, comb, q)

	t.Run("disk", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "apply_docs.zcsr")
		if err := WriteDiskClusterCSR(path, NewEngine(BuildIndex(docs))); err != nil {
			t.Fatalf("WriteDiskClusterCSR: %v", err)
		}
		disk, err := OpenDiskClusterEngine(path)
		if err != nil {
			t.Fatalf("OpenDiskClusterEngine: %v", err)
		}
		if removed := disk.ApplyDocs(drops, upserts); removed != 2 {
			t.Fatalf("disk ApplyDocs removed %d, want 2", removed)
		}
		if got := clusterStateCanonicalDigest(disk.ensureClusterState()); got != wantDigest {
			t.Fatalf("disk ApplyDocs cluster digest mismatch:\n full=%s\n disk=%s", wantDigest, got)
		}
		if got := ComputeCorpusDigest(disk.Index); got != wantCorpus {
			t.Fatalf("disk ApplyDocs corpus digest mismatch: full=%x disk=%x", wantCorpus, got)
		}
		assertSameQuery(t, full, disk, q)
	})
}

func TestApplyDocsSyncsStage3Once(t *testing.T) {
	docs := appendProbeDocs(120)
	drops, upserts, _ := applyDocsProbeBatch(docs)

	seqPlugin := &syncCountingPlugin{}
	seq := NewEngine(BuildIndex(docs))
	seq.SetStage3Plugin(seqPlugin, true)
	seq.ensureClusterState()
	seq.DeleteDocs(drops...)
	seq.AppendDocs(upserts[:1])

	combPlugin := &syncCountingPlugin{}
	comb := NewEngine(BuildIndex(docs))
	comb.SetStage3Plugin(combPlugin, true)
	comb.ensureClusterState()
	comb.ApplyDocs(drops, upserts[:1])

	if seqPlugin.syncs != 2 {
		t.Fatalf("sequential syncs = %d, want 2", seqPlugin.syncs)
	}
	if combPlugin.syncs != 1 {
		t.Fatalf("ApplyDocs syncs = %d, want 1", combPlugin.syncs)
	}
	if combPlugin.lastN != comb.Index.N || seqPlugin.lastN != comb.Index.N {
		t.Fatalf("stage3 last N = seq %d / comb %d, engine %d", seqPlugin.lastN, combPlugin.lastN, comb.Index.N)
	}
}

// TestAppendDocsPreservesOverlayDocs pins the overlay fallback: the CSR snapshot
// only covers the base index, so an append while a delta overlay is mounted must
// keep the overlay documents instead of rebuilding from the base alone.
func TestAppendDocsPreservesOverlayDocs(t *testing.T) {
	docs := appendProbeDocs(120)
	base, overlay := docs[:100], docs[100:]

	eng := NewEngine(BuildIndex(base))
	eng.ensureClusterState()
	eng.SetDeltaSnapshot(overlay, nil)
	extra := appendProbeDocs(121)[120]
	eng.AppendDocs([]Doc{extra})

	full := NewEngine(BuildIndex(append(append([]Doc{}, docs...), extra)))
	full.ensureClusterState()
	if got, want := clusterStateCanonicalDigest(eng.ensureClusterState()), clusterStateCanonicalDigest(full.ensureClusterState()); got != want {
		t.Fatalf("overlay append digest mismatch:\n want=%s\n got =%s", want, got)
	}
	assertSameQuery(t, full, eng, digestProbeQuery(append(append([]Doc{}, docs...), extra)))
}
