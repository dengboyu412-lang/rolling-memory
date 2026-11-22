// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package cli

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/rocareer/rolling-memory/internal/config"
	"github.com/rocareer/rolling-memory/internal/episodic"
	"github.com/rocareer/rolling-memory/internal/episodicunit"
	"github.com/rocareer/rolling-memory/internal/harness"
	"github.com/rocareer/rolling-memory/internal/hostid"
	"github.com/rocareer/rolling-memory/internal/ids"
	"github.com/rocareer/rolling-memory/internal/mcp"
	"github.com/rocareer/rolling-memory/internal/memory/facade"
	"github.com/rocareer/rolling-memory/internal/memory/localvault"
	"github.com/rocareer/rolling-memory/internal/text"
)

const reconcileDefaultLimit = 50

// HarnessEnvItem describes one coding harness environment on this machine.
type HarnessEnvItem struct {
	ID             string `json:"id"`
	Label          string `json:"label"`
	Detected       bool   `json:"detected"`
	Active         bool   `json:"active"`
	SkillWired     bool   `json:"skill_wired"`
	MCPWired       bool   `json:"mcp_wired"`
	HooksWired     bool   `json:"hooks_wired"`
	HooksSupported bool   `json:"hooks_supported"`
	// SessionsScanSupported reports whether this build can discover the
	// harness's local session store, so the panel can show its session count.
	SessionsScanSupported bool `json:"sessions_scan_supported"`
	// TurnsScanSupported is false for harnesses this build can discover sessions
	// for but cannot read turn content from (compressed or unsupported transcript
	// schema): their 0/0 turn counts mean "not counted", not "no history".
	TurnsScanSupported bool `json:"turns_scan_supported"`
	SessionsCount      int  `json:"sessions_count"`
	// LocalTurnsCount is the importable turn count: completed turns inside the
	// configured sync.since bound. Out-of-bound history and turns without
	// confirmed completion are reported separately and never counted as pending
	// work the importer would not perform.
	LocalTurnsCount    int `json:"local_turns_count"`
	SyncedTurnsCount   int `json:"synced_turns_count"`
	UnsyncedTurnsCount int `json:"unsynced_turns_count"`
	// OutOfScopeTurnsCount counts scanned turns older than sync.since.
	OutOfScopeTurnsCount int `json:"out_of_scope_turns_count,omitempty"`
	// IncompleteTurnsCount counts turns without confirmed successful completion
	// (an open tail or an aborted run). They are not importable by contract.
	IncompleteTurnsCount int `json:"incomplete_turns_count,omitempty"`
	// ExcludedSubagentSourcesCount counts host-side subagent sources outside
	// automatic coverage. This is a documented scope limit surfaced as a count,
	// not an unreadable source.
	ExcludedSubagentSourcesCount int    `json:"excluded_subagent_sources_count,omitempty"`
	LastActiveAt                 int64  `json:"last_active_at"`
	LastActiveText               string `json:"last_active_text"`
}

// HealthCheckSubItem is one dimension of the Memory Health score.
type HealthCheckSubItem struct {
	Key      string `json:"key"`
	LabelZH  string `json:"label_zh"`
	LabelEN  string `json:"label_en"`
	Status   string `json:"status"` // "healthy" | "warning" | "error"
	Score    int    `json:"score"`
	MaxScore int    `json:"max_score"`
	DetailZH string `json:"detail_zh"`
	DetailEN string `json:"detail_en"`
}

// LayerAlignmentStat holds the local indexing state for one cognitive layer.
type LayerAlignmentStat struct {
	Layer                string `json:"layer"`
	LocalVaultCount      int    `json:"local_vault_count"`
	SyncedCount          int    `json:"synced_count"`
	HarnessUnsyncedTurns int    `json:"harness_unsynced_turns,omitempty"`
	MountedDirsCount     int    `json:"mounted_dirs_count,omitempty"`
	TrackedFilesCount    int    `json:"tracked_files_count,omitempty"`
	DirtyFilesCount      int    `json:"dirty_files_count,omitempty"`
	StaleCount           int    `json:"stale_count,omitempty"`
	SupersededCount      int    `json:"superseded_count,omitempty"`
	TotalPendingSync     int    `json:"total_pending_sync"`
	AlignmentPct         int    `json:"alignment_pct"`
	AlignmentKnown       bool   `json:"alignment_known"`
	Aligned              bool   `json:"aligned"`
}

// EpisodicIndexStatus reports local index coverage and live scan/backfill progress without reading source transcripts.
type EpisodicIndexStatus struct {
	Compatible       int     `json:"compatible"`
	Incompatible     int     `json:"incompatible"`
	Missing          int     `json:"missing"`
	Corrupt          int     `json:"corrupt"`
	NeedsRebuild     int     `json:"needs_rebuild"`
	Orphaned         int     `json:"orphaned"`
	TotalDocs        int     `json:"total_docs"`
	CoveragePct      float64 `json:"coverage_pct"`
	Paused           bool    `json:"paused"`
	Running          bool    `json:"running"`
	BackfillCursor   string  `json:"backfill_cursor,omitempty"`
	ScanSource       string  `json:"scan_source,omitempty"`
	ScanIndex        int     `json:"scan_index,omitempty"`
	SourceItemErrors int     `json:"source_item_errors,omitempty"`
	Error            string  `json:"error,omitempty"`
}

// EnvHealthAndSyncStatus is the unified payload for `rolling-memory sync status --json`
// and `/admin/plugin/rolling-memory/env/health`.
type EnvHealthAndSyncStatus struct {
	ActiveHarnessID    string `json:"active_harness_id"`
	ActiveHarnessLabel string `json:"active_harness_label"`
	ActiveWorkspace    string `json:"active_workspace"`
	ActiveSessionID    string `json:"active_session_id"`
	DetectionSource    string `json:"detection_source"`
	// Attachment is the live session the console should present as the
	// current environment. It is absent when no client holds a live lease.
	Attachment *hostid.Attachment `json:"attachment,omitempty"`
	// Attachments lists every live session, newest activity first.
	Attachments []hostid.Attachment `json:"attachments,omitempty"`
	// RecentHarnessID/Label describe the host with the freshest transcript or
	// last call. They are telemetry, never a substitute for Attachment.
	RecentHarnessID    string                        `json:"recent_harness_id,omitempty"`
	RecentHarnessLabel string                        `json:"recent_harness_label,omitempty"`
	Mode               string                        `json:"mode"`
	Harnesses          []HarnessEnvItem              `json:"harnesses"`
	HealthScore        int                           `json:"health_score"`
	HealthGrade        string                        `json:"health_grade"` // "healthy" | "subhealthy" | "degraded"
	HealthChecks       []HealthCheckSubItem          `json:"health_checks"`
	Layers             map[string]LayerAlignmentStat `json:"layers"`
	EpisodicIndex      *EpisodicIndexStatus          `json:"episodic_index,omitempty"`
	TotalPendingSync   int                           `json:"total_pending_sync"`
	UpdatedAt          string                        `json:"updated_at"`
}

// ReconcileReport is the execution receipt for `rolling-memory sync reconcile --json`
// and `/admin/plugin/rolling-memory/sync/reconcile`.
type ReconcileReport struct {
	Layer                        string `json:"layer"`
	TargetHarness                string `json:"target_harness"`
	EpisodicImportedFromHar      int    `json:"episodic_imported_from_harness"`
	EpisodicIndexesBackfilled    int    `json:"episodic_indexes_backfilled"`
	EpisodicIndexScanLimit       int    `json:"episodic_index_scan_limit_per_harness"`
	EpisodicIndexScanned         int    `json:"episodic_index_scanned"`
	EpisodicIndexSourceChanged   int    `json:"episodic_index_source_changed"`
	EpisodicIndexPointerMismatch int    `json:"episodic_index_pointer_mismatch"`
	EpisodicIndexUnverified      int    `json:"episodic_index_unverified"`
	EpisodicIndexCompatible      int    `json:"episodic_index_compatible"`
	EpisodicIndexIncompatible    int    `json:"episodic_index_incompatible"`
	EpisodicIndexMissing         int    `json:"episodic_index_missing"`
	EpisodicIndexCorrupt         int    `json:"episodic_index_corrupt"`
	EpisodicIndexNeedsRebuild    int    `json:"episodic_index_needs_rebuild"`
	BackgroundSyncedFiles        int    `json:"background_synced_files"`
	// ExcludedSubagentSources counts host-side subagent transcripts/sessions
	// that are outside automatic coverage by design. They are data completeness
	// scope, not a read failure, and never enter Warnings.
	ExcludedSubagentSources int                    `json:"excluded_subagent_sources,omitempty"`
	TotalReconciled         int                    `json:"total_reconciled"`
	ElapsedMS               int                    `json:"elapsed_ms"`
	Warnings                []string               `json:"warnings,omitempty"`
	Status                  EnvHealthAndSyncStatus `json:"status"`
}

// candidateEpisodicTurn represents one extracted User+Assistant conversational turn from a harness.
type candidateEpisodicTurn struct {
	HarnessID  string
	SessionID  string
	Locus      string
	LocusKnown bool
	DocID      string
	RefPointer string
	// TurnRef is the provenance of the unit (`@ref:zcode:...` / `@ref:jsonl:...`,
	// without the `#<k>` segment suffix). Content-addressed units can be said in
	// several turns; the lineage table keeps every occurrence.
	TurnRef    string
	Text       string
	OccurredAt int64
	// Completed requires an explicit successful completion event.
	Completed bool
}

