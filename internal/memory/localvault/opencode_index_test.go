// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package localvault

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	"github.com/rocareer/rolling-memory/internal/episodicunit"
	_ "modernc.org/sqlite"
)

// TestOpenCodePointerReflectsSourceEdits locks the live resolver for the
// OpenCode pointer family: the same store the host writes is read on demand,
// and a changed source invalidates the old content identity.
func TestOpenCodePointerReflectsSourceEdits(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dataHome := filepath.Join(home, "xdg-data")
	t.Setenv("XDG_DATA_HOME", dataHome)
	path := filepath.Join(dataHome, "opencode", "opencode.db")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	source, err := sql.Open("sqlite", vaultTestDSN(path))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = source.Close() })
	for _, statement := range []string{
		`CREATE TABLE session_message (id TEXT PRIMARY KEY, session_id TEXT, type TEXT, seq INTEGER, time_created INTEGER, time_updated INTEGER, data TEXT)`,
		`INSERT INTO session_message VALUES ('m1', 's1', 'user', 1, 1, 1, '{"text":"alpha"}')`,
		`INSERT INTO session_message VALUES ('m2', 's1', 'assistant', 2, 2, 2, '{"content":[{"type":"text","text":"reply"}],"time":{"created":2,"completed":3}}')`,
		`INSERT INTO session_message VALUES ('m3', 's1', 'idle', 3, 3, 3, '{"outcome":"succeeded"}')`,
	} {
		if _, err := source.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	db := migrationTestDB(t)
	v, err := NewVault(db, []byte("opencode-source-edit"))
	if err != nil {
		t.Fatal(err)
	}
	turn := "@ref:opencode:s1:m1"
	ref := turn + "#0"
	oldText := "[user] alpha"
	const scope = "mem:source-edit"
	oldID := episodicunit.DocID(scope, oldText)
	pkt, _ := v.PackBlindPacket(oldID, oldText, 1)
	if err := v.SaveLocalEpisodicPointerIndexed(oldID, 1, ref, turn, oldText, pkt, IngestResponse{}); err != nil {
		t.Fatal(err)
	}
	if got, err := v.LoadVerifiedEpisodicPointer(oldID, ref); err != nil || got != oldText {
		t.Fatalf("initial source: %q %v", got, err)
	}
	if got, err := ResolveEpisodicPointerLive(turn + "#1"); err != nil || got != "[assistant] reply" {
		t.Fatalf("assistant segment: %q %v", got, err)
	}
	if _, err := source.Exec(`UPDATE session_message SET data = '{"text":"bravo"}' WHERE id = 'm1'`); err != nil {
		t.Fatal(err)
	}
	newText := "[user] bravo"
	if got, err := ResolveEpisodicPointerLive(ref); err != nil || got != newText {
		t.Fatalf("updated source: %q %v, want %q", got, err, newText)
	}
	if got, err := v.LoadVerifiedEpisodicPointer(oldID, ref); err == nil || got != "" {
		t.Fatalf("changed source accepted under old identity: %q %v", got, err)
	}
	newID := episodicunit.DocID(scope, newText)
	pkt, _ = v.PackBlindPacket(newID, newText, 2)
	if err := v.SaveLocalEpisodicPointerIndexed(newID, 2, ref, turn, newText, pkt, IngestResponse{}); err != nil {
		t.Fatalf("import updated source: %v", err)
	}
	if got, err := v.LoadVerifiedEpisodicPointer(newID, ref); err != nil || got != newText {
		t.Fatalf("verified updated source: %q %v", got, err)
	}
}
