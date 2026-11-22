// plugin: rolling-memory v0.0.58
// three_layers.go — 插件后台「三层认知记忆管理与统计（Background × Episodic × Noetic）」及本地多目录挂载后端实现。
package rollingmemory

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/ai-go-hub/ai-go-admin/internal/kit/httpx"
	"github.com/gin-gonic/gin"
)

// LayerStatsResponse 三层认知记忆统计与分布响应（同时兼容 BuildAdmin useTableManager 的 {list, total} 列表契约）。
type LayerStatsResponse struct {
	DBPath               string          `json:"db_path"`
	TotalUnits           int             `json:"total_units"`
	BackgroundCount      int             `json:"background_count"`
	BackgroundDirCount   int             `json:"background_dir_count"`
	BackgroundFileCount  int             `json:"background_file_count"`
	BackgroundChunkCount int             `json:"background_chunk_count"`
	EpisodicCount        int             `json:"episodic_count"`
	EpisodicClusterCount int             `json:"episodic_cluster_count"`
	NoeticActiveCount    int             `json:"noetic_active_count"`
	NoeticStaleCount     int             `json:"noetic_stale_count"`
	NoeticCitedLinks     int             `json:"noetic_cited_links"`
	UpdatedAt            string          `json:"updated_at"`
	Total                int             `json:"total"`
	List                 []ThreeLayerRow `json:"list"`
	Items                []ThreeLayerRow `json:"items"`
}

// tableWhereCondition 与宿主 useTableManager / ComSearch 生成的查询项同构。
type tableWhereCondition struct {
	Field    string `json:"field"`
	Operator string `json:"operator"`
	Value    any    `json:"value"`
}

// tableWhereGroup 与宿主 useTableManager 生成的 wheres 分组同构（组间 AND，组内按 Or 决定 OR/AND）。
type tableWhereGroup struct {
	Wheres []tableWhereCondition `json:"wheres"`
	Or     bool                  `json:"or"`
}

// ThreeLayerRow 统一三层记忆浏览行（覆盖 L1 Background / L2 Episodic / L3 Noetic）。
type ThreeLayerRow struct {
	ID              string   `json:"id"`
	Layer           string   `json:"layer"`  // background | episodic | noetic
	Status          string   `json:"status"` // active | stale | superseded
	Title           string   `json:"title"`
	Content         string   `json:"content"`
	AnswerText      string   `json:"answer_text,omitempty"`
	CitedUnitIDs    []string `json:"cited_unit_ids,omitempty"`
	AddressURI      string   `json:"address_uri,omitempty"`
	RelPath         string   `json:"rel_path,omitempty"`
	Anchor          string   `json:"anchor,omitempty"`
	OccurredAt      int64    `json:"occurred_at"`
	TimeText        string   `json:"time_text"`
	AssistantID     string   `json:"assistant_id,omitempty"`
	AdoptionOutcome string   `json:"adoption_outcome,omitempty"`
	MatchedAnchors  []string `json:"matched_anchors,omitempty"`
	// BodyUnavailable marks source text that could not be read or verified.
	BodyUnavailable bool `json:"body_unavailable,omitempty"`
}

// BackgroundDirItem 已挂载的本地背景目录条目。
type BackgroundDirItem struct {
	DirID       string   `json:"dir_id"`
	RootPath    string   `json:"root_path"`
	AddedAt     int64    `json:"added_at"`
	AddedText   string   `json:"added_text"`
	FileCount   int      `json:"file_count"`
	ChunkCount  int      `json:"chunk_count"`
	IncludeExts []string `json:"include_exts"`
}

// BackgroundFileItem 已挂载目录内的单个文件条目（目录 → 文件 → 分块三级浏览的中间层）。
type BackgroundFileItem struct {
	AbsPath     string `json:"abs_path"`
	DirID       string `json:"dir_id"`
	RelPath     string `json:"rel_path"`
	FileSize    int64  `json:"file_size"`
	SizeText    string `json:"size_text"`
	ChunkCount  int    `json:"chunk_count"`
	UpdatedAt   int64  `json:"updated_at"`
	UpdatedText string `json:"updated_text"`
}

// BackgroundSkippedFile 记录本次同步未能读取/提取的支持文件（内容超限、为空或不可读）：
// 该文件不会进入挂载索引，早期已索引的行会被清理，同步其余部分照常完成。
type BackgroundSkippedFile struct {
	AbsPath string `json:"abs_path"`
	RelPath string `json:"rel_path,omitempty"`
	Reason  string `json:"reason"`
}

// BackgroundSyncResult 手动或自动增量同步报告。
type BackgroundSyncResult struct {
	MountedDirs          int                     `json:"mounted_dirs"`
	ScannedFiles         int                     `json:"scanned_files"`
	UnchangedFiles       int                     `json:"unchanged_files"`
	IngestedFiles        int                     `json:"ingested_files"`
	IngestedChunks       int                     `json:"ingested_chunks"`
	DeletedFiles         int                     `json:"deleted_files"`
	DeletedChunks        int                     `json:"deleted_chunks"`
	SkippedFiles         []BackgroundSkippedFile `json:"skipped_files,omitempty"`
	UnreadableDirs       []string                `json:"unreadable_dirs,omitempty"`
	InvalidatedNoeticIDs []string                `json:"invalidated_noetic_ids"`
}

