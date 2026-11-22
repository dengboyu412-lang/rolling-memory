// plugin: rolling-memory v0.0.58
package rollingmemory

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

// engineCall 记录一次 engine 内部通道调用（op、参数与请求 JSON）。
type engineCall struct {
	op      string
	args    []string
	request map[string]any
}

// stubEngineRunner 用桩替换 engineCommandRunner，并保证 resolveBinary 解析得到占位二进制：
// 生产实现（exec 产品二进制）在单测中绝不执行，所有请求/应答断言都落在桩上。
func stubEngineRunner(t *testing.T, reply func(call engineCall) (string, error)) *[]engineCall {
	t.Helper()
	shim := filepath.Join(t.TempDir(), "rolling-memory-stub")
	if err := os.WriteFile(shim, []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
		t.Fatalf("write engine shim: %v", err)
	}
	t.Setenv("ROLLING_MEMORY_BIN", shim)
	var calls []engineCall
	previous := engineCommandRunner
	t.Cleanup(func() { engineCommandRunner = previous })
	engineCommandRunner = func(_ context.Context, bin, op string, args []string, request []byte) ([]byte, error) {
		if strings.TrimSpace(bin) == "" {
			t.Errorf("engine runner called without a resolved binary")
		}
		var payload map[string]any
		if len(request) > 0 {
			_ = json.Unmarshal(request, &payload)
		}
		call := engineCall{op: op, args: append([]string(nil), args...), request: payload}
		calls = append(calls, call)
		out, err := reply(call)
		return []byte(out), err
	}
	return &calls
}

// trapDaemonSocket 把 ROLLING_MEMORY_DAEMON_SOCKET 指向一个只计数的 Unix socket。
// 桩替换 engineCommandRunner 后，对它出现的任何连接都意味着控制台内又出现了裸 socket
// 客户端（未核验连接）——测试据此对回潮零容忍。
func trapDaemonSocket(t *testing.T) *atomic.Int64 {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "rm-admin-trap-")
	if err != nil {
		t.Fatalf("mkdir trap dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	path := filepath.Join(dir, "engine.sock")
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatalf("listen trap socket: %v", err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	var connections atomic.Int64
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			connections.Add(1)
			_ = conn.Close()
		}
	}()
	t.Setenv("ROLLING_MEMORY_DAEMON_SOCKET", path)
	return &connections
}

// engineReplyJSON 组装一个 engine 通道应答。
func engineReplyJSON(t *testing.T, payload map[string]any) string {
	t.Helper()
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal engine reply: %v", err)
	}
	return string(raw)
}

func engineRequestItems(t *testing.T, call engineCall) []map[string]any {
	t.Helper()
	rawItems, _ := call.request["items"].([]any)
	items := make([]map[string]any, 0, len(rawItems))
	for _, raw := range rawItems {
		item, _ := raw.(map[string]any)
		items = append(items, item)
	}
	return items
}

// TestBackgroundChunkRowUsesDaemonBody 锁定 L1 分块列表行的正文口径：
// 批量预热经 `engine documents` 一次解析并把源文件正文写进 content，
// 未裁决的行（源文件被移动/删除）保留来源位置预览并显式标记 body_unavailable。
func TestBackgroundChunkRowUsesDaemonBody(t *testing.T) {
	readableID := "bg:readable#0"
	goneID := "bg:gone#0"
	connections := trapDaemonSocket(t)
	calls := stubEngineRunner(t, func(call engineCall) (string, error) {
		if call.op != "documents" {
			t.Errorf("unexpected engine op %q", call.op)
		}
		docs := []map[string]any{}
		for _, item := range engineRequestItems(t, call) {
			id := asString(item["unit_id"])
			if id == readableID {
				docs = append(docs, map[string]any{"unit_id": id, "raw_text": "# Emergency Failover Runbook\nExecute command `aurora-ctl drain --graceful`."})
				continue
			}
			docs = append(docs, map[string]any{"unit_id": id, "error": "source unavailable"})
		}
		return engineReplyJSON(t, map[string]any{"ok": true, "docs": docs}), nil
	})
	home := t.TempDir()

	readableRow := map[string]any{
		"src": "chunk", "id": readableID, "layer": "background", "status": "active",
		"rel_path": "docs/runbook.md", "anchor": "L1-7", "abs_path": "/ws/docs/runbook.md", "occurred_at": 1762380000,
	}
	goneRow := map[string]any{
		"src": "chunk", "id": goneID, "layer": "background", "status": "active",
		"rel_path": "docs/retired.md", "anchor": "L1-3", "abs_path": "/ws/docs/retired.md", "occurred_at": 1762380000,
	}
	rows := []map[string]any{readableRow, goneRow}
	warmHydrateLocalDocumentsBatch(home, rows)

	row := layerRowFromSQL(readableRow, home)
	if row.BodyUnavailable || !strings.Contains(row.Content, "aurora-ctl drain --graceful") {
		t.Fatalf("readable chunk row = %+v, want the daemon-resolved chunk body", row)
	}
	if row.Title != "docs/runbook.md #L1-7" {
		t.Fatalf("chunk row title = %q, want the file location", row.Title)
	}
	rowGone := layerRowFromSQL(goneRow, home)
	if !rowGone.BodyUnavailable || rowGone.Content != "[docs/retired.md#L1-3] (/ws/docs/retired.md)" {
		t.Fatalf("source-gone chunk row = %+v, want location preview flagged body_unavailable", rowGone)
	}
	if got := len(*calls); got != 1 {
		t.Fatalf("engine calls = %d, want a single warm batch", got)
	}
	// 缓存命中后重复构造行不得再打 engine 通道。
	_ = layerRowFromSQL(readableRow, home)
	if got := len(*calls); got != 1 {
		t.Fatalf("cached chunk row must not call the engine again, calls = %d", got)
	}
	if got := connections.Load(); got != 0 {
		t.Fatalf("raw socket connections = %d, want 0", got)
	}
}

