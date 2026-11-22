// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package cli

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/klauspost/compress/zstd"
	"github.com/rocareer/rolling-memory/internal/config"
	"github.com/rocareer/rolling-memory/internal/episodic"
	"github.com/rocareer/rolling-memory/internal/episodicunit"
	"github.com/rocareer/rolling-memory/internal/memory/facade"
	_ "modernc.org/sqlite"
)

func TestEpisodicSourceCompletionIdentityAndUnknownTime(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("DSH_HOME", filepath.Join(home, ".dsh"))
	t.Setenv("ROLLING_MEMORY_AUTO_SYNC", "true")
	t.Setenv("ROLLING_MEMORY_HARNESS", "claude-code")
	t.Setenv("ZCODE_PROJECT_DIR", "")
	t.Setenv("CLAUDE_PROJECT_DIR", "")
	repo := filepath.Join(home, "ActualRepo")
	if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(home, ".claude", "projects", "demo", "session.jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	body := fmt.Sprintf(`{"cwd":%q,"type":"user","message":{"role":"user","content":"pending question"}}`+"\n"+
		`{"type":"assistant","message":{"role":"assistant","content":"pending narration"}}`+"\n", repo)
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}
	memory, err := facade.New(&config.Config{DB: filepath.Join(home, "memory.db"), Scope: "mem:completion", Mode: "local"})
	if err != nil {
		t.Fatal(err)
	}
	defer memory.Close()
	count := func() int {
		t.Helper()
		var n int
		if err := memory.Store.DB().QueryRow(`SELECT COUNT(*) FROM rm_units WHERE layer = 'episodic'`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if err := runEpisodicSourceScanTick(context.Background(), memory, home); err != nil {
		t.Fatal(err)
	}
	if rep, err := executeReconcileSync(context.Background(), memory, "episodic", "claude-code", 100); err != nil || rep.EpisodicImportedFromHar != 0 {
		t.Fatalf("incomplete reconcile: report=%+v err=%v", rep, err)
	}
	if ran, _, _ := RunAutoWatchTick(context.Background(), memory, 0, 0); !ran || count() != 0 {
		t.Fatalf("incomplete watcher: ran=%v count=%d", ran, count())
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, err = fmt.Fprintln(f, `{"type":"assistant","message":{"role":"assistant","stop_reason":"end_turn","content":"final answer"}}`+"\n"+`{"type":"user","message":{"role":"user","content":"next pending question"}}`)
	if closeErr := f.Close(); err != nil || closeErr != nil {
		t.Fatalf("append: write=%v close=%v", err, closeErr)
	}
	rep, err := executeReconcileSync(context.Background(), memory, "episodic", "claude-code", 100)
	if err != nil || rep.EpisodicImportedFromHar != 3 || count() != 3 {
		t.Fatalf("completed reconcile: report=%+v count=%d err=%v", rep, count(), err)
	}
	for _, text := range []string{"[user] pending question", "[assistant] pending narration", "[assistant] final answer"} {
		var assistant, session, locus string
		var occurred int64
		id := episodicunit.DocID("mem:completion", text)
		if err := memory.Store.DB().QueryRow(`SELECT assistant_id, incarnation_id, locus, occurred_at FROM rm_units WHERE doc_id = ?`, id).Scan(&assistant, &session, &locus, &occurred); err != nil {
			t.Fatal(err)
		}
		if assistant != "claude-code" || session != "session" || locus != "ActualRepo" || occurred != 0 {
			t.Fatalf("%q identity=(%q,%q,%q) occurred=%d", text, assistant, session, locus, occurred)
		}
	}
	if err := runEpisodicSourceScanTick(context.Background(), memory, home); err != nil || count() != 3 {
		t.Fatalf("repeat source scan: count=%d err=%v", count(), err)
	}
}

func TestEpisodicSourceIdentityRepairAndIsolation(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("DSH_HOME", filepath.Join(home, ".dsh"))
	repo := filepath.Join(home, "SecondRepo")
	if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o700); err != nil {
		t.Fatal(err)
	}
	first := filepath.Join(home, ".claude", "projects", "a", "first.jsonl")
	second := filepath.Join(home, ".claude", "projects", "b", "second.jsonl")
	for _, path := range []string{first, second} {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		cwd := ""
		if path == second {
			cwd = repo
		}
		body := fmt.Sprintf(`{"cwd":%q,"type":"user","message":{"role":"user","content":"shared speech"}}`+"\n"+
			`{"type":"assistant","message":{"role":"assistant","stop_reason":"end_turn","content":"shared answer"}}`+"\n"+
			`{"type":"user","message":{"role":"user","content":"next"}}`+"\n", cwd)
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	memory, err := facade.New(&config.Config{DB: filepath.Join(home, "memory.db"), Scope: "mem:identity", Mode: "local"})
	if err != nil {
		t.Fatal(err)
	}
	defer memory.Close()
	if err := runEpisodicSourceScanTick(context.Background(), memory, home); err != nil {
		t.Fatal(err)
	}
	id := episodicunit.DocID("mem:identity", "[user] shared speech")
	var pointer, assistant, session, locus string
	if err := memory.Store.DB().QueryRow(`SELECT raw_text, assistant_id, incarnation_id, locus FROM rm_units WHERE doc_id = ?`, id).Scan(&pointer, &assistant, &session, &locus); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(pointer, first) || assistant != "claude-code" || session != "first" || locus != "" {
		t.Fatalf("cross-source identity: pointer=%q identity=(%q,%q,%q)", pointer, assistant, session, locus)
	}
	if _, err := memory.Store.DB().Exec(`UPDATE rm_units SET assistant_id='default', incarnation_id='', locus='wrong', occurred_at=42 WHERE doc_id=?`, id); err != nil {
		t.Fatal(err)
	}
	if err := runEpisodicSourceScanTick(context.Background(), memory, home); err != nil {
		t.Fatal(err)
	}
	var occurred int64
	if err := memory.Store.DB().QueryRow(`SELECT assistant_id, incarnation_id, locus, occurred_at FROM rm_units WHERE doc_id = ?`, id).Scan(&assistant, &session, &locus, &occurred); err != nil {
		t.Fatal(err)
	}
	if assistant != "claude-code" || session != "first" || locus != "wrong" || occurred != 0 {
		t.Fatalf("unknown locus must preserve prior value: identity=(%q,%q,%q) occurred=%d", assistant, session, locus, occurred)
	}
	plainDir := filepath.Join(home, "outside-repository")
	if err := os.MkdirAll(plainDir, 0o700); err != nil {
		t.Fatal(err)
	}
	body := fmt.Sprintf(`{"cwd":%q,"type":"user","message":{"role":"user","content":"shared speech"}}`+"\n"+
		`{"type":"assistant","message":{"role":"assistant","stop_reason":"end_turn","content":"shared answer"}}`+"\n", plainDir)
	if err := os.WriteFile(first, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := runEpisodicSourceScanTick(context.Background(), memory, home); err != nil {
		t.Fatal(err)
	}
	if err := memory.Store.DB().QueryRow(`SELECT locus FROM rm_units WHERE doc_id = ?`, id).Scan(&locus); err != nil || locus != "" {
		t.Fatalf("known non-repository locus=%q err=%v", locus, err)
	}
	var lineage int
	if err := memory.Store.DB().QueryRow(`SELECT COUNT(*) FROM rm_episodic_lineage WHERE doc_id = ?`, id).Scan(&lineage); err != nil || lineage != 2 {
		t.Fatalf("lineage=%d err=%v", lineage, err)
	}
}

func TestEpisodicSourceConcurrentHostsKeepIdentity(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("DSH_HOME", filepath.Join(home, ".dsh"))
	for _, fixture := range []struct{ hostDir, session, text, repo string }{
		{".claude", "claude-session", "claude unique", "ClaudeRepo"},
		{".workbuddy", "work-session", "work unique", "WorkRepo"},
	} {
		repo := filepath.Join(home, fixture.repo)
		if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o700); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(home, fixture.hostDir, "projects", "demo", fixture.session+".jsonl")
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		body := fmt.Sprintf(`{"cwd":%q,"type":"user","message":{"role":"user","content":%q}}`+"\n"+
			`{"type":"assistant","message":{"role":"assistant","stop_reason":"end_turn","content":"answer"}}`+"\n"+
			`{"type":"user","message":{"role":"user","content":"next"}}`+"\n", repo, fixture.text)
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	memory, err := facade.New(&config.Config{DB: filepath.Join(home, "memory.db"), Scope: "mem:concurrent", Mode: "local"})
	if err != nil {
		t.Fatal(err)
	}
	defer memory.Close()
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for _, host := range []string{"claude-code", "workbuddy"} {
		wg.Add(1)
		go func(host string) {
			defer wg.Done()
			_, err := executeReconcileSync(context.Background(), memory, "episodic", host, 100)
			errs <- err
		}(host)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	for _, want := range []struct{ text, host, session, locus string }{
		{"[user] claude unique", "claude-code", "claude-session", "ClaudeRepo"},
		{"[user] work unique", "workbuddy", "work-session", "WorkRepo"},
	} {
		var host, session, locus string
		if err := memory.Store.DB().QueryRow(`SELECT assistant_id, incarnation_id, locus FROM rm_units WHERE doc_id = ?`, episodicunit.DocID("mem:concurrent", want.text)).Scan(&host, &session, &locus); err != nil {
			t.Fatal(err)
		}
		if host != want.host || session != want.session || locus != want.locus {
			t.Fatalf("%q identity=(%q,%q,%q), want=(%q,%q,%q)", want.text, host, session, locus, want.host, want.session, want.locus)
		}
	}
}

func TestEpisodicZCodeCompletionEvidence(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, ".zcode", "cli", "db", "db.sqlite")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE message(id TEXT, session_id TEXT, sequence INTEGER, time_created INTEGER, data TEXT);
		CREATE TABLE part(message_id TEXT, sequence INTEGER, data TEXT);
		INSERT INTO message VALUES ('u','s',1,1577836800000,'{"role":"user"}'),('a','s',2,1767225600000,'{"role":"assistant"}');
		INSERT INTO part VALUES ('u',1,'{"type":"text","text":"question"}'),('a',1,'{"type":"text","text":"answer"}');`); err != nil {
		t.Fatal(err)
	}
	source := episodicScanSource{HarnessID: "zcode", Kind: "zcode", Path: path, SessionID: "s"}
	check := func(want bool, count int) {
		t.Helper()
		var turns []candidateEpisodicTurn
		_, err := streamEpisodicScanSource(context.Background(), source, "mem:zcode-completion", func(_ int, turn candidateEpisodicTurn) error {
			turns = append(turns, turn)
			return nil
		})
		if err != nil || len(turns) != count {
			t.Fatalf("completion=%v turns=%+v err=%v", want, turns, err)
		}
		for _, turn := range turns {
			if turn.Completed != want {
				t.Fatalf("completion=%v turns=%+v", want, turns)
			}
		}
	}
	check(false, 2)
	if _, err := db.Exec(`UPDATE message SET data='{"role":"assistant","finish":"tool-calls","time":{"completed":2}}' WHERE id='a'`); err != nil {
		t.Fatal(err)
	}
	check(false, 2)
	if _, err := db.Exec(`UPDATE message SET data='{"role":"assistant","finish":"stop","time":{"completed":2}}' WHERE id='a'; INSERT INTO part VALUES ('a',2,'{"type":"tool"}');`); err != nil {
		t.Fatal(err)
	}
	check(false, 1)
	if _, err := db.Exec(`DELETE FROM part WHERE message_id='a' AND sequence=2`); err != nil {
		t.Fatal(err)
	}
	check(true, 2)
	var timestamps []int64
	if _, err := streamEpisodicScanSource(context.Background(), source, "mem:zcode-completion", func(_ int, turn candidateEpisodicTurn) error {
		timestamps = append(timestamps, turn.OccurredAt)
		return nil
	}); err != nil || len(timestamps) != 2 || timestamps[0] != 1577836800 || timestamps[1] != 1767225600 {
		t.Fatalf("zcode segment timestamps=%v err=%v", timestamps, err)
	}
	if _, err := db.Exec(`INSERT INTO message VALUES ('hidden','s',3,1,'{"role":"user","semantics":{"uiVisibility":"hidden"}}'),('next','s',4,1,'{"role":"user"}');
		INSERT INTO part VALUES ('hidden',1,'{"type":"text","text":"hidden boundary"}'),('next',1,'{"type":"text","text":"next question"}');`); err != nil {
		t.Fatal(err)
	}
	var turns []candidateEpisodicTurn
	if _, err := streamEpisodicScanSource(context.Background(), source, "mem:zcode-completion", func(_ int, turn candidateEpisodicTurn) error {
		turns = append(turns, turn)
		return nil
	}); err != nil || len(turns) != 3 || !turns[0].Completed || !turns[1].Completed || turns[2].Completed {
		t.Fatalf("hidden boundary turns=%+v err=%v", turns, err)
	}
}

func TestEpisodicReconcileHostScopeAndDeletion(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("DSH_HOME", filepath.Join(home, ".dsh"))
	for _, fixture := range []struct{ dir, session, text string }{
		{".claude", "claude", "claude scope"},
		{".workbuddy", "work", "work scope"},
	} {
		path := filepath.Join(home, fixture.dir, "projects", "demo", fixture.session+".jsonl")
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		body := fmt.Sprintf(`{"type":"user","message":{"role":"user","content":%q}}`+"\n"+
			`{"type":"assistant","message":{"role":"assistant","stop_reason":"end_turn","content":%q}}`+"\n", fixture.text, fixture.text+" answer")
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	memory, err := facade.New(&config.Config{DB: filepath.Join(home, "memory.db"), Scope: "mem:scope", Mode: "local"})
	if err != nil {
		t.Fatal(err)
	}
	defer memory.Close()
	deleted := episodicunit.DocID("mem:scope", "[user] work scope")
	tc, err := memory.LocalVault()
	if err != nil {
		t.Fatal(err)
	}
	tc.Vault.RecordDeletedUnits("episodic", deleted)
	rep, err := executeReconcileSync(context.Background(), memory, "episodic", "claude-code", 100)
	if err != nil || rep.EpisodicImportedFromHar != 2 {
		t.Fatalf("single host report=%+v err=%v", rep, err)
	}
	var n int
	if err := memory.Store.DB().QueryRow(`SELECT COUNT(*) FROM rm_units WHERE doc_id = ?`, episodicunit.DocID("mem:scope", "[assistant] work scope answer")).Scan(&n); err != nil || n != 0 {
		t.Fatalf("single-host imported another host: count=%d err=%v", n, err)
	}
	rep, err = executeReconcileSync(context.Background(), memory, "episodic", "all", 100)
	if err != nil || rep.EpisodicImportedFromHar != 1 {
		t.Fatalf("all-host report=%+v err=%v", rep, err)
	}
	if err := memory.Store.DB().QueryRow(`SELECT COUNT(*) FROM rm_units WHERE doc_id = ?`, deleted).Scan(&n); err != nil || n != 0 {
		t.Fatalf("deleted unit resurrected: count=%d err=%v", n, err)
	}
	rep, err = executeReconcileSync(context.Background(), memory, "episodic", "all", 100)
	if err != nil || rep.EpisodicImportedFromHar != 0 {
		t.Fatalf("repeat report=%+v err=%v", rep, err)
	}
}

func TestEpisodicDSHCompressedDiscoveryAndHydration(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dshHome := filepath.Join(home, ".dsh")
	t.Setenv("DSH_HOME", dshHome)
	repo := filepath.Join(home, "DSHRepo")
	if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dshHome, "sessions", "workspace", "session-dsh", "session.v4.jsonl.zstd")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	body := fmt.Sprintf(`{"type":"session","version":4,"cwd":%q}`+"\n"+
		`{"type":"turn/start","data":{"turn":1}}`+"\n"+
		`{"type":"user/message","time":1794407222466,"data":{"role":"user","source":{"kind":"user"},"content":[{"type":"text","text":"DSH question"}]}}`+"\n"+
		`{"type":"assistant/message","time":1794407225890,"data":{"message":{"role":"assistant","content":[{"type":"text","text":"DSH answer"}]}}}`+"\n"+
		`{"type":"turn/end","data":{"turn":1,"reason":{"kind":"completed"}}}`+"\n", repo)
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	encoder, err := zstd.NewWriter(f)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := encoder.Write([]byte(body)); err != nil {
		t.Fatal(err)
	}
	if err := encoder.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	sources, err := discoverEpisodicScanSources(context.Background(), home, "dsh")
	if err != nil || len(sources) != 1 {
		t.Fatalf("dsh sources=%+v err=%v", sources, err)
	}
	memory, err := facade.New(&config.Config{DB: filepath.Join(home, "memory.db"), Scope: "mem:dsh", Mode: "local"})
	if err != nil {
		t.Fatal(err)
	}
	defer memory.Close()
	if err := runEpisodicSourceScanTick(context.Background(), memory, home); err != nil {
		t.Fatal(err)
	}
	tc, err := memory.LocalVault()
	if err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{"[user] DSH question", "[assistant] DSH answer"} {
		id := episodicunit.DocID("mem:dsh", text)
		var pointer, host, session, locus string
		if err := memory.Store.DB().QueryRow(`SELECT raw_text, assistant_id, incarnation_id, locus FROM rm_units WHERE doc_id = ?`, id).Scan(&pointer, &host, &session, &locus); err != nil {
			t.Fatal(err)
		}
		resolved, err := tc.Vault.LoadVerifiedEpisodicPointer(id, pointer)
		if err != nil || resolved != text || host != "dsh" || session != "session-dsh" || locus != "DSHRepo" {
			t.Fatalf("dsh resolved=%q identity=(%q,%q,%q) err=%v", resolved, host, session, locus, err)
		}
	}
}

func TestEpisodicSourceUnverifiedLegacyDoesNotStarveOtherSources(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_STATE_HOME", filepath.Join(home, ".local", "state"))
	t.Setenv("DSH_HOME", filepath.Join(home, ".dsh"))
	// This test calls the status collector in-process; it must not inherit the
	// developer's host markers or the real attachment ledger.
	t.Setenv("ROLLING_MEMORY_CONSOLE_STATUS", "1")
	t.Setenv("ROLLING_MEMORY_DISABLE_PROCESS_ANCESTRY", "1")
	t.Setenv("OPENCODE_SESSION_ID", "")
	t.Setenv("AI_AGENT", "")
	dir := filepath.Join(home, ".claude", "projects", "demo")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	first := filepath.Join(dir, "a.jsonl")
	body := `{"type":"user","message":{"role":"user","content":"legacy shared"}}` + "\n" +
		`{"type":"assistant","message":{"role":"assistant","stop_reason":"end_turn","content":"legacy answer"}}` + "\n" +
		`{"type":"user","message":{"role":"user","content":"same source healthy"}}` + "\n" +
		`{"type":"assistant","message":{"role":"assistant","stop_reason":"end_turn","content":"same source answer"}}` + "\n"
	if err := os.WriteFile(first, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	second := filepath.Join(dir, "b.jsonl")
	var later strings.Builder
	for i := 0; i < reconcileDefaultLimit+5; i++ {
		fmt.Fprintf(&later, `{"type":"user","message":{"role":"user","content":"healthy %d"}}`+"\n"+
			`{"type":"assistant","message":{"role":"assistant","stop_reason":"end_turn","content":"healthy answer %d"}}`+"\n", i, i)
	}
	if err := os.WriteFile(second, []byte(later.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	memory, err := facade.New(&config.Config{DB: filepath.Join(home, "memory.db"), Scope: "mem:legacy", Mode: "local"})
	if err != nil {
		t.Fatal(err)
	}
	defer memory.Close()
	if _, err := memory.LocalVault(); err != nil {
		t.Fatal(err)
	}
	id := episodicunit.DocID("mem:legacy", "[user] legacy shared")
	missing := filepath.Join(dir, "missing.jsonl")
	pointer := "@ref:jsonl:" + missing + ":0#0"
	if _, err := memory.Store.DB().Exec(`INSERT INTO rm_units(doc_id, layer, occurred_at, raw_text, sentences_json) VALUES (?, 'episodic', 0, ?, '[]')`, id, pointer); err != nil {
		t.Fatal(err)
	}
	for page := 0; page < 3; page++ {
		err := runEpisodicSourceScanTick(context.Background(), memory, home)
		if page == 0 && (err == nil || !strings.Contains(err.Error(), "source item unavailable")) {
			t.Fatalf("legacy diagnostic: %v", err)
		}
	}
	var n int
	sameSourceID := episodicunit.DocID("mem:legacy", "[user] same source healthy")
	if err := memory.Store.DB().QueryRow(`SELECT COUNT(*) FROM rm_units WHERE doc_id = ?`, sameSourceID).Scan(&n); err != nil || n != 1 {
		t.Fatalf("healthy item in failed source starved: count=%d err=%v", n, err)
	}
	var itemErrors int
	if err := memory.Store.DB().QueryRow(`SELECT CAST(v AS INTEGER) FROM rm_meta WHERE k = ?`, "episodic_source_scan_item_errors:mem:legacy").Scan(&itemErrors); err != nil || itemErrors == 0 {
		t.Fatalf("missing durable source diagnostic: errors=%d err=%v", itemErrors, err)
	}
	status, err := inspectEnvHealthAndSync(context.Background(), memory, "")
	if err != nil || status.Layers["episodic"].AlignmentKnown || status.Layers["episodic"].Aligned {
		t.Fatalf("source error reported aligned: status=%+v err=%v", status.Layers["episodic"], err)
	}
	lastID := episodicunit.DocID("mem:legacy", fmt.Sprintf("[assistant] healthy answer %d", reconcileDefaultLimit+4))
	if err := memory.Store.DB().QueryRow(`SELECT COUNT(*) FROM rm_units WHERE doc_id = ?`, lastID).Scan(&n); err != nil || n != 1 {
		t.Fatalf("healthy source starved: count=%d err=%v", n, err)
	}
	if err := os.WriteFile(missing, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	for page := 0; page < 8; page++ {
		if err := runEpisodicSourceScanTick(context.Background(), memory, home); err != nil {
			t.Fatalf("recovered scan page %d: %v", page, err)
		}
	}
	tc, err := memory.LocalVault()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tc.Vault.LoadVerifiedEpisodicPointer(id, pointer); err != nil {
		t.Fatalf("legacy source did not recover: %v", err)
	}
	if err := memory.Store.DB().QueryRow(`SELECT CAST(v AS INTEGER) FROM rm_meta WHERE k = ?`, "episodic_source_scan_item_errors:mem:legacy").Scan(&itemErrors); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("source diagnostic not cleared after recovery: errors=%d err=%v", itemErrors, err)
	}
	status, err = inspectEnvHealthAndSync(context.Background(), memory, "")
	if err != nil || !status.Layers["episodic"].AlignmentKnown || !status.Layers["episodic"].Aligned {
		t.Fatalf("recovered source status=%+v err=%v", status.Layers["episodic"], err)
	}
}

func TestEpisodicSourceScanWriteAcrossScales(t *testing.T) {
	for _, count := range []int{10, 100, 1000, 10000} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			t.Setenv("DSH_HOME", filepath.Join(home, ".dsh"))
			dir := filepath.Join(home, ".claude", "projects", "demo")
			if err := os.MkdirAll(dir, 0o700); err != nil {
				t.Fatal(err)
			}
			var body strings.Builder
			for i := 0; i < count; i++ {
				fmt.Fprintf(&body, `{"type":"user","message":{"role":"user","content":"scale question %d"}}`+"\n"+
					`{"type":"assistant","message":{"role":"assistant","stop_reason":"end_turn","content":"scale answer %d"}}`+"\n", i, i)
			}
			if err := os.WriteFile(filepath.Join(dir, "a.jsonl"), []byte(body.String()), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "z.jsonl"), []byte(`{"type":"user","message":{"role":"user","content":"later source"}}`+"\n"+
				`{"type":"assistant","message":{"role":"assistant","stop_reason":"end_turn","content":"later answer"}}`+"\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			memory, err := facade.New(&config.Config{DB: filepath.Join(home, "memory.db"), Scope: "mem:scale", Mode: "local"})
			if err != nil {
				t.Fatal(err)
			}
			defer memory.Close()
			started := time.Now()
			// The deadline is only a hang guard for a functional assertion, so it
			// scales with the fixture size instead of pinning every scale to one
			// wall-clock constant: shared CI runners pace pages far slower than a
			// workstation (10k units is ~400 pages at the default page size).
			budget := 120 * time.Second
			if scaled := time.Duration(count) * 75 * time.Millisecond; scaled > budget {
				budget = scaled
			}
			if raceDetectorEnabled && count >= 10000 {
				t.Skip("10k scale case exceeds the race-detector hang-guard budget")
			}
			ctx, cancel := context.WithTimeout(context.Background(), budget)
			defer cancel()
			pages := 0
			for {
				err := runEpisodicSourceScanTick(ctx, memory, home)
				if err != nil {
					t.Fatalf("page %d after %s: %v", pages, time.Since(started), err)
				}
				pages++
				var n int
				if err := memory.Store.DB().QueryRow(`SELECT COUNT(*) FROM rm_units WHERE doc_id = ?`, episodicunit.DocID("mem:scale", "[assistant] later answer")).Scan(&n); err != nil {
					t.Fatal(err)
				} else if n == 1 {
					break
				}
				if pages > (2*count+2)/reconcileDefaultLimit+2 {
					t.Fatal("later source starved")
				}
			}
			var docs int
			if err := memory.Store.DB().QueryRow(`SELECT COUNT(*) FROM rm_units WHERE layer = 'episodic'`).Scan(&docs); err != nil || docs != 2*count+2 {
				t.Fatalf("docs=%d err=%v", docs, err)
			}
			tc, err := memory.LocalVault()
			if err != nil {
				t.Fatal(err)
			}
			var pointer string
			id := episodicunit.DocID("mem:scale", "[assistant] later answer")
			if err := memory.Store.DB().QueryRow(`SELECT raw_text FROM rm_units WHERE doc_id = ?`, id).Scan(&pointer); err != nil {
				t.Fatal(err)
			}
			if text, err := tc.Vault.LoadVerifiedEpisodicPointer(id, pointer); err != nil || text != "[assistant] later answer" {
				t.Fatalf("later source hydration=%q err=%v", text, err)
			}
			t.Logf("%d turns: %d docs, %d pages, elapsed %s", count, docs, pages, time.Since(started))
		})
	}
}

func TestEpisodicSourceUsesSegmentTimestamp(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, "session.jsonl")
	body := `{"type":"user","timestamp":"2020-01-01T00:00:00Z","message":{"role":"user","content":"dated user"}}` + "\n" +
		`{"type":"assistant","timestamp":"2026-01-01T00:00:00Z","message":{"role":"assistant","stop_reason":"end_turn","content":"dated assistant"}}` + "\n" +
		`{"type":"user","message":{"role":"user","content":"unknown time"}}` + "\n" +
		`{"type":"assistant","timestamp":"2026-01-02T00:00:00Z","message":{"role":"assistant","stop_reason":"end_turn","content":"second answer"}}` + "\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	var turns []candidateEpisodicTurn
	if _, err := streamEpisodicScanSource(context.Background(), episodicScanSource{HarnessID: "claude-code", Kind: "jsonl", Path: path}, "mem:time", func(_ int, turn candidateEpisodicTurn) error {
		turns = append(turns, turn)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	want := []int64{1577836800, 1767225600, 0, 1767312000}
	if len(turns) != len(want) {
		t.Fatalf("turns=%+v", turns)
	}
	for i := range want {
		if turns[i].OccurredAt != want[i] {
			t.Errorf("turn %d timestamp=%d want=%d", i, turns[i].OccurredAt, want[i])
		}
	}
}

func TestEpisodicZCodeCompletionAcrossScales(t *testing.T) {
	for _, count := range []int{100, 1000, 10000} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			home := t.TempDir()
			path := filepath.Join(home, "zcode.sqlite")
			db, err := sql.Open("sqlite", path)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			if _, err := db.Exec(`CREATE TABLE message(id TEXT PRIMARY KEY, session_id TEXT, sequence INTEGER, time_created INTEGER, data TEXT);
				CREATE TABLE part(message_id TEXT, sequence INTEGER, data TEXT);
				CREATE INDEX message_session_sequence ON message(session_id, sequence);
				CREATE INDEX part_message ON part(message_id, sequence);`); err != nil {
				t.Fatal(err)
			}
			tx, err := db.Begin()
			if err != nil {
				t.Fatal(err)
			}
			for i := 0; i < count; i++ {
				user, assistant := fmt.Sprintf("u%d", i), fmt.Sprintf("a%d", i)
				if _, err := tx.Exec(`INSERT INTO message VALUES (?, 's', ?, 1, '{"role":"user"}'), (?, 's', ?, 2, '{"role":"assistant","finish":"stop","time":{"completed":2}}')`, user, i*2, assistant, i*2+1); err != nil {
					t.Fatal(err)
				}
				if _, err := tx.Exec(`INSERT INTO part VALUES (?, 1, ?), (?, 1, ?)`, user, fmt.Sprintf(`{"type":"text","text":"user %d"}`, i), assistant, fmt.Sprintf(`{"type":"text","text":"answer %d"}`, i)); err != nil {
					t.Fatal(err)
				}
			}
			if err := tx.Commit(); err != nil {
				t.Fatal(err)
			}
			started := time.Now()
			seen := 0
			_, err = streamEpisodicScanSource(context.Background(), episodicScanSource{HarnessID: "zcode", Kind: "zcode", Path: path, SessionID: "s"}, "mem:scale", func(_ int, turn candidateEpisodicTurn) error {
				if !turn.Completed {
					t.Fatal("completed turn marked incomplete")
				}
				seen++
				return nil
			})
			if err != nil || seen != count*2 {
				t.Fatalf("seen=%d err=%v", seen, err)
			}
			t.Logf("%d turns: %d units, elapsed %s", count, seen, time.Since(started))
		})
	}
}

func TestEpisodicReconcileCancellationDoesNotImport(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("DSH_HOME", filepath.Join(home, ".dsh"))
	path := filepath.Join(home, ".claude", "projects", "demo", "session.jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	body := `{"type":"user","message":{"role":"user","content":"cancel question"}}` + "\n" +
		`{"type":"assistant","message":{"role":"assistant","stop_reason":"end_turn","content":"cancel answer"}}` + "\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	memory, err := facade.New(&config.Config{DB: filepath.Join(home, "memory.db"), Scope: "mem:cancel", Mode: "local"})
	if err != nil {
		t.Fatal(err)
	}
	defer memory.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := executeReconcileSync(ctx, memory, "episodic", "claude-code", 100); !errors.Is(err, context.Canceled) {
		t.Fatalf("reconcile cancellation err=%v", err)
	}
	var n int
	if err := memory.Store.DB().QueryRow(`SELECT COUNT(*) FROM rm_units WHERE layer = 'episodic'`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("cancelled reconcile wrote %d documents: %v", n, err)
	}
}

func TestEpisodicStatusRejectsMalformedCheckpoint(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("DSH_HOME", filepath.Join(home, ".dsh"))
	memory, err := facade.New(&config.Config{DB: filepath.Join(home, "memory.db"), Scope: "mem:checkpoint", Mode: "local"})
	if err != nil {
		t.Fatal(err)
	}
	defer memory.Close()
	if _, err := memory.LocalVault(); err != nil {
		t.Fatal(err)
	}
	if _, err := memory.Store.DB().Exec(`INSERT INTO rm_meta(k, v) VALUES (?, ?)`, "episodic_source_scan_cursor:mem:checkpoint", []byte("invalid")); err != nil {
		t.Fatal(err)
	}
	status, err := inspectEnvHealthAndSync(context.Background(), memory, "")
	if err != nil || status.EpisodicIndex == nil || status.EpisodicIndex.Error == "" || status.Layers["episodic"].AlignmentKnown || status.Layers["episodic"].Aligned {
		t.Fatalf("malformed checkpoint status=%+v err=%v", status, err)
	}
}

func TestEpisodicZCodeCompletionUsesReadSnapshot(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, "zcode.sqlite")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`PRAGMA journal_mode=WAL;
		CREATE TABLE message(id TEXT PRIMARY KEY, session_id TEXT, sequence INTEGER, time_created INTEGER, data TEXT);
		CREATE TABLE part(message_id TEXT, sequence INTEGER, data TEXT);
		INSERT INTO message VALUES ('u','s',1,1,'{"role":"user"}'),('a','s',2,2,'{"role":"assistant","finish":"stop","time":{"completed":2}}');
		INSERT INTO part VALUES ('u',1,'{"type":"text","text":"question"}'),('a',1,'{"type":"text","text":"answer"}');`); err != nil {
		t.Fatal(err)
	}
	tx, err := db.BeginTx(context.Background(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	var initial int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM message`).Scan(&initial); err != nil || initial != 2 {
		t.Fatalf("snapshot count=%d err=%v", initial, err)
	}
	if _, err := db.Exec(`UPDATE message SET data='{"role":"assistant","finish":"tool-calls","time":{"completed":2}}' WHERE id='a'; INSERT INTO part VALUES ('a',2,'{"type":"tool"}')`); err != nil {
		t.Fatal(err)
	}
	var snapshotTurns []episodic.Turn
	if err := episodic.VisitZCodeTurnsFrom(context.Background(), tx, "s", "", func(turn episodic.Turn, _ bool) error {
		snapshotTurns = append(snapshotTurns, turn)
		return nil
	}); err != nil || len(snapshotTurns) != 1 || len(snapshotTurns[0].Segments) != 2 {
		t.Fatalf("snapshot turns=%+v err=%v", snapshotTurns, err)
	}
	complete, err := zcodeTurnCompleted(context.Background(), tx, "s", "u")
	if err != nil || !complete {
		t.Fatalf("old snapshot completion=%v err=%v", complete, err)
	}
	complete, err = zcodeTurnCompleted(context.Background(), db, "s", "u")
	if err != nil || complete {
		t.Fatalf("new snapshot completion=%v err=%v", complete, err)
	}
}

func TestEpisodicZCodeStreamKeepsSnapshotDuringWrite(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, "zcode.sqlite")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`PRAGMA journal_mode=WAL;
		CREATE TABLE message(id TEXT PRIMARY KEY, session_id TEXT, sequence INTEGER, time_created INTEGER, data TEXT);
		CREATE TABLE part(message_id TEXT, sequence INTEGER, data TEXT);
		INSERT INTO message VALUES ('u1','s',1,1,'{"role":"user"}'),('a1','s',2,2,'{"role":"assistant","finish":"stop","time":{"completed":2}}'),('u2','s',3,3,'{"role":"user"}'),('a2','s',4,4,'{"role":"assistant","finish":"stop","time":{"completed":4}}');
		INSERT INTO part VALUES ('u1',1,'{"type":"text","text":"question one"}'),('a1',1,'{"type":"text","text":"answer one"}'),('u2',1,'{"type":"text","text":"question two"}'),('a2',1,'{"type":"text","text":"answer two"}');`); err != nil {
		t.Fatal(err)
	}
	source := episodicScanSource{HarnessID: "zcode", Kind: "zcode", Path: path, SessionID: "s"}
	changed := false
	var observed []candidateEpisodicTurn
	_, err = streamEpisodicScanSource(context.Background(), source, "mem:snapshot", func(_ int, turn candidateEpisodicTurn) error {
		observed = append(observed, turn)
		if !changed {
			changed = true
			_, err := db.Exec(`UPDATE message SET data='{"role":"assistant","finish":"tool-calls","time":{"completed":4}}' WHERE id='a2'; INSERT INTO part VALUES ('a2',2,'{"type":"tool"}')`)
			return err
		}
		return nil
	})
	if err != nil || len(observed) != 4 || !observed[2].Completed || !observed[3].Completed {
		t.Fatalf("snapshot observed=%+v err=%v", observed, err)
	}
	observed = nil
	_, err = streamEpisodicScanSource(context.Background(), source, "mem:snapshot", func(_ int, turn candidateEpisodicTurn) error {
		observed = append(observed, turn)
		return nil
	})
	if err != nil || len(observed) != 3 || observed[2].Completed {
		t.Fatalf("fresh source observed=%+v err=%v", observed, err)
	}
}

