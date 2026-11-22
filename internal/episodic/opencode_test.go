// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package episodic

import (
	"database/sql"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

func openOpenCodeFixture(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "opencode.db"))
	if err != nil {
		t.Fatalf("open fixture: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	for _, ddl := range []string{
		`CREATE TABLE session_message (id TEXT PRIMARY KEY, session_id TEXT, type TEXT, seq INTEGER, time_created INTEGER, time_updated INTEGER, data TEXT)`,
	} {
		if _, err := db.Exec(ddl); err != nil {
			t.Fatalf("ddl: %v", err)
		}
	}
	insert := func(id, typ string, seq int, ts int64, payload string) {
		if _, err := db.Exec(`INSERT INTO session_message(id, session_id, type, seq, time_created, time_updated, data) VALUES (?, 's1', ?, ?, ?, ?, ?)`,
			id, typ, seq, ts, ts, payload); err != nil {
			t.Fatalf("insert %s: %v", id, err)
		}
	}
	// turn 1: completed answer, closed by idle succeeded
	insert("u1", "user", 1, 1793102400000, `{"text":"把缓存按 docID 失效。"}`)
	insert("a1", "assistant", 2, 1793102401000, `{"content":[{"type":"reasoning","text":"internal"},{"type":"text","text":"<system-reminder>banner</system-reminder>结论：缓存按 docID 失效。"}],"time":{"created":1793102401000,"completed":1793102402000}}`)
	insert("i1", "idle", 3, 1793102402000, `{"outcome":"succeeded"}`)
	// turn 2: tool step carries narration and no final answer -> unimportable
	insert("u2", "user", 4, 1793102403000, `{"text":"继续排查。"}`)
	insert("a2", "assistant", 5, 1793102404000, `{"content":[{"type":"text","text":"先确认断言。"},{"type":"tool","text":"{}"}],"time":{"created":1793102404000,"completed":1793102405000}}`)
	insert("i2", "idle", 6, 1793102405000, `{"outcome":"succeeded"}`)
	// turn 3: no idle event (migrated store); the next user message ends it
	insert("u3", "user", 7, 1793102406000, `{"text":"没有空闲事件的轮次。"}`)
	insert("a3", "assistant", 8, 1793102407000, `{"content":[{"type":"text","text":"这是最终回答。"}],"time":{"created":1793102407000,"completed":1793102408000}}`)
	insert("u4", "user", 9, 1793102409000, `{"text":"下一轮。"}`)
	insert("a4", "assistant", 10, 1793102410000, `{"content":[{"type":"text","text":"进行中的回答。"}],"time":{"created":1793102410000}}`)
	return db
}

func TestOpenCodeSessionTurns(t *testing.T) {
	db := openOpenCodeFixture(t)
	turns, err := OpenCodeSessionTurns(db, "s1")
	if err != nil {
		t.Fatalf("turns: %v", err)
	}
	if len(turns) != 4 {
		t.Fatalf("turns = %d, want 4: %+v", len(turns), turns)
	}
	first := turns[0]
	if first.UserMsgID != "u1" || !first.Completed || len(first.Segments) != 2 {
		t.Fatalf("first turn = %+v", first)
	}
	if first.Segments[1].Text != "结论：缓存按 docID 失效。" || first.Segments[1].Role != RoleAssistant {
		t.Fatalf("cleaned assistant segment = %+v", first.Segments[1])
	}
	second := turns[1]
	if second.UserMsgID != "u2" || second.Completed {
		t.Fatalf("tool-narration turn = %+v", second)
	}
	if len(second.Segments) != 1 {
		t.Fatalf("tool step must not contribute speech: %+v", second.Segments)
	}
	third := turns[2]
	if third.UserMsgID != "u3" || !third.Completed || len(third.Segments) != 2 {
		t.Fatalf("no-idle completed turn = %+v", third)
	}
	fourth := turns[3]
	if fourth.UserMsgID != "u4" || fourth.Completed {
		t.Fatalf("open tail turn = %+v", fourth)
	}
}

func TestOpenCodeTurnSegmentsUpToMatchesSessionReader(t *testing.T) {
	db := openOpenCodeFixture(t)
	turns, err := OpenCodeSessionTurns(db, "s1")
	if err != nil {
		t.Fatalf("turns: %v", err)
	}
	segments, completed, err := OpenCodeTurnSegmentsUpTo(db, "s1", "u3", 0)
	if err != nil {
		t.Fatalf("turn segments: %v", err)
	}
	if !completed || len(segments) != len(turns[2].Segments) {
		t.Fatalf("resolver turn = %+v completed=%v", segments, completed)
	}
	for i := range segments {
		if segments[i] != turns[2].Segments[i] {
			t.Fatalf("segment %d drifted: %+v vs %+v", i, segments[i], turns[2].Segments[i])
		}
	}
	if _, _, err := OpenCodeTurnSegmentsUpTo(db, "s1", "missing", 0); err == nil {
		t.Fatal("missing user message must fail closed")
	}
}