func (a *application) runSyncCommand(memory *facade.Memory) (int, error) {
	sub := a.args.Subcommand()
	switch sub {
	case "status":
		// 控制台采集路径可经环境变量打开进度事件通道（stderr JSONL），CLI 直用保持静默。
		if beginStatusProgress() {
			defer endStatusProgress()
		}
		harnessFilter := strings.TrimSpace(a.args.String("harness"))
		st, err := inspectEnvHealthAndSync(context.Background(), memory, harnessFilter)
		if err != nil {
			return 1, err
		}
		return 0, a.emit(st, func() {
			active := "none detected"
			if st.ActiveHarnessID != "" {
				active = fmt.Sprintf("%s (%s)", st.ActiveHarnessLabel, st.ActiveHarnessID)
			}
			a.printf("active harness: %s · health: %d%% (%s) · pending sync: %d\n",
				active, st.HealthScore, st.HealthGrade, st.TotalPendingSync)
			if idx := st.EpisodicIndex; idx != nil {
				if idx.Error != "" {
					a.printf("episodic index: unavailable (%s)\n", idx.Error)
				} else {
					stateLabel := "idle"
					if idx.Paused {
						stateLabel = "paused"
					} else if idx.Running {
						stateLabel = "running"
					}
					a.printf("episodic index (%s, %.1f%%): %d/%d compatible · %d missing · %d incompatible · %d corrupt · %d need rebuild\n",
						stateLabel, idx.CoveragePct, idx.Compatible, idx.TotalDocs, idx.Missing, idx.Incompatible, idx.Corrupt, idx.NeedsRebuild)
					if idx.BackfillCursor != "" || idx.ScanSource != "" {
						a.printf("cursors: backfill=%s · scan=%s#%d\n", idx.BackfillCursor, idx.ScanSource, idx.ScanIndex)
					}
				}
			}
		})
	case "reconcile":
		if a.args.Flag("rebuild_episodic") {
			report, err := rebuildEpisodicUnits(context.Background(), memory, a.args.Flag("yes"))
			if err != nil {
				return 1, err
			}
			return 0, a.emit(report, func() {
				a.printf("episodic rebuild estimate: %d units → %d segments · +%d · unmatched %d · %s\n",
					report.OldUnits, report.NewUnits, report.UnitsAdded, report.UnitsRemoved, report.Mode)
				for _, warning := range report.Warnings {
					a.printf("warning: %s\n", warning)
				}
			})
		}
		layer := strings.ToLower(strings.TrimSpace(a.args.String("layer")))
		if layer == "" {
			layer = "all"
		}
		harnessFilter := strings.TrimSpace(a.args.String("harness"))
		limit := a.args.Int("limit")
		if limit <= 0 {
			limit = reconcileDefaultLimit
		}
		scanLimit := a.args.Int("scan_limit")
		if scanLimit <= 0 {
			scanLimit = 2000
		}
		if scanLimit > 50000 {
			scanLimit = 50000
		}
		rep, err := executeReconcileSyncWithScanLimit(context.Background(), memory, layer, harnessFilter, limit, scanLimit)
		if err != nil {
			return 1, err
		}
		return 0, a.emit(rep, func() {
			a.printf("reconcile (%s, local_plain): harness +%d · background files +%d · health %d%%\n",
				rep.Layer, rep.EpisodicImportedFromHar, rep.BackgroundSyncedFiles, rep.Status.HealthScore)
			a.printf("episodic index: +%d backfilled · %d compatible · %d missing · %d incompatible · %d corrupt · %d need rebuild\n",
				rep.EpisodicIndexesBackfilled, rep.EpisodicIndexCompatible, rep.EpisodicIndexMissing, rep.EpisodicIndexIncompatible, rep.EpisodicIndexCorrupt, rep.EpisodicIndexNeedsRebuild)
			for _, warning := range rep.Warnings {
				a.printf("warning: %s\n", warning)
			}
		})
	case "prune":
		report, err := pruneEpisodicUnitsWithOptions(context.Background(), memory, EpisodicPruneOptions{
			Limit:               a.args.Int("limit"),
			Apply:               a.args.Flag("apply"),
			Confirmed:           a.args.Flag("yes"),
			IncludeUnreadable:   a.args.Flag("include_unreadable"),
			ReanchorCrossSource: a.args.Flag("reanchor_cross_source"),
		})
		if err != nil {
			return 1, err
		}
		return 0, a.emit(report, func() { a.printEpisodicPrune(report) })
	case "watch":
		if a.args.Flag("pause") {
			if memory == nil || memory.Store == nil || memory.Store.DB() == nil {
				return 1, fmt.Errorf("sync watch --pause requires an open local store")
			}
			if err := SetEpisodicSyncPaused(memory.Store.DB(), true); err != nil {
				return 1, err
			}
			st, err := inspectEnvHealth(context.Background(), memory, "", false)
			if err != nil {
				return 1, err
			}
			return 0, a.emit(map[string]any{"ok": true, "paused": true, "episodic_index": st.EpisodicIndex}, func() {
				a.printf("sync paused (cursors preserved)\n")
			})
		}
		if a.args.Flag("resume") {
			if memory == nil || memory.Store == nil || memory.Store.DB() == nil {
				return 1, fmt.Errorf("sync watch --resume requires an open local store")
			}
			if err := SetEpisodicSyncPaused(memory.Store.DB(), false); err != nil {
				return 1, err
			}
			st, err := inspectEnvHealth(context.Background(), memory, "", false)
			if err != nil {
				return 1, err
			}
			return 0, a.emit(map[string]any{"ok": true, "paused": false, "episodic_index": st.EpisodicIndex}, func() {
				a.printf("sync resumed\n")
			})
		}
		if a.args.Flag("step") {
			if memory == nil || memory.Store == nil || memory.Store.DB() == nil {
				return 1, fmt.Errorf("sync watch --step requires an open local store")
			}
			stepCtx, cancelStep := context.WithTimeout(context.Background(), autoWatchReconcileBudget)
			var backfillRep localvault.EpisodicIndexBackfillReport
			var backfillErr error
			for stepCtx.Err() == nil {
				pageRep, pErr := runEpisodicIndexBackfillTick(stepCtx, memory)
				backfillRep.Cursor = pageRep.Cursor
				backfillRep.Scanned += pageRep.Scanned
				backfillRep.Indexed += pageRep.Indexed
				backfillRep.Unchanged += pageRep.Unchanged
				backfillRep.Unavailable += pageRep.Unavailable
				backfillRep.Unverified += pageRep.Unverified
				backfillRep.Failed += pageRep.Failed
				if pErr != nil {
					backfillErr = pErr
					break
				}
				if pageRep.Scanned < reconcileDefaultLimit || pageRep.Cursor == "" {
					break
				}
			}
			cancelStep()
			home, _ := os.UserHomeDir()
			var scanErr error
			if home != "" {
				scanScope := config.DefaultScope
				if memory.Config != nil && strings.TrimSpace(memory.Config.Scope) != "" {
					scanScope = strings.TrimSpace(memory.Config.Scope)
				}
				scanKey := "episodic_source_scan_cursor:" + scanScope
				scanCtx, cancelScan := context.WithTimeout(context.Background(), autoWatchReconcileBudget)
				for scanCtx.Err() == nil {
					var beforeCursor []byte
					_ = memory.Store.DB().QueryRow(`SELECT v FROM rm_meta WHERE k = ?`, scanKey).Scan(&beforeCursor)
					sErr := runEpisodicSourceScanTick(scanCtx, memory, home)
					if sErr != nil {
						scanErr = sErr
						break
					}
					var afterCursor []byte
					_ = memory.Store.DB().QueryRow(`SELECT v FROM rm_meta WHERE k = ?`, scanKey).Scan(&afterCursor)
					if string(afterCursor) == string(beforeCursor) || len(afterCursor) == 0 || string(afterCursor) == "{}" || strings.Contains(string(afterCursor), `"source":""`) {
						break
					}
				}
				cancelScan()
			}
			// 只愈合巡检：漂移的情景指针（含跨轮次重锚）随「推进一轮」一并修复；
			// 确证不存在的行留给 `sync prune --apply --yes`，本步永不删除。
			healCtx, cancelHeal := context.WithTimeout(context.Background(), autoWatchReconcileBudget)
			healScanned, healRepaired, healErr := healDriftedEpisodicPointers(healCtx, memory, episodicHealStepLimit)
			cancelHeal()
			if healRepaired > 0 && memory.Config != nil && mcp.ShouldUseUnixDaemon(memory.Config.DB) {
				// 跨进程写：通知常驻守护进程丢弃内存快照，面板与 MCP 立刻看到重锚后的坐标。
				mcp.InvalidateUnixDaemonCaches("")
			}
			st, _ := inspectEnvHealth(context.Background(), memory, "", false)
			var warnings []string
			if backfillErr != nil && !errors.Is(backfillErr, context.DeadlineExceeded) && !strings.Contains(backfillErr.Error(), "checkpoint changed") {
				warnings = append(warnings, backfillErr.Error())
			}
			if scanErr != nil && !errors.Is(scanErr, context.DeadlineExceeded) && !strings.Contains(scanErr.Error(), "checkpoint changed") {
				warnings = append(warnings, scanErr.Error())
			}
			if healErr != nil && !errors.Is(healErr, context.DeadlineExceeded) {
				warnings = append(warnings, healErr.Error())
			}
			out := map[string]any{
				"ok":             true,
				"stepped":        true,
				"backfill":       backfillRep,
				"episodic_index": st.EpisodicIndex,
				"heal": map[string]any{
					"scanned":  healScanned,
					"repaired": healRepaired,
				},
				"warnings": warnings,
			}
			return 0, a.emit(out, func() {
				a.printf("sync step: backfilled +%d (scanned %d) · cursor=%s\n", backfillRep.Indexed, backfillRep.Scanned, backfillRep.Cursor)
				if healScanned > 0 {
					a.printf("sync step: pointer heal scanned %d · repaired %d\n", healScanned, healRepaired)
				}
			})
		}
		intervalSec := a.args.Int("interval_sec")
		if intervalSec <= 0 {
			intervalSec = 10
		}
		if a.args.Flag("once") {
			ran, bgChunks, epiImported := RunAutoWatchTick(context.Background(), memory, bgSettleWindow, episodicQuiescenceWindow)
			out := map[string]any{
				"ok":                true,
				"once":              true,
				"ran":               ran,
				"background_chunks": bgChunks,
				"episodic_imported": epiImported,
			}
			return 0, a.emit(out, func() {
				a.printf("sync watch (once): ran=%v · background +%d · episodic +%d\n", ran, bgChunks, epiImported)
			})
		}
		RunAutoWatchLoop(context.Background(), memory, time.Duration(intervalSec)*time.Second)
		return 0, nil
	default:
		return 1, fmt.Errorf("unknown sync subcommand %q (expected status, reconcile or watch)", sub)
	}
}

