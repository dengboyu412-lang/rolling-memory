// plugin: rolling-memory v0.0.58
package rollingmemory

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ai-go-hub/ai-go-admin/internal/kit/httpx"
	"github.com/gin-gonic/gin"
)

type roiFactsRequest struct {
	Scope   string `json:"scope"`
	Harness string `json:"harness"`
	Limit   int    `json:"limit"`
}

type roiFactEvent struct {
	ObservedAt  int64  `json:"observed_at"`
	Action      string `json:"action"`
	Outcome     string `json:"outcome"`
	Observation string `json:"observation"`
	Harness     string `json:"harness"`
	Hook        string `json:"hook"`
	TextBytes   *int64 `json:"text_bytes"`
}

// roiFactEffectiveRow is one settled ledger event from a single effective-set scan.
type roiFactEffectiveRow struct {
	Action       string
	Outcome      string
	Observation  string
	Harness      string
	Hook         string
	Bytes        sql.NullInt64
	ObservedAt   int64
	EffectiveSeq int64
}

type roiFactCounts struct {
	Total             int64            `json:"total"`
	Actions           map[string]int64 `json:"actions"`
	DecisionOutcomes  map[string]int64 `json:"decision_outcomes"`
	OutputOutcomes    map[string]int64 `json:"output_outcomes"`
	RecallOutcomes    map[string]int64 `json:"recall_outcomes"`
	KnownTextBytes    int64            `json:"known_text_bytes"`
	KnownByteEvents   int64            `json:"known_byte_events"`
	UnknownByteEvents int64            `json:"unknown_byte_events"`
}

// roiFactWindow aggregates settled ledger events and live vault activity into one rolling window.
type roiFactWindow struct {
	Key                 string           `json:"key"`
	Seconds             int64            `json:"seconds"`
	SinceUnix           int64            `json:"since_unix"`
	Total               int64            `json:"total"`
	Actions             map[string]int64 `json:"actions"`
	RecallOutcomes      map[string]int64 `json:"recall_outcomes"`
	KnownTextBytes      int64            `json:"known_text_bytes"`
	UnknownByteEvents   int64            `json:"unknown_byte_events"`
	EpisodicAdded       int64            `json:"episodic_added"`
	BackgroundAdded     int64            `json:"background_added"`
	NoeticCrystallized  int64            `json:"noetic_crystallized"`
	CitationLinks       int64            `json:"citation_links"`
	UniqueCitedUnits    int64            `json:"unique_cited_units"`
	DeliveredTokens     int64            `json:"delivered_tokens"`
	AuditedRecalls      int64            `json:"audited_recalls"`
	AdoptedRecalls      int64            `json:"adopted_recalls"`
	AdoptedToolCount    int64            `json:"adopted_tool_count"`
	AdoptedContextCount int64            `json:"adopted_context_count"`
	AdoptionRatePct     float64          `json:"adoption_rate_pct"`
}

// roiDailyBucket tracks daily 3-layer ingestion, L3 crystallization, citation
// activity, and the settled evidence-adoption audit outcome of that ingest day.
type roiDailyBucket struct {
	Date                string  `json:"date"`
	EpisodicCount       int64   `json:"episodic_count"`
	BackgroundCount     int64   `json:"background_count"`
	NoeticCount         int64   `json:"noetic_count"`
	CitationLinks       int64   `json:"citation_links"`
	DeliveredTokens     int64   `json:"delivered_tokens"`
	AdoptedCount        int64   `json:"adopted_count"`
	AdoptedToolCount    int64   `json:"adopted_tool_count"`
	AdoptedContextCount int64   `json:"adopted_context_count"`
	UnadoptedCount      int64   `json:"unadopted_count"`
	PendingCount        int64   `json:"pending_count"`
	AdoptionRatePct     float64 `json:"adoption_rate_pct"`
}

// roiUnitCitation is one recorded recall that cited a unit, with the full query
// and the crystallized answer so the provenance drawer never shows a truncated row.
type roiUnitCitation struct {
	NoeticID        string   `json:"noetic_id"`
	QueryText       string   `json:"query_text"`
	AnswerText      string   `json:"answer_text"`
	OccurredAt      int64    `json:"occurred_at"`
	AssistantID     string   `json:"assistant_id,omitempty"`
	AdoptionOutcome string   `json:"adoption_outcome,omitempty"`
	MatchedAnchors  []string `json:"matched_anchors,omitempty"`
}

// roiTopCitedUnit ranks underlying L1/L2 memory units by how often L3 Noetic recalls cite them.
type roiTopCitedUnit struct {
	UnitID          string            `json:"unit_id"`
	Layer           string            `json:"layer"`
	Locus           string            `json:"locus"`
	AddressURI      string            `json:"address_uri"`
	WaveformBar     string            `json:"waveform_bar"`
	CitationCount   int64             `json:"citation_count"`
	AdoptedCount    int64             `json:"adopted_count"`
	LastCitedAt     int64             `json:"last_cited_at"`
	OccurredAt      int64             `json:"occurred_at"`
	Preview         string            `json:"preview"`
	FullText        string            `json:"full_text"`
	IsHydrated      bool              `json:"is_hydrated"`
	Tokens          int64             `json:"tokens"`
	IsZeroCopyRef   bool              `json:"is_zero_copy_ref"`
	RecentQueries   []string          `json:"recent_queries"`
	Citations       []roiUnitCitation `json:"citations"`
	storedRef       string
	fallbackPreview string
}

// roiCitedUnitDetail is one cited unit expanded for the L3 evidence drawer.
type roiCitedUnitDetail struct {
	UnitID        string `json:"unit_id"`
	Layer         string `json:"layer"`
	Locus         string `json:"locus"`
	AddressURI    string `json:"address_uri"`
	StoredText    string `json:"stored_text"`
	FullText      string `json:"full_text"`
	IsHydrated    bool   `json:"is_hydrated"`
	IsZeroCopyRef bool   `json:"is_zero_copy_ref"`
}

// roiLocusRow summarizes per-workspace (locus) memory coverage and recall citation heat.
type roiLocusRow struct {
	Locus           string `json:"locus"`
	EpisodicCount   int64  `json:"episodic_count"`
	BackgroundCount int64  `json:"background_count"`
	CitedUnitCount  int64  `json:"cited_unit_count"`
	CitationHits    int64  `json:"citation_hits"`
	AdoptedHits     int64  `json:"adopted_hits"`
	LastActiveAt    int64  `json:"last_active_at"`
	Status          string `json:"status"` // "hot" | "warm" | "cold"
}

// roiNoeticEvent represents one crystallized L3 Noetic retrieval handle and its cited evidence.
type roiNoeticEvent struct {
	NoeticID          string               `json:"noetic_id"`
	OccurredAt        int64                `json:"occurred_at"`
	IngestAt          int64                `json:"-"`
	Status            string               `json:"status"`
	QueryText         string               `json:"query_text"`
	AnswerText        string               `json:"answer_text"`
	CitedUnitIDs      []string             `json:"cited_unit_ids"`
	CitedSentences    []string             `json:"cited_sentences"`
	CitedUnits        []roiCitedUnitDetail `json:"cited_units"`
	CitationCount     int                  `json:"citation_count"`
	DeliveredTokens   int64                `json:"delivered_tokens"`
	AddressURI        string               `json:"address_uri"`
	WaveformBar       string               `json:"waveform_bar"`
	AssistantID       string               `json:"assistant_id"`
	IncarnationID     string               `json:"incarnation_id"`
	AdoptionOutcome   string               `json:"adoption_outcome"`
	MatchedAnchors    []string             `json:"matched_anchors"`
	EchoExcludedCount int                  `json:"echo_excluded_count"`
}

// roiFactsVault reports what the selected memory database currently holds, along
// with physical tokenz-r distillation compression and cross-session citation reuse.
type roiFactsVault struct {
	SchemaEra               string  `json:"schema_era"`
	TotalUnits              int64   `json:"total_units"`
	BackgroundDirs          int64   `json:"background_dirs"`
	BackgroundFiles         int64   `json:"background_files"`
	BackgroundChunks        int64   `json:"background_chunks"`
	BackgroundBytes         int64   `json:"background_bytes"`
	EpisodicDocs            int64   `json:"episodic_docs"`
	EpisodicPointers        int64   `json:"episodic_pointers"`
	EpisodicText            int64   `json:"episodic_text"`
	EpisodicPointerRatioPct float64 `json:"episodic_pointer_ratio_pct"`
	EpisodicCells           int64   `json:"episodic_cells"`
	NoeticUnits             int64   `json:"noetic_units"`
	NoeticStale             int64   `json:"noetic_stale"`
	NoeticWithCitations     int64   `json:"noetic_with_citations"`
	TotalCitationLinks      int64   `json:"total_citation_links"`
	UniqueCitedUnits        int64   `json:"unique_cited_units"`
	ReusedUnitsCount        int64   `json:"reused_units_count"`
	AvgReuseMultiplier      float64 `json:"avg_reuse_multiplier"`
	MaxReuseCount           int64   `json:"max_reuse_count"`
	AvgCitationsPerRecall   float64 `json:"avg_citations_per_recall"`
	TotalDeliveredTokens    int64   `json:"total_delivered_tokens"`
	AvgDeliveredTokens      int64   `json:"avg_delivered_tokens"`
	InlineRawTokens         int64   `json:"inline_raw_tokens"`
	InlineDistilledTokens   int64   `json:"inline_distilled_tokens"`
	DistillationSavedPct    float64 `json:"distillation_saved_pct"`
	TodayEpisodic           int64   `json:"today_episodic"`
	TodayBackground         int64   `json:"today_background"`
	TodayNoetic             int64   `json:"today_noetic"`
	TodayCitations          int64   `json:"today_citations"`
	InjectionLedgerRows     int64   `json:"injection_ledger_rows"`
	AuditedRecalls          int64   `json:"audited_recalls"`
	AdoptedRecalls          int64   `json:"adopted_recalls"`
	AdoptedToolCount        int64   `json:"adopted_tool_count"`
	AdoptedContextCount     int64   `json:"adopted_context_count"`
	EchoExcludedTotal       int64   `json:"echo_excluded_total"`
	AdoptionRatePct         float64 `json:"adoption_rate_pct"`
}

type roiFactsResponse struct {
	Status         string            `json:"status"`
	Reason         string            `json:"reason,omitempty"`
	Source         string            `json:"source"`
	Coverage       string            `json:"coverage"`
	IdentityStatus string            `json:"identity_status"`
	Scope          string            `json:"scope"`
	Harness        string            `json:"harness"`
	Counts         *roiFactCounts    `json:"counts"`
	Windows        []roiFactWindow   `json:"windows"`
	VaultWindows   []roiFactWindow   `json:"vault_windows"`
	Vault          *roiFactsVault    `json:"vault"`
	DailySeries    []roiDailyBucket  `json:"daily_series"`
	TopCitedUnits  []roiTopCitedUnit `json:"top_cited_units"`
	LocusMatrix    []roiLocusRow     `json:"locus_matrix"`
	NoeticStream   []roiNoeticEvent  `json:"noetic_stream"`
	RecentEvents   []roiFactEvent    `json:"recent_events"`
}

// roiFactWindows is the fixed rolling-window set the monitor reports.
var roiFactWindows = []struct {
	Key     string
	Seconds int64
}{
	{Key: "1h", Seconds: 3600},
	{Key: "24h", Seconds: 86400},
	{Key: "7d", Seconds: 7 * 86400},
	{Key: "30d", Seconds: 30 * 86400},
}

func roiFactsUnknown(scope, harness, reason string) roiFactsResponse {
	return roiFactsResponse{
		Status:         "unknown",
		Reason:         reason,
		Source:         "admin_selected_db",
		Coverage:       "unknown",
		IdentityStatus: "unknown",
		Scope:          scope,
		Harness:        harness,
		Windows:        []roiFactWindow{},
		VaultWindows:   []roiFactWindow{},
		DailySeries:    []roiDailyBucket{},
		TopCitedUnits:  []roiTopCitedUnit{},
		LocusMatrix:    []roiLocusRow{},
		NoeticStream:   []roiNoeticEvent{},
		RecentEvents:   []roiFactEvent{},
	}
}

func roiFactsHandler(c *gin.Context) {
	var req roiFactsRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		httpx.Fail(c, httpx.WithMessage("参数格式错误"))
		return
	}
	home, err := os.UserHomeDir()
	if err != nil {
		httpx.Success(c, httpx.WithData(roiFactsUnknown(req.Scope, req.Harness, "database_unavailable")))
		return
	}
	dbPath := resolveMemoryDBPath(home)
	triggerProductionRecallLedgerSync(home, dbPath)
	resp := queryROIFacts(c.Request.Context(), dbPath, req)
	if resp.Status == "available" && resp.IdentityStatus != "verified" {
		resp.Status, resp.Reason, resp.Coverage, resp.IdentityStatus = "unknown", "ledger_identity_unverified", "unknown", "unknown"
		resp.Counts, resp.Windows, resp.RecentEvents = nil, []roiFactWindow{}, []roiFactEvent{}
	}
	httpx.Success(c, httpx.WithData(resp))
}

// bytesToTokenzR converts UTF-8 delivered recall payload bytes into tokenz-r
// tokens using the measured 128k Byte-Level BPE density on <rolling-memory-recall>
// bilingual outputs (0.24 token/byte).
func bytesToTokenzR(bytes int64) int64 {
	if bytes <= 0 {
		return 0
	}
	tok := int64(float64(bytes)*0.24 + 0.5)
	if tok < 1 {
		tok = 1
	}
	return tok
}

// The background-document cache does not store episodic pointer text.
const (
	hydrateCacheTTL       = 2 * time.Minute
	hydrateCacheMaxKeys   = 131072
	hydrateFlavorLocalDoc = "localdoc"
	localDocCacheTTL      = hydrateCacheTTL
)

type hydrateCacheEntry struct {
	text string
	at   time.Time
}

var (
	hydrateMu    sync.Mutex
	hydrateCache = map[string]hydrateCacheEntry{}
)

func hydrateCacheKey(flavor, home, pointer string) string {
	return flavor + "\x00" + strings.TrimSpace(home) + "\x00" + strings.TrimSpace(pointer)
}