// TestLayersGetBackgroundChunkReturnsDaemonBody 锁定 L1 分块详情（layers/get）：
// 正文可读时 content 为源文件正文且不带不可读标记；读取引擎不可用时退化为位置预览 + body_unavailable。
func TestLayersGetBackgroundChunkReturnsDaemonBody(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "bg_detail.db")
	db, err := openWritableVaultDB(dbPath)
	if err != nil {
		t.Fatalf("openWritableVaultDB: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO rm_bg_chunks(unit_id, abs_path, rel_path, chunk_index, anchor, occurred_at, address_uri)
		VALUES ('bg:detail#0', '/ws/docs/plan.md', 'docs/plan.md', 0, 'L1-9', 1762380000, 'C6:L1:BG#00')`); err != nil {
		_ = db.Close()
		t.Fatalf("insert bg_chunk: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close vault: %v", err)
	}
	t.Setenv("ROLLING_MEMORY_DB", dbPath)
	// 固定 HOME：分块正文缓存按 (flavor, home, unit_id) 分键，本测试两个阶段必须落在同一键上，
	// 阶段 ② 才会真正重建「引擎不可用」路径。
	home := t.TempDir()
	t.Setenv("HOME", home)

	decodeRow := func(raw string) ThreeLayerRow {
		t.Helper()
		var envelope struct {
			Code int `json:"code"`
			Data struct {
				Row ThreeLayerRow `json:"row"`
			} `json:"data"`
		}
		if err := json.Unmarshal([]byte(raw), &envelope); err != nil {
			t.Fatalf("decode layers/get: %v", err)
		}
		if envelope.Code != 0 {
			t.Fatalf("layers/get returned business error: %s", raw)
		}
		return envelope.Data.Row
	}

	// ① 引擎通道可用：content 为分块正文。
	connections := trapDaemonSocket(t)
	stubEngineRunner(t, func(call engineCall) (string, error) {
		docs := []map[string]any{}
		for _, item := range engineRequestItems(t, call) {
			docs = append(docs, map[string]any{"unit_id": asString(item["unit_id"]), "raw_text": "# Deployment Plan\nStep 1: drain the aurora gateway."})
		}
		return engineReplyJSON(t, map[string]any{"ok": true, "docs": docs}), nil
	})
	row := decodeRow(callPluginHandler(t, layersGetHandler,
		"/admin/plugin/rolling-memory/layers/get", `{"id":"bg:detail#0"}`))
	if row.BodyUnavailable || !strings.Contains(row.Content, "drain the aurora gateway") {
		t.Fatalf("layers/get chunk row = %+v, want the canonical chunk body", row)
	}
	if row.RelPath != "docs/plan.md" || row.Anchor != "L1-9" {
		t.Fatalf("layers/get chunk row lost its source location: %+v", row)
	}

	// ② 读取引擎不可用（守护路由停用）：位置预览 + body_unavailable，绝不冒充正文。
	// 先清掉 ① 写入的正文缓存，否则这里会命中缓存拿不到「不可读」路径。
	hydrateMu.Lock()
	delete(hydrateCache, hydrateCacheKey(hydrateFlavorLocalDoc, home, "bg:detail#0"))
	hydrateMu.Unlock()
	t.Setenv("ROLLING_MEMORY_DAEMON", "0")
	row = decodeRow(callPluginHandler(t, layersGetHandler,
		"/admin/plugin/rolling-memory/layers/get", `{"id":"bg:detail#0"}`))
	if !row.BodyUnavailable || row.Content != "[docs/plan.md#L1-9] (/ws/docs/plan.md)" {
		t.Fatalf("engine-down chunk row = %+v, want location preview flagged body_unavailable", row)
	}
	if got := connections.Load(); got != 0 {
		t.Fatalf("raw socket connections = %d, want 0", got)
	}
}

// TestMemoryDetailBackgroundChunkReturnsDaemonBody 锁定检索调试台详情（memories/detail）的
// L1 分块分支与列表同一口径：正文可读时给出正文，不可读时以位置预览兜底。
func TestMemoryDetailBackgroundChunkReturnsDaemonBody(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "bg_mem_detail.db")
	db, err := openWritableVaultDB(dbPath)
	if err != nil {
		t.Fatalf("openWritableVaultDB: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO rm_bg_chunks(unit_id, abs_path, rel_path, chunk_index, anchor, occurred_at, address_uri)
		VALUES ('bg:mem#0', '/ws/docs/notes.md', 'docs/notes.md', 0, 'L1-5', 1762380000, 'C6:L1:BG#01')`); err != nil {
		_ = db.Close()
		t.Fatalf("insert bg_chunk: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close vault: %v", err)
	}
	t.Setenv("ROLLING_MEMORY_DB", dbPath)

	trapDaemonSocket(t)
	stubEngineRunner(t, func(call engineCall) (string, error) {
		docs := []map[string]any{}
		for _, item := range engineRequestItems(t, call) {
			docs = append(docs, map[string]any{"unit_id": asString(item["unit_id"]), "raw_text": "Meeting notes: rotate the optical transceivers on Friday."})
		}
		return engineReplyJSON(t, map[string]any{"ok": true, "docs": docs}), nil
	})
	home := t.TempDir()
	t.Setenv("HOME", home)

	det, err := queryMemoryDetailFromDB(dbPath, "bg:mem#0")
	if err != nil {
		t.Fatalf("queryMemoryDetailFromDB: %v", err)
	}
	if !strings.Contains(det.TraceText, "rotate the optical transceivers") ||
		!strings.Contains(det.Item.Object, "rotate the optical transceivers") {
		t.Fatalf("memory detail must carry the canonical chunk body: %+v", det)
	}
	if det.TraceAnchor != "[docs/notes.md#L1-5] (/ws/docs/notes.md)" {
		t.Fatalf("memory detail trace anchor = %q, want the source location", det.TraceAnchor)
	}
}

