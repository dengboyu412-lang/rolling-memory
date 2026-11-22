// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package harness

import (
	"os"
	"path/filepath"
	"strings"
)

// Transcript layout is the read face of the same host adapters BuildHarnesses
// describes: which file holds a host's local conversation history. It lives next
// to the install targets so a host has exactly one path authority, and the four
// consumers (session inventory, change fingerprint, auto-watch diff, episodic
// importer) can never drift apart.

// ResolveDSHHome returns the DeepSeek Harness home directory: $DSH_HOME when it
// is set, otherwise <home>/.dsh. Both the install targets inside BuildHarnesses
// and the transcript scan root below resolve through here, so an install can
// never land in a directory the scanner does not read.
func ResolveDSHHome(home string) string {
	if strings.TrimSpace(home) == "" {
		if resolved, err := os.UserHomeDir(); err == nil {
			home = resolved
		}
	}
	if env := strings.TrimSpace(os.Getenv("DSH_HOME")); env != "" {
		return env
	}
	return filepath.Join(home, ".dsh")
}

// TranscriptGlobs returns the glob patterns holding harnessID's local
// conversation transcripts under home. An unknown or unsupported host yields
// nil: callers report "no transcript scanner" instead of inventing a path.
//
// Layouts, all verified against the host's own store:
//   - zcode:        <home>/.zcode/projects/<project>/sessions/<session>.jsonl
//   - claude-code:  <home>/.claude/projects/<project>/<session>.jsonl
//     and <home>/.claude/projects/<project>/sessions/<session>.jsonl
//   - codex:        <home>/.codex/sessions/<y>/<m>/<d>/<session>.jsonl[.zst],
//     a flat <home>/.codex/sessions/<session>.jsonl[.zst], and archived_sessions
//   - dsh:          <DSH_HOME>/sessions/--<cwd>--/<session-id>/session[.v<N>].jsonl.zstd
//     The directory name carries the session id and the file name carries the
//     DSH transcript format version, so both path levels vary.
//   - codebuddy / workbuddy: <home>/.<host>/projects/<project>/<session>.jsonl
//     and <home>/.<host>/projects/<project>/sessions/<session>.jsonl
func TranscriptGlobs(home, harnessID string) []string {
	switch harnessID {
	case "zcode":
		return []string{filepath.Join(home, ".zcode", "projects", "*", "sessions", "*.jsonl")}
	case "claude-code":
		return []string{
			filepath.Join(home, ".claude", "projects", "*", "*.jsonl"),
			filepath.Join(home, ".claude", "projects", "*", "sessions", "*.jsonl"),
		}
	case "codex":
		return []string{
			filepath.Join(home, ".codex", "sessions", "*", "*", "*", "*.jsonl"),
			filepath.Join(home, ".codex", "sessions", "*.jsonl"),
			filepath.Join(home, ".codex", "archived_sessions", "*.jsonl"),
			filepath.Join(home, ".codex", "sessions", "*", "*", "*", "*.jsonl.zst"),
			filepath.Join(home, ".codex", "sessions", "*.jsonl.zst"),
			filepath.Join(home, ".codex", "archived_sessions", "*.jsonl.zst"),
		}
	case "dsh":
		return []string{
			filepath.Join(ResolveDSHHome(home), "sessions", "*", "*", "session.v*.jsonl.zstd"),
			filepath.Join(ResolveDSHHome(home), "sessions", "*", "*", "session.jsonl.zstd"),
		}
	case "codebuddy":
		return []string{
			filepath.Join(home, ".codebuddy", "projects", "*", "*.jsonl"),
			filepath.Join(home, ".codebuddy", "projects", "*", "sessions", "*.jsonl"),
		}
	case "workbuddy":
		return []string{
			filepath.Join(home, ".workbuddy", "projects", "*", "*.jsonl"),
			filepath.Join(home, ".workbuddy", "projects", "*", "sessions", "*.jsonl"),
		}
	case "doubao":
		// Doubao keeps live chat history behind its authenticated service and
		// encrypted SDK store; the supported local path is a user-assisted JSONL
		// export (one conversation per file) placed in the app workspace.
		return []string{
			filepath.Join(home, "Doubao", "exports", "*.jsonl"),
			filepath.Join(home, ".doubao", "exports", "*.jsonl"),
		}
	}
	return nil
}

// TranscriptSessionID derives the host's own session identifier from one
// transcript path. Hosts that name the file after the session use the file stem
// (compression suffixes are not part of the id); DeepSeek Harness names
// its transcripts session[.v<N>].jsonl.zstd and carries the session id in the
// enclosing directory.
func TranscriptSessionID(harnessID, path string) string {
	if harnessID == "dsh" {
		return filepath.Base(filepath.Dir(path))
	}
	stem := strings.TrimSuffix(filepath.Base(path), ".zstd")
	stem = strings.TrimSuffix(stem, ".zst")
	return strings.TrimSuffix(stem, filepath.Ext(stem))
}
