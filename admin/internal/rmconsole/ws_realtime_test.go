// plugin: rolling-memory v0.0.58
package rollingmemory

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	wsengine "github.com/ai-go-hub/ai-go-admin/pkg/ws"

	"github.com/coder/websocket"
	"github.com/gin-gonic/gin"
)

// TestWSRealtimeRoomJoinAndPush 锁定同源 pkg/ws 实时通道契约：
// 客户端连接 /ws 并 join 房间（rm/status、rm/env_health）后，
// 可在同一条 WebSocket 连接上多路复用收到各房间的实时帧。
func TestWSRealtimeRoomJoinAndPush(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("ROLLING_MEMORY_MODE", "local")
	dbPath := filepath.Join(home, "ws_test.db")
	t.Setenv("ROLLING_MEMORY_DB", dbPath)
	seedVault(t, dbPath,
		`INSERT INTO rm_units(doc_id, layer, occurred_at, raw_text, address_uri)
		 VALUES ('ws-doc-1', 'episodic', 1793270000, 'ws realtime test doc', 'C6:L1:LOCAL#ws-doc-1')`)

	prevExec := executeAdminSyncCLI
	t.Cleanup(func() { executeAdminSyncCLI = prevExec })
	executeAdminSyncCLI = func(_ context.Context, _, _, _, _ string, _ int) ([]byte, error) {
		return []byte(`{"mode":"local","active_harness_id":"zcode","active_harness_label":"ZCode","health_score":95,"harnesses":[{"id":"zcode","label":"ZCode"}]}`), nil
	}

	testSrv, err := wsengine.NewServer(wsengine.Options{
		ReadLimit:    1 << 20,
		SendBuffer:   64,
		PingInterval: 25 * time.Second,
		WriteTimeout: 10 * time.Second,
		AcceptOptions: &websocket.AcceptOptions{
			InsecureSkipVerify: true,
		},
		Authenticator: func(_ *http.Request) (wsengine.Identity, error) {
			return wsengine.Identity{ID: "1", Name: "rolling"}, nil
		},
		OnRoomJoin:  onRMWSRoomJoin,
		OnRoomLeave: onRMWSRoomLeave,
	})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	prevServer := rmWSServer
	rmWSServer = testSrv
	t.Cleanup(func() {
		rmWSServer = prevServer
	})

	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.GET("/ws", func(c *gin.Context) {
		testSrv.ServeHTTP(c.Writer, c.Request)
	})
	ts := httptest.NewServer(engine)
	defer ts.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()

	wsURL := "ws" + ts.URL[len("http"):] + "/ws?token=test-token"
	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatalf("websocket.Dial: %v", err)
	}
	defer conn.Close(websocket.StatusNormalClosure, "")

	joinCmd, _ := json.Marshal(map[string]any{
		"type":   "group",
		"action": "join",
		"room":   wsRoomEnvHealth,
	})
	if err := conn.Write(ctx, websocket.MessageText, joinCmd); err != nil {
		t.Fatalf("write join: %v", err)
	}

	gotEnvHealth := false
	for i := 0; i < 12; i++ {
		_, raw, rErr := conn.Read(ctx)
		if rErr != nil {
			t.Fatalf("read frame: %v", rErr)
		}
		var msg wsengine.Message
		if err := json.Unmarshal(raw, &msg); err != nil {
			t.Fatalf("unmarshal ws message: %v", err)
		}
		if msg.Type == wsengine.TypeGroup && msg.Action == wsengine.ActionMessage && msg.Room == wsRoomEnvHealth {
			var env struct {
				Event string         `json:"event"`
				Data  map[string]any `json:"data"`
			}
			if err := json.Unmarshal(msg.Data, &env); err != nil {
				t.Fatalf("unmarshal envelope: %v", err)
			}
			if env.Event == "env_health" && env.Data["health_score"] == float64(95) {
				gotEnvHealth = true
				break
			}
		}
	}
	if !gotEnvHealth {
		t.Fatalf("did not receive env_health frame over WebSocket")
	}
}

func TestParseWSRoomSpec_ROIScopeAndHarness(t *testing.T) {
	key, events, ok := parseWSRoomSpec("rm/roi:mem:self:zcode")
	if !ok {
		t.Fatalf("expected rm/roi:mem:self:zcode to parse")
	}
	if key.kind != streamKindROI || key.scope != "mem:self" || key.harness != "zcode" {
		t.Fatalf("unexpected key: %+v", key)
	}
	if len(events) != 1 || events[0] != "roi" {
		t.Fatalf("unexpected events: %+v", events)
	}
}

