// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package core

import (
	"math"
	"testing"
)

// TestProximityEntityDispatchCoversAllUnits pins the dispatch pool at the
// empty, single, two-unit and multi-worker boundaries: every candidate index
// is visited exactly once, and the nil state/group arguments are never
// dereferenced when every candidate is ineligible (fewer than two matches or
// non-positive core lex).
func TestProximityEntityDispatchCoversAllUnits(t *testing.T) {
	old := proximityWorkersOverride
	proximityWorkersOverride = 4
	defer func() {
		proximityWorkersOverride = old
		proximityUnitEvaluations.Store(0)
	}()
	for _, n := range []int{0, 1, 2, 17} {
		proximityUnitEvaluations.Store(0)
		units := make([]int32, n)
		for i := range units {
			units[i] = int32(i)
		}
		dispatchProximityEntity(units, make([]float64, n), make([]rmCandidate, n), nil, nil, nil, 0, 0)
		if got := proximityUnitEvaluations.Load(); got != int64(n) {
			t.Fatalf("n=%d visited %d units, want %d", n, got, n)
		}
	}
}

// TestProximityEntityDispatchBitIdenticalSerialVsParallel runs the same fully
// evaluable candidates through the pinned serial and parallel pools and
// requires bitwise-equal entity energy per unit; under -race it also exercises
// the concurrent read-only access to the shared cluster state.
func TestProximityEntityDispatchBitIdenticalSerialVsParallel(t *testing.T) {
	const n = 64
	units := make([]int32, n)
	for i := range units {
		units[i] = int32(i)
	}
	st := &rmClusterState{
		docRowPtr:      make([]uint32, n+1),
		docEntriesFlat: make([]rmDocEntry, 2*n),
	}
	for u := 0; u <= n; u++ {
		st.docRowPtr[u] = uint32(2 * u)
	}
	for i := range st.docEntriesFlat {
		st.docEntriesFlat[i] = rmDocEntry{posOff: uint32(2 * i), posLen: 2}
	}
	st.posPool = make([]uint32, 4*n)
	for i := range st.posPool {
		// One sentence, ascending slots: every entry's position slice stays
		// sorted, and adjacent entries always share the sentence, so every
		// pair scan lands on a finite closest-slot distance.
		st.posPool[i] = packTermPosition(0, i+1)
	}
	groups := map[uint64]float64{11: 0.7, 22: 1.3}
	build := func() []rmCandidate {
		candidates := make([]rmCandidate, n)
		for u := range candidates {
			candidates[u] = rmCandidate{
				unit: int32(u),
				matches: []rmFeatureMatch{
					{group: 11, mass: 0.9, entry: int32(u % 2)},
					{group: 22, mass: 0.5, entry: int32((u + 1) % 2)},
				},
			}
		}
		return candidates
	}
	coreLex := make([]float64, n)
	gates := make([]float64, n)
	for i := range coreLex {
		coreLex[i] = 1.5
		gates[i] = 0.8
	}
	run := func(workers int) []float64 {
		candidates := build()
		proximityWorkersOverride = workers
		defer func() { proximityWorkersOverride = 0 }()
		dispatchProximityEntity(units, coreLex, candidates, gates, st, groups, 2.0, 0.6)
		out := make([]float64, n)
		for u := range out {
			out[u] = candidates[u].entity
		}
		return out
	}
	serial := run(1)
	parallel := run(14)
	for u := range serial {
		if serial[u] != parallel[u] {
			t.Fatalf("unit %d: serial=%v parallel=%v", u, serial[u], parallel[u])
		}
	}
	if math.IsNaN(serial[0]) || serial[0] == 0 {
		t.Fatalf("evaluations produced no finite entity energy: %v", serial[0])
	}
}