// TestHydratedPointerPrefersDaemonCanonicalText verifies every read goes through the
// verified engine channel and reverifies the source per request.
func TestHydratedPointerPrefersDaemonCanonicalText(t *testing.T) {
	ref := "@ref:zcode:sess_fixture:msg_turn_1#1"
	connections := trapDaemonSocket(t)
	calls := stubEngineRunner(t, func(call engineCall) (string, error) {
		texts := []map[string]any{}
		for _, item := range engineRequestItems(t, call) {
			entry := map[string]any{"doc_id": asString(item["doc_id"]), "ref": asString(item["ref"])}
			if asString(item["ref"]) == ref {
				entry["actual_ref"] = ref
				entry["text"] = "[assistant] 规范分段正文"
				entry["verified"] = true
			}
			texts = append(texts, entry)
		}
		return engineReplyJSON(t, map[string]any{"ok": true, "texts": texts}), nil
	})
	home := t.TempDir()

	text, ok := hydratedEpisodicPointerLive(home, filepath.Join(home, "memory.db"), "doc-one", ref)
	if !ok || text != "[assistant] 规范分段正文" {
		t.Fatalf("engine hydration = (%q, %v), want canonical text", text, ok)
	}
	if got := len(*calls); got != 1 {
		t.Fatalf("engine calls = %d, want 1", got)
	}
	if _, ok := hydratedEpisodicPointerLive(home, filepath.Join(home, "memory.db"), "doc-one", ref); !ok {
		t.Fatalf("second verified read failed")
	}
	if got := len(*calls); got != 2 {
		t.Fatalf("each read must reverify the source, calls = %d", got)
	}
	if (*calls)[0].op != "hydrate" {
		t.Fatalf("hydration op = %q", (*calls)[0].op)
	}
	items := engineRequestItems(t, (*calls)[0])
	if len(items) != 1 || asString(items[0]["doc_id"]) != "doc-one" || asString(items[0]["ref"]) != ref {
		t.Fatalf("request identity = %+v", items)
	}
	if got := connections.Load(); got != 0 {
		t.Fatalf("raw socket connections = %d, want 0", got)
	}
}

// TestWarmHydrateBatchVerifiesEachDocument tests mixed, duplicate, and missing identities.
func TestWarmHydrateBatchVerifiesEachDocument(t *testing.T) {
	ref := "@ref:zcode:sess:msg#0"
	trapDaemonSocket(t)
	calls := stubEngineRunner(t, func(call engineCall) (string, error) {
		texts := []map[string]any{}
		for _, item := range engineRequestItems(t, call) {
			texts = append(texts, map[string]any{"doc_id": asString(item["doc_id"]), "ref": ref, "actual_ref": ref, "text": "[user] verified", "verified": true})
		}
		return engineReplyJSON(t, map[string]any{"ok": true, "texts": texts}), nil
	})
	home := t.TempDir()
	rows := []map[string]any{
		{"id": "doc-a", "content": ref},
		{"id": "doc-a", "content": ref},
		{"id": "doc-b", "content": ref},
		{"content": ref},
		{"id": "doc-missing", "content": "@ref:jsonl:/missing:0#0"},
	}
	warmHydratePointersBatch(home, filepath.Join(home, "memory.db"), rows)
	if len(*calls) != 1 {
		t.Fatalf("calls = %d", len(*calls))
	}
	for i := 0; i < 3; i++ {
		if text, ok := hydratedPointerRowText(rows[i]); !ok || text != "[user] verified" {
			t.Fatalf("row %d = (%q, %v)", i, text, ok)
		}
	}
	for _, row := range rows[3:] {
		if text, ok := hydratedPointerRowText(row); ok || text != "" {
			t.Fatalf("unavailable row = %q", text)
		}
	}
	items := engineRequestItems(t, (*calls)[0])
	if len(items) != 3 {
		t.Fatalf("items = %+v, want distinct doc/ref pairs", items)
	}
}

