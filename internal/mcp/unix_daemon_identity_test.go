// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package mcp

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/rocareer/rolling-memory/internal/config"
)

// TestUnixDaemonConfigIDIgnoresOutOfBandPreferences pins the identity boundary:
// knobs the daemon never reads must not invalidate a live daemon, while
// retrieval-semantics inputs must still change the identity.
func TestUnixDaemonConfigIDIgnoresOutOfBandPreferences(t *testing.T) {
	memory := newMemory(t)
	base, err := unixDaemonConfigID(memory.Config)
	if err != nil {
		t.Fatal(err)
	}

	preferences := *memory.Config
	preferences.Daemon.MemoryLimit = "512MiB"
	preferences.Daemon.SnapshotInterval = 5 * time.Minute
	preferences.Eval.Model = "judge-model"
	preferences.Eval.Judge = "judge"
	preferences.Update.Channel = "stable"
	preferences.Update.Source = "gitee"
	got, err := unixDaemonConfigID(&preferences)
	if err != nil {
		t.Fatal(err)
	}
	if got != base {
		t.Fatalf("out-of-band preferences must not change the daemon identity: %s != %s", got, base)
	}

	for _, tc := range []struct {
		name   string
		mutate func(*config.Config)
	}{
		{"scope", func(c *config.Config) { c.Scope += "-other" }},
		{"recall chars", func(c *config.Config) { c.Recall.Chars++ }},
		{"reader", func(c *config.Config) { c.Reader = "owner" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			changed := *memory.Config
			tc.mutate(&changed)
			got, err := unixDaemonConfigID(&changed)
			if err != nil {
				t.Fatal(err)
			}
			if got == base {
				t.Fatalf("%s must change the daemon identity", tc.name)
			}
		})
	}
}

// TestUnixDaemonShutdownRetiresIdentityMismatch covers the recovery path after
// a configuration-only change: the daemon is still serving this vault, so an
// explicit stop/restart must retire it instead of dead-ending on the handshake.
func TestUnixDaemonShutdownRetiresIdentityMismatch(t *testing.T) {
	memory := newMemory(t)
	dir, err := os.MkdirTemp("/tmp", "rm-identity-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	sock := filepath.Join(dir, "engine.sock")
	t.Setenv("ROLLING_MEMORY_CONFIG", memory.Config.Path)
	t.Setenv("ROLLING_MEMORY_DB", memory.Config.DB)
	t.Setenv("ROLLING_MEMORY_LOG", memory.Config.Log)
	t.Setenv("ROLLING_MEMORY_MODE", memory.Config.Mode)
	t.Setenv("ROLLING_MEMORY_SCOPE", memory.Config.Scope+"-other")
	t.Setenv("ROLLING_MEMORY_READER", memory.Config.Reader)
	caller, err := config.Load(config.LoadOptions{})
	if err != nil {
		t.Fatal(err)
	}
	callerID, err := unixDaemonConfigID(caller)
	if err != nil {
		t.Fatal(err)
	}
	servedID, err := unixDaemonConfigID(memory.Config)
	if err != nil {
		t.Fatal(err)
	}
	if callerID == servedID {
		t.Fatal("test setup must produce a configuration identity mismatch")
	}

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
		t.Fatalf("shutdown must retire a same-vault daemon with a different config identity: %v", err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("daemon did not exit")
	}
}