func inspectEnvHealthAndSync(ctx context.Context, memory *facade.Memory, harnessFilter string) (EnvHealthAndSyncStatus, error) {
	return inspectEnvHealth(ctx, memory, harnessFilter, true)
}

// resolveSyncTransport opens the local vault client shared by the sync report
// and the reconcile pass.
func resolveSyncTransport(memory *facade.Memory) (*localvault.Client, error) {
	tc, err := memory.LocalVault()
	if err != nil {
		return nil, fmt.Errorf("init localvault failed: %w", err)
	}
	if tc == nil {
		return nil, fmt.Errorf("init localvault failed: no client returned")
	}
	return tc, nil
}

// inspectEnvHealth builds the health/sync snapshot. withTurnCounts selects whether
// harness transcripts are scanned for turn counts: `sync status` needs them, while a
// background reconcile pass only needs wiring and per-layer sets and must not pay a
// full five-harness transcript scan on every tick.
func inspectEnvHealth(ctx context.Context, memory *facade.Memory, harnessFilter string, withTurnCounts bool) (EnvHealthAndSyncStatus, error) {
	home, _ := os.UserHomeDir()
	scope := config.DefaultScope
	if memory != nil && memory.Config != nil && strings.TrimSpace(memory.Config.Scope) != "" {
		scope = strings.TrimSpace(memory.Config.Scope)
	}

	out := EnvHealthAndSyncStatus{
		Harnesses:    []HarnessEnvItem{},
		HealthChecks: []HealthCheckSubItem{},
		Layers:       map[string]LayerAlignmentStat{},
		UpdatedAt:    time.Now().Format("2006-01-02 15:04:05"),
	}

	// 1. Load local SQLite vault unit sets
	localEpisodic := map[string]bool{}
	deletedEpisodic := map[string]bool{}
	localBackground := map[string]bool{}
	localNoeticActive := map[string]bool{}
	localNoeticStaleCount := 0
	localNoeticSupersededCount := 0
	mountedDirsCount := 0
	trackedFilesCount := 0
	dirtyFilesCount := 0
	var healthDB *sql.DB

	endVaultStage := stageStart(progressStageVault, nil)
	if memory != nil && memory.Store != nil && memory.Store.DB() != nil {
		db := memory.Store.DB()
		healthDB = db
		if rows, err := db.Query(`SELECT unit_id, COALESCE(layer, 'episodic') FROM rm_docs`); err == nil {
			for rows.Next() {
				var id, lyr string
				if rows.Scan(&id, &lyr) == nil && strings.TrimSpace(id) != "" {
					l := strings.ToLower(strings.TrimSpace(lyr))
					if l == "background" {
						localBackground[id] = true
					} else if l != "noetic" {
						localEpisodic[id] = true
					}
				}
			}
			rows.Close()
		}
		if rows, err := db.Query(`SELECT unit_id FROM rm_deleted_units WHERE layer = 'episodic' OR layer = ''`); err == nil {
			for rows.Next() {
				var uid string
				if rows.Scan(&uid) == nil && strings.TrimSpace(uid) != "" {
					deletedEpisodic[strings.TrimSpace(uid)] = true
				}
			}
			rows.Close()
		}
		if rows, err := db.Query(`SELECT unit_id FROM rm_bg_chunks`); err == nil {
			for rows.Next() {
				var uid string
				if rows.Scan(&uid) == nil && strings.TrimSpace(uid) != "" {
					localBackground[uid] = true
				}
			}
			rows.Close()
		}
		if rows, err := db.Query(`SELECT noetic_id, status FROM rm_noetic`); err == nil {
			for rows.Next() {
				var nid, st string
				if rows.Scan(&nid, &st) == nil && strings.TrimSpace(nid) != "" {
					switch strings.ToLower(strings.TrimSpace(st)) {
					case "active":
						localNoeticActive[nid] = true
					case "superseded":
						// Superseding is the normal trace-dedupe lifecycle: the
						// newer trace carries the same cue. It is not a failed or
						// stale imprint and must not degrade health.
						localNoeticSupersededCount++
					default:
						localNoeticStaleCount++
					}
				}
			}
			rows.Close()
		}
		_ = db.QueryRow(`SELECT COUNT(*) FROM rm_bg_dirs`).Scan(&mountedDirsCount)
		_ = db.QueryRow(`SELECT COUNT(*) FROM rm_bg_files`).Scan(&trackedFilesCount)
		dirtyFilesCount = countDirtyBackgroundFiles(db)
	}
	endVaultStage(map[string]any{
		"episodic": len(localEpisodic), "background": len(localBackground),
		"noetic": len(localNoeticActive), "dirty_files": dirtyFilesCount,
	})

	// 2. Detect live client attachments and scan local conversation turns.
	// A live attachment is a session lease, not a recent file write. The
	// console's status subprocess sets ROLLING_MEMORY_CONSOLE_STATUS=1 so it
	// never mistakes the server process's own shell identity for the user's
	// current window.
	endWiringStage := stageStart(progressStageWiring, nil)
	attachments, activeAttachment, detectSrc := detectLiveAttachments()
	activeID, activeWS, activeSess, presenceAt := "", "", "", int64(0)
	if activeAttachment != nil {
		activeID = activeAttachment.Host
		activeWS = activeAttachment.Locus
		activeSess = activeAttachment.SessionID
		presenceAt = activeAttachment.LastSeenAt
	}
	liveHosts := map[string]bool{}
	for _, attachment := range attachments {
		if attachment.Host != "" {
			liveHosts[attachment.Host] = true
		}
	}
	// An explicit `--harness` filter is a soft scan/display scope; it is not
	// recency evidence and can never outvote a fresher transcript.
	displayFilter := ""
	if harnessFilter != "" && harnessFilter != "all" {
		displayFilter = harnessFilter
	}
	listID := activeID
	if displayFilter != "" {
		listID = displayFilter
	}
	defs := harnessEnvDefinitions(home, listID)
	checkReport := harness.Check(defs, harness.InstallOptions{
		Scope: "user",
		Hooks: true,
		Home:  home,
	}, false)
	wiredMap := map[string]map[string]bool{}
	for _, act := range checkReport.Actions {
		if wiredMap[act.Harness] == nil {
			wiredMap[act.Harness] = map[string]bool{}
		}
		if act.Status == harness.StatusOK {
			wiredMap[act.Harness][act.Component] = true
		}
	}

	endWiringStage(map[string]any{"active": activeID})

	totalHarnessUnsynced := 0
	harnessAlignmentKnown := withTurnCounts
	// The ingest lower bound is part of the vault's configured collection scope:
	// turns older than sync.since are intentionally never imported, so they are
	// not pending work and must not degrade alignment. A malformed bound keeps
	// the status honest by marking alignment unknown instead of guessing.
	cfgPath := ""
	if memory != nil && memory.Config != nil {
		cfgPath = memory.Config.Path
	}
	since, sinceErr := EpisodicScanSince(cfgPath)
	if sinceErr != nil {
		harnessAlignmentKnown = false
	}
	// The harness count cache keys on the total local vault unit count: a new
	// import changes attribution, so cached per-harness counts may not be reused.
	vaultUnits := len(localEpisodic) + len(localBackground) + len(localNoeticActive)
	endScanStage := stageStart(progressStageScan, map[string]any{"harnesses": len(defs)})

	for _, d := range defs {
		if err := ctx.Err(); err != nil {
			return out, err
		}
		_, statErr := os.Stat(d.Detect)
		detected := statErr == nil
		if statErr != nil && !os.IsNotExist(statErr) {
			harnessAlignmentKnown = false
		}
		w := wiredMap[d.ID]
		sessionsCnt, turns, lastTS := 0, []candidateEpisodicTurn(nil), int64(0)
		syncedCnt, unsyncedCnt, localTurnsCnt := 0, 0, 0
		outOfScopeCnt, incompleteCnt, excludedSubagentCnt := 0, 0, 0
		if withTurnCounts {
			var excludedKnown bool
			excludedSubagentCnt, excludedKnown = countExcludedSubagentSources(ctx, home, d.ID)
			if !excludedKnown {
				harnessAlignmentKnown = false
			}
			cached, cacheOK := loadHarnessCountCache(healthDB, d.ID, home, vaultUnits, since)
			if cacheOK {
				sessionsCnt, localTurnsCnt = cached.Sessions, cached.LocalTurns
				syncedCnt, unsyncedCnt = cached.Synced, cached.Unsynced
				outOfScopeCnt, incompleteCnt = cached.OutOfScope, cached.Incomplete
				lastTS = cached.LastActiveAt
				if !cached.Complete {
					harnessAlignmentKnown = false
				}
				emitStatusProgress(progressStageScan, "step", map[string]any{
					"harness": d.ID, "phase": "done", "cached": true,
					"local_turns": localTurnsCnt, "synced": syncedCnt, "unsynced": unsyncedCnt,
					"out_of_scope": outOfScopeCnt, "incomplete": incompleteCnt,
					"excluded_subagents": excludedSubagentCnt,
				})
			} else {
				var complete bool
				sessionsCnt, turns, lastTS, complete, outOfScopeCnt = scanHarnessTurnsWithCoverageBoundContext(ctx, home, d.ID, scope, 2000, since)
				if !complete {
					harnessAlignmentKnown = false
				}
				for _, turn := range turns {
					// A turn without confirmed completion is never importable (open
					// tail or aborted run); out-of-bound history is outside the
					// configured collection window. Neither is pending work.
					if !turn.Completed {
						incompleteCnt++
						continue
					}
					if episodicOccurredBefore(turn.OccurredAt, since) {
						outOfScopeCnt++
						continue
					}
					if localEpisodic[turn.DocID] || deletedEpisodic[turn.DocID] {
						syncedCnt++
					} else {
						unsyncedCnt++
					}
				}
				localTurnsCnt = syncedCnt + unsyncedCnt
				if healthDB != nil {
					storeHarnessCountCache(healthDB, d.ID, home, vaultUnits, since, sessionsCnt, syncedCnt, unsyncedCnt, localTurnsCnt, outOfScopeCnt, incompleteCnt, lastTS, complete)
				}
				emitStatusProgress(progressStageScan, "step", map[string]any{
					"harness": d.ID, "phase": "done", "cached": false,
					"local_turns": localTurnsCnt, "synced": syncedCnt, "unsynced": unsyncedCnt,
					"out_of_scope": outOfScopeCnt, "incomplete": incompleteCnt,
					"excluded_subagents": excludedSubagentCnt,
				})
			}
		}
		totalHarnessUnsynced += unsyncedCnt
		// A detected host without a turn reader is a documented collection-scope
		// limitation, not an unreadable source: the harness item reports
		// turns_scan_supported=false and the panel labels it. It must not hold
		// the supported scope at "alignment unknown" forever.
		lastTxt := "-"
		if lastTS > 0 {
			lastTxt = time.Unix(lastTS, 0).Format("2006-01-02 15:04:05")
		}
		// INSTALLED lights on real footprint evidence: a state directory survives
		// an uninstall and can be created by our own wiring, so bare directory
		// existence is not proof the host is still here. Session activity, the
		// host's own app/launcher, or a live attachment qualify.
		installed := detected && (sessionsCnt > 0 || lastTS > 0 || liveHosts[d.ID] || harness.AppPresent(d.ID, home))
		out.Harnesses = append(out.Harnesses, HarnessEnvItem{
			ID:                           d.ID,
			Label:                        d.Label,
			Detected:                     installed,
			Active:                       liveHosts[d.ID],
			SkillWired:                   w["skill"],
			MCPWired:                     w["mcp"],
			HooksWired:                   w["hooks"],
			HooksSupported:               d.HooksKind != "",
			SessionsScanSupported:        harness.SessionsScannable(d.ID),
			TurnsScanSupported:           harness.TurnsScannable(d.ID),
			SessionsCount:                sessionsCnt,
			LocalTurnsCount:              localTurnsCnt,
			SyncedTurnsCount:             syncedCnt,
			UnsyncedTurnsCount:           unsyncedCnt,
			OutOfScopeTurnsCount:         outOfScopeCnt,
			IncompleteTurnsCount:         incompleteCnt,
			ExcludedSubagentSourcesCount: excludedSubagentCnt,
			LastActiveAt:                 lastTS,
			LastActiveText:               lastTxt,
		})
	}

	// Recent activity is telemetry: the newest transcript or last-call claim.
	// It never substitutes for a live attachment in the console badge.
	recentID, _ := resolveActiveHarness(
		out.Harnesses,
		harnessClaims(out.Harnesses, activeID, detectSrc, presenceAt, time.Now().Unix()),
	)
	var recentItem *HarnessEnvItem
	for i := range out.Harnesses {
		if recentID == "" || out.Harnesses[i].ID != recentID {
			continue
		}
		cp := out.Harnesses[i]
		recentItem = &cp
	}
	endScanStage(map[string]any{"unsynced": totalHarnessUnsynced})

	out.Attachment = activeAttachment
	out.Attachments = attachments
	out.ActiveHarnessID = activeID
	out.ActiveHarnessLabel = ""
	if activeAttachment != nil {
		out.ActiveHarnessLabel = activeAttachment.Label
	}
	if out.ActiveHarnessLabel == "" && activeID != "" {
		out.ActiveHarnessLabel = activeID
	}
	out.ActiveWorkspace = activeWS
	out.ActiveSessionID = activeSess
	out.DetectionSource = detectSrc
	if out.DetectionSource == "" {
		out.DetectionSource = "none"
	}
	out.RecentHarnessID = recentID
	out.RecentHarnessLabel = ""
	if recentItem != nil {
		out.RecentHarnessLabel = recentItem.Label
	} else if recentID != "" {
		out.RecentHarnessLabel = recentID
	}

	// 3. Read the local vault's index coverage and scan progress. The product
	// has no remote transport: every alignment counter is local.
	endIndexStage := stageStart(progressStageIndex, nil)
	tc, err := resolveSyncTransport(memory)
	if err != nil {
		return out, err
	}
	if tc != nil && tc.Vault != nil {
		cfgPath := ""
		if memory != nil && memory.Config != nil {
			cfgPath = memory.Config.Path
		}
		status := &EpisodicIndexStatus{
			Paused: IsEpisodicSyncPaused(healthDB, cfgPath),
		}
		coverage, err := tc.Vault.EpisodicIndexFaultDiagnostics()
		if err != nil {
			status.Error = err.Error()
		} else {
			status.Compatible = coverage.Compatible
			status.Incompatible = coverage.Incompatible
			status.Missing = coverage.Missing
			status.Corrupt = coverage.Corrupt
			status.NeedsRebuild = coverage.NeedsRebuild
			status.Orphaned = coverage.Orphaned
			status.TotalDocs = coverage.Compatible + coverage.Incompatible + coverage.Missing + coverage.Corrupt + coverage.NeedsRebuild
			if status.TotalDocs > 0 {
				status.CoveragePct = float64(status.Compatible*1000/status.TotalDocs) / 10.0
			} else {
				status.CoveragePct = 100.0
			}
		}
		if healthDB != nil {
			if err := healthDB.QueryRow(`SELECT CAST(v AS INTEGER) FROM rm_meta WHERE k = ?`, "episodic_source_scan_item_errors:"+scope).Scan(&status.SourceItemErrors); err != nil && !errors.Is(err, sql.ErrNoRows) {
				status.Error = err.Error()
			}
			_ = healthDB.QueryRow(`SELECT CAST(v AS TEXT) FROM rm_meta WHERE k = ?`, "episodic_index_backfill_cursor:"+scope).Scan(&status.BackfillCursor)
			var scanRaw []byte
			scanErr := healthDB.QueryRow(`SELECT v FROM rm_meta WHERE k = ?`, "episodic_source_scan_cursor:"+scope).Scan(&scanRaw)
			if scanErr != nil && !errors.Is(scanErr, sql.ErrNoRows) {
				status.Error = scanErr.Error()
			} else if scanErr == nil && len(scanRaw) > 0 {
				var cur episodicSourceCursor
				if err := json.Unmarshal(scanRaw, &cur); err != nil {
					status.Error = err.Error()
				} else {
					if cur.RetrySources {
						harnessAlignmentKnown = false
					}
					if cur.ItemErrors > status.SourceItemErrors {
						status.SourceItemErrors = cur.ItemErrors
					}
					parts := strings.Split(cur.Source, "\x00")
					if len(parts) >= 3 && parts[2] != "" {
						status.ScanSource = parts[0] + ":" + filepath.Base(parts[2])
						if len(parts) >= 4 && parts[3] != "" {
							status.ScanSource += ":" + parts[3]
						}
					} else {
						status.ScanSource = cur.Source
					}
					status.ScanIndex = cur.Index
				}
			}
			var leaseVal string
			if healthDB.QueryRow(`SELECT CAST(v AS TEXT) FROM rm_meta WHERE k = 'autowatch_lease'`).Scan(&leaseVal) == nil && strings.Contains(leaseVal, ":") {
				parts := strings.Split(leaseVal, ":")
				if len(parts) >= 2 {
					if exp, pErr := strconv.ParseInt(parts[1], 10, 64); pErr == nil && exp >= time.Now().Unix() && !status.Paused {
						status.Running = true
					}
				}
			}
		}
		out.EpisodicIndex = status
	}
	indexFields := map[string]any{}
	if out.EpisodicIndex != nil {
		indexFields["compatible"] = out.EpisodicIndex.Compatible
		indexFields["missing"] = out.EpisodicIndex.Missing
		indexFields["coverage_pct"] = out.EpisodicIndex.CoveragePct
	}
	endIndexStage(indexFields)
	out.Mode = "local"

	// 4. Compute per-layer alignment stats
	epiStat := computeLayerDiff("episodic", localEpisodic, totalHarnessUnsynced, 0)
	if !harnessAlignmentKnown || (out.EpisodicIndex != nil && (out.EpisodicIndex.SourceItemErrors > 0 || out.EpisodicIndex.Missing > 0 || out.EpisodicIndex.Corrupt > 0 || out.EpisodicIndex.NeedsRebuild > 0 || out.EpisodicIndex.Error != "")) {
		epiStat.AlignmentKnown = false
		epiStat.Aligned = false
	}
	bgStat := computeLayerDiff("background", localBackground, 0, dirtyFilesCount)
	bgStat.MountedDirsCount = mountedDirsCount
	bgStat.TrackedFilesCount = trackedFilesCount
	bgStat.DirtyFilesCount = dirtyFilesCount

	noeStat := computeLayerDiff("noetic", localNoeticActive, 0, 0)
	noeStat.StaleCount = localNoeticStaleCount
	noeStat.SupersededCount = localNoeticSupersededCount

	out.Layers["episodic"] = epiStat
	out.Layers["background"] = bgStat
	out.Layers["noetic"] = noeStat
	out.TotalPendingSync = epiStat.TotalPendingSync + bgStat.TotalPendingSync + noeStat.TotalPendingSync

	// 5. Compute 5-dimension Memory Health Score (0-100)
	totalScore := 0

	// (1) Local Vault Availability (25 pts)
	vaultDetailZH := fmt.Sprintf("本地明文金库 (local_plain) · 零云端请求 · scope=local://vault")
	vaultDetailEN := "Local plaintext vault (local_plain) · zero cloud requests · scope=local://vault"
	totalScore += 25
	out.HealthChecks = append(out.HealthChecks, HealthCheckSubItem{
		Key: "local_vault", LabelZH: "本地金库可用性 (local_plain)", LabelEN: "Local Vault Availability (local_plain)",
		Status: "healthy", Score: 25, MaxScore: 25, DetailZH: vaultDetailZH, DetailEN: vaultDetailEN,
	})

	// (2) Active Harness Wiring (15 pts)
	var activeItem *HarnessEnvItem
	for i := range out.Harnesses {
		if activeID != "" && out.Harnesses[i].ID == activeID {
			cp := out.Harnesses[i]
			activeItem = &cp
			break
		}
	}
	hScore := 0
	hStatus := "warning"
	hDetailZH := fmt.Sprintf("当前环境：%s", out.ActiveHarnessLabel)
	hDetailEN := fmt.Sprintf("Active harness: %s", out.ActiveHarnessLabel)
	if activeItem != nil {
		if activeItem.SkillWired {
			hScore += 5
		}
		if activeItem.MCPWired {
			hScore += 5
		}
		if !activeItem.HooksSupported || activeItem.HooksWired {
			hScore += 5
		}
		if hScore == 15 {
			hStatus = "healthy"
		}
		hDetailZH = fmt.Sprintf("%s · 静态配置 Skill:%v · MCP:%v · Hooks:%v（未验证宿主运行）",
			activeItem.Label, activeItem.SkillWired, activeItem.MCPWired, activeItem.HooksWired)
		hDetailEN = fmt.Sprintf("%s · Configured Skill:%v · MCP:%v · Hooks:%v (host runtime not verified)",
			activeItem.Label, activeItem.SkillWired, activeItem.MCPWired, activeItem.HooksWired)
	}
	totalScore += hScore
	out.HealthChecks = append(out.HealthChecks, HealthCheckSubItem{
		Key: "harness_wiring", LabelZH: "宿主与钩子静态配置", LabelEN: "Harness & Hooks Configuration",
		Status: hStatus, Score: hScore, MaxScore: 15, DetailZH: hDetailZH, DetailEN: hDetailEN,
	})

	// (3) L2 Episodic Alignment (25 pts)
	eScore := (epiStat.AlignmentPct * 25) / 100
	eStatus := "healthy"
	if epiStat.TotalPendingSync > 0 || !epiStat.AlignmentKnown {
		if eScore > 20 {
			eScore = 20
		}
		eStatus = "warning"
	}
	eDetailZH := fmt.Sprintf("本地金库 %d · 宿主待导入对话 %d 轮",
		epiStat.LocalVaultCount, epiStat.HarnessUnsyncedTurns)
	eDetailEN := fmt.Sprintf("Local %d · Unsynced turns %d",
		epiStat.LocalVaultCount, epiStat.HarnessUnsyncedTurns)
	if !epiStat.AlignmentKnown {
		eDetailZH += " · 部分宿主或历史范围未确认"
		eDetailEN += " · Some hosts or history are not verified"
	}
	totalScore += eScore
	out.HealthChecks = append(out.HealthChecks, HealthCheckSubItem{
		Key: "episodic_sync", LabelZH: "L2 Episodic 历史对话对齐", LabelEN: "L2 Episodic Dialogue Sync",
		Status: eStatus, Score: eScore, MaxScore: 25, DetailZH: eDetailZH, DetailEN: eDetailEN,
	})

	// (4) L1 Background Alignment (20 pts)
	bScore := (bgStat.AlignmentPct * 20) / 100
	bStatus := "healthy"
	if bgStat.TotalPendingSync > 0 {
		if bScore > 16 {
			bScore = 16
		}
		bStatus = "warning"
	}
	bDetailZH := fmt.Sprintf("挂载目录 %d · 本地分块 %d · 待处理文件 %d",
		bgStat.MountedDirsCount, bgStat.LocalVaultCount, bgStat.TotalPendingSync)
	bDetailEN := fmt.Sprintf("Dirs %d · Local chunks %d · Pending files %d",
		bgStat.MountedDirsCount, bgStat.LocalVaultCount, bgStat.TotalPendingSync)
	totalScore += bScore
	out.HealthChecks = append(out.HealthChecks, HealthCheckSubItem{
		Key: "background_sync", LabelZH: "L1 Background 背景知识对齐", LabelEN: "L1 Background Corpus Sync",
		Status: bStatus, Score: bScore, MaxScore: 20, DetailZH: bDetailZH, DetailEN: bDetailEN,
	})

	// (5) L3 Noetic Health (15 pts)
	nScore := (noeStat.AlignmentPct * 15) / 100
	nStatus := "healthy"
	if noeStat.TotalPendingSync > 0 || noeStat.StaleCount > 0 {
		if nScore > 12 {
			nScore = 12
		}
		nStatus = "warning"
	}
	nDetailZH := fmt.Sprintf("活跃印迹 %d · 失效(Stale) %d",
		noeStat.LocalVaultCount, noeStat.StaleCount)
	nDetailEN := fmt.Sprintf("Active %d · Stale %d",
		noeStat.LocalVaultCount, noeStat.StaleCount)
	if noeStat.SupersededCount > 0 {
		nDetailZH += fmt.Sprintf(" · 被替代 %d", noeStat.SupersededCount)
		nDetailEN += fmt.Sprintf(" · Superseded %d", noeStat.SupersededCount)
	}
	totalScore += nScore
	out.HealthChecks = append(out.HealthChecks, HealthCheckSubItem{
		Key: "noetic_sync", LabelZH: "L3 Noetic 知性印迹健康度", LabelEN: "L3 Noetic Imprint Health",
		Status: nStatus, Score: nScore, MaxScore: 15, DetailZH: nDetailZH, DetailEN: nDetailEN,
	})

	out.HealthScore = totalScore
	switch {
	case totalScore >= 85:
		out.HealthGrade = "healthy"
	case totalScore >= 60:
		out.HealthGrade = "subhealthy"
	default:
		out.HealthGrade = "degraded"
	}

	return out, nil
}

