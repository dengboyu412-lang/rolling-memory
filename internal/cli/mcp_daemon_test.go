// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package cli_test

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rocareer/rolling-memory/internal/config"
	"github.com/rocareer/rolling-memory/internal/mcp"
	"github.com/rocareer/rolling-memory/internal/memory/facade"
)

func TestDaemonRecallUsesCallerConfig(t *testing.T) {
	env := newEnv(t)
	dir, err := os.MkdirTemp("/tmp", "rm-cli-config-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	sock := filepath.Join(dir, "engine.sock")
	t.Setenv("ROLLING_MEMORY_DAEMON", "on")
	t.Setenv("ROLLING_MEMORY_DAEMON_SOCKET", sock)
	path, db, scope, mode := filepath.Join(env.base, "alternate.toml"), filepath.Join(env.base, "alternate.db"), "mem:alternate", "local"
	if err := os.WriteFile(path, []byte("[recall]\nchars = 3210\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(config.LoadOptions{Path: &path, DB: &db, Mode: &mode, Scope: &scope})
	if err != nil {
		t.Fatal(err)
	}
	memory, err := facade.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- mcp.ServeUnix(sock, memory, nil) }()
	defer func() {
		client := &http.Client{Transport: &http.Transport{DialContext: func(_ context.Context, _, _ string) (net.Conn, error) {
			return net.Dial("unix", sock)
		}}}
		resp, _ := client.Post("http://unix/v1/shutdown", "application/json", strings.NewReader(`{}`))
		if resp != nil {
			_ = resp.Body.Close()
		}
		select {
		case <-done:
		case <-time.After(2 * time.Second):
		}
	}()
	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, err := mcp.PingUnixDaemon(sock, 100*time.Millisecond); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("daemon not ready")
		}
		time.Sleep(10 * time.Millisecond)
	}
	result := run(t, "recall", "--q", "test", "--config", path, "--db", db, "--scope", scope, "--mode", mode, "--json")
	if result.code != 0 {
		t.Fatalf("caller configuration not forwarded: %+v", result)
	}
	result = run(t, "recall", "--q", "test", "--config", path, "--db", db, "--scope", "mem:other", "--mode", mode, "--json")
	if result.code == 0 || !strings.Contains(result.stdout, "identity mismatch") {
		t.Fatalf("mismatched scope should fail closed: %+v", result)
	}
}

func TestDaemonRecallRetriesOnceAfterDisconnect(t *testing.T) {
	env := newEnv(t)
	dir, err := os.MkdirTemp("/tmp", "rm-cli-disconnect-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	sock := filepath.Join(dir, "engine.sock")
	t.Setenv("ROLLING_MEMORY_DAEMON", "on")
	t.Setenv("ROLLING_MEMORY_DAEMON_SOCKET", sock)
	cfg, err := config.Load(config.LoadOptions{})
	if err != nil {
		t.Fatal(err)
	}
	// The identity must come from the production helper so this fixture keeps
	// matching when non-semantic configuration fields are excluded from it.
	configID, err := mcp.DaemonConfigID(cfg)
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int64
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/health" {
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "pid": os.Getpid(), "db": cfg.DB, "identity_version": 1, "mode": "local", "pulse_mode": "local_plain", "config_id": configID})
			return
		}
		calls.Add(1)
		conn, _, hijackErr := w.(http.Hijacker).Hijack()
		if hijackErr == nil {
			_ = conn.Close()
		}
	})}
	go func() { _ = server.Serve(listener) }()
	defer server.Close()
	// A post-send disconnect is a transient transport failure; recall is
	// read-only, so the caller retries once. This listener closes every request,
	// so the second attempt also fails and the command must still fail without
	// opening an in-process fallback.
	result := run(t, "recall", "--q", "test", "--json")
	if result.code == 0 || calls.Load() != 2 {
		t.Fatalf("post-send disconnect must fail after one retry: code=%d calls=%d output=%s", result.code, calls.Load(), result.stdout)
	}
	if _, err := os.Stat(filepath.Join(env.base, "memory.db")); !os.IsNotExist(err) {
		t.Fatalf("in-process fallback opened the database: %v", err)
	}
}

func TestMCPLocalRejectsLegacyDaemon(t *testing.T) {
	newEnv(t)
	dir, err := os.MkdirTemp("/tmp", "rm-cli-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	sock := filepath.Join(dir, "engine.sock")
	t.Setenv("ROLLING_MEMORY_DAEMON", "on")
	t.Setenv("ROLLING_MEMORY_DAEMON_SOCKET", sock)
	var forwarded, outbound, shutdown atomic.Int64
	counter := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		outbound.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer counter.Close()
	listener, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/health":
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "pid": os.Getpid()})
		case "/v1/shutdown":
			shutdown.Add(1)
		default:
			forwarded.Add(1)
			resp, postErr := http.Post(counter.URL, "application/json", strings.NewReader(`{}`))
			if postErr == nil {
				_ = resp.Body.Close()
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": 7})
		}
	})}
	go func() { _ = server.Serve(listener) }()
	defer server.Close()
	request := `{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"name":"recall","arguments":{"cue":"test"}}}` + "\n"
	result := runInput(t, request, "mcp", "--mode", "local")
	if result.code != 0 {
		t.Fatalf("mcp exit %d: %s", result.code, result.stderr)
	}
	response := jsonObject(t, strings.TrimSpace(result.stdout))
	payload, ok := response["result"].(map[string]any)
	if !ok || payload["isError"] != true {
		t.Fatalf("expected fail-closed tool error, got %s", result.stdout)
	}
	if forwarded.Load() != 0 || outbound.Load() != 0 || shutdown.Load() != 0 {
		t.Fatalf("unexpected calls: mcp=%d HTTP=%d shutdown=%d", forwarded.Load(), outbound.Load(), shutdown.Load())
	}
	result = runInput(t, request, "mcp", "--mode", "cloud")
	if result.code == 0 || (!strings.Contains(result.stderr, "requires local mode") && !strings.Contains(result.stderr, "mode must be one of ('local')") && !strings.Contains(result.stderr, "invalid choice")) {
		t.Fatalf("non-local MCP mode must fail closed: %+v", result)
	}
	if forwarded.Load() != 0 || outbound.Load() != 0 || shutdown.Load() != 0 {
		t.Fatalf("non-local MCP contacted daemon: mcp=%d HTTP=%d shutdown=%d", forwarded.Load(), outbound.Load(), shutdown.Load())
	}
}
