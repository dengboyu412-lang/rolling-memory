// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package cli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// seedDSHStore writes DeepSeek Harness transcript paths under a private DSH_HOME.
func seedDSHStore(t *testing.T, home string, mtime time.Time) string {
	t.Helper()
	dshHome := filepath.Join(home, "dsh-home")
	t.Setenv("DSH_HOME", dshHome)
	sessions := filepath.Join(dshHome, "sessions")
	for _, dir := range []string{"session-aaa", "bbb"} {
		path := filepath.Join(sessions, "--demo-repo--", dir, "session.v4.jsonl.zstd")
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("mkdir dsh session: %v", err)
		}
		if err := os.WriteFile(path, []byte("{}\n"), 0o644); err != nil {
			t.Fatalf("write dsh transcript: %v", err)
		}
		if err := os.Chtimes(path, mtime, mtime); err != nil {
			t.Fatalf("chtimes dsh transcript: %v", err)
		}
	}
	return sessions
}

func TestDiscoverEpisodicScanSourcesIncludesDSH(t *testing.T) {
	home := t.TempDir()
	seedDSHStore(t, home, time.Now())
	claudePath := filepath.Join(home, ".claude", "projects", "demo", "s1.jsonl")
	if err := os.MkdirAll(filepath.Dir(claudePath), 0o755); err != nil {
		t.Fatalf("mkdir claude: %v", err)
	}
	if err := os.WriteFile(claudePath, []byte("{}\n"), 0o644); err != nil {
		t.Fatalf("write claude transcript: %v", err)
	}

	dshSources, err := discoverEpisodicScanSources(context.Background(), home, "dsh")
	if err != nil {
		t.Fatalf("discover dsh: %v", err)
	}
	if len(dshSources) != 2 {
		t.Fatalf("dsh scan sources = %d, want 2: %+v", len(dshSources), dshSources)
	}
	claudeSources, err := discoverEpisodicScanSources(context.Background(), home, "claude-code")
	if err != nil {
		t.Fatalf("discover claude: %v", err)
	}
	if len(claudeSources) != 1 {
		t.Fatalf("claude-code scan sources = %d, want 1", len(claudeSources))
	}
}

func TestHarnessTranscriptPatternsCoverImportableHosts(t *testing.T) {
	home := t.TempDir()
	t.Setenv("DSH_HOME", filepath.Join(home, "dsh-home"))
	patterns := harnessTranscriptPatterns(home)
	if len(patterns) == 0 {
		t.Fatal("transcript patterns are empty")
	}
	joined := strings.Join(patterns, "\n")
	for _, want := range []string{".claude", ".codex", ".zcode", "dsh-home", "Doubao/exports"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("importable host missing from the change detector: %s", want)
		}
	}
}

func TestLatestHarnessTranscriptMTimeIncludesDSH(t *testing.T) {
	home := t.TempDir()
	base := time.Unix(1700000000, 0)
	sessions := seedDSHStore(t, home, base)
	_ = os.Chtimes(sessions, base, base)
	before := latestHarnessTranscriptMTime(home)
	target := filepath.Join(sessions, "--demo-repo--", "session-aaa", "session.v4.jsonl.zstd")
	newer := base.Add(30 * time.Minute)
	if err := os.Chtimes(target, newer, newer); err != nil {
		t.Fatalf("chtimes dsh transcript: %v", err)
	}
	if after := latestHarnessTranscriptMTime(home); after <= before || after != newer.Unix() {
		t.Fatalf("latest transcript mtime did not track DSH: %d -> %d", before, after)
	}
}