func TestEpisodicSourceDeletionAfterSnapshotIsSkipped(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	path := filepath.Join(home, "session.jsonl")
	body := `{"type":"user","message":{"role":"user","content":"deleted after snapshot"}}` + "\n" +
		`{"type":"assistant","message":{"role":"assistant","stop_reason":"end_turn","content":"remaining answer"}}` + "\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	memory, err := facade.New(&config.Config{DB: filepath.Join(home, "memory.db"), Scope: "mem:delete-race", Mode: "local"})
	if err != nil {
		t.Fatal(err)
	}
	defer memory.Close()
	tc, err := memory.LocalVault()
	if err != nil {
		t.Fatal(err)
	}
	deletedBeforeScan := tc.Vault.DeletedUnitSet("episodic")
	var turns []candidateEpisodicTurn
	if _, err := streamEpisodicScanSource(context.Background(), episodicScanSource{HarnessID: "claude-code", Kind: "jsonl", Path: path}, "mem:delete-race", func(_ int, turn candidateEpisodicTurn) error {
		turns = append(turns, turn)
		return nil
	}); err != nil || len(turns) != 2 {
		t.Fatalf("turns=%+v err=%v", turns, err)
	}
	tc.Vault.RecordDeletedUnits("episodic", turns[0].DocID)
	if !tc.Vault.DeletedUnitSet("episodic")[turns[0].DocID] || deletedBeforeScan[turns[0].DocID] {
		t.Fatal("tombstone was not added after the scan snapshot")
	}
	sources := []episodicScanSource{{HarnessID: "claude-code", Kind: "jsonl", Path: path}}
	cursor, more, err := visitEpisodicSources(context.Background(), sources, "mem:delete-race", episodicSourceCursor{}, 10, func(turn candidateEpisodicTurn) error {
		return saveEpisodicSourceTurn(context.Background(), memory.Store.DB(), tc.Vault, "mem:delete-race", deletedBeforeScan, turn)
	})
	if err != nil || more || cursor.RetrySources || cursor.ItemErrors != 0 {
		t.Fatalf("deleted item treated as scan failure: cursor=%+v more=%v err=%v", cursor, more, err)
	}
	var deleted, remaining int
	if err := memory.Store.DB().QueryRow(`SELECT COUNT(*) FROM rm_units WHERE doc_id = ?`, turns[0].DocID).Scan(&deleted); err != nil {
		t.Fatal(err)
	}
	if err := memory.Store.DB().QueryRow(`SELECT COUNT(*) FROM rm_units WHERE doc_id = ?`, turns[1].DocID).Scan(&remaining); err != nil {
		t.Fatal(err)
	}
	if deleted != 0 || remaining != 1 {
		t.Fatalf("deleted=%d remaining=%d", deleted, remaining)
	}
}