// readHydrateCacheWithin reads a cached background document body within its TTL.
func readHydrateCacheWithin(key string, ttl time.Duration) (string, bool) {
	hydrateMu.Lock()
	entry, ok := hydrateCache[key]
	hydrateMu.Unlock()
	if !ok || time.Since(entry.at) >= ttl {
		return "", false
	}
	return entry.text, true
}

// storeHydrateCache 写入缓存；容量满时先清理过期项，仍满则淘汰 1/4（避免整表清空引发遍历期缓存雪崩）。
func storeHydrateCache(key, text string) {
	now := time.Now()
	hydrateMu.Lock()
	evictHydrateCacheLocked(now)
	hydrateCache[key] = hydrateCacheEntry{text: text, at: now}
	hydrateMu.Unlock()
}

func evictHydrateCacheLocked(now time.Time) {
	if len(hydrateCache) < hydrateCacheMaxKeys {
		return
	}
	for k, v := range hydrateCache {
		if now.Sub(v.at) >= hydrateCacheTTL {
			delete(hydrateCache, k)
		}
	}
	if len(hydrateCache) < hydrateCacheMaxKeys {
		return
	}
	target := hydrateCacheMaxKeys / 4
	removed := 0
	for k := range hydrateCache {
		delete(hydrateCache, k)
		removed++
		if removed >= target {
			break
		}
	}
}

type hydrateRef struct {
	DocID string `json:"doc_id"`
	Ref   string `json:"ref"`
}

// hydratedEpisodicPointerLive never reuses a prior verification across requests.
func hydratedEpisodicPointerLive(home, dbPath, docID, pointer string) (string, bool) {
	item := hydrateRef{DocID: strings.TrimSpace(docID), Ref: strings.TrimSpace(pointer)}
	texts, _ := hydrateRefsViaDaemon(home, dbPath, []hydrateRef{item})
	text := strings.TrimSpace(texts[item])
	return text, text != ""
}

// hydrateDaemonBatchSize 单次向守护进程请求水合的指针数上限；超出即分批。
const hydrateDaemonBatchSize = 64

// hydrateRefsViaDaemon returns only results explicitly verified for the requested
// document and pointer. Missing, malformed, or failed results remain unavailable.
// 水合一律经产品二进制 `engine hydrate` 的已核验连接执行（控制台不自带 socket 客户端，
// 也未核验的 socket 一律失败关闭）；守护进程不可用时 handled=false，调用方按
// 「正文暂不可读」降级。
func hydrateRefsViaDaemon(home, dbPath string, refs []hydrateRef) (map[hydrateRef]string, bool) {
	if len(refs) == 0 || strings.TrimSpace(dbPath) == "" {
		return nil, false
	}
	switch strings.ToLower(strings.TrimSpace(os.Getenv("ROLLING_MEMORY_DAEMON"))) {
	case "0", "false", "off", "no":
		return nil, false
	}
	// 测试二进制默认不路由守护进程（与引擎侧 ShouldUseUnixDaemon 同一不变量），
	// 除非显式指定 socket 覆盖，避免单测串到开发机上的真实实例。
	if adminTestBinary() && strings.TrimSpace(os.Getenv("ROLLING_MEMORY_DAEMON_SOCKET")) == "" {
		return nil, false
	}
	sockPath := resolveDaemonSocketPath(home)
	if _, err := os.Stat(sockPath); err != nil {
		triggerConsoleDaemonBootstrap(home, dbPath)
		return nil, false
	}
	bin := resolveBinary(home)
	if bin == "" {
		return nil, false
	}
	out := make(map[hydrateRef]string, len(refs))
	handled := false
	unique := make([]hydrateRef, 0, len(refs))
	seenRefs := make(map[hydrateRef]bool, len(refs))
	for _, ref := range refs {
		ref.DocID, ref.Ref = strings.TrimSpace(ref.DocID), strings.TrimSpace(ref.Ref)
		if ref.DocID != "" && strings.HasPrefix(ref.Ref, "@ref:") && !seenRefs[ref] {
			seenRefs[ref] = true
			unique = append(unique, ref)
		}
	}
	refs = unique
	for start := 0; start < len(refs); start += hydrateDaemonBatchSize {
		end := start + hydrateDaemonBatchSize
		if end > len(refs) {
			end = len(refs)
		}
		items := refs[start:end]
		requested := make(map[hydrateRef]bool, len(items))
		for _, ref := range items {
			requested[ref] = true
		}
		bodyBytes, err := json.Marshal(map[string]any{"db": dbPath, "items": items})
		if err != nil {
			break
		}
		ctx, cancel := context.WithTimeout(context.Background(), engineDataExecTimeout)
		rawBody, execErr := engineCommandRunner(ctx, bin, "hydrate", []string{"--db", dbPath}, bodyBytes)
		cancel()
		var dResp struct {
			OK    bool `json:"ok"`
			Texts []struct {
				DocID     string `json:"doc_id"`
				Ref       string `json:"ref"`
				ActualRef string `json:"actual_ref"`
				Text      string `json:"text"`
				Verified  bool   `json:"verified"`
				Error     string `json:"error"`
			} `json:"texts"`
		}
		if execErr != nil || json.Unmarshal(rawBody, &dResp) != nil || !dResp.OK {
			if execErr != nil {
				triggerConsoleDaemonBootstrap(home, dbPath)
			}
			break
		}
		handled = true
		seen := make(map[hydrateRef]bool, len(items))
		for _, item := range dResp.Texts {
			key := hydrateRef{DocID: strings.TrimSpace(item.DocID), Ref: strings.TrimSpace(item.Ref)}
			if !requested[key] {
				continue
			}
			if seen[key] {
				delete(out, key)
				requested[key] = false
				continue
			}
			seen[key] = true
			// A relocated source cannot be displayed under the requested coordinate.
			if item.Verified && item.Error == "" && strings.TrimSpace(item.ActualRef) == key.Ref && strings.TrimSpace(item.Text) != "" {
				out[key] = item.Text
			}
		}
	}
	return out, handled
}

// documentDaemonBatchSize 单次向守护进程请求正文解析的文档数上限；超出即分批。
// 每个 L1 分块都要现场重读重切其源文件，批量宁小勿大（守护进程侧另有 64 条硬上限）。
const documentDaemonBatchSize = 32

// localDocumentBody 是守护进程按规范口径解析出的一份本地文档正文：
// L1 背景分块 = 源文件磁盘现场重读重切 + 已存 kept 句索引重建的蒸馏句。
type localDocumentBody struct {
	raw       string
	distilled string
}

// documentsViaDaemon 经产品二进制 `engine documents` 的已核验通道批量解析本地文档正文
// （`/v1/documents` → 产品正典 `Vault.LoadLocalDocument`）。控制台禁止导入 localvault，
// 也不允许自研第二套文件分块副本——L1 分块的正文只能由内核现场重读重切，展示层不得
// 用路径坐标冒充正文；控制台亦不自带 socket 客户端，未核验 socket 一律失败关闭；
// handled=false 表示守护进程不可用，调用方按「正文暂不可读」降级。
//
// 返回 map 中出现的 unitID 即为守护进程已裁决（raw 可能为空 = 源文件不可读/内容为空，
// 调用方应负缓存，绝不用本地副本猜内容顶上）。
func documentsViaDaemon(home string, unitIDs []string) (map[string]localDocumentBody, bool) {
	if len(unitIDs) == 0 {
		return nil, false
	}
	switch strings.ToLower(strings.TrimSpace(os.Getenv("ROLLING_MEMORY_DAEMON"))) {
	case "0", "false", "off", "no":
		return nil, false
	}
	// 测试二进制默认不路由守护进程（与引擎侧 ShouldUseUnixDaemon 同一不变量），
	// 除非显式指定 socket 覆盖，避免单测串到开发机上的真实实例。
	if adminTestBinary() && strings.TrimSpace(os.Getenv("ROLLING_MEMORY_DAEMON_SOCKET")) == "" {
		return nil, false
	}
	sockPath := resolveDaemonSocketPath(home)
	if _, err := os.Stat(sockPath); err != nil {
		triggerConsoleDaemonBootstrap(home, resolveMemoryDBPath(home))
		return nil, false
	}
	bin := resolveBinary(home)
	if bin == "" {
		return nil, false
	}
	dbPath := resolveMemoryDBPath(home)
	out := make(map[string]localDocumentBody, len(unitIDs))
	for start := 0; start < len(unitIDs); start += documentDaemonBatchSize {
		end := start + documentDaemonBatchSize
		if end > len(unitIDs) {
			end = len(unitIDs)
		}
		chunk := unitIDs[start:end]
		items := make([]map[string]string, 0, len(chunk))
		for _, unitID := range chunk {
			items = append(items, map[string]string{"unit_id": unitID})
		}
		bodyBytes, err := json.Marshal(map[string]any{"db": dbPath, "items": items})
		if err != nil {
			break
		}
		ctx, cancel := context.WithTimeout(context.Background(), engineDataExecTimeout)
		rawBody, execErr := engineCommandRunner(ctx, bin, "documents", []string{"--db", dbPath}, bodyBytes)
		cancel()
		var dResp struct {
			OK   bool `json:"ok"`
			Docs []struct {
				UnitID        string `json:"unit_id"`
				RawText       string `json:"raw_text"`
				DistilledText string `json:"distilled_text"`
				Error         string `json:"error"`
			} `json:"docs"`
		}
		if execErr != nil || json.Unmarshal(rawBody, &dResp) != nil || !dResp.OK {
			if execErr != nil {
				triggerConsoleDaemonBootstrap(home, dbPath)
			}
			break
		}
		for _, item := range dResp.Docs {
			unitID := strings.TrimSpace(item.UnitID)
			if unitID == "" {
				continue
			}
			if strings.TrimSpace(item.Error) != "" {
				out[unitID] = localDocumentBody{}
				continue
			}
			out[unitID] = localDocumentBody{raw: item.RawText, distilled: item.DistilledText}
		}
	}
	if len(out) == 0 {
		return nil, false
	}
	return out, true
}

// hydratedLocalDocumentLive 缓存优先、未命中同步向守护进程解析一次。
// L1 分块正文是磁盘现场的重读重切结果（短 TTL），且绝不自研第二套文件分块副本兜底：
// 守护进程不可用即判「正文暂不可读」，展示层回退到来源位置预览 + 如实说明。
func hydratedLocalDocumentLive(home, unitID string) (string, bool) {
	unitID = strings.TrimSpace(unitID)
	if unitID == "" {
		return "", false
	}
	key := hydrateCacheKey(hydrateFlavorLocalDoc, home, unitID)
	if text, ok := readHydrateCacheWithin(key, localDocCacheTTL); ok {
		return text, text != ""
	}
	bodies, handled := documentsViaDaemon(home, []string{unitID})
	if !handled {
		return "", false
	}
	body, answered := bodies[unitID]
	if !answered {
		return "", false
	}
	// 守护进程已裁决（含源文件不可读/内容为空）→ 负缓存，避免每轮重复打守护进程。
	text := strings.TrimSpace(body.raw)
	storeHydrateCache(key, text)
	return text, text != ""
}

// invalidateDaemonViaSocket 经产品二进制 `engine invalidate` 的已核验单连接通知本机常驻
// 守护进程立即丢弃内存中的三层快照与分块缓存；尽力而为，守护进程不在或不核验一律静默。
func invalidateDaemonViaSocket(home string) {
	if strings.TrimSpace(home) == "" {
		return
	}
	switch strings.ToLower(strings.TrimSpace(os.Getenv("ROLLING_MEMORY_DAEMON"))) {
	case "0", "false", "off", "no":
		return
	}
	if adminTestBinary() && strings.TrimSpace(os.Getenv("ROLLING_MEMORY_DAEMON_SOCKET")) == "" {
		return
	}
	sockPath := resolveDaemonSocketPath(home)
	if _, err := os.Stat(sockPath); err != nil {
		return
	}
	bin := resolveBinary(home)
	if bin == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), engineInvalidateExecTimeout)
	defer cancel()
	_, _ = engineCommandRunner(ctx, bin, "invalidate", []string{"--db", resolveMemoryDBPath(home)}, []byte(`{}`))
}

// warmHydrateLocalDocumentsBatch 为三层列表投影行中的 L1 背景分块（src == 'chunk'）
// 批量预热正文：一次守护进程批量解析（≤ documentDaemonBatchSize 条/批）写入缓存，
// 供 layerRowFromSQL 逐行读取，避免逐行同步打守护进程。守护进程不可用时不做任何事，
// 展示层按「正文暂不可读」降级（保留来源位置预览）。
func warmHydrateLocalDocumentsBatch(home string, rows []map[string]any) {
	if strings.TrimSpace(home) == "" || len(rows) == 0 {
		return
	}
	seen := make(map[string]struct{}, len(rows))
	unitIDs := make([]string, 0, len(rows))
	for _, r := range rows {
		if strings.ToLower(strings.TrimSpace(asString(r["src"]))) != "chunk" {
			continue
		}
		unitID := strings.TrimSpace(asString(r["id"]))
		if unitID == "" {
			continue
		}
		if _, dup := seen[unitID]; dup {
			continue
		}
		seen[unitID] = struct{}{}
		if _, ok := readHydrateCacheWithin(hydrateCacheKey(hydrateFlavorLocalDoc, home, unitID), localDocCacheTTL); ok {
			continue
		}
		unitIDs = append(unitIDs, unitID)
	}
	if len(unitIDs) == 0 {
		return
	}
	bodies, handled := documentsViaDaemon(home, unitIDs)
	if !handled {
		return
	}
	for _, unitID := range unitIDs {
		body, answered := bodies[unitID]
		if !answered {
			continue
		}
		storeHydrateCache(hydrateCacheKey(hydrateFlavorLocalDoc, home, unitID), strings.TrimSpace(body.raw))
	}
}

