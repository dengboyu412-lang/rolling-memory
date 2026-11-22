// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package cli

import (
	"testing"
	"time"
)

func TestSnapshotBuildDue(t *testing.T) {
	now := time.Unix(1700000000, 0)
	cases := []struct {
		name     string
		ago      time.Duration
		failures int32
		interval time.Duration
		ready    bool
		known    bool
		gap      int64
		want     bool
	}{
		{name: "first generation", ready: false, known: true, gap: 0, want: true},
		{name: "nothing pending", ago: time.Hour, ready: true, known: true, gap: 0, want: false},
		{name: "inside cadence", ago: 30 * time.Second, ready: true, known: true, gap: 5, want: false},
		{name: "cadence elapsed", ago: 61 * time.Second, ready: true, known: true, gap: 5, want: true},
		{name: "large backlog still waits", ago: 30 * time.Second, ready: true, known: true, gap: 10000, want: false},
		{name: "failure backoff", ago: 30 * time.Second, failures: 1, ready: true, known: true, gap: 5, want: false},
		{name: "failure retry", ago: 3 * time.Minute, failures: 1, ready: true, known: true, gap: 5, want: true},
		{name: "unknown marker forces a build", ago: time.Hour, ready: true, known: false, gap: 0, want: true},
		{name: "zero interval uses the default", ago: 30 * time.Second, interval: 0, ready: true, known: true, gap: 5, want: false},
		{name: "zero interval default elapsed", ago: 61 * time.Second, interval: 0, ready: true, known: true, gap: 5, want: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var lastNS int64
			if tc.ago > 0 {
				lastNS = now.Add(-tc.ago).UnixNano()
			}
			if got := snapshotBuildDue(now, lastNS, tc.failures, tc.interval, tc.ready, tc.known, tc.gap); got != tc.want {
				t.Fatalf("snapshotBuildDue = %v, want %v", got, tc.want)
			}
		})
	}
}
