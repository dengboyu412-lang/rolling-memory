// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package harness

// SessionsScannable reports whether this build can discover the host's local
// conversation sessions (the count shown on the panel).
func SessionsScannable(harnessID string) bool {
	switch harnessID {
	case "zcode", "claude-code", "codex", "dsh", "codebuddy", "workbuddy", "opencode", "doubao":
		return true
	}
	return false
}

// TurnsScannable reports whether the transcript reader can also extract turn
// content. A host whose transcript is compressed or uses an unsupported schema
// counts sessions but reports zero turns; the panel labels that as "not
// counted" rather than "no history". OpenCode exposes a session inventory only,
// so it stays out of the turn list.
func TurnsScannable(harnessID string) bool {
	switch harnessID {
	case "zcode", "claude-code", "codex", "dsh", "codebuddy", "workbuddy", "doubao":
		return true
	}
	return false
}
