// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package cli

import (
	"bufio"
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
	"github.com/rocareer/rolling-memory/internal/ids"
	"github.com/rocareer/rolling-memory/internal/memory/facade"
	"github.com/rocareer/rolling-memory/internal/memory/localvault"
	"github.com/rocareer/rolling-memory/internal/speech"
)

type episodicScanSource struct {
	HarnessID  string
	Kind       string
	Path       string
	SessionID  string
	Locus      string
	LocusKnown bool
	RawDir     string
	// LastActivity is the source's last write in unix seconds (0 = unknown).
	// Discovery drops sources older than sync.since so the crawl stays
	// proportional to the bound instead of reading every historical transcript.
	LastActivity int64
}

type episodicSourceCursor struct {
	Source    string `json:"source"`
	Index     int    `json:"index"`
	Stamp     string `json:"stamp"`
	LastDocID string `json:"last_doc_id,omitempty"`
	// Skipped counts turns dropped by the sync.since bound in this cycle. They
	// advance Index without consuming the page budget, so browsing the back
	// catalogue costs reads, not one pass per fifty turns.
	Skipped      int    `json:"skipped,omitempty"`
	Grew         bool   `json:"grew,omitempty"`
	RetrySources bool   `json:"retry_sources,omitempty"`
	ItemErrors   int    `json:"item_errors,omitempty"`
	RetryCycle   bool   `json:"retry_cycle,omitempty"`
	TurnRef      string `json:"turn_ref,omitempty"`
	TurnStartIdx int    `json:"turn_start_idx,omitempty"`
}

func (s episodicScanSource) key() string {
	return s.HarnessID + "\x00" + s.Kind + "\x00" + s.Path + "\x00" + s.SessionID
}

func discoverEpisodicScanSources(ctx context.Context, home, harnessID string) ([]episodicScanSource, error) {
	if !harness.TurnsScannable(harnessID) {
		// The host is inventoried (the console counts its sessions) but this
		// build cannot read its turn content. Enumerating its transcripts here
		// would read gigabytes this build cannot parse, hold the shared scan
		// cursor on files that keep changing, and starve the hosts whose turns
		// do import — so the importer stays silent until a turn reader exists.
		return nil, nil
	}
	// Transcript roots come from the harness adapter: the importer and the
	// console's session inventory must never disagree about where a host keeps
	// its conversations.
	patterns := harness.TranscriptGlobs(home, harnessID)
	var sources []episodicScanSource
	seen := map[string]bool{}
	for _, pattern := range patterns {
		matches, err := filepath.Glob(pattern)
		if err != nil {
			return nil, err
		}
		for _, path := range matches {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			if seen[path] {
				continue
			}
			info, err := os.Stat(path)
			if os.IsNotExist(err) {
				continue
			}
			if err != nil {
				return sources, err
			}
			if info.IsDir() {
				continue
			}
			seen[path] = true
			sources = append(sources, episodicScanSource{
				HarnessID:    harnessID,
				Kind:         "jsonl",
				Path:         path,
				LastActivity: info.ModTime().Unix(),
			})
		}
	}
	if harnessID == "zcode" {
		path := harness.SessionStorePath(home, harnessID)
		if _, err := os.Stat(path); err != nil {
			if !os.IsNotExist(err) {
				return nil, err
			}
		} else {
			db, err := sql.Open("sqlite", ids.SQLiteFileURI(path)+"?mode=ro&_pragma=busy_timeout(1500)")
			if err != nil {
				return nil, err
			}
			// One cheap read of each session's last write drives the sync.since
			// source filter; the message table is far too large to scan per session.
			// A read failure aborts the pass because an empty map would leave every
			// source eligible, which is exactly the backfill the bound exists to
			// stop. A schema without time_updated is the one exception: it cannot
			// date a session, so every source stays eligible rather than being
			// wrongly dropped.
			activity := map[string]int64{}
			actRows, actErr := db.QueryContext(ctx, `SELECT id, COALESCE(time_updated, 0) FROM session`)
			if actErr != nil {
				if !strings.Contains(strings.ToLower(actErr.Error()), "no such column") {
					_ = db.Close()
					return nil, fmt.Errorf("zcode session activity: %w", actErr)
				}
			} else {
				for actRows.Next() {
					var id string
					var updated int64
					if scanErr := actRows.Scan(&id, &updated); scanErr != nil {
						_ = actRows.Close()
						_ = db.Close()
						return nil, fmt.Errorf("zcode session activity: %w", scanErr)
					}
					if updated > 0 {
						activity[id] = updated / 1000
					}
				}
				if rowsErr := actRows.Err(); rowsErr != nil {
					_ = actRows.Close()
					_ = db.Close()
					return nil, fmt.Errorf("zcode session activity: %w", rowsErr)
				}
				_ = actRows.Close()
			}
			hasDirCol := true
			rows, err := db.QueryContext(ctx, `SELECT id, COALESCE(directory, '') FROM session WHERE COALESCE(parent_id, '') = '' AND COALESCE(task_type, '') != 'subagent_child' ORDER BY id`)
			if err != nil {
				hasDirCol = false
				rows, err = db.QueryContext(ctx, `SELECT id FROM session WHERE COALESCE(parent_id, '') = '' AND COALESCE(task_type, '') != 'subagent_child' ORDER BY id`)
			}
			if err != nil {
				_ = db.Close()
				return nil, err
			}
			for rows.Next() {
				var session, dir string
				var scanErr error
				if hasDirCol {
					scanErr = rows.Scan(&session, &dir)
				} else {
					scanErr = rows.Scan(&session)
				}
				if scanErr != nil {
					_ = rows.Close()
					_ = db.Close()
					return nil, scanErr
				}
				sources = append(sources, episodicScanSource{
					HarnessID:    harnessID,
					Kind:         "zcode",
					Path:         path,
					SessionID:    session,
					RawDir:       dir,
					LastActivity: activity[session],
				})
			}
			err = rows.Err()
			_ = rows.Close()
			_ = db.Close()
			if err != nil {
				return nil, err
			}
		}
	}
	sort.Slice(sources, func(i, j int) bool { return sources[i].key() < sources[j].key() })
	return sources, nil
}