// warmHydrateZCodePointersBatch keeps verified text only in the caller-owned rows.
func warmHydrateZCodePointersBatch(home, dbPath string, rows []map[string]any) {
	refs := make([]hydrateRef, 0, len(rows))
	seen := make(map[hydrateRef]bool, len(rows))
	for _, r := range rows {
		delete(r, "_hydrated_ref_text")
		delete(r, "_hydrated_ref_checked")
		ref := strings.TrimSpace(asString(r["content"]))
		if !strings.HasPrefix(ref, "@ref:") {
			continue
		}
		r["_hydrated_ref_checked"] = true
		item := hydrateRef{DocID: strings.TrimSpace(asString(r["id"])), Ref: ref}
		if item.DocID != "" && !seen[item] {
			seen[item] = true
			refs = append(refs, item)
		}
	}
	texts, _ := hydrateRefsViaDaemon(home, dbPath, refs)
	for _, r := range rows {
		ref := hydrateRef{DocID: strings.TrimSpace(asString(r["id"])), Ref: strings.TrimSpace(asString(r["content"]))}
		if text, ok := texts[ref]; ok {
			r["_hydrated_ref_text"] = strings.TrimSpace(text)
		}
	}
}

func hydratedPointerRowText(r map[string]any) (string, bool) {
	text := strings.TrimSpace(asString(r["_hydrated_ref_text"]))
	checked, _ := r["_hydrated_ref_checked"].(bool)
	return text, checked && text != ""
}

// zcodeRecallLedgerLookback 是宿主库首次导入的回溯窗口。宿主库
// （~/.zcode/cli/db/db.sqlite）由外部进程持续读写、体量在 GB 级：只回溯近期流水即可满足
// 面板 1h / 24h / 7d / 30d 窗口，避免对无索引谓词做全表扫。
const zcodeRecallLedgerLookback = 30 * 24 * time.Hour

var (
	zcodeLedgerSyncMu      sync.Mutex
	zcodeLedgerSyncRunning bool
)

// triggerProductionRecallLedgerSync 以单飞方式在后台导入宿主 MCP recall 流水。
// 宿主库是外部在读写的 GB 级实时库，导入绝不阻塞控制台读接口：本轮快照少读一段，
// 下一轮补齐（导入按 event_id 幂等）。
func triggerProductionRecallLedgerSync(home, memoryPath string) {
	zcodeLedgerSyncMu.Lock()
	if zcodeLedgerSyncRunning {
		zcodeLedgerSyncMu.Unlock()
		return
	}
	zcodeLedgerSyncRunning = true
	zcodeLedgerSyncMu.Unlock()

	go func() {
		defer func() {
			zcodeLedgerSyncMu.Lock()
			zcodeLedgerSyncRunning = false
			zcodeLedgerSyncMu.Unlock()
		}()
		syncProductionRecallLedger(home, memoryPath)
	}()
}

// readZcodeToolWatermark 返回已导入的宿主 recall 流水水位（毫秒时间戳；0 表示尚未导入）。
func readZcodeToolWatermark(ldb *sql.DB) int64 {
	var raw string
	if err := ldb.QueryRow(`SELECT value FROM efficacy_meta WHERE key = 'zcode_tool_watermark'`).Scan(&raw); err != nil {
		return 0
	}
	ms, _ := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
	return ms
}

// readAdminLedgerFingerprint 只读读取账本已绑定的 db_fingerprint；文件缺失、
// 未绑定或读取失败一律返回 ""（调用方按「无法证明异库」处理，绝不误判）。
func readAdminLedgerFingerprint(ledgerPath string) string {
	db := openVaultReadOnly(ledgerPath)
	if db == nil {
		return ""
	}
	defer db.Close()
	var stored string
	if err := db.QueryRow(`SELECT value FROM efficacy_meta WHERE key = 'db_fingerprint'`).Scan(&stored); err != nil {
		return ""
	}
	return strings.TrimSpace(stored)
}

// rotateAdminLedgerEpoch 把账本与其 -wal/-shm 归档成 .epoch-<unix>[-N] 侧册
// （与引擎侧轮转约定同名，绝不删除数据）；重名时递增序号直到可用。
func rotateAdminLedgerEpoch(ledgerPath string) error {
	baseSuffix := fmt.Sprintf(".epoch-%d", time.Now().Unix())
	suffix := baseSuffix
	for seq := 1; ; seq++ {
		if _, err := os.Stat(ledgerPath + suffix); errors.Is(err, os.ErrNotExist) {
			break
		}
		suffix = fmt.Sprintf("%s-%d", baseSuffix, seq)
	}
	for _, candidate := range []string{ledgerPath, ledgerPath + "-wal", ledgerPath + "-shm"} {
		if _, err := os.Stat(candidate); err != nil {
			continue
		}
		if err := os.Rename(candidate, candidate+suffix); err != nil {
			return err
		}
	}
	return nil
}

// syncProductionRecallLedger idempotently merges any rotated .epoch-* sidecar
// ledgers and ingests completed MCP recall tool_usage records from the local
// ZCode host database into the production efficacy ledger. It is strictly no-op
// whenever ROLLING_MEMORY_DB is set or when memoryPath is a test database.
func syncProductionRecallLedger(home, memoryPath string) {
	if strings.TrimSpace(home) == "" || strings.TrimSpace(os.Getenv("ROLLING_MEMORY_DB")) != "" {
		return
	}
	defaultPath := filepath.Join(home, ".local", "share", "rolling-memory", "memory.db")
	if filepath.Clean(memoryPath) != filepath.Clean(defaultPath) {
		return
	}
	fp := computeAdminDBFingerprint(memoryPath, UserScope)
	if fp == "" {
		return
	}
	ledgerPath := memoryPath + ".efficacy.sqlite"
	// 账本与金库身份绑定：已绑定指纹与当前金库不符时，这本账属于另一个金库
	// （或变更前的旧世代），绝不改写指纹、把两边事件混在同一身份下——按既有轮转
	// 约定（.epoch-*）把旧账归档到旁边，从空账重新开世代；归档件在下一步按自身
	// 指纹核验，异库侧册不会被拉回。
	if storedFP := readAdminLedgerFingerprint(ledgerPath); storedFP != "" && storedFP != fp {
		if err := rotateAdminLedgerEpoch(ledgerPath); err != nil {
			return
		}
	}
	ldb, err := openPlainSQLite(ledgerPath)
	if err != nil {
		return
	}
	defer ldb.Close()

	_, _ = ldb.Exec(`
		CREATE TABLE IF NOT EXISTS efficacy_event (
			seq INTEGER PRIMARY KEY AUTOINCREMENT,
			event_id TEXT NOT NULL UNIQUE,
			execution_id TEXT NOT NULL,
			session TEXT NOT NULL,
			scope TEXT NOT NULL,
			harness TEXT NOT NULL,
			hook TEXT NOT NULL,
			output_outcome TEXT NOT NULL,
			action TEXT NOT NULL,
			target_event_id TEXT NOT NULL,
			observed_at INTEGER NOT NULL,
			observation TEXT NOT NULL,
			text_sha256 TEXT NOT NULL,
			text_bytes INTEGER
		);
		CREATE TABLE IF NOT EXISTS efficacy_meta (
			key TEXT PRIMARY KEY,
			value TEXT NOT NULL
		);
		PRAGMA user_version = 1;
	`)
	_, _ = ldb.Exec(`INSERT OR REPLACE INTO efficacy_meta(key, value) VALUES ('db_fingerprint', ?)`, fp)

	// 1. Merge any rotated .epoch-* sidecar ledgers from the same production database.
	//    侧册必须与当前金库指纹一致才可并入；异库（或旧世代）侧册保留在磁盘上，
	//    既不并入也不删除。合并失败（读表、插入或提交任一失败）一律不删侧册，
	//    留到下一轮重试，杜绝「未确认迁移成功就删归档」。
	if matches, gErr := filepath.Glob(ledgerPath + ".epoch-*"); gErr == nil {
		for _, epPath := range matches {
			if strings.HasSuffix(epPath, "-wal") || strings.HasSuffix(epPath, "-shm") {
				continue
			}
			if sideFP := readAdminLedgerFingerprint(epPath); sideFP != "" && sideFP != fp {
				continue
			}
			edb := openVaultReadOnly(epPath)
			if edb == nil {
				continue
			}
			merged := false
			rows, qErr := edb.Query(`
				SELECT event_id, execution_id, session, scope, harness, hook,
				       output_outcome, action, target_event_id, observed_at,
				       observation, text_sha256, text_bytes
				FROM efficacy_event
			`)
			if qErr == nil {
				tx, txErr := ldb.Begin()
				if txErr == nil {
					failed := false
					for rows.Next() && !failed {
						var evID, execID, sess, sc, harn, hk, outc, act, tgt, obs, sha string
						var obsAt int64
						var tb sql.NullInt64
						if rows.Scan(&evID, &execID, &sess, &sc, &harn, &hk, &outc, &act, &tgt, &obsAt, &obs, &sha, &tb) != nil {
							failed = true
							continue
						}
						var tbArg any
						if tb.Valid {
							tbArg = tb.Int64
						}
						if _, err := tx.Exec(`
							INSERT OR IGNORE INTO efficacy_event(
								event_id, execution_id, session, scope, harness, hook,
								output_outcome, action, target_event_id, observed_at,
								observation, text_sha256, text_bytes
							) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
						`, evID, execID, sess, sc, harn, hk, outc, act, tgt, obsAt, obs, sha, tbArg); err != nil {
							failed = true
						}
					}
					if rows.Err() != nil {
						failed = true
					}
					rows.Close()
					if failed {
						_ = tx.Rollback()
					} else if err := tx.Commit(); err == nil {
						merged = true
					}
				} else {
					rows.Close()
				}
			}
			edb.Close()
			if merged {
				_ = os.Remove(epPath)
				_ = os.Remove(epPath + "-wal")
				_ = os.Remove(epPath + "-shm")
			}
		}
	}

	// 2. Sync real MCP recall tool calls from all JSONL harnesses (Claude Code, Codex, WorkBuddy, CodeBuddy, DSH)
	//    as well as the ZCode host tool_usage table.
	syncMultiHarnessJSONLRecallLedger(home, fp, ldb)

	zdbPath := filepath.Join(home, ".zcode", "cli", "db", "db.sqlite")
	zdb, zOpenErr := memoryRODB(zdbPath)
	if zOpenErr != nil || zdb == nil {
		return
	}
	// 以 started_at 水位增量扫描（命中宿主 tool_usage_started_tool_idx 的前缀列）；
	// `tool_name IN (...)` 单独作谓词无索引可用，对 GB 级实时库会退化成全表扫。
	watermarkMS := readZcodeToolWatermark(ldb)
	if watermarkMS <= 0 {
		watermarkMS = time.Now().Add(-zcodeRecallLedgerLookback).UnixMilli()
	}
	zRows, zErr := zdb.Query(`
		SELECT tool_call_id, session_id, status, coalesce(started_at, 0), coalesce(completed_at, 0), coalesce(output_bytes, 0)
		FROM tool_usage
		WHERE started_at >= ?
		  AND tool_name IN ('mcp__plugin_rolling-memory_rolling-memory__recall', 'mcp__rolling-memory__recall', 'rolling-memory:rolling-memory__recall')
		ORDER BY started_at ASC
	`, watermarkMS)
	if zErr != nil {
		return
	}
	defer zRows.Close()
	tx, txErr := ldb.Begin()
	if txErr != nil {
		return
	}
	var maxStartedMS int64
	var minRunningStartMS int64
	for zRows.Next() {
		var callID, sessID, status string
		var startMS, endMS, outBytes int64
		if zRows.Scan(&callID, &sessID, &status, &startMS, &endMS, &outBytes) != nil {
			continue
		}
		callID = strings.TrimSpace(callID)
		if callID == "" {
			continue
		}
		if status == "running" {
			// 进行中的调用不能被当成「空结果」固化：跳过不入账，并把水位回退到最早
			// 进行中的调用起点，下一轮其状态落定后重新扫描、按真实 outcome 记一笔
			// （旧行为按 start 时间插入 empty，完成后的 INSERT OR IGNORE 无法修正）。
			if startMS > 0 && (minRunningStartMS == 0 || startMS < minRunningStartMS) {
				minRunningStartMS = startMS
			}
			continue
		}
		tsMS := endMS
		if tsMS <= 0 {
			tsMS = startMS
		}
		if tsMS <= 0 {
			continue
		}
		obsAt := tsMS * int64(time.Millisecond)
		outcome := zcodeRecallToolOutcome(status, outBytes)
		sum := sha256.Sum256([]byte("zcode\x00" + callID))
		shaHex := hex.EncodeToString(sum[:])
		sessSum := sha256.Sum256([]byte(fp + "\x00" + strings.TrimSpace(sessID)))
		sessDigest := "s_" + hex.EncodeToString(sessSum[:])[:24]
		_, _ = tx.Exec(`
			INSERT OR IGNORE INTO efficacy_event(
				event_id, execution_id, session, scope, harness, hook,
				output_outcome, action, target_event_id, observed_at,
				observation, text_sha256, text_bytes
			) VALUES (?, ?, ?, 'mem:self', 'zcode', 'mcp_recall:tool', ?, 'recall', '', ?, 'observed', ?, ?)
		`, "zcode_"+callID, shaHex[:32], sessDigest, outcome, obsAt, shaHex, outBytes)
		if startMS > maxStartedMS {
			maxStartedMS = startMS
		}
	}
	_ = tx.Commit()
	// 自愈校准：早期守护进程在未携带宿主环境变量时将 ZCode 触发的直写行（mcp_recall:<mode>）
	// 误标为 harness='cli'，此处与已落盘的 zcode 回填孪生行（±2s 同窗口）对齐还原为 'zcode'。
	_, _ = ldb.Exec(`
			UPDATE efficacy_event
			SET harness = 'zcode'
			WHERE harness = 'cli'
			  AND action = 'recall'
			  AND hook LIKE 'mcp_recall:%'
			  AND hook != 'mcp_recall:tool'
			  AND EXISTS (
				SELECT 1 FROM efficacy_event t
				WHERE t.scope = efficacy_event.scope
				  AND t.harness = 'zcode'
				  AND t.action = 'recall'
				  AND t.hook = 'mcp_recall:tool'
				  AND ABS(t.observed_at - efficacy_event.observed_at) <= 2000000000
			  )
		`)
	// 水位只前进（写已提交之后落盘）：下轮导入从本次最大 started_at 接着扫。
	// 尚有进行中的调用时，水位收回到最早那一笔的起点，保证其完成后会被重新扫到
	// （已入账行按 INSERT OR IGNORE 幂等重放）。
	maxStartedMS = zcodeToolWatermark(maxStartedMS, minRunningStartMS)
	if maxStartedMS > 0 {
		_, _ = ldb.Exec(`INSERT OR REPLACE INTO efficacy_meta(key, value) VALUES ('zcode_tool_watermark', ?)`, strconv.FormatInt(maxStartedMS, 10))
		notifyStreamKindRefresh(streamKindROI)
	}
}