func TestEpisodicSourceScanJSONLResumeAndChange(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, ".claude", "projects", "demo")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 105; i++ {
		path := filepath.Join(dir, fmt.Sprintf("%03d.jsonl", i))
		if err := os.WriteFile(path, []byte(fmt.Sprintf(`{"type":"user","message":{"role":"user","content":"item %d"}}`+"\n", i)), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	sources, err := discoverEpisodicScanSources(context.Background(), home, "claude-code")
	if err != nil || len(sources) != 105 {
		t.Fatalf("discovery sources=%d err=%v", len(sources), err)
	}
	seen := map[string]int{}
	cursor := episodicSourceCursor{}
	for {
		var more bool
		cursor, more, err = visitEpisodicSources(context.Background(), sources, "mem:test", cursor, 7, func(turn candidateEpisodicTurn) error {
			seen[turn.RefPointer]++
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		if !more {
			break
		}
	}
	if len(seen) != 105 {
		t.Fatalf("visited %d of 105 files", len(seen))
	}
	for pointer, count := range seen {
		if count != 1 {
			t.Fatalf("%s visited %d times", pointer, count)
		}
	}

	path := sources[0].Path
	var lines strings.Builder
	for i := 0; i < 1001; i++ {
		fmt.Fprintf(&lines, `{"type":"user","message":{"role":"user","content":"turn %d"}}`+"\n", i)
	}
	if err := os.WriteFile(path, []byte(lines.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	failure := errors.New("save failed")
	cursor, more, err := visitEpisodicSources(context.Background(), sources[:1], "mem:test", episodicSourceCursor{}, 1001, func(turn candidateEpisodicTurn) error {
		if strings.Contains(turn.Text, "turn 7") {
			return failure
		}
		return nil
	})
	if !errors.Is(err, failure) || !more || cursor.Index != 7 {
		t.Fatalf("failure cursor=%+v more=%v err=%v", cursor, more, err)
	}
	if err := os.WriteFile(path, []byte(lines.String()+`{"type":"user","message":{"role":"user","content":"new turn"}}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var first string
	_, _, err = visitEpisodicSources(context.Background(), sources[:1], "mem:test", cursor, 1, func(turn candidateEpisodicTurn) error { first = turn.Text; return nil })
	if err != nil || !strings.Contains(first, "turn 7") {
		t.Fatalf("changed source did not resume failed turn: first=%q err=%v", first, err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	_, more, err = visitEpisodicSources(context.Background(), sources[:1], "mem:test", cursor, 1, func(candidateEpisodicTurn) error { return nil })
	if err == nil || !more {
		t.Fatalf("missing source: more=%v err=%v", more, err)
	}
}

func TestEpisodicSourceScanTickRestartFailureAndFairness(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, ".claude", "projects", "demo")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "a.jsonl")
	var transcript strings.Builder
	for i := 0; i < reconcileDefaultLimit+2; i++ {
		fmt.Fprintf(&transcript, `{"type":"user","message":{"role":"user","content":"entry %d"}}`+"\n"+`{"type":"event_msg","payload":{"type":"task_complete"}}`+"\n", i)
	}
	original := transcript.String()
	if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(dir, "z.jsonl")
	if err := os.WriteFile(other, []byte(`{"type":"user","message":{"role":"user","content":"other session"}}`+"\n"+`{"type":"event_msg","payload":{"type":"task_complete"}}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	oldTime := time.Now().Add(-time.Hour)
	if err := os.Chtimes(other, oldTime, oldTime); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{DB: filepath.Join(home, "memory.db"), Scope: "mem:scan-test", Mode: "local"}
	memory, err := facade.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := runEpisodicSourceScanTick(context.Background(), memory, home); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := memory.Store.DB().QueryRow(`SELECT count(*) FROM rm_units WHERE layer = 'episodic'`).Scan(&count); err != nil || count != reconcileDefaultLimit {
		t.Fatalf("first page count=%d err=%v", count, err)
	}
	if err := memory.Close(); err != nil {
		t.Fatal(err)
	}
	memory, err = facade.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer memory.Close()
	failedID := episodicunit.DocID(cfg.Scope, fmt.Sprintf("[user] entry %d", reconcileDefaultLimit))
	if _, err := memory.Store.DB().Exec(`CREATE TRIGGER scan_test_failure BEFORE INSERT ON rm_episodic_index WHEN NEW.doc_id = '` + failedID + `' BEGIN SELECT RAISE(ABORT, 'write failed'); END`); err != nil {
		t.Fatal(err)
	}
	if err := runEpisodicSourceScanTick(context.Background(), memory, home); err == nil {
		t.Fatal("expected index failure")
	}
	if err := memory.Store.DB().QueryRow(`SELECT count(*) FROM rm_units WHERE doc_id = ?`, failedID).Scan(&count); err != nil || count != 0 {
		t.Fatalf("failed write persisted document count=%d err=%v", count, err)
	}
	if _, err := memory.Store.DB().Exec(`DROP TRIGGER scan_test_failure`); err != nil {
		t.Fatal(err)
	}
	// Appends must not restart the first page or starve the other session.
	for i := 0; i < 3; i++ {
		f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
		if err != nil {
			t.Fatal(err)
		}
		_, err = fmt.Fprintf(f, `{"type":"user","message":{"role":"user","content":"new entry %d"}}`+"\n"+`{"type":"event_msg","payload":{"type":"task_complete"}}`+"\n", i)
		_ = f.Close()
		if err != nil {
			t.Fatal(err)
		}
		if err := runEpisodicSourceScanTick(context.Background(), memory, home); err != nil {
			t.Fatal(err)
		}
	}
	oldTime = time.Now().Add(-time.Hour)
	if err := os.Chtimes(path, oldTime, oldTime); err != nil {
		t.Fatal(err)
	}
	if err := runEpisodicSourceScanTick(context.Background(), memory, home); err != nil {
		t.Fatal(err)
	}
	if err := runEpisodicSourceScanTick(context.Background(), memory, home); err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{fmt.Sprintf("[user] entry %d", reconcileDefaultLimit), "[user] other session", "[user] new entry 2"} {
		id := episodicunit.DocID(cfg.Scope, text)
		if err := memory.Store.DB().QueryRow(`SELECT count(*) FROM rm_units WHERE doc_id = ?`, id).Scan(&count); err != nil || count != 1 {
			t.Fatalf("missing %q count=%d err=%v", text, count, err)
		}
	}
	data, err := os.ReadFile(path)
	if err != nil || !strings.HasPrefix(string(data), original) {
		t.Fatalf("source was modified: %v", err)
	}
}

func TestEpisodicSourceScanIndependentScopesAndSources(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, ".claude", "projects", "demo")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 105; i++ {
		path := filepath.Join(dir, fmt.Sprintf("%03d.jsonl", i))
		if err := os.WriteFile(path, []byte(fmt.Sprintf(`{"type":"user","timestamp":"2026-01-01T00:00:00Z","message":{"role":"user","content":"record %d"}}`+"\n"+`{"type":"event_msg","payload":{"type":"task_complete"}}`+"\n", i)), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	path := filepath.Join(home, ".zcode", "cli", "db", "db.sqlite")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("invalid database"), 0o600); err != nil {
		t.Fatal(err)
	}
	for i, scope := range []string{"mem:scan-one", "mem:scan-two"} {
		memory, err := facade.New(&config.Config{DB: filepath.Join(home, fmt.Sprintf("memory-%d.db", i)), Scope: scope, Mode: "local"})
		if err != nil {
			t.Fatal(err)
		}
		for i := 0; i < 3; i++ {
			if err := runEpisodicSourceScanTick(context.Background(), memory, home); err == nil {
				t.Fatal("expected unavailable zcode source to be reported")
			}
		}
		var count int
		if err := memory.Store.DB().QueryRow(`SELECT count(*) FROM rm_units WHERE layer = 'episodic'`).Scan(&count); err != nil || count != 105 {
			t.Fatalf("scope %s: count=%d err=%v", scope, count, err)
		}
		_ = memory.Close()
	}
}

func TestEpisodicSourceScanRetainsDuplicateLineage(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, ".claude", "projects", "demo")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"a.jsonl", "b.jsonl"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(`{"type":"user","timestamp":"2026-01-01T00:00:00Z","message":{"role":"user","content":"same record"}}`+"\n"+`{"type":"event_msg","payload":{"type":"task_complete"}}`+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	memory, err := facade.New(&config.Config{DB: filepath.Join(home, "memory.db"), Scope: "mem:lineage", Mode: "local"})
	if err != nil {
		t.Fatal(err)
	}
	defer memory.Close()
	if err := runEpisodicSourceScanTick(context.Background(), memory, home); err != nil {
		t.Fatal(err)
	}
	var documents, lineage int
	if err := memory.Store.DB().QueryRow(`SELECT count(*) FROM rm_units WHERE layer = 'episodic'`).Scan(&documents); err != nil {
		t.Fatal(err)
	}
	if err := memory.Store.DB().QueryRow(`SELECT count(*) FROM rm_episodic_lineage`).Scan(&lineage); err != nil {
		t.Fatal(err)
	}
	if documents != 1 || lineage != 2 {
		t.Fatalf("documents=%d lineage=%d", documents, lineage)
	}
}

func TestEpisodicSourceScanZCodeActiveWAL(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, ".zcode", "cli", "db", "db.sqlite")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`PRAGMA journal_mode=WAL; CREATE TABLE session(id TEXT, parent_id TEXT, task_type TEXT); CREATE TABLE message(id TEXT, session_id TEXT, sequence INTEGER, time_created INTEGER, data TEXT); CREATE TABLE part(message_id TEXT, sequence INTEGER, data TEXT); INSERT INTO session VALUES ('a','','interactive');`); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 120; i++ {
		id := fmt.Sprintf("m%03d", i)
		if _, err := db.Exec(`INSERT INTO message VALUES (?, 'a', ?, 1, '{"role":"user"}')`, id, i); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`INSERT INTO part VALUES (?, 1, ?)`, id, fmt.Sprintf(`{"type":"text","text":"wal record %d"}`, i)); err != nil {
			t.Fatal(err)
		}
	}
	sources, err := discoverEpisodicScanSources(context.Background(), home, "zcode")
	if err != nil {
		t.Fatal(err)
	}
	cursor := episodicSourceCursor{}
	seen := map[string]bool{}
	for page := 0; page < 3; page++ {
		if _, err := db.Exec(`UPDATE session SET task_type = 'interactive' WHERE id = 'a'`); err != nil {
			t.Fatal(err)
		}
		var more bool
		cursor, more, err = visitEpisodicSources(context.Background(), sources, "mem:test", cursor, 50, func(turn candidateEpisodicTurn) error {
			seen[turn.RefPointer] = true
			return nil
		})
		if err != nil || (page < 2 && !more) {
			t.Fatalf("page %d more=%v err=%v", page, more, err)
		}
	}
	if len(seen) != 120 {
		t.Fatalf("visited %d of 120 records", len(seen))
	}
}

func TestEpisodicSourceScanZCodeSessions(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, ".zcode", "cli", "db", "db.sqlite")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`CREATE TABLE session(id TEXT, parent_id TEXT, task_type TEXT); CREATE TABLE message(id TEXT, session_id TEXT, sequence INTEGER, time_created INTEGER, data TEXT); CREATE TABLE part(message_id TEXT, sequence INTEGER, data TEXT);
	INSERT INTO session VALUES ('a', '', 'interactive'), ('b', '', 'interactive');
	INSERT INTO message VALUES ('a1','a',1,1,'{"role":"user"}'), ('b1','b',1,1,'{"role":"user"}');
	INSERT INTO part VALUES ('a1',1,'{"type":"text","text":"first"}'), ('b1',1,'{"type":"text","text":"second"}');`)
	_ = db.Close()
	if err != nil {
		t.Fatal(err)
	}
	sources, err := discoverEpisodicScanSources(context.Background(), home, "zcode")
	if err != nil || len(sources) != 2 {
		t.Fatalf("sources=%+v err=%v", sources, err)
	}
	seen := map[string]bool{}
	cursor := episodicSourceCursor{}
	for {
		var more bool
		cursor, more, err = visitEpisodicSources(context.Background(), sources, "mem:test", cursor, 1, func(turn candidateEpisodicTurn) error { seen[turn.SessionID] = true; return nil })
		if err != nil {
			t.Fatal(err)
		}
		if !more {
			break
		}
	}
	if !seen["a"] || !seen["b"] {
		t.Fatalf("sessions visited: %v", seen)
	}
}

// TestEpisodicStampZCodeConcurrentWriteIsNotIncomplete locks the live-host
// completeness rule: a zcode scan reads each session from one transaction
// snapshot, so an append that lands mid-scan must not turn the whole pass into
// "incomplete coverage". Transcript files keep the strict stamp check.
func TestEpisodicStampZCodeConcurrentWriteIsNotIncomplete(t *testing.T) {
	if episodicStampMatters(episodicScanSource{Kind: "zcode"}, "1:2", "1:3") {
		t.Fatal("zcode concurrent append marked the scan incomplete")
	}
	if !episodicStampMatters(episodicScanSource{Kind: "jsonl"}, "1:2", "1:3") {
		t.Fatal("jsonl stamp change was ignored")
	}
	if episodicStampMatters(episodicScanSource{Kind: "jsonl"}, "1:2", "1:2") {
		t.Fatal("unchanged jsonl stamp marked the scan incomplete")
	}
}

// TestNoeticHealthSeparatesSupersededFromStale locks the L3 health boundary:
// superseding is the normal trace-dedupe lifecycle (the newer trace carries the
// cue), so it must not be reported as stale or reduce the Noetic health score.
func TestNoeticHealthSeparatesSupersededFromStale(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	memory, err := facade.New(&config.Config{DB: filepath.Join(home, "memory.db"), Scope: "mem:noetic-health", Mode: "local"})
	if err != nil {
		t.Fatal(err)
	}
	defer memory.Close()
	if _, err := memory.LocalVault(); err != nil {
		t.Fatal(err)
	}
	for _, row := range []struct{ id, status string }{
		{"NT-ACTIVE", "active"},
		{"NT-SUPERSEDED", "superseded"},
	} {
		if _, err := memory.Store.DB().Exec(
			`INSERT INTO rm_noetic(noetic_id, status, query_text, answer_text) VALUES (?, ?, ?, ?)`,
			row.id, row.status, row.id, row.id,
		); err != nil {
			t.Fatal(err)
		}
	}
	status, err := inspectEnvHealthAndSync(context.Background(), memory, "")
	if err != nil {
		t.Fatal(err)
	}
	noe := status.Layers["noetic"]
	if noe.StaleCount != 0 || noe.SupersededCount != 1 {
		t.Fatalf("noetic lifecycle counts = stale %d superseded %d", noe.StaleCount, noe.SupersededCount)
	}
	for _, check := range status.HealthChecks {
		if check.Key != "noetic_sync" {
			continue
		}
		if check.Status != "healthy" || check.Score != 15 || !strings.Contains(check.DetailEN, "Superseded 1") {
			t.Fatalf("noetic health = %+v", check)
		}
		return
	}
	t.Fatal("noetic health check missing")
}

// TestResolveJSONLTranscriptLocusCanonicalIdentity 锁定入库面的工作区身份口径：
// 会话 cwd 解析为仓库根 basename（子目录归仓库），非仓库目录得到已解析的空串
// （而不是目录名），只有读不到 cwd 时才报告未解析（调用方保留存量 locus）。
func TestResolveJSONLTranscriptLocusCanonicalIdentity(t *testing.T) {
	repoRoot := filepath.Join(t.TempDir(), "ws-repo")
	if err := os.MkdirAll(filepath.Join(repoRoot, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	plainDir := filepath.Join(t.TempDir(), "codex-smoke")
	if err := os.MkdirAll(plainDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeTranscript := func(cwd string) string {
		t.Helper()
		path := filepath.Join(t.TempDir(), "sess.jsonl")
		body := fmt.Sprintf(`{"type":"user","cwd":%q,"message":{"role":"user","content":"hi"}}`+"\n", cwd)
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		return path
	}
	noCwd := filepath.Join(t.TempDir(), "nocwd.jsonl")
	if err := os.WriteFile(noCwd, []byte(`{"type":"user","message":{"role":"user","content":"hi"}}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name      string
		path      string
		wantLocus string
		wantOK    bool
	}{
		{"repo root", writeTranscript(repoRoot), "ws-repo", true},
		{"repo subdirectory", writeTranscript(filepath.Join(repoRoot, "admin", "web")), "ws-repo", true},
		{"non-repo directory", writeTranscript(plainDir), "", true},
		{"no cwd line", noCwd, "", false},
		{"missing file", filepath.Join(t.TempDir(), "nope.jsonl"), "", false},
	}
	for _, tc := range cases {
		loc, ok := resolveJSONLTranscriptLocus(tc.path)
		if loc != tc.wantLocus || ok != tc.wantOK {
			t.Errorf("%s: resolveJSONLTranscriptLocus = (%q, %v), want (%q, %v)", tc.name, loc, ok, tc.wantLocus, tc.wantOK)
		}
	}
}

// writeOpenCodeScanFixture creates a session_message store with `turns` complete
// user/assistant pairs. It returns the writer handle so a test can append to the
// same store while a page is running.
func writeOpenCodeScanFixture(t *testing.T, path string, turns int) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(`CREATE TABLE session_message (
		id TEXT PRIMARY KEY, session_id TEXT, type TEXT, seq INTEGER,
		time_created INTEGER, time_updated INTEGER, data TEXT)`); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < turns; i++ {
		userID, assistantID := fmt.Sprintf("u%d", i), fmt.Sprintf("a%d", i)
		if _, err := db.Exec(`INSERT INTO session_message VALUES
			(?, 's1', 'user', ?, ?, ?, ?),
			(?, 's1', 'assistant', ?, ?, ?, ?),
			(?, 's1', 'idle', ?, ?, ?, '{"outcome":"succeeded"}')`,
			userID, i*3+1, int64(1793102400000+i*1000), int64(1793102400000+i*1000),
			fmt.Sprintf(`{"text":"question %d"}`, i),
			assistantID, i*3+2, int64(1793102400100+i*1000), int64(1793102400100+i*1000),
			fmt.Sprintf(`{"content":[{"type":"text","text":"answer %d"}],"time":{"created":%d,"completed":%d}}`, i, 1793102400100+i*1000, 1793102400200+i*1000),
			fmt.Sprintf("i%d", i), i*3+3, int64(1793102400200+i*1000), int64(1793102400200+i*1000),
		); err != nil {
			t.Fatal(err)
		}
	}
	return db
}

// TestEpisodicOpenCodePageToleratesLiveWrites pins the SQLite source contract:
// a host write during the page does not invalidate the rows already read, so the
// page completes instead of retrying as a changed transcript.
func TestEpisodicOpenCodePageToleratesLiveWrites(t *testing.T) {
	path := filepath.Join(t.TempDir(), "opencode.db")
	writer := writeOpenCodeScanFixture(t, path, 2)
	sources := []episodicScanSource{{HarnessID: "opencode", Kind: "opencode", Path: path, SessionID: "s1"}}
	seen := 0
	cursor, more, err := visitEpisodicSourcesVerified(context.Background(), sources, "mem:oc-live", episodicSourceCursor{}, 10, 0, func(_ *episodic.SourceSnapshot, turn candidateEpisodicTurn) error {
		seen++
		if seen == 1 {
			if _, werr := writer.Exec(`INSERT INTO session_message VALUES
				('u9', 's1', 'user', 99, 1793102500000, 1793102500000, '{"text":"live question"}'),
				('a9', 's1', 'assistant', 100, 1793102500100, 1793102500100, '{"content":[{"type":"text","text":"live answer"}],"time":{"created":1793102500100,"completed":1793102500200}}')`); werr != nil {
				t.Fatalf("append during page: %v", werr)
			}
		}
		return nil
	})
	if err != nil || more {
		t.Fatalf("live write failed the page: more=%v err=%v", more, err)
	}
	if seen != 4 {
		t.Fatalf("page visited %d units, want 4", seen)
	}
	if cursor.RetrySources {
		t.Fatalf("live write requested a retry: %+v", cursor)
	}
}

// TestEpisodicOpenCodeCursorResume pins the page cursor for the SQLite source
// kind: the second page restarts at the saved turn without repeating units.
func TestEpisodicOpenCodeCursorResume(t *testing.T) {
	path := filepath.Join(t.TempDir(), "opencode.db")
	writeOpenCodeScanFixture(t, path, 3)
	sources := []episodicScanSource{{HarnessID: "opencode", Kind: "opencode", Path: path, SessionID: "s1"}}
	var first []string
	cursor, more, err := visitEpisodicSourcesVerified(context.Background(), sources, "mem:oc-resume", episodicSourceCursor{}, 1, 0, func(_ *episodic.SourceSnapshot, turn candidateEpisodicTurn) error {
		first = append(first, turn.Text)
		return nil
	})
	if err != nil || !more || len(first) != 1 {
		t.Fatalf("first page more=%v texts=%v err=%v", more, first, err)
	}
	var rest []string
	_, more, err = visitEpisodicSourcesVerified(context.Background(), sources, "mem:oc-resume", cursor, 10, 0, func(_ *episodic.SourceSnapshot, turn candidateEpisodicTurn) error {
		rest = append(rest, turn.Text)
		return nil
	})
	if err != nil || more {
		t.Fatalf("second page more=%v err=%v", more, err)
	}
	if len(rest) != 5 {
		t.Fatalf("second page visited %d units, want 5: %v", len(rest), rest)
	}
	all := append(first, rest...)
	seen := map[string]bool{}
	for _, text := range all {
		if seen[text] {
			t.Fatalf("unit repeated across pages: %q", text)
		}
		seen[text] = true
	}
	if !seen["[user] question 0"] || !seen["[assistant] answer 2"] {
		t.Fatalf("pages missed units: %v", all)
	}
}
