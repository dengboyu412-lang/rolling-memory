// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package cli

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/rocareer/rolling-memory/internal/config"
	"github.com/rocareer/rolling-memory/internal/memory/facade"
)

const (
	// snapshotBuildRetryAfter is the failure backoff. A healthy builder follows
	// the configured cadence instead.
	snapshotBuildRetryAfter = 2 * time.Minute
	// snapshotBuildMaxFailures stops the restart loop after a persistent fault
	// (disk full, corrupt vault); the status row keeps the reason.
	snapshotBuildMaxFailures = 5
	snapshotBuildStatusKey   = "snapshot_build_status"
)

// snapshotBuildDue reports whether the background builder should run. A missing
// or stale generation is built immediately; otherwise the builder waits for the
// configured cadence. Failures use the retry floor instead of the cadence.
func snapshotBuildDue(now time.Time, lastNS int64, failures int32, interval time.Duration, ready, known bool, gap int64) bool {
	if ready && known && gap <= 0 {
		return false
	}
	if lastNS == 0 {
		return true
	}
	wait := interval
	if wait <= 0 {
		wait = config.DefaultSnapshotInterval
	}
	if failures > 0 {
		wait = snapshotBuildRetryAfter
	}
	return now.Sub(time.Unix(0, lastNS)) >= wait
}

// ensureSnapshotBuilder makes sure a read-only recall surface has a durable
// disk cluster and schedules the background builder. The builder publishes on a
// fixed cadence (default 60s) and only when changes are pending; the first
// generation is built immediately. The builder is a separate process, so its
// memory and CPU never enter the recall runtime.
func (a *application) ensureSnapshotBuilder(ctx context.Context, memory *facade.Memory) {
	if a == nil || memory == nil || memory.Config == nil || !memory.SnapshotOnly() {
		return
	}
	if !IsAutoSyncEnabled(memory.Config.Path) {
		return
	}
	if IsEpisodicSyncPaused(memory.Store.DB(), memory.Config.Path) {
		return
	}
	if a.snapshotBuildRunning.Load() {
		return
	}
	if a.snapshotBuildFailures.Load() >= snapshotBuildMaxFailures {
		// A manual `rmem build` clears the status row; let that unblock the
		// daemon without a restart.
		if failures := readSnapshotBuildFailureCount(memory.Store.DB()); failures < snapshotBuildMaxFailures {
			a.snapshotBuildFailures.Store(int32(failures))
		} else {
			return
		}
	}
	ready := memory.PublishedSnapshotReady()
	gap, known := memory.SnapshotChangeGap(ctx)
	if !known {
		gap, known = memory.SnapshotBuildGap(ctx)
	}
	if !snapshotBuildDue(time.Now(), a.snapshotBuildLastNS.Load(), a.snapshotBuildFailures.Load(),
		memory.Config.Daemon.EffectiveSnapshotInterval(), ready, known, gap) {
		return
	}
	if !a.snapshotBuildRunning.CompareAndSwap(false, true) {
		return
	}
	cfg := *memory.Config
	db := memory.Store.DB()
	go func() {
		defer a.snapshotBuildRunning.Store(false)
		err := runSnapshotBuilder(cfg)
		if err != nil {
			failures := a.snapshotBuildFailures.Add(1)
			writeSnapshotBuildStatus(db, int(failures), err.Error())
		} else {
			a.snapshotBuildFailures.Store(0)
			writeSnapshotBuildStatus(db, 0, "")
		}
		a.snapshotBuildLastNS.Store(time.Now().UnixNano())
	}()
}

// writeSnapshotBuildStatus records the builder outcome for `scan status`. A
// persistent fault keeps the reason instead of silently retrying forever.
func writeSnapshotBuildStatus(db *sql.DB, failures int, lastError string) {
	if db == nil {
		return
	}
	raw, err := json.Marshal(map[string]any{
		"failures":   failures,
		"last_error": lastError,
		"blocked":    failures >= snapshotBuildMaxFailures,
		"at":         time.Now().Unix(),
	})
	if err != nil {
		return
	}
	_, _ = db.Exec(`INSERT INTO rm_meta(k, v) VALUES (?, ?) ON CONFLICT(k) DO UPDATE SET v = excluded.v`,
		snapshotBuildStatusKey, raw)
}

// readSnapshotBuildFailureCount reads the durable failure counter. A manual
// successful build clears it.
func readSnapshotBuildFailureCount(db *sql.DB) int {
	if db == nil {
		return 0
	}
	var raw []byte
	if err := db.QueryRow(`SELECT v FROM rm_meta WHERE k = ?`, snapshotBuildStatusKey).Scan(&raw); err != nil {
		return 0
	}
	var status struct {
		Failures int `json:"failures"`
	}
	if json.Unmarshal(raw, &status) != nil {
		return 0
	}
	return status.Failures
}

// runSnapshotBuilder executes `rolling-memory build` in its own process. The
// child owns the build lock, so a manual build and a daemon build never race.
func runSnapshotBuilder(cfg config.Config) error {
	exe, err := os.Executable()
	if err != nil || exe == "" {
		return fmt.Errorf("resolve executable: %w", err)
	}
	args := []string{"build", "--config", cfg.Path, "--db", cfg.DB, "--mode", cfg.Mode, "--scope", cfg.Scope}
	cmd := exec.Command(exe, args...)
	cmd.Env = append(os.Environ(), "ROLLING_MEMORY_BUILD_BACKGROUND=1", "GOMAXPROCS=2")
	logPath := cfg.Log + ".snapshot-build"
	if cfg.Log == "" {
		logPath = cfg.DB + ".snapshot-build.log"
	}
	if err := os.MkdirAll(filepath.Dir(logPath), 0o700); err == nil {
		if handle, openErr := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600); openErr == nil {
			cmd.Stdout = handle
			cmd.Stderr = handle
			defer func() { _ = handle.Close() }()
		}
	}
	if err := cmd.Run(); err != nil {
		log.Printf("snapshot builder: %v", err)
		return err
	}
	log.Printf("snapshot builder: published %s", cfg.DB)
	return nil
}
