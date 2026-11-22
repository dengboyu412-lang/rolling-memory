// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package harness

import "testing"

// TestTurnScannerCapability locks which hosts can contribute turn content. A
// host that is merely inventoried must not claim turn coverage, and the OpenCode
// SQLite reader must stay registered.
func TestTurnScannerCapability(t *testing.T) {
	for _, id := range []string{"zcode", "claude-code", "codex", "dsh", "codebuddy", "workbuddy", "opencode", "doubao"} {
		if !TurnsScannable(id) {
			t.Errorf("%s must be turn-scannable", id)
		}
	}
	for _, id := range []string{"cursor", "windsurf", "cline", "unknown"} {
		if TurnsScannable(id) {
			t.Errorf("%s has no turn reader and must not claim one", id)
		}
	}
	if !SessionsScannable("opencode") {
		t.Error("OpenCode session inventory must stay scannable")
	}
}
