// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

// Package efficacy reports hook activity and token economics for the local vault.
package efficacy

import (
	"bufio"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/rocareer/rolling-memory/internal/config"
	"github.com/rocareer/rolling-memory/internal/harness"
)

type Options struct {
	Scope        string
	Harness      string
	Limit        int
	HomeDir      string
	WorkspaceDir string
	// BudgetSeconds bounds the wall-clock time this run may spend on NEW per-turn
	// evaluations. 0 means unlimited. When the budget is exhausted the run stops
	// re-evaluating further turns, finalizes and persists the snapshot with whatever
	// is already settled (Report.Partial=true) instead of being killed mid-flight by
	// the caller, so consecutive runs make strictly monotone persisted progress.
	BudgetSeconds int
}

// FunnelBreakdown categorizes recall hook decisions into five mutually exclusive buckets.
type FunnelBreakdown struct {
	HighValueAdopted int `json:"high_value_adopted"` // Recalled code coords directly hit with fewer exploration steps than baseline
	ContextAssisted  int `json:"context_assisted"`   // Specific recalled entity adopted in reply/action with 0 blind search
	DedupSuppressed  int `json:"dedup_suppressed"`   // Echo/duplicate memory block suppressed by injection_ledger (0 token cost)
	SilenceFiltered  int `json:"silence_filtered"`   // Absent, pure-ack short-circuit, or low-specificity noise blocked (0 token cost)
	NoiseInjected    int `json:"noise_injected"`     // Injected memory not adopted OR agent searched anyway (net token loss)
}

// HarnessEfficacy summarizes efficacy metrics for one coding harness.
type HarnessEfficacy struct {
	ID             string  `json:"id"`
	Title          string  `json:"title"`
	Detected       bool    `json:"detected"`
	Installed      bool    `json:"installed"`
	HooksEnabled   bool    `json:"hooks_enabled"`
	Sessions       int     `json:"sessions"`
	Turns          int     `json:"turns"`
	Injections     int     `json:"injections"`
	Adopted        int     `json:"adopted"`
	Silenced       int     `json:"silenced"`
	AdoptRate      float64 `json:"adopt_rate"`
	SilenceRate    float64 `json:"silence_rate"`
	SavedRounds    int     `json:"saved_rounds"`
	NetSavedTokens int     `json:"net_saved_tokens"`
	AvgLatencyMS   int     `json:"avg_latency_ms"`
	Status         string  `json:"status"` // active | ready | standby
}

// SessionEvaluation holds the per-turn empirical audit record.
type SessionEvaluation struct {
	SessionID         string   `json:"session_id"`
	Harness           string   `json:"harness"`
	Workspace         string   `json:"workspace"`
	Prompt            string   `json:"prompt"`
	Outcome           string   `json:"outcome"` // high_value | assisted | silenced | dedup | noise
	VerdictLabel      string   `json:"verdict_label"`
	AttributionSource string   `json:"attribution_source"` // l1_causal_tool | l1_entity_anchor | l1_reply_coord | l2_piggyback_heal | gate_silence | ledger_dedup | unadopted_noise
	ValuePillar       string   `json:"value_pillar"`       // navigation | guardrail | continuity | silence | noise
	RecalledSummary   string   `json:"recalled_summary"`
	MatchedAnchors    []string `json:"matched_anchors"`
	EchoExcluded      int      `json:"echo_excluded"`
	HasCodeCoord      bool     `json:"has_code_coord"`
	CausalVerified    bool     `json:"causal_verified"`
	SelfHealed        int      `json:"self_healed"`
	ModelRequests     int      `json:"model_requests"`
	ToolCalls         int      `json:"tool_calls"`
	ExploreSteps      int      `json:"explore_steps"`
	RoundCostTokens   int      `json:"round_cost_tokens"`   // marginal exploration round-trip context tokens (fresh + cached)
	RoundFreshTokens  int      `json:"round_fresh_tokens"`  // marginal uncached fresh input + output tokens per exploration round-trip
	RoundCachedTokens int      `json:"round_cached_tokens"` // marginal cached context tokens re-read per exploration round-trip
	SavedRounds       int      `json:"saved_rounds"`
	SavedSeconds      int      `json:"saved_seconds"` // empirical wall-clock exploration wait seconds saved
	InjectedTokens    int      `json:"injected_tokens"`
	SavedTokens       int      `json:"saved_tokens"`        // total saved raw context tokens (SavedFreshTokens + SavedCachedTokens)
	SavedFreshTokens  int      `json:"saved_fresh_tokens"`  // saved uncached input + output tokens
	SavedCachedTokens int      `json:"saved_cached_tokens"` // saved cached context re-read tokens
	BilledNetTokens   int      `json:"billed_net_tokens"`   // API-billed equivalent net saved tokens (SavedFresh + 0.10*SavedCached - Injected)
	NetTokens         int      `json:"net_tokens"`          // raw context net saved tokens (SavedTokens - InjectedTokens)
	LatencyMS         int      `json:"latency_ms"`
	RecordedAt        int64    `json:"recorded_at"`
	TimeText          string   `json:"time_text"`
	ToolTimeline      []string `json:"tool_timeline,omitempty"`
	TargetFiles       []string `json:"target_files,omitempty"`
}