// vaultCoreSchemaDDL bootstraps the canonical corpus tables and the rm_units read model for an empty database. 全程 CREATE TABLE IF NOT EXISTS，
// 幂等且不破坏任何既有数据。金库 schema 的唯一正本永远是记忆引擎本身。
const vaultCoreSchemaDDL = `
	CREATE TABLE IF NOT EXISTS rm_docs (
		unit_id TEXT PRIMARY KEY,
		occurred_at INTEGER NOT NULL DEFAULT 0,
		narrated_at INTEGER NOT NULL DEFAULT 0,
		ingested_at INTEGER NOT NULL DEFAULT 0,
		author_hash INTEGER NOT NULL DEFAULT 0,
		doc_hash INTEGER NOT NULL DEFAULT 0,
		block_hash INTEGER NOT NULL DEFAULT 0,
		line_start INTEGER NOT NULL DEFAULT 0,
		line_end INTEGER NOT NULL DEFAULT 0,
		layer TEXT NOT NULL DEFAULT '',
		term_count INTEGER NOT NULL DEFAULT 0,
		raw_text TEXT NOT NULL DEFAULT ''
	);
	CREATE TABLE IF NOT EXISTS rm_unit_meta (
		unit_id TEXT PRIMARY KEY,
		layer TEXT NOT NULL DEFAULT '',
		ref_pointer TEXT NOT NULL DEFAULT '',
		sentences_json TEXT NOT NULL DEFAULT '[]',
		kept_indices_json TEXT NOT NULL DEFAULT '[]',
		address_uri TEXT NOT NULL DEFAULT '',
		coord64 INTEGER NOT NULL DEFAULT 0,
		waveform_bar TEXT NOT NULL DEFAULT '',
		doc_macro_wave TEXT NOT NULL DEFAULT '',
		assistant_id TEXT NOT NULL DEFAULT '',
		incarnation_id TEXT NOT NULL DEFAULT '',
		locus TEXT NOT NULL DEFAULT '',
		inserted_at INTEGER NOT NULL DEFAULT 0,
		meta_rev INTEGER NOT NULL DEFAULT 0
	);
	CREATE VIEW IF NOT EXISTS rm_units AS
	SELECT d.unit_id AS doc_id,
	       d.layer AS layer,
	       d.occurred_at AS occurred_at,
	       COALESCE(NULLIF(m.inserted_at, 0), d.ingested_at, d.occurred_at) AS inserted_at,
	       COALESCE(NULLIF(m.ref_pointer, ''), d.raw_text) AS raw_text,
	       COALESCE(m.sentences_json, '[]') AS sentences_json,
	       COALESCE(m.address_uri, '') AS address_uri,
	       COALESCE(m.coord64, 0) AS coord64,
	       COALESCE(m.waveform_bar, '') AS waveform_bar,
	       COALESCE(m.doc_macro_wave, '') AS doc_macro_wave,
	       COALESCE(m.kept_indices_json, '[]') AS kept_indices_json,
	       COALESCE(m.assistant_id, '') AS assistant_id,
	       COALESCE(m.incarnation_id, '') AS incarnation_id,
	       COALESCE(m.locus, '') AS locus
	  FROM rm_docs d
	  LEFT JOIN rm_unit_meta m ON m.unit_id = d.unit_id;
	CREATE TRIGGER IF NOT EXISTS rm_units_insert INSTEAD OF INSERT ON rm_units
	BEGIN
	  INSERT INTO rm_docs(unit_id, layer, occurred_at, raw_text)
	  VALUES (NEW.doc_id, COALESCE(NULLIF(NEW.layer, ''), 'episodic'), COALESCE(NEW.occurred_at, 0),
	          CASE WHEN substr(trim(COALESCE(NEW.raw_text, '')), 1, 5) = '@ref:' THEN '' ELSE COALESCE(NEW.raw_text, '') END)
	  ON CONFLICT(unit_id) DO UPDATE SET layer = excluded.layer, occurred_at = CASE WHEN excluded.occurred_at > 0 THEN excluded.occurred_at ELSE rm_docs.occurred_at END, raw_text = excluded.raw_text;
	  INSERT INTO rm_unit_meta(unit_id, layer, ref_pointer, sentences_json, kept_indices_json, address_uri, coord64, waveform_bar, doc_macro_wave, assistant_id, incarnation_id, locus, inserted_at, meta_rev)
	  VALUES (NEW.doc_id, COALESCE(NULLIF(NEW.layer, ''), 'episodic'),
	          CASE WHEN substr(trim(COALESCE(NEW.raw_text, '')), 1, 5) = '@ref:' THEN NEW.raw_text ELSE '' END,
	          COALESCE(NEW.sentences_json, '[]'), COALESCE(NEW.kept_indices_json, '[]'),
	          COALESCE(NEW.address_uri, ''), COALESCE(NEW.coord64, 0), COALESCE(NEW.waveform_bar, ''), COALESCE(NEW.doc_macro_wave, ''),
	          COALESCE(NEW.assistant_id, ''), COALESCE(NEW.incarnation_id, ''), COALESCE(NEW.locus, ''),
	          COALESCE(NULLIF(NEW.inserted_at, 0), NEW.occurred_at, 0), 1)
	  ON CONFLICT(unit_id) DO UPDATE SET layer = excluded.layer, ref_pointer = excluded.ref_pointer,
	          sentences_json = excluded.sentences_json, kept_indices_json = excluded.kept_indices_json,
	          address_uri = excluded.address_uri, coord64 = excluded.coord64, waveform_bar = excluded.waveform_bar,
	          doc_macro_wave = excluded.doc_macro_wave, assistant_id = excluded.assistant_id,
	          incarnation_id = excluded.incarnation_id, locus = excluded.locus, inserted_at = excluded.inserted_at,
	          meta_rev = rm_unit_meta.meta_rev + 1;
	END;
	CREATE TRIGGER IF NOT EXISTS rm_units_update INSTEAD OF UPDATE ON rm_units
	BEGIN
	  UPDATE rm_docs SET layer = COALESCE(NULLIF(NEW.layer, ''), layer), occurred_at = COALESCE(NEW.occurred_at, occurred_at),
	          raw_text = CASE WHEN substr(trim(COALESCE(NEW.raw_text, '')), 1, 5) = '@ref:' THEN '' ELSE COALESCE(NEW.raw_text, raw_text) END
	    WHERE unit_id = OLD.doc_id;
	  UPDATE rm_unit_meta SET layer = COALESCE(NULLIF(NEW.layer, ''), layer),
	          ref_pointer = CASE WHEN substr(trim(COALESCE(NEW.raw_text, '')), 1, 5) = '@ref:' THEN NEW.raw_text ELSE '' END,
	          sentences_json = COALESCE(NEW.sentences_json, sentences_json),
	          kept_indices_json = COALESCE(NEW.kept_indices_json, kept_indices_json),
	          address_uri = COALESCE(NEW.address_uri, address_uri), coord64 = COALESCE(NEW.coord64, coord64),
	          waveform_bar = COALESCE(NEW.waveform_bar, waveform_bar), doc_macro_wave = COALESCE(NEW.doc_macro_wave, doc_macro_wave),
	          assistant_id = COALESCE(NEW.assistant_id, assistant_id), incarnation_id = COALESCE(NEW.incarnation_id, incarnation_id),
	          locus = COALESCE(NEW.locus, locus), inserted_at = COALESCE(NULLIF(NEW.inserted_at, 0), inserted_at),
	          meta_rev = meta_rev + 1
	    WHERE unit_id = OLD.doc_id;
	END;
	CREATE TRIGGER IF NOT EXISTS rm_units_delete INSTEAD OF DELETE ON rm_units
	BEGIN
	  DELETE FROM rm_docs WHERE unit_id = OLD.doc_id;
	END;
	CREATE TABLE IF NOT EXISTS rm_noetic (
		noetic_id TEXT PRIMARY KEY,
		occurred_at INTEGER NOT NULL DEFAULT 0,
		status TEXT NOT NULL DEFAULT 'active',
		query_text TEXT NOT NULL,
		answer_text TEXT NOT NULL,
		cited_unit_ids_json TEXT NOT NULL DEFAULT '[]',
		cited_sentences_json TEXT NOT NULL DEFAULT '[]',
		address_uri TEXT NOT NULL DEFAULT '',
		coord64 INTEGER NOT NULL DEFAULT 0,
		assistant_id TEXT NOT NULL DEFAULT '',
		incarnation_id TEXT NOT NULL DEFAULT ''
	);
	CREATE TABLE IF NOT EXISTS rm_bg_dirs (
		dir_id TEXT PRIMARY KEY,
		root_path TEXT NOT NULL UNIQUE,
		added_at INTEGER NOT NULL DEFAULT 0,
		include_exts_json TEXT NOT NULL DEFAULT '[]'
	);
	CREATE TABLE IF NOT EXISTS rm_bg_files (
		abs_path TEXT PRIMARY KEY,
		dir_id TEXT NOT NULL,
		rel_path TEXT NOT NULL,
		mtime_ns INTEGER NOT NULL DEFAULT 0,
		file_size INTEGER NOT NULL DEFAULT 0,
		content_sha256 TEXT NOT NULL DEFAULT '',
		chunk_count INTEGER NOT NULL DEFAULT 0,
		unit_ids_json TEXT NOT NULL DEFAULT '[]',
		chunk_hashes_json TEXT NOT NULL DEFAULT '[]',
		chunker_generation TEXT NOT NULL DEFAULT ''
	);
	CREATE TABLE IF NOT EXISTS rm_bg_chunks (
		unit_id TEXT PRIMARY KEY,
		abs_path TEXT NOT NULL,
		rel_path TEXT NOT NULL,
		chunk_index INTEGER NOT NULL DEFAULT 0,
		anchor TEXT NOT NULL DEFAULT '',
		occurred_at INTEGER NOT NULL DEFAULT 0,
		inserted_at INTEGER NOT NULL DEFAULT 0,
		address_uri TEXT NOT NULL DEFAULT '',
		coord64 INTEGER NOT NULL DEFAULT 0,
		waveform_bar TEXT NOT NULL DEFAULT '',
		kept_indices_json TEXT NOT NULL DEFAULT '[]'
	);`

// openVaultWriteOnly 打开可写连接并保证核心表存在，**不做任何破坏性 DDL**：
// 不 ALTER、不 DROP、不建 FTS 镜像。记忆管理页面的写操作（删除记忆单元 / 挂载与解挂背景目录）
// 一律走它 —— 早期实现让每一次读请求都执行整套 DDL + 24 张遗留表 DROP，既拖慢列表又对用户金库
// 做了无确认的破坏性动作。
func openVaultWriteOnly(dbPath string) (*sql.DB, error) {
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o755); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", sqliteFileURI(dbPath)+"?_txlock=immediate&_pragma=busy_timeout(15000)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)")
	if err != nil {
		return nil, err
	}
	var existingTables int
	if err := db.QueryRow(`
		SELECT COUNT(*) FROM sqlite_master
		WHERE type = 'table' AND name IN (
			'rm_meta', 'rm_docs', 'rm_unit_meta', 'rm_noetic',
			'rm_bg_dirs', 'rm_bg_files', 'rm_bg_chunks'
		)
	`).Scan(&existingTables); err == nil && existingTables == 7 {
		return db, nil
	}
	if _, err := db.Exec(vaultCoreSchemaDDL); err != nil {
		_ = db.Close()
		return nil, err
	}
	return db, nil
}

// openPlainSQLite 打开任意本地 SQLite 文件的可写连接，但**不创建 / 不迁移任何记忆表**。
// 供 ROI 台账（memory.db.efficacy.sqlite）等自带 schema 的辅助库使用：它们绝不该被套上
// 记忆表皮，否则辅助库会被误写成半个记忆金库。
func openPlainSQLite(dbPath string) (*sql.DB, error) {
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o755); err != nil {
		return nil, err
	}
	return sql.Open("sqlite", sqliteFileURI(dbPath)+"?_txlock=immediate&_pragma=busy_timeout(15000)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)")
}

