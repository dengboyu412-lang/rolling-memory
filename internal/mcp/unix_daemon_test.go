// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package mcp

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/rocareer/rolling-memory/internal/config"
	"github.com/rocareer/rolling-memory/internal/memory/facade"
)

func TestUnixDaemonMCPPinsValidatedMemory(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "rm-mcp-swap-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	configPath := filepath.Join(dir, "config.toml")
	dbPath := filepath.Join(dir, "memory.db")
	t.Setenv("HOME", dir)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(dir, "data"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(dir, "state"))
	t.Setenv("ROLLING_MEMORY_CONFIG", configPath)
	t.Setenv("ROLLING_MEMORY_DB", dbPath)
	t.Setenv("ROLLING_MEMORY_LOG", filepath.Join(dir, "memory.log"))
	t.Setenv("ROLLING_MEMORY_MODE", "")
	t.Setenv("ROLLING_MEMORY_SCOPE", "mem:test")
	t.Setenv("ROLLING_MEMORY_BRAIN_KEY", "mem2.test-key")
	var outbound atomic.Int64
	counter := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		outbound.Add(1)
		http.Error(w, "transport witness", http.StatusServiceUnavailable)
	}))
	defer counter.Close()
	t.Setenv("ROLLING_MEMORY_BRAIN_URL", counter.URL+"/open/memory/v1")
	if err := os.WriteFile(configPath, []byte("[mode]\nname = \"local\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	sock := filepath.Join(dir, "engine.sock")
	done := make(chan error, 1)
	go func() { done <- ServeUnix(sock, nil, nil) }()
	defer func() {
		client := newUnixHTTPClient(sock, time.Second)
		resp, _ := client.Post("http://unix/v1/shutdown", "application/json", strings.NewReader(`{}`))
		if resp != nil {
			_ = resp.Body.Close()
		}
		select {
		case <-done:
		case <-time.After(2 * time.Second):
		}
	}()
	deadline := time.Now().Add(3 * time.Second)
	for {
		if _, err := PingUnixDaemon(sock, 100*time.Millisecond); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("daemon did not become healthy")
		}
		time.Sleep(10 * time.Millisecond)
	}
	body := `{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"name":"recall","arguments":{"cue":"test","mode":"probe"}}}`
	conn, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := fmt.Fprintf(conn, "POST /mcp HTTP/1.1\r\nHost: localhost\r\nContent-Type: application/json\r\nExpect: 100-continue\r\nContent-Length: %d\r\n\r\n", len(body)); err != nil {
		t.Fatal(err)
	}
	reader := bufio.NewReader(conn)
	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	interim, err := http.ReadResponse(reader, &http.Request{Method: http.MethodPost})
	if err != nil || interim.StatusCode != http.StatusContinue {
		t.Fatalf("handler did not start reading the request: response=%v err=%v", interim, err)
	}
	_ = interim.Body.Close()
	if err := os.WriteFile(configPath, []byte("[mode]\nname = \"cloud\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	replacement := filepath.Join(dir, "replacement.db")
	if err := os.WriteFile(replacement, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(replacement, dbPath); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Write([]byte(body)); err != nil {
		t.Fatal(err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	resp, err := http.ReadResponse(reader, &http.Request{Method: http.MethodPost})
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	responseBody, err := io.ReadAll(resp.Body)
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("MCP response status=%d body=%s err=%v", resp.StatusCode, responseBody, err)
	}
	_ = resp.Body.Close()
	_ = conn.Close()
	if outbound.Load() != 0 {
		t.Fatalf("validated local request made %d network requests: %s", outbound.Load(), responseBody)
	}
	var result map[string]any
	if err := json.Unmarshal(responseBody, &result); err != nil {
		t.Fatalf("invalid MCP response: %v", err)
	}
	payload, ok := result["result"].(map[string]any)
	if !ok || payload["isError"] != false {
		t.Fatalf("validated local request did not complete: %s", responseBody)
	}
	healthResp, err := newUnixHTTPClient(sock, time.Second).Get("http://unix/v1/health")
	if err != nil {
		t.Fatal(err)
	}
	_ = healthResp.Body.Close()
	if healthResp.StatusCode != http.StatusConflict || outbound.Load() != 0 {
		t.Fatalf("changed configuration health=%d outbound=%d", healthResp.StatusCode, outbound.Load())
	}
}

func TestUnixDaemonServeRecallAndStdioProxy(t *testing.T) {
	memory := newMemory(t)
	catText := "老张养了一只橘猫叫大黄。"
	if _, err := memory.Remember(facade.RememberRequest{
		Content:    &catText,
		Assertions: []map[string]any{{"s": "老张", "x": "养了", "o": "橘猫"}},
	}); err != nil {
		t.Fatalf("Remember: %v", err)
	}

	sockPath := filepath.Join(t.TempDir(), "engine.sock")
	t.Setenv(DaemonSocketEnv, sockPath)
	t.Setenv("ROLLING_MEMORY_CONFIG", memory.Config.Path)
	t.Setenv("ROLLING_MEMORY_DB", memory.Config.DB)
	t.Setenv("ROLLING_MEMORY_LOG", memory.Config.Log)
	t.Setenv("ROLLING_MEMORY_MODE", memory.Config.Mode)
	t.Setenv("ROLLING_MEMORY_SCOPE", memory.Config.Scope)
	t.Setenv("ROLLING_MEMORY_READER", memory.Config.Reader)

	done := make(chan error, 1)
	go func() {
		done <- ServeUnix(sockPath, memory, nil)
	}()
	t.Cleanup(func() {
		requestUnixDaemonShutdown(sockPath)
		select {
		case <-done:
		case <-time.After(2 * time.Second):
		}
	})

	// Wait for the Unix socket to bind.
	deadline := time.Now().Add(2 * time.Second)
	var health *UnixHealthResponse
	for time.Now().Before(deadline) {
		if h, err := PingUnixDaemon(sockPath, 100*time.Millisecond); err == nil && h != nil {
			health = h
			break
		}
		time.Sleep(15 * time.Millisecond)
	}
	if health == nil || !health.OK {
		select {
		case err := <-done:
			t.Fatalf("daemon did not become healthy on %s: %v", sockPath, err)
		default:
			t.Fatalf("daemon did not become healthy on %s", sockPath)
		}
	}

	// 1. Direct /v1/recall call (used by CLI and Admin Console).
	resp, err := CallUnixDaemonRecall(context.Background(), sockPath, memory.Config, UnixRecallPayload{
		Cue:           "猫",
		Mode:          "full",
		NoCrystallize: true,
		NeedRendered:  true,
	})
	if err != nil {
		t.Fatalf("CallUnixDaemonRecall: %v", err)
	}
	if !resp.OK || resp.Result == nil || !strings.Contains(resp.Rendered, "<rolling-memory-recall") {
		t.Fatalf("unexpected recall response: %+v", resp)
	}
	meta, _ := resp.Result["meta"].(map[string]any)
	if _, isFloat := meta["candidates"].(float64); isFloat {
		t.Fatalf("daemon response integers must not degrade to float64: %#v", meta["candidates"])
	}

	// 2. DB mismatch returns conflict error so caller can fall back to local open.
	_, err = CallUnixDaemonRecall(context.Background(), sockPath, memory.Config, UnixRecallPayload{
		DB:   filepath.Join(t.TempDir(), "other.db"),
		Cue:  "猫",
		Mode: "full",
	})
	if err == nil || !strings.Contains(err.Error(), "daemon db mismatch") {
		t.Fatalf("expected db mismatch error, got %v", err)
	}

	// 3. Stdio MCP Server with ProxyToDaemon=true forwards `tools/call` over the Unix socket
	//    without opening its own LazyMemory (`lazy.memory` stays nil).
	var out bytes.Buffer
	proxyLazy := NewLazyMemory()
	stdioSrv := &Server{
		In:            strings.NewReader(`{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"name":"recall","arguments":{"cue":"猫","mode":"probe"}}}` + "\n"),
		Out:           &out,
		Lazy:          proxyLazy,
		ProxyToDaemon: true,
	}
	if err := stdioSrv.Run(); err != nil {
		t.Fatalf("stdio proxy Run: %v", err)
	}
	if proxyLazy.memory != nil {
		t.Fatalf("stdio proxy must not open local LazyMemory when Unix daemon succeeds")
	}
	lines := decodeLines(t, out.String())
	if len(lines) != 1 || isError(t, lines[0]) {
		t.Fatalf("proxied tools/call response: %#v", lines)
	}
}

func TestUnixDaemonProxyRejectsUnverifiedHealth(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "rm-peer-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	t.Setenv("HOME", dir)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(dir, "data"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(dir, "state"))
	t.Setenv("ROLLING_MEMORY_CONFIG", filepath.Join(dir, "config.toml"))
	t.Setenv("ROLLING_MEMORY_DB", filepath.Join(dir, "memory.db"))
	t.Setenv("ROLLING_MEMORY_LOG", filepath.Join(dir, "memory.log"))
	t.Setenv("ROLLING_MEMORY_MODE", "local")
	t.Setenv("ROLLING_MEMORY_SCOPE", "mem:test")
	t.Setenv("ROLLING_MEMORY_READER", "deliverable")
	t.Setenv(DaemonEnableEnv, "on")
	cfg, err := config.Load(config.LoadOptions{})
	if err != nil {
		t.Fatal(err)
	}
	health, err := unixDaemonHealth(cfg, currentExeModTimeNS())
	if err != nil {
		t.Fatal(err)
	}
	var outbound atomic.Int64
	counter := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		outbound.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer counter.Close()

	for _, tc := range []struct {
		name   string
		mutate func(*UnixHealthResponse)
	}{
		{"legacy", func(h *UnixHealthResponse) { h.IdentityVersion = 0 }},
		{"missing mode", func(h *UnixHealthResponse) { h.Mode = "" }},
		{"different mode", func(h *UnixHealthResponse) { h.Mode = "cloud" }},
		{"missing pulse mode", func(h *UnixHealthResponse) { h.PulseMode = "" }},
		{"different pulse mode", func(h *UnixHealthResponse) { h.PulseMode = "pulse_plain" }},
		{"configuration mismatch", func(h *UnixHealthResponse) { h.ConfigID = "different" }},
		{"database mismatch", func(h *UnixHealthResponse) { h.DB = filepath.Join(dir, "other.db") }},
		{"false pid", func(h *UnixHealthResponse) { h.PID++ }},
		{"not healthy", func(h *UnixHealthResponse) { h.OK = false }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sock := filepath.Join(dir, "engine.sock")
			listener, err := net.Listen("unix", sock)
			if err != nil {
				t.Fatal(err)
			}
			t.Setenv(DaemonSocketEnv, sock)
			var calls, shutdown atomic.Int64
			reported := health
			tc.mutate(&reported)
			server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/v1/health":
					writeJSON(w, http.StatusOK, reported)
				case "/v1/shutdown":
					shutdown.Add(1)
				default:
					calls.Add(1)
					resp, postErr := http.Post(counter.URL, "application/json", strings.NewReader(`{}`))
					if postErr == nil {
						_ = resp.Body.Close()
					}
					writeJSON(w, http.StatusOK, map[string]any{"jsonrpc": "2.0", "id": 7})
				}
			})}
			go func() { _ = server.Serve(listener) }()
			defer server.Close()
			request := `{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"name":"recall","arguments":{"cue":"test"}}}` + "\n"
			var out bytes.Buffer
			if err := ServeWithDaemonProxy(strings.NewReader(request), &out); err != nil {
				t.Fatal(err)
			}
			responses := decodeLines(t, out.String())
			if len(responses) != 1 || !isError(t, responses[0]) {
				t.Fatalf("expected fail-closed MCP response, got %s", out.String())
			}
			if _, err := CallUnixDaemonRecall(context.Background(), sock, cfg, UnixRecallPayload{Cue: "test"}); err == nil {
				t.Fatal("CLI recall must reject the same unverified daemon")
			}
			if calls.Load() != 0 || outbound.Load() != 0 || shutdown.Load() != 0 {
				t.Fatalf("unexpected calls: mcp=%d HTTP=%d shutdown=%d", calls.Load(), outbound.Load(), shutdown.Load())
			}
		})
	}
}