// resolveJSONLTranscriptLocus extracts the workspace locus recorded inside the
// opening lines of a JSONL session transcript (`cwd` or `payload.cwd`). The
// locus is the canonical workspace identity (repository root basename, "" when
// the cwd sits outside any repository); resolved reports whether a cwd was
// found at all, so callers can tell "outside any repository" apart from "no
// readable cwd" (unknown — keep the stored locus).
func resolveJSONLTranscriptLocus(path string) (string, bool) {
	file, err := episodic.OpenJSONL(strings.TrimSpace(path))
	if err != nil {
		return "", false
	}
	defer file.Close()
	reader := bufio.NewReader(file)
	for lineIdx := 0; lineIdx < 32; lineIdx++ {
		line, readErr := reader.ReadString('\n')
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "{") && strings.Contains(trimmed, `"cwd"`) {
			var obj map[string]any
			if json.Unmarshal([]byte(trimmed), &obj) == nil && obj != nil {
				cwd, _ := obj["cwd"].(string)
				if cwd == "" {
					if payload, ok := obj["payload"].(map[string]any); ok && payload != nil {
						cwd, _ = payload["cwd"].(string)
					}
				}
				if cwd = strings.TrimSpace(cwd); cwd != "" {
					if !filepath.IsAbs(cwd) {
						return "", false
					}
					return facade.ResolveWorkspaceLocus(cwd), true
				}
			}
		}
		if readErr != nil {
			break
		}
	}
	return "", false
}

func episodicScanSourceStamp(source episodicScanSource) (string, error) {
	info, err := os.Stat(source.Path)
	if err != nil {
		return "", err
	}
	stamp := fmt.Sprintf("%d:%d", info.ModTime().UnixNano(), info.Size())
	if source.Kind == "zcode" {
		wal, err := os.Stat(source.Path + "-wal")
		if err == nil {
			stamp += fmt.Sprintf(":%d:%d", wal.ModTime().UnixNano(), wal.Size())
		} else if !os.IsNotExist(err) {
			return "", err
		}
	}
	return stamp, nil
}

var (
	errEpisodicScanPageStop = errors.New("episodic scan page stop")
	errEpisodicSourceItem   = errors.New("episodic source item unavailable")
)

func streamEpisodicScanSource(ctx context.Context, source episodicScanSource, scope string, visit func(int, candidateEpisodicTurn) error) (int, error) {
	return streamEpisodicScanSourceFrom(ctx, source, scope, "", 0, func(i int, _ int, turn candidateEpisodicTurn) error {
		return visit(i, turn)
	})
}

// emitEpisodicScanSegmentUnits derives the units of one enumerated segment and
// hands each candidate to visit with its running unit ordinal. The streaming turn
// reader and the frozen-parse reader both call it, so `#<k>` and the emitted
// candidates cannot drift between the two paths.
func emitEpisodicScanSegmentUnits(ctx context.Context, source episodicScanSource, scope, prefix string, turnStart, unitIdx int, segment episodic.Segment, timestamp int64, completed bool, visit func(unitIdx int, turnStartIdx int, turn candidateEpisodicTurn) error) (int, error) {
	for _, unit := range episodicunit.SegmentUnits(scope, prefix, segment, timestamp, completed) {
		if err := ctx.Err(); err != nil {
			return unitIdx, err
		}
		occurredAt := segment.OccurredAt
		if occurredAt > 1e11 {
			occurredAt /= 1000
		}
		if occurredAt < 0 {
			occurredAt = 0
		}
		turn := candidateEpisodicTurn{
			HarnessID:  source.HarnessID,
			SessionID:  source.SessionID,
			Locus:      source.Locus,
			LocusKnown: source.LocusKnown,
			DocID:      unit.DocID,
			RefPointer: unit.RefPointer,
			TurnRef:    prefix,
			Text:       unit.Text,
			OccurredAt: occurredAt,
			Completed:  unit.Completed,
		}
		curIdx := unitIdx
		unitIdx++
		if err := visit(curIdx, turnStart, turn); err != nil {
			return unitIdx, err
		}
	}
	return unitIdx, nil
}

