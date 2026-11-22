// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package cli_test

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/rocareer/rolling-memory/internal/config"
	"github.com/rocareer/rolling-memory/internal/text"
	_ "modernc.org/sqlite"
)

// writeActiveWorkspacePresence 以 hook 写侧的同一形状落一条短租约会话记录。
func writeActiveWorkspacePresence(t *testing.T, carrier, session string, updatedAt int64) {
	t.Helper()
	coords := text.PresenceCoordinates{Carrier: carrier, Locus: "demo-repo"}
	if err := text.SavePresenceFile(config.DefaultStateDir(), coords, session, updatedAt); err != nil {
		t.Fatalf("write presence: %v", err)
	}
}

func runStatusJSON(t *testing.T, dbPath string, extra ...string) map[string]any {
	t.Helper()
	args := append([]string{"sync", "status", "--db", dbPath, "--json"}, extra...)
	res := run(t, args...)
	if res.code != 0 {
		t.Fatalf("sync status exit=%d stdout=%s stderr=%s", res.code, res.stdout, res.stderr)
	}
	return jsonObject(t, res.stdout)
}

func harnessItem(t *testing.T, payload map[string]any, id string) map[string]any {
	t.Helper()
	items, _ := payload["harnesses"].([]any)
	for _, raw := range items {
		item, _ := raw.(map[string]any)
		if item["id"] == id {
			return item
		}
	}
	t.Fatalf("harness %q missing from inventory: %v", id, payload["harnesses"])
	return nil
}

// TestSyncStatusResolvesWorkBuddyPresenceToWorkBuddy 锁定宿主激活探测回归：
// presence 文件里的宿主是 workbuddy（WorkBuddy 插件 hook 写入），早期因注册表
// 只有 codebuddy 条目而匹配失败，落进「最近活跃」兜底并把并列 0 分的第 0 位
// （zcode）点亮 —— 在从未安装 ZCode 的机器上显示「ZCode 激活」。
func TestSyncStatusResolvesWorkBuddyPresenceToWorkBuddy(t *testing.T) {
	env := newEnv(t)
	if err := os.MkdirAll(filepath.Join(os.Getenv("HOME"), ".workbuddy"), 0o755); err != nil {
		t.Fatalf("mkdir workbuddy: %v", err)
	}
	writeActiveWorkspacePresence(t, "harness:workbuddy", "sess-workbuddy-1", time.Now().Unix())

	payload := runStatusJSON(t, filepath.Join(env.base, "workbuddy.db"))
	if payload["active_harness_id"] != "workbuddy" {
		t.Fatalf("active_harness_id = %v, want workbuddy", payload["active_harness_id"])
	}
	if payload["active_harness_label"] != "WorkBuddy" {
		t.Fatalf("active_harness_label = %v, want WorkBuddy", payload["active_harness_label"])
	}
	if payload["detection_source"] != "hook" {
		t.Fatalf("detection_source = %v, want hook", payload["detection_source"])
	}
	workbuddy := harnessItem(t, payload, "workbuddy")
	if workbuddy["active"] != true || workbuddy["detected"] != true {
		t.Fatalf("workbuddy item = %+v, want active+detected", workbuddy)
	}
	if zcode := harnessItem(t, payload, "zcode"); zcode["active"] != false {
		t.Fatalf("zcode must not be active: %+v", zcode)
	}
}

// TestSyncStatusWithoutHarnessEvidenceReportsNone 锁定零证据诚实口径：
// 没有 presence 文件、没有宿主环境变量、没有转录 —— 活跃宿主为「无」，
// 而不是凭硬编码默认值点亮某个宿主。
func TestSyncStatusWithoutHarnessEvidenceReportsNone(t *testing.T) {
	env := newEnv(t)
	payload := runStatusJSON(t, filepath.Join(env.base, "no-evidence.db"))
	if payload["active_harness_id"] != "" || payload["active_harness_label"] != "" {
		t.Fatalf("expected no active harness, got id=%v label=%v", payload["active_harness_id"], payload["active_harness_label"])
	}
	if payload["detection_source"] != "none" {
		t.Fatalf("detection_source = %v, want none", payload["detection_source"])
	}
	items, _ := payload["harnesses"].([]any)
	for _, raw := range items {
		item, _ := raw.(map[string]any)
		if item["active"] == true {
			t.Fatalf("harness %v marked active without evidence", item["id"])
		}
	}
}