// openWritableVaultDB 在 openVaultWriteOnly 之上补齐历史列迁移与早期遗留表回收。
//
// **仅限测试与作者显式维护使用**：它会对金库执行 ALTER/DROP，因此绝不允许出现在任何请求路径上
// （读路径用 memoryRODB 只读连接，写路径用 openVaultWriteOnly）。
func openWritableVaultDB(dbPath string) (*sql.DB, error) {
	return openVaultWriteOnly(dbPath)
}

// bindJSON 解析请求体：空体视为「缺省请求」（BuildAdmin 表格刷新会发 `{}`，部分调用方发空体），
// 但**非空且畸形**的 JSON 必须如实报错，绝不允许被静默当成空请求执行
// （早期实现一律 `_ = c.ShouldBindJSON(&req)`，参数写错时表现为「筛选失效」而不是报错）。
func bindJSON(c *gin.Context, dst any) bool {
	if err := c.ShouldBindJSON(dst); err != nil && !errors.Is(err, io.EOF) {
		httpx.Fail(c, httpx.WithMessage("请求参数格式错误: "+err.Error()))
		return false
	}
	return true
}

// layersStatsHandler 返回三层记忆（Background / Episodic / Noetic）统计数据与按层过滤的记忆单元列表。
// 同时作为 POST /layers/list 与 POST /layers/stats 处理器，100% 兼容 BuildAdmin TableManagerAPI（page/limit/sort/order/wheres/quickSearchKeywords）。
func layersStatsHandler(c *gin.Context) {
	var req struct {
		Layer               string            `json:"layer"`    // "" | "all" | "background" | "episodic" | "noetic"
		Status              string            `json:"status"`   // L3 Noetic 状态过滤："" | "all" | "active" | "stale"（其余层忽略）
		DirID               string            `json:"dir_id"`   // L1 Background 挂载目录过滤（其余层忽略）
		RelPath             string            `json:"rel_path"` // L1 Background 相对路径过滤
		Keyword             string            `json:"keyword"`
		QuickSearchKeywords any               `json:"quickSearchKeywords"`
		Page                int               `json:"page"`
		Limit               int               `json:"limit"`
		Sort                string            `json:"sort"`
		Order               string            `json:"order"`
		Wheres              []tableWhereGroup `json:"wheres"`
	}
	if !bindJSON(c, &req) {
		return
	}
	defaultLimit := 100
	if req.Page > 0 {
		defaultLimit = 20
	}
	if req.Limit <= 0 {
		req.Limit = defaultLimit
	} else if req.Limit > 500 {
		req.Limit = 500
	}
	page := req.Page
	if page <= 0 {
		page = 1
	}

	layerFilter := strings.ToLower(strings.TrimSpace(req.Layer))
	if layerFilter == "all" {
		layerFilter = ""
	}
	statusFilter := strings.ToLower(strings.TrimSpace(req.Status))
	if statusFilter != "active" && statusFilter != "stale" {
		statusFilter = ""
	}
	dirFilter := strings.TrimSpace(req.DirID)
	relPathFilter := strings.ToLower(strings.TrimSpace(req.RelPath))
	kw := strings.ToLower(strings.TrimSpace(req.Keyword))
	if kw == "" && req.QuickSearchKeywords != nil {
		switch v := req.QuickSearchKeywords.(type) {
		case string:
			kw = strings.ToLower(strings.TrimSpace(v))
		case []any:
			for _, item := range v {
				if s := strings.ToLower(strings.TrimSpace(asString(item))); s != "" {
					kw = s
					break
				}
			}
		}
	}

	home, _ := os.UserHomeDir()
	dbPath := resolveMemoryDBPath(home)
	if err := checkVaultReadable(dbPath); err != nil {
		httpx.Fail(c, httpx.WithMessage("读取本地记忆库失败: "+err.Error()))
		return
	}

	resp := LayerStatsResponse{
		DBPath:    dbPath,
		UpdatedAt: time.Now().Format("15:04:05"),
		List:      []ThreeLayerRow{},
		Items:     []ThreeLayerRow{},
	}

	// ---- 三层全局计数（与当前页筛选无关，口径与卡片头一致；一律只读、缺表即归零，按文件签名缓存）----
	fillCachedLayerGlobalCounts(dbPath, &resp)

	// ---- 三层统一列表：layer/status/dir/rel_path/keyword/wheres 过滤与排序、分页全部下推 SQL ----
	// 三层语义在 SQL 侧严格保持：L1=挂载目录文件分块、L2=对话记录单元、L3=提问+证据固化（cited 指回 L1/L2）。
	items, total, err := queryLayerRows(dbPath, layerListQuery{
		layer:   layerFilter,
		status:  statusFilter,
		dirID:   dirFilter,
		relPath: relPathFilter,
		keyword: kw,
		wheres:  req.Wheres,
		sort:    req.Sort,
		order:   req.Order,
		limit:   req.Limit,
		offset:  (page - 1) * req.Limit,
	}, home)
	if err != nil {
		httpx.Fail(c, httpx.WithMessage("查询记忆列表失败: "+err.Error()))
		return
	}
	enrichThreeLayerRowsWithAdoption(dbPath, items)
	resp.Items = items
	resp.List = items
	resp.Total = total
	resp.TotalUnits = resp.BackgroundCount + resp.EpisodicCount + resp.NoeticActiveCount
	httpx.Success(c, httpx.WithData(resp))
}

// checkVaultReadable 检查库是否存在及是否可读。若库不存在返回 nil，存在但损坏返回 error。
func checkVaultReadable(dbPath string) error {
	if _, err := os.Stat(dbPath); errors.Is(err, os.ErrNotExist) {
		return nil
	}
	db, err := memoryRODB(dbPath)
	if err != nil {
		return err
	}
	var cnt int
	return db.QueryRow(`SELECT count(*) FROM sqlite_master`).Scan(&cnt)
}
func layersGetHandler(c *gin.Context) {
	pk := strings.TrimSpace(c.Param("pk"))
	if pk == "" {
		var req struct {
			ID string `json:"id"`
		}
		if !bindJSON(c, &req) {
			return
		}
		pk = strings.TrimSpace(req.ID)
	}
	if pk == "" {
		httpx.Fail(c, httpx.WithMessage("记忆单元 ID 不能为空"))
		return
	}
	home, _ := os.UserHomeDir()
	dbPath := resolveMemoryDBPath(home)
	if err := checkVaultReadable(dbPath); err != nil {
		httpx.Fail(c, httpx.WithMessage("记忆库读取错误: "+err.Error()))
		return
	}
	// 详情是只读面：走只读连接池（不建库、不建表、不落任何写入，金库不存在即按未找到处置）。
	db, err := memoryRODB(dbPath)
	if err != nil {
		httpx.Fail(c, httpx.WithMessage("未找到记忆单元: "+pk))
		return
	}

	// 1. Check rm_units
	var did, lyr, rawTxt, addr string
	var occ int64
	if db.QueryRow(`SELECT doc_id, layer, occurred_at, raw_text, address_uri FROM rm_units WHERE doc_id = ?`, pk).
		Scan(&did, &lyr, &occ, &rawTxt, &addr) == nil {
		lyr = strings.ToLower(strings.TrimSpace(lyr))
		if lyr != "background" && lyr != "noetic" {
			lyr = "episodic"
		}
		bodyUnavailable := false
		if strings.HasPrefix(strings.TrimSpace(rawTxt), "@ref:") {
			if resolved, ok := hydratedEpisodicPointerLive(home, dbPath, did, rawTxt); ok {
				rawTxt = resolved
			} else {
				rawTxt = vaultRefGapMarker
				bodyUnavailable = true
			}
		}
		row := ThreeLayerRow{
			ID:              did,
			Layer:           lyr,
			Status:          "active",
			Title:           clipRunes(rawTxt, 64),
			Content:         rawTxt,
			AddressURI:      addr,
			OccurredAt:      occ,
			TimeText:        formatUnixTime(occ, false),
			BodyUnavailable: bodyUnavailable,
		}
		httpx.Success(c, httpx.WithData(gin.H{"row": row}))
		return
	}

	// 2. Check rm_noetic
	var nid, st, qTxt, aTxt, citedJSON, nAddr, nAsst string
	if db.QueryRow(`SELECT noetic_id, occurred_at, status, query_text, answer_text, cited_unit_ids_json, COALESCE(address_uri, ''), COALESCE(assistant_id, '') FROM rm_noetic WHERE noetic_id = ?`, pk).
		Scan(&nid, &occ, &st, &qTxt, &aTxt, &citedJSON, &nAddr, &nAsst) == nil {
		var cited []string
		_ = json.Unmarshal([]byte(citedJSON), &cited)
		row := ThreeLayerRow{
			ID:           nid,
			Layer:        "noetic",
			Status:       st,
			Title:        qTxt,
			Content:      "Q: " + qTxt + "\nA: " + aTxt,
			AnswerText:   aTxt,
			AddressURI:   nAddr,
			AssistantID:  nAsst,
			CitedUnitIDs: cited,
			OccurredAt:   occ,
			TimeText:     formatUnixTime(occ, false),
		}
		tmp := []ThreeLayerRow{row}
		enrichThreeLayerRowsWithAdoption(dbPath, tmp)
		httpx.Success(c, httpx.WithData(gin.H{"row": tmp[0]}))
		return
	}

	// 3. Check rm_bg_chunks
	var uid, absP, relP, anchor string
	var cIdx int
	if db.QueryRow(`SELECT unit_id, abs_path, rel_path, chunk_index, anchor, occurred_at, address_uri FROM rm_bg_chunks WHERE unit_id = ?`, pk).
		Scan(&uid, &absP, &relP, &cIdx, &anchor, &occ, &addr) == nil {
		preview := fmt.Sprintf("[%s#%s] (%s)", relP, anchor, absP)
		row := ThreeLayerRow{
			ID:         uid,
			Layer:      "background",
			Status:     "active",
			Title:      fmt.Sprintf("%s #%s", relP, anchor),
			AddressURI: addr,
			RelPath:    relP,
			Anchor:     anchor,
			OccurredAt: occ,
			TimeText:   formatUnixTime(occ, false),
		}
		// 分块正文按源文件磁盘现场读取（守护进程正典解析）；只有源文件被移动/删除或
		// 读取引擎未就绪时，才退化为来源位置预览并显式标记正文不可读。
		if body, ok := hydratedLocalDocumentLive(home, uid); ok {
			row.Content = body
		} else {
			row.Content = preview
			row.BodyUnavailable = true
		}
		httpx.Success(c, httpx.WithData(gin.H{"row": row}))
		return
	}

	httpx.Fail(c, httpx.WithMessage("未找到记忆单元: "+pk))
}