func streamEpisodicScanSourceFrom(ctx context.Context, source episodicScanSource, scope, startTurnRef string, startTurnIdx int, visit func(unitIdx int, turnStartIdx int, turn candidateEpisodicTurn) error) (int, error) {
	if !source.LocusKnown {
		switch source.Kind {
		case "jsonl":
			source.Locus, source.LocusKnown = resolveJSONLTranscriptLocus(source.Path)
		case "zcode":
			if filepath.IsAbs(source.RawDir) {
				source.Locus = facade.ResolveWorkspaceLocus(source.RawDir)
				source.LocusKnown = true
			}
		}
	}
	idx := 0
	switch source.Kind {
	case "jsonl":
		source.SessionID = harness.TranscriptSessionID(source.HarnessID, source.Path)
		err := episodic.VisitJSONLTurns(ctx, source.Path, func(turn episodic.JSONLTurn, closed bool) error {
			prefix := fmt.Sprintf("@ref:jsonl:%s:%d", source.Path, turn.Index)
			turnStart := idx
			for _, segment := range turn.Segments {
				var emitErr error
				idx, emitErr = emitEpisodicScanSegmentUnits(ctx, source, scope, prefix, turnStart, idx, segment, turn.OccurredAt, closed, visit)
				if emitErr != nil {
					return emitErr
				}
			}
			return nil
		})
		return idx, err
	case "zcode":
		db, err := sql.Open("sqlite", ids.SQLiteFileURI(source.Path)+"?mode=ro&_pragma=busy_timeout(1500)")
		if err != nil {
			return 0, err
		}
		defer db.Close()
		tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
		if err != nil {
			return 0, err
		}
		defer tx.Rollback()
		startUserMsgID := ""
		expectedPrefix := "@ref:zcode:" + source.SessionID + ":"
		if startTurnIdx > 0 && strings.HasPrefix(startTurnRef, expectedPrefix) {
			candidateMsgID := strings.TrimPrefix(startTurnRef, expectedPrefix)
			var seq sql.NullInt64
			if err := tx.QueryRowContext(ctx, `SELECT sequence FROM message WHERE session_id = ? AND id = ?`, source.SessionID, candidateMsgID).Scan(&seq); err == nil && seq.Valid {
				startUserMsgID = candidateMsgID
				idx = startTurnIdx
			} else if err != nil && !errors.Is(err, sql.ErrNoRows) {
				return 0, err
			}
		}
		err = episodic.VisitZCodeTurnsFrom(ctx, tx, source.SessionID, startUserMsgID, func(turn episodic.Turn, _ bool) error {
			ts := turn.OccurredAt
			if ts > 1e11 {
				ts /= 1000
			}
			completed, err := zcodeTurnCompleted(ctx, tx, source.SessionID, turn.UserMsgID)
			if err != nil {
				return err
			}
			prefix := fmt.Sprintf("@ref:zcode:%s:%s", source.SessionID, turn.UserMsgID)
			turnStart := idx
			for _, segment := range turn.Segments {
				var emitErr error
				idx, emitErr = emitEpisodicScanSegmentUnits(ctx, source, scope, prefix, turnStart, idx, segment, ts, completed, visit)
				if emitErr != nil {
					return emitErr
				}
			}
			return nil
		})
		return idx, err
	default:
		return 0, fmt.Errorf("unknown episodic source %q", source.Kind)
	}
}

// streamEpisodicScanSnapshotFrom enumerates one JSONL source's units from a frozen
// parse: the same unit derivation, pointer prefixes and candidates as
// streamEpisodicScanSourceFrom, without re-reading or re-decompressing the
// transcript. A cursor resume point (`startTurnRef` / `startTurnIdx`) restarts the
// enumeration at the saved turn with its saved unit ordinal when the ref names this
// source's own pointer grammar and the turn still exists in the frozen parse;
// otherwise enumeration restarts at turn 0 with ordinal 0, so the caller's
// `unitIdx < start` skip observes exactly the units the streaming reader would have
// produced.
func streamEpisodicScanSnapshotFrom(ctx context.Context, source episodicScanSource, scope string, snapshot *episodic.SourceSnapshot, startTurnRef string, startTurnIdx int, visit func(unitIdx int, turnStartIdx int, turn candidateEpisodicTurn) error) (int, error) {
	if !source.LocusKnown {
		source.Locus, source.LocusKnown = resolveJSONLTranscriptLocus(source.Path)
	}
	source.SessionID = harness.TranscriptSessionID(source.HarnessID, source.Path)
	idx := 0
	firstTurn := 0
	if startTurnIdx > 0 {
		prefix := "@ref:jsonl:" + source.Path + ":"
		if resume := strings.TrimSpace(startTurnRef); strings.HasPrefix(resume, prefix) {
			if turn, convErr := strconv.Atoi(strings.TrimPrefix(resume, prefix)); convErr == nil && turn >= 0 && turn < snapshot.Turns() {
				firstTurn, idx = turn, startTurnIdx
			}
		}
	}
	for turn := firstTurn; turn < snapshot.Turns(); turn++ {
		if err := ctx.Err(); err != nil {
			return idx, err
		}
		segments, ok := snapshot.Segments(turn)
		if !ok {
			continue
		}
		occurredAt, _ := snapshot.OccurredAt(turn)
		completed := snapshot.Completed(turn)
		prefix := fmt.Sprintf("@ref:jsonl:%s:%d", source.Path, turn)
		turnStart := idx
		for _, segment := range segments {
			var emitErr error
			idx, emitErr = emitEpisodicScanSegmentUnits(ctx, source, scope, prefix, turnStart, idx, segment, occurredAt, completed, visit)
			if emitErr != nil {
				return idx, emitErr
			}
		}
	}
	return idx, nil
}