func computeLayerDiff(layer string, localSet map[string]bool, extraHarnessUnsynced, dirtyFiles int) LayerAlignmentStat {
	synced := len(localSet)
	pending := extraHarnessUnsynced + dirtyFiles
	denom := len(localSet) + pending
	pct := 100
	if denom > 0 {
		pct = (synced * 100) / denom
		if pct > 100 {
			pct = 100
		}
	}
	return LayerAlignmentStat{
		Layer:                layer,
		LocalVaultCount:      len(localSet),
		SyncedCount:          synced,
		HarnessUnsyncedTurns: extraHarnessUnsynced,
		TotalPendingSync:     pending,
		AlignmentPct:         pct,
		AlignmentKnown:       true,
		Aligned:              pending == 0,
	}
}

// episodicTurnGroup is one frozen-parse batch of the reconcile save section: every
// turn whose pointer names the same JSONL source, in the order the scanned page
// produced them.
type episodicTurnGroup struct {
	path  string
	turns []candidateEpisodicTurn
}

// groupEpisodicTurnsBySource buckets scanned turns by the JSONL transcript their
// pointer names, in the order the sources first appear, so one frozen parse per
// source can drive every Verified save of that source's turns. Turns whose
// pointers are not JSONL refs (zcode, empty) fall into a pathless group and keep
// the live save path.
func groupEpisodicTurnsBySource(turns []candidateEpisodicTurn) []episodicTurnGroup {
	if len(turns) == 0 {
		return nil
	}
	groups := make([]episodicTurnGroup, 0, 4)
	slotByPath := make(map[string]int, 4)
	for _, turn := range turns {
		path := episodicTurnSourcePath(turn.RefPointer)
		slot, known := slotByPath[path]
		if !known {
			slot = len(groups)
			slotByPath[path] = slot
			groups = append(groups, episodicTurnGroup{path: path})
		}
		groups[slot].turns = append(groups[slot].turns, turn)
	}
	return groups
}

