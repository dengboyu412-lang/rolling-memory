// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestParseSnapshotInterval(t *testing.T) {
	cases := map[string]struct {
		want time.Duration
		ok   bool
	}{
		"":     {DefaultSnapshotInterval, true},
		"60s":  {60 * time.Second, true},
		"5m":   {5 * time.Minute, true},
		"1h":   {time.Hour, true},
		"90":   {90 * time.Second, true},
		"10s":  {10 * time.Second, true},
		"9s":   {0, false},
		"2h":   {0, false},
		"soon": {0, false},
	}
	for raw, tc := range cases {
		got, ok := ParseSnapshotInterval(raw)
		if ok != tc.ok || (ok && got != tc.want) {
			t.Fatalf("ParseSnapshotInterval(%q) = (%v, %v), want (%v, %v)", raw, got, ok, tc.want, tc.ok)
		}
	}
}

func TestLoadSnapshotInterval(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	db := filepath.Join(dir, "memory.db")
	write := func(body string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	load := func(env map[string]string) (*Config, error) {
		p, d := path, db
		return Load(LoadOptions{Path: &p, DB: &d, Env: env})
	}

	write("")
	cfg, err := load(map[string]string{})
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.Daemon.EffectiveSnapshotInterval(); got != DefaultSnapshotInterval {
		t.Fatalf("default interval = %v, want %v", got, DefaultSnapshotInterval)
	}

	write("[daemon]\nsnapshot_interval = \"30s\"\n")
	cfg, err = load(map[string]string{})
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.Daemon.EffectiveSnapshotInterval(); got != 30*time.Second {
		t.Fatalf("file interval = %v, want 30s", got)
	}

	write("")
	cfg, err = load(map[string]string{"ROLLING_MEMORY_SNAPSHOT_INTERVAL": "5m"})
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.Daemon.EffectiveSnapshotInterval(); got != 5*time.Minute {
		t.Fatalf("env interval = %v, want 5m", got)
	}

	if _, err := load(map[string]string{"ROLLING_MEMORY_SNAPSHOT_INTERVAL": "1s"}); err == nil {
		t.Fatal("expected a below-floor interval to be rejected")
	}
}