// episodicScanSourceSnapshot builds the one frozen parse this page's iteration of a
// JSONL source runs on: every pointer verification of the page then answers from it
// in O(1) instead of re-reading the transcript per unit. A non-JSONL source, or a
// parse that fails (oversize, corrupt, cancelled), yields nil and the caller keeps
// the streaming path unchanged.
func episodicScanSourceSnapshot(ctx context.Context, source episodicScanSource) *episodic.SourceSnapshot {
	if source.Kind != "jsonl" {
		return nil
	}
	snapshot, err := episodic.NewSourceSnapshotContext(ctx, source.Path)
	if err != nil {
		return nil
	}
	return snapshot
}

// episodicScanUnits streams one source's units: from the frozen parse when snapshot
// is non-nil, from the live readers otherwise. Both produce the same unit sequence
// (the same ordinals, prefixes and candidates) for a JSONL source.
func episodicScanUnits(ctx context.Context, source episodicScanSource, scope string, snapshot *episodic.SourceSnapshot, startTurnRef string, startTurnIdx int, visit func(unitIdx int, turnStartIdx int, turn candidateEpisodicTurn) error) (int, error) {
	if snapshot != nil && source.Kind == "jsonl" {
		return streamEpisodicScanSnapshotFrom(ctx, source, scope, snapshot, startTurnRef, startTurnIdx, visit)
	}
	return streamEpisodicScanSourceFrom(ctx, source, scope, startTurnRef, startTurnIdx, visit)
}

// zcodeTurnCompleted checks the final message before the next user message that
// the shared turn reader would accept as speech. Hidden and tool-result messages
// do not form turn boundaries.
func zcodeTurnCompleted(ctx context.Context, db episodic.ZCodeQueryer, sessionID, userMsgID string) (bool, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT m.id, COALESCE(json_extract(m.data, '$.role'), ''),
		       COALESCE(json_extract(m.data, '$.time.completed'), 0),
		       COALESCE(json_extract(m.data, '$.finish'), '')
		FROM message m
		WHERE m.session_id = ? AND m.sequence > (SELECT sequence FROM message WHERE session_id = ? AND id = ?)
		ORDER BY m.sequence ASC`, sessionID, sessionID, userMsgID)
	if err != nil {
		return false, err
	}
	type message struct {
		id, role, finish string
		completion       int64
	}
	defer rows.Close()
	var last message
	for rows.Next() {
		var m message
		if err := rows.Scan(&m.id, &m.role, &m.completion, &m.finish); err != nil {
			return false, err
		}
		role := strings.ToLower(strings.TrimSpace(m.role))
		if role == "user" || role == "human" {
			var texts string
			err := db.QueryRowContext(ctx, `SELECT COALESCE(json_group_array(json_extract(p.data, '$.text')), '[]') FROM part p WHERE p.message_id = ? AND `+episodic.ZCodeTextPartFilter, m.id).Scan(&texts)
			if err != nil {
				return false, err
			}
			var parts []string
			if err := json.Unmarshal([]byte(texts), &parts); err != nil {
				return false, err
			}
			boundary := false
			for _, text := range parts {
				if speech.StripConversationNoise(text) != "" {
					boundary = true
					break
				}
			}
			if boundary {
				break
			}
			continue
		}
		last = m
	}
	if err := rows.Err(); err != nil {
		return false, err
	}
	if err := rows.Close(); err != nil {
		return false, err
	}
	role := strings.ToLower(strings.TrimSpace(last.role))
	if (role != "assistant" && role != "ai" && role != "bot" && role != "agent") || last.completion <= 0 || last.finish != "stop" {
		return false, nil
	}
	var hasTool int
	if err := db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM part WHERE message_id = ? AND json_extract(data, '$.type') = 'tool')`, last.id).Scan(&hasTool); err != nil {
		return false, err
	}
	return hasTool == 0, nil
}

// runEpisodicSourceScanTick keeps the history scan independent of the foreground
// reconcile window. A completed cycle starts over so changes and streaming tails
// are revisited, while each checkpoint contains only successfully saved work.
func runEpisodicSourceScanTick(ctx context.Context, memory *facade.Memory, home string) error {
	return runEpisodicSourceScanTickWithLimit(ctx, memory, home, reconcileDefaultLimit)
}