func TestUnixDaemonRejectsForgedPeer(t *testing.T) {
	tool, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 unavailable")
	}
	dir, err := os.MkdirTemp("/tmp", "rm-forge-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	t.Setenv("HOME", dir)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(dir, "data"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(dir, "state"))
	t.Setenv("ROLLING_MEMORY_CONFIG", filepath.Join(dir, "config.toml"))
	t.Setenv("ROLLING_MEMORY_DB", filepath.Join(dir, "memory.db"))
	t.Setenv("ROLLING_MEMORY_LOG", filepath.Join(dir, "memory.log"))
	t.Setenv("ROLLING_MEMORY_MODE", "local")
	t.Setenv(DaemonEnableEnv, "on")
	sock := filepath.Join(dir, "engine.sock")
	t.Setenv(DaemonSocketEnv, sock)
	cfg, err := config.Load(config.LoadOptions{})
	if err != nil {
		t.Fatal(err)
	}
	id, err := unixDaemonConfigID(cfg)
	if err != nil {
		t.Fatal(err)
	}
	var outbound atomic.Int64
	counter := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		outbound.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer counter.Close()
	marker := filepath.Join(dir, "mcp-called")
	script := `import http.server,json,os,socketserver,sys,urllib.request
class Handler(http.server.BaseHTTPRequestHandler):
 def do_GET(self):
  data=json.dumps({'ok':True,'pid':os.getpid(),'db':sys.argv[2],'identity_version':1,'mode':'local','pulse_mode':'local_plain','config_id':sys.argv[3]}).encode()
  self.send_response(200); self.send_header('Content-Length',str(len(data))); self.end_headers(); self.wfile.write(data)
 def do_POST(self):
  open(sys.argv[4],'w').close(); urllib.request.urlopen(sys.argv[5],data=b'{}',timeout=2).read(); self.send_response(200); self.end_headers()
 def log_message(self,*args): pass
socketserver.UnixStreamServer(sys.argv[1],Handler).serve_forever()`
	cmd := exec.Command(tool, "-I", "-c", script, sock, cfg.DB, id, marker, counter.URL)
	cmd.Env = []string{"HOME=" + dir, "PATH=/usr/bin:/bin"}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	}()
	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, err := os.Lstat(sock); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("forged daemon did not bind")
		}
		time.Sleep(10 * time.Millisecond)
	}
	response, handled := ForwardToolCallToUnixDaemon(7, map[string]any{"cue": "test"}, nil)
	if !handled || !isError(t, response) || !strings.Contains(textOf(t, response), "peer verification") {
		t.Fatalf("forged peer must fail closed: %#v", response)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) || outbound.Load() != 0 {
		t.Fatalf("forged daemon received a request: marker=%v HTTP=%d", err, outbound.Load())
	}
}

func TestUnixDaemonPinsVerifiedConnection(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "rm-swap-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	t.Setenv("HOME", dir)
	t.Setenv("ROLLING_MEMORY_CONFIG", filepath.Join(dir, "config.toml"))
	t.Setenv("ROLLING_MEMORY_DB", filepath.Join(dir, "memory.db"))
	t.Setenv("ROLLING_MEMORY_LOG", filepath.Join(dir, "memory.log"))
	t.Setenv("ROLLING_MEMORY_MODE", "local")
	t.Setenv(DaemonEnableEnv, "on")
	sock := filepath.Join(dir, "engine.sock")
	t.Setenv(DaemonSocketEnv, sock)
	cfg, err := config.Load(config.LoadOptions{})
	if err != nil {
		t.Fatal(err)
	}
	health, err := unixDaemonHealth(cfg, currentExeModTimeNS())
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	listener.(*net.UnixListener).SetUnlinkOnClose(false)
	var originalCalls, replacementCalls atomic.Int64
	swapped := make(chan net.Listener, 1)
	original := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/health" {
			if err := os.Remove(sock); err != nil {
				swapped <- nil
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			replacement, err := net.Listen("unix", sock)
			if err != nil {
				swapped <- nil
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			go func() {
				conn, err := replacement.Accept()
				if err == nil {
					replacementCalls.Add(1)
					_ = conn.Close()
				}
			}()
			swapped <- replacement
			writeJSON(w, http.StatusOK, health)
			return
		}
		originalCalls.Add(1)
		writeJSON(w, http.StatusOK, map[string]any{"jsonrpc": "2.0", "id": 7, "result": map[string]any{"isError": false}})
	})}
	go func() { _ = original.Serve(listener) }()
	defer original.Close()
	conn, reader, err := verifiedUnixDaemonConn(sock, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	req, err := http.NewRequest(http.MethodPost, "http://unix/mcp", strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if err := req.Write(conn); err != nil {
		t.Fatal(err)
	}
	resp, err := http.ReadResponse(reader, req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	var replacement net.Listener
	select {
	case replacement = <-swapped:
	case <-time.After(time.Second):
		t.Fatal("health request did not swap socket")
	}
	if replacement != nil {
		_ = replacement.Close()
	}
	if replacement == nil || resp.StatusCode != http.StatusOK || originalCalls.Load() != 1 || replacementCalls.Load() != 0 {
		t.Fatalf("connection swap: status=%d original=%d replacement=%d", resp.StatusCode, originalCalls.Load(), replacementCalls.Load())
	}
}

func TestUnixDaemonSameImageCanForgeHealth(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "rm-self-forge-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	t.Setenv("HOME", dir)
	t.Setenv("ROLLING_MEMORY_CONFIG", filepath.Join(dir, "config.toml"))
	t.Setenv("ROLLING_MEMORY_DB", filepath.Join(dir, "memory.db"))
	t.Setenv("ROLLING_MEMORY_LOG", filepath.Join(dir, "memory.log"))
	t.Setenv("ROLLING_MEMORY_MODE", "local")
	t.Setenv(DaemonEnableEnv, "on")
	sock := filepath.Join(dir, "engine.sock")
	t.Setenv(DaemonSocketEnv, sock)
	cfg, err := config.Load(config.LoadOptions{})
	if err != nil {
		t.Fatal(err)
	}
	health, err := unixDaemonHealth(cfg, currentExeModTimeNS())
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
			writeJSON(w, http.StatusOK, health)
			return
		}
		calls.Add(1)
		writeJSON(w, http.StatusOK, map[string]any{"jsonrpc": "2.0", "id": 999, "result": map[string]any{"isError": false}})
	})}
	go func() { _ = server.Serve(listener) }()
	defer server.Close()
	response, handled := ForwardToolCallToUnixDaemon(7, map[string]any{"cue": "test"}, nil)
	if !handled || calls.Load() != 1 || !isError(t, response) || !strings.Contains(textOf(t, response), "ID mismatch") {
		t.Fatalf("mismatched response must fail closed: response=%#v calls=%d", response, calls.Load())
	}
}