func TestHydrateFailsClosedWhenDaemonMissing(t *testing.T) {
	home := t.TempDir()
	t.Setenv("ROLLING_MEMORY_DAEMON_SOCKET", filepath.Join(home, "missing.sock"))
	calls := stubEngineRunner(t, func(engineCall) (string, error) {
		t.Error("engine channel must not run when the daemon socket is missing")
		return "", nil
	})
	ref := "@ref:jsonl:" + filepath.Join(home, "transcript.jsonl") + ":0#0"
	if err := os.WriteFile(filepath.Join(home, "transcript.jsonl"), []byte(`{"type":"user","message":{"role":"user","content":"secret"}}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if text, ok := hydratedEpisodicPointerLive(home, filepath.Join(home, "memory.db"), "doc", ref); ok || text != "" {
		t.Fatalf("missing daemon = (%q, %v)", text, ok)
	}
	if len(*calls) != 0 {
		t.Fatalf("engine calls = %d, want 0", len(*calls))
	}
}

func TestHydrateDaemonRejectsUntrustedResults(t *testing.T) {
	ref := "@ref:zcode:session:message#0"
	trapDaemonSocket(t)
	calls := stubEngineRunner(t, func(call engineCall) (string, error) {
		var results []map[string]any
		for _, item := range engineRequestItems(t, call) {
			result := map[string]any{"doc_id": asString(item["doc_id"]), "ref": asString(item["ref"]), "actual_ref": asString(item["ref"]), "text": "secret", "verified": true}
			switch asString(item["doc_id"]) {
			case "good":
			case "unverified":
				delete(result, "verified")
			case "error":
				result["error"] = "mismatch"
			case "wrong-id":
				result["doc_id"] = "other"
			case "wrong-ref":
				result["ref"] = "@ref:zcode:other:msg#0"
			case "no-actual":
				delete(result, "actual_ref")
			case "relocated":
				result["actual_ref"] = "@ref:zcode:session:message#1"
			case "duplicate":
				results = append(results, result)
			}
			results = append(results, result)
		}
		return engineReplyJSON(t, map[string]any{"ok": true, "texts": results}), nil
	})
	home := t.TempDir()
	dbPath := filepath.Join(home, "vault.db")
	if got, ok := hydrateRefsViaDaemon(home, dbPath, nil); ok || len(got) != 0 || len(*calls) != 0 {
		t.Fatalf("empty batch = %v, %v", got, ok)
	}
	ids := []string{"good", "unverified", "error", "wrong-id", "wrong-ref", "no-actual", "relocated", "duplicate", ""}
	items := make([]hydrateRef, 0, len(ids))
	for _, id := range ids {
		items = append(items, hydrateRef{DocID: id, Ref: ref})
	}
	got, ok := hydrateRefsViaDaemon(home, dbPath, items)
	if !ok || len(got) != 1 || got[hydrateRef{DocID: "good", Ref: ref}] != "secret" {
		t.Fatalf("verified results = %+v, handled=%v", got, ok)
	}
	if len(*calls) != 1 {
		t.Fatalf("batch calls = %d", len(*calls))
	}
}

func TestHydrateRevalidatesChangedSourceAndIdentity(t *testing.T) {
	ref := "@ref:zcode:session:message#0"
	trapDaemonSocket(t)
	var valid atomic.Bool
	valid.Store(true)
	stubEngineRunner(t, func(call engineCall) (string, error) {
		var results []map[string]any
		for _, item := range engineRequestItems(t, call) {
			if asString(item["doc_id"]) == "doc-a" && asString(item["ref"]) == ref && valid.Load() {
				results = append(results, map[string]any{"doc_id": asString(item["doc_id"]), "ref": asString(item["ref"]), "actual_ref": ref, "text": "indexed original", "verified": true})
			} else {
				results = append(results, map[string]any{"doc_id": asString(item["doc_id"]), "ref": asString(item["ref"]), "error": "content or identity mismatch"})
			}
		}
		return engineReplyJSON(t, map[string]any{"ok": true, "texts": results}), nil
	})
	home := t.TempDir()
	dbPath := filepath.Join(home, "memory.db")
	if text, ok := hydratedEpisodicPointerLive(home, dbPath, "doc-a", ref); !ok || text != "indexed original" {
		t.Fatalf("valid source = (%q, %v)", text, ok)
	}
	for _, item := range []hydrateRef{{DocID: "doc-b", Ref: ref}, {DocID: "doc-a", Ref: ref + "wrong"}, {Ref: ref}} {
		if text, ok := hydratedEpisodicPointerLive(home, dbPath, item.DocID, item.Ref); ok || text != "" {
			t.Fatalf("wrong identity = (%q, %v)", text, ok)
		}
	}
	valid.Store(false)
	if text, ok := hydratedEpisodicPointerLive(home, dbPath, "doc-a", ref); ok || text != "" {
		t.Fatalf("changed source reused text = (%q, %v)", text, ok)
	}
	t.Setenv("ROLLING_MEMORY_DAEMON", "0")
	if text, ok := hydratedEpisodicPointerLive(home, dbPath, "doc-a", ref); ok || text != "" {
		t.Fatalf("disabled daemon reused text = (%q, %v)", text, ok)
	}
}

func TestCachedROIBundleRevalidatesEpisodicEvidence(t *testing.T) {
	ref := "@ref:zcode:session:message#0"
	trapDaemonSocket(t)
	var valid atomic.Bool
	valid.Store(true)
	stubEngineRunner(t, func(call engineCall) (string, error) {
		var results []map[string]any
		for _, item := range engineRequestItems(t, call) {
			if valid.Load() && asString(item["doc_id"]) == "doc-a" && asString(item["ref"]) == ref {
				results = append(results, map[string]any{"doc_id": asString(item["doc_id"]), "ref": asString(item["ref"]), "actual_ref": ref, "text": "authenticated source", "verified": true})
			} else {
				results = append(results, map[string]any{"doc_id": asString(item["doc_id"]), "ref": asString(item["ref"]), "error": "source changed"})
			}
		}
		return engineReplyJSON(t, map[string]any{"ok": true, "texts": results}), nil
	})
	t.Setenv("HOME", t.TempDir())
	memoryPath := filepath.Join(t.TempDir(), "vault.db")
	bundle := vaultAnalyticsBundle{
		vault:         &roiFactsVault{},
		topCitedUnits: []roiTopCitedUnit{{UnitID: "doc-a", IsZeroCopyRef: true, storedRef: ref, fallbackPreview: "citation preview", FullText: "old cached text", IsHydrated: true}},
		noeticStream:  []roiNoeticEvent{{CitedUnits: []roiCitedUnitDetail{{UnitID: "doc-a", StoredText: ref, IsZeroCopyRef: true, FullText: "old cached text", IsHydrated: true}}}},
	}
	roiVaultBundleMu.Lock()
	roiVaultBundleCache[memoryPath] = &roiVaultBundleEntry{sig: roiVaultBundleSignature(memoryPath, time.Now()), bundle: bundle}
	roiVaultBundleMu.Unlock()
	t.Cleanup(func() { roiVaultBundleMu.Lock(); delete(roiVaultBundleCache, memoryPath); roiVaultBundleMu.Unlock() })
	first := readROIFactsVaultBundle(memoryPath)
	if first.topCitedUnits[0].FullText != "authenticated source" || first.noeticStream[0].CitedUnits[0].FullText != "authenticated source" {
		t.Fatalf("verified bundle = %+v %+v", first.topCitedUnits, first.noeticStream)
	}
	valid.Store(false)
	second := readROIFactsVaultBundle(memoryPath)
	if second.topCitedUnits[0].FullText != "" || second.topCitedUnits[0].IsHydrated || second.topCitedUnits[0].Preview != "citation preview" || second.noeticStream[0].CitedUnits[0].FullText != "" || second.noeticStream[0].CitedUnits[0].IsHydrated {
		t.Fatalf("stale bundle = %+v %+v", second.topCitedUnits, second.noeticStream)
	}
	if first.topCitedUnits[0].FullText != "authenticated source" {
		t.Fatal("revalidation mutated previous response")
	}
}

func TestPointerRowSnapshotDoesNotCacheVerifiedText(t *testing.T) {
	home := t.TempDir()
	dbPath := filepath.Join(home, "vault.db")
	ref := "@ref:zcode:session:message#0"
	seedVault(t, dbPath, `INSERT INTO rm_units(doc_id, layer, occurred_at, raw_text, address_uri) VALUES ('doc-a', 'episodic', 1, '`+ref+`', 'addr')`)
	trapDaemonSocket(t)
	stubEngineRunner(t, func(call engineCall) (string, error) {
		texts := []map[string]any{}
		for _, item := range engineRequestItems(t, call) {
			texts = append(texts, map[string]any{"doc_id": asString(item["doc_id"]), "ref": asString(item["ref"]), "actual_ref": asString(item["ref"]), "text": "verified", "verified": true})
		}
		return engineReplyJSON(t, map[string]any{"ok": true, "texts": texts}), nil
	})
	first, err := cachedVaultPointerDocRows(dbPath, layerListQuery{})
	if err != nil || len(first) != 1 {
		t.Fatalf("pointer rows = %+v, %v", first, err)
	}
	warmHydratePointersBatch(home, dbPath, first)
	if text, ok := hydratedPointerRowText(first[0]); !ok || text != "verified" {
		t.Fatalf("first row = %q, %v", text, ok)
	}
	second, err := cachedVaultPointerDocRows(dbPath, layerListQuery{})
	if err != nil || len(second) != 1 {
		t.Fatalf("second rows = %+v, %v", second, err)
	}
	if text, ok := hydratedPointerRowText(second[0]); ok || text != "" {
		t.Fatalf("snapshot retained text = %q, %v", text, ok)
	}
}

func TestLargePointerSearchFindsLateVerifiedMatch(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dbPath := filepath.Join(home, "vault.db")
	seedVault(t, dbPath)
	db, err := sql.Open("sqlite", "file:"+dbPath)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 513; i++ {
		if _, err := tx.Exec(`INSERT INTO rm_units(doc_id, layer, occurred_at, raw_text, address_uri) VALUES (?, 'episodic', ?, ?, '')`, fmt.Sprintf("doc-%03d", i), i, fmt.Sprintf("@ref:zcode:session:msg-%03d#0", i)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
	targetRef := "@ref:zcode:session:msg-512#0"
	trapDaemonSocket(t)
	calls := stubEngineRunner(t, func(call engineCall) (string, error) {
		texts := []map[string]any{}
		for _, item := range engineRequestItems(t, call) {
			text := "ordinary"
			if asString(item["ref"]) == targetRef {
				text = "needle verified"
			}
			texts = append(texts, map[string]any{"doc_id": asString(item["doc_id"]), "ref": asString(item["ref"]), "actual_ref": asString(item["ref"]), "text": text, "verified": true})
		}
		return engineReplyJSON(t, map[string]any{"ok": true, "texts": texts}), nil
	})
	t.Setenv("ROLLING_MEMORY_DB", dbPath)
	rows, total, err := queryLayerRows(dbPath, layerListQuery{layer: episodicLayer, keyword: "needle", limit: 20}, home)
	if err != nil || total != 1 || len(rows) != 1 || rows[0].ID != "doc-512" {
		t.Fatalf("large search = %+v, %d, %v", rows, total, err)
	}
	if len(*calls) != 9 {
		t.Fatalf("batch calls=%d, want 9", len(*calls))
	}
	memories, err := queryMemoriesFromDB(dbPath, MemoriesQueryRequest{Layer: episodicLayer, Keyword: "needle", Page: 1, PageSize: 20})
	if err != nil || memories.Total != 1 || len(memories.Items) != 1 || memories.Items[0].ID != "doc-512" {
		t.Fatalf("large memories search = %+v, %v", memories, err)
	}
}

func TestListSearchAndDashboardDoNotReuseStalePointerText(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dbPath := filepath.Join(home, "vault.db")
	t.Setenv("ROLLING_MEMORY_DB", dbPath)
	ref := "@ref:zcode:session:message#0"
	seedVault(t, dbPath, `INSERT INTO rm_units(doc_id, layer, occurred_at, raw_text, address_uri) VALUES ('doc-a', 'episodic', 1700000000, '`+ref+`', 'addr')`)
	trapDaemonSocket(t)
	var valid atomic.Bool
	valid.Store(true)
	stubEngineRunner(t, func(call engineCall) (string, error) {
		var results []map[string]any
		for _, item := range engineRequestItems(t, call) {
			if valid.Load() && asString(item["doc_id"]) == "doc-a" && asString(item["ref"]) == ref {
				results = append(results, map[string]any{"doc_id": asString(item["doc_id"]), "ref": asString(item["ref"]), "actual_ref": ref, "text": "unique searchable source", "verified": true})
			} else {
				results = append(results, map[string]any{"doc_id": asString(item["doc_id"]), "ref": asString(item["ref"]), "error": "source changed"})
			}
		}
		return engineReplyJSON(t, map[string]any{"ok": true, "texts": results}), nil
	})
	query := layerListQuery{layer: episodicLayer, keyword: "searchable", limit: 20}
	rows, total, err := queryLayerRows(dbPath, query, home)
	if err != nil || total != 1 || len(rows) != 1 || rows[0].Content != "unique searchable source" {
		t.Fatalf("initial search = %+v, %d, %v", rows, total, err)
	}
	cached := DashboardResponse{RecentAssertions: []RecentAssertion{{ID: "doc-a", Rendered: ref}}}
	if got := hydrateDashboardRecent(home, dbPath, cached); got.RecentAssertions[0].Rendered != "unique searchable source" {
		t.Fatalf("dashboard = %+v", got.RecentAssertions)
	}
	valid.Store(false)
	rows, total, err = queryLayerRows(dbPath, query, home)
	if err != nil || total != 0 || len(rows) != 0 {
		t.Fatalf("stale search = %+v, %d, %v", rows, total, err)
	}
	rows, total, err = queryLayerRows(dbPath, layerListQuery{layer: episodicLayer, limit: 20}, home)
	if err != nil || total != 1 || len(rows) != 1 || !rows[0].BodyUnavailable || rows[0].Content != vaultRefGapMarker {
		t.Fatalf("stale listing = %+v, %d, %v", rows, total, err)
	}
	if got := hydrateDashboardRecent(home, dbPath, cached); got.RecentAssertions[0].Rendered != vaultRefGapMarker {
		t.Fatalf("stale dashboard = %+v", got.RecentAssertions)
	}
	if cached.RecentAssertions[0].Rendered != ref {
		t.Fatal("dashboard snapshot was mutated")
	}
}

// TestTerminalStreamLinesHydratePointers 锁定 Live Console 终端回显：
// 最近写入单元的 `@ref:` 指针必须展示水合后的正文，而不是指针字符串本身。
func TestTerminalStreamLinesHydratePointers(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	ref := "@ref:zcode:sess_term:msg_term_1#0"
	trapDaemonSocket(t)
	stubEngineRunner(t, func(call engineCall) (string, error) {
		texts := []map[string]any{}
		for _, item := range engineRequestItems(t, call) {
			if asString(item["ref"]) == ref {
				texts = append(texts, map[string]any{"doc_id": asString(item["doc_id"]), "ref": ref, "actual_ref": ref, "text": "[user] 终端回显正文", "verified": true})
				continue
			}
			texts = append(texts, map[string]any{"doc_id": asString(item["doc_id"]), "ref": asString(item["ref"]), "error": "unavailable"})
		}
		return engineReplyJSON(t, map[string]any{"ok": true, "texts": texts}), nil
	})

	dbPath := filepath.Join(t.TempDir(), "memory.db")
	db, err := sql.Open("sqlite", "file:"+dbPath)
	if err != nil {
		t.Fatalf("open vault fixture: %v", err)
	}
	if _, err := db.Exec(`CREATE TABLE rm_units (doc_id TEXT, layer TEXT, occurred_at INTEGER, address_uri TEXT, raw_text TEXT);`); err != nil {
		t.Fatalf("create fixture: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO rm_units (doc_id, layer, occurred_at, address_uri, raw_text) VALUES ('ep_term_1', 'episodic', 1700000000, 'addr-1', ?);`, ref); err != nil {
		t.Fatalf("seed fixture: %v", err)
	}
	_ = db.Close()

	lines := buildTerminalStreamLines(dbPath, filepath.Join(t.TempDir(), "missing.log"), 4)
	for _, line := range lines {
		if line.ID != "unit-ep_term_1" {
			continue
		}
		if strings.Contains(line.Text, "@ref:") {
			t.Fatalf("terminal echo still prints the raw pointer: %q", line.Text)
		}
		if !strings.Contains(line.Text, "终端回显正文") {
			t.Fatalf("terminal echo missing hydrated text: %q", line.Text)
		}
		return
	}
	t.Fatalf("terminal stream lines did not include the seeded unit: %+v", lines)
}

// TestGraphDisplayTextHydratesPointers 锁定图谱节点摘要/跨度：
// 指针行取规范水合正文，非指针行维持「蒸馏优先、原文兜底」口径。
func TestGraphDisplayTextHydratesPointers(t *testing.T) {
	ref := "@ref:zcode:sess_graph:msg_graph_1#1"
	trapDaemonSocket(t)
	stubEngineRunner(t, func(call engineCall) (string, error) {
		texts := []map[string]any{}
		for _, item := range engineRequestItems(t, call) {
			if asString(item["ref"]) == ref {
				texts = append(texts, map[string]any{"doc_id": asString(item["doc_id"]), "ref": ref, "actual_ref": ref, "text": "[assistant] 图谱规范正文", "verified": true})
				continue
			}
			texts = append(texts, map[string]any{"doc_id": asString(item["doc_id"]), "ref": asString(item["ref"]), "error": "unavailable"})
		}
		return engineReplyJSON(t, map[string]any{"ok": true, "texts": texts}), nil
	})
	home := t.TempDir()

	txt, span := hydrateEpisodicDisplayText(home, filepath.Join(home, "memory.db"), "doc-one", ref, "")
	if txt != "[assistant] 图谱规范正文" || span != "[assistant] 图谱规范正文" {
		t.Fatalf("pointer row display = (%q, %q), want hydrated text", txt, span)
	}
	if txt, span := hydrateEpisodicDisplayText(home, filepath.Join(home, "memory.db"), "doc-one", "plain text", "distilled text"); txt != "distilled text" || span != "plain text" {
		t.Fatalf("plain row display = (%q, %q), want distilled/raw passthrough", txt, span)
	}
}

// TestVaultFollowupDocsWarmUncachedPointers 锁定采纳审计语料：
// 冷缓存的零拷贝指针行必须先走规范批量水合再入语料，早期实现只读缓存会把这行静默丢掉。
func TestVaultFollowupDocsWarmUncachedPointers(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	ref := "@ref:zcode:sess_follow:msg_follow_1#1"
	trapDaemonSocket(t)
	stubEngineRunner(t, func(call engineCall) (string, error) {
		texts := []map[string]any{}
		for _, item := range engineRequestItems(t, call) {
			if asString(item["ref"]) == ref {
				texts = append(texts, map[string]any{"doc_id": asString(item["doc_id"]), "ref": ref, "actual_ref": ref, "text": "[assistant] 审计语料正文", "verified": true})
				continue
			}
			texts = append(texts, map[string]any{"doc_id": asString(item["doc_id"]), "ref": asString(item["ref"]), "error": "unavailable"})
		}
		return engineReplyJSON(t, map[string]any{"ok": true, "texts": texts}), nil
	})

	dbPath := filepath.Join(t.TempDir(), "memory.db")
	db, err := sql.Open("sqlite", "file:"+dbPath)
	if err != nil {
		t.Fatalf("open vault fixture: %v", err)
	}
	defer func() { _ = db.Close() }()
	if _, err := db.Exec(`CREATE TABLE rm_units (doc_id TEXT, layer TEXT, occurred_at INTEGER, address_uri TEXT, raw_text TEXT, sentences_json TEXT, locus TEXT);`); err != nil {
		t.Fatalf("create fixture: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO rm_units (doc_id, layer, occurred_at, address_uri, raw_text, sentences_json, locus) VALUES ('ep_follow_1', 'episodic', 1700000200, 'addr-f', ?, '[]', 'locus-f');`, ref); err != nil {
		t.Fatalf("seed fixture: %v", err)
	}

	rows, _ := loadVaultFollowupDocs(context.Background(), db, t.TempDir(), dbPath, 0)
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want the uncached pointer row to survive hydration", len(rows))
	}
	if rows[0].text != "[assistant] 审计语料正文" || rows[0].docID != "ep_follow_1" {
		t.Fatalf("row = %+v, want canonical hydrated text", rows[0])
	}
	if _, err := db.Exec(`UPDATE rm_units SET sentences_json = '["unverified fallback"]' WHERE doc_id = 'ep_follow_1'`); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ROLLING_MEMORY_DAEMON", "0")
	rows, _ = loadVaultFollowupDocs(context.Background(), db, t.TempDir(), dbPath, 0)
	if len(rows) != 0 {
		t.Fatalf("unverified pointer entered followup corpus: %+v", rows)
	}
}

// TestUnresolvableRefShowsGapMarker 锁定诚实占位：无法水合的零拷贝指针绝不以正文身份上屏，
// 展示面回「来源缺口」占位（与产品 recent 同一措辞）。
func TestUnresolvableRefShowsGapMarker(t *testing.T) {
	ref := "@ref:zcode:sess_pruned:msg_pruned_1#0"
	trapDaemonSocket(t)
	stubEngineRunner(t, func(call engineCall) (string, error) {
		texts := []map[string]any{}
		for _, item := range engineRequestItems(t, call) {
			// 规范判定：宿主原文已清理，不可水合。
			texts = append(texts, map[string]any{"doc_id": asString(item["doc_id"]), "ref": asString(item["ref"]), "error": "source unavailable"})
		}
		return engineReplyJSON(t, map[string]any{"ok": true, "texts": texts}), nil
	})
	t.Setenv("HOME", t.TempDir())
	home := t.TempDir()

	if got := hydrateVaultTextForDisplay(filepath.Join(home, "memory.db"), "doc-one", ref); got != vaultRefGapMarker {
		t.Fatalf("display = %q, want gap marker", got)
	}
	if txt, span := hydrateEpisodicDisplayText(home, filepath.Join(home, "memory.db"), "doc-one", ref, ""); txt != vaultRefGapMarker || span != vaultRefGapMarker {
		t.Fatalf("graph display = (%q, %q), want gap marker", txt, span)
	}
	if got := hydrateVaultTextForDisplay(filepath.Join(home, "memory.db"), "doc-one", "普通正文"); got != "普通正文" {
		t.Fatalf("plain text must pass through, got %q", got)
	}
}

// TestConsoleDaemonChannelNeverDialsRawSocket 锁定修复要点：控制台的 recall/hydrate/
// documents/invalidate 四个面一律经 engine 内部通道，绝不向配置的 socket 发起裸连接
// （陷阱 socket 全程零连接）。多出的连接即意味着未核验 socket 客户端回潮。
func TestConsoleDaemonChannelNeverDialsRawSocket(t *testing.T) {
	connections := trapDaemonSocket(t)
	calls := stubEngineRunner(t, func(call engineCall) (string, error) {
		switch call.op {
		case "recall":
			return engineReplyJSON(t, map[string]any{"ok": true, "result": map[string]any{"absent": false, "meta": map[string]any{"elapsed_ms": 42}}}), nil
		case "hydrate":
			texts := []map[string]any{}
			for _, item := range engineRequestItems(t, call) {
				texts = append(texts, map[string]any{"doc_id": asString(item["doc_id"]), "ref": asString(item["ref"]), "actual_ref": asString(item["ref"]), "text": "verified body", "verified": true})
			}
			return engineReplyJSON(t, map[string]any{"ok": true, "texts": texts}), nil
		case "documents":
			docs := []map[string]any{}
			for _, item := range engineRequestItems(t, call) {
				docs = append(docs, map[string]any{"unit_id": asString(item["unit_id"]), "raw_text": "chunk body"})
			}
			return engineReplyJSON(t, map[string]any{"ok": true, "docs": docs}), nil
		case "invalidate":
			return engineReplyJSON(t, map[string]any{"ok": true, "notified": true}), nil
		}
		return "", errors.New("unexpected engine op " + call.op)
	})
	home := t.TempDir()
	dbPath := filepath.Join(home, "memory.db")

	raw, handled, err := tryConsoleDaemonRecall(
		context.Background(), home, dbPath, "mem:self", "owner", "full",
		8, 60, 12000, 5, "检索性能", "Rolling-Memory", false, false, "", "", "",
	)
	if err != nil || !handled || raw == nil {
		t.Fatalf("tryConsoleDaemonRecall = (%v, %v, %v), want handled", raw, handled, err)
	}
	if cue := asString((*calls)[0].request["cue"]); cue != "[Rolling-Memory] 检索性能" {
		t.Fatalf("engine payload cue = %q, want anchored cue", cue)
	}
	if noCrystallize, _ := (*calls)[0].request["no_crystallize"].(bool); !noCrystallize {
		t.Fatalf("engine payload no_crystallize = %v, want true", (*calls)[0].request["no_crystallize"])
	}
	if text, ok := hydratedEpisodicPointerLive(home, dbPath, "doc-one", "@ref:zcode:sess:msg#0"); !ok || text != "verified body" {
		t.Fatalf("hydrate = (%q, %v)", text, ok)
	}
	if text, ok := hydratedLocalDocumentLive(home, "bg:raw#0"); !ok || text != "chunk body" {
		t.Fatalf("documents = (%q, %v)", text, ok)
	}
	invalidateDaemonViaSocket(home)

	ops := map[string]int{}
	for _, call := range *calls {
		ops[call.op]++
	}
	for _, op := range []string{"recall", "hydrate", "documents", "invalidate"} {
		if ops[op] == 0 {
			t.Fatalf("engine op %q never ran: %+v", op, ops)
		}
	}
	if got := connections.Load(); got != 0 {
		t.Fatalf("raw socket connections = %d, want 0 (console must only use the engine channel)", got)
	}
}

// TestConsoleDaemonChannelFailuresDegrade 锁定负控：engine 子进程退出非 0 或输出非法
// JSON 时，四个面全部回既有降级路径（hydrate = 不可读、recall = 交回 CLI 回退），
// 绝不静默改用未核验 socket 或伪造正文。
func TestConsoleDaemonChannelFailuresDegrade(t *testing.T) {
	cases := []struct {
		name  string
		reply func(engineCall) (string, error)
	}{
		{"engine_exit_nonzero", func(engineCall) (string, error) { return "", errors.New("exit status 1") }},
		{"engine_invalid_json", func(engineCall) (string, error) { return "not json", nil }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			connections := trapDaemonSocket(t)
			stubEngineRunner(t, tc.reply)
			home := t.TempDir()
			dbPath := filepath.Join(home, "vault.db")

			if text, ok := hydratedEpisodicPointerLive(home, dbPath, "doc-one", "@ref:zcode:sess:msg#0"); ok || text != "" {
				t.Fatalf("pointer hydration = (%q, %v), want unavailable", text, ok)
			}
			if text, ok := hydratedLocalDocumentLive(home, "bg:x#0"); ok || text != "" {
				t.Fatalf("document read = (%q, %v), want unavailable", text, ok)
			}
			raw, handled, err := tryConsoleDaemonRecall(
				context.Background(), home, dbPath, "mem:self", "owner", "full",
				8, 60, 12000, 5, "x", "", false, false, "", "", "",
			)
			if handled || err != nil || raw != nil {
				t.Fatalf("recall must fall back to the CLI path, got (%v, %v, %v)", raw, handled, err)
			}
			invalidateDaemonViaSocket(home)
			if got := connections.Load(); got != 0 {
				t.Fatalf("raw socket connections = %d, want 0", got)
			}
		})
	}
}

