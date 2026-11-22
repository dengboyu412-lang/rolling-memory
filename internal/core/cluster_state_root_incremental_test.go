// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

// 本件钉住族根变化时的增量路径：新增文档带更小的族标签、删除唯一携带标签的文档、
// 删除仅含「标签不等于自身哈希」词项的文档，以及随机混合追加/删除/覆盖链，均须与
// 一次性全量构建逐位一致；族根变化不得再以整建回退替代增量扣减/并入。
package core

import (
	"encoding/hex"
	"math/rand"
	"testing"
)

type rootProbeTerm struct {
	hash  uint64
	group uint64
	slot  int
}

// rootProbeDoc 构造只含给定词项的单句文档（句内槽位由 slot 指定，族标签由 group 指定）。
func rootProbeDoc(id string, ts int64, terms ...rootProbeTerm) Doc {
	d := Doc{
		UnitID:     id,
		OccurredAt: ts,
		Terms:      make(map[uint64]Term, len(terms)),
		Counts:     make(map[uint64]int, len(terms)),
		Positions:  make(map[uint64][]TermPosition, len(terms)),
	}
	for _, rt := range terms {
		d.Terms[rt.hash] = Term{Hash: rt.hash, Bits: 40, Role: RolePrimary, ExtFlags: ExtFlagSeenPrimary, Group: rt.group, Slot: uint16(rt.slot)}
		d.Counts[rt.hash] = 1
		d.Positions[rt.hash] = []TermPosition{{Sentence: 0, Slot: rt.slot}}
	}
	return d
}

// withRootLabel 返回副本：把一个随机词项的族标签压到 [800,864)，使其族根下降（族根变化输入）。
func withRootLabel(d Doc, rng *rand.Rand) Doc {
	keys := make([]uint64, 0, len(d.Terms))
	for h := range d.Terms {
		keys = append(keys, h)
	}
	sortUint64Asc(keys)
	out := d
	out.Terms = make(map[uint64]Term, len(d.Terms))
	for h, t := range d.Terms {
		out.Terms[h] = t
	}
	h := keys[rng.Intn(len(keys))]
	t := out.Terms[h]
	t.Group = uint64(800 + rng.Intn(64))
	out.Terms[h] = t
	return out
}

func aggCanonicalDigest(agg *clusterAggregates) string {
	if agg == nil {
		return "nil"
	}
	var d digestWriter
	d.tag("agg-v1")
	keys := make([]uint64, 0, len(agg.fams))
	for k := range agg.fams {
		keys = append(keys, k)
	}
	sortUint64Asc(keys)
	d.u64(uint64(len(keys)))
	for _, k := range keys {
		fa := agg.fams[k]
		d.u64(k)
		d.i32(fa.df)
		d.i32(fa.multiDocCount)
		d.f64(fa.weight)
		d.i64s(fa.clusterKey)
		d.i32s(fa.clusterCnt)
		d.u64s(fa.partnerKey)
		d.i32s(fa.partnerCnt)
	}
	if agg.termPartners != nil {
		tkeys := make([]uint64, 0, len(agg.termPartners))
		for k := range agg.termPartners {
			tkeys = append(tkeys, k)
		}
		sortUint64Asc(tkeys)
		d.tag("termPartners")
		d.u64(uint64(len(tkeys)))
		for _, k := range tkeys {
			d.u64(k)
			d.u64s(agg.termPartners[k])
			d.i32s(agg.termPartnerCnt[k])
		}
	}
	return hex.EncodeToString(d.h[:])
}

func assertRootIncrementalMatchesFull(t *testing.T, label string, full, inc *Engine, q map[uint64]Term) {
	t.Helper()
	fs, is := full.ensureClusterState(), inc.ensureClusterState()
	if a, b := clusterStateCanonicalDigest(fs), clusterStateCanonicalDigest(is); a != b {
		t.Fatalf("%s: cluster digest mismatch:\n full=%s\n inc =%s", label, a, b)
	}
	if a, b := aggCanonicalDigest(fs.agg), aggCanonicalDigest(is.agg); a != b {
		t.Fatalf("%s: family aggregate digest mismatch:\n full=%s\n inc =%s", label, a, b)
	}
	if a, b := ComputeCorpusDigest(full.Index), ComputeCorpusDigest(inc.Index); a != b {
		t.Fatalf("%s: corpus digest mismatch: full=%x inc=%x", label, a, b)
	}
	assertSameQuery(t, full, inc, q)
}

// TestIncrementalRootChangeAppendMatchesFullRebuild：新增文档把已有词项 1005 的族标签压到 900，
// 族根由 1005 变为 900；增量路径必须按受影响文档重键，而不是整建。
func TestIncrementalRootChangeAppendMatchesFullRebuild(t *testing.T) {
	docs := appendProbeDocs(240)
	extra := rootProbeDoc("probe-rooted-append", 1700000000+240*3600,
		rootProbeTerm{hash: 1005, group: 900, slot: 0},
		rootProbeTerm{hash: 1011, group: 1011, slot: 1},
	)
	want := append(append([]Doc(nil), docs...), extra)
	full := NewEngine(BuildIndex(want))
	full.ensureClusterState()

	inc := NewEngine(BuildIndex(docs))
	inc.ensureClusterState()
	inc.AppendDocs([]Doc{extra})
	assertIncrementalFired(t, inc, true)
	assertRootIncrementalMatchesFull(t, "root-change append", full, inc, digestProbeQuery(want))
}