func TestUnixDaemonRejectsPreloadedResponse(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "rm-preload-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	t.Setenv("HOME", dir)
	t.Setenv("ROLLING_MEMORY_CONFIG", filepath.Join(dir, "config.toml"))
	t.Setenv("ROLLING_MEMORY_DB", filepath.Join(dir, "memory.db"))
	t.Setenv("ROLLING_MEMORY_LOG", filepath.Join(dir, "memory.log"))
	t.Setenv("ROLLING_MEMORY_MODE", "local")
	cfg, err := config.Load(config.LoadOptions{})
	if err != nil {
		t.Fatal(err)
	}
	health, err := unixDaemonHealth(cfg, currentExeModTimeNS())
	if err != nil {
		t.Fatal(err)
	}
	sock := filepath.Join(dir, "engine.sock")
	listener, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	done := make(chan struct{})
	go func() {
		defer close(done)
		conn, acceptErr := listener.Accept()
		if acceptErr != nil {
			return
		}
		defer conn.Close()
		_, _ = http.ReadRequest(bufio.NewReader(conn))
		raw, _ := json.Marshal(health)
		_, _ = fmt.Fprintf(conn, "HTTP/1.1 200 OK\r\nContent-Length: %d\r\n\r\n%sHTTP/1.1 200 OK\r\nContent-Length: 2\r\n\r\n{}", len(raw), raw)
	}()
	conn, _, err := verifiedUnixDaemonConn(sock, cfg)
	if conn != nil {
		_ = conn.Close()
	}
	<-done
	if err == nil {
		t.Fatal("preloaded response accepted")
	}
}