// episodicTurnSourcePath extracts the transcript path of one JSONL pointer
// (`@ref:jsonl:<abs path>:<turn>#<k>`), empty when the pointer is not a JSONL ref.
// The suffix grammar is the one the ingest writes: the path runs up to the colon
// before the turn index.
func episodicTurnSourcePath(refPointer string) string {
	trimmed := strings.TrimSpace(refPointer)
	if !strings.HasPrefix(trimmed, "@ref:jsonl:") {
		return ""
	}
	rest := strings.TrimPrefix(trimmed, "@ref:jsonl:")
	if colon := strings.LastIndex(rest, ":"); colon > 0 {
		return rest[:colon]
	}
	return ""
}

func executeReconcileSync(ctx context.Context, memory *facade.Memory, layer, harnessFilter string, limit int) (ReconcileReport, error) {
	return executeReconcileSyncWithScanLimit(ctx, memory, layer, harnessFilter, limit, 2000)
}

// executeReconcileSyncWithScanLimit drains up to scanLimit harness turns per
// harness in a foreground pass. The historical 2000-turn window is enough for
// incremental auto-watch, but a manual recovery after a vault reset must be
// able to walk the full local transcript window without changing the import
// semantics (the reconcile path only adds or heals units; it never deletes).
func executeReconcileSyncWithScanLimit(ctx context.Context, memory *facade.Memory, layer, harnessFilter string, limit, scanLimit int) (ReconcileReport, error) {
	t0 := time.Now()
	if limit <= 0 {
		limit = reconcileDefaultLimit
	}
	rep := ReconcileReport{
		Layer:         layer,
		TargetHarness: harnessFilter,
	}
	if memory == nil || memory.Store == nil || memory.Store.DB() == nil {
		return rep, fmt.Errorf("reconcile requires an open local SQLite memory store")
	}

	tc, err := resolveSyncTransport(memory)
	if err != nil {
		return rep, err
	}

	home, _ := os.UserHomeDir()
	scope := config.DefaultScope
	if memory.Config != nil && strings.TrimSpace(memory.Config.Scope) != "" {
		scope = strings.TrimSpace(memory.Config.Scope)
	}
	// Pre-fetch current status so we know which harness is active and which units
	// are still missing locally. A background pass (caller supplied a deadline)
	// skips the transcript turn counting: it must stay inside the auto-watch tick
	// and only needs wiring + local unit sets.
	_, backgroundPass := ctx.Deadline()
	preStatus, err := inspectEnvHealth(ctx, memory, harnessFilter, !backgroundPass)
	if err != nil {
		return rep, err
	}
	targetHar := harnessFilter
	if targetHar == "" || targetHar == "all" {
		targetHar = preStatus.ActiveHarnessID
	}
	rep.TargetHarness = targetHar
	db := memory.Store.DB()

	// 1. Reconcile L2 Episodic (Harness history into local vault)
	if layer == "all" || layer == "episodic" {
		// This importer is the second path into the vault; the same sync.since
		// bound must hold here, otherwise a fresh vault backfills the oldest
		// harness turns before the scanner ever sees them.
		since, sinceErr := EpisodicScanSince(memory.Config.Path)
		if sinceErr != nil {
			return rep, sinceErr
		}
		localEpi := map[string]bool{}
		deletedEpi := tc.Vault.DeletedUnitSet("episodic")
		storedRaw := map[string]string{}
		pointerDocs := map[string][]string{}
		unverified := map[string]bool{}
		if rows, qErr := db.Query(`
			SELECT d.unit_id, COALESCE(NULLIF(m.ref_pointer, ''), d.raw_text)
			FROM rm_docs d
			LEFT JOIN rm_unit_meta m ON m.unit_id = d.unit_id
			WHERE d.layer = 'episodic' OR d.layer = ''`); qErr == nil {
			for rows.Next() {
				var id, raw string
				if rows.Scan(&id, &raw) == nil && id != "" {
					localEpi[id] = true
					storedRaw[id] = raw
					if strings.HasPrefix(raw, "@ref:") {
						pointerDocs[raw] = append(pointerDocs[raw], id)
						unverified[id] = true
					}
				}
			}
			rows.Close()
		}

		// Step 1a: bring harness turns into the local vault. Local import is a zero-copy
		// pointer write (no network, no plaintext duplication, and it resolves live on
		// read), so every turn the harness already finished is visible to local
		// recall/recent on this very pass.
		var harnessIDs []string
		if harnessFilter == "all" {
			for _, h := range preStatus.Harnesses {
				if harness.TurnsScannable(h.ID) {
					harnessIDs = append(harnessIDs, h.ID)
				}
			}
		} else if targetHar != "" {
			harnessIDs = []string{targetHar}
		}

		// Scan breadth: a background pass (caller set a deadline) only needs the newest
		// window because local import converges over successive ticks; a foreground run
		// keeps the requested window (default 2000, up to 50000) so one command can
		// drain a full backlog. Both walk newest-first.
		scanCap := scanLimit
		if scanCap <= 0 {
			scanCap = 2000
		}
		if scanCap > 50000 {
			scanCap = 50000
		}
		if backgroundPass {
			scanCap = max(limit*4, limit)
		}
		rep.EpisodicIndexScanLimit = scanCap
		rep.Warnings = append(rep.Warnings, "episodic index backfill covers only the scanned harness window; unavailable or unscanned historical sources are unknown")
		changedSources := map[string]bool{}
		for _, hid := range harnessIDs {
			_, turns, _, complete, _ := scanHarnessTurnsWithCoverageBoundContext(ctx, home, hid, scope, scanCap, since)
			if excluded, known := countExcludedSubagentSources(ctx, home, hid); known {
				rep.ExcludedSubagentSources += excluded
			} else {
				rep.Warnings = append(rep.Warnings, fmt.Sprintf("%s: subagent coverage inventory unreadable", hid))
			}
			if !complete {
				rep.Warnings = append(rep.Warnings, fmt.Sprintf("%s: incomplete or unreadable episodic source coverage", hid))
			}
			if err := ctx.Err(); err != nil {
				return rep, err
			}
			rep.EpisodicIndexScanned += len(turns)
			sort.SliceStable(turns, func(i, j int) bool { return turns[i].OccurredAt > turns[j].OccurredAt })
			for _, group := range groupEpisodicTurnsBySource(turns) {
				var snapshot *episodic.SourceSnapshot
				if group.path != "" {
					if parsed, snapErr := episodic.NewSourceSnapshotContext(ctx, group.path); snapErr == nil {
						snapshot = parsed
					}
				}
				for _, turn := range group.turns {
					if err := ctx.Err(); err != nil {
						return rep, err
					}
					if !turn.Completed {
						continue
					}
					if episodicunit.DocID(scope, turn.Text) != turn.DocID {
						rep.Warnings = append(rep.Warnings, fmt.Sprintf("episodic index %s: content identity mismatch", turn.DocID))
						continue
					}
					if deletedEpi[turn.DocID] {
						continue
					}
					vault := tc.Vault.CloneWithSharedSlotCache()
					vault.SetIdentity(localvault.VaultIdentity{AssistantID: turn.HarnessID, IncarnationID: turn.SessionID, Locus: turn.Locus})
					for _, id := range pointerDocs[turn.RefPointer] {
						if id != turn.DocID && !changedSources[id] {
							changedSources[id] = true
							rep.EpisodicIndexSourceChanged++
						}
					}
					if localEpi[turn.DocID] {
						stored, ok := storedRaw[turn.DocID]
						if !ok {
							if err := db.QueryRow(`SELECT COALESCE(NULLIF(m.ref_pointer, ''), d.raw_text) FROM rm_docs d LEFT JOIN rm_unit_meta m ON m.unit_id = d.unit_id WHERE d.unit_id = ?`, turn.DocID).Scan(&stored); err != nil {
								rep.Warnings = append(rep.Warnings, fmt.Sprintf("episodic index %s: %v", turn.DocID, err))
								continue
							}
							storedRaw[turn.DocID] = stored
						}
						if strings.HasPrefix(stored, "@ref:") {
							if stored != turn.RefPointer {
								healed := false
								if strings.TrimSpace(turn.Text) != "" {
									if sErr := vault.SaveLocalEpisodicPointerIndexedVerified(scope, snapshot, turn.DocID, turn.OccurredAt, turn.RefPointer, turn.TurnRef, turn.Text, localvault.IngestResponse{}); sErr == nil {
										var current string
										if err := db.QueryRowContext(ctx, `SELECT COALESCE(NULLIF(m.ref_pointer, ''), d.raw_text) FROM rm_docs d LEFT JOIN rm_unit_meta m ON m.unit_id = d.unit_id WHERE d.unit_id = ?`, turn.DocID).Scan(&current); err != nil {
											return rep, err
										}
										storedRaw[turn.DocID] = current
										if current == turn.RefPointer {
											delete(unverified, turn.DocID)
										}
										healed = true
									} else if errors.Is(sErr, localvault.ErrEpisodicUnitDeleted) {
										deletedEpi[turn.DocID] = true
										continue
									} else if !errors.Is(sErr, localvault.ErrEpisodicIndexItem) && !errors.Is(sErr, localvault.ErrEpisodicPointerSourceConflict) {
										return rep, sErr
									}
								}
								if !healed {
									rep.EpisodicIndexPointerMismatch++
								}
							} else if strings.TrimSpace(turn.Text) != "" {
								created, err := vault.EnsureLocalEpisodicIndexVerified(scope, snapshot, turn.DocID, turn.RefPointer, turn.Text)
								if err != nil {
									if errors.Is(err, localvault.ErrEpisodicUnitDeleted) {
										deletedEpi[turn.DocID] = true
										continue
									}
									if !errors.Is(err, localvault.ErrEpisodicIndexItem) {
										return rep, err
									}
									rep.Warnings = append(rep.Warnings, fmt.Sprintf("episodic index %s: %v", turn.DocID, err))
								} else {
									delete(unverified, turn.DocID)
									if created {
										rep.EpisodicIndexesBackfilled++
									}
									if err := updateEpisodicUnitSourceMeta(ctx, db, turn.DocID, turn.RefPointer, turn.HarnessID, turn.SessionID, turn.LocusKnown, turn.Locus, turn.OccurredAt); err != nil {
										return rep, err
									}
								}
							}
						}
						continue
					}
					if strings.TrimSpace(turn.Text) == "" || turn.RefPointer == "" {
						continue
					}
					if episodicOccurredBefore(turn.OccurredAt, since) {
						continue
					}
					if err := vault.SaveLocalEpisodicPointerIndexedVerified(scope, snapshot, turn.DocID, turn.OccurredAt, turn.RefPointer, turn.TurnRef, turn.Text, localvault.IngestResponse{}); err != nil {
						if errors.Is(err, localvault.ErrEpisodicUnitDeleted) {
							deletedEpi[turn.DocID] = true
							continue
						}
						if !errors.Is(err, localvault.ErrEpisodicIndexItem) && !errors.Is(err, localvault.ErrEpisodicPointerSourceConflict) {
							return rep, err
						}
						rep.Warnings = append(rep.Warnings, fmt.Sprintf("episodic import %s: %v", turn.DocID, err))
						continue
					}
					storedRaw[turn.DocID] = turn.RefPointer
					rep.EpisodicImportedFromHar++
					localEpi[turn.DocID] = true
				}
				if snapshot != nil {
					_ = snapshot.Close()
				}
			}
		}
		rep.EpisodicIndexUnverified = len(unverified)
		if !backgroundPass {
			coverage, indexErr := tc.Vault.EpisodicIndexFaultDiagnostics()
			if indexErr != nil {
				rep.Warnings = append(rep.Warnings, fmt.Sprintf("episodic index coverage unavailable: %v", indexErr))
			} else {
				rep.EpisodicIndexCompatible = coverage.Compatible
				rep.EpisodicIndexIncompatible = coverage.Incompatible
				rep.EpisodicIndexMissing = coverage.Missing
				rep.EpisodicIndexCorrupt = coverage.Corrupt
				rep.EpisodicIndexNeedsRebuild = coverage.NeedsRebuild
			}
		}
	}

	// 2. Reconcile L1 Background (mounted dirs sync when dirty)
	if layer == "all" || layer == "background" {
		if layer == "background" || preStatus.Layers["background"].DirtyFilesCount > 0 {
			if bgRep, bgErr := tc.SyncBackgroundDirs(ctx); bgErr == nil {
				rep.BackgroundSyncedFiles = bgRep.IngestedFiles
			} else {
				rep.Warnings = append(rep.Warnings, fmt.Sprintf("background dir sync: %v", bgErr))
			}
		}
	}

	rep.TotalReconciled = rep.EpisodicImportedFromHar + rep.BackgroundSyncedFiles
	rep.ElapsedMS = int(time.Since(t0).Milliseconds())
	if !backgroundPass && memory.Config != nil && mcp.ShouldUseUnixDaemon(memory.Config.DB) {
		mcp.InvalidateUnixDaemonCaches("")
	}
	if backgroundPass {
		// A background pass reports the snapshot it started from: re-scanning every
		// transcript here would double the tick cost and the caller never reads it.
		rep.Status = preStatus
	} else {
		postStatus, _ := inspectEnvHealth(ctx, memory, harnessFilter, true)
		rep.Status = postStatus
	}
	return rep, nil
}