// SessionContinuity tracks the 3D situational awareness readiness (Where / What Just Happened / Active Thread).
type SessionContinuity struct {
	Ready                  bool   `json:"ready"`
	ActiveLocus            string `json:"active_locus"`
	WhereSpatialBands      int    `json:"where_spatial_bands"`      // Spatial anchor bands (project boundaries & redlines)
	EpisodicTailTraces     int    `json:"episodic_tail_traces"`     // Recent turn traces available for "What Just Happened"
	ActiveThreadAssertions int    `json:"active_thread_assertions"` // Recent active assertions (last 24h) for "What Am I Doing Now"
	LedgerProtectedBands   int    `json:"ledger_protected_bands"`   // Session-deduplicated bands in injection_ledger
	LastTailPreview        string `json:"last_tail_preview"`        // 1-line preview of the latest episodic tail
}

// DailySavingBucket aggregates per-day recall efficacy and token savings.
type DailySavingBucket struct {
	Date                 string  `json:"date"`
	Turns                int     `json:"turns"`
	InjectedCount        int     `json:"injected_count"`
	AdoptedCount         int     `json:"adopted_count"`
	SavedRounds          int     `json:"saved_rounds"`
	SavedSeconds         int     `json:"saved_seconds"`
	InjectedTokens       int     `json:"injected_tokens"`
	SavedFreshTokens     int     `json:"saved_fresh_tokens"`
	SavedCachedTokens    int     `json:"saved_cached_tokens"`
	GrossSavedTokens     int     `json:"gross_saved_tokens"`
	BilledNetSavedTokens int     `json:"billed_net_saved_tokens"`
	NetSavedTokens       int     `json:"net_saved_tokens"`
	NetTokenROI          float64 `json:"net_token_roi"`
}

