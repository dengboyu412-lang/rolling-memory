// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package facade_test

import (
	"fmt"
	"testing"

	"github.com/rocareer/rolling-memory/internal/memory/facade"
)

// The revision counters are the snapshot freshness identity, so doctor must
// report a drift instead of leaving a writer bug to be discovered as a cache
// that never matches.
func TestDoctorChecksCorpusCounters(t *testing.T) {
	memory := newVaultOnlyMemoryWithScope(t, "mem:corpus-counters")
	content := "Unit one records the Glasgow hotel cost in GBP."
	if _, err := memory.Remember(facade.RememberRequest{
		Content:    &content,
		Session:    "s1",
		OccurredAt: 1700000000,
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	check := func() (bool, bool) {
		report, err := memory.Doctor()
		if err != nil {
			t.Fatalf("doctor: %v", err)
		}
		checks, _ := report["checks"].([]any)
		for _, entry := range checks {
			entry, ok := entry.(map[string]any)
			if !ok || entry["name"] != "corpus_counters" {
				continue
			}
			passed, _ := entry["ok"].(bool)
			return true, passed
		}
		return false, false
	}

	found, passed := check()
	if !found {
		t.Fatal("doctor reported no corpus_counters check")
	}
	if !passed {
		t.Fatal("fresh counters failed the consistency check")
	}

	if _, err := memory.Store.DB().Exec(`UPDATE rm_revision SET docs_cnt = 99 WHERE id = 1`); err != nil {
		t.Fatal(err)
	}
	found, passed = check()
	if !found || passed {
		t.Fatalf("drifted counters were not reported: found=%v passed=%v", found, passed)
	}

	report, err := memory.Doctor()
	if err != nil {
		t.Fatal(err)
	}
	warnings, _ := report["warnings"].([]any)
	flagged := false
	for _, warning := range warnings {
		if fmt.Sprint(warning) == "corpus counters drifted from the live row counts; the snapshot signature and delta gate will keep missing" {
			flagged = true
		}
	}
	if !flagged {
		t.Fatalf("doctor warnings = %v, want the counter drift warning", warnings)
	}
}
