package rollingmemory

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestSystemVersionAndUpdateHandlers(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell stub test runs on unix")
	}
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)
	t.Setenv("XDG_DATA_HOME", filepath.Join(tmpHome, ".local", "share"))

	binDir := filepath.Join(tmpHome, ".local", "share", "rolling-memory", "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	fakeCLI := filepath.Join(binDir, "rolling-memory")
	script := `#!/bin/sh
if [ "$1" = "config" ]; then
  exit 0
fi
if [ "$1" = "update" ]; then
  for arg in "$@"; do
    if [ "$arg" = "--check" ]; then
      echo '{"ok":true,"action":"check","current_version":"0.0.1","latest_tag":"0.0.2","update_available":true,"cli":{"asset":"rolling-memory-darwin-arm64","update_available":true}}'
      exit 0
    fi
  done
  echo '{"ok":true,"action":"update","updated":true,"components":["rolling-memory-darwin-arm64","web-dist.tar.gz"],"previous_version":"0.0.1","latest_tag":"0.0.2","console_restart_deferred":false}'
  exit 0
fi
exit 1
`
	if err := os.WriteFile(fakeCLI, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ROLLING_MEMORY_BIN", fakeCLI)

	// 1. /system/version returns local version and SHA-256 without network access.
	verRaw := callPluginHandler(t, systemVersionHandler, "/admin/plugin/rolling-memory/system/version", `{}`)
	var verResp struct {
		Code int            `json:"code"`
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal([]byte(verRaw), &verResp); err != nil {
		t.Fatalf("unmarshal version response: %v (%s)", err, verRaw)
	}
	if verResp.Code != 0 || verResp.Data["ok"] != true {
		t.Fatalf("unexpected version response: %+v", verResp)
	}
	cliInfo, _ := verResp.Data["cli"].(map[string]any)
	if cliSHA, _ := cliInfo["local_sha256"].(string); len(cliSHA) != 64 {
		t.Fatalf("expected 64-char cli local_sha256, got %q", cliSHA)
	}

	// 2. Reject invalid release_base schemes, non-loopback HTTP, or flag injection.
	for _, badBase := range []string{"--bad-flag", "http://evil.example.com/releases", "http://127.0.0.1.evil.com/releases", "file:///tmp/evil"} {
		badRaw := callPluginHandler(t, systemUpdateCheckHandler, "/admin/plugin/rolling-memory/system/update/check", `{"release_base":"`+badBase+`"}`)
		if !strings.Contains(badRaw, "invalid release_base") {
			t.Fatalf("expected invalid release_base rejection for %q, got %s", badBase, badRaw)
		}
	}

	// 3. /system/update/status returns the cached snapshot shape without blocking.
	statusRaw := callPluginHandler(t, systemUpdateStatusHandler, "/admin/plugin/rolling-memory/system/update/status", `{}`)
	var statusResp struct {
		Code int            `json:"code"`
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal([]byte(statusRaw), &statusResp); err != nil {
		t.Fatalf("unmarshal status response: %v (%s)", err, statusRaw)
	}
	if statusResp.Code != 0 || statusResp.Data["ok"] != true {
		t.Fatalf("unexpected status response: %+v", statusResp)
	}

	// 3a. force refresh is accepted without blocking; the async check itself is
	// skipped in test binaries, but the request contract is still pinned.
	forceRaw := callPluginHandler(t, systemUpdateStatusHandler, "/admin/plugin/rolling-memory/system/update/status", `{"force":true}`)
	var forceResp struct {
		Code int            `json:"code"`
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal([]byte(forceRaw), &forceResp); err != nil {
		t.Fatalf("unmarshal forced status response: %v (%s)", err, forceRaw)
	}
	if forceResp.Code != 0 || forceResp.Data["forced"] != true {
		t.Fatalf("unexpected forced status response: %+v", forceResp)
	}

	// 3b. /system/update/channel persists the channel through the CLI.
	channelRaw := callPluginHandler(t, systemUpdateChannelHandler, "/admin/plugin/rolling-memory/system/update/channel", `{"channel":"beta"}`)
	var channelResp struct {
		Code int            `json:"code"`
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal([]byte(channelRaw), &channelResp); err != nil {
		t.Fatalf("unmarshal channel response: %v (%s)", err, channelRaw)
	}
	if channelResp.Code != 0 || channelResp.Data["channel"] != "beta" {
		t.Fatalf("unexpected channel response: %+v", channelResp)
	}

	// 3c. /system/update/source persists the source and rejects unknown values.
	sourceRaw := callPluginHandler(t, systemUpdateSourceHandler, "/admin/plugin/rolling-memory/system/update/source", `{"source":"gitee"}`)
	var sourceResp struct {
		Code int            `json:"code"`
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal([]byte(sourceRaw), &sourceResp); err != nil {
		t.Fatalf("unmarshal source response: %v (%s)", err, sourceRaw)
	}
	if sourceResp.Code != 0 || sourceResp.Data["source"] != "gitee" {
		t.Fatalf("unexpected source response: %+v", sourceResp)
	}
	badSourceRaw := callPluginHandler(t, systemUpdateSourceHandler, "/admin/plugin/rolling-memory/system/update/source", `{"source":"gitlab"}`)
	if !strings.Contains(badSourceRaw, "source must be github or gitee") {
		t.Fatalf("expected invalid source rejection, got %s", badSourceRaw)
	}

	// 3d. /system/update/auto persists the background policy and rejects
	// unknown values.
	autoRaw := callPluginHandler(t, systemUpdateAutoHandler, "/admin/plugin/rolling-memory/system/update/auto", `{"auto":"apply"}`)
	var autoResp struct {
		Code int            `json:"code"`
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal([]byte(autoRaw), &autoResp); err != nil {
		t.Fatalf("unmarshal auto response: %v (%s)", err, autoRaw)
	}
	if autoResp.Code != 0 || autoResp.Data["auto"] != "apply" || autoResp.Data["schedule_action"] != "install" {
		t.Fatalf("unexpected auto response: %+v", autoResp)
	}
	badAutoRaw := callPluginHandler(t, systemUpdateAutoHandler, "/admin/plugin/rolling-memory/system/update/auto", `{"auto":"always"}`)
	if !strings.Contains(badAutoRaw, "auto must be off, notify or apply") {
		t.Fatalf("expected invalid auto rejection, got %s", badAutoRaw)
	}

	// 4. /system/update/check returns parsed check payload.
	chkRaw := callPluginHandler(t, systemUpdateCheckHandler, "/admin/plugin/rolling-memory/system/update/check", `{"release_base":"https://example.com/releases"}`)
	var chkResp struct {
		Code int            `json:"code"`
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal([]byte(chkRaw), &chkResp); err != nil {
		t.Fatalf("unmarshal check response: %v (%s)", err, chkRaw)
	}
	if chkResp.Code != 0 || chkResp.Data["update_available"] != true || chkResp.Data["latest_tag"] != "0.0.2" {
		t.Fatalf("unexpected check response: %+v", chkResp)
	}

	// 5. /system/update/apply runs update with --defer-console-restart and returns updated components.
	appRaw := callPluginHandler(t, systemUpdateApplyHandler, "/admin/plugin/rolling-memory/system/update/apply", `{"release_base":"https://example.com/releases","force":true}`)
	var appResp struct {
		Code int            `json:"code"`
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal([]byte(appRaw), &appResp); err != nil {
		t.Fatalf("unmarshal apply response: %v (%s)", err, appRaw)
	}
	if appResp.Code != 0 || appResp.Data["updated"] != true || appResp.Data["latest_tag"] != "0.0.2" {
		t.Fatalf("unexpected apply response: %+v", appResp)
	}
}

// TestUpdateStateStaleUsesTenMinuteConsoleWindow 锁定控制台在线时的刷新周期：
// 成功快照 10 分钟后过期，失败快照仍按 30 分钟重试。
func TestUpdateStateStaleUsesTenMinuteConsoleWindow(t *testing.T) {
	home := t.TempDir()
	t.Setenv("XDG_STATE_HOME", filepath.Join(home, "state"))
	t.Setenv("ROLLING_MEMORY_PORTABLE", "")

	writeState := func(age time.Duration, ok bool) {
		t.Helper()
		checked := time.Now().Add(-age)
		payload := map[string]any{
			"ok":               ok,
			"checked_at":       checked.UTC().Format(time.RFC3339),
			"checked_at_unix":  checked.Unix(),
			"update_available": true,
		}
		raw, err := json.Marshal(payload)
		if err != nil {
			t.Fatal(err)
		}
		path := updateStatePath(home)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, raw, 0o644); err != nil {
			t.Fatal(err)
		}
	}

	writeState(9*time.Minute, true)
	if _, stale := updateStateStale(home); stale {
		t.Fatal("a successful snapshot younger than 10 minutes must stay fresh")
	}

	writeState(11*time.Minute, true)
	if _, stale := updateStateStale(home); !stale {
		t.Fatal("a successful snapshot older than 10 minutes must be stale")
	}

	writeState(29*time.Minute, false)
	if _, stale := updateStateStale(home); stale {
		t.Fatal("a failed snapshot must keep the 30-minute retry window")
	}

	writeState(31*time.Minute, false)
	if _, stale := updateStateStale(home); !stale {
		t.Fatal("a failed snapshot older than 30 minutes must be stale")
	}
}