// WindowSavingBucket aggregates recall efficacy and token savings for one rolling
// monitor window (时 / 日 / 周 / 月) so the ROI screen can switch the monitored span
// without re-reading per-turn records client-side.
type WindowSavingBucket struct {
	Key                  string  `json:"key"`        // 1h | 24h | 7d | 30d
	Seconds              int64   `json:"seconds"`    // rolling window length in seconds
	SinceUnix            int64   `json:"since_unix"` // now - seconds at evaluation time
	Turns                int     `json:"turns"`
	Sessions             int     `json:"sessions"`
	InjectedCount        int     `json:"injected_count"`
	AdoptedCount         int     `json:"adopted_count"`
	SilencedCount        int     `json:"silenced_count"`
	DedupCount           int     `json:"dedup_count"`
	SavedRounds          int     `json:"saved_rounds"`
	SavedSeconds         int     `json:"saved_seconds"`
	InjectedTokens       int     `json:"injected_tokens"`
	SavedFreshTokens     int     `json:"saved_fresh_tokens"`
	SavedCachedTokens    int     `json:"saved_cached_tokens"`
	GrossSavedTokens     int     `json:"gross_saved_tokens"`
	BilledNetSavedTokens int     `json:"billed_net_saved_tokens"`
	NetSavedTokens       int     `json:"net_saved_tokens"`
	AdoptRate            float64 `json:"adopt_rate"`
	NetTokenROI          float64 `json:"net_token_roi"`
	BilledTokenROI       float64 `json:"billed_token_roi"`
	AvgLatencyMS         int     `json:"avg_latency_ms"`
	SelfHealedCount      int     `json:"self_healed_count"`
	CodeCoordHits        int     `json:"code_coord_hits"`
	CausalDirectHits     int     `json:"causal_direct_hits"`
	NavigationDirectHits int     `json:"navigation_direct_hits"`
	GuardrailHits        int     `json:"guardrail_hits"`
	ContinuityHits       int     `json:"continuity_hits"`
	// Window-scoped recall decision funnel: the five buckets partition `Turns` exactly like
	// the report-level FunnelBreakdown partitions EvaluatedTurns.
	FunnelHighValue int `json:"funnel_high_value"`
	FunnelAssisted  int `json:"funnel_assisted"`
	FunnelDedup     int `json:"funnel_dedup"`
	FunnelSilence   int `json:"funnel_silence"`
	FunnelNoise     int `json:"funnel_noise"`
	latencySumMS    int
	latencySamples  int
}

// HourlySavingBucket aggregates the same recall efficacy counters per clock hour so the
// 时 window shows a bounded (last 48h) hourly series instead of a single scalar.
type HourlySavingBucket struct {
	Hour                 string  `json:"hour"` // local "2006-01-02 15:00"
	Unix                 int64   `json:"unix"`
	Turns                int     `json:"turns"`
	InjectedCount        int     `json:"injected_count"`
	AdoptedCount         int     `json:"adopted_count"`
	SavedRounds          int     `json:"saved_rounds"`
	SavedSeconds         int     `json:"saved_seconds"`
	InjectedTokens       int     `json:"injected_tokens"`
	SavedFreshTokens     int     `json:"saved_fresh_tokens"`
	SavedCachedTokens    int     `json:"saved_cached_tokens"`
	GrossSavedTokens     int     `json:"gross_saved_tokens"`
	BilledNetSavedTokens int     `json:"billed_net_saved_tokens"`
	NetSavedTokens       int     `json:"net_saved_tokens"`
	NetTokenROI          float64 `json:"net_token_roi"`
}