// detectLiveAttachments returns the lease-valid sessions from the attachment
// ledger. A direct CLI invocation (not the console's status subprocess) may
// additionally report a strong process identity when no ledger entry exists,
// because that command itself is running inside the host. Weak shell exports
// are deliberately ignored: they describe a workspace, not an identity.
func detectLiveAttachments() ([]hostid.Attachment, *hostid.Attachment, string) {
	now := ids.UTCNow()
	live := hostid.LiveAttachments(hostid.LoadLedger(config.DefaultStateDir()), now)
	if len(live) == 0 && os.Getenv("ROLLING_MEMORY_CONSOLE_STATUS") != "1" {
		identity := hostid.DetectFromEnvOnly()
		if identity.Strong && identity.ID != hostid.Unknown && identity.ID != hostid.CLI {
			cwd, _ := os.Getwd()
			live = append(live, hostid.Attachment{
				Host:       string(identity.ID),
				Label:      identity.Label,
				SessionID:  identity.SessionID,
				CWD:        cwd,
				Locus:      text.DetectGitWorkspaceName(cwd),
				Evidence:   identity.Evidence,
				Mode:       "env",
				LastSeenAt: now,
				LeaseUntil: now,
			})
		}
	}
	if len(live) == 0 {
		return nil, nil, "none"
	}
	latest := live[0]
	source := latest.Evidence
	if source == "" {
		source = "attachment"
	}
	return live, &latest, source
}