// layersDeleteHandler 批量删除指定三层记忆单元（兼容 BuildAdmin TableManagerAPI.delete(pks)），
// 并在删除底层 L1/L2 证据单元时自动级联标记关联的 L3 Noetic 结晶为 stale。
func layersDeleteHandler(c *gin.Context) {
	var req struct {
		PKs        []string `json:"pks"`
		PurgeStale bool     `json:"purge_stale"`
	}
	if !bindJSON(c, &req) {
		return
	}

	home, _ := os.UserHomeDir()
	dbPath := resolveMemoryDBPath(home)
	// 写路径只开「非破坏」可写连接：不再对用户金库执行 ALTER / DROP 等 DDL。
	db, err := openVaultWriteOnly(dbPath)
	if err != nil {
		httpx.Fail(c, httpx.WithMessage("打开本地记忆库失败: "+err.Error()))
		return
	}
	defer db.Close()

	cleanPKs := make([]string, 0, len(req.PKs))
	for _, pk := range req.PKs {
		if s := strings.TrimSpace(pk); s != "" {
			cleanPKs = append(cleanPKs, s)
		}
	}
	if len(cleanPKs) == 0 && !req.PurgeStale {
		httpx.Fail(c, httpx.WithMessage("请选择要删除的记忆单元"))
		return
	}
	deleted, invalidatedNoetic, _, delErr := deleteMemoryUnits(db, cleanPKs, req.PurgeStale)
	if delErr != nil {
		httpx.Fail(c, httpx.WithMessage(delErr.Error()))
		return
	}

	invalidateVaultReadCaches()
	invalidateEnvHealthCache(home)
	httpx.Success(c, httpx.WithData(gin.H{
		"deleted":            deleted,
		"invalidated_noetic": invalidatedNoetic,
	}))
}

// deleteMemoryUnitsByPKs 在本地金库中按主键删除记忆单元，并完整维护谱系行、
// 防同步复活墓碑（rm_deleted_units）、背景文件记账收敛与 L3 引证级联置 stale。
func deleteMemoryUnitsByPKs(db *sql.DB, cleanPKs []string) (deleted int, invalidatedNoetic int, purgedIDs []string, err error) {
	return deleteMemoryUnits(db, cleanPKs, false)
}