// zcodeRecallToolOutcome 把一条 zcode tool_usage 记录映射成账本 outcome：
// error/cancelled → error，零输出 → empty，其余 → hits（进行中的记录在调用方
// 先行跳过，绝不入账）。
func zcodeRecallToolOutcome(status string, outBytes int64) string {
	if status == "error" || status == "cancelled" {
		return "error"
	}
	if outBytes <= 0 {
		return "empty"
	}
	return "hits"
}

// zcodeToolWatermark 决定本轮 zcode 导入的下一水位：存在进行中的调用时以最早一笔
// 起点为准（其完成后必须被重新扫到），否则取本轮已入账的最大 started_at。
func zcodeToolWatermark(maxStartedMS, minRunningStartMS int64) int64 {
	if minRunningStartMS > 0 {
		return minRunningStartMS
	}
	return maxStartedMS
}

// openVaultReadOnly opens the memory database read-only. It returns nil when the
// path is not a plain regular file, so callers never create or mutate a vault.
func openVaultReadOnly(memoryPath string) *sql.DB {
	if strings.TrimSpace(memoryPath) == "" || memoryPath == ":memory:" || strings.HasPrefix(memoryPath, "file:") || strings.ContainsAny(memoryPath, "?#") {
		return nil
	}
	absPath, err := filepath.Abs(memoryPath)
	if err != nil {
		return nil
	}
	info, err := os.Stat(absPath)
	if err != nil || !info.Mode().IsRegular() {
		return nil
	}
	uri := sqliteFileURI(absPath) + "?mode=ro&_pragma=busy_timeout%2815000%29&_pragma=cache_size%28-32768%29&_pragma=temp_store%28MEMORY%29"
	db, err := sql.Open("sqlite", uri)
	if err != nil {
		return nil
	}
	return db
}

// borrowVaultReadOnly reuses the in-process read-only connection pool (memoryRODB)
// when available, avoiding repeated sql.Open/Close overhead on every ROI pass.
func borrowVaultReadOnly(memoryPath string) (*sql.DB, func()) {
	if strings.TrimSpace(memoryPath) == "" || memoryPath == ":memory:" || strings.HasPrefix(memoryPath, "file:") || strings.ContainsAny(memoryPath, "?#") {
		return nil, func() {}
	}
	absPath, err := filepath.Abs(memoryPath)
	if err != nil {
		return nil, func() {}
	}
	info, err := os.Stat(absPath)
	if err != nil || !info.Mode().IsRegular() {
		return nil, func() {}
	}
	if pooled, pErr := memoryRODB(absPath); pErr == nil && pooled != nil {
		return pooled, func() {}
	}
	db := openVaultReadOnly(absPath)
	if db == nil {
		return nil, func() {}
	}
	return db, func() { _ = db.Close() }
}

// computeAdminDBFingerprint mirrors the ledger writer's identity formula: one
// database, one fingerprint, independent of which scope the console is viewing.
func computeAdminDBFingerprint(memoryPath, scope string) string {
	db, release := borrowVaultReadOnly(memoryPath)
	if db == nil {
		return ""
	}
	defer release()
	var createdAt, engine, firstDocID string
	_ = db.QueryRow(`SELECT coalesce(value, '') FROM db_meta WHERE key = 'created_at'`).Scan(&createdAt)
	_ = db.QueryRow(`SELECT coalesce(value, '') FROM db_meta WHERE key = 'engine'`).Scan(&engine)
	// rm_units 是视图（无 rowid）；与疗效账本 ComputeDBFingerprint 同源，按规范表
	// rm_docs 的最早写入行取锚，保证两侧指纹逐位一致。旧夹具只有 rm_units 表时
	// 退化为按 doc_id 定序，仍保持确定性（不把空锚误报成身份失效）。
	_ = db.QueryRow(`SELECT coalesce(unit_id, '') FROM rm_docs ORDER BY occurred_at ASC, rowid ASC LIMIT 1`).Scan(&firstDocID)
	if strings.TrimSpace(firstDocID) == "" {
		_ = db.QueryRow(`SELECT coalesce(doc_id, '') FROM rm_units ORDER BY occurred_at ASC, doc_id ASC LIMIT 1`).Scan(&firstDocID)
	}
	if strings.TrimSpace(firstDocID) == "" {
		_ = db.QueryRow(`SELECT coalesce(noetic_id, '') FROM rm_noetic ORDER BY occurred_at ASC, rowid ASC LIMIT 1`).Scan(&firstDocID)
	}
	if strings.TrimSpace(createdAt) == "" || strings.TrimSpace(firstDocID) == "" {
		return ""
	}
	raw := strings.Join([]string{
		strings.TrimSpace(createdAt),
		strings.TrimSpace(engine),
		strings.TrimSpace(firstDocID),
	}, "|")
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])[:24]
}

// vaultDroppedLegacyAnalytics reports a live local vault that no longer
// carries the retired trace/assertion tables the per-turn settlement engine
// reads. A database that cannot be opened is not judged here.
func vaultDroppedLegacyAnalytics(memoryPath string) bool {
	db, release := borrowVaultReadOnly(memoryPath)
	if db == nil {
		return false
	}
	defer release()
	var vaultTables int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE name = 'rm_units' AND type IN ('table', 'view')`).Scan(&vaultTables); err != nil || vaultTables == 0 {
		return false
	}
	var legacyTables int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name IN ('trace', 'assertion')`).Scan(&legacyTables); err != nil {
		return false
	}
	return legacyTables == 0
}

// vaultAnalyticsBundle holds all live vault telemetry extracted in a single read pass.
type vaultAnalyticsBundle struct {
	vault         *roiFactsVault
	vaultWindows  []roiFactWindow
	dailySeries   []roiDailyBucket
	topCitedUnits []roiTopCitedUnit
	locusMatrix   []roiLocusRow
	noeticStream  []roiNoeticEvent
}

type roiVaultBundleEntry struct {
	sig        string
	writeEpoch uint64
	at         time.Time
	refreshing bool
	bundle     vaultAnalyticsBundle
}

var (
	roiVaultBundleMu    sync.Mutex
	roiVaultBundleCache = map[string]*roiVaultBundleEntry{}
)

func roiVaultBundleSignature(memoryPath string, now time.Time) string {
	var b strings.Builder
	b.WriteString(strconv.FormatInt(now.Unix()/60, 10))
	b.WriteByte(':')
	b.WriteString(strconv.FormatUint(currentVaultWriteEpoch(), 10))
	for _, p := range []string{
		memoryPath,
		memoryPath + "-wal",
		memoryPath + ".efficacy.sqlite",
		memoryPath + ".efficacy.sqlite-wal",
	} {
		b.WriteByte('|')
		if st, err := os.Stat(p); err == nil {
			b.WriteString(strconv.FormatInt(st.Size(), 10))
			b.WriteByte(':')
			b.WriteString(strconv.FormatInt(st.ModTime().UnixNano(), 10))
		} else {
			b.WriteString("-")
		}
	}
	return b.String()
}

var roiVaultComputeMu sync.Mutex

// readROIFactsVaultBundle reads the live vault telemetry, L3 Noetic citation graph,
// per-workspace coverage matrix, daily ingestion/crystallization series, and top cited units.
func readROIFactsVaultBundle(memoryPath string) vaultAnalyticsBundle {
	return refreshROIEpisodicTexts(memoryPath, readROIFactsVaultBundleCached(memoryPath))
}

func readROIFactsVaultBundleCached(memoryPath string) vaultAnalyticsBundle {
	now := time.Now()
	writeEp := currentVaultWriteEpoch()
	sig := roiVaultBundleSignature(memoryPath, now)
	isLargeVault := false
	if st, err := os.Stat(memoryPath); err == nil && st.Size() >= 16*1024*1024 {
		isLargeVault = true
	}
	roiVaultBundleMu.Lock()
	if cached := roiVaultBundleCache[memoryPath]; cached != nil && cached.bundle.vault != nil {
		if cached.sig == sig || (isLargeVault && cached.writeEpoch == writeEp && now.Sub(cached.at) < 15*time.Second) {
			b := cached.bundle
			roiVaultBundleMu.Unlock()
			return b
		}
		if isLargeVault {
			b := cached.bundle
			if !cached.refreshing {
				cached.refreshing = true
				go func(p string) {
					fresh, fSig := computeROIFactsVaultBundle(p)
					curEp := currentVaultWriteEpoch()
					roiVaultBundleMu.Lock()
					if fresh.vault != nil {
						roiVaultBundleCache[p] = &roiVaultBundleEntry{
							sig:        fSig,
							writeEpoch: curEp,
							at:         time.Now(),
							bundle:     fresh,
						}
					} else if cur := roiVaultBundleCache[p]; cur != nil {
						cur.refreshing = false
					}
					roiVaultBundleMu.Unlock()
					if fresh.vault != nil {
						notifyStreamKindRefresh(streamKindROI)
					}
				}(memoryPath)
			}
			roiVaultBundleMu.Unlock()
			return b
		}
	}
	roiVaultBundleMu.Unlock()

	roiVaultComputeMu.Lock()
	defer roiVaultComputeMu.Unlock()
	roiVaultBundleMu.Lock()
	if cached := roiVaultBundleCache[memoryPath]; cached != nil && cached.bundle.vault != nil {
		if cached.sig == sig || (isLargeVault && cached.writeEpoch == writeEp && time.Since(cached.at) < 15*time.Second) {
			b := cached.bundle
			roiVaultBundleMu.Unlock()
			return b
		}
	}
	roiVaultBundleMu.Unlock()

	out, computedSig := computeROIFactsVaultBundle(memoryPath)
	if out.vault != nil {
		roiVaultBundleMu.Lock()
		roiVaultBundleCache[memoryPath] = &roiVaultBundleEntry{
			sig:        computedSig,
			writeEpoch: currentVaultWriteEpoch(),
			at:         time.Now(),
			bundle:     out,
		}
		roiVaultBundleMu.Unlock()
	}
	return out
}

func refreshROIEpisodicTexts(memoryPath string, bundle vaultAnalyticsBundle) vaultAnalyticsBundle {
	bundle.topCitedUnits = append([]roiTopCitedUnit(nil), bundle.topCitedUnits...)
	bundle.noeticStream = append([]roiNoeticEvent(nil), bundle.noeticStream...)
	var refs []hydrateRef
	for _, item := range bundle.topCitedUnits {
		if item.IsZeroCopyRef {
			refs = append(refs, hydrateRef{DocID: strings.TrimSpace(item.UnitID), Ref: strings.TrimSpace(item.storedRef)})
		}
	}
	for i := range bundle.noeticStream {
		bundle.noeticStream[i].CitedUnits = append([]roiCitedUnitDetail(nil), bundle.noeticStream[i].CitedUnits...)
		for _, item := range bundle.noeticStream[i].CitedUnits {
			if item.IsZeroCopyRef {
				refs = append(refs, hydrateRef{DocID: strings.TrimSpace(item.UnitID), Ref: strings.TrimSpace(item.StoredText)})
			}
		}
	}
	home, _ := os.UserHomeDir()
	texts, _ := hydrateRefsViaDaemon(home, memoryPath, refs)
	for i := range bundle.topCitedUnits {
		item := &bundle.topCitedUnits[i]
		if !item.IsZeroCopyRef {
			continue
		}
		text := strings.TrimSpace(texts[hydrateRef{DocID: strings.TrimSpace(item.UnitID), Ref: strings.TrimSpace(item.storedRef)}])
		item.FullText, item.IsHydrated = clipRunes(text, 8192), text != ""
		item.Tokens = int64(estimateTokenzRCount(text))
		if text != "" {
			item.Preview = clipRunes(text, 240)
		} else {
			item.Preview = clipRunes(item.fallbackPreview, 240)
		}
	}
	for i := range bundle.noeticStream {
		for j := range bundle.noeticStream[i].CitedUnits {
			item := &bundle.noeticStream[i].CitedUnits[j]
			if !item.IsZeroCopyRef {
				continue
			}
			text := strings.TrimSpace(texts[hydrateRef{DocID: strings.TrimSpace(item.UnitID), Ref: strings.TrimSpace(item.StoredText)}])
			item.FullText, item.IsHydrated = clipRunes(text, 8192), text != ""
		}
	}
	return bundle
}

// roiFactsClock 是金库事实账本的取时来源：单测把「现在」钉在本地正午，
// 使窗口与今日桶断言不随真实时刻（尤其跨日、跨时区）漂移。
var roiFactsClock = time.Now