// harnessEnvDefinitions is the environment-inventory list behind the console's
// environment panel: the five core harnesses are always listed; an extended
// harness (Cursor / Windsurf / Cline / WorkBuddy / Doubao) joins the list once it
// is present on this machine or is the detected active host, so the panel never
// omits the environment the user is actually in, nor pads the grid with hosts
// that have no footprint here.
func harnessEnvDefinitions(home, activeID string) []harness.Harness {
	defs := harness.BuildHarnesses(home, "")
	for _, d := range harness.BuildExtendedHarnesses(home) {
		if d.ID == activeID {
			defs = append(defs, d)
			continue
		}
		if _, err := os.Stat(d.Detect); err == nil {
			defs = append(defs, d)
		}
	}
	return defs
}

// activeHarnessClaim is one piece of recency evidence for the active-harness pick.
type activeHarnessClaim struct {
	id     string
	ts     int64
	source string
}

// harnessClaims assembles the recency evidence behind the recent-harness pick:
// the live attachment contributes its last-seen time, and every harness with a
// transcript contributes its last-activity time. The explicit CLI filter
// deliberately contributes nothing here: it is a scan scope, not evidence.
func harnessClaims(items []HarnessEnvItem, signalID, detectSrc string, presenceAt, now int64) []activeHarnessClaim {
	var claims []activeHarnessClaim
	if signalID != "" && detectSrc != "" && detectSrc != "none" && presenceAt > 0 {
		claims = append(claims, activeHarnessClaim{signalID, presenceAt, detectSrc})
	}
	for _, it := range items {
		if it.LastActiveAt > 0 {
			claims = append(claims, activeHarnessClaim{it.ID, it.LastActiveAt, "recent_activity"})
		}
	}
	return claims
}

// resolveActiveHarness picks the harness with the strongest recency evidence: the
// newest timestamp wins, and equal timestamps keep the earlier (more specific)
// claim. Claims whose id is not part of the inventory are dropped, and no claims
// at all yield an empty id — the caller then reports "none" rather than inventing
// a default.
func resolveActiveHarness(items []HarnessEnvItem, claims []activeHarnessClaim) (harnessID, source string) {
	inInventory := make(map[string]bool, len(items))
	for _, it := range items {
		inInventory[it.ID] = true
	}
	best := -1
	for i, c := range claims {
		if c.ts <= 0 || !inInventory[c.id] {
			continue
		}
		if best < 0 || c.ts > claims[best].ts {
			best = i
		}
	}
	if best < 0 {
		return "", "none"
	}
	return claims[best].id, claims[best].source
}

