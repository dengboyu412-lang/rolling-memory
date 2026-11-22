// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package harness

import (
	"path/filepath"
	"testing"
)

func TestSupportedIDsCoverEveryAdapter(t *testing.T) {
	ids := make(map[string]bool)
	for _, id := range SupportedIDs(t.TempDir()) {
		ids[id] = true
	}
	for _, want := range []string{
		"zcode", "claude-code", "codex", "dsh", "codebuddy",
		"opencode", "cursor", "windsurf", "cline", "workbuddy", "doubao",
	} {
		if !ids[want] {
			t.Fatalf("adapter %s missing from SupportedIDs", want)
		}
	}
}

func TestActivityPathsIncludeEverySessionStore(t *testing.T) {
	home := t.TempDir()
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, "xdg"))
	paths := make(map[string]bool)
	for _, p := range ActivityPaths(home) {
		paths[p] = true
	}
	for _, want := range []string{
		filepath.Join(home, ".zcode", "cli", "db", "db.sqlite"),
		filepath.Join(home, "xdg", "opencode", "opencode.db"),
		filepath.Join(home, ".claude", "projects"),
		filepath.Join(home, ".codex", "sessions"),
	} {
		if !paths[want] {
			t.Fatalf("activity path %s missing", want)
		}
	}
}

func TestSessionStorePathOnlyForSQLiteHosts(t *testing.T) {
	home := t.TempDir()
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, "xdg"))
	if got := SessionStorePath(home, "zcode"); got != filepath.Join(home, ".zcode", "cli", "db", "db.sqlite") {
		t.Fatalf("zcode store = %s", got)
	}
	if got := SessionStorePath(home, "opencode"); got != filepath.Join(home, "xdg", "opencode", "opencode.db") {
		t.Fatalf("opencode store = %s", got)
	}
	for _, fileBased := range []string{"claude-code", "codex", "dsh", "cursor"} {
		if got := SessionStorePath(home, fileBased); got != "" {
			t.Fatalf("%s store = %s, want empty", fileBased, got)
		}
	}
}