// runEpisodicSourceScanTickWithLimit scans up to limit turns from the discovered
// sources. The recovery command uses a larger page and a longer budget; the
// autowatch path keeps the bounded default.
func runEpisodicSourceScanTickWithLimit(ctx context.Context, memory *facade.Memory, home string, limit int) error {
	if limit <= 0 {
		limit = reconcileDefaultLimit
	}
	if memory == nil || memory.Store == nil || memory.Store.DB() == nil {
		return errors.New("episodic source scan requires an open local store")
	}
	scope := config.DefaultScope
	if memory.Config != nil && strings.TrimSpace(memory.Config.Scope) != "" {
		scope = strings.TrimSpace(memory.Config.Scope)
	}
	tc, err := memory.LocalVault()
	if err != nil {
		return err
	}
	if tc == nil || tc.Vault == nil {
		return errors.New("episodic source scan requires a local vault")
	}
	db := memory.Store.DB()
	deletedEpi := tc.Vault.DeletedUnitSet("episodic")
	key := "episodic_source_scan_cursor:" + scope
	var raw []byte
	var cursor episodicSourceCursor
	err = db.QueryRowContext(ctx, `SELECT v FROM rm_meta WHERE k = ?`, key).Scan(&raw)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	exists := err == nil
	if exists {
		if err := json.Unmarshal(raw, &cursor); err != nil {
			return fmt.Errorf("episodic source scan checkpoint: %w", err)
		}
	}
	var sources []episodicScanSource
	var discoveryErr error
	for _, definition := range harnessEnvDefinitions(home, "") {
		discoverCtx, cancelDiscover := context.WithTimeout(ctx, 5*time.Second)
		found, err := discoverEpisodicScanSources(discoverCtx, home, definition.ID)
		cancelDiscover()
		if err != nil {
			discoveryErr = errors.Join(discoveryErr, fmt.Errorf("%s: %w", definition.ID, err))
		}
		sources = append(sources, found...)
	}
	// A configured lower bound keeps an existing history out of the vault: the
	// turn is skipped without being stored, while the cursor still advances past
	// it, so a machine with a large transcript store can start collecting from
	// now on instead of rewriting everything first.
	since, sinceErr := EpisodicScanSince(memory.Config.Path)
	if sinceErr != nil {
		return sinceErr
	}
	// The turn filter below only saves writes: the walk would still read every
	// historical transcript and the page budget is 50 turns, so reaching today
	// through the back catalogue would take hours. Drop whole sources older than
	// the bound first; the turn filter then trims the boundary session, whose
	// activity is current but whose older turns must stay out.
	sources = filterEpisodicScanSourcesSince(sources, since)
	next, more, scanErr := visitEpisodicSourcesVerified(ctx, sources, scope, cursor, limit, since, func(snapshot *episodic.SourceSnapshot, turn candidateEpisodicTurn) error {
		// Second line: the walker already drops out-of-bound turns, but the write
		// itself must never persist one if a future caller reaches this point.
		if episodicOccurredBefore(turn.OccurredAt, since) {
			return nil
		}
		return saveEpisodicSourceTurnVerified(ctx, db, tc.Vault, scope, deletedEpi, turn, snapshot)
	})
	skippedBeforeSince := next.Skipped
	if err := saveEpisodicScanSkipped(db, scope, skippedBeforeSince); err != nil {
		return err
	}
	if discoveryErr != nil {
		next.RetrySources = true
		if !more {
			next = episodicSourceCursor{RetrySources: true, RetryCycle: true, ItemErrors: next.ItemErrors}
		}
	}
	if discoveryErr == nil && scanErr == nil && !more {
		next = episodicSourceCursor{}
	}
	nextRaw, err := json.Marshal(next)
	if err != nil {
		return err
	}
	if err := saveEpisodicCheckpoint(db, key, raw, exists, nextRaw); err != nil {
		return errors.Join(discoveryErr, scanErr, fmt.Errorf("episodic source scan checkpoint: %w", err))
	}
	if next.ItemErrors > 0 {
		if _, err := db.ExecContext(ctx, `INSERT OR REPLACE INTO rm_meta(k, v) VALUES (?, ?)`, "episodic_source_scan_item_errors:"+scope, next.ItemErrors); err != nil {
			return errors.Join(discoveryErr, scanErr, err)
		}
	} else if discoveryErr == nil && scanErr == nil && !more {
		if _, err := db.ExecContext(ctx, `DELETE FROM rm_meta WHERE k = ? AND EXISTS (SELECT 1 FROM rm_meta WHERE k = ? AND CAST(v AS BLOB) = ?)`, "episodic_source_scan_item_errors:"+scope, key, nextRaw); err != nil {
			return err
		}
	}
	return errors.Join(discoveryErr, scanErr)
}

// saveEpisodicScanSkipped records how many turns the sync.since bound kept out
// of the vault during the last pass, so `scan status` can show that the skip is
// working rather than looking like a stalled scanner.
func saveEpisodicScanSkipped(db *sql.DB, scope string, skipped int) error {
	key := "episodic_source_scan_skipped:" + scope
	if skipped <= 0 {
		_, err := db.ExecContext(context.Background(), `DELETE FROM rm_meta WHERE k = ?`, key)
		return err
	}
	_, err := db.ExecContext(context.Background(), `INSERT OR REPLACE INTO rm_meta(k, v) VALUES (?, ?)`, key, strconv.Itoa(skipped))
	return err
}

// readEpisodicScanSkipped reports the skip count recorded by the last pass.
func readEpisodicScanSkipped(db *sql.DB, scope string) int {
	var raw string
	if err := db.QueryRow(`SELECT CAST(v AS TEXT) FROM rm_meta WHERE k = ?`, "episodic_source_scan_skipped:"+scope).Scan(&raw); err != nil {
		return 0
	}
	skipped, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil {
		return 0
	}
	return skipped
}

