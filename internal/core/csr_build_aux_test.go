// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

// 本件钉住「已发布代可续写」：从 .zcsr + 族聚合旁车重开的引擎，在应用删除/覆盖/追加后
// 必须与一次性全量构建逐位一致，且连续多代发布仍保持可续写。
package core

import (
	"fmt"
	"path/filepath"
	"testing"
)

func TestReopenedGenerationMatchesFullRebuild(t *testing.T) {
	for _, termMode := range []bool{false, true} {
		name := "root"
		if termMode {
			name = "term"
		}
		t.Run(name, func(t *testing.T) {
			SetTermPartnerStats(termMode)
			defer SetTermPartnerStats(false)

			docs := appendProbeDocs(340)
			cur := append([]Doc(nil), docs[:240]...)
			eng := NewEngine(BuildIndex(cur))
			eng.MaterializeClusterState()

			for step := 0; step < 4; step++ {
				batch := append([]Doc(nil), docs[240+step*20:260+step*20]...)
				victimIdx := 10 + step*7
				updateIdx := 30 + step*11
				victim := cur[victimIdx].UnitID
				updated := mutateProbeDoc(cur[updateIdx], step+1)
				upserts := append([]Doc{updated}, batch...)

				want := make([]Doc, 0, len(cur)+len(batch))
				for i, d := range cur {
					switch i {
					case victimIdx:
						continue
					case updateIdx:
						want = append(want, updated)
						continue
					}
					want = append(want, d)
				}
				want = append(want, batch...)

				dir := t.TempDir()
				csrPath := filepath.Join(dir, "cluster.zcsr")
				auxPath := filepath.Join(dir, "cluster.agg")
				if err := WriteDiskClusterCSR(csrPath, eng); err != nil {
					t.Fatalf("step %d: write csr: %v", step, err)
				}
				if err := WriteClusterBuildAux(auxPath, eng); err != nil {
					t.Fatalf("step %d: write aux: %v", step, err)
				}
				_ = eng.Close()

				reopened, err := OpenDiskClusterEngine(csrPath)
				if err != nil {
					t.Fatalf("step %d: open csr: %v", step, err)
				}
				if err := AttachClusterBuildAux(auxPath, reopened); err != nil {
					t.Fatalf("step %d: attach aux: %v", step, err)
				}
				reopened.RetainBuildBase(true)
				reopened.ApplyDocs([]string{victim}, upserts)
				reopened.MaterializeClusterState()
				if st := reopened.ensureClusterState(); st == nil || st.agg == nil || !st.agg.extended {
					t.Fatalf("step %d: expected an incremental extension, got state=%p", step, st)
				}

				full := NewEngine(BuildIndex(want))
				full.MaterializeClusterState()
				assertRootIncrementalMatchesFull(t, fmt.Sprintf("%s step %d", name, step), full, reopened, digestProbeQuery(want))
				cur = want
				eng = reopened
			}
		})
	}
}

func TestReopenedGenerationWithoutAuxFallsBackToFullRebuild(t *testing.T) {
	SetTermPartnerStats(false)
	defer SetTermPartnerStats(false)

	docs := appendProbeDocs(260)
	base, delta := docs[:200], append([]Doc(nil), docs[200:]...)
	victim := base[40].UnitID
	want := make([]Doc, 0, len(docs)-1)
	for _, d := range base {
		if d.UnitID != victim {
			want = append(want, d)
		}
	}
	want = append(want, delta...)

	pub := NewEngine(BuildIndex(base))
	pub.MaterializeClusterState()
	csrPath := filepath.Join(t.TempDir(), "cluster.zcsr")
	if err := WriteDiskClusterCSR(csrPath, pub); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenDiskClusterEngine(csrPath)
	if err != nil {
		t.Fatal(err)
	}
	reopened.RetainBuildBase(true)
	reopened.ApplyDocs([]string{victim}, delta)
	reopened.MaterializeClusterState()

	full := NewEngine(BuildIndex(want))
	full.MaterializeClusterState()
	if st := reopened.ensureClusterState(); st == nil || st.agg == nil || st.agg.extended {
		t.Fatalf("expected a full rebuild without aux, got extended=%v", st != nil && st.agg != nil && st.agg.extended)
	}
	assertRootIncrementalMatchesFull(t, "no-aux", full, reopened, digestProbeQuery(want))
}

func TestClusterBuildAuxRejectsDifferentGeneration(t *testing.T) {
	SetTermPartnerStats(false)
	defer SetTermPartnerStats(false)

	small := appendProbeDocs(64)
	large := appendProbeDocs(65)
	smallEng := NewEngine(BuildIndex(small))
	smallEng.MaterializeClusterState()
	dir := t.TempDir()
	auxPath := filepath.Join(dir, "cluster.agg")
	if err := WriteClusterBuildAux(auxPath, smallEng); err != nil {
		t.Fatal(err)
	}
	largeEng := NewEngine(BuildIndex(large))
	largeEng.MaterializeClusterState()
	csrPath := filepath.Join(dir, "cluster.zcsr")
	if err := WriteDiskClusterCSR(csrPath, largeEng); err != nil {
		t.Fatal(err)
	}
	opened, err := OpenDiskClusterEngine(csrPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := AttachClusterBuildAux(auxPath, opened); err == nil {
		t.Fatal("expected a unit-count mismatch to reject the aux sidecar")
	}
}