// Report is the top-level cross-harness recall efficacy evaluation output.
type Report struct {
	Scope                     string               `json:"scope"`
	EvaluatedAt               string               `json:"evaluated_at"`
	TotalHookRuns             int                  `json:"total_hook_runs"`
	TotalSessions             int                  `json:"total_sessions"`
	EvaluatedTurns            int                  `json:"evaluated_turns"`
	InjectedCount             int                  `json:"injected_count"`
	SilencedCount             int                  `json:"silenced_count"`
	AdoptedCount              int                  `json:"adopted_count"`
	CodeCoordHits             int                  `json:"code_coord_hits"`
	CausalDirectHits          int                  `json:"causal_direct_hits"`     // turns where target file Read/Edit occurred with < baseline blind search
	NavigationDirectHits      int                  `json:"navigation_direct_hits"` // Pillar 1: zero-blind-search direct file tool navigation hits
	GuardrailHits             int                  `json:"guardrail_hits"`         // Pillar 2: epistemic guardrail / ruling / gotcha / recipe hits avoiding rework
	ContinuityHits            int                  `json:"continuity_hits"`        // Pillar 3: short-prompt / cross-turn / boot-landmark continuity hits
	EchoExcludedCount         int                  `json:"echo_excluded_count"`    // anchors stripped because they already appeared in the user's prompt or prior turns
	SelfHealedCount           int                  `json:"self_healed_count"`      // Pillar 4: stale assertions superseded/retracted via L2 piggybacked agent writebacks
	WritebackCount            int                  `json:"writeback_count"`        // durable assertions written back via remember across audited sessions
	AdoptRate                 float64              `json:"adopt_rate"`             // % of evaluated injections that genuinely saved steps or supplied adopted facts
	SilenceRate               float64              `json:"silence_rate"`           // % of hook triggers that stayed silent (dedup + absent + guard)
	AvgRoundCostTokens        int                  `json:"avg_round_cost_tokens"`  // marginal context tokens per exploration round-trip (fresh + cached)
	AvgRoundFreshTokens       int                  `json:"avg_round_fresh_tokens"`
	AvgRoundCachedTokens      int                  `json:"avg_round_cached_tokens"`
	AvgSavedRounds            float64              `json:"avg_saved_rounds"`        // avg exploration round-trips saved on adopted turns
	TotalSavedRounds          int                  `json:"total_saved_rounds"`      // all-time cumulative round-trips saved across sessions
	TotalSavedSeconds         int                  `json:"total_saved_seconds"`     // all-time cumulative wall-clock exploration wait seconds saved
	InjectedTokens            int                  `json:"injected_tokens"`         // all-time total tokens spent on recall injections
	SavedFreshTokens          int                  `json:"saved_fresh_tokens"`      // all-time saved uncached input + output tokens
	SavedCachedTokens         int                  `json:"saved_cached_tokens"`     // all-time saved cached historical context tokens
	GrossSavedTokens          int                  `json:"gross_saved_tokens"`      // all-time total saved raw context tokens (SavedFreshTokens + SavedCachedTokens)
	NetSavedTokens            int                  `json:"net_saved_tokens"`        // all-time raw context net saved (gross_saved_tokens - injected_tokens)
	NetTokenROI               float64              `json:"net_token_roi"`           // all-time raw context ROI %
	BilledNetSavedTokens      int                  `json:"billed_net_saved_tokens"` // all-time API-billed equivalent net saved (Fresh + 0.10*Cached - Injected)
	BilledTokenROI            float64              `json:"billed_token_roi"`        // all-time API-billed equivalent ROI %
	LogDedupTotal             int                  `json:"log_dedup_total"`         // all-time hook ledger dedup count from rolling-memory.log
	LogAbsentTotal            int                  `json:"log_absent_total"`        // all-time hook absent count from rolling-memory.log
	TodayTurns                int                  `json:"today_turns"`             // turns evaluated in the active today window
	TodayInjectedCount        int                  `json:"today_injected_count"`
	TodayAdoptedCount         int                  `json:"today_adopted_count"`
	TodaySavedRounds          int                  `json:"today_saved_rounds"`
	TodaySavedSeconds         int                  `json:"today_saved_seconds"`
	TodayInjectedTokens       int                  `json:"today_injected_tokens"`
	TodaySavedFreshTokens     int                  `json:"today_saved_fresh_tokens"`
	TodaySavedCachedTokens    int                  `json:"today_saved_cached_tokens"`
	TodayGrossSavedTokens     int                  `json:"today_gross_saved_tokens"`
	TodayBilledNetSavedTokens int                  `json:"today_billed_net_saved_tokens"`
	TodayBilledTokenROI       float64              `json:"today_billed_token_roi"`
	TodayNetSavedTokens       int                  `json:"today_net_saved_tokens"`
	TodayNetTokenROI          float64              `json:"today_net_token_roi"`
	WeekSavedRounds           int                  `json:"week_saved_rounds"`
	WeekNetSavedTokens        int                  `json:"week_net_saved_tokens"`
	Windows                   []WindowSavingBucket `json:"windows"`        // rolling 1h / 24h / 7d / 30d monitor windows (时 / 日 / 周 / 月)
	HourlySavings             []HourlySavingBucket `json:"hourly_savings"` // last 48 clock-hour buckets for the 时 window series
	DailySavings              []DailySavingBucket  `json:"daily_savings"`
	Partial                   bool                 `json:"partial"`        // true when this snapshot was finalized before every in-window turn was settled
	DeferredTurns             int                  `json:"deferred_turns"` // turns left for the next incremental run (settled from the persistent ledger then)
	BudgetSeconds             int                  `json:"budget_seconds"` // per-run evaluation budget that produced this snapshot (0 = unbounded)
	AvgLatencyMS              int                  `json:"avg_latency_ms"`
	P50LatencyMS              int                  `json:"p50_latency_ms"`
	P95LatencyMS              int                  `json:"p95_latency_ms"`
	EfficacyScore             float64              `json:"efficacy_score"` // strict composite 0..100 score
	Grade                     string               `json:"grade"`          // S | A | B | C | D
	Continuity                SessionContinuity    `json:"continuity"`
	Funnel                    FunnelBreakdown      `json:"funnel"`
	Harnesses                 []HarnessEfficacy    `json:"harnesses"`
	Sessions                  []SessionEvaluation  `json:"sessions"`
}