// filterEpisodicScanSourcesSince drops sources whose last activity is older than
// the bound. A source with unknown activity is kept: it cannot be proven old, and
// dropping it would hide new turns.
func filterEpisodicScanSourcesSince(sources []episodicScanSource, since int64) []episodicScanSource {
	if since <= 0 {
		return sources
	}
	kept := sources[:0]
	for _, source := range sources {
		if source.LastActivity > 0 && source.LastActivity < since {
			continue
		}
		kept = append(kept, source)
	}
	return kept
}

// episodicOccurredBefore reports whether a turn predates the bound. Some readers
// keep millisecond timestamps while the bound is seconds, so the comparison
// normalises first; without it a millisecond value reads as "far future" and the
// turn slips through. An unknown time is out of bound when a bound is set.
func episodicOccurredBefore(occurredAt, since int64) bool {
	if since <= 0 {
		return false
	}
	if occurredAt > 1e11 {
		occurredAt /= 1000
	}
	return occurredAt <= 0 || occurredAt < since
}

// saveEpisodicSourceTurn stores one scanned turn with the live pointer
// verification (`SaveLocalEpisodicPointerIndexed`).
func saveEpisodicSourceTurn(ctx context.Context, db *sql.DB, base *localvault.Vault, scope string, deletedEpi map[string]bool, turn candidateEpisodicTurn) error {
	return saveEpisodicSourceTurnVerified(ctx, db, base, scope, deletedEpi, turn, nil)
}