func TestUnixDaemonRejectsInvalidResponseStream(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "rm-rpc-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	t.Setenv("HOME", dir)
	t.Setenv("ROLLING_MEMORY_CONFIG", filepath.Join(dir, "config.toml"))
	t.Setenv("ROLLING_MEMORY_DB", filepath.Join(dir, "memory.db"))
	t.Setenv("ROLLING_MEMORY_LOG", filepath.Join(dir, "memory.log"))
	t.Setenv("ROLLING_MEMORY_MODE", "local")
	t.Setenv(DaemonEnableEnv, "on")
	cfg, err := config.Load(config.LoadOptions{})
	if err != nil {
		t.Fatal(err)
	}
	health, err := unixDaemonHealth(cfg, currentExeModTimeNS())
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		body string
		ok   bool
	}{
		{"valid result", `{"jsonrpc":"2.0","id":7,"result":{"content":[],"isError":false}}`, true},
		{"valid error", `{"jsonrpc":"2.0","id":7,"error":{"code":-32603,"message":"failed"}}`, true},
		{"wrong id", `{"jsonrpc":"2.0","id":999,"result":{"content":[]}}`, false},
		{"missing id", `{"jsonrpc":"2.0","result":{"content":[]}}`, false},
		{"wrong version", `{"jsonrpc":"1.0","id":7,"result":{"content":[]}}`, false},
		{"both result and error", `{"jsonrpc":"2.0","id":7,"result":{"content":[]},"error":{"code":-1,"message":"bad"}}`, false},
		{"invalid result", `{"jsonrpc":"2.0","id":7,"result":null}`, false},
		{"missing content", `{"jsonrpc":"2.0","id":7,"result":{}}`, false},
		{"invalid content block", `{"jsonrpc":"2.0","id":7,"result":{"content":[{"type":"text"}]}}`, false},
		{"invalid isError", `{"jsonrpc":"2.0","id":7,"result":{"content":[],"isError":null}}`, false},
		{"invalid error", `{"jsonrpc":"2.0","id":7,"error":{"code":"bad","message":"bad"}}`, false},
		{"null error code", `{"jsonrpc":"2.0","id":7,"error":{"code":null,"message":"bad"}}`, false},
		{"trailing object", `{"jsonrpc":"2.0","id":7,"result":{"content":[]}} {}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sock := filepath.Join(dir, "engine.sock")
			t.Setenv(DaemonSocketEnv, sock)
			listener, err := net.Listen("unix", sock)
			if err != nil {
				t.Fatal(err)
			}
			server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/v1/health" {
					writeJSON(w, http.StatusOK, health)
					return
				}
				_, _ = w.Write([]byte(tc.body))
			})}
			go func() { _ = server.Serve(listener) }()
			defer server.Close()
			response, handled := ForwardToolCallToUnixDaemon(7, map[string]any{"cue": "test"}, nil)
			if !handled {
				t.Fatal("request not handled")
			}
			if tc.ok {
				if result := asMap(t, response); result["id"] != int64(7) {
					t.Fatalf("valid response: %#v", response)
				}
			} else if !isError(t, response) {
				t.Fatalf("invalid response accepted: %#v", response)
			}
		})
	}
}

func TestUnixDaemonRecallCancelsDuringHealth(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "rm-cancel-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	t.Setenv("HOME", dir)
	t.Setenv("ROLLING_MEMORY_CONFIG", filepath.Join(dir, "config.toml"))
	t.Setenv("ROLLING_MEMORY_DB", filepath.Join(dir, "memory.db"))
	t.Setenv("ROLLING_MEMORY_LOG", filepath.Join(dir, "memory.log"))
	t.Setenv("ROLLING_MEMORY_MODE", "local")
	cfg, err := config.Load(config.LoadOptions{})
	if err != nil {
		t.Fatal(err)
	}
	sock := filepath.Join(dir, "engine.sock")
	listener, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	seen := make(chan struct{})
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		_, _ = http.ReadRequest(bufio.NewReader(conn))
		close(seen)
		_, _ = io.Copy(io.Discard, conn)
	}()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := CallUnixDaemonRecall(ctx, sock, cfg, UnixRecallPayload{Cue: "test"})
		done <- err
	}()
	select {
	case <-seen:
	case <-time.After(time.Second):
		t.Fatal("health request not received")
	}
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("canceled health request succeeded")
		}
	case <-time.After(time.Second):
		t.Fatal("cancellation did not interrupt health request")
	}
}

func TestUnixDaemonRecallCancelsAfterSend(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "rm-cancel-send-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	t.Setenv("HOME", dir)
	t.Setenv("ROLLING_MEMORY_CONFIG", filepath.Join(dir, "config.toml"))
	t.Setenv("ROLLING_MEMORY_DB", filepath.Join(dir, "memory.db"))
	t.Setenv("ROLLING_MEMORY_LOG", filepath.Join(dir, "memory.log"))
	t.Setenv("ROLLING_MEMORY_MODE", "local")
	cfg, err := config.Load(config.LoadOptions{})
	if err != nil {
		t.Fatal(err)
	}
	health, err := unixDaemonHealth(cfg, currentExeModTimeNS())
	if err != nil {
		t.Fatal(err)
	}
	sock := filepath.Join(dir, "engine.sock")
	listener, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	seen := make(chan struct{})
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/health" {
			writeJSON(w, http.StatusOK, health)
			return
		}
		close(seen)
		<-r.Context().Done()
	})}
	go func() { _ = server.Serve(listener) }()
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := CallUnixDaemonRecall(ctx, sock, cfg, UnixRecallPayload{Cue: "test"})
		done <- err
	}()
	select {
	case <-seen:
	case <-time.After(time.Second):
		t.Fatal("recall request not received")
	}
	cancel()
	select {
	case err := <-done:
		var notSent *DaemonRecallNotSentError
		if err == nil || errors.As(err, &notSent) || !strings.Contains(err.Error(), "outcome unknown") {
			t.Fatalf("sent request must not be retryable: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("cancellation did not interrupt recall")
	}
}

func TestUnixDaemonRecallHonorsCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := CallUnixDaemonRecall(ctx, filepath.Join(t.TempDir(), "absent.sock"), nil, UnixRecallPayload{Cue: "test"})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled request: %v", err)
	}
}

func TestUnixDaemonControlRejectsRedirectAndLegacy(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "rm-control-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	t.Setenv("HOME", dir)
	t.Setenv("ROLLING_MEMORY_CONFIG", filepath.Join(dir, "config.toml"))
	t.Setenv("ROLLING_MEMORY_DB", filepath.Join(dir, "memory.db"))
	t.Setenv("ROLLING_MEMORY_LOG", filepath.Join(dir, "memory.log"))
	t.Setenv("ROLLING_MEMORY_MODE", "local")
	cfg, err := config.Load(config.LoadOptions{})
	if err != nil {
		t.Fatal(err)
	}
	health, err := unixDaemonHealth(cfg, currentExeModTimeNS())
	if err != nil {
		t.Fatal(err)
	}
	var external atomic.Int64
	counter := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		external.Add(1)
	}))
	defer counter.Close()
	for _, legacy := range []bool{false, true} {
		sock := filepath.Join(dir, "engine.sock")
		t.Setenv(DaemonSocketEnv, sock)
		listener, err := net.Listen("unix", sock)
		if err != nil {
			t.Fatal(err)
		}
		var controls atomic.Int64
		server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/v1/health" {
				if legacy {
					writeJSON(w, http.StatusOK, map[string]any{"ok": true})
				} else {
					writeJSON(w, http.StatusOK, health)
				}
				return
			}
			controls.Add(1)
			w.Header().Set("Location", counter.URL)
			w.WriteHeader(http.StatusTemporaryRedirect)
		})}
		go func() { _ = server.Serve(listener) }()
		for _, path := range []string{"/v1/shutdown", "/v1/invalidate"} {
			if err := requestUnixDaemonControl(sock, path, cfg); err == nil {
				t.Fatalf("legacy=%v path=%s must fail", legacy, path)
			}
		}
		if err := ShutdownUnixDaemon(sock); err == nil {
			t.Fatalf("legacy=%v shutdown must not report success", legacy)
		}
		_ = server.Close()
		if external.Load() != 0 || (legacy && controls.Load() != 0) || (!legacy && controls.Load() != 3) {
			t.Fatalf("legacy=%v controls=%d external=%d", legacy, controls.Load(), external.Load())
		}
	}
}

func TestUnixDaemonPeerAcceptsHardLinkedExecutable(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("Darwin executable path behavior")
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(filepath.Dir(exe), "daemon-peer-hardlink-"+filepath.Base(t.TempDir()))
	if err := os.Link(exe, link); err != nil {
		t.Skipf("hard links unavailable: %v", err)
	}
	t.Cleanup(func() { _ = os.Remove(link) })
	dir, err := os.MkdirTemp("/tmp", "rm-link-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	sock := filepath.Join(dir, "engine.sock")
	cmd := exec.Command(link, "-test.run=^TestUnixDaemonPeerHardlinkHelper$")
	cmd.Env = append(os.Environ(), "RM_PEER_HARDLINK_SOCKET="+sock)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	}()
	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, err := os.Lstat(sock); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("hard-linked peer did not bind")
		}
		time.Sleep(10 * time.Millisecond)
	}
	conn, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := verifyUnixDaemonPeer(conn); err != nil {
		t.Fatal(err)
	}
}

func TestUnixDaemonPeerHardlinkHelper(t *testing.T) {
	sock := os.Getenv("RM_PEER_HARDLINK_SOCKET")
	if sock == "" {
		return
	}
	listener, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	conn, err := listener.Accept()
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_, _ = io.Copy(io.Discard, conn)
}

func TestUnixDaemonShutdownConfirmation(t *testing.T) {
	memory := newMemory(t)
	dir, err := os.MkdirTemp("/tmp", "rm-shutdown-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	sock := filepath.Join(dir, "engine.sock")
	t.Setenv("ROLLING_MEMORY_CONFIG", memory.Config.Path)
	t.Setenv("ROLLING_MEMORY_DB", memory.Config.DB)
	t.Setenv("ROLLING_MEMORY_LOG", memory.Config.Log)
	t.Setenv("ROLLING_MEMORY_MODE", memory.Config.Mode)
	t.Setenv("ROLLING_MEMORY_SCOPE", memory.Config.Scope)
	t.Setenv("ROLLING_MEMORY_READER", memory.Config.Reader)
	done := make(chan error, 1)
	go func() { done <- ServeUnix(sock, memory, nil) }()
	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, err := PingUnixDaemon(sock, 100*time.Millisecond); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("daemon not ready")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := ShutdownUnixDaemon(sock); err != nil {
		t.Fatalf("verified shutdown: %v", err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("daemon did not exit")
	}
	if err := ShutdownUnixDaemon(sock); err == nil {
		t.Fatal("absent daemon must not report confirmed shutdown")
	}
}

func TestUnixDaemonRetireDrainsInFlight(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "rm-drain-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	sock := filepath.Join(dir, "engine.sock")
	listener, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	var inFlight atomic.Int64
	started := make(chan struct{})
	release := make(chan struct{})
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		inFlight.Add(1)
		defer inFlight.Add(-1)
		close(started)
		<-release
		_, _ = w.Write([]byte("complete"))
	})}
	go func() { _ = server.Serve(listener) }()
	type result struct {
		body string
		err  error
	}
	results := make(chan result, 1)
	go func() {
		resp, err := newUnixHTTPClient(sock, 5*time.Second).Get("http://unix/block")
		if err != nil {
			results <- result{err: err}
			return
		}
		raw, readErr := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		results <- result{body: string(raw), err: readErr}
	}()
	<-started
	_ = listener.Close()
	retired := make(chan struct{})
	go func() {
		quiescentUnixDaemon(&inFlight)
		closeUnixDaemon(server)
		close(retired)
	}()
	time.Sleep(100 * time.Millisecond)
	select {
	case <-retired:
		t.Fatal("retirement closed the server while a request was in flight")
	default:
	}
	close(release)
	got := <-results
	if got.err != nil || got.body != "complete" {
		t.Fatalf("in-flight response was cut: body=%q err=%v", got.body, got.err)
	}
	select {
	case <-retired:
	case <-time.After(2 * time.Second):
		t.Fatal("retirement did not finish after the request drained")
	}
}

func TestUnixDaemonRetireBoundsStuckRequests(t *testing.T) {
	previous := shutdownDeadline
	shutdownDeadline = 50 * time.Millisecond
	t.Cleanup(func() { shutdownDeadline = previous })
	dir, err := os.MkdirTemp("/tmp", "rm-bound-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	sock := filepath.Join(dir, "engine.sock")
	listener, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	var inFlight atomic.Int64
	started := make(chan struct{})
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		inFlight.Add(1)
		defer inFlight.Add(-1)
		close(started)
		<-release
	})}
	go func() { _ = server.Serve(listener) }()
	go func() {
		resp, err := newUnixHTTPClient(sock, 5*time.Second).Get("http://unix/block")
		if err == nil {
			_ = resp.Body.Close()
		}
	}()
	<-started
	_ = listener.Close()
	retired := make(chan struct{})
	go func() {
		quiescentUnixDaemon(&inFlight)
		closeUnixDaemon(server)
		close(retired)
	}()
	select {
	case <-retired:
	case <-time.After(2 * time.Second):
		t.Fatal("retirement did not bound a stuck request")
	}
}

func TestUnixDaemonStartupPreservesExistingSocket(t *testing.T) {
	memory := newMemory(t)
	dir, err := os.MkdirTemp("/tmp", "rm-existing-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	sock := filepath.Join(dir, "engine.sock")
	listener, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	if err := ServeUnix(sock, memory, nil); err == nil {
		t.Fatal("daemon should refuse an occupied socket")
	}
	conn, err := net.DialTimeout("unix", sock, time.Second)
	if err != nil {
		t.Fatalf("existing listener was disturbed: %v", err)
	}
	_ = conn.Close()
}

func testUnixDaemonLeasePath(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "rm-lease-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return filepath.Join(dir, "engine.sock")
}

func TestUnixDaemonSocketPathNormalization(t *testing.T) {
	for _, length := range []int{16, 42, 62, 80, maxUnixSocketPathLen} {
		cand := "/" + strings.Repeat("s", length-len("/engine.sock")-1) + "/engine.sock"
		if len(cand) != length {
			t.Fatalf("fixture length %d != %d", len(cand), length)
		}
		if got := normalizeUnixSocketPath(cand); got != cand {
			t.Errorf("normalize(%q) = %q, want unchanged", cand, got)
		}
	}
	dir, err := os.MkdirTemp("/tmp", "rm-norm-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	t.Setenv(DaemonSocketEnv, "")
	t.Setenv("HOME", dir)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "config"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(dir, "state"))
	want := filepath.Join(config.DefaultStateDir(), "engine.sock")
	if len(want) > maxUnixSocketPathLen {
		t.Fatalf("fixture default path too long: %s", want)
	}
	if got := DefaultUnixSocketPath(); got != want {
		t.Errorf("DefaultUnixSocketPath() = %q, want %q", got, want)
	}
	long := "/" + strings.Repeat("l", maxUnixSocketPathLen) + "/engine.sock"
	migrated := normalizeUnixSocketPath(long)
	if migrated == long || len(migrated) > maxUnixSocketPathLen {
		t.Fatalf("long path not migrated: %q", migrated)
	}
	if again := normalizeUnixSocketPath(long); again != migrated {
		t.Fatalf("migration unstable: %q vs %q", migrated, again)
	}
	if !strings.HasPrefix(migrated, os.TempDir()) && !strings.HasPrefix(migrated, "/tmp/") {
		t.Fatalf("migrated path is not under a temp dir: %q", migrated)
	}
	if filepath.Base(migrated) != "engine.sock" {
		t.Fatalf("migrated path lost its socket name: %q", migrated)
	}
	if leaseDir := unixDaemonLeaseDir(migrated); len(leaseDir)+len("/socket") > maxUnixSocketPathLen {
		t.Fatalf("lease anchor exceeds the path budget: %q", leaseDir)
	}
}

func TestUnixDaemonLeaseDirPlacement(t *testing.T) {
	short := testUnixDaemonLeasePath(t)
	sum := sha256.Sum256([]byte(filepath.Base(short)))
	want := filepath.Join(filepath.Dir(short), ".rm-socket-"+hex.EncodeToString(sum[:8]))
	if got := unixDaemonLeaseDir(short); got != want {
		t.Fatalf("adjacent lease dir = %q, want %q", got, want)
	}
	base, err := os.MkdirTemp("/tmp", "rm-deep-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(base) })
	deep := filepath.Join(base, strings.Repeat("d", 45))
	sock := filepath.Join(deep, "engine.sock")
	if len(sock) > maxUnixSocketPathLen {
		t.Fatalf("fixture path exceeds the publish budget: %d", len(sock))
	}
	if len(filepath.Join(deep, ".rm-socket-0000000000000000"))+len("/socket") <= maxUnixSocketPathLen {
		t.Fatalf("fixture does not exercise the lease fallback: %d", len(deep))
	}
	got := unixDaemonLeaseDir(sock)
	if strings.HasPrefix(got, deep) {
		t.Fatalf("lease dir stayed adjacent despite the path budget: %q", got)
	}
	if !strings.HasPrefix(got, os.TempDir()) && !strings.HasPrefix(got, "/tmp/") {
		t.Fatalf("lease dir is not under a temp dir: %q", got)
	}
	if len(got)+len("/socket") > maxUnixSocketPathLen {
		t.Fatalf("fallback lease anchor exceeds the path budget: %q", got)
	}
	if again := unixDaemonLeaseDir(sock); again != got {
		t.Fatalf("lease dir unstable: %q vs %q", got, again)
	}
	if sibling := unixDaemonLeaseDir(filepath.Join(deep, "other.sock")); sibling == got {
		t.Fatalf("different sockets share one fallback lease dir: %q", got)
	}
}

func TestUnixDaemonLeaseRecoversOnlyPinnedStaleSocket(t *testing.T) {
	if !unixDaemonPeerVerificationAvailable() {
		t.Skip("Unix daemon lease unavailable")
	}
	sock := testUnixDaemonLeasePath(t)
	lease, err := acquireUnixDaemonSocketLease(sock, true)
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("unix", lease.anchor)
	if err != nil {
		t.Fatal(err)
	}
	listener.(*net.UnixListener).SetUnlinkOnClose(false)
	if err := os.Link(lease.anchor, sock); err != nil {
		t.Fatal(err)
	}
	if other, err := acquireUnixDaemonSocketLease(sock, false); !errors.Is(err, errUnixDaemonStarting) {
		if other != nil {
			other.close()
		}
		t.Fatalf("live lease not held: %v", err)
	}
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	lease.close()
	lease, err = acquireUnixDaemonSocketLease(sock, false)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.close()
	if err := lease.recover(); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{sock, lease.anchor} {
		if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("stale path remains: %s: %v", path, err)
		}
	}
}

func TestUnixDaemonLeasePreservesReplacedPaths(t *testing.T) {
	if !unixDaemonPeerVerificationAvailable() {
		t.Skip("Unix daemon lease unavailable")
	}
	for _, replacement := range []string{"socket", "file", "symlink", "directory"} {
		t.Run(replacement, func(t *testing.T) {
			sock := testUnixDaemonLeasePath(t)
			lease, err := acquireUnixDaemonSocketLease(sock, true)
			if err != nil {
				t.Fatal(err)
			}
			defer lease.close()
			listener, err := net.Listen("unix", lease.anchor)
			if err != nil {
				t.Fatal(err)
			}
			listener.(*net.UnixListener).SetUnlinkOnClose(false)
			if err := os.Link(lease.anchor, sock); err != nil {
				t.Fatal(err)
			}
			_ = listener.Close()
			if err := os.Remove(sock); err != nil {
				t.Fatal(err)
			}
			var replacementListener net.Listener
			switch replacement {
			case "socket":
				replacementListener, err = net.Listen("unix", sock)
				if err == nil {
					defer replacementListener.Close()
				}
			case "file":
				err = os.WriteFile(sock, []byte("preserve"), 0o600)
			case "symlink":
				err = os.Symlink(lease.anchor, sock)
			case "directory":
				err = os.Mkdir(sock, 0o700)
			}
			if err != nil {
				t.Fatal(err)
			}
			before, err := os.Lstat(sock)
			if err != nil {
				t.Fatal(err)
			}
			if err := lease.recover(); err == nil {
				t.Fatal("replaced path was accepted")
			}
			after, err := os.Lstat(sock)
			if err != nil || !os.SameFile(before, after) {
				t.Fatalf("replacement was disturbed: %v", err)
			}
		})
	}
}

func TestUnixDaemonLeaseStagedRemovalProtectsReplacements(t *testing.T) {
	if !unixDaemonPeerVerificationAvailable() {
		t.Skip("Unix daemon lease unavailable")
	}
	for _, kind := range []string{"socket", "file", "symlink", "directory"} {
		t.Run(kind, func(t *testing.T) {
			sock := testUnixDaemonLeasePath(t)
			lease, err := acquireUnixDaemonSocketLease(sock, true)
			if err != nil {
				t.Fatal(err)
			}
			defer lease.close()
			listener, err := net.Listen("unix", lease.anchor)
			if err != nil {
				t.Fatal(err)
			}
			listener.(*net.UnixListener).SetUnlinkOnClose(false)
			anchor, err := os.Lstat(lease.anchor)
			if err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "socket":
				if err := os.Link(lease.anchor, sock); err != nil {
					t.Fatal(err)
				}
			case "file":
				if err := os.WriteFile(sock, []byte("preserve"), 0o600); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				if err := os.Symlink(lease.anchor, sock); err != nil {
					t.Fatal(err)
				}
			case "directory":
				if err := os.Mkdir(sock, 0o700); err != nil {
					t.Fatal(err)
				}
			}
			_ = listener.Close()
			err = lease.stageAndRemoveStaleSocket(anchor)
			if kind == "socket" {
				if err != nil {
					t.Fatal(err)
				}
				if _, err := os.Lstat(sock); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("stale socket left behind: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("%s was removed", kind)
			}
			info, statErr := os.Lstat(sock)
			if statErr != nil {
				t.Fatalf("replacement was not restored: %v", statErr)
			}
			switch kind {
			case "file":
				raw, readErr := os.ReadFile(sock)
				if !info.Mode().IsRegular() || readErr != nil || string(raw) != "preserve" {
					t.Fatalf("file replacement damaged: mode=%v content=%q err=%v", info.Mode(), raw, readErr)
				}
			case "symlink":
				if info.Mode()&os.ModeSymlink == 0 {
					t.Fatalf("symlink replacement damaged: mode=%v", info.Mode())
				}
			case "directory":
				if !info.IsDir() {
					t.Fatalf("directory replacement damaged: mode=%v", info.Mode())
				}
			}
			if _, err := os.Lstat(filepath.Join(lease.dirPath, "stale")); err == nil {
				t.Fatal("staged entry left in the lease directory")
			}
		})
	}
}

func TestUnixDaemonLeaseRejectsUnmarkedAndUnsafePaths(t *testing.T) {
	if !unixDaemonPeerVerificationAvailable() {
		t.Skip("Unix daemon lease unavailable")
	}
	for _, kind := range []string{"stale socket", "file", "symlink", "unsafe parent", "unsafe lease"} {
		t.Run(kind, func(t *testing.T) {
			sock := testUnixDaemonLeasePath(t)
			if kind == "unsafe parent" {
				if err := os.Chmod(filepath.Dir(sock), 0o777); err != nil {
					t.Fatal(err)
				}
			} else if kind == "unsafe lease" {
				lease, err := acquireUnixDaemonSocketLease(sock, true)
				if err != nil {
					t.Fatal(err)
				}
				lease.close()
				if err := os.Chmod(lease.dirPath, 0o777); err != nil {
					t.Fatal(err)
				}
			} else {
				var err error
				switch kind {
				case "stale socket":
					var listener net.Listener
					listener, err = net.Listen("unix", sock)
					if err == nil {
						listener.(*net.UnixListener).SetUnlinkOnClose(false)
						_ = listener.Close()
					}
				case "file":
					err = os.WriteFile(sock, []byte("preserve"), 0o600)
				case "symlink":
					err = os.Symlink(filepath.Join(filepath.Dir(sock), "missing"), sock)
				}
				if err != nil {
					t.Fatal(err)
				}
			}
			lease, err := acquireUnixDaemonSocketLease(sock, true)
			if err == nil {
				err = lease.recover()
				lease.close()
			}
			if err == nil {
				t.Fatalf("%s accepted", kind)
			}
			if kind != "unsafe parent" && kind != "unsafe lease" {
				before, err := os.Lstat(sock)
				if err != nil {
					t.Fatalf("unmarked path removed: %v", err)
				}
				if kind == "stale socket" {
					memory := newMemory(t)
					err := ensureUnixDaemonContext(context.Background(), sock, memory.Config)
					after, statErr := os.Lstat(sock)
					if err == nil || !strings.Contains(err.Error(), sock) || !strings.Contains(err.Error(), "confirming no daemon is running") ||
						!strings.Contains(err.Error(), "remove this socket manually") || statErr != nil || !os.SameFile(before, after) {
						t.Fatalf("legacy stale socket: err=%v stat=%v", err, statErr)
					}
				}
			}
		})
	}
}

func TestUnixDaemonLeaseSerializesStartups(t *testing.T) {
	if !unixDaemonPeerVerificationAvailable() {
		t.Skip("Unix daemon lease unavailable")
	}
	sock := testUnixDaemonLeasePath(t)
	start := make(chan struct{})
	var wg sync.WaitGroup
	var acquired atomic.Int64
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			lease, err := acquireUnixDaemonSocketLease(sock, true)
			if err == nil {
				acquired.Add(1)
				time.Sleep(100 * time.Millisecond)
				lease.close()
			} else if !errors.Is(err, errUnixDaemonStarting) {
				t.Errorf("acquire lease: %v", err)
			}
		}()
	}
	close(start)
	wg.Wait()
	if acquired.Load() != 1 {
		t.Fatalf("concurrent lease holders: %d", acquired.Load())
	}
}

func TestUnixDaemonLeaseBoundBeforeListenIsNotRecovered(t *testing.T) {
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skip("Unix socket helper unavailable")
	}
	tool, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 unavailable")
	}
	sock := testUnixDaemonLeasePath(t)
	ready := filepath.Join(filepath.Dir(sock), "ready")
	cmd := exec.Command(tool, "-I", "-c", `import os,socket,sys,time
s=socket.socket(socket.AF_UNIX); s.bind(sys.argv[1]); open(sys.argv[2],"w").close(); time.sleep(5); s.listen(1); time.sleep(5)`, sock, ready)
	cmd.Env = []string{"HOME=" + filepath.Dir(sock), "PATH=/usr/bin:/bin"}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()
	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, err := os.Lstat(ready); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("helper did not bind")
		}
		time.Sleep(10 * time.Millisecond)
	}
	conn, err := net.DialTimeout("unix", sock, time.Second)
	if conn != nil {
		_ = conn.Close()
	}
	if !unixDaemonConnectionRefused(err) {
		t.Fatalf("expected refusal before listen: %v", err)
	}
	before, err := os.Lstat(sock)
	if err != nil {
		t.Fatal(err)
	}
	lease, err := acquireUnixDaemonSocketLease(sock, true)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.close()
	if err := lease.recover(); err == nil {
		t.Fatal("unmarked bound socket was recovered")
	}
	after, err := os.Lstat(sock)
	if err != nil || !os.SameFile(before, after) {
		t.Fatalf("bound socket changed: %v", err)
	}
}

func TestUnixDaemonLeaseRejectsForeignOwnership(t *testing.T) {
	if !unixDaemonPeerVerificationAvailable() {
		t.Skip("Unix daemon lease unavailable")
	}
	if os.Geteuid() != 0 {
		t.Skip("changing fixture ownership requires root")
	}
	sock := testUnixDaemonLeasePath(t)
	lease, err := acquireUnixDaemonSocketLease(sock, true)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.close()
	listener, err := net.Listen("unix", lease.anchor)
	if err != nil {
		t.Fatal(err)
	}
	listener.(*net.UnixListener).SetUnlinkOnClose(false)
	if err := os.Link(lease.anchor, sock); err != nil {
		t.Fatal(err)
	}
	_ = listener.Close()
	if err := os.Chown(lease.anchor, 1, -1); err != nil {
		t.Fatal(err)
	}
	if err := lease.recover(); err == nil {
		t.Fatal("foreign-owned socket accepted")
	}
	if _, err := os.Lstat(sock); err != nil {
		t.Fatalf("foreign-owned socket removed: %v", err)
	}
}

func TestUnixDaemonLeasePreservesReplacedOwnedAnchor(t *testing.T) {
	if !unixDaemonPeerVerificationAvailable() {
		t.Skip("Unix daemon lease unavailable")
	}
	sock := testUnixDaemonLeasePath(t)
	lease, err := acquireUnixDaemonSocketLease(sock, true)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.close()
	listener, err := net.Listen("unix", lease.anchor)
	if err != nil {
		t.Fatal(err)
	}
	listener.(*net.UnixListener).SetUnlinkOnClose(false)
	lease.owned, err = os.Lstat(lease.anchor)
	if err != nil {
		t.Fatal(err)
	}
	_ = listener.Close()
	// Build the replacement at a scratch path first and rename it into place:
	// binding in place right after the unlink can reuse the removed entry's
	// inode number (routine on Linux filesystems), which would make the
	// replacement indistinguishable from the pinned socket on dev+inode alone.
	replacementPath := lease.anchor + ".replacement"
	replacement, err := net.Listen("unix", replacementPath)
	if err != nil {
		t.Fatal(err)
	}
	replacement.(*net.UnixListener).SetUnlinkOnClose(false)
	_ = replacement.Close()
	if err := os.Remove(lease.anchor); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(replacementPath, lease.anchor); err != nil {
		t.Fatal(err)
	}
	before, err := os.Lstat(lease.anchor)
	if err != nil {
		t.Fatal(err)
	}
	if err := lease.recover(); err == nil {
		t.Fatal("replaced owned anchor accepted")
	}
	after, err := os.Lstat(lease.anchor)
	if err != nil || !os.SameFile(before, after) {
		t.Fatalf("replacement anchor disturbed: %v", err)
	}
}

func TestUnixDaemonLeaseRejectsClosedLease(t *testing.T) {
	if !unixDaemonPeerVerificationAvailable() {
		t.Skip("Unix daemon lease unavailable")
	}
	sock := testUnixDaemonLeasePath(t)
	lease, err := acquireUnixDaemonSocketLease(sock, true)
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("unix", lease.anchor)
	if err != nil {
		t.Fatal(err)
	}
	listener.(*net.UnixListener).SetUnlinkOnClose(false)
	if err := os.Link(lease.anchor, sock); err != nil {
		t.Fatal(err)
	}
	_ = listener.Close()
	lease.close()
	if err := lease.recover(); err == nil {
		t.Fatal("recovery without a held lease succeeded")
	}
	if _, err := os.Lstat(sock); err != nil {
		t.Fatalf("socket removed without lease: %v", err)
	}
}

func TestUnixDaemonLeaseRejectsChangedDirectoriesAndAnchor(t *testing.T) {
	if !unixDaemonPeerVerificationAvailable() {
		t.Skip("Unix daemon lease unavailable")
	}
	for _, kind := range []string{"lease directory replaced", "parent directory replaced", "lease symlink", "anchor file", "anchor symlink", "anchor only stale", "anchor only live"} {
		t.Run(kind, func(t *testing.T) {
			sock := testUnixDaemonLeasePath(t)
			lease, err := acquireUnixDaemonSocketLease(sock, true)
			if err != nil {
				t.Fatal(err)
			}
			defer lease.close()
			var listener net.Listener
			switch kind {
			case "lease directory replaced":
				if err = os.Rename(lease.dirPath, lease.dirPath+"-old"); err == nil {
					err = os.Mkdir(lease.dirPath, 0o700)
				}
			case "parent directory replaced":
				parent := filepath.Dir(sock)
				if err = os.Rename(parent, parent+"-old"); err == nil {
					t.Cleanup(func() { _ = os.RemoveAll(parent + "-old") })
					err = os.Mkdir(parent, 0o700)
				}
			case "lease symlink":
				lease.close()
				if err = os.Remove(lease.dirPath); err == nil {
					err = os.Symlink(filepath.Dir(sock), lease.dirPath)
				}
				if err == nil {
					var other *unixDaemonSocketLease
					other, err = acquireUnixDaemonSocketLease(sock, true)
					if err == nil {
						other.close()
						t.Fatal("lease directory symlink accepted")
					}
					return
				}
			case "anchor file":
				err = os.WriteFile(lease.anchor, []byte("preserve"), 0o600)
			case "anchor symlink":
				err = os.Symlink(sock, lease.anchor)
			case "anchor only stale", "anchor only live":
				listener, err = net.Listen("unix", lease.anchor)
				if err == nil {
					listener.(*net.UnixListener).SetUnlinkOnClose(false)
					if kind == "anchor only stale" {
						_ = listener.Close()
					} else {
						defer listener.Close()
					}
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			err = lease.recover()
			if kind == "anchor only stale" {
				if err != nil {
					t.Fatal(err)
				}
				if _, err := os.Lstat(lease.anchor); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("stale anchor remains: %v", err)
				}
			} else if err == nil {
				t.Fatalf("%s accepted", kind)
			}
		})
	}
}

func TestUnixDaemonSignalKilledRestarts(t *testing.T) {
	if !unixDaemonPeerVerificationAvailable() {
		t.Skip("Unix daemon lease unavailable")
	}
	sock := testUnixDaemonLeasePath(t)
	dir := filepath.Dir(sock)
	t.Setenv("HOME", dir)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(dir, "data"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(dir, "state"))
	t.Setenv("ROLLING_MEMORY_CONFIG", filepath.Join(dir, "config.toml"))
	t.Setenv("ROLLING_MEMORY_DB", filepath.Join(dir, "memory.db"))
	t.Setenv("ROLLING_MEMORY_LOG", filepath.Join(dir, "memory.log"))
	t.Setenv("ROLLING_MEMORY_MODE", "local")
	cfg, err := config.Load(config.LoadOptions{})
	if err != nil {
		t.Fatal(err)
	}
	start := func() *exec.Cmd {
		t.Helper()
		cmd := exec.Command(os.Args[0], "-test.run=^TestUnixDaemonSignalHelper$")
		cmd.Env = append(os.Environ(), "RM_SIGNAL_SOCKET="+sock)
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		return cmd
	}
	first := start()
	waited := false
	defer func() {
		if !waited {
			_ = first.Process.Kill()
			_ = first.Wait()
		}
	}()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := PingUnixDaemon(sock, 100*time.Millisecond); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("first daemon did not start")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := first.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = first.Wait()
	waited = true
	restarted := start()
	restartedWaited := false
	defer func() {
		if !restartedWaited {
			_ = restarted.Process.Kill()
			_ = restarted.Wait()
		}
	}()
	if err := waitUnixDaemon(context.Background(), sock, cfg); err != nil {
		t.Fatalf("restart after kill: %v", err)
	}
	if err := restarted.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	if err := restarted.Wait(); err != nil {
		t.Fatal(err)
	}
	restartedWaited = true
	if _, err := os.Lstat(sock); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("restarted daemon left socket after SIGTERM: %v", err)
	}
}

func TestUnixDaemonShutdownDrainsSlowRequest(t *testing.T) {
	if !unixDaemonPeerVerificationAvailable() {
		t.Skip("Unix daemon lease unavailable")
	}
	for _, mode := range []string{"sigterm", "control"} {
		t.Run(mode, func(t *testing.T) {
			sock := testUnixDaemonLeasePath(t)
			dir := filepath.Dir(sock)
			cmd := exec.Command(os.Args[0], "-test.run=^TestUnixDaemonSignalHelper$")
			cmd.Env = []string{"HOME=" + dir, "TMPDIR=" + dir, "PATH=/usr/bin:/bin", "RM_SIGNAL_SOCKET=" + sock,
				"ROLLING_MEMORY_CONFIG=" + filepath.Join(dir, "config.toml"), "ROLLING_MEMORY_DB=" + filepath.Join(dir, "memory.db"),
				"ROLLING_MEMORY_LOG=" + filepath.Join(dir, "memory.log"), "ROLLING_MEMORY_MODE=local"}
			var output bytes.Buffer
			cmd.Stdout, cmd.Stderr = &output, &output
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			waited := false
			defer func() {
				if !waited {
					_ = cmd.Process.Kill()
					_ = cmd.Wait()
				}
			}()
			deadline := time.Now().Add(5 * time.Second)
			for {
				if _, err := PingUnixDaemon(sock, 100*time.Millisecond); err == nil {
					break
				}
				if time.Now().After(deadline) {
					_ = cmd.Process.Kill()
					_ = cmd.Wait()
					waited = true
					t.Fatalf("helper did not become healthy: %s", output.String())
				}
				time.Sleep(10 * time.Millisecond)
			}
			conn, err := net.Dial("unix", sock)
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			body := `{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"name":"recall","arguments":{"cue":"drain","mode":"probe"}}}`
			if _, err := fmt.Fprintf(conn, "POST /mcp HTTP/1.1\r\nHost: localhost\r\nContent-Type: application/json\r\nExpect: 100-continue\r\nContent-Length: %d\r\n\r\n", len(body)); err != nil {
				t.Fatal(err)
			}
			reader := bufio.NewReader(conn)
			_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
			interim, err := http.ReadResponse(reader, &http.Request{Method: http.MethodPost})
			if err != nil || interim.StatusCode != http.StatusContinue {
				t.Fatalf("handler did not start reading the request: response=%v err=%v", interim, err)
			}
			_ = interim.Body.Close()
			switch mode {
			case "sigterm":
				if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
					t.Fatal(err)
				}
			case "control":
				resp, err := newUnixHTTPClient(sock, 2*time.Second).Post("http://unix/v1/shutdown", "application/json", strings.NewReader(`{}`))
				if err != nil {
					t.Fatal(err)
				}
				raw, readErr := io.ReadAll(resp.Body)
				_ = resp.Body.Close()
				if readErr != nil || resp.StatusCode != http.StatusOK || !bytes.Contains(raw, []byte(`"ok":true`)) {
					t.Fatalf("shutdown control reply: status=%d body=%s err=%v", resp.StatusCode, raw, readErr)
				}
			}
			time.Sleep(300 * time.Millisecond)
			if _, err := conn.Write([]byte(body)); err != nil {
				_ = cmd.Process.Kill()
				_ = cmd.Wait()
				waited = true
				t.Fatalf("in-flight request was cut before it could finish: %v", err)
			}
			_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
			resp, err := http.ReadResponse(reader, &http.Request{Method: http.MethodPost})
			if err != nil {
				_ = cmd.Process.Kill()
				_ = cmd.Wait()
				waited = true
				t.Fatalf("in-flight response was cut: %v (helper: %s)", err, output.String())
			}
			raw, readErr := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			if readErr != nil || resp.StatusCode != http.StatusOK || !bytes.Contains(raw, []byte(`"jsonrpc"`)) {
				t.Fatalf("in-flight response incomplete: status=%d body=%s err=%v", resp.StatusCode, raw, readErr)
			}
			_ = conn.Close()
			if err := cmd.Wait(); err != nil {
				waited = true
				t.Fatalf("helper exit: %v %s", err, output.String())
			}
			waited = true
			if _, err := os.Lstat(sock); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("socket left behind: %v", err)
			}
		})
	}
}

func TestUnixDaemonBandPathRunsAndRecovers(t *testing.T) {
	if !unixDaemonPeerVerificationAvailable() {
		t.Skip("Unix daemon lease unavailable")
	}
	base, err := os.MkdirTemp("/tmp", "rm-band-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(base) })
	deep := filepath.Join(base, strings.Repeat("d", 45))
	if err := os.Mkdir(deep, 0o700); err != nil {
		t.Fatal(err)
	}
	sock := filepath.Join(deep, "engine.sock")
	if len(sock) > maxUnixSocketPathLen || len(filepath.Dir(sock))+len("/.rm-socket-")+16+len("/socket") <= maxUnixSocketPathLen {
		t.Fatalf("fixture must exercise the band that only the reserved-space rule migrated: %d", len(sock))
	}
	t.Setenv("HOME", base)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(base, "config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(base, "data"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(base, "state"))
	t.Setenv("ROLLING_MEMORY_CONFIG", filepath.Join(base, "config.toml"))
	t.Setenv("ROLLING_MEMORY_DB", filepath.Join(base, "memory.db"))
	t.Setenv("ROLLING_MEMORY_LOG", filepath.Join(base, "memory.log"))
	t.Setenv("ROLLING_MEMORY_MODE", "local")
	cfg, err := config.Load(config.LoadOptions{})
	if err != nil {
		t.Fatal(err)
	}
	start := func() *exec.Cmd {
		t.Helper()
		cmd := exec.Command(os.Args[0], "-test.run=^TestUnixDaemonSignalHelper$")
		cmd.Env = append(os.Environ(), "RM_SIGNAL_SOCKET="+sock)
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		return cmd
	}
	first := start()
	waited := false
	defer func() {
		if !waited {
			_ = first.Process.Kill()
			_ = first.Wait()
		}
	}()
	if err := waitUnixDaemon(context.Background(), sock, cfg); err != nil {
		_ = first.Process.Kill()
		_ = first.Wait()
		waited = true
		t.Fatalf("band-path daemon did not publish at %s: %v", sock, err)
	}
	if info, err := os.Lstat(sock); err != nil || info.Mode()&os.ModeSocket == 0 {
		t.Fatalf("published path is not the requested socket: %v", err)
	}
	if leaseDir := unixDaemonLeaseDir(sock); strings.HasPrefix(leaseDir, deep) {
		t.Fatalf("lease directory should fall back to the temp dir: %s", leaseDir)
	}
	if err := first.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = first.Wait()
	waited = true
	second := start()
	secondWaited := false
	defer func() {
		if !secondWaited {
			_ = second.Process.Kill()
			_ = second.Wait()
		}
	}()
	if err := waitUnixDaemon(context.Background(), sock, cfg); err != nil {
		_ = second.Process.Kill()
		_ = second.Wait()
		secondWaited = true
		t.Fatalf("band-path daemon did not recover at %s: %v", sock, err)
	}
	if err := second.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	if err := second.Wait(); err != nil {
		t.Fatal(err)
	}
	secondWaited = true
	if _, err := os.Lstat(sock); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("band-path daemon left socket behind: %v", err)
	}
}

func TestUnixDaemonConcurrentRecoveryAndStart(t *testing.T) {
	if !unixDaemonPeerVerificationAvailable() {
		t.Skip("Unix daemon lease unavailable")
	}
	memory := newMemory(t)
	sock := testUnixDaemonLeasePath(t)
	lease, err := acquireUnixDaemonSocketLease(sock, true)
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("unix", lease.anchor)
	if err != nil {
		t.Fatal(err)
	}
	listener.(*net.UnixListener).SetUnlinkOnClose(false)
	if err := os.Link(lease.anchor, sock); err != nil {
		t.Fatal(err)
	}
	_ = listener.Close()
	lease.close()
	var wg sync.WaitGroup
	var running atomic.Int64
	start := make(chan struct{})
	servers := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			servers <- ServeUnix(sock, memory, func(context.Context) { running.Add(1) })
		}()
	}
	close(start)
	if err := waitUnixDaemon(context.Background(), sock, memory.Config); err != nil {
		t.Fatalf("concurrent startup: %v", err)
	}
	if running.Load() != 1 {
		t.Errorf("running=%d", running.Load())
	}
	if err := requestUnixDaemonControl(sock, "/v1/shutdown", memory.Config); err != nil {
		t.Fatal(err)
	}
	wg.Wait()
	close(servers)
	var success int
	for err := range servers {
		if err == nil {
			success++
		}
	}
	if success != 1 {
		t.Fatalf("successful daemons: %d", success)
	}
}

func TestUnixDaemonSlowHealthAndLegacyMismatchPreserved(t *testing.T) {
	if !unixDaemonPeerVerificationAvailable() {
		t.Skip("Unix daemon peer verification unavailable")
	}
	memory := newMemory(t)
	for _, slow := range []bool{true, false} {
		t.Run(fmt.Sprintf("slow=%v", slow), func(t *testing.T) {
			sock := testUnixDaemonLeasePath(t)
			listener, err := net.Listen("unix", sock)
			if err != nil {
				t.Fatal(err)
			}
			var requests atomic.Int64
			server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				if slow {
					time.Sleep(2 * daemonPingTimeout)
				}
				writeJSON(w, http.StatusOK, map[string]any{"ok": true, "pid": os.Getpid()})
			})}
			go func() { _ = server.Serve(listener) }()
			defer server.Close()
			before, err := os.Lstat(sock)
			if err != nil {
				t.Fatal(err)
			}
			err = ensureUnixDaemonContext(context.Background(), sock, memory.Config)
			after, statErr := os.Lstat(sock)
			if err == nil || statErr != nil || !os.SameFile(before, after) || requests.Load() != 1 {
				t.Fatalf("err=%v stat=%v requests=%d", err, statErr, requests.Load())
			}
			conn, err := net.DialTimeout("unix", sock, time.Second)
			if err != nil {
				t.Fatalf("existing listener disturbed: %v", err)
			}
			_ = conn.Close()
		})
	}
}

func TestUnixDaemonSignalHelper(t *testing.T) {
	sock := os.Getenv("RM_SIGNAL_SOCKET")
	if sock == "" {
		return
	}
	if err := ServeUnix(sock, nil, nil); err != nil {
		t.Fatal(err)
	}
}

func TestUnixDaemonSignalCleanup(t *testing.T) {
	if !unixDaemonPeerVerificationAvailable() {
		t.Skip("Unix daemon lease unavailable")
	}
	for _, sig := range []os.Signal{syscall.SIGTERM, os.Interrupt, os.Kill} {
		t.Run(sig.String(), func(t *testing.T) {
			sock := testUnixDaemonLeasePath(t)
			dir := filepath.Dir(sock)
			cmd := exec.Command(os.Args[0], "-test.run=^TestUnixDaemonSignalHelper$")
			cmd.Env = []string{"HOME=" + dir, "TMPDIR=" + dir, "PATH=/usr/bin:/bin", "RM_SIGNAL_SOCKET=" + sock,
				"ROLLING_MEMORY_CONFIG=" + filepath.Join(dir, "config.toml"), "ROLLING_MEMORY_DB=" + filepath.Join(dir, "memory.db"),
				"ROLLING_MEMORY_LOG=" + filepath.Join(dir, "memory.log"), "ROLLING_MEMORY_MODE=local"}
			var output bytes.Buffer
			cmd.Stdout, cmd.Stderr = &output, &output
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			waited := false
			defer func() {
				if !waited {
					_ = cmd.Process.Kill()
					_ = cmd.Wait()
				}
			}()
			deadline := time.Now().Add(5 * time.Second)
			for {
				if _, err := PingUnixDaemon(sock, 100*time.Millisecond); err == nil {
					break
				}
				if time.Now().After(deadline) {
					t.Fatalf("helper not healthy: %s", output.String())
				}
				time.Sleep(10 * time.Millisecond)
			}
			if err := cmd.Process.Signal(sig); err != nil {
				t.Fatal(err)
			}
			err := cmd.Wait()
			waited = true
			if sig == syscall.SIGTERM {
				if err != nil {
					t.Fatalf("SIGTERM exit: %v %s", err, output.String())
				}
				if _, err := os.Lstat(sock); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("SIGTERM left socket: %v", err)
				}
			} else {
				if err == nil {
					t.Fatalf("%s did not terminate process", sig)
				}
				var exitErr *exec.ExitError
				if !errors.As(err, &exitErr) {
					t.Fatalf("%s exit: %v", sig, err)
				}
				if status, ok := exitErr.Sys().(syscall.WaitStatus); !ok || !status.Signaled() || status.Signal() != sig {
					t.Fatalf("%s did not retain signal termination: %v", sig, exitErr.Sys())
				}
				if info, err := os.Lstat(sock); err != nil || info.Mode()&os.ModeSocket == 0 {
					t.Fatalf("%s did not leave socket: %v", sig, err)
				}
				lease, err := acquireUnixDaemonSocketLease(sock, false)
				if err != nil {
					t.Fatal(err)
				}
				if err := lease.recover(); err != nil {
					lease.close()
					t.Fatalf("recover after %s: %v", sig, err)
				}
				lease.close()
			}
		})
	}
}

func TestUnixDaemonRejectsNonLocalAndChangedConfig(t *testing.T) {
	memory := newMemory(t)
	dir, err := os.MkdirTemp("/tmp", "rm-config-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	sock := filepath.Join(dir, "engine.sock")
	memory.Config.Mode = "cloud"
	if err := ServeUnix(sock, memory, nil); err == nil {
		t.Fatal("non-local daemon must not start")
	}
	if _, err := os.Lstat(sock); !os.IsNotExist(err) {
		t.Fatalf("non-local daemon created a socket: %v", err)
	}
	memory.Config.Mode = "local"
	t.Setenv(DaemonSocketEnv, sock)
	t.Setenv("ROLLING_MEMORY_CONFIG", memory.Config.Path)
	t.Setenv("ROLLING_MEMORY_DB", memory.Config.DB)
	t.Setenv("ROLLING_MEMORY_LOG", memory.Config.Log)
	t.Setenv("ROLLING_MEMORY_MODE", memory.Config.Mode)
	t.Setenv("ROLLING_MEMORY_SCOPE", memory.Config.Scope)
	t.Setenv("ROLLING_MEMORY_READER", memory.Config.Reader)
	done := make(chan error, 1)
	go func() { done <- ServeUnix(sock, memory, nil) }()
	t.Cleanup(func() {
		requestUnixDaemonShutdown(sock)
		select {
		case <-done:
		case <-time.After(2 * time.Second):
		}
	})
	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, err := PingUnixDaemon(sock, 100*time.Millisecond); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("daemon did not become healthy")
		}
		time.Sleep(15 * time.Millisecond)
	}
	t.Setenv("ROLLING_MEMORY_SCOPE", "mem:other")
	response, handled := ForwardToolCallToUnixDaemon(7, map[string]any{"cue": "test"}, nil)
	if !handled || !isError(t, response) || !strings.Contains(textOf(t, response), "identity mismatch") {
		t.Fatalf("configuration mismatch should fail closed: %#v", response)
	}
	t.Setenv("ROLLING_MEMORY_MODE", "cloud")
	response, handled = ForwardToolCallToUnixDaemon(8, map[string]any{"cue": "test"}, nil)
	if !handled || !isError(t, response) || !strings.Contains(textOf(t, response), "mode must be one of ('local')") {
		t.Fatalf("non-local request should fail closed: %#v", response)
	}
}

// TestUnixDaemonDocumentsResolveBackgroundChunkBody 锁定 L1 背景分块正文的读取通道：
// 展示层（Admin 控制台）不能导入 localvault，只能经守护进程 /v1/documents 取规范正文；
// 守护进程必须从挂载目录的源文件现场重读重切，返回分块正文，而不是让调用方拿路径坐标代替。
func TestUnixDaemonDocumentsResolveBackgroundChunkBody(t *testing.T) {
	memory := newMemory(t)
	tc, err := memory.LocalVault()
	if err != nil || tc == nil || tc.Vault == nil {
		t.Fatalf("local vault: %v", err)
	}
	dir := t.TempDir()
	chunkBody := "# Emergency Failover Runbook\nExecute command `aurora-ctl drain --graceful` before replacing optical transceivers.\n"
	if err := os.WriteFile(filepath.Join(dir, "runbook.md"), []byte(chunkBody), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	if _, err := tc.AddBackgroundDir(dir); err != nil {
		t.Fatalf("AddBackgroundDir: %v", err)
	}
	if _, err := tc.SyncBackgroundDirs(context.Background()); err != nil {
		t.Fatalf("SyncBackgroundDirs: %v", err)
	}

	db, err := sql.Open("sqlite", memory.Config.DB)
	if err != nil {
		t.Fatalf("open vault: %v", err)
	}
	var unitID string
	if err := db.QueryRow(`SELECT unit_id FROM rm_bg_chunks LIMIT 1`).Scan(&unitID); err != nil {
		_ = db.Close()
		t.Fatalf("background chunk was not ingested: %v", err)
	}
	_ = db.Close()

	sockDir, err := os.MkdirTemp("/tmp", "rm-doc-")
	if err != nil {
		t.Fatalf("mkdir socket dir: %v", err)
	}
	sockPath := filepath.Join(sockDir, "engine.sock")
	t.Cleanup(func() { _ = os.RemoveAll(sockDir) })

	done := make(chan error, 1)
	go func() {
		done <- ServeUnix(sockPath, memory, nil)
	}()
	t.Cleanup(func() {
		requestUnixDaemonShutdown(sockPath)
		select {
		case <-done:
		case <-time.After(2 * time.Second):
		}
	})
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := PingUnixDaemon(sockPath, 100*time.Millisecond); err == nil {
			break
		}
		time.Sleep(15 * time.Millisecond)
	}

	request := func(payload UnixDocumentPayload) UnixDocumentResponse {
		t.Helper()
		raw, err := json.Marshal(payload)
		if err != nil {
			t.Fatalf("marshal payload: %v", err)
		}
		client := newUnixHTTPClient(sockPath, 5*time.Second)
		resp, err := client.Post("http://unix/v1/documents", "application/json", bytes.NewReader(raw))
		if err != nil {
			t.Fatalf("POST /v1/documents: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != 200 {
			t.Fatalf("POST /v1/documents status = %d", resp.StatusCode)
		}
		var out UnixDocumentResponse
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
			t.Fatalf("decode /v1/documents: %v", err)
		}
		return out
	}

	out := request(UnixDocumentPayload{DB: memory.Config.DB, Items: []UnixDocumentItem{{UnitID: unitID}}})
	if !out.OK || len(out.Docs) != 1 {
		t.Fatalf("documents response: %+v", out)
	}
	if out.Docs[0].Error != "" || !strings.Contains(out.Docs[0].RawText, "aurora-ctl drain --graceful") {
		t.Fatalf("chunk body must be the source text, got %+v", out.Docs[0])
	}

	// Unresolvable unit ids are per-item errors, never a batch-wide failure: the display
	// layer keeps its location preview for that row only.
	out = request(UnixDocumentPayload{Items: []UnixDocumentItem{{UnitID: "bg:nonexistent#0"}}})
	if !out.OK || len(out.Docs) != 1 || out.Docs[0].Error == "" {
		t.Fatalf("missing unit must answer with a per-item error: %+v", out)
	}
}

// TestRetryableDaemonTransport pins which failures earn the single recall retry:
// a closed listener between the health probe and the request is transient, while
// an engine or protocol error is a real answer that must not be replayed.
func TestRetryableDaemonTransport(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want bool
	}{
		{"eof", io.EOF, true},
		{"unexpected eof", fmt.Errorf("daemon recall outcome unknown: %w", io.ErrUnexpectedEOF), true},
		{"reset", fmt.Errorf("write: %w", syscall.ECONNRESET), true},
		{"broken pipe", syscall.EPIPE, true},
		{"engine error", &EngineRecallError{Msg: "bad query"}, false},
		{"protocol error", errors.New("invalid daemon response stream"), false},
	} {
		if got := retryableDaemonTransport(tc.err); got != tc.want {
			t.Fatalf("%s: retryable=%v want=%v", tc.name, got, tc.want)
		}
	}
}