// TestEngineCaptureBoundsChildOutput 钉住生产运行器的有界捕获：超限后继续排空
// （子进程不阻塞在写管道上）但不再存储，由调用方按协议失败处理。
func TestEngineCaptureBoundsChildOutput(t *testing.T) {
	capture := &engineCapture{limit: 8}
	if n, err := capture.Write([]byte("12345")); n != 5 || err != nil || capture.overflow {
		t.Fatalf("first write = (%d, %v, overflow=%v)", n, err, capture.overflow)
	}
	if n, err := capture.Write([]byte("6789")); n != 4 || err != nil || !capture.overflow {
		t.Fatalf("second write = (%d, %v, overflow=%v)", n, err, capture.overflow)
	}
	if n, err := capture.Write([]byte("more")); n != 4 || err != nil || capture.buffer.Len() != 8 {
		t.Fatalf("drain write = (%d, %v) buffer=%d", n, err, capture.buffer.Len())
	}
	if capture.buffer.String() != "12345678" {
		t.Fatalf("captured = %q", capture.buffer.String())
	}
}

// TestAdminDaemonHelpersHaveNoRawSocketClient 源码门禁：控制台守护调用面的两个文件
// 不得再出现裸 socket 客户端原语（回潮即红）。
func TestAdminDaemonHelpersHaveNoRawSocketClient(t *testing.T) {
	for _, name := range []string{"memory.go", "roi_facts.go"} {
		raw, err := os.ReadFile(name)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		source := string(raw)
		for _, forbidden := range []string{"net.Dialer", "net.Dial(", "DialContext", `"unix"`} {
			if strings.Contains(source, forbidden) {
				t.Fatalf("%s still contains a raw daemon socket client: %q", name, forbidden)
			}
		}
	}
}
