// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package facade_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/rocareer/rolling-memory/internal/memory/facade"
)

// A burst of writes can push the change-log gap past the fixed threshold while
// the builder is healthy and catching up. A recent successful build must read
// as backlog; a stale or failed builder must still fail the check.
func TestDoctorChangeGapUsesBuilderFreshness(t *testing.T) {
	memory := newVaultOnlyMemoryWithScope(t, "mem:change-gap")
	content := "Unit one records the Glasgow hotel cost in GBP."
	if _, err := memory.Remember(facade.RememberRequest{
		Content:    &content,
		Session:    "s1",
		OccurredAt: 1700000000,
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	connection := memory.Store.DB()
	if _, err := connection.Exec(`UPDATE rm_revision SET change_seq = 2000 WHERE id = 1`); err != nil {
		t.Fatal(err)
	}
	if _, err := connection.Exec(`INSERT INTO rm_meta(k, v) VALUES ('snapshot_built_change_seq', '0') ON CONFLICT(k) DO UPDATE SET v = excluded.v`); err != nil {
		t.Fatal(err)
	}

	check := func() bool {
		t.Helper()
		report, err := memory.Doctor()
		if err != nil {
			t.Fatalf("doctor: %v", err)
		}
		checks, _ := report["checks"].([]any)
		for _, entry := range checks {
			entry, ok := entry.(map[string]any)
			if ok && entry["name"] == "snapshot_change_gap" {
				passed, _ := entry["ok"].(bool)
				return passed
			}
		}
		t.Fatal("doctor reported no snapshot_change_gap check")
		return false
	}

	if check() {
		t.Fatal("a large backlog without a builder must fail the gap check")
	}

	writeStatus := func(at int64, failures int, blocked bool) {
		t.Helper()
		raw := fmt.Sprintf(`{"at":%d,"failures":%d,"blocked":%t,"last_error":""}`, at, failures, blocked)
		if _, err := connection.Exec(`INSERT INTO rm_meta(k, v) VALUES ('snapshot_build_status', ?) ON CONFLICT(k) DO UPDATE SET v = excluded.v`, raw); err != nil {
			t.Fatal(err)
		}
	}

	writeStatus(time.Now().Unix(), 0, false)
	if !check() {
		t.Fatal("a recent successful build must read the gap as backlog")
	}

	writeStatus(time.Now().Add(-time.Hour).Unix(), 0, false)
	if check() {
		t.Fatal("a stale builder must still fail the gap check")
	}

	writeStatus(time.Now().Unix(), 2, false)
	if check() {
		t.Fatal("a failed builder must still fail the gap check")
	}
}