// ToMap converts Report to a generic map for JSON/API emission, preserving integer types via json.Number.
func (r *Report) ToMap() map[string]any {
	raw, err := json.Marshal(r)
	if err != nil {
		return map[string]any{}
	}
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.UseNumber()
	out := map[string]any{}
	_ = dec.Decode(&out)
	return out
}

type toolAction struct {
	Name      string
	Target    string
	IsSearch  bool
	ReadOnly  bool
	StartedAt int64 // unix seconds or milliseconds
}

type turnUsageRecord struct {
	StartedAt       int64
	CompletedAt     int64
	ModelRequests   int
	ToolCalls       int
	InputTokens     int
	CacheReadTokens int
	OutputTokens    int
	ReasoningTokens int
}

type nativeSessionStats struct {
	Harness            string
	Workspace          string
	Title              string
	Turns              int
	ModelRequests      int
	ToolCalls          int
	ExploreCalls       int
	InputTokens        int
	CacheReadTokens    int
	OutputTokens       int
	ReasoningTokens    int
	RoundFreshTokens   int // Measured uncached fresh input + output tokens per 1 model round-trip
	RoundCachedTokens  int // Measured cached historical session context tokens per 1 model round-trip
	DynamicRoundTokens int // Total context tokens per 1 model round-trip (RoundFreshTokens + RoundCachedTokens)
	TurnUsages         []turnUsageRecord
	Actions            []toolAction
	TouchedFiles       []string
}

type authoredBandRecord struct {
	BandTitle string
	CreatedAt int64
}

type sessionSelfHealStats struct {
	Writebacks    int
	SelfHealed    int
	HealedAt      []int64
	AuthoredBands []authoredBandRecord
}