// TestSyncStatusStalePresenceLosesToFresherTranscript 锁定过期 presence 不再挟持读数：
// 三天前的 zcode 租约对上一个刚写入的 claude-code 转录，「当前附着」为空，
// 「最近活跃」如实记 claude-code。
func TestSyncStatusStalePresenceLosesToFresherTranscript(t *testing.T) {
	env := newEnv(t)
	writeActiveWorkspacePresence(t, "harness:zcode", "sess-old", time.Now().Add(-72*time.Hour).Unix())

	sessionDir := filepath.Join(os.Getenv("HOME"), ".claude", "projects", "demo")
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		t.Fatalf("mkdir claude sessions: %v", err)
	}
	lines := `{"type":"user","sessionId":"s1","timestamp":"2026-11-12T00:00:00","message":{"role":"user","content":[{"type":"text","text":"检查活跃宿主检测。"}]}}` + "\n" +
		`{"type":"assistant","sessionId":"s1","timestamp":"2026-11-12T00:00:10","message":{"role":"assistant","content":[{"type":"text","text":"已修复并复验。"}]}}` + "\n"
	if err := os.WriteFile(filepath.Join(sessionDir, "s1.jsonl"), []byte(lines), 0o644); err != nil {
		t.Fatalf("write claude transcript: %v", err)
	}

	payload := runStatusJSON(t, filepath.Join(env.base, "stale.db"))
	if payload["active_harness_id"] != "" {
		t.Fatalf("stale lease must not be the current attachment: %v", payload["active_harness_id"])
	}
	if payload["detection_source"] != "none" {
		t.Fatalf("detection_source = %v, want none", payload["detection_source"])
	}
	if payload["recent_harness_id"] != "claude-code" {
		t.Fatalf("recent_harness_id = %v, want claude-code", payload["recent_harness_id"])
	}
}

// TestSyncStatusHarnessFilterIsScopeNotEvidence 锁定 `--harness` 只是扫描/展示范围：
// 它可以让过滤目标进入清单，但不得把自身当作「活跃」证据无中生有。
func TestSyncStatusHarnessFilterIsScopeNotEvidence(t *testing.T) {
	env := newEnv(t)
	payload := runStatusJSON(t, filepath.Join(env.base, "filter.db"), "--harness", "workbuddy")
	if payload["active_harness_id"] != "" {
		t.Fatalf("filter fabricated an active harness: %v", payload["active_harness_id"])
	}
	if !func() bool {
		items, _ := payload["harnesses"].([]any)
		for _, raw := range items {
			if item, _ := raw.(map[string]any); item["id"] == "workbuddy" {
				return true
			}
		}
		return false
	}() {
		t.Fatalf("filtered harness missing from inventory: %v", payload["harnesses"])
	}
}

// TestSyncReconcileImportsWorkBuddyJSONLTurns 锁定 16 机器 WorkBuddy 会话同步：
// `~/.workbuddy/projects/<ws>/<conv>.jsonl`（毫秒级 timestamp + input_text/output_text 块）
// 在 `sync status` 与 `sync reconcile` 下可正确计数并导入本地金库。
func TestSyncReconcileImportsWorkBuddyJSONLTurns(t *testing.T) {
	env := newEnv(t)
	dbPath := filepath.Join(env.base, "workbuddy-sync.db")
	projDir := filepath.Join(os.Getenv("HOME"), ".workbuddy", "projects", "Users-demo-WorkBuddy-2026-11-11")
	if err := os.MkdirAll(projDir, 0o755); err != nil {
		t.Fatalf("mkdir workbuddy project: %v", err)
	}
	nowMS := time.Now().Add(-5 * time.Minute).UnixMilli()
	lines := `{"id":"u1","timestamp":` + jsonNumberString(nowMS) + `,"type":"message","role":"user","content":[{"type":"input_text","text":"在 16 机器上同步 WorkBuddy 会话记忆。"}]}` + "\n" +
		`{"id":"a1","timestamp":` + jsonNumberString(nowMS+2000) + `,"type":"assistant","message":{"role":"assistant","stop_reason":"end_turn","content":[{"type":"output_text","text":"WorkBuddy JSONL 会话已成功扫描并入库。"}]}}` + "\n"
	if err := os.WriteFile(filepath.Join(projDir, "conv-1.jsonl"), []byte(lines), 0o644); err != nil {
		t.Fatalf("write workbuddy transcript: %v", err)
	}
	writeActiveWorkspacePresence(t, "harness:workbuddy", "conv-1", time.Now().Unix())

	st := runStatusJSON(t, dbPath)
	wb := harnessItem(t, st, "workbuddy")
	if wb["turns_scan_supported"] != true || wb["sessions_count"] != float64(1) || wb["local_turns_count"] != float64(2) || wb["unsynced_turns_count"] != float64(2) {
		t.Fatalf("workbuddy status before reconcile = %+v", wb)
	}

	res := run(t, "sync", "reconcile", "--db", dbPath, "--json")
	if res.code != 0 {
		t.Fatalf("sync reconcile exit=%d stdout=%s stderr=%s", res.code, res.stdout, res.stderr)
	}
	rep := jsonObject(t, res.stdout)
	if rep["target_harness"] != "workbuddy" || rep["episodic_imported_from_harness"] != float64(2) {
		t.Fatalf("workbuddy reconcile report = %+v", rep)
	}
	postSt, _ := rep["status"].(map[string]any)
	wbAfter := harnessItem(t, postSt, "workbuddy")
	if wbAfter["synced_turns_count"] != float64(2) || wbAfter["unsynced_turns_count"] != float64(0) {
		t.Fatalf("workbuddy status after reconcile = %+v", wbAfter)
	}
}