func deleteMemoryUnits(db *sql.DB, cleanPKs []string, purgeStale bool) (deleted int, invalidatedNoetic int, purgedIDs []string, err error) {
	tx, err := db.Begin()
	if err != nil {
		return 0, 0, nil, fmt.Errorf("开始删除事务失败: %w", err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`CREATE TABLE IF NOT EXISTS rm_deleted_units (unit_id TEXT PRIMARY KEY, layer TEXT NOT NULL DEFAULT '', deleted_at INTEGER NOT NULL DEFAULT 0)`); err != nil {
		return 0, 0, nil, fmt.Errorf("创建删除墓碑表失败: %w", err)
	}
	if purgeStale {
		rows, qErr := tx.Query(`SELECT noetic_id FROM rm_noetic WHERE COALESCE(NULLIF(status, ''), 'active') <> 'active'`)
		if qErr != nil {
			return 0, 0, nil, fmt.Errorf("读取失效记忆失败: %w", qErr)
		}
		for rows.Next() {
			var id string
			if scanErr := rows.Scan(&id); scanErr != nil {
				_ = rows.Close()
				return 0, 0, nil, fmt.Errorf("读取失效记忆失败: %w", scanErr)
			}
			if strings.TrimSpace(id) != "" {
				purgedIDs = append(purgedIDs, id)
			}
		}
		if rowsErr := rows.Err(); rowsErr != nil {
			_ = rows.Close()
			return 0, 0, nil, fmt.Errorf("读取失效记忆失败: %w", rowsErr)
		}
		if closeErr := rows.Close(); closeErr != nil {
			return 0, 0, nil, fmt.Errorf("关闭失效记忆查询失败: %w", closeErr)
		}
		res, execErr := tx.Exec(`DELETE FROM rm_noetic WHERE COALESCE(NULLIF(status, ''), 'active') <> 'active'`)
		if execErr != nil {
			return 0, 0, nil, fmt.Errorf("清理失效记忆失败: %w", execErr)
		}
		n, countErr := res.RowsAffected()
		if countErr != nil {
			return 0, 0, nil, fmt.Errorf("读取清理数量失败: %w", countErr)
		}
		deleted += int(n)
	}
	nowUnix := time.Now().Unix()
	deletedEvidence := map[string]bool{}
	touchedFiles := map[string]bool{}
	seenPKs := map[string]bool{}
	for _, pk := range cleanPKs {
		if seenPKs[pk] {
			continue
		}
		seenPKs[pk] = true
		var docLayer string
		if scanErr := tx.QueryRow(`SELECT COALESCE(layer, 'episodic') FROM rm_units WHERE doc_id = ?`, pk).Scan(&docLayer); scanErr != nil && !errors.Is(scanErr, sql.ErrNoRows) {
			return 0, 0, nil, fmt.Errorf("读取记忆文档失败: %w", scanErr)
		}
		var exists int
		if scanErr := tx.QueryRow(`SELECT EXISTS(SELECT 1 FROM rm_units WHERE doc_id = ?)`, pk).Scan(&exists); scanErr != nil {
			return 0, 0, nil, fmt.Errorf("读取记忆文档失败: %w", scanErr)
		}
		res, execErr := tx.Exec(`DELETE FROM rm_units WHERE doc_id = ?`, pk)
		if execErr != nil {
			return 0, 0, nil, fmt.Errorf("删除记忆文档失败: %w", execErr)
		}
		n, execErr := res.RowsAffected()
		if execErr != nil {
			return 0, 0, nil, fmt.Errorf("读取删除文档数量失败: %w", execErr)
		}
		if exists != 0 && n == 0 {
			n = 1
		}
		if n > 0 {
			deleted += int(n)
			deletedEvidence[pk] = true
			purgedIDs = append(purgedIDs, pk)
			lyr := strings.ToLower(strings.TrimSpace(docLayer))
			if lyr == "" {
				lyr = "episodic"
			}
			if _, execErr := tx.Exec(`INSERT OR REPLACE INTO rm_deleted_units(unit_id, layer, deleted_at) VALUES (?, ?, ?)`, pk, lyr, nowUnix); execErr != nil {
				return 0, 0, nil, fmt.Errorf("写入删除墓碑失败: %w", execErr)
			}
			for _, table := range []string{"rm_episodic_lineage", "rm_episodic_index", "rm_episodic_sources"} {
				if execErr := deleteOptionalVaultRow(tx, table, "doc_id", pk); execErr != nil {
					return 0, 0, nil, fmt.Errorf("删除记忆索引或谱系失败: %w", execErr)
				}
			}
		}
		res, execErr = tx.Exec(`DELETE FROM rm_noetic WHERE noetic_id = ?`, pk)
		if execErr != nil {
			return 0, 0, nil, fmt.Errorf("删除沉淀记忆失败: %w", execErr)
		}
		n, execErr = res.RowsAffected()
		if execErr != nil {
			return 0, 0, nil, fmt.Errorf("读取删除沉淀数量失败: %w", execErr)
		}
		if n > 0 {
			deleted += int(n)
			deletedEvidence[pk] = true
			purgedIDs = append(purgedIDs, pk)
		}
		// L1 背景分块是「磁盘文件 → 分块」的派生物：删除前先记下其归属文件，
		// 删除后必须把该文件的派生记账（chunk_count / unit_ids_json / chunk_hashes_json）收敛，
		// 否则 rm_bg_files 会一直声称这些分块仍然存在（目录统计虚高、增量同步语义错乱）。
		var ownerAbsPath string
		if scanErr := tx.QueryRow(`SELECT abs_path FROM rm_bg_chunks WHERE unit_id = ?`, pk).Scan(&ownerAbsPath); scanErr != nil && !errors.Is(scanErr, sql.ErrNoRows) {
			return 0, 0, nil, fmt.Errorf("读取背景分块失败: %w", scanErr)
		}
		res, execErr = tx.Exec(`DELETE FROM rm_bg_chunks WHERE unit_id = ?`, pk)
		if execErr != nil {
			return 0, 0, nil, fmt.Errorf("删除背景分块失败: %w", execErr)
		}
		n, execErr = res.RowsAffected()
		if execErr != nil {
			return 0, 0, nil, fmt.Errorf("读取删除分块数量失败: %w", execErr)
		}
		if n > 0 {
			deleted += int(n)
			deletedEvidence[pk] = true
			purgedIDs = append(purgedIDs, pk)
			if _, execErr := tx.Exec(`INSERT OR REPLACE INTO rm_deleted_units(unit_id, layer, deleted_at) VALUES (?, 'background', ?)`, pk, nowUnix); execErr != nil {
				return 0, 0, nil, fmt.Errorf("写入删除墓碑失败: %w", execErr)
			}
			if strings.TrimSpace(ownerAbsPath) != "" {
				touchedFiles[ownerAbsPath] = true
			}
		}
	}
	for absPath := range touchedFiles {
		if execErr := resyncBackgroundFileBookkeeping(tx, absPath); execErr != nil {
			return 0, 0, nil, fmt.Errorf("更新背景文件记录失败: %w", execErr)
		}
	}

	// 若删除了底层 L1/L2/L3 单元，级联将引证它的 L3 Noetic 置为 stale（引证链不再指向失效证据）。
	if len(deletedEvidence) > 0 {
		nRows, qErr := tx.Query(`SELECT noetic_id, cited_unit_ids_json FROM rm_noetic WHERE COALESCE(NULLIF(status, ''), 'active') = 'active'`)
		if qErr != nil {
			return 0, 0, nil, fmt.Errorf("读取引证失败: %w", qErr)
		}
		var toStale []string
		for nRows.Next() {
			var nid, citedJSON string
			if scanErr := nRows.Scan(&nid, &citedJSON); scanErr != nil {
				_ = nRows.Close()
				return 0, 0, nil, fmt.Errorf("读取引证失败: %w", scanErr)
			}
			var cited []string
			if parseErr := json.Unmarshal([]byte(citedJSON), &cited); parseErr != nil {
				_ = nRows.Close()
				return 0, 0, nil, fmt.Errorf("解析引证失败: %w", parseErr)
			}
			for _, cid := range cited {
				if deletedEvidence[strings.TrimSpace(cid)] {
					toStale = append(toStale, nid)
					break
				}
			}
		}
		if rowsErr := nRows.Err(); rowsErr != nil {
			_ = nRows.Close()
			return 0, 0, nil, fmt.Errorf("读取引证失败: %w", rowsErr)
		}
		if closeErr := nRows.Close(); closeErr != nil {
			return 0, 0, nil, fmt.Errorf("关闭引证查询失败: %w", closeErr)
		}
		for _, nid := range toStale {
			if _, uErr := tx.Exec(`UPDATE rm_noetic SET status = 'stale' WHERE noetic_id = ?`, nid); uErr != nil {
				return 0, 0, nil, fmt.Errorf("更新引证状态失败: %w", uErr)
			}
			invalidatedNoetic++
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, 0, nil, fmt.Errorf("提交删除事务失败: %w", err)
	}
	return deleted, invalidatedNoetic, purgedIDs, nil
}

// vaultTableExistsTx 判断薄金库中的可选表是否真实存在（旧库可能缺少某些可选索引表）。
func vaultTableExistsTx(tx *sql.Tx, name string) (bool, error) {
	var exists int
	err := tx.QueryRow(`SELECT EXISTS(SELECT 1 FROM sqlite_master WHERE type = 'table' AND name = ?)`, name).Scan(&exists)
	return exists != 0, err
}

func deleteOptionalVaultRow(tx *sql.Tx, table, column, id string) error {
	exists, err := vaultTableExistsTx(tx, table)
	if err != nil || !exists {
		return err
	}
	_, err = tx.Exec(`DELETE FROM `+table+` WHERE `+column+` = ?`, id)
	return err
}

// resyncBackgroundFileBookkeeping 收敛某个被 Console 改写过分块的 L1 文件的派生记账：
// 以 rm_bg_chunks 的真实剩余集合重算 chunk_count 与 unit_ids_json，并把
// chunk_hashes_json 置空 —— 让引擎在下一次 background sync 重新分块校验，而不是信任一份被
// Console 拼接过的哈希表（L1 背景单元的正确收敛方式永远是从磁盘文件重新派生）。
func resyncBackgroundFileBookkeeping(tx *sql.Tx, absPath string) error {
	rows, err := tx.Query(`SELECT unit_id FROM rm_bg_chunks WHERE abs_path = ? ORDER BY chunk_index ASC, unit_id ASC`, absPath)
	if err != nil {
		return err
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	uidJSON, err := json.Marshal(ids)
	if err != nil {
		return err
	}
	_, err = tx.Exec(
		`UPDATE rm_bg_files SET chunk_count = ?, unit_ids_json = ?, chunk_hashes_json = '[]' WHERE abs_path = ?`,
		len(ids), string(uidJSON), absPath,
	)
	return err
}

// backgroundDirsHandler 返回所有已挂载的本地背景目录及其文件/分块统计与扩展名过滤配置。
func backgroundDirsHandler(c *gin.Context) {
	home, _ := os.UserHomeDir()
	dbPath := resolveMemoryDBPath(home)
	db, err := memoryRODB(dbPath)
	if err != nil {
		// 尚无金库：如实下发空目录清单，绝不在读路径建库建表。
		httpx.Success(c, httpx.WithData([]BackgroundDirItem{}))
		return
	}

	out := []BackgroundDirItem{}
	rows, err := db.Query(`SELECT dir_id, root_path, added_at, COALESCE(include_exts_json, '[]') FROM rm_bg_dirs ORDER BY added_at ASC, dir_id ASC`)
	if err == nil {
		for rows.Next() {
			var item BackgroundDirItem
			var extsJSON string
			if rows.Scan(&item.DirID, &item.RootPath, &item.AddedAt, &extsJSON) == nil {
				item.AddedText = formatUnixTime(item.AddedAt, false)
				item.IncludeExts = []string{}
				if strings.TrimSpace(extsJSON) != "" && extsJSON != "[]" {
					var parsed []string
					if json.Unmarshal([]byte(extsJSON), &parsed) == nil && len(parsed) > 0 {
						item.IncludeExts = parsed
					}
				}
				out = append(out, item)
			}
		}
		rows.Close()
	} else {
		rowsLegacy, errLegacy := db.Query(`SELECT dir_id, root_path, added_at FROM rm_bg_dirs ORDER BY added_at ASC, dir_id ASC`)
		if errLegacy != nil {
			httpx.Fail(c, httpx.WithMessage(errLegacy.Error()))
			return
		}
		defer rowsLegacy.Close()
		for rowsLegacy.Next() {
			var item BackgroundDirItem
			if rowsLegacy.Scan(&item.DirID, &item.RootPath, &item.AddedAt) == nil {
				item.AddedText = formatUnixTime(item.AddedAt, false)
				item.IncludeExts = []string{}
				out = append(out, item)
			}
		}
	}
	for i := range out {
		_ = db.QueryRow(`SELECT COUNT(*), COALESCE(SUM(chunk_count), 0) FROM rm_bg_files WHERE dir_id = ?`, out[i].DirID).
			Scan(&out[i].FileCount, &out[i].ChunkCount)
	}
	httpx.Success(c, httpx.WithData(out))
}

// backgroundFilesHandler 返回挂载目录内的文件级清单（目录 → 文件 → 分块三级浏览的中间层，只读）。
func backgroundFilesHandler(c *gin.Context) {
	var req struct {
		DirID   string `json:"dir_id"` // 为空时返回全部挂载目录的文件
		Keyword string `json:"keyword"`
		Limit   int    `json:"limit"`
	}
	if !bindJSON(c, &req) {
		return
	}
	if req.Limit <= 0 || req.Limit > 500 {
		req.Limit = 200
	}
	dirFilter := strings.TrimSpace(req.DirID)
	kw := strings.ToLower(strings.TrimSpace(req.Keyword))

	home, _ := os.UserHomeDir()
	dbPath := resolveMemoryDBPath(home)
	if err := checkVaultReadable(dbPath); err != nil {
		httpx.Fail(c, httpx.WithMessage("读取本地记忆库失败: "+err.Error()))
		return
	}
	// 只读清单：关键词过滤必须发生在 SQL（LIMIT 之前）。早期实现先 LIMIT 再在 Go 里过滤，
	// 超过 limit 的匹配文件会被静默丢弃 —— 用户搜文件名查不到明明存在的文件。
	rows, err := sqliteQueryJSONArgs(dbPath, `
		SELECT abs_path, dir_id, rel_path, file_size, mtime_ns, chunk_count
		FROM rm_bg_files
		WHERE (? = '' OR dir_id = ?)
		  AND (? = '' OR instr(lower(rel_path || ' ' || abs_path), ?) > 0)
		ORDER BY rel_path ASC, abs_path ASC
		LIMIT ?
	`, dirFilter, dirFilter, kw, kw, req.Limit)
	if err != nil {
		if !vaultTablePresent(dbPath, "rm_bg_files") {
			httpx.Success(c, httpx.WithData([]BackgroundFileItem{}))
			return
		}
		httpx.Fail(c, httpx.WithMessage("查询背景文件失败: "+err.Error()))
		return
	}

	out := []BackgroundFileItem{}
	for _, r := range rows {
		item := BackgroundFileItem{
			AbsPath:    asString(r["abs_path"]),
			DirID:      asString(r["dir_id"]),
			RelPath:    asString(r["rel_path"]),
			FileSize:   int64(asInt(r["file_size"])),
			ChunkCount: asInt(r["chunk_count"]),
		}
		var mtimeNS int64
		switch v := r["mtime_ns"].(type) {
		case int64:
			mtimeNS = v
		case int:
			mtimeNS = int64(v)
		case float64:
			mtimeNS = int64(v)
		}
		item.SizeText = formatByteSize(item.FileSize)
		if mtimeNS > 0 {
			item.UpdatedAt = mtimeNS / int64(time.Second)
		}
		item.UpdatedText = formatUnixTime(item.UpdatedAt, false)
		out = append(out, item)
	}
	httpx.Success(c, httpx.WithData(out))
}

// formatByteSize 人类可读的字节数文本（纯展示格式化，不改动任何数值语义）。
func formatByteSize(size int64) string {
	if size <= 0 {
		return "0 B"
	}
	units := []string{"B", "KiB", "MiB", "GiB"}
	value := float64(size)
	idx := 0
	for value >= 1024 && idx < len(units)-1 {
		value /= 1024
		idx++
	}
	if idx == 0 {
		return fmt.Sprintf("%d %s", size, units[idx])
	}
	return fmt.Sprintf("%.1f %s", value, units[idx])
}

type bgIncludeExtsCtxKey struct{}

func withBackgroundIncludeExts(ctx context.Context, exts []string) context.Context {
	if exts == nil {
		return ctx
	}
	cleaned := make([]string, 0, len(exts))
	for _, raw := range exts {
		s := strings.ToLower(strings.TrimSpace(raw))
		if s == "" {
			continue
		}
		if s == "*" || s == "all" {
			return context.WithValue(ctx, bgIncludeExtsCtxKey{}, []string{"*"})
		}
		if !strings.HasPrefix(s, ".") {
			s = "." + s
		}
		cleaned = append(cleaned, s)
	}
	if len(cleaned) == 0 {
		return context.WithValue(ctx, bgIncludeExtsCtxKey{}, []string{"*"})
	}
	return context.WithValue(ctx, bgIncludeExtsCtxKey{}, cleaned)
}

func bgIncludeExtsFromContext(ctx context.Context) []string {
	if ctx == nil {
		return nil
	}
	v, _ := ctx.Value(bgIncludeExtsCtxKey{}).([]string)
	return v
}

// backgroundAddHandler 挂载一个新的本地目录（含其全部嵌套子目录）作为 L1 Background 数据源。
// 挂载决策（同一物理目录幂等、嵌套挂载覆盖提示、文档类型过滤）与同步统一复用根仓 CLI，避免在 Admin 模块复制记忆算法。
func backgroundAddHandler(c *gin.Context) {
	var req struct {
		Path        string   `json:"path"`
		IncludeExts []string `json:"include_exts"`
	}
	if !bindJSON(c, &req) {
		return
	}
	if strings.TrimSpace(req.Path) == "" {
		httpx.Fail(c, httpx.WithMessage("请输入有效的本地目录路径"))
		return
	}
	cleanRoot, err := canonicalBackgroundDirPath(req.Path)
	if err != nil {
		httpx.Fail(c, httpx.WithMessage(err.Error()))
		return
	}

	home, _ := os.UserHomeDir()
	dbPath := resolveMemoryDBPath(home)
	callCtx := withBackgroundIncludeExts(c.Request.Context(), req.IncludeExts)
	out, err := executeAdminBackgroundCLI(callCtx, "add", dbPath, cleanRoot)
	invalidateVaultReadCaches()
	invalidateEnvHealthCache(home)
	if err != nil {
		httpx.Fail(c, httpx.WithMessage("目录已保留供重试，但文档同步失败: "+err.Error()))
		return
	}
	var payload struct {
		Mount struct {
			DirID       string   `json:"dir_id"`
			RootPath    string   `json:"root_path"`
			Notice      string   `json:"notice"`
			IncludeExts []string `json:"include_exts"`
		} `json:"mount"`
		Sync BackgroundSyncResult `json:"sync"`
	}
	if err := json.Unmarshal(out, &payload); err != nil {
		httpx.Fail(c, httpx.WithMessage("解析 background add JSON 失败: "+err.Error()))
		return
	}
	includeExts := payload.Mount.IncludeExts
	if includeExts == nil {
		includeExts = []string{}
	}
	httpx.Success(c, httpx.WithData(gin.H{
		"dir_id":       payload.Mount.DirID,
		"root_path":    payload.Mount.RootPath,
		"notice":       payload.Mount.Notice,
		"include_exts": includeExts,
		"sync":         payload.Sync,
	}))
}

// backgroundRemoveHandler 先清除云端背景单元，再解挂本地目录并清理派生分块。
func backgroundRemoveHandler(c *gin.Context) {
	var req struct {
		Path  string `json:"path"`
		DirID string `json:"dir_id"`
	}
	if !bindJSON(c, &req) {
		return
	}
	home, _ := os.UserHomeDir()
	dbPath := resolveMemoryDBPath(home)
	cleanRoot := strings.TrimSpace(req.Path)
	targetDirID := strings.TrimSpace(req.DirID)

	if cleanRoot == "" && targetDirID != "" {
		db, err := openVaultWriteOnly(dbPath)
		if err != nil {
			httpx.Fail(c, httpx.WithMessage(err.Error()))
			return
		}
		err = db.QueryRow(`SELECT root_path FROM rm_bg_dirs WHERE dir_id = ?`, targetDirID).Scan(&cleanRoot)
		_ = db.Close()
		if err != nil {
			httpx.Fail(c, httpx.WithMessage("未找到待解挂目录: "+err.Error()))
			return
		}
	}
	if cleanRoot == "" {
		httpx.Fail(c, httpx.WithMessage("待解挂目录路径不能为空"))
		return
	}

	removal, err := runAdminBackgroundDirRemoveReport(c.Request.Context(), dbPath, cleanRoot)
	invalidateVaultReadCaches()
	invalidateEnvHealthCache(home)
	if err != nil {
		httpx.Fail(c, httpx.WithMessage("目录仍保留供重试，云端 Purge 或本地解挂失败: "+err.Error()))
		return
	}
	httpx.Success(c, httpx.WithData(gin.H{
		"removed_chunks":   len(removal.RemovedUnitIDs),
		"removed_unit_ids": removal.RemovedUnitIDs,
		"kept_files":       removal.KeptFiles,
	}))
}

// backgroundSyncHandler 触发全量挂载目录的正文读取、真实分块与盲化脉冲增量同步。
func backgroundSyncHandler(c *gin.Context) {
	home, _ := os.UserHomeDir()
	dbPath := resolveMemoryDBPath(home)
	res, err := runAdminBackgroundDirsSync(c.Request.Context(), dbPath)
	invalidateVaultReadCaches()
	invalidateEnvHealthCache(home)
	if err != nil {
		httpx.Fail(c, httpx.WithMessage("文档同步失败: "+err.Error()))
		return
	}
	httpx.Success(c, httpx.WithData(res))
}

var executeAdminBackgroundCLI = runAdminBackgroundCLI

func runAdminBackgroundDirsSync(ctx context.Context, dbPath string) (BackgroundSyncResult, error) {
	res := BackgroundSyncResult{InvalidatedNoeticIDs: []string{}}
	out, err := executeAdminBackgroundCLI(ctx, "sync", dbPath, "")
	if err != nil {
		return res, err
	}
	if err := json.Unmarshal(out, &res); err != nil {
		return res, fmt.Errorf("解析 background sync JSON 失败: %w", err)
	}
	return res, nil
}

// backgroundDirRemovalResult 解挂结果：真正回收的云端单元，以及因仍被其他挂载目录覆盖而保留归属的文件数。
type backgroundDirRemovalResult struct {
	RemovedUnitIDs []string
	KeptFiles      int
}

func runAdminBackgroundDirRemoveReport(ctx context.Context, dbPath, rootPath string) (backgroundDirRemovalResult, error) {
	out, err := executeAdminBackgroundCLI(ctx, "remove", dbPath, rootPath)
	if err != nil {
		return backgroundDirRemovalResult{}, err
	}
	var payload struct {
		RemovedUnitIDs []string `json:"removed_unit_ids"`
		KeptFiles      *int     `json:"kept_files"`
	}
	if err := json.Unmarshal(out, &payload); err != nil {
		return backgroundDirRemovalResult{}, fmt.Errorf("解析 background remove JSON 失败: %w", err)
	}
	result := backgroundDirRemovalResult{RemovedUnitIDs: payload.RemovedUnitIDs}
	if payload.KeptFiles != nil {
		result.KeptFiles = *payload.KeptFiles
	}
	return result, nil
}

func runAdminBackgroundDirRemove(ctx context.Context, dbPath, rootPath string) ([]string, error) {
	result, err := runAdminBackgroundDirRemoveReport(ctx, dbPath, rootPath)
	if err != nil {
		return nil, err
	}
	return result.RemovedUnitIDs, nil
}

// runAdminBackgroundCLI 复用根仓 localvault，避免在独立 Admin 模块复制记忆算法。
func runAdminBackgroundCLI(ctx context.Context, action, dbPath, rootPath string) ([]byte, error) {
	home, _ := os.UserHomeDir()
	bin := resolveBinary(home)
	if bin == "" {
		return nil, fmt.Errorf("未找到 rolling-memory 引擎二进制")
	}
	childEnv := stripCloudBrainEnv(os.Environ())

	args := []string{"background", action, "--db", dbPath, "--json"}
	if strings.TrimSpace(rootPath) != "" {
		args = append(args, "--path", rootPath)
	}
	if action == "add" {
		if exts := bgIncludeExtsFromContext(ctx); len(exts) > 0 {
			args = append(args, "--ext", strings.Join(exts, ","))
		}
	}
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Env = childEnv
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err == nil {
		return out, nil
	}
	var failure struct {
		Error string `json:"error"`
	}
	if json.Unmarshal(out, &failure) == nil && strings.TrimSpace(failure.Error) != "" {
		return out, fmt.Errorf("background %s: %s", action, failure.Error)
	}
	if detail := strings.TrimSpace(stderr.String()); detail != "" {
		return out, fmt.Errorf("background %s: %s", action, detail)
	}
	return out, fmt.Errorf("background %s: %w", action, err)
}

var executeAdminSyncCLI = runAdminSyncCLI

// detectActiveHarnessLabel reads the live attachment ledger and returns the
// display name of the session that currently holds a lease. No live lease
// means "unknown": a stale document, a transcript timestamp, or another
// host's last write must never be presented as the current environment.
func detectActiveHarnessLabel(home string) string {
	attachment, ok := livePresenceAttachment(home)
	if !ok {
		return ""
	}
	if label := strings.TrimSpace(attachment.Label); label != "" {
		return label
	}
	return strings.TrimSpace(attachment.Host)
}

// rollingMemoryStateDir mirrors the engine's default state directory so the
// console and the CLI read the same attachment document under XDG overrides.
func rollingMemoryStateDir(home string) string {
	if xdg := strings.TrimSpace(os.Getenv("XDG_STATE_HOME")); xdg != "" {
		return filepath.Join(xdg, "rolling-memory")
	}
	return filepath.Join(home, ".local", "state", "rolling-memory")
}

// rollingMemoryLogPath is the default engine log path, used by the console's
// activity watcher. A custom log location still wins in the engine config; the
// console's fallback follows the same default as config.DefaultStateDir.
func rollingMemoryLogPath(home string) string {
	return filepath.Join(rollingMemoryStateDir(home), "rolling-memory.log")
}

type presenceAttachment struct {
	Host       string `json:"host"`
	Label      string `json:"label"`
	SessionID  string `json:"session_id"`
	Locus      string `json:"locus"`
	Evidence   string `json:"evidence"`
	Mode       string `json:"mode"`
	LastSeenAt int64  `json:"last_seen_at"`
	LeaseUntil int64  `json:"lease_until"`
}

type presenceLedger struct {
	Version  int                           `json:"version"`
	Sessions map[string]presenceAttachment `json:"sessions"`
}

// livePresenceAttachment returns the most recently active session whose lease
// has not expired. The console only needs the summary fields; the schema is
// intentionally small and version-tolerant.
func livePresenceAttachment(home string) (presenceAttachment, bool) {
	raw, err := os.ReadFile(filepath.Join(rollingMemoryStateDir(home), "active_workspace.json"))
	if err != nil {
		return presenceAttachment{}, false
	}
	var doc presenceLedger
	if json.Unmarshal(raw, &doc) != nil || len(doc.Sessions) == 0 {
		return presenceAttachment{}, false
	}
	now := time.Now().Unix()
	best := presenceAttachment{}
	found := false
	for _, attachment := range doc.Sessions {
		if attachment.LeaseUntil < now {
			continue
		}
		if !found || attachment.LastSeenAt > best.LastSeenAt {
			best = attachment
			found = true
		}
	}
	return best, found
}

// envHealthHandler 返回当前运行环境（Harness）检测、记忆健康度评分与三层（Episodic / Background / Noetic）本地 vs 远端对齐状态。
// 实时刷新由 WebSocket rm/env_health 共享房间承担；本端点保留为一次性取值（页面首屏 / 手动刷新）。
// 空 harness 解析为当前 Harness 过滤扫描（与实时流 /「重新检测」同一口径）：
// 全量跨 Harness 扫描约 10–23s、单 Harness 约 1s，而两者载荷逐字段一致（仅 updated_at 不同）；
// 本端点被四个记忆页的首屏挂载与「同步 / 对齐」后的显式刷新反复调用，全量扫描正是不带进度帧的
// 一次性等待把页面拖住的元凶。全量诊断由 CLI `sync status` 直连与上层显式路径承担。
func envHealthHandler(c *gin.Context) {
	var req struct {
		Harness string `json:"harness"`
		// Force 由用户显式「重新检测」路径携带：跳过 TTL 复用，立刻重跑一次真实采集。
		Force bool `json:"force"`
	}
	if !bindJSON(c, &req) {
		return
	}
	home, _ := os.UserHomeDir()
	harness := strings.TrimSpace(req.Harness)
	if harness == "" {
		harness = activeHarnessID(home)
	}
	payload, err := cachedEnvHealthPayload(c.Request.Context(), home, harness, req.Force)
	if err != nil {
		httpx.Fail(c, httpx.WithMessage(err.Error()))
		return
	}
	httpx.Success(c, httpx.WithData(payload))
}

// collectEnvHealthPayload 采集 Harness 检测 + 健康度评分 + 三层对齐状态（`sync status --json` 子进程），
// 并叠加控制台侧同步开关：一次性端点与实时流共用同一采集口径。
// harness 非空时透传给子进程做历史扫描过滤——本机实测全量扫描约 22.9s、单 Harness 过滤约 0.9s，载荷内容一致。
func collectEnvHealthPayload(ctx context.Context, home, harness string) (map[string]any, error) {
	dbPath := resolveMemoryDBPath(home)

	out, err := executeAdminSyncCLI(ctx, "status", dbPath, harness, "", 0)
	if err != nil {
		return nil, fmt.Errorf("环境与记忆健康诊断失败: %w", err)
	}
	var payload map[string]any
	if err := json.Unmarshal(out, &payload); err != nil {
		return nil, fmt.Errorf("解析诊断 JSON 失败: %w", err)
	}
	sw := parseConfigSwitches(filepath.Join(home, ".config", "rolling-memory", "config.toml"), true)
	payload["auto_sync_enabled"] = sw.SyncAutoWatch
	payload["delivery_window"] = sw.DeliveryWindow
	if sw.SyncAutoWatch {
		payload["sync_mode"] = "auto"
	} else {
		payload["sync_mode"] = "manual"
	}
	return payload, nil
}

// syncReconcileHandler 执行三层记忆（all | episodic | background | noetic）与当前运行环境（Harness 历史对话）的手动对齐同步。
func syncReconcileHandler(c *gin.Context) {
	var req struct {
		Action  string `json:"action"` // "" | "reconcile" | "step" | "pause" | "resume"
		Layer   string `json:"layer"`
		Harness string `json:"harness"`
		Limit   int    `json:"limit"`
	}
	if !bindJSON(c, &req) {
		return
	}
	home, _ := os.UserHomeDir()
	// 同步 / 暂停 / 推进都会改写对齐状态：完成后丢弃环境健康读数缓存，页面随即读到新的待同步计数。
	defer invalidateEnvHealthCache(home)
	dbPath := resolveMemoryDBPath(home)
	act := strings.ToLower(strings.TrimSpace(req.Action))
	switch act {
	case "pause", "resume", "step":
		out, err := executeAdminSyncCLI(c.Request.Context(), act, dbPath, "", "", 0)
		if err != nil {
			httpx.Fail(c, httpx.WithMessage("执行同步控制失败: "+err.Error()))
			return
		}
		var payload map[string]any
		if err := json.Unmarshal(out, &payload); err != nil {
			httpx.Fail(c, httpx.WithMessage("解析同步控制 JSON 失败: "+err.Error()))
			return
		}
		httpx.Success(c, httpx.WithData(payload))
		return
	case "", "reconcile":
		// 主路径：对齐同步（控制台各页「同步」按钮不传 action，默认即 reconcile）。
	default:
		httpx.Fail(c, httpx.WithMessage("未知同步动作: "+req.Action+"；仅支持 reconcile / step / pause / resume"))
		return
	}
	layer := strings.ToLower(strings.TrimSpace(req.Layer))
	if layer == "" {
		layer = "all"
	}
	limit := req.Limit
	if limit <= 0 || limit > 200 {
		limit = 50
	}

	out, err := executeAdminSyncCLI(c.Request.Context(), "reconcile", dbPath, strings.TrimSpace(req.Harness), layer, limit)
	if err != nil {
		httpx.Fail(c, httpx.WithMessage("执行记忆对齐同步失败: "+err.Error()))
		return
	}
	var payload map[string]any
	if err := json.Unmarshal(out, &payload); err != nil {
		httpx.Fail(c, httpx.WithMessage("解析同步报告 JSON 失败: "+err.Error()))
		return
	}
	// CLI 的 reconcile 报告恒附带一条扫描窗口范围说明（供 CLI JSON 契约断言），
	// 它不是错误或异常；在管理后台 API 侧直接剥离，避免任何前端（含未刷新的旧缓存页）弹出黄色警告。
	if rawWarnings, ok := payload["warnings"].([]any); ok {
		filtered := make([]string, 0, len(rawWarnings))
		for _, w := range rawWarnings {
			s := strings.TrimSpace(asString(w))
			if s == "" || strings.Contains(s, "episodic index backfill covers only the scanned harness window") {
				continue
			}
			filtered = append(filtered, s)
		}
		if len(filtered) > 0 {
			payload["warnings"] = filtered
		} else {
			delete(payload, "warnings")
		}
	}
	// reconcile 子进程结尾已执行过一次完整的 inspectEnvHealth 并装入 status：
	// 直接补齐控制台同步开关并广播给实时流订阅者，前端可直接采用 status 而无需再起第二个 status 子进程。
	if statusMap, ok := payload["status"].(map[string]any); ok && statusMap != nil {
		sw := parseConfigSwitches(filepath.Join(home, ".config", "rolling-memory", "config.toml"), true)
		statusMap["auto_sync_enabled"] = sw.SyncAutoWatch
		if sw.SyncAutoWatch {
			statusMap["sync_mode"] = "auto"
		} else {
			statusMap["sync_mode"] = "manual"
		}
		storeCachedEnvHealthPayload(home, activeHarnessID(home), statusMap)
		if hub := streamHubPeek(streamKey{kind: streamKindEnvHealth, harness: ""}); hub != nil && hub.hasSubscribers() {
			hub.publishStateFrame("env_health", envHealthPayloadSignature(statusMap), statusMap)
		}
	}
	httpx.Success(c, httpx.WithData(payload))
}

func runAdminSyncCLI(ctx context.Context, action, dbPath, harnessID, layer string, limit int) ([]byte, error) {
	home, _ := os.UserHomeDir()
	bin := resolveBinary(home)
	if bin == "" {
		return nil, fmt.Errorf("未找到 rolling-memory 引擎二进制")
	}
	// status 是页面加载路径（env/health），历史上曾因引擎全量扫描卡住数分钟把整个
	// 面板拖死；给它一个硬上限，把同类慢化从「页面无限转圈」变成「快速可诊断报错」。
	// 其余动作（reconcile/暂停/继续/单步）是用户显式长操作，保持请求上下文不设限。
	if action == "status" {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, 45*time.Second)
		defer cancel()
	}
	// 子进程零云端覆盖变量：不注入任何远端端点/凭据，并剥掉可能从宿主继承的
	// BRAIN_URL/BRAIN_KEY —— 否则子进程会把开发期覆盖当成云端开关，把本地读写打成远端请求。
	childEnv := stripCloudBrainEnv(os.Environ())
	// The console is a machine-wide reader. It must never inherit the server
	// process's own shell identity as the user's current host.
	childEnv = append(childEnv, "ROLLING_MEMORY_CONSOLE_STATUS=1")
	if action == "status" && envHealthProgressSink(ctx) != nil {
		// 仅 env/health 流式采集请求进度事件（CLI 侧默认静默）：一次性端点与直连 CLI 行为不变。
		childEnv = append(childEnv, statusProgressEnv+"=1")
	}

	args := []string{"sync", action, "--db", dbPath, "--json"}
	if action == "pause" || action == "resume" || action == "step" {
		args = []string{"sync", "watch", "--" + action, "--db", dbPath, "--json"}
	}
	if strings.TrimSpace(harnessID) != "" {
		args = append(args, "--harness", strings.TrimSpace(harnessID))
	}
	if action == "reconcile" {
		if strings.TrimSpace(layer) != "" {
			args = append(args, "--layer", strings.TrimSpace(layer))
		}
		if limit > 0 {
			args = append(args, "--limit", fmt.Sprintf("%d", limit))
		}
	}
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Env = childEnv
	if action == "status" {
		if sink := envHealthProgressSink(ctx); sink != nil {
			return runAdminSyncCLIStreaming(cmd, sink)
		}
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	return finishAdminSyncCLIResult(action, out, stderr.String(), err)
}

// finishAdminSyncCLIResult 把子进程的产出/诊断归一到与 cmd.Output() 相同的语义：
// 退出码为零直接返回 stdout；否则先取 stdout 内的 JSON error 字段，再退回 stderr 文本。
func finishAdminSyncCLIResult(action string, out []byte, stderrText string, err error) ([]byte, error) {
	if err == nil {
		return out, nil
	}
	var failure struct {
		Error string `json:"error"`
	}
	if json.Unmarshal(out, &failure) == nil && strings.TrimSpace(failure.Error) != "" {
		return out, fmt.Errorf("sync %s: %s", action, failure.Error)
	}
	if detail := strings.TrimSpace(stderrText); detail != "" {
		return out, fmt.Errorf("sync %s: %s", action, detail)
	}
	return out, fmt.Errorf("sync %s: %w", action, err)
}

// stripCloudBrainEnv 从子进程环境中剔除云端记忆面覆盖变量（BRAIN_URL / BRAIN_KEY）：
// 控制台只读写本地明文金库，派生出的 `rolling-memory` 子进程不可能被宿主环境误开云端通道
// （子进程零云端覆盖变量的纵深防线）。
func stripCloudBrainEnv(env []string) []string {
	out := make([]string, 0, len(env))
	for _, kv := range env {
		if strings.HasPrefix(kv, "ROLLING_MEMORY_BRAIN_URL=") || strings.HasPrefix(kv, "ROLLING_MEMORY_BRAIN_KEY=") {
			continue
		}
		out = append(out, kv)
	}
	return out
}