// TestWSRoomPermissionRulesUseCanonicalRuleNames 锁定 WebSocket 房间权限名与
// admin_rules.name 同形：HTTP 侧由 BuildCheckPath 去掉 /admin 前缀，房间侧没有
// 路径可剥，带前缀会让所有非超管角色被 403。
func TestWSRoomPermissionRulesUseCanonicalRuleNames(t *testing.T) {
	for _, kind := range []streamKind{
		streamKindStatus, streamKindDashboard, streamKindEnvHealth, streamKindTerminal,
		streamKindROI, streamKindEval, streamKindSystem, streamKindUpdate,
	} {
		rules, op := wsRoomPermissionRules(kind)
		if len(rules) == 0 || op == "" {
			t.Fatalf("kind %v has no permission rules", kind)
		}
		for _, rule := range rules {
			if strings.HasPrefix(rule, "admin/") {
				t.Fatalf("kind %v rule %q must not carry the /admin route prefix", kind, rule)
			}
			if !strings.HasPrefix(rule, "plugin/rolling-memory/") {
				t.Fatalf("kind %v rule %q is not a rolling-memory admin rule", kind, rule)
			}
		}
	}
}

func TestAuthorizeRMWSRoomAndToggleDeleteMemoryGuards(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("ROLLING_MEMORY_MODE", "local")
	t.Setenv("ROLLING_MEMORY_SCOPE", "default")
	t.Setenv("ROLLING_MEMORY_BIN", "/bin/echo")
	dbPath := filepath.Join(home, "guard_test.db")
	t.Setenv("ROLLING_MEMORY_DB", dbPath)
	seedVault(t, dbPath,
		`CREATE TABLE IF NOT EXISTS rm_meta (k TEXT PRIMARY KEY, v TEXT NOT NULL);
		 INSERT INTO rm_meta(k, v) VALUES ('local_plain_scope', 'default');
		 INSERT INTO rm_units(doc_id, layer, occurred_at, raw_text, address_uri)
		 VALUES ('doc-guard-1', 'episodic', 1793270000, 'secret text', 'C6:L1:LOCAL#doc-guard-1');
		 INSERT INTO rm_noetic(noetic_id, occurred_at, query_text, answer_text, cited_unit_ids_json, status)
		 VALUES ('nt-guard-1', 1793270010, 'q', 'a', '["doc-guard-1"]', 'active');`)

	prevPerm := checkRMConsolePermission
	t.Cleanup(func() { checkRMConsolePermission = prevPerm })

	// 1) WebSocket RoomAuthorizer denies join when terminal permission is missing.
	checkRMConsolePermission = func(_ context.Context, _ uint, ruleNames []string, _ string) (bool, error) {
		for _, r := range ruleNames {
			if r == "plugin/rolling-memory/terminal" {
				return false, nil
			}
		}
		return true, nil
	}
	if err := authorizeRMWSRoom(context.Background(), wsengine.Identity{ID: "7"}, wsRoomTerminal); err == nil {
		t.Fatalf("expected authorizeRMWSRoom to reject terminal room without permission")
	}
	if err := authorizeRMWSRoom(context.Background(), wsengine.Identity{ID: "7"}, wsRoomStatus); err != nil {
		t.Fatalf("expected authorizeRMWSRoom to allow status room with permission, got %v", err)
	}
	if err := authorizeRMWSRoom(context.Background(), wsengine.Identity{ID: "7"}, "rm/dashboard:mem:foreign"); err == nil {
		t.Fatalf("expected authorizeRMWSRoom to reject unsupported foreign scope")
	}

	// 2) applySwitchToggle("delete_memory") maintains tombstone + cascading stale on L3.
	if err := applySwitchToggle(home, "delete_memory", "unit:doc-guard-1"); err != nil {
		t.Fatalf("applySwitchToggle delete_memory: %v", err)
	}
	rows, err := sqliteQueryJSON(dbPath, `SELECT unit_id, layer FROM rm_deleted_units WHERE unit_id = 'doc-guard-1'`)
	if err != nil || len(rows) != 1 {
		t.Fatalf("expected tombstone for deleted unit, got rows=%v err=%v", rows, err)
	}
	nRows, err := sqliteQueryJSON(dbPath, `SELECT status FROM rm_noetic WHERE noetic_id = 'nt-guard-1'`)
	if err != nil || len(nRows) != 1 || asString(nRows[0]["status"]) != "stale" {
		t.Fatalf("expected citing noetic unit to be marked stale, got %v err=%v", nRows, err)
	}
}
