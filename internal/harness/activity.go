// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package harness

import (
	"os"
	"path/filepath"
	"strings"
)

// SupportedIDs returns every adapter id in registry order: the install targets
// plus the extended registry (OpenCode, Cursor, Windsurf, Cline, WorkBuddy,
// Doubao). Callers that need a stable inventory use this instead of repeating
// a host list.
func SupportedIDs(home string) []string {
	all := append(BuildHarnesses(home, ""), BuildExtendedHarnesses(home)...)
	seen := make(map[string]bool, len(all))
	ids := make([]string, 0, len(all))
	for _, h := range all {
		if h.ID == "" || seen[h.ID] {
			continue
		}
		seen[h.ID] = true
		ids = append(ids, h.ID)
	}
	return ids
}

// OpenCodeDataHome resolves the directory holding OpenCode's SQLite session
// store. OpenCode follows XDG_DATA_HOME when it is set.
func OpenCodeDataHome(home string) string {
	if xdg := strings.TrimSpace(os.Getenv("XDG_DATA_HOME")); xdg != "" {
		return filepath.Join(xdg, "opencode")
	}
	if strings.TrimSpace(home) == "" {
		if resolved, err := os.UserHomeDir(); err == nil {
			home = resolved
		}
	}
	return filepath.Join(home, ".local", "share", "opencode")
}

// SessionStorePath returns the host's SQLite session store when it keeps one.
// Empty means the host stores transcripts as files or exposes no local store.
func SessionStorePath(home, harnessID string) string {
	if strings.TrimSpace(home) == "" {
		if resolved, err := os.UserHomeDir(); err == nil {
			home = resolved
		}
	}
	switch harnessID {
	case "zcode":
		return filepath.Join(home, ".zcode", "cli", "db", "db.sqlite")
	case "opencode":
		return filepath.Join(OpenCodeDataHome(home), "opencode.db")
	}
	return ""
}

// ActivityPaths returns the host-owned files and directories whose mtime or
// size changes as a session progresses. Change detectors (console freshness,
// auto-watch, ROI refresh) consume this one list so no single host is the only
// source of "something happened" evidence.
func ActivityPaths(home string) []string {
	if strings.TrimSpace(home) == "" {
		if resolved, err := os.UserHomeDir(); err == nil {
			home = resolved
		}
	}
	paths := make([]string, 0, 32)
	add := func(p string) {
		if strings.TrimSpace(p) != "" {
			paths = append(paths, p)
		}
	}
	addStore := func(id string) {
		if store := SessionStorePath(home, id); store != "" {
			add(store)
			add(store + "-wal")
		}
	}
	addStore("zcode")
	add(filepath.Join(home, ".zcode", "projects"))
	addStore("opencode")
	add(OpenCodeDataHome(home))
	add(filepath.Join(home, ".claude", "projects"))
	add(filepath.Join(home, ".codex", "sessions"))
	add(filepath.Join(home, ".codex", "archived_sessions"))
	add(filepath.Join(ResolveDSHHome(home), "sessions"))
	add(filepath.Join(home, ".codebuddy", "projects"))
	add(filepath.Join(home, ".workbuddy", "projects"))
	add(filepath.Join(home, ".cursor"))
	add(filepath.Join(home, ".windsurf"))
	add(filepath.Join(home, ".cline"))
	add(filepath.Join(home, ".config", "devin"))
	add(filepath.Join(home, "Doubao", "exports"))
	add(filepath.Join(home, ".doubao", "exports"))
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
