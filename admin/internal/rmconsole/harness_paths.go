// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package rollingmemory

import (
	"os"
	"path/filepath"
	"strings"
)

// hostTranscriptPatterns returns the JSONL globs of every supported file-based
// host. Adoption audits consume these transcripts plus the vault's own episodic
// rows, so no host-private SQLite schema is required.
func hostTranscriptPatterns(home string) []string {
	dshHome := strings.TrimSpace(os.Getenv("DSH_HOME"))
	if dshHome == "" {
		dshHome = filepath.Join(home, ".dsh")
	}
	return []string{
		filepath.Join(home, ".claude", "projects", "*", "*.jsonl"),
		filepath.Join(home, ".claude", "projects", "*", "sessions", "*.jsonl"),
		filepath.Join(home, ".workbuddy", "projects", "*", "*.jsonl"),
		filepath.Join(home, ".workbuddy", "projects", "*", "sessions", "*.jsonl"),
		filepath.Join(home, ".codebuddy", "projects", "*", "*.jsonl"),
		filepath.Join(home, ".codebuddy", "projects", "*", "sessions", "*.jsonl"),
		filepath.Join(home, ".codex", "sessions", "*", "*", "*", "*.jsonl"),
		filepath.Join(home, ".codex", "sessions", "*.jsonl"),
		filepath.Join(home, ".codex", "archived_sessions", "*.jsonl"),
		filepath.Join(dshHome, "sessions", "*.jsonl"),
		filepath.Join(home, ".cursor", "projects", "*", "*.jsonl"),
		filepath.Join(home, ".cursor", "sessions", "*.jsonl"),
		filepath.Join(home, ".windsurf", "sessions", "*.jsonl"),
		filepath.Join(home, ".cline", "sessions", "*.jsonl"),
		filepath.Join(home, ".zcode", "projects", "*", "sessions", "*.jsonl"),
	}
}

// harnessActivityPaths returns every host-owned store or transcript root whose
// mtime/size changes as a session progresses. The console uses this single list
// for freshness fingerprints, so no host is the only source of "something
// happened" evidence. Hosts that keep a SQLite session store contribute the
// database and its WAL; file-based hosts contribute their transcript roots.
func harnessActivityPaths(home string) []string {
	if strings.TrimSpace(home) == "" {
		if resolved, err := os.UserHomeDir(); err == nil {
			home = resolved
		}
	}
	opencodeData := filepath.Join(home, ".local", "share", "opencode")
	if xdg := strings.TrimSpace(os.Getenv("XDG_DATA_HOME")); xdg != "" {
		opencodeData = filepath.Join(xdg, "opencode")
	}
	dshHome := strings.TrimSpace(os.Getenv("DSH_HOME"))
	if dshHome == "" {
		dshHome = filepath.Join(home, ".dsh")
	}
	paths := []string{
		filepath.Join(home, ".zcode", "cli", "db", "db.sqlite"),
		filepath.Join(home, ".zcode", "cli", "db", "db.sqlite-wal"),
		filepath.Join(home, ".zcode", "projects"),
		filepath.Join(opencodeData, "opencode.db"),
		filepath.Join(opencodeData, "opencode.db-wal"),
		opencodeData,
		filepath.Join(home, ".claude", "projects"),
		filepath.Join(home, ".codex", "sessions"),
		filepath.Join(home, ".codex", "archived_sessions"),
		filepath.Join(dshHome, "sessions"),
		filepath.Join(home, ".codebuddy", "projects"),
		filepath.Join(home, ".workbuddy", "projects"),
		filepath.Join(home, ".cursor"),
		filepath.Join(home, ".windsurf"),
		filepath.Join(home, ".cline"),
		filepath.Join(home, ".config", "devin"),
		filepath.Join(home, "Doubao", "exports"),
		filepath.Join(home, ".doubao", "exports"),
	}
	seen := make(map[string]bool, len(paths))
	out := paths[:0]
	for _, p := range paths {
		clean := filepath.Clean(p)
		if clean == "" || seen[clean] {
			continue
		}
		seen[clean] = true
		out = append(out, clean)
	}
	return out
}