// countExcludedSubagentSources reports how many host-side subagent sources sit
// outside automatic coverage. The public coverage contract states that subagent
// conversations are not guaranteed, so this is a known collection-scope limit:
// it is surfaced as a count, never as an unreadable-source failure. The boolean
// is false when the inventory itself could not be read.
func countExcludedSubagentSources(ctx context.Context, home, harnessID string) (int, bool) {
	switch harnessID {
	case "claude-code":
		seen := map[string]bool{}
		count := 0
		for _, pattern := range []string{
			filepath.Join(home, ".claude", "projects", "*", "*", "subagents", "*.jsonl"),
			filepath.Join(home, ".claude", "projects", "*", "sessions", "*", "subagents", "*.jsonl"),
		} {
			matches, err := filepath.Glob(pattern)
			if err != nil {
				return 0, false
			}
			for _, path := range matches {
				if seen[path] {
					continue
				}
				info, statErr := os.Stat(path)
				if statErr != nil {
					if os.IsNotExist(statErr) {
						continue
					}
					return 0, false
				}
				if info.IsDir() {
					continue
				}
				seen[path] = true
				count++
			}
		}
		return count, true
	case "zcode":
		path := harness.SessionStorePath(home, "zcode")
		if _, err := os.Stat(path); err != nil {
			return 0, os.IsNotExist(err)
		}
		db, err := sql.Open("sqlite", ids.SQLiteFileURI(path)+"?mode=ro&_pragma=busy_timeout(1500)")
		if err != nil {
			return 0, false
		}
		defer db.Close()
		var count int
		if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM session WHERE COALESCE(parent_id, '') != '' OR COALESCE(task_type, '') = 'subagent_child'`).Scan(&count); err != nil {
			return 0, false
		}
		return count, true
	default:
		return 0, true
	}
}

// scanHarnessTurns discovers local conversation sessions and extracts User+Assistant
// turn pairs for a given harness (`zcode`, `claude-code`, `codex`, `dsh`, `codebuddy`, `workbuddy`).
func scanHarnessTurns(home, harnessID, scope string, maxTurns int) (int, []candidateEpisodicTurn, int64) {
	sessions, turns, lastActiveAt, _ := scanHarnessTurnsWithCoverage(home, harnessID, scope, maxTurns)
	return sessions, turns, lastActiveAt
}

func scanHarnessTurnsWithCoverage(home, harnessID, scope string, maxTurns int) (int, []candidateEpisodicTurn, int64, bool) {
	sessions, turns, lastActiveAt, complete, _ := scanHarnessTurnsWithCoverageContext(context.Background(), home, harnessID, scope, maxTurns)
	return sessions, turns, lastActiveAt, complete
}

// episodicStampMatters reports whether a source stamp change invalidates the
// completeness verdict. SQLite-backed zcode sources are read through one
// read-only transaction per session: concurrent appends land after the snapshot
// and cannot corrupt the turns already read, so a live database/WAL write is not
// an incomplete scan. Transcript files keep the strict stamp check.
func episodicStampMatters(source episodicScanSource, before, after string) bool {
	return before != after && source.Kind != "zcode"
}

// episodicSourceActivity returns the source's last-write time in unix seconds.
// SQLite-backed sources carry the per-session time_updated (the database file
// mtime is shared by every session); transcript sources fall back to the file
// mtime. Used only for scan ordering, newest first.
func episodicSourceActivity(source episodicScanSource) int64 {
	if source.LastActivity > 0 {
		return source.LastActivity
	}
	if info, err := os.Stat(source.Path); err == nil {
		return info.ModTime().Unix()
	}
	return 0
}

// scanHarnessTurnsWithCoverageContext scans every source without an ingest bound.
func scanHarnessTurnsWithCoverageContext(ctx context.Context, home, harnessID, scope string, maxTurns int) (sessionsCount int, turns []candidateEpisodicTurn, lastActiveAt int64, complete bool, outOfScope int) {
	return scanHarnessTurnsWithCoverageBoundContext(ctx, home, harnessID, scope, maxTurns, 0)
}

// scanHarnessTurnsWithCoverageBoundContext is the coverage scan with the ingest
// lower bound applied to source discovery: a source whose last activity predates
// sync.since cannot contribute an importable turn, so reading it would only spend
// the page budget and could mark the scanned window incomplete for history the
// vault is configured never to store. sessionsCount still reports the full
// inventory; complete describes the collection scope, not the host's whole past.
func scanHarnessTurnsWithCoverageBoundContext(ctx context.Context, home, harnessID, scope string, maxTurns int, since int64) (sessionsCount int, turns []candidateEpisodicTurn, lastActiveAt int64, complete bool, outOfScope int) {
	complete = true
	if maxTurns <= 0 {
		maxTurns = 100
	}
	if harnessID == "opencode" {
		s, t, l, c := scanOpenCodeSessions(ctx, home)
		return s, t, l, c, 0
	}
	if !harness.TurnsScannable(harnessID) {
		seen := map[string]bool{}
		for _, pattern := range harness.TranscriptGlobs(home, harnessID) {
			matches, _ := filepath.Glob(pattern)
			for _, path := range matches {
				info, err := os.Stat(path)
				if err != nil || info.IsDir() || seen[path] {
					continue
				}
				seen[path] = true
				sessionsCount++
				if info.ModTime().Unix() > lastActiveAt {
					lastActiveAt = info.ModTime().Unix()
				}
			}
		}
		return sessionsCount, nil, lastActiveAt, sessionsCount == 0, 0
	}
	sources, err := discoverEpisodicScanSources(ctx, home, harnessID)
	if err != nil {
		complete = false
	}
	sessionsCount = len(sources)
	// Last activity is host telemetry, not collection scope: compute it before
	// the ingest bound filters the read set, so an old host still shows when it
	// last wrote instead of collapsing to "-".
	for _, source := range sources {
		if info, err := os.Stat(source.Path); err == nil && info.ModTime().Unix() > lastActiveAt {
			lastActiveAt = info.ModTime().Unix()
		}
	}
	if since > 0 {
		// Scan scope follows the configured collection window. The inventory
		// count above stays whole-host; only the read set is bounded.
		sources = filterEpisodicScanSourcesSince(sources, since)
	}
	sort.SliceStable(sources, func(i, j int) bool {
		return episodicSourceActivity(sources[i]) > episodicSourceActivity(sources[j])
	})
	for _, source := range sources {
		if ctx.Err() != nil {
			return sessionsCount, turns, lastActiveAt, false, outOfScope
		}
		if len(turns) >= maxTurns {
			complete = false
			continue
		}
		var sourceTurns []candidateEpisodicTurn
		next := 0
		wrapped := false
		before, stampErr := episodicScanSourceStamp(source)
		_, scanErr := streamEpisodicScanSource(ctx, source, scope, func(_ int, turn candidateEpisodicTurn) error {
			// Turns older than the configured ingest bound are outside the
			// collection scope: count them for diagnostics, but never let them
			// consume the page budget or mark the in-bound window incomplete.
			if episodicOccurredBefore(turn.OccurredAt, since) {
				outOfScope++
				return nil
			}
			// Turns without confirmed completion (open tail / aborted run) are
			// not importable by contract. Their absence is reported per harness
			// as IncompleteTurnsCount; it does not make the source unreadable.
			if len(sourceTurns) < maxTurns {
				sourceTurns = append(sourceTurns, turn)
			} else {
				sourceTurns[next] = turn
				next = (next + 1) % maxTurns
				wrapped = true
				complete = false
			}
			return nil
		})
		if wrapped {
			ordered := make([]candidateEpisodicTurn, 0, len(sourceTurns))
			ordered = append(ordered, sourceTurns[next:]...)
			ordered = append(ordered, sourceTurns[:next]...)
			sourceTurns = ordered
		}
		after, afterErr := episodicScanSourceStamp(source)
		if stampErr != nil || scanErr != nil || afterErr != nil || episodicStampMatters(source, before, after) {
			complete = false
		}
		if remaining := maxTurns - len(turns); len(sourceTurns) > remaining {
			complete = false
			sourceTurns = sourceTurns[len(sourceTurns)-remaining:]
		}
		turns = append(turns, sourceTurns...)
	}
	return sessionsCount, turns, lastActiveAt, complete, outOfScope
}

// scanOpenCodeSessions reads OpenCode's SQLite session store for inventory and
// last-activity. OpenCode keeps turns in message/part rows with a host-specific
// JSON schema, so turn extraction stays unsupported; the panel reports
// sessions_scan_supported=true and turns_scan_supported=false.
func scanOpenCodeSessions(ctx context.Context, home string) (int, []candidateEpisodicTurn, int64, bool) {
	path := harness.SessionStorePath(home, "opencode")
	if _, err := os.Stat(path); err != nil {
		return 0, nil, 0, os.IsNotExist(err)
	}
	db, err := sql.Open("sqlite", ids.SQLiteFileURI(path)+"?mode=ro&_pragma=busy_timeout(1500)")
	if err != nil {
		return 0, nil, 0, false
	}
	defer db.Close()
	var count int
	var lastMS int64
	if err := db.QueryRowContext(ctx,
		`SELECT COUNT(*), COALESCE(MAX(time_updated), 0) FROM session WHERE COALESCE(time_archived, 0) = 0`,
	).Scan(&count, &lastMS); err != nil {
		return 0, nil, 0, false
	}
	if lastMS < 0 {
		lastMS = 0
	}
	return count, nil, lastMS / 1000, true
}

// countDirtyBackgroundFiles checks whether any mounted background file on disk has changed mtime/size
// compared to `rm_bg_files`.
func countDirtyBackgroundFiles(db *sql.DB) int {
	rows, err := db.Query(`SELECT abs_path, mtime_ns, file_size FROM rm_bg_files`)
	if err != nil {
		return 0
	}
	defer rows.Close()
	dirty := 0
	for rows.Next() {
		var p string
		var mtimeNS, fsize int64
		if rows.Scan(&p, &mtimeNS, &fsize) != nil {
			continue
		}
		info, sErr := os.Stat(p)
		if sErr != nil || info.ModTime().UnixNano() != mtimeNS || info.Size() != fsize {
			dirty++
		}
	}
	return dirty
}