// TestIncrementalRootChangeDeleteMatchesFullRebuild：唯一携带低标签的文档被删除后，
// 词项 h0 的族根回升；幸存的含 h0 文档须按旧根扣减、新根并入。
func TestIncrementalRootChangeDeleteMatchesFullRebuild(t *testing.T) {
	docs := appendProbeDocs(240)
	const carrierAt = 119
	carrier := docs[carrierAt]
	h0 := uint64(0)
	for h := range carrier.Terms {
		if h0 == 0 || h < h0 {
			h0 = h
		}
	}
	labelled := rootProbeDoc(carrier.UnitID, carrier.OccurredAt)
	for h, term := range carrier.Terms {
		term.Group = h
		if h == h0 {
			term.Group = 900
		}
		labelled.Terms[h] = term
		labelled.Counts[h] = carrier.Counts[h]
		labelled.Positions[h] = carrier.Positions[h]
	}
	withCarrier := append([]Doc(nil), docs...)
	withCarrier[carrierAt] = labelled

	kept := make([]Doc, 0, len(docs)-1)
	for i, d := range docs {
		if i != carrierAt {
			kept = append(kept, d)
		}
	}
	full := NewEngine(BuildIndex(kept))
	full.ensureClusterState()

	inc := NewEngine(BuildIndex(withCarrier))
	inc.ensureClusterState()
	if removed := inc.DeleteDocs(carrier.UnitID); removed != 1 {
		t.Fatalf("DeleteDocs removed %d, want 1", removed)
	}
	assertIncrementalFired(t, inc, true)
	assertRootIncrementalMatchesFull(t, "root-change delete", full, inc, digestProbeQuery(kept))
}

// TestIncrementalDeleteVanishedRootMatchesFullRebuild：删除唯一含词项 5000 的文档（其族标签
// 4999 ≠ 自身哈希）。词项整体消失时，扣减必须沿用旧族根，否则伙伴对残留。
func TestIncrementalDeleteVanishedRootMatchesFullRebuild(t *testing.T) {
	base := appendProbeDocs(200)
	victim := rootProbeDoc("probe-vanish", 1700000000+200*3600,
		rootProbeTerm{hash: 5000, group: 4999, slot: 0},
		rootProbeTerm{hash: 1001, group: 1001, slot: 1},
	)
	full := NewEngine(BuildIndex(base))
	full.ensureClusterState()

	inc := NewEngine(BuildIndex(append(append([]Doc(nil), base...), victim)))
	inc.ensureClusterState()
	if removed := inc.DeleteDocs(victim.UnitID); removed != 1 {
		t.Fatalf("DeleteDocs removed %d, want 1", removed)
	}
	assertIncrementalFired(t, inc, true)
	assertRootIncrementalMatchesFull(t, "vanished-root delete", full, inc, digestProbeQuery(base))
}

// TestIncrementalRootChurnChainMatchesFullRebuild：随机混合追加、删除与覆盖，且部分文档
// 携带会移动族根的标签；每一步增量结果都必须与一次性全量构建逐位一致。
func TestIncrementalRootChurnChainMatchesFullRebuild(t *testing.T) {
	docs := appendProbeDocs(560)
	rng := rand.New(rand.NewSource(23))
	cur := append([]Doc(nil), docs[:200]...)
	eng := NewEngine(BuildIndex(cur))
	eng.ensureClusterState()
	next := 200
	fired := 0
	for step := 0; step < 18; step++ {
		switch rng.Intn(3) {
		case 0:
			n := 1 + rng.Intn(4)
			batch := make([]Doc, 0, n)
			for k := 0; k < n; k++ {
				d := docs[next+k]
				if rng.Intn(2) == 0 {
					d = withRootLabel(d, rng)
				}
				batch = append(batch, d)
			}
			next += n
			cur = append(cur, batch...)
			eng.AppendDocs(batch)
		case 1:
			if len(cur) > 40 {
				n := 1 + rng.Intn(2)
				drop := make(map[string]struct{}, n)
				victims := make([]string, 0, n)
				for k := 0; k < n; k++ {
					id := cur[rng.Intn(len(cur))].UnitID
					if _, dup := drop[id]; dup {
						continue
					}
					drop[id] = struct{}{}
					victims = append(victims, id)
				}
				kept := make([]Doc, 0, len(cur))
				for _, d := range cur {
					if _, gone := drop[d.UnitID]; !gone {
						kept = append(kept, d)
					}
				}
				cur = kept
				eng.DeleteDocs(victims...)
			}
		case 2:
			at := rng.Intn(len(cur))
			replaced := withRootLabel(cur[at], rng)
			cur[at] = replaced
			eng.AppendDocs([]Doc{replaced})
		}
		if st := eng.ensureClusterState(); st != nil && st.agg != nil && st.agg.extended {
			fired++
		}
		full := NewEngine(BuildIndex(cur))
		full.ensureClusterState()
		assertRootIncrementalMatchesFull(t, "root churn step", full, eng, digestProbeQuery(cur))
	}
	t.Logf("root churn chain: incremental steps %d/18", fired)
	if fired == 0 {
		t.Fatal("root churn chain never took the incremental path")
	}
}
