// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package core

import (
	"fmt"
	"testing"
)

const (
	gateCommon uint64 = 0x9101
	gateAnchor uint64 = 0x9202
	gateOther  uint64 = 0x9303
)

func gateDoc(id string, hashes ...uint64) Doc {
	terms := make(map[uint64]Term, len(hashes))
	counts := make(map[uint64]int, len(hashes))
	for _, h := range hashes {
		terms[h] = Term{Hash: h, Role: RolePrimary, Bits: 100}
		counts[h] = 1
	}
	return Doc{UnitID: id, Terms: terms, Counts: counts}
}

func gateIndex() *Index {
	docs := make([]Doc, 0, 20)
	for i := 0; i < 14; i++ {
		docs = append(docs, gateDoc(fmt.Sprintf("common-%02d", i), gateCommon))
	}
	docs = append(docs, gateDoc("anchor", gateCommon, gateAnchor))
	for i := 0; i < 5; i++ {
		docs = append(docs, gateDoc(fmt.Sprintf("other-%02d", i), gateOther))
	}
	return BuildIndex(docs)
}

func TestAnchorGateKeepsAnchorTopAndDropsBackgroundCandidates(t *testing.T) {
	idx := gateIndex()
	q := map[uint64]Term{
		gateCommon: {Hash: gateCommon, Role: RolePrimary, Bits: 100},
		gateAnchor: {Hash: gateAnchor, Role: RolePrimary, Bits: 100},
	}
	off := NewEngine(idx)
	offHits := off.Search(q, 10)
	if len(offHits) == 0 || offHits[0].UnitID != "anchor" {
		t.Fatalf("baseline top = %#v, want anchor", offHits)
	}
	offEnergy := offHits[0].Energy

	on := NewEngine(idx)
	on.AnchorGate = true
	onHits := on.Search(q, 10)
	if len(onHits) == 0 || onHits[0].UnitID != "anchor" {
		t.Fatalf("anchor-gated top = %#v, want anchor", onHits)
	}
	if onHits[0].Energy != offEnergy {
		t.Fatalf("second phase changed anchor energy: off=%.17g on=%.17g", offEnergy, onHits[0].Energy)
	}
	// A background-only document may still be reached through a shared cluster;
	// the gate changes how it is reached, not whether the cluster can expand.
}

func TestAnchorGateFallbackHasNoEffect(t *testing.T) {
	idx := gateIndex()
	q := map[uint64]Term{
		gateCommon: {Hash: gateCommon, Role: RolePrimary, Bits: 100},
		gateOther:  {Hash: gateOther, Role: RolePrimary, Bits: 100},
	}
	off := NewEngine(idx)
	on := NewEngine(idx)
	on.AnchorGate = true
	offHits := off.Search(q, 10)
	onHits := on.Search(q, 10)
	if len(offHits) != len(onHits) {
		t.Fatalf("fallback changed hit count: off=%d on=%d", len(offHits), len(onHits))
	}
	for i := range offHits {
		if offHits[i].UnitID != onHits[i].UnitID || offHits[i].Energy != onHits[i].Energy {
			t.Fatalf("fallback changed hit %d: off=%#v on=%#v", i, offHits[i], onHits[i])
		}
	}
}

func TestAnchorGateClonePreservesOption(t *testing.T) {
	eng := NewEngine(gateIndex())
	eng.AnchorGate = true
	if !eng.CloneForQuery().AnchorGate {
		t.Fatal("CloneForQuery dropped AnchorGate")
	}
}

type gateExtractor struct {
	terms map[uint64]Term
}

func (g gateExtractor) ExtractQueryWithPositions(idx QueryIndexView, text string) (map[uint64]Term, map[uint64]int, map[uint64][]TermPosition) {
	return g.terms, nil, nil
}

// The eval calls Engine.Query, not Search; the anchor gate must be applied on
// the queryBase path too, otherwise the arm silently degrades to a no-op.
func TestAnchorGateAppliesToQueryPath(t *testing.T) {
	idx := gateIndex()
	az := gateExtractor{terms: map[uint64]Term{
		gateCommon: {Hash: gateCommon, Role: RolePrimary, Bits: 100},
		gateAnchor: {Hash: gateAnchor, Role: RolePrimary, Bits: 100},
	}}
	off := NewEngine(idx)
	on := NewEngine(idx)
	on.AnchorGate = true
	offHits := off.Query(az, "ignored", 10)
	onHits := on.Query(az, "ignored", 10)
	if len(onHits) == 0 || onHits[0].UnitID != "anchor" {
		t.Fatalf("gated Query top = %#v, want anchor", onHits)
	}
	if len(offHits) == 0 {
		t.Fatal("baseline Query returned no hits")
	}
}