func computeROIFactsVaultBundle(memoryPath string) (vaultAnalyticsBundle, string) {
	now := roiFactsClock()
	sig := roiVaultBundleSignature(memoryPath, now)
	out := vaultAnalyticsBundle{
		vaultWindows:  []roiFactWindow{},
		dailySeries:   []roiDailyBucket{},
		topCitedUnits: []roiTopCitedUnit{},
		locusMatrix:   []roiLocusRow{},
		noeticStream:  []roiNoeticEvent{},
	}
	home, _ := os.UserHomeDir()
	db, release := borrowVaultReadOnly(memoryPath)
	if db == nil {
		return out, sig
	}
	defer release()
	// The ledger scan is a plain base-table pass unless the ledger actually
	// carries revisions or non-tool MCP sibling rows; 10s lets the whole bundle
	// finish before the deadline instead of dropping its tail silently.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var hasVault int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE name = 'rm_units' AND type IN ('table', 'view')`).Scan(&hasVault); err != nil || hasVault == 0 {
		return out, sig
	}

	nowUnix := now.Unix()
	todayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location()).Unix()

	floor1h := nowUnix - 3600
	floor24h := nowUnix - 86400
	floor7d := nowUnix - 7*86400
	floor30d := nowUnix - 30*86400

	// 「今日/窗口新增」与「记忆吞吐趋势」同口径：按首次入库时间（inserted_at，
	// 未迁移的只读旧夹具缺列时平滑回退内容时间 occurred_at）聚合，避免编辑
	// 旧文件把存量分块刷进近期新增的账实不一致。
	docTsExpr := vaultIngestTimeExpr(memoryPath, "rm_units")
	bgTsExpr := vaultIngestTimeExpr(memoryPath, "rm_bg_chunks")
	ntTsExpr := vaultIngestTimeExpr(memoryPath, "rm_noetic")

	// 层归属与控制台统一口径（三层浏览 / facets / 趋势图）一致：docs 表中
	// layer=='background' 的行属 L1 背景层（计入背景计数），其余属 L2 情景层；
	// 旧夹具缺 layer 列时退回不分层（全部按 L2 计）。
	bgDocCond := "0"
	var layerCols int
	if lErr := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM pragma_table_info('rm_units') WHERE name = 'layer'`).Scan(&layerCols); lErr == nil && layerCols > 0 {
		bgDocCond = "lower(trim(COALESCE(layer, ''))) = 'background'"
	}

	vault := &roiFactsVault{SchemaEra: "canonical"}
	var ep1h, ep24h, ep7d, ep30d, epAll int64
	_ = db.QueryRowContext(ctx, fmt.Sprintf(`
			SELECT COUNT(*),
			       COALESCE(SUM(CASE WHEN raw_text LIKE '@ref:%%' THEN 1 ELSE 0 END), 0),
			       COALESCE(SUM(CASE WHEN %s >= ? THEN 1 ELSE 0 END), 0),
			       COALESCE(SUM(CASE WHEN %s >= ? THEN 1 ELSE 0 END), 0),
			       COALESCE(SUM(CASE WHEN %s >= ? THEN 1 ELSE 0 END), 0),
			       COALESCE(SUM(CASE WHEN %s >= ? THEN 1 ELSE 0 END), 0),
			       COALESCE(SUM(CASE WHEN %s >= ? THEN 1 ELSE 0 END), 0),
			       COALESCE(SUM(CASE WHEN %s >= 0 THEN 1 ELSE 0 END), 0)
			FROM rm_units WHERE NOT (%s)`, docTsExpr, docTsExpr, docTsExpr, docTsExpr, docTsExpr, docTsExpr, bgDocCond),
		todayStart, floor1h, floor24h, floor7d, floor30d,
	).Scan(&vault.EpisodicDocs, &vault.EpisodicPointers, &vault.TodayEpisodic, &ep1h, &ep24h, &ep7d, &ep30d, &epAll)
	// 背景层文档（layer=='background'）的窗口计数单取，随后并入 L1 背景计数。
	var bgDocToday, bgDoc1h, bgDoc24h, bgDoc7d, bgDoc30d, bgDocAll int64
	_ = db.QueryRowContext(ctx, fmt.Sprintf(`
			SELECT COALESCE(SUM(CASE WHEN %s >= ? THEN 1 ELSE 0 END), 0),
			       COALESCE(SUM(CASE WHEN %s >= ? THEN 1 ELSE 0 END), 0),
			       COALESCE(SUM(CASE WHEN %s >= ? THEN 1 ELSE 0 END), 0),
			       COALESCE(SUM(CASE WHEN %s >= ? THEN 1 ELSE 0 END), 0),
			       COALESCE(SUM(CASE WHEN %s >= ? THEN 1 ELSE 0 END), 0),
			       COALESCE(SUM(CASE WHEN %s >= 0 THEN 1 ELSE 0 END), 0)
			FROM rm_units WHERE %s`, docTsExpr, docTsExpr, docTsExpr, docTsExpr, docTsExpr, docTsExpr, bgDocCond),
		todayStart, floor1h, floor24h, floor7d, floor30d,
	).Scan(&bgDocToday, &bgDoc1h, &bgDoc24h, &bgDoc7d, &bgDoc30d, &bgDocAll)
	_ = db.QueryRowContext(ctx, fmt.Sprintf(`SELECT COUNT(DISTINCT CASE WHEN instr(address_uri, '#') > 0 THEN substr(address_uri, 1, instr(address_uri, '#') - 1) ELSE address_uri END) FROM rm_units WHERE address_uri != '' AND NOT (%s)`, bgDocCond)).Scan(&vault.EpisodicCells)
	vault.EpisodicText = vault.EpisodicDocs - vault.EpisodicPointers
	if vault.EpisodicDocs > 0 {
		vault.EpisodicPointerRatioPct = roundFloat(float64(vault.EpisodicPointers)*100.0/float64(vault.EpisodicDocs), 1)
	}
	_ = db.QueryRowContext(ctx, `SELECT COUNT(*) FROM rm_bg_dirs`).Scan(&vault.BackgroundDirs)
	_ = db.QueryRowContext(ctx, `SELECT COUNT(*), COALESCE(SUM(file_size), 0) FROM rm_bg_files`).Scan(&vault.BackgroundFiles, &vault.BackgroundBytes)

	var bg1h, bg24h, bg7d, bg30d, bgAll int64
	_ = db.QueryRowContext(ctx, fmt.Sprintf(`
			SELECT COUNT(*),
			       COALESCE(SUM(CASE WHEN %s >= ? THEN 1 ELSE 0 END), 0),
			       COALESCE(SUM(CASE WHEN %s >= ? THEN 1 ELSE 0 END), 0),
			       COALESCE(SUM(CASE WHEN %s >= ? THEN 1 ELSE 0 END), 0),
			       COALESCE(SUM(CASE WHEN %s >= ? THEN 1 ELSE 0 END), 0),
			       COALESCE(SUM(CASE WHEN %s >= ? THEN 1 ELSE 0 END), 0),
			       COALESCE(SUM(CASE WHEN %s >= 0 THEN 1 ELSE 0 END), 0)
			FROM rm_bg_chunks`, bgTsExpr, bgTsExpr, bgTsExpr, bgTsExpr, bgTsExpr, bgTsExpr),
		todayStart, floor1h, floor24h, floor7d, floor30d,
	).Scan(&vault.BackgroundChunks, &vault.TodayBackground, &bg1h, &bg24h, &bg7d, &bg30d, &bgAll)
	// 层归属统一：L1 = 背景分块 + docs 表背景层文档；L2 已按层过滤（上方便只剩情景文档）。
	vault.BackgroundChunks += bgDocAll
	vault.TodayBackground += bgDocToday
	bg1h += bgDoc1h
	bg24h += bgDoc24h
	bg7d += bgDoc7d
	bg30d += bgDoc30d
	bgAll += bgDocAll

	_ = db.QueryRowContext(ctx, `
			SELECT COALESCE(SUM(CASE WHEN status = 'active' THEN 1 ELSE 0 END), 0),
			       COALESCE(SUM(CASE WHEN status = 'stale' THEN 1 ELSE 0 END), 0),
			       COALESCE(SUM(CASE WHEN status = 'active' AND cited_unit_ids_json NOT IN ('', '[]') THEN 1 ELSE 0 END), 0)
			FROM rm_noetic`,
	).Scan(&vault.NoeticUnits, &vault.NoeticStale, &vault.NoeticWithCitations)
	_ = db.QueryRowContext(ctx, fmt.Sprintf(`SELECT COUNT(*) FROM rm_noetic WHERE status = 'active' AND %s >= ?`, ntTsExpr), todayStart).Scan(&vault.TodayNoetic)
	_ = db.QueryRowContext(ctx, `SELECT COUNT(*) FROM injection_ledger`).Scan(&vault.InjectionLedgerRows)

	vault.TotalUnits = vault.BackgroundChunks + vault.EpisodicDocs + vault.NoeticUnits

	// Prepare rolling windows (1h, 24h, 7d, 30d, plus "all" for full-history view).
	type winSpec struct {
		Key     string
		Seconds int64
		Floor   int64
		EpAdded int64
		BgAdded int64
	}
	specs := []winSpec{
		{Key: "1h", Seconds: 3600, Floor: floor1h, EpAdded: ep1h, BgAdded: bg1h},
		{Key: "24h", Seconds: 86400, Floor: floor24h, EpAdded: ep24h, BgAdded: bg24h},
		{Key: "7d", Seconds: 7 * 86400, Floor: floor7d, EpAdded: ep7d, BgAdded: bg7d},
		{Key: "30d", Seconds: 30 * 86400, Floor: floor30d, EpAdded: ep30d, BgAdded: bg30d},
		{Key: "all", Seconds: 0, Floor: 0, EpAdded: epAll, BgAdded: bgAll},
	}
	winMap := make(map[string]*roiFactWindow, len(specs))
	winCitedSets := make(map[string]map[string]bool, len(specs))
	for _, sp := range specs {
		winMap[sp.Key] = &roiFactWindow{
			Key:             sp.Key,
			Seconds:         sp.Seconds,
			SinceUnix:       sp.Floor,
			EpisodicAdded:   sp.EpAdded,
			BackgroundAdded: sp.BgAdded,
			Actions:         map[string]int64{},
			RecallOutcomes:  map[string]int64{},
		}
		winCitedSets[sp.Key] = make(map[string]bool)
	}

	// Scan all active & historical L3 Noetic rows to compute citation graph, reuse multipliers,
	// delivered tokenz-r tokens, window aggregations, daily buckets, and the Noetic stream.
	type citeStat struct {
		count        int64
		adoptedCount int64
		lastAt       int64
		queries      []string
		seenQuery    map[string]bool
		firstSnippet string
		citations    []roiUnitCitation
	}
	citedStats := make(map[string]*citeStat)
	dailyMap := make(map[string]*roiDailyBucket)
	for d := 13; d >= 0; d-- {
		dayStr := now.AddDate(0, 0, -d).Format("2006-01-02")
		dailyMap[dayStr] = &roiDailyBucket{Date: dayStr}
	}

	// Read actual delivered byte measurements from the sidecar efficacy ledger
	// (.efficacy.sqlite) so token delivery metrics reflect full <rolling-memory-recall>
	// payloads rather than truncated L3 citation snippets.
	recallBytesBySec := make(map[int64]int64)
	ledgerWinTokens := make(map[string]int64, len(specs))
	ledgerDayTokens := make(map[string]int64, 14)
	var ledgerTotalTokens, mcpRecallHitTokens, mcpRecallHitCalls, ledgerHitCalls int64
	if ldb := openVaultReadOnly(memoryPath + ".efficacy.sqlite"); ldb != nil {
		var lVer int
		if ldb.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&lVer) == nil && lVer == 1 {
			scanSQL := `
					SELECT e.action, e.output_outcome, coalesce(e.text_bytes, 0), e.observed_at
					FROM efficacy_event e
					WHERE e.action IN ('recall', 'output_attempt') AND e.observation = 'observed' AND e.text_bytes > 0`
			// 仅当账本确有直写孪生行时才需要反连接；否则整段相关子查询可证明为空操作。
			if _, siblings, shapeOK := roiLedgerShape(ctx, ldb); !shapeOK || siblings {
				scanSQL += `
					  AND (e.hook != 'mcp_recall:tool' OR NOT EXISTS (
						SELECT 1 FROM efficacy_event d
						WHERE d.scope = e.scope
						  AND d.action = 'recall'
						  AND d.hook LIKE 'mcp_recall:%'
						  AND d.hook != 'mcp_recall:tool'
						  AND d.observed_at BETWEEN e.observed_at - 2000000000 AND e.observed_at + 2000000000
					  ))`
			}
			lRows, lErr := ldb.QueryContext(ctx, scanSQL)
			if lErr == nil {
				for lRows.Next() {
					var act, outc string
					var tBytes, obsAt int64
					if lRows.Scan(&act, &outc, &tBytes, &obsAt) != nil || tBytes <= 0 {
						continue
					}
					sec := obsAt / int64(time.Second)
					if sec <= 0 {
						sec = obsAt
					}
					if tBytes > recallBytesBySec[sec] {
						recallBytesBySec[sec] = tBytes
					}
					tok := bytesToTokenzR(tBytes)
					ledgerTotalTokens += tok
					ledgerHitCalls++
					if act == "recall" && outc == "hits" {
						mcpRecallHitTokens += tok
						mcpRecallHitCalls++
					}
					for _, sp := range specs {
						if sec >= sp.Floor {
							ledgerWinTokens[sp.Key] += tok
						}
					}
					dayStr := time.Unix(sec, 0).In(now.Location()).Format("2006-01-02")
					if _, ok := dailyMap[dayStr]; ok {
						ledgerDayTokens[dayStr] += tok
					}
				}
				lRows.Close()
			}
		}
		ldb.Close()
	}

	noeticRows, err := db.QueryContext(ctx, fmt.Sprintf(`
		SELECT noetic_id, occurred_at, %s AS ingest_at, status, query_text, answer_text,
		       cited_unit_ids_json, cited_sentences_json,
		       coalesce(address_uri, ''), coalesce(waveform_bar, ''),
		       coalesce(assistant_id, ''), coalesce(incarnation_id, '')
		FROM rm_noetic
		ORDER BY occurred_at DESC, rowid DESC
	`, ntTsExpr))
	var allNoetics []roiNoeticEvent
	var matchedFlags []bool
	var matchedTokSum, matchedCiteSum int64
	if err == nil {
		for noeticRows.Next() {
			var ev roiNoeticEvent
			var citedIDsRaw, citedSentsRaw string
			if scanErr := noeticRows.Scan(
				&ev.NoeticID, &ev.OccurredAt, &ev.IngestAt, &ev.Status, &ev.QueryText, &ev.AnswerText,
				&citedIDsRaw, &citedSentsRaw,
				&ev.AddressURI, &ev.WaveformBar, &ev.AssistantID, &ev.IncarnationID,
			); scanErr != nil {
				continue
			}
			_ = json.Unmarshal([]byte(citedIDsRaw), &ev.CitedUnitIDs)
			_ = json.Unmarshal([]byte(citedSentsRaw), &ev.CitedSentences)
			if ev.CitedUnitIDs == nil {
				ev.CitedUnitIDs = []string{}
			}
			if ev.CitedSentences == nil {
				ev.CitedSentences = []string{}
			}
			ev.CitationCount = len(ev.CitedUnitIDs)
			deliveredText := ev.AnswerText
			if len(ev.CitedSentences) > 0 {
				deliveredText = strings.Join(ev.CitedSentences, "\n")
			}
			ev.DeliveredTokens = int64(estimateTokenzRCount(ev.QueryText + "\n" + deliveredText))
			hasFullMatch := false
			if ev.OccurredAt > 0 && len(recallBytesBySec) > 0 {
				var matchedBytes int64
				for delta := int64(0); delta <= 8; delta++ {
					if b := recallBytesBySec[ev.OccurredAt-delta]; b > matchedBytes {
						matchedBytes = b
					}
					if b := recallBytesBySec[ev.OccurredAt+delta]; b > matchedBytes {
						matchedBytes = b
					}
					if matchedBytes > 256 {
						break
					}
				}
				if matchedBytes > 256 {
					if actualTok := bytesToTokenzR(matchedBytes); actualTok > ev.DeliveredTokens {
						ev.DeliveredTokens = actualTok
						hasFullMatch = true
						if ev.CitationCount > 0 {
							matchedTokSum += actualTok
							matchedCiteSum += int64(ev.CitationCount)
						}
					}
				}
			}
			allNoetics = append(allNoetics, ev)
			matchedFlags = append(matchedFlags, hasFullMatch)
		}
		noeticRows.Close()
	}

	var endogenousTokPerCite int64
	if matchedCiteSum > 0 {
		endogenousTokPerCite = (matchedTokSum + matchedCiteSum/2) / matchedCiteSum
	}

	for i := range allNoetics {
		ev := &allNoetics[i]
		if !matchedFlags[i] && endogenousTokPerCite > 0 && ev.CitationCount > 0 {
			if derived := endogenousTokPerCite * int64(ev.CitationCount); derived > ev.DeliveredTokens {
				ev.DeliveredTokens = derived
			}
		}
		if len(out.noeticStream) < 50 {
			out.noeticStream = append(out.noeticStream, *ev)
		}
		if ev.Status != "active" {
			continue
		}
		vault.TotalDeliveredTokens += ev.DeliveredTokens
		vault.TotalCitationLinks += int64(ev.CitationCount)
		if ev.IngestAt >= todayStart {
			vault.TodayCitations += int64(ev.CitationCount)
		}
		if ev.IngestAt > 0 {
			dayStr := time.Unix(ev.IngestAt, 0).In(now.Location()).Format("2006-01-02")
			if b, ok := dailyMap[dayStr]; ok {
				b.NoeticCount++
				b.CitationLinks += int64(ev.CitationCount)
				b.DeliveredTokens += ev.DeliveredTokens
			}
		}

		for _, sp := range specs {
			if ev.IngestAt >= sp.Floor {
				w := winMap[sp.Key]
				w.NoeticCrystallized++
				w.CitationLinks += int64(ev.CitationCount)
				w.DeliveredTokens += ev.DeliveredTokens
				for _, uid := range ev.CitedUnitIDs {
					u := strings.TrimSpace(uid)
					if u != "" {
						winCitedSets[sp.Key][u] = true
					}
				}
			}
		}

		for idx, uid := range ev.CitedUnitIDs {
			u := strings.TrimSpace(uid)
			if u == "" {
				continue
			}
			st, exists := citedStats[u]
			if !exists {
				st = &citeStat{seenQuery: make(map[string]bool)}
				citedStats[u] = st
			}
			st.count++
			if ev.OccurredAt > st.lastAt {
				st.lastAt = ev.OccurredAt
			}
			if st.firstSnippet == "" {
				if idx < len(ev.CitedSentences) && strings.TrimSpace(ev.CitedSentences[idx]) != "" {
					st.firstSnippet = strings.TrimSpace(ev.CitedSentences[idx])
				} else if len(ev.CitedSentences) > 0 && strings.TrimSpace(ev.CitedSentences[0]) != "" {
					st.firstSnippet = strings.TrimSpace(ev.CitedSentences[0])
				} else if strings.TrimSpace(ev.AnswerText) != "" {
					st.firstSnippet = strings.TrimSpace(ev.AnswerText)
				}
			}
			qTrim := strings.TrimSpace(ev.QueryText)
			if qTrim != "" && !st.seenQuery[qTrim] && len(st.queries) < 5 {
				st.seenQuery[qTrim] = true
				st.queries = append(st.queries, qTrim)
			}
			if len(st.citations) < 16 {
				st.citations = append(st.citations, roiUnitCitation{
					NoeticID:   ev.NoeticID,
					QueryText:  clipRunes(ev.QueryText, 240),
					AnswerText: clipRunes(ev.AnswerText, 360),
					OccurredAt: ev.OccurredAt,
				})
			}
		}
		// Re-consolidated L3 handles (occurred_at > ingest_at) were recalled again across
		// subsequent turns; credit the L3 handle itself as a reused memory unit.
		if ev.IngestAt > 0 && ev.OccurredAt > ev.IngestAt && len(ev.CitedUnitIDs) > 0 {
			u := strings.TrimSpace(ev.NoeticID)
			if u != "" {
				st, exists := citedStats[u]
				if !exists {
					st = &citeStat{seenQuery: make(map[string]bool), count: 1}
					citedStats[u] = st
				}
				st.count++
				if ev.OccurredAt > st.lastAt {
					st.lastAt = ev.OccurredAt
				}
				if st.firstSnippet == "" {
					st.firstSnippet = strings.TrimSpace("Q: " + ev.QueryText + "\nA: " + ev.AnswerText)
				}
				qTrim := strings.TrimSpace(ev.QueryText)
				if qTrim != "" && !st.seenQuery[qTrim] && len(st.queries) < 5 {
					st.seenQuery[qTrim] = true
					st.queries = append(st.queries, qTrim)
				}
				if len(st.citations) < 16 {
					st.citations = append(st.citations, roiUnitCitation{
						NoeticID:   ev.NoeticID,
						QueryText:  clipRunes(ev.QueryText, 240),
						AnswerText: clipRunes(ev.AnswerText, 360),
						OccurredAt: ev.OccurredAt,
					})
				}
			}
		}
	}

	vault.UniqueCitedUnits = int64(len(citedStats))
	for _, st := range citedStats {
		if st.count >= 2 {
			vault.ReusedUnitsCount++
		}
		if st.count > vault.MaxReuseCount {
			vault.MaxReuseCount = st.count
		}
	}
	if vault.UniqueCitedUnits > 0 {
		vault.AvgReuseMultiplier = roundFloat(float64(vault.TotalCitationLinks)/float64(vault.UniqueCitedUnits), 2)
	}
	if vault.NoeticWithCitations > 0 {
		vault.AvgCitationsPerRecall = roundFloat(float64(vault.TotalCitationLinks)/float64(vault.NoeticWithCitations), 2)
	}
	for _, sp := range specs {
		if ledgerWinTokens[sp.Key] > winMap[sp.Key].DeliveredTokens {
			winMap[sp.Key].DeliveredTokens = ledgerWinTokens[sp.Key]
		}
	}
	for d, b := range dailyMap {
		if ledgerDayTokens[d] > b.DeliveredTokens {
			b.DeliveredTokens = ledgerDayTokens[d]
		}
	}
	if ledgerTotalTokens > vault.TotalDeliveredTokens {
		vault.TotalDeliveredTokens = ledgerTotalTokens
	}
	if mcpRecallHitCalls > 0 {
		vault.AvgDeliveredTokens = (mcpRecallHitTokens + mcpRecallHitCalls/2) / mcpRecallHitCalls
	} else if ledgerHitCalls > 0 {
		vault.AvgDeliveredTokens = (ledgerTotalTokens + ledgerHitCalls/2) / ledgerHitCalls
	} else if vault.NoeticUnits > 0 {
		vault.AvgDeliveredTokens = (vault.TotalDeliveredTokens + vault.NoeticUnits/2) / vault.NoeticUnits
	}

	// Measure inline distillation compression ratio (raw sentences vs the memory engine kept_indices).
	distRows, err := db.QueryContext(ctx, `
		SELECT raw_text, sentences_json, kept_indices_json
		FROM rm_units
		WHERE raw_text NOT LIKE '@ref:%' AND raw_text != ''
		LIMIT 500
	`)
	if err == nil {
		for distRows.Next() {
			var rawTxt, sentsJSON, keptJSON string
			if distRows.Scan(&rawTxt, &sentsJSON, &keptJSON) != nil {
				continue
			}
			rawTok := int64(estimateTokenzRCount(rawTxt))
			vault.InlineRawTokens += rawTok
			var sents []string
			var kept []int
			_ = json.Unmarshal([]byte(sentsJSON), &sents)
			_ = json.Unmarshal([]byte(keptJSON), &kept)
			if len(sents) > 0 && len(kept) > 0 && len(kept) < len(sents) {
				var keptParts []string
				for _, idx := range kept {
					if idx >= 0 && idx < len(sents) {
						keptParts = append(keptParts, sents[idx])
					}
				}
				if len(keptParts) > 0 {
					distTok := int64(estimateTokenzRCount(strings.Join(keptParts, "\n")))
					if distTok < rawTok {
						vault.InlineDistilledTokens += distTok
						continue
					}
				}
			}
			vault.InlineDistilledTokens += rawTok
		}
		distRows.Close()
	}
	if vault.InlineRawTokens > 0 && vault.InlineDistilledTokens < vault.InlineRawTokens {
		vault.DistillationSavedPct = roundFloat(float64(vault.InlineRawTokens-vault.InlineDistilledTokens)*100.0/float64(vault.InlineRawTokens), 1)
	} else if vault.InlineRawTokens > 0 && vault.TotalDeliveredTokens > 0 && vault.TotalDeliveredTokens < vault.InlineRawTokens {
		vault.InlineDistilledTokens = vault.TotalDeliveredTokens
		vault.DistillationSavedPct = roundFloat(float64(vault.InlineRawTokens-vault.TotalDeliveredTokens)*100.0/float64(vault.InlineRawTokens), 1)
	}

	// Fill daily episodic & background counts for the last 14 days.
	floor14d := now.AddDate(0, 0, -14).Unix()
	epDayRows, err := db.QueryContext(ctx, fmt.Sprintf(`
		SELECT date(%s, 'unixepoch', 'localtime') AS d,
		       COALESCE(SUM(CASE WHEN %s THEN 1 ELSE 0 END), 0) AS bg_doc_cnt,
		       COALESCE(SUM(CASE WHEN NOT (%s) THEN 1 ELSE 0 END), 0) AS ep_cnt
		FROM rm_units
		WHERE %s >= ?
		GROUP BY d
	`, docTsExpr, bgDocCond, bgDocCond, docTsExpr), floor14d)
	if err == nil {
		for epDayRows.Next() {
			var d string
			var bgDocCnt, epCnt int64
			if epDayRows.Scan(&d, &bgDocCnt, &epCnt) == nil {
				if b, ok := dailyMap[d]; ok {
					b.EpisodicCount += epCnt
					b.BackgroundCount += bgDocCnt
				}
			}
		}
		epDayRows.Close()
	}
	bgDayRows, err := db.QueryContext(ctx, fmt.Sprintf(`
		SELECT date(%s, 'unixepoch', 'localtime') AS d, COUNT(*)
		FROM rm_bg_chunks
		WHERE %s >= ?
		GROUP BY d
	`, bgTsExpr, bgTsExpr), floor14d)
	if err == nil {
		for bgDayRows.Next() {
			var d string
			var cnt int64
			if bgDayRows.Scan(&d, &cnt) == nil {
				if b, ok := dailyMap[d]; ok {
					b.BackgroundCount += cnt
				}
			}
		}
		bgDayRows.Close()
	}
	for d := 13; d >= 0; d-- {
		dayStr := now.AddDate(0, 0, -d).Format("2006-01-02")
		if b, ok := dailyMap[dayStr]; ok {
			out.dailySeries = append(out.dailySeries, *b)
		}
	}

	// Build Top Cited Units (Top 25) and resolve unit metadata + locus attribution.
	type unitMeta struct {
		layer       string
		locus       string
		addressURI  string
		waveformBar string
		occurredAt  int64
		rawText     string
		isBGChunk   bool
	}
	unitMetaMap := make(map[string]unitMeta, len(citedStats))
	allCitedUIDs := make([]string, 0, len(citedStats))
	for uid := range citedStats {
		allCitedUIDs = append(allCitedUIDs, uid)
	}
	const metaBatchSize = 256
	for start := 0; start < len(allCitedUIDs); start += metaBatchSize {
		end := start + metaBatchSize
		if end > len(allCitedUIDs) {
			end = len(allCitedUIDs)
		}
		chunk := allCitedUIDs[start:end]
		ph := strings.TrimSuffix(strings.Repeat("?,", len(chunk)), ",")
		args := make([]any, len(chunk))
		for i, uid := range chunk {
			args[i] = uid
		}
		if dRows, dErr := db.QueryContext(ctx, `
				SELECT doc_id, coalesce(layer, 'episodic'), coalesce(locus, ''), coalesce(address_uri, ''),
				       coalesce(waveform_bar, ''), occurred_at, raw_text
				FROM rm_units WHERE doc_id IN (`+ph+`)
			`, args...); dErr == nil {
			for dRows.Next() {
				var id string
				var m unitMeta
				if dRows.Scan(&id, &m.layer, &m.locus, &m.addressURI, &m.waveformBar, &m.occurredAt, &m.rawText) == nil {
					unitMetaMap[id] = m
				}
			}
			dRows.Close()
		}
		var missingBG []any
		for _, uid := range chunk {
			if _, ok := unitMetaMap[uid]; !ok {
				missingBG = append(missingBG, uid)
			}
		}
		if len(missingBG) > 0 {
			bgPH := strings.TrimSuffix(strings.Repeat("?,", len(missingBG)), ",")
			if bRows, bErr := db.QueryContext(ctx, `
					SELECT unit_id, coalesce(abs_path, ''), coalesce(rel_path, ''), coalesce(anchor, ''), coalesce(address_uri, ''),
					       coalesce(waveform_bar, ''), occurred_at
					FROM rm_bg_chunks WHERE unit_id IN (`+bgPH+`)
				`, missingBG...); bErr == nil {
				for bRows.Next() {
					var id, absPath, relPath, anchor string
					var m unitMeta
					if bRows.Scan(&id, &absPath, &relPath, &anchor, &m.addressURI, &m.waveformBar, &m.occurredAt) == nil {
						m.layer = "background"
						m.isBGChunk = true
						if absPath != "" {
							m.locus = filepath.Base(filepath.Dir(absPath))
							m.rawText = fmt.Sprintf("[%s#%s] (%s)", relPath, anchor, absPath)
						} else {
							m.rawText = relPath + " " + anchor
						}
						unitMetaMap[id] = m
					}
				}
				bRows.Close()
			}
		}
	}
	for i := range allNoetics {
		ev := &allNoetics[i]
		uid := strings.TrimSpace(ev.NoeticID)
		if uid == "" {
			continue
		}
		if _, exists := unitMetaMap[uid]; exists {
			continue
		}
		locus := ""
		for _, cid := range ev.CitedUnitIDs {
			if cm, ok := unitMetaMap[strings.TrimSpace(cid)]; ok && cm.locus != "" {
				locus = cm.locus
				break
			}
		}
		unitMetaMap[uid] = unitMeta{
			layer:       "noetic",
			locus:       locus,
			addressURI:  ev.AddressURI,
			waveformBar: ev.WaveformBar,
			occurredAt:  ev.OccurredAt,
			rawText:     strings.TrimSpace("Q: " + ev.QueryText + "\nA: " + ev.AnswerText),
		}
	}

	// 零拷贝指针与 L1 分块先走引擎守护进程的规范批量水合，再进下方缓存读；
	// 否则冷缓存会让证据抽屉退化成片段预览或仅显示路径坐标。
	var refRows []map[string]any
	var bgDocRows []map[string]any
	for uid, m := range unitMetaMap {
		if raw := strings.TrimSpace(m.rawText); strings.HasPrefix(raw, "@ref:") {
			refRows = append(refRows, map[string]any{"id": uid, "content": raw})
		} else if m.isBGChunk {
			bgDocRows = append(bgDocRows, map[string]any{"src": "chunk", "id": uid})
		}
	}
	refTexts := make(map[hydrateRef]string, len(refRows))
	if len(refRows) > 0 {
		warmHydrateZCodePointersBatch(home, memoryPath, refRows)
		for _, row := range refRows {
			if text, ok := hydratedPointerRowText(row); ok {
				refTexts[hydrateRef{DocID: strings.TrimSpace(asString(row["id"])), Ref: strings.TrimSpace(asString(row["content"]))}] = text
			}
		}
	}
	if len(bgDocRows) > 0 {
		warmHydrateLocalDocumentsBatch(home, bgDocRows)
	}

	// Expand every cited unit of the L3 stream with its full stored/hydrated text
	// so the evidence drawer can render complete content for each citation.
	hydrationCache := make(map[string]string, len(unitMetaMap))
	resolveFullText := func(uid string) (string, bool, bool) {
		m, ok := unitMetaMap[uid]
		if !ok {
			return "", false, false
		}
		stored := strings.TrimSpace(m.rawText)
		if cached, seen := hydrationCache[uid]; seen {
			return cached, cached != "" && cached != stored, strings.HasPrefix(stored, "@ref:")
		}
		if m.isBGChunk {
			full := stored
			hydrated := false
			if body, ok := hydratedLocalDocumentLive(home, uid); ok && strings.TrimSpace(body) != "" {
				full = body
				hydrated = true
			}
			hydrationCache[uid] = full
			return full, hydrated, false
		}
		isRef := strings.HasPrefix(stored, "@ref:")
		full := stored
		if isRef {
			// 指针行只回水合正文：无法水合时留空（前端回退片段预览 + 「宿主原文已清理」提示），
			// 绝不把原始 `@ref:` 指针当正文下发。
			full = refTexts[hydrateRef{DocID: uid, Ref: stored}]
		}
		hydrationCache[uid] = full
		return full, full != "" && full != stored, isRef
	}
	for i := range allNoetics {
		ev := &allNoetics[i]
		if len(ev.CitedUnitIDs) == 0 {
			ev.CitedUnits = []roiCitedUnitDetail{}
			continue
		}
		details := make([]roiCitedUnitDetail, 0, len(ev.CitedUnitIDs))
		for _, rawUID := range ev.CitedUnitIDs {
			uid := strings.TrimSpace(rawUID)
			if uid == "" {
				continue
			}
			m := unitMetaMap[uid]
			layer := m.layer
			if layer == "" {
				if strings.HasPrefix(uid, "bg:") {
					layer = "background"
				} else if strings.HasPrefix(uid, "nt_") || strings.HasPrefix(uid, "noetic_") {
					layer = "noetic"
				} else {
					layer = "episodic"
				}
			}
			full, hydrated, isRef := resolveFullText(uid)
			details = append(details, roiCitedUnitDetail{
				UnitID:        uid,
				Layer:         layer,
				Locus:         m.locus,
				AddressURI:    m.addressURI,
				StoredText:    strings.TrimSpace(m.rawText),
				FullText:      clipRunes(full, 8192),
				IsHydrated:    hydrated,
				IsZeroCopyRef: isRef,
			})
		}
		ev.CitedUnits = details
	}
	adoptSummary := auditNoeticStreamAdoption(ctx, db, home, memoryPath, allNoetics)
	vault.AuditedRecalls = adoptSummary.AuditedRecalls
	vault.AdoptedRecalls = adoptSummary.AdoptedRecalls
	vault.AdoptedToolCount = adoptSummary.AdoptedToolCount
	vault.AdoptedContextCount = adoptSummary.AdoptedContextCount
	vault.EchoExcludedTotal = adoptSummary.EchoExcludedTotal
	vault.AdoptionRatePct = adoptSummary.AdoptionRatePct

	out.noeticStream = out.noeticStream[:0]
	for i := range allNoetics {
		if len(out.noeticStream) >= 50 {
			break
		}
		out.noeticStream = append(out.noeticStream, allNoetics[i])
	}

	// Propagate per-Noetic adoption outcomes across ALL Noetic events to rolling windows and cited underlying units.
	noeticAdoptByID := make(map[string]roiNoeticEvent, len(allNoetics))
	for _, ev := range allNoetics {
		noeticAdoptByID[ev.NoeticID] = ev
		if ev.Status != "active" {
			continue
		}
		for _, sp := range specs {
			if ev.IngestAt >= sp.Floor {
				w := winMap[sp.Key]
				switch ev.AdoptionOutcome {
				case "adopted_tool":
					w.AuditedRecalls++
					w.AdoptedRecalls++
					w.AdoptedToolCount++
				case "adopted_context":
					w.AuditedRecalls++
					w.AdoptedRecalls++
					w.AdoptedContextCount++
				case "unadopted":
					w.AuditedRecalls++
				}
			}
		}
	}
	for _, sp := range specs {
		w := winMap[sp.Key]
		if w.AuditedRecalls > 0 {
			w.AdoptionRatePct = roundFloat(float64(w.AdoptedRecalls)*100.0/float64(w.AuditedRecalls), 1)
		}
	}

	// Fill the per-day adoption buckets (last 14 days) from the same settled audit
	// outcomes: only active crystallizations count, pending outcomes stay out of the
	// adoption-rate denominator, mirroring the vault-window aggregation above.
	dailyIdx := make(map[string]int, len(out.dailySeries))
	for i := range out.dailySeries {
		dailyIdx[out.dailySeries[i].Date] = i
	}
	for i := range allNoetics {
		ev := &allNoetics[i]
		if ev.Status != "active" || ev.IngestAt <= 0 {
			continue
		}
		idx, ok := dailyIdx[time.Unix(ev.IngestAt, 0).In(now.Location()).Format("2006-01-02")]
		if !ok {
			continue
		}
		b := &out.dailySeries[idx]
		switch ev.AdoptionOutcome {
		case "adopted_tool":
			b.AdoptedCount++
			b.AdoptedToolCount++
		case "adopted_context":
			b.AdoptedCount++
			b.AdoptedContextCount++
		case "unadopted":
			b.UnadoptedCount++
		default:
			b.PendingCount++
		}
	}
	for i := range out.dailySeries {
		b := &out.dailySeries[i]
		if audited := b.AdoptedCount + b.UnadoptedCount; audited > 0 {
			b.AdoptionRatePct = roundFloat(float64(b.AdoptedCount)*100.0/float64(audited), 1)
		}
	}
	for _, st := range citedStats {
		for idx := range st.citations {
			c := &st.citations[idx]
			if ev, ok := noeticAdoptByID[c.NoeticID]; ok {
				c.AdoptionOutcome = ev.AdoptionOutcome
				c.MatchedAnchors = ev.MatchedAnchors
				c.AssistantID = ev.AssistantID
				if ev.AdoptionOutcome == "adopted_tool" || ev.AdoptionOutcome == "adopted_context" {
					st.adoptedCount++
				}
			}
		}
	}

	type rankedCite struct {
		uid string
		st  *citeStat
	}
	rankedCites := make([]rankedCite, 0, len(citedStats))
	for uid, st := range citedStats {
		rankedCites = append(rankedCites, rankedCite{uid: uid, st: st})
	}
	sort.Slice(rankedCites, func(i, j int) bool {
		if rankedCites[i].st.count != rankedCites[j].st.count {
			return rankedCites[i].st.count > rankedCites[j].st.count
		}
		if rankedCites[i].st.lastAt != rankedCites[j].st.lastAt {
			return rankedCites[i].st.lastAt > rankedCites[j].st.lastAt
		}
		return rankedCites[i].uid < rankedCites[j].uid
	})
	if len(rankedCites) > 25 {
		rankedCites = rankedCites[:25]
	}
	for _, rc := range rankedCites {
		uid, st := rc.uid, rc.st
		m, existsInVault := unitMetaMap[uid]
		layer := m.layer
		if layer == "" {
			if strings.HasPrefix(uid, "bg:") {
				layer = "background"
			} else if strings.HasPrefix(uid, "nt_") || strings.HasPrefix(uid, "noetic_") {
				layer = "noetic"
			} else {
				layer = "episodic"
			}
		}
		fullText, isHydrated, isRef := resolveFullText(uid)
		if !existsInVault && fullText == "" {
			fullText = st.firstSnippet
		}
		preview := fullText
		if (isRef || m.isBGChunk) && !isHydrated && st.firstSnippet != "" {
			preview = st.firstSnippet
		}
		if preview == "" {
			preview = st.firstSnippet
		}
		if preview == "" {
			preview = uid
		}
		fallbackPreview := st.firstSnippet
		if isRef {
			fallbackPreview = vaultRefGapMarker
		} else if fallbackPreview == "" {
			fallbackPreview = uid
		}
		out.topCitedUnits = append(out.topCitedUnits, roiTopCitedUnit{
			storedRef:       m.rawText,
			fallbackPreview: fallbackPreview,
			UnitID:          uid,
			Layer:           layer,
			Locus:           m.locus,
			AddressURI:      m.addressURI,
			WaveformBar:     m.waveformBar,
			CitationCount:   st.count,
			AdoptedCount:    st.adoptedCount,
			LastCitedAt:     st.lastAt,
			OccurredAt:      m.occurredAt,
			Preview:         clipRunes(preview, 240),
			FullText:        clipRunes(fullText, 8192),
			IsHydrated:      isHydrated,
			Tokens:          int64(estimateTokenzRCount(fullText)),
			IsZeroCopyRef:   isRef,
			RecentQueries:   st.queries,
			Citations:       st.citations,
		})
	}

	// Build Workspace (Locus) Coverage & Heat Matrix.
	locusMap := make(map[string]*roiLocusRow)
	ensureLocus := func(raw string) *roiLocusRow {
		k := strings.TrimSpace(raw)
		if k == "" {
			k = "(global / unscoped)"
		}
		row, ok := locusMap[k]
		if !ok {
			row = &roiLocusRow{Locus: k, Status: "cold"}
			locusMap[k] = row
		}
		return row
	}
	locRows, err := db.QueryContext(ctx, `
		SELECT coalesce(layer, 'episodic'), coalesce(locus, ''), COUNT(*), COALESCE(MAX(occurred_at), 0)
		FROM rm_units
		GROUP BY 1, 2
	`)
	if err == nil {
		for locRows.Next() {
			var layer, loc string
			var cnt, maxAt int64
			if locRows.Scan(&layer, &loc, &cnt, &maxAt) != nil {
				continue
			}
			r := ensureLocus(loc)
			if strings.EqualFold(layer, "background") {
				r.BackgroundCount += cnt
			} else {
				r.EpisodicCount += cnt
			}
			if maxAt > r.LastActiveAt {
				r.LastActiveAt = maxAt
			}
		}
		locRows.Close()
	}
	if vault.BackgroundChunks > 0 {
		// 优先按 rm_bg_files 的 chunk_count 与 mtime_ns 聚合，避免每帧对 5.6 万分块做
		// c.abs_path = f.abs_path 文本键三表 JOIN；仅当单测夹具未填 chunk_count/mtime_ns 时回退子查询。
		bgDirRows, bgErr := db.QueryContext(ctx, `
			SELECT
				d.root_path,
				CASE
					WHEN COALESCE(SUM(f.chunk_count), 0) > 0 THEN SUM(f.chunk_count)
					ELSE (SELECT COUNT(c.unit_id) FROM rm_bg_files f2 JOIN rm_bg_chunks c ON c.abs_path = f2.abs_path WHERE f2.dir_id = d.dir_id)
				END AS chunk_cnt,
				COALESCE(
					NULLIF(MAX(f.mtime_ns / 1000000000), 0),
					(SELECT MAX(c.occurred_at) FROM rm_bg_files f2 JOIN rm_bg_chunks c ON c.abs_path = f2.abs_path WHERE f2.dir_id = d.dir_id),
					0
				) AS max_at
			FROM rm_bg_dirs d
			LEFT JOIN rm_bg_files f ON f.dir_id = d.dir_id
			GROUP BY d.dir_id, d.root_path
		`)
		if bgErr == nil {
			for bgDirRows.Next() {
				var rootPath string
				var chunkCnt, maxAt int64
				if bgDirRows.Scan(&rootPath, &chunkCnt, &maxAt) == nil && chunkCnt > 0 {
					r := ensureLocus(filepath.Base(rootPath))
					r.BackgroundCount += chunkCnt
					if maxAt > r.LastActiveAt {
						r.LastActiveAt = maxAt
					}
				}
			}
			bgDirRows.Close()
		}
	}
	for uid, st := range citedStats {
		m := unitMetaMap[uid]
		r := ensureLocus(m.locus)
		r.CitedUnitCount++
		r.CitationHits += st.count
		r.AdoptedHits += st.adoptedCount
		if st.lastAt > r.LastActiveAt {
			r.LastActiveAt = st.lastAt
		}
	}
	for _, r := range locusMap {
		if r.CitationHits > 0 {
			r.Status = "hot"
		} else if r.LastActiveAt >= nowUnix-86400 {
			r.Status = "warm"
		} else {
			r.Status = "cold"
		}
		out.locusMatrix = append(out.locusMatrix, *r)
	}
	sort.Slice(out.locusMatrix, func(i, j int) bool {
		if out.locusMatrix[i].CitationHits != out.locusMatrix[j].CitationHits {
			return out.locusMatrix[i].CitationHits > out.locusMatrix[j].CitationHits
		}
		totI := out.locusMatrix[i].EpisodicCount + out.locusMatrix[i].BackgroundCount
		totJ := out.locusMatrix[j].EpisodicCount + out.locusMatrix[j].BackgroundCount
		if totI != totJ {
			return totI > totJ
		}
		return out.locusMatrix[i].Locus < out.locusMatrix[j].Locus
	})

	for _, sp := range specs {
		w := winMap[sp.Key]
		w.UniqueCitedUnits = int64(len(winCitedSets[sp.Key]))
		out.vaultWindows = append(out.vaultWindows, *w)
	}
	out.vault = vault
	return out, sig
}

// roiLedgerRowQuery 是账本形状探测所需的最小查询面（*sql.DB 与 *sql.Tx 均满足）。
type roiLedgerRowQuery interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// roiLedgerShape 判定账本是否含修订行（correction/retraction）与 MCP 直写孪生行
// （hook 为非 tool 的 mcp_recall:*）。两者皆无时，有效集 LEFT JOIN 与反连接均可
// 证明为空操作，直接扫描基表与完整 CTE 等价；查询失败返回 ok=false，调用方走完整路径。
func roiLedgerShape(ctx context.Context, q roiLedgerRowQuery) (revisions bool, siblings bool, ok bool) {
	var found int
	if err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM efficacy_event WHERE action IN ('correction', 'retraction'))`).Scan(&found); err != nil {
		return true, true, false
	}
	revisions = found != 0
	if err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM efficacy_event WHERE action = 'recall' AND hook LIKE 'mcp_recall:%' AND hook <> 'mcp_recall:tool')`).Scan(&found); err != nil {
		return true, true, false
	}
	siblings = found != 0
	return revisions, siblings, true
}

// queryROIFacts reads a single SQLite snapshot. Only explicitly selected scopes
// are included; unassigned scopes and harnesses never enter a selected bucket.
func queryROIFacts(ctx context.Context, memoryPath string, req roiFactsRequest) roiFactsResponse {
	scope := strings.TrimSpace(req.Scope)
	harness := strings.TrimSpace(req.Harness)
	if scope == "" {
		scope = UserScope
	}
	altScope := scope
	if scope == UserScope {
		altScope = LegacyUserScope
	} else if scope == LegacyUserScope {
		altScope = UserScope
	}
	bundle := readROIFactsVaultBundle(memoryPath)
	attachBundle := func(resp roiFactsResponse) roiFactsResponse {
		resp.Vault = bundle.vault
		resp.VaultWindows = bundle.vaultWindows
		resp.DailySeries = bundle.dailySeries
		resp.TopCitedUnits = bundle.topCitedUnits
		resp.LocusMatrix = bundle.locusMatrix
		resp.NoeticStream = bundle.noeticStream
		return resp
	}
	unknown := func(reason string) roiFactsResponse {
		return attachBundle(roiFactsUnknown(scope, harness, reason))
	}
	if scope == "ALL" || scope == "ALL_WITH_EVAL" || strings.TrimSpace(memoryPath) == "" || memoryPath == ":memory:" || strings.HasPrefix(memoryPath, "file:") || strings.ContainsAny(memoryPath, "?#") {
		return unknown("invalid_selection")
	}
	path, err := filepath.Abs(memoryPath)
	if err != nil {
		return unknown("database_unavailable")
	}
	path += ".efficacy.sqlite"
	info, err := os.Stat(path)
	if os.IsNotExist(err) {
		return unknown("ledger_missing")
	}
	if err != nil || !info.Mode().IsRegular() {
		return unknown("database_unavailable")
	}
	db, release := borrowVaultReadOnly(path)
	if db == nil {
		return unknown("database_unavailable")
	}
	defer release()
	// The effective-set walk degrades to a base-table scan when the ledger has
	// no revisions or non-tool MCP sibling rows; 10s tolerates multi-fold growth
	// on the correlated path before the snapshot fails closed.
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return unknown("database_unavailable")
	}
	defer tx.Rollback()
	var version int
	if err := tx.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		return unknown("database_unavailable")
	}
	if version != 1 {
		return unknown("schema_version")
	}

	limit := req.Limit
	if limit <= 0 {
		limit = 20
	}
	if limit > 100 {
		limit = 100
	}
	// A correction replaces its original observation; a retraction removes it.
	// The scope and harness must match both the original and its revision.
	const effective = `WITH effective AS (
			SELECT seq, action, harness, hook, outcome, observation, text_bytes, observed_at, effective_seq
			FROM (
				SELECT e.seq, e.event_id, e.action, e.harness, e.hook, COALESCE(r.output_outcome, e.output_outcome) AS outcome,
					COALESCE(r.observation, e.observation) AS observation,
					CASE WHEN r.seq IS NULL THEN e.text_bytes ELSE r.text_bytes END AS text_bytes,
					COALESCE(r.observed_at, e.observed_at) AS observed_at,
					COALESCE(r.seq, e.seq) AS effective_seq
				FROM efficacy_event e
				LEFT JOIN efficacy_event r ON r.seq = (
					SELECT x.seq FROM efficacy_event x
					WHERE x.target_event_id = e.event_id AND x.scope = e.scope
						AND x.harness = e.harness AND x.session = e.session AND x.hook = e.hook
						AND x.execution_id = e.execution_id AND x.action IN ('correction', 'retraction')
					ORDER BY x.seq DESC LIMIT 1)
					WHERE e.scope IN (?, ?) AND e.scope != '' AND (? = '' OR e.harness = ?)
						AND e.action IN ('decision', 'prepared', 'output_attempt', 'recall')
						AND (r.seq IS NULL OR r.action != 'retraction')
						AND (e.hook != 'mcp_recall:tool' OR NOT EXISTS (
							SELECT 1 FROM efficacy_event d
							WHERE d.scope = e.scope
							  AND d.action = 'recall'
							  AND d.hook LIKE 'mcp_recall:%'
							  AND d.hook != 'mcp_recall:tool'
							  AND d.observed_at BETWEEN e.observed_at - 2000000000 AND e.observed_at + 2000000000
						))
				)
				GROUP BY event_id
				)`
	revisions, siblings, shapeOK := roiLedgerShape(ctx, tx)
	var rows *sql.Rows
	if shapeOK && !revisions && !siblings {
		// 无修订、无直写孪生行：基表即有效集，省去两次全表相关的派生/反连接。
		rows, err = tx.QueryContext(ctx, `
				SELECT e.action, e.output_outcome, e.observation, e.harness, e.hook, e.text_bytes, e.observed_at, e.seq
				FROM efficacy_event e
				WHERE e.scope IN (?, ?) AND e.scope != '' AND (? = '' OR e.harness = ?)
					AND e.action IN ('decision', 'prepared', 'output_attempt', 'recall')`,
			scope, altScope, harness, harness)
	} else {
		rows, err = tx.QueryContext(ctx, effective+` SELECT action, outcome, observation, harness, hook, text_bytes, observed_at, effective_seq FROM effective`, scope, altScope, harness, harness)
	}
	if err != nil {
		return unknown("schema_or_query_error")
	}
	settled := []roiFactEffectiveRow{}
	for rows.Next() {
		var item roiFactEffectiveRow
		if err := rows.Scan(&item.Action, &item.Outcome, &item.Observation, &item.Harness, &item.Hook, &item.Bytes, &item.ObservedAt, &item.EffectiveSeq); err != nil {
			rows.Close()
			return unknown("schema_or_query_error")
		}
		settled = append(settled, item)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return unknown("database_unavailable")
	}
	rows.Close()
	// A single effective-set scan feeds the aggregate counts, the rolling
	// windows and the recent tail; re-running the correlated-CTE per view
	// triples the scan cost and blows the request budget on large ledgers.
	counts := &roiFactCounts{Actions: map[string]int64{}, DecisionOutcomes: map[string]int64{}, OutputOutcomes: map[string]int64{}, RecallOutcomes: map[string]int64{}}
	for _, item := range settled {
		if !validROIFactOutcome(item.Action, item.Outcome) || (item.Observation != "observed" && item.Observation != "unknown") {
			return unknown("invalid_event")
		}
		counts.Total++
		counts.Actions[item.Action]++
		switch item.Action {
		case "decision":
			counts.DecisionOutcomes[item.Outcome]++
		case "output_attempt":
			counts.OutputOutcomes[item.Outcome]++
		case "recall":
			counts.RecallOutcomes[item.Outcome]++
		}
		if item.Observation == "observed" && item.Bytes.Valid && item.Bytes.Int64 >= 0 {
			if item.Bytes.Int64 > int64(^uint64(0)>>1)-counts.KnownTextBytes {
				return unknown("invalid_event")
			}
			counts.KnownTextBytes += item.Bytes.Int64
			counts.KnownByteEvents++
		} else {
			counts.UnknownByteEvents++
		}
	}
	if counts.Total == 0 {
		return unknown("no_matching_events")
	}
	windows := foldROIFactWindows(settled, bundle.vaultWindows)
	sort.Slice(settled, func(i, j int) bool { return settled[i].EffectiveSeq > settled[j].EffectiveSeq })
	recent := []roiFactEvent{}
	for _, item := range settled {
		if len(recent) >= limit {
			break
		}
		event := roiFactEvent{
			Action:      item.Action,
			Outcome:     item.Outcome,
			Observation: item.Observation,
			Harness:     item.Harness,
			Hook:        item.Hook,
			ObservedAt:  item.ObservedAt,
		}
		if event.ObservedAt > 0 {
			event.ObservedAt /= int64(time.Second)
		}
		if item.Observation == "observed" && item.Bytes.Valid && item.Bytes.Int64 >= 0 {
			bytes := item.Bytes.Int64
			event.TextBytes = &bytes
		}
		recent = append(recent, event)
	}
	identityStatus := "unknown"
	var storedFP string
	if err := tx.QueryRowContext(ctx, `SELECT value FROM efficacy_meta WHERE key = 'db_fingerprint'`).Scan(&storedFP); err == nil && storedFP != "" {
		if expectedFP := computeAdminDBFingerprint(memoryPath, scope); expectedFP != "" && storedFP == expectedFP {
			identityStatus = "verified"
		}
	}
	return attachBundle(roiFactsResponse{
		Status:         "available",
		Source:         "admin_selected_db",
		Coverage:       "unknown",
		IdentityStatus: identityStatus,
		Scope:          scope,
		Harness:        harness,
		Counts:         counts,
		Windows:        windows,
		RecentEvents:   recent,
	})
}

// validROIFactOutcome keeps the closed outcome set per action; an unknown action
// or outcome means the ledger was written by something this reader cannot
// honestly interpret, so the whole snapshot is refused.
func validROIFactOutcome(action, outcome string) bool {
	switch action {
	case "decision":
		return outcome == "output" || outcome == "empty" || outcome == "error" || outcome == "unknown"
	case "prepared":
		return outcome == "not_applicable"
	case "output_attempt":
		return outcome == "success" || outcome == "failure" || outcome == "short_write" || outcome == "unknown"
	case "recall":
		return outcome == "hits" || outcome == "empty" || outcome == "error" || outcome == "unknown"
	}
	return false
}

// foldROIFactWindows folds the settled events into the fixed rolling windows
// and merges any live vault window metrics for the same keys. Events older
// than the widest window fall out through the per-window since check.
func foldROIFactWindows(settled []roiFactEffectiveRow, vaultWins []roiFactWindow) []roiFactWindow {
	now := time.Now().Unix()
	buckets := map[string]*roiFactWindow{}
	order := make([]string, 0, len(roiFactWindows))
	vaultByKey := make(map[string]roiFactWindow, len(vaultWins))
	for _, vw := range vaultWins {
		vaultByKey[vw.Key] = vw
	}
	for _, window := range roiFactWindows {
		vw := vaultByKey[window.Key]
		buckets[window.Key] = &roiFactWindow{
			Key:                 window.Key,
			Seconds:             window.Seconds,
			SinceUnix:           now - window.Seconds,
			Actions:             map[string]int64{},
			RecallOutcomes:      map[string]int64{},
			EpisodicAdded:       vw.EpisodicAdded,
			BackgroundAdded:     vw.BackgroundAdded,
			NoeticCrystallized:  vw.NoeticCrystallized,
			CitationLinks:       vw.CitationLinks,
			UniqueCitedUnits:    vw.UniqueCitedUnits,
			DeliveredTokens:     vw.DeliveredTokens,
			AuditedRecalls:      vw.AuditedRecalls,
			AdoptedRecalls:      vw.AdoptedRecalls,
			AdoptedToolCount:    vw.AdoptedToolCount,
			AdoptedContextCount: vw.AdoptedContextCount,
			AdoptionRatePct:     vw.AdoptionRatePct,
		}
		order = append(order, window.Key)
	}
	for _, item := range settled {
		for _, key := range order {
			window := buckets[key]
			if item.ObservedAt < window.SinceUnix*int64(time.Second) {
				continue
			}
			window.Total++
			window.Actions[item.Action]++
			if item.Action == "recall" {
				window.RecallOutcomes[item.Outcome]++
			}
			if item.Observation == "observed" && item.Bytes.Valid && item.Bytes.Int64 >= 0 {
				window.KnownTextBytes += item.Bytes.Int64
			} else {
				window.UnknownByteEvents++
			}
		}
	}
	windows := make([]roiFactWindow, 0, len(order))
	for _, key := range order {
		windows = append(windows, *buckets[key])
	}
	return windows
}