// saveEpisodicSourceTurnVerified stores one scanned turn whose pointer was
// enumerated from the page's frozen parse (`snapshot`, nil when the streaming
// fallback produced the turn): every write verifies its pointer against that one
// parse instead of re-reading and re-decompressing the transcript per unit. The
// turn bookkeeping, tombstone handling and error classification are identical to
// the live path.
func saveEpisodicSourceTurnVerified(ctx context.Context, db *sql.DB, base *localvault.Vault, scope string, deletedEpi map[string]bool, turn candidateEpisodicTurn, snapshot *episodic.SourceSnapshot) error {
	if episodicunit.DocID(scope, turn.Text) != turn.DocID {
		return fmt.Errorf("episodic source scan %s: content identity mismatch", turn.DocID)
	}
	if !turn.Completed || deletedEpi[turn.DocID] {
		return nil
	}
	vault := base.CloneWithSharedSlotCache()
	vault.SetIdentity(localvault.VaultIdentity{
		AssistantID:   turn.HarnessID,
		IncarnationID: turn.SessionID,
		Locus:         turn.Locus,
	})
	var stored string
	err := db.QueryRowContext(ctx, `SELECT COALESCE(NULLIF(m.ref_pointer, ''), d.raw_text) FROM rm_docs d LEFT JOIN rm_unit_meta m ON m.unit_id = d.unit_id WHERE d.unit_id = ?`, turn.DocID).Scan(&stored)
	if err == nil {
		if stored != turn.RefPointer {
			// Authenticate the existing pointer before adding another occurrence.
			// An unavailable legacy source must not block unrelated transcripts.
			if strings.HasPrefix(stored, "@ref:") && !vault.HasLocalEpisodicIndex(turn.DocID, stored) {
				if _, err := vault.EnsureLocalEpisodicIndexVerified(scope, snapshot, turn.DocID, stored, turn.Text); err != nil {
					if errors.Is(err, localvault.ErrEpisodicUnitDeleted) {
						return nil
					}
					if errors.Is(err, localvault.ErrEpisodicIndexItem) {
						return fmt.Errorf("%w: %s: %v", errEpisodicSourceItem, turn.DocID, err)
					}
					return err
				}
			}
			if sErr := vault.SaveLocalEpisodicPointerIndexedVerified(scope, snapshot, turn.DocID, turn.OccurredAt, turn.RefPointer, turn.TurnRef, turn.Text, localvault.IngestResponse{}); sErr != nil {
				if errors.Is(sErr, localvault.ErrEpisodicUnitDeleted) {
					return nil
				}
				if errors.Is(sErr, localvault.ErrEpisodicIndexItem) || errors.Is(sErr, localvault.ErrEpisodicPointerSourceConflict) {
					return fmt.Errorf("%w: %s: %v", errEpisodicSourceItem, turn.DocID, sErr)
				}
				return sErr
			}
		} else {
			if !vault.HasLocalEpisodicIndex(turn.DocID, turn.RefPointer) {
				if _, err = vault.EnsureLocalEpisodicIndexVerified(scope, snapshot, turn.DocID, turn.RefPointer, turn.Text); err != nil {
					if errors.Is(err, localvault.ErrEpisodicUnitDeleted) {
						return nil
					}
					if errors.Is(err, localvault.ErrEpisodicIndexItem) {
						return fmt.Errorf("%w: %s: %v", errEpisodicSourceItem, turn.DocID, err)
					}
					return err
				}
			}
			var sourceRecorded int
			if err := db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM rm_episodic_sources s JOIN rm_episodic_lineage l ON l.turn_ref = s.turn_ref AND l.doc_id = s.doc_id WHERE s.turn_ref = ? AND s.doc_id = ? AND s.ref_pointer = ?)`, turn.TurnRef, turn.DocID, turn.RefPointer).Scan(&sourceRecorded); err != nil {
				return err
			}
			if sourceRecorded == 0 {
				if err := vault.SaveLocalEpisodicPointerIndexedVerified(scope, snapshot, turn.DocID, turn.OccurredAt, turn.RefPointer, turn.TurnRef, turn.Text, localvault.IngestResponse{}); err != nil {
					if errors.Is(err, localvault.ErrEpisodicUnitDeleted) {
						return nil
					}
					if errors.Is(err, localvault.ErrEpisodicIndexItem) {
						return fmt.Errorf("%w: %s: %v", errEpisodicSourceItem, turn.DocID, err)
					}
					return err
				}
			}
			if err := updateEpisodicUnitSourceMeta(ctx, db, turn.DocID, turn.RefPointer, turn.HarnessID, turn.SessionID, turn.LocusKnown, turn.Locus, turn.OccurredAt); err != nil {
				return err
			}
		}
		return nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if err := vault.SaveLocalEpisodicPointerIndexedVerified(scope, snapshot, turn.DocID, turn.OccurredAt, turn.RefPointer, turn.TurnRef, turn.Text, localvault.IngestResponse{}); err != nil {
		if errors.Is(err, localvault.ErrEpisodicUnitDeleted) {
			return nil
		}
		if errors.Is(err, localvault.ErrEpisodicIndexItem) || errors.Is(err, localvault.ErrEpisodicPointerSourceConflict) {
			return fmt.Errorf("%w: %s: %v", errEpisodicSourceItem, turn.DocID, err)
		}
		return err
	}
	return nil
}

// visitEpisodicSources advances only after each successful visit. Changes during
// a page are retried, and completed cycles revisit earlier source occurrences.
func visitEpisodicSources(ctx context.Context, sources []episodicScanSource, scope string, cursor episodicSourceCursor, limit int, visit func(candidateEpisodicTurn) error) (episodicSourceCursor, bool, error) {
	return visitEpisodicSourcePages(ctx, sources, scope, cursor, limit, 0, func(_ *episodic.SourceSnapshot, turn candidateEpisodicTurn) error {
		return visit(turn)
	})
}

// visitEpisodicSourcesVerified is visitEpisodicSources with the page's frozen parse
// of the visited turn's source handed to the callback, so one save can verify each
// pointer in O(1) against the same view the iteration used instead of re-reading
// the transcript per unit. The snapshot is non-nil exactly for JSONL turns whose
// source parse was constructed for this page; a nil snapshot means the streaming
// fallback produced the turn.
func visitEpisodicSourcesVerified(ctx context.Context, sources []episodicScanSource, scope string, cursor episodicSourceCursor, limit int, since int64, visit func(snapshot *episodic.SourceSnapshot, turn candidateEpisodicTurn) error) (episodicSourceCursor, bool, error) {
	return visitEpisodicSourcePages(ctx, sources, scope, cursor, limit, since, visit)
}

func visitEpisodicSourcePages(ctx context.Context, sources []episodicScanSource, scope string, cursor episodicSourceCursor, limit int, since int64, visit func(snapshot *episodic.SourceSnapshot, turn candidateEpisodicTurn) error) (episodicSourceCursor, bool, error) {
	if limit <= 0 {
		return cursor, false, fmt.Errorf("episodic scan page requires a positive limit")
	}
	ordered := append([]episodicScanSource(nil), sources...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].key() < ordered[j].key() })
	visited := 0
	var sourceErr error
	hadSourceErr := cursor.RetrySources
	itemErrors := cursor.ItemErrors
	itemErrorReported := false
	if cursor.Source == "" && cursor.RetryCycle {
		hadSourceErr = false
		itemErrors = 0
	}

	for srcIdx, source := range ordered {
		if err := ctx.Err(); err != nil {
			cursor.RetrySources = true
			return cursor, true, errors.Join(sourceErr, err)
		}
		key := source.key()
		if key < cursor.Source {
			continue
		}
		stamp, err := episodicScanSourceStamp(source)
		if err != nil {
			sourceErr = errors.Join(sourceErr, err)
			hadSourceErr = true
			continue
		}
		// One frozen parse per source per page drives the whole page of this source:
		// the anchor probe, the turn iteration and the per-unit save verification all
		// answer from it (one read instead of one re-read per unit). The snapshot is
		// kept for the source's page and closed before the next source; a source whose
		// parse fails (oversize, corrupt, cancelled) keeps the streaming path
		// byte-for-byte, and a non-JSONL source has none.
		entryCursor := cursor
		snapshot := episodicScanSourceSnapshot(ctx, source)
		closeSnapshot := func() {
			if snapshot != nil {
				_ = snapshot.Close()
				snapshot = nil
			}
		}
		start := 0
		grew := false
		if key == cursor.Source && cursor.Index > 0 {
			start = cursor.Index
			grew = cursor.Grew
			if stamp != cursor.Stamp && cursor.Stamp != "" && source.Kind != "zcode" {
				var anchorMatched bool
				cnt, sErr := episodicScanUnits(ctx, source, scope, snapshot, "", 0, func(i int, _ int, turn candidateEpisodicTurn) error {
					if i == start-1 {
						anchorMatched = (turn.DocID == cursor.LastDocID)
						return errEpisodicScanPageStop
					}
					return nil
				})
				if sErr != nil && !errors.Is(sErr, errEpisodicScanPageStop) {
					if errors.Is(sErr, context.Canceled) || errors.Is(sErr, context.DeadlineExceeded) {
						closeSnapshot()
						cursor.RetrySources = true
						return cursor, true, errors.Join(sourceErr, sErr)
					}
					closeSnapshot()
					sourceErr = errors.Join(sourceErr, sErr)
					hadSourceErr = true
					continue
				}
				if cnt < start || (cursor.LastDocID != "" && !anchorMatched) {
					start = 0
					grew = false
				} else {
					if srcIdx+1 < len(ordered) && cursor.Grew {
						closeSnapshot()
						continue
					}
					grew = true
				}
			}
		}

		var visitCallErr error
		lastDocID := ""
		startTurnRef := ""
		startTurnIdx := 0
		lastTurnRef := ""
		lastTurnStartIdx := 0
		if key == cursor.Source && start > 0 {
			lastDocID = cursor.LastDocID
			startTurnRef = cursor.TurnRef
			startTurnIdx = cursor.TurnStartIdx
			lastTurnRef = cursor.TurnRef
			lastTurnStartIdx = cursor.TurnStartIdx
		}
		lastVisitedIdx := start
		stoppedForLimit := false
		_, sErr := episodicScanUnits(ctx, source, scope, snapshot, startTurnRef, startTurnIdx, func(i int, turnStart int, turn candidateEpisodicTurn) error {
			if i < start {
				return nil
			}
			// Turns older than the bound advance the cursor but do not consume the
			// page budget: a back catalogue of tens of thousands of turns must not
			// cost one pass per fifty of them.
			if episodicOccurredBefore(turn.OccurredAt, since) {
				lastVisitedIdx = i + 1
				cursor.Source = key
				cursor.Index = lastVisitedIdx
				cursor.Stamp = stamp
				cursor.LastDocID = turn.DocID
				cursor.TurnRef = turn.TurnRef
				cursor.TurnStartIdx = turnStart
				cursor.Skipped++
				return nil
			}
			if visited == limit {
				stoppedForLimit = true
				return errEpisodicScanPageStop
			}
			if err := visit(snapshot, turn); err != nil {
				if !errors.Is(err, errEpisodicSourceItem) {
					visitCallErr = err
					return errEpisodicScanPageStop
				}
				if !itemErrorReported {
					sourceErr = errors.Join(sourceErr, err)
					itemErrorReported = true
				}
				hadSourceErr = true
				itemErrors++
			}
			lastDocID = turn.DocID
			lastTurnRef = turn.TurnRef
			lastTurnStartIdx = turnStart
			lastVisitedIdx = i + 1
			cursor = episodicSourceCursor{
				Source:       key,
				Index:        lastVisitedIdx,
				Stamp:        stamp,
				LastDocID:    lastDocID,
				Grew:         grew,
				RetrySources: hadSourceErr,
				ItemErrors:   itemErrors,
				TurnRef:      lastTurnRef,
				TurnStartIdx: lastTurnStartIdx,
			}
			visited++
			return nil
		})
		if visitCallErr != nil {
			closeSnapshot()
			cursor.RetrySources = true
			return cursor, true, errors.Join(sourceErr, visitCallErr)
		}
		if sErr != nil && !errors.Is(sErr, errEpisodicScanPageStop) {
			if errors.Is(sErr, context.Canceled) || errors.Is(sErr, context.DeadlineExceeded) {
				closeSnapshot()
				cursor.RetrySources = true
				return cursor, true, errors.Join(sourceErr, sErr)
			}
			closeSnapshot()
			sourceErr = errors.Join(sourceErr, sErr)
			hadSourceErr = true
			cursor.RetrySources = true
			continue
		}
		if snapshot != nil && snapshot.Stale() {
			// The frozen parse no longer describes the source: the file changed while
			// the page was running, so the page's saves were verified against a view the
			// source has left. The page fails and the cursor does not advance past it —
			// the same failure class as a source that changed mid-stream — and the next
			// page re-observes and retries from this checkpoint (saves are idempotent).
			closeSnapshot()
			cursor = entryCursor
			cursor.RetrySources = true
			return cursor, true, errors.Join(sourceErr, fmt.Errorf("episodic source changed during scan: %s", source.Path))
		}
		closeSnapshot()
		after, err := episodicScanSourceStamp(source)
		if err != nil {
			sourceErr = errors.Join(sourceErr, err)
			hadSourceErr = true
			continue
		}
		if after != stamp && source.Kind != "zcode" {
			cursor.RetrySources = true
			return cursor, true, errors.Join(sourceErr, fmt.Errorf("episodic source changed during scan: %s", source.Path))
		}
		if stoppedForLimit {
			cursor.RetrySources = hadSourceErr
			return cursor, true, sourceErr
		}
		cursor = episodicSourceCursor{
			Source:       key,
			Index:        lastVisitedIdx,
			Stamp:        stamp,
			LastDocID:    lastDocID,
			Grew:         grew,
			RetrySources: hadSourceErr,
			ItemErrors:   itemErrors,
			TurnRef:      lastTurnRef,
			TurnStartIdx: lastTurnStartIdx,
		}
	}
	if hadSourceErr {
		return episodicSourceCursor{RetrySources: true, RetryCycle: true, ItemErrors: itemErrors}, true, sourceErr
	}
	return cursor, false, nil
}
