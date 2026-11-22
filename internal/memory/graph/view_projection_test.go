// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package graph

import (
	"fmt"
	"testing"

	core "github.com/rocareer/rolling-memory/internal/core"
)

// The display projection is a read-only view over the full aggregates: a
// background word family and any phrase touching it disappear from the
// rendered selection, while wordAgg/phraseAgg keep every entry.
func TestDisplayProjectionFiltersBackgroundAndKeepsAggregates(t *testing.T) {
	const (
		hHigh  uint64 = 0x1001
		hRare1 uint64 = 0x1002
		hRare2 uint64 = 0x1003
	)
	docs := make([]core.Doc, 0, 8)
	for i := 0; i < 8; i++ {
		docs = append(docs, core.Doc{
			UnitID: fmt.Sprintf("u%d", i),
			Terms: map[uint64]core.Term{
				hHigh: {Hash: hHigh, Role: core.RolePrimary, Bits: 100},
			},
		})
	}
	docs[0].Terms[hRare1] = core.Term{Hash: hRare1, Role: core.RolePrimary, Bits: 100}
	docs[1].Terms[hRare2] = core.Term{Hash: hRare2, Role: core.RolePrimary, Bits: 100}

	idx := core.BuildIndex(docs)
	if idx.TermDF(hHigh) != 8 || idx.TermDF(hRare1) != 1 || idx.TermDF(hRare2) != 1 {
		t.Fatalf("unexpected df: high=%d rare1=%d rare2=%d", idx.TermDF(hHigh), idx.TermDF(hRare1), idx.TermDF(hRare2))
	}
	snap := &Snapshot{engine: core.NewEngine(idx)}
	state := &viewState{
		snap: snap,
		labels: map[uint64]string{
			hHigh:  "的",
			hRare1: "熵",
			hRare2: "记忆",
		},
		termMass: map[uint64]float64{},
		wordAgg: map[uint64]*wordAgg{
			hHigh:  {group: hHigh, weight: 100, df: 8},
			hRare1: {group: hRare1, weight: 10, df: 1},
			hRare2: {group: hRare2, weight: 9, df: 1},
		},
		phraseAgg: map[phraseKey]*phraseAgg{
			{a: hHigh, b: hRare1}:  {a: hHigh, b: hRare1, binding: 50},
			{a: hRare1, b: hRare2}: {a: hRare1, b: hRare2, binding: 5},
		},
		wordBudget:   8,
		phraseBudget: 8,
		backgroundDF: idx.HighDFThreshold(),
	}
	if state.displayableWord(hHigh) {
		t.Fatal("background word must not be displayable")
	}
	if !state.displayableWord(hRare1) || !state.displayableWord(hRare2) {
		t.Fatal("rare words must stay displayable")
	}
	wordsBefore, phrasesBefore := len(state.wordAgg), len(state.phraseAgg)

	phrases := state.selectPhrases()
	if len(phrases) != 1 || phrases[0].a != hRare1 || phrases[0].b != hRare2 {
		t.Fatalf("filtered phrases = %#v, want only rare pair", phrases)
	}
	words := state.selectWords(phrases)
	if len(words) != 2 || words[0] != hRare1 || words[1] != hRare2 {
		t.Fatalf("filtered words = %#v, want rare pair only", words)
	}
	if state.filteredWords != 1 || state.filteredPhrases != 1 {
		t.Fatalf("filtered counts = (%d,%d), want (1,1)", state.filteredWords, state.filteredPhrases)
	}
	if len(state.wordAgg) != wordsBefore || len(state.phraseAgg) != phrasesBefore {
		t.Fatal("display projection mutated the aggregates")
	}

	if got := state.topClusterWords(map[uint64]float64{hHigh: 100, hRare1: 10}, 8); len(got) != 1 || got[0].GroupHex != hex16(hRare1) {
		t.Fatalf("cluster words = %#v, want only rare word", got)
	}
	if got := state.topClusterPhrases(map[phraseKey]float64{{a: hHigh, b: hRare1}: 10}, 8); len(got) != 0 {
		t.Fatalf("cluster phrases = %#v, want background phrase filtered", got)
	}
}

func TestDisplayProjectionKeepsUnlabeledGroupsOut(t *testing.T) {
	const h uint64 = 0x2001
	idx := core.BuildIndex([]core.Doc{{
		UnitID: "u0",
		Terms:  map[uint64]core.Term{h: {Hash: h, Role: core.RolePrimary, Bits: 100}},
	}})
	state := &viewState{
		snap:         &Snapshot{engine: core.NewEngine(idx)},
		labels:       map[uint64]string{},
		wordAgg:      map[uint64]*wordAgg{h: {group: h, weight: 1, df: 1}},
		backgroundDF: idx.HighDFThreshold(),
	}
	if state.displayableWord(h) {
		t.Fatal("a group without a primary surface must not be displayable")
	}
	if got := state.selectWords(nil); len(got) != 0 {
		t.Fatalf("selected = %#v, want none", got)
	}
}