// Evaluate reports hook activity and local-vault corpus counters. Adoption
// auditing needs host tool traces and the retired assertion-graph replay, so the
// local vault reports the observable hook ledger and token counters only.
func Evaluate(db *sql.DB, cfg *config.Config, opts Options) (*Report, error) {
	scope := strings.TrimSpace(opts.Scope)
	if scope == "" && cfg != nil {
		scope = cfg.Scope
	}
	if scope == "" {
		scope = config.DefaultScope
	}
	home := opts.HomeDir
	if home == "" {
		home, _ = os.UserHomeDir()
	}
	report := &Report{
		Scope:         scope,
		EvaluatedAt:   time.Now().Format("2006-01-02 15:04:05"),
		Harnesses:     []HarnessEfficacy{},
		Sessions:      []SessionEvaluation{},
		Windows:       []WindowSavingBucket{},
		HourlySavings: []HourlySavingBucket{},
		DailySavings:  []DailySavingBucket{},
		BudgetSeconds: opts.BudgetSeconds,
	}

	// Harness inventory.
	harnessOrder := []string{}
	allHarnesses := append(harness.BuildHarnesses(home, ""), harness.BuildExtendedHarnesses(home)...)
	installOptions := harness.DefaultInstallOptions(home, "")
	installedIDs := make(map[string]bool, len(allHarnesses))
	for _, h := range harness.InstalledHarnesses(allHarnesses, installOptions) {
		installedIDs[h.ID] = true
	}
	for _, h := range allHarnesses {
		if h.ID == "" {
			continue
		}
		_, detectErr := os.Stat(h.Detect)
		detected := detectErr == nil
		installed := installedIDs[h.ID]
		status := "standby"
		if installed {
			status = "ready"
		}
		report.Harnesses = append(report.Harnesses, HarnessEfficacy{
			ID:           h.ID,
			Title:        h.Label,
			Detected:     detected,
			Installed:    installed,
			HooksEnabled: h.HooksPath != nil && installed,
			Status:       status,
		})
		harnessOrder = append(harnessOrder, h.ID)
	}
	_ = harnessOrder

	// Hook log counters.
	logPath := ""
	if cfg != nil && cfg.Log != "" {
		logPath = cfg.Log
	} else if home != "" {
		logPath = filepath.Join(home, ".local", "state", "rolling-memory", "rolling-memory.log")
	}
	var latencies []int
	if logPath != "" {
		if file, err := os.Open(logPath); err == nil {
			scanner := bufio.NewScanner(file)
			for scanner.Scan() {
				line := strings.TrimSpace(scanner.Text())
				if line == "" {
					continue
				}
				var row map[string]any
				if json.Unmarshal([]byte(line), &row) != nil {
					continue
				}
				if value, _ := row["event"].(string); value != "recall" {
					continue
				}
				report.TotalHookRuns++
				if ms, ok := row["ms"].(float64); ok && ms > 0 {
					latencies = append(latencies, int(ms))
				}
				switch {
				case numberAtLeastOne(row["dedup"]):
					report.LogDedupTotal++
				case numberAtLeastOne(row["absent"]):
					report.LogAbsentTotal++
				}
			}
			_ = file.Close()
		}
	}
	if len(latencies) > 0 {
		total := 0
		for _, v := range latencies {
			total += v
		}
		report.AvgLatencyMS = total / len(latencies)
		sorted := append([]int(nil), latencies...)
		sort.Ints(sorted)
		report.P50LatencyMS = sorted[len(sorted)/2]
		report.P95LatencyMS = sorted[int(float64(len(sorted)-1)*0.95)]
	}

	// Hook fact ledger (sidecar): observable decisions and outputs.
	seenSessions := map[string]struct{}{}
	if cfg != nil {
		if path, err := DefaultLedgerPath(cfg.DB); err == nil {
			if ledger, err := OpenLedger(path); err == nil {
				if entries, err := ledger.Events(0, 10000); err == nil {
					for _, entry := range entries {
						if entry.Session != "" {
							seenSessions[entry.Session] = struct{}{}
						}
						if entry.Hook != "recall" {
							continue
						}
						switch entry.Action {
						case "output_attempt":
							if entry.OutputOutcome == "output" {
								report.InjectedCount++
							}
						case "decision":
							if entry.OutputOutcome == "empty" {
								report.SilencedCount++
							}
						}
					}
				}
				_ = ledger.Close()
			}
		}
	}
	report.TotalSessions = len(seenSessions)

	// Local corpus counters.
	if db != nil {
		if tableExists(db, "rm_docs") {
			_ = db.QueryRow(`SELECT COUNT(*) FROM rm_docs WHERE layer = 'episodic'`).Scan(&report.EvaluatedTurns)
		}
	}
	return report, nil
}

func numberAtLeastOne(value any) bool {
	number, ok := value.(float64)
	return ok && number >= 1
}

func tableExists(db *sql.DB, name string) bool {
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?`, name).Scan(&count); err != nil {
		return false
	}
	return count > 0
}