func TestSyncStatusReportsIncompleteTurnsAsScopeNotPending(t *testing.T) {
	env := newEnv(t)
	dbPath := filepath.Join(env.base, "multi-host.db")
	t.Setenv("ROLLING_MEMORY_HARNESS", "claude-code")
	for _, fixture := range []struct{ hostDir, session, text string }{
		{".claude", "claude", "first host pending"},
		{".codex", "codex", "second host pending"},
	} {
		var path string
		if fixture.hostDir == ".codex" {
			path = filepath.Join(os.Getenv("HOME"), fixture.hostDir, "sessions", fixture.session+".jsonl")
		} else {
			path = filepath.Join(os.Getenv("HOME"), fixture.hostDir, "projects", "demo", fixture.session+".jsonl")
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		body := `{"type":"user","message":{"role":"user","content":"` + fixture.text + `"}}` + "\n" +
			`{"type":"user","message":{"role":"user","content":"next ` + fixture.session + `"}}` + "\n"
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	payload := runStatusJSON(t, dbPath)
	layers, _ := payload["layers"].(map[string]any)
	epi, _ := layers["episodic"].(map[string]any)
	// A turn without confirmed completion is not importable by contract, so it is
	// a known scope exclusion rather than pending work or unknown coverage.
	if epi["harness_unsynced_turns"] != nil || epi["aligned"] != true || epi["alignment_known"] != true {
		t.Fatalf("multi-host status = %+v", epi)
	}
	if payload["total_pending_sync"].(float64) != 0 {
		t.Fatalf("incomplete turns counted as pending: %+v", payload)
	}
	for _, id := range []string{"claude-code", "codex"} {
		item := harnessItem(t, payload, id)
		if item["local_turns_count"] != float64(0) || item["incomplete_turns_count"] != float64(2) {
			t.Fatalf("%s status = %+v", id, item)
		}
	}
}

// TestSyncStatusScopesCountsToIngestBound locks the dashboard contract for a
// configured sync.since lower bound: history older than the bound is outside the
// vault's collection scope, so it is neither pending work nor an alignment gap.
// Only turns the importer would actually store drive the unsynced count.
func TestSyncStatusScopesCountsToIngestBound(t *testing.T) {
	env := newEnv(t)
	now := time.Now()
	t.Setenv("ROLLING_MEMORY_SYNC_SINCE", now.Add(-time.Hour).Format(time.RFC3339))
	path := filepath.Join(os.Getenv("HOME"), ".claude", "projects", "demo", "bounded.jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	oldTS := now.Add(-2 * time.Hour).Format(time.RFC3339)
	newTS := now.Add(-10 * time.Minute).Format(time.RFC3339)
	body := fmt.Sprintf(
		`{"timestamp":%q,"type":"user","message":{"role":"user","content":"bound old question"}}`+"\n"+
			`{"timestamp":%q,"type":"assistant","message":{"role":"assistant","stop_reason":"end_turn","content":"bound old answer"}}`+"\n"+
			`{"timestamp":%q,"type":"user","message":{"role":"user","content":"bound new question"}}`+"\n"+
			`{"timestamp":%q,"type":"assistant","message":{"role":"assistant","stop_reason":"end_turn","content":"bound new answer"}}`+"\n",
		oldTS, oldTS, newTS, newTS)
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	dbPath := filepath.Join(env.base, "bounded.db")
	st := runStatusJSON(t, dbPath)
	item := harnessItem(t, st, "claude-code")
	if item["local_turns_count"] != float64(2) || item["unsynced_turns_count"] != float64(2) || item["out_of_scope_turns_count"] != float64(2) {
		t.Fatalf("bounded status = %+v", item)
	}

	res := run(t, "sync", "reconcile", "--db", dbPath, "--harness", "claude-code", "--layer", "episodic", "--json")
	if res.code != 0 {
		t.Fatalf("reconcile exit=%d stdout=%s stderr=%s", res.code, res.stdout, res.stderr)
	}
	rep := jsonObject(t, res.stdout)
	if rep["episodic_imported_from_harness"] != float64(2) {
		t.Fatalf("bounded reconcile imported = %+v, want 2 in-bound units", rep["episodic_imported_from_harness"])
	}
	payload := runStatusJSON(t, dbPath)
	item = harnessItem(t, payload, "claude-code")
	layer := payload["layers"].(map[string]any)["episodic"].(map[string]any)
	if item["unsynced_turns_count"] != float64(0) || layer["aligned"] != true || layer["alignment_known"] != true {
		t.Fatalf("bounded status after reconcile = item %+v layer %+v", item, layer)
	}
}

func TestSyncStatusExcludedSubagentIsKnownScope(t *testing.T) {
	env := newEnv(t)
	home := os.Getenv("HOME")
	main := filepath.Join(home, ".claude", "projects", "demo", "main.jsonl")
	if err := os.MkdirAll(filepath.Dir(main), 0o700); err != nil {
		t.Fatal(err)
	}
	body := `{"type":"user","message":{"role":"user","content":"main question"}}` + "\n" +
		`{"type":"assistant","message":{"role":"assistant","stop_reason":"end_turn","content":"main answer"}}` + "\n"
	if err := os.WriteFile(main, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(env.base, "subagent.db")
	res := run(t, "sync", "reconcile", "--db", dbPath, "--harness", "claude-code", "--layer", "episodic", "--json")
	if res.code != 0 {
		t.Fatalf("reconcile exit=%d stdout=%s stderr=%s", res.code, res.stdout, res.stderr)
	}
	getLayer := func() map[string]any {
		t.Helper()
		payload := runStatusJSON(t, dbPath)
		layers, _ := payload["layers"].(map[string]any)
		layer, _ := layers["episodic"].(map[string]any)
		return layer
	}
	if layer := getLayer(); layer["alignment_known"] != true || layer["aligned"] != true {
		t.Fatalf("main session control = %+v", layer)
	}
	child := filepath.Join(home, ".claude", "projects", "demo", "main", "subagents", "agent-child.jsonl")
	if err := os.MkdirAll(filepath.Dir(child), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(child, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	payload := runStatusJSON(t, dbPath)
	layer := payload["layers"].(map[string]any)["episodic"].(map[string]any)
	// Subagent transcripts are a documented collection-scope limit, not an
	// unreadable source: the supported scope stays aligned and the exclusion is
	// reported as a count on the harness item.
	if layer["alignment_known"] != true || layer["aligned"] != true {
		t.Fatalf("excluded subagent poisoned supported-scope alignment: %+v", layer)
	}
	item := harnessItem(t, payload, "claude-code")
	if item["excluded_subagent_sources_count"] != float64(1) {
		t.Fatalf("excluded subagent count = %+v", item)
	}
}

// TestSyncStatusUnsupportedHostIsKnownScope locks the no-scanner boundary: a
// detected host without any local reader (Cursor / Windsurf / Cline) is
// explicitly out of automatic coverage. It must not hold the supported scope at
// "alignment unknown" forever; the harness item carries the capability flags and
// the panel labels it as "no automatic turn scanner".
func TestSyncStatusUnsupportedHostIsKnownScope(t *testing.T) {
	env := newEnv(t)
	home := os.Getenv("HOME")
	if err := os.MkdirAll(filepath.Join(home, ".cursor"), 0o700); err != nil {
		t.Fatal(err)
	}
	// The app bundle is the install evidence; the data directory alone is only
	// a leftover workspace. Each platform probes its own install shape, so the
	// fixture must create the candidate the detector actually looks for.
	switch runtime.GOOS {
	case "darwin":
		if err := os.MkdirAll(filepath.Join(home, "Applications", "Cursor.app"), 0o700); err != nil {
			t.Fatal(err)
		}
	case "windows":
		localAppData := os.Getenv("LOCALAPPDATA")
		if localAppData == "" {
			localAppData = filepath.Join(home, "AppData", "Local")
		}
		program := filepath.Join(localAppData, "Programs", "Cursor", "Cursor.exe")
		if err := os.MkdirAll(filepath.Dir(program), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(program, nil, 0o600); err != nil {
			t.Fatal(err)
		}
	default:
		desktop := filepath.Join(home, ".local", "share", "applications", "Cursor.desktop")
		if err := os.MkdirAll(filepath.Dir(desktop), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(desktop, nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	payload := runStatusJSON(t, filepath.Join(env.base, "cursor.db"))
	item := harnessItem(t, payload, "cursor")
	if item["detected"] != true || item["turns_scan_supported"] != false || item["sessions_scan_supported"] != false {
		t.Fatalf("cursor item = %+v", item)
	}
	layer := payload["layers"].(map[string]any)["episodic"].(map[string]any)
	if layer["alignment_known"] != true || layer["aligned"] != true {
		t.Fatalf("unsupported host poisoned alignment: %+v", layer)
	}
	if payload["total_pending_sync"].(float64) != 0 {
		t.Fatalf("unsupported host counted as pending: %+v", payload)
	}
}

// TestSyncStatusOpenCodeInventoryHasNoTurnClaims locks the split capability for
// OpenCode: sessions can be counted, but this build has no turn reader, so the
// item must not claim turn coverage or fabricate a 0/0 pending count.
func TestSyncStatusOpenCodeInventoryHasNoTurnClaims(t *testing.T) {
	env := newEnv(t)
	configDir := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "opencode")
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(os.Getenv("XDG_DATA_HOME"), "opencode", "opencode.db")
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o700); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE session (id TEXT, time_archived INTEGER, time_updated INTEGER);
		INSERT INTO session VALUES ('s1', 0, 1700000000000);`); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	payload := runStatusJSON(t, filepath.Join(env.base, "opencode.db"))
	item := harnessItem(t, payload, "opencode")
	if item["sessions_scan_supported"] != true || item["turns_scan_supported"] != false || item["sessions_count"] != float64(1) {
		t.Fatalf("opencode item = %+v", item)
	}
	if item["local_turns_count"] != float64(0) || item["unsynced_turns_count"] != float64(0) {
		t.Fatalf("opencode fabricated turn counts: %+v", item)
	}
	layer := payload["layers"].(map[string]any)["episodic"].(map[string]any)
	if layer["alignment_known"] != true || layer["aligned"] != true {
		t.Fatalf("opencode inventory poisoned alignment: %+v", layer)
	}
}

// TestSyncReconcileImportsDoubaoJSONLExport locks the documented Doubao path:
// the app's live history stays behind its authenticated service, and a local
// JSONL export in ~/Doubao/exports is a first-class harness transcript source.
func TestSyncReconcileImportsDoubaoJSONLExport(t *testing.T) {
	env := newEnv(t)
	home := os.Getenv("HOME")
	now := time.Now()
	t.Setenv("ROLLING_MEMORY_SYNC_SINCE", now.Add(-time.Hour).Format(time.RFC3339))
	export := filepath.Join(home, "Doubao", "exports", "conv-1.jsonl")
	if err := os.MkdirAll(filepath.Dir(export), 0o700); err != nil {
		t.Fatal(err)
	}
	ts := now.Add(-10 * time.Minute).Unix()
	body := fmt.Sprintf(
		`{"cwd":%q,"timestamp":%d,"type":"user","message":{"role":"user","content":"豆包导出的一轮对话"}}`+"\n"+
			`{"cwd":%q,"timestamp":%d,"type":"assistant","message":{"role":"assistant","stop_reason":"end_turn","content":"豆包导出的回答"}}`+"\n",
		filepath.Join(home, "Doubao"), ts, filepath.Join(home, "Doubao"), ts+1)
	if err := os.WriteFile(export, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(env.base, "doubao-export.db")
	st := runStatusJSON(t, dbPath)
	item := harnessItem(t, st, "doubao")
	if item["turns_scan_supported"] != true || item["sessions_count"] != float64(1) ||
		item["local_turns_count"] != float64(2) || item["unsynced_turns_count"] != float64(2) {
		t.Fatalf("doubao export status = %+v", item)
	}
	res := run(t, "sync", "reconcile", "--db", dbPath, "--harness", "doubao", "--layer", "episodic", "--json")
	if res.code != 0 {
		t.Fatalf("reconcile exit=%d stdout=%s stderr=%s", res.code, res.stdout, res.stderr)
	}
	rep := jsonObject(t, res.stdout)
	if rep["episodic_imported_from_harness"] != float64(2) {
		t.Fatalf("doubao export reconcile = %+v", rep)
	}
	after := harnessItem(t, runStatusJSON(t, dbPath), "doubao")
	if after["synced_turns_count"] != float64(2) || after["unsynced_turns_count"] != float64(0) {
		t.Fatalf("doubao export status after reconcile = %+v", after)
	}
}

func jsonNumberString(v int64) string {
	b, _ := json.Marshal(v)
	return string(b)
}
