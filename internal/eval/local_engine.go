// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package eval

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"unicode"

	"github.com/rocareer/rolling-memory/internal/config"
	core "github.com/rocareer/rolling-memory/internal/core"
	"github.com/rocareer/rolling-memory/internal/evalns"
	"github.com/rocareer/rolling-memory/internal/ids"
	"github.com/rocareer/rolling-memory/internal/memory/localvault"
	local "github.com/rocareer/rolling-memory/sdk/go/local"
	plain "github.com/rocareer/rolling-memory/sdk/go/pulse/plain"
	_ "modernc.org/sqlite"
)

const (
	// LocalPlainTransport identifies the local plaintext evaluation transport.
	LocalPlainTransport = "ramesdk.local_plain.offline-v1"

	// 现役唯一评测脉冲模式 = 本地明文（local_plain）：本地 SQLite 明文金库 +
	// 离线双簇检索，零云端请求。本项目不存在任何云端脉冲模式。
	PulseModeLocalPlain = local.ModeLocalPlain

	// defaultEvalUserID 是未配置用户命名空间时的默认租户（scope 前缀派生为 u10，
	// 与历史默认一致）。仅用于 scope 前缀派生，不代表任何云端身份。
	defaultEvalUserID = "10"
)

// IsValidEvalTransport 判定 manifest / summary 的 transport 是否属于合法的评测传输通道。
// 唯一合法通道 = 本地明文。
func IsValidEvalTransport(tr string) bool {
	return strings.TrimSpace(tr) == LocalPlainTransport
}

// 盲态脉冲的切词器（本地评测臂的三种诊断切词）：
//   - PulseTokenizerProduct（默认）：产品同配切词器——128k Byte-Level BPE 纯子串分词 + 伴生词展开
//     （CanonicalCompanionTags）+ 结构化日期信标 + 单位/谐波装饰，与产品金库逐位同源；
//   - PulseTokenizerStandard：SDK 原生表面词 + 子词/谐波切词（对照验证臂）；
//   - PulseTokenizerTrigram：按 trigram 滑窗（3 字符窗口，`plain.FTS5TrigramSegmenter`）切词的对照臂。
//
// 三臂都只消费本地金库检索返回的 UnitID：绝不与其它切词臂的结果做并集、融合或补足。
const (
	PulseTokenizerProduct  = "product"
	PulseTokenizerTrigram  = "trigram"
	PulseTokenizerStandard = "standard"
)

// 本地明文索引/召回面：
//   - product：唯一默认面，与用户日常使用的产品金库完全同源（闭集谐波/单位/地理表全开、
//     RB 原生伴随词展开 plain.CanonicalCompanionTags、localvault.RememberEpisodic 摄入与
//     localvault.RecallWithOptions 召回）；
//   - eval：仅用于显式 `--pulse-tokenizer standard|trigram` 的对照切词实验。
const (
	LocalIndexSurfaceProduct = "product"
	LocalIndexSurfaceEval    = "eval"
)

// LocalEngineOption 是构造 LocalEngine 的可选项（保持既有调用点源码兼容）。
type LocalEngineOption func(*localEngineConfig)

type localEngineConfig struct {
	pulseTokenizer string
}

// WithPulseTokenizer 指定盲态脉冲切词器（空/未知值回落到 PulseTokenizerProduct 默认臂）。
func WithPulseTokenizer(mode string) LocalEngineOption {
	return func(cfg *localEngineConfig) {
		cfg.pulseTokenizer = normalizePulseTokenizer(mode)
	}
}

func normalizePulseTokenizer(mode string) string {
	low := strings.ToLower(strings.TrimSpace(mode))
	switch low {
	case PulseTokenizerStandard, "native", "default":
		return PulseTokenizerStandard
	case PulseTokenizerTrigram:
		return PulseTokenizerTrigram
	default:
		return PulseTokenizerProduct
	}
}

// packBPELexemeHarmonicSentence 将单句明文编码为标准词元装饰脉冲帧。
func packBPELexemeHarmonicSentence(enc *plain.BlindEncoder, sentence string, _ int) []plain.BlindTokenAtom {
	if enc == nil {
		enc = plain.NewBlindEncoder(plain.DemoSecret)
	}
	clone := enc.CloneWithPepper(nil)
	clone.SetCanonicalHarmonics(plain.CanonicalHarmonicOptions{
		EnableStructuredDates: true,
		Expander:              plain.CanonicalCompanionTags,
	})
	return clone.PackSentenceTerms(sentence)
}

var packLocalOfflineShannonSentence = packBPELexemeHarmonicSentence

func applyPulseTokenizer(store *local.JSONFileStore, mode string, secret []byte) {
	if store == nil || store.BlindEncoder == nil {
		return
	}
	kenc := plain.NewBlindEncoder(secret)
	norm := normalizePulseTokenizer(mode)
	switch norm {
	case PulseTokenizerStandard:
		kenc.SetCanonicalHarmonics(plain.CanonicalHarmonicOptions{})
		kenc.SetSegmenterOptions(plain.SegmenterPackOptions{})
		store.BlindEncoder.SetCanonicalHarmonics(local.CanonicalHarmonicOptions{})
		store.BlindEncoder.SetSegmenterOptions(local.SegmenterPackOptions{})
	case PulseTokenizerTrigram:
		kenc.SetCanonicalHarmonics(plain.CanonicalHarmonicOptions{EnableStructuredDates: true})
		kenc.SetSegmenterOptions(plain.SegmenterPackOptions{Segmenter: plain.FTS5TrigramSegmenter, HarmonicOverlay: true})
		store.BlindEncoder.SetCanonicalHarmonics(local.CanonicalHarmonicOptions{EnableStructuredDates: true})
		store.BlindEncoder.SetSegmenterOptions(local.SegmenterPackOptions{
			Segmenter:       plain.FTS5TrigramSegmenter,
			HarmonicOverlay: true,
		})
	default:
		kenc.SetCanonicalHarmonics(plain.CanonicalHarmonicOptions{
			EnableStructuredDates: true,
			Expander:              plain.CanonicalCompanionTags,
		})
		kenc.SetSegmenterOptions(plain.SegmenterPackOptions{HarmonicOverlay: true})
		store.BlindEncoder.SetCanonicalHarmonics(local.CanonicalHarmonicOptions{
			EnableStructuredDates: true,
			Expander:              plain.CanonicalCompanionTags,
		})
		store.BlindEncoder.SetSegmenterOptions(local.SegmenterPackOptions{HarmonicOverlay: true})
	}
}

// EvalDocument is one source-faithful benchmark unit.
//
// Beyond the four mandatory fields it may carry plaintext-free spatial/structural
// metadata (speaker, tri-temporal, 4D spatial coordinates) plus high-resolution free-block
// slices. All optional fields are omitempty: a document without them digests and
// packs exactly as before, so historical manifests keep their meaning.
//
// 一个 EvalDocument 恒定对应本地金库恰好一个 UnitID：高分辨率子块（SubUnits /
// FileParse）在打包父单元时并入其句帧，绝不另开子 UnitID——这样 store 权威全集
// 与 corpus 一一相等（VerifyPrepared 精确通过、零 foreign unit），且 top_k 名额
// 不会被同一文档的子块重复挤占。
type EvalDocument struct {
	ID         string `json:"id"`
	Text       string `json:"text"`
	OccurredAt int64  `json:"occurred_at"`
	Layer      string `json:"layer"`
	// Author 是说话人/署名（仅用于本地盲态作者承诺 author_h，不发明文外泄）。
	Author string `json:"author,omitempty"`
	// NarratedAt 是叙述时刻（与 OccurredAt 分离的三时态之二）。
	NarratedAt int64 `json:"narrated_at,omitempty"`
	// DocKey / FileURL 标识同源物理文档（同文档切片共享同一条 DocHash 承诺）。
	DocKey  string `json:"doc_key,omitempty"`
	FileURL string `json:"file_url,omitempty"`
	// BlockKey / BlockID 标识文档内的逻辑块（条目级/章节级标识）。
	BlockKey string `json:"block_key,omitempty"`
	BlockID  string `json:"block_id,omitempty"`
	// LineStart / LineEnd 是该切片在源文档中的 1-based 物理行跨度。
	LineStart int `json:"line_start,omitempty"`
	LineEnd   int `json:"line_end,omitempty"`
	// FileParse 是解析器给出的附加文本（图片描述/观察/附件），随父单元一并编码。
	FileParse string `json:"file_parse,omitempty"`
	// SubUnits 是同一单元内的高分辨率自由块切片（结构化子字段），并入父单元句帧。
	SubUnits []local.SubUnit `json:"sub_units,omitempty"`
}

// UnitMeta 把 EvalDocument 的可选元数据转换为 SDK 的 UnitMeta。
// 空值保持为零值，只有真正提供的字段才会改变脉冲包（与历史行为逐位兼容）。
func (d EvalDocument) UnitMeta() local.UnitMeta {
	fileURL := strings.TrimSpace(d.FileURL)
	if fileURL == "" {
		fileURL = strings.TrimSpace(d.DocKey)
	}
	blockID := strings.TrimSpace(d.BlockID)
	if blockID == "" {
		blockID = strings.TrimSpace(d.BlockKey)
	}
	lineEnd := d.LineEnd
	if d.LineStart > 0 && lineEnd < d.LineStart {
		lineEnd = d.LineStart
	}
	return local.UnitMeta{
		Author:     strings.TrimSpace(d.Author),
		OccurredAt: d.OccurredAt,
		NarratedAt: d.NarratedAt,
		FileURL:    fileURL,
		BlockID:    blockID,
		LineStart:  d.LineStart,
		LineEnd:    lineEnd,
	}
}

// HasStructuredMeta reports whether the document carries any optional spatial,
// speaker, tri-temporal or free-block metadata beyond the four mandatory fields.
func (d EvalDocument) HasStructuredMeta() bool {
	if strings.TrimSpace(d.Author) != "" || d.NarratedAt > 0 || d.LineStart > 0 || d.LineEnd > 0 ||
		strings.TrimSpace(d.FileParse) != "" || len(d.SubUnits) > 0 {
		return true
	}
	return strings.TrimSpace(d.FileURL) != "" || strings.TrimSpace(d.DocKey) != "" ||
		strings.TrimSpace(d.BlockID) != "" || strings.TrimSpace(d.BlockKey) != ""
}

// PackedText 返回真正进入父单元脉冲的文本：源文本 + 解析附件 + 高分辨率自由块。
// 去重判据是纯字符串包含（不含任何语义词表），已出现在正文/前序块中的片段不重复追加。
func (d EvalDocument) PackedText() string {
	base := strings.TrimSpace(d.Text)
	blocks := make([]string, 0, len(d.SubUnits)+2)
	blocks = append(blocks, base)
	appendBlock := func(raw string) {
		text := strings.TrimSpace(raw)
		if text == "" {
			return
		}
		kept := make([]string, 0, len(blocks)+1)
		for _, existing := range blocks {
			if strings.Contains(existing, text) {
				// 已被现有块完整覆盖：不重复追加。
				return
			}
			if strings.Contains(text, existing) {
				// 新块覆盖了现有块：用新块替换，避免同段内容出现两次。
				continue
			}
			kept = append(kept, existing)
		}
		// 若多行块整体未出现，但其中部分行已存在于正文中（如 header 块含 Subject+Summary，而 Summary 已在正文），
		// 仅提取尚未出现在现有块中的新行追加，防止把已存在的长行（Location/Short Caption/Summary）重复编码两遍。
		if strings.Contains(text, "\n") {
			var novelLines []string
			for _, line := range strings.Split(text, "\n") {
				line = strings.TrimSpace(line)
				if line == "" {
					continue
				}
				alreadyPresent := false
				for _, existing := range blocks {
					if strings.Contains(existing, line) {
						alreadyPresent = true
						break
					}
				}
				if !alreadyPresent {
					novelLines = append(novelLines, line)
				}
			}
			if len(novelLines) == 0 {
				return
			}
			text = strings.Join(novelLines, "\n")
		}
		blocks = append(kept, text)
	}
	appendBlock(d.FileParse)
	for _, sub := range d.SubUnits {
		if strings.TrimSpace(sub.Text) != "" {
			appendBlock(sub.Text)
			continue
		}
		appendBlock(sub.FileParse)
	}
	return strings.Join(blocks, "\n")
}

// EvalManifest proves which plaintext vault, tenant scope and corpus generation a run used.
// 结构体名与 JSON 键名（transport / endpoint / tenant_id / user_id / ...）为跨版本数据兼容
// 保持不变；endpoint 现恒为本地金库标识，不再指向任何远端。
type EvalManifest struct {
	Transport     string `json:"transport"`
	Endpoint      string `json:"endpoint"`
	TenantID      string `json:"tenant_id"`
	UserID        string `json:"user_id"`
	Scope         string `json:"scope"`
	Suite         string `json:"suite"`
	CorpusSHA256  string `json:"corpus_sha256"`
	Documents     int    `json:"documents"`
	Reused        bool   `json:"reused,omitempty"`
	LocalFallback bool   `json:"local_fallback"`
	// PurgedUnits 是本次「重算记忆」在重灌前清空的旧记忆条数（本地金库全量清点后的删除回执）。
	// 0 = 该 store 原本为空；只有 rebuild 路径才会写非零值。
	PurgedUnits int `json:"purged_units,omitempty"`
	// InsertedUnits 是本次 ingest 真实写入（全量重灌、断点补灌、正文自愈重灌合计）的单元数；
	// 暖库续灌命中封条且零补灌时为 0。审计据此区分「全量重灌」与「暖库复用」，
	// 不再以 corpus 大小冒充写入数。
	InsertedUnits int `json:"inserted_units,omitempty"`
	// Generation 是本语料所属的评测世代号（scope = <user>-mem-eval-<suite>-<generation>）。
	// 同一世代可承载多次批次读取；每次新评测（新语料重建）都分配独立世代。
	Generation string `json:"generation,omitempty"`
	// RetiredScopes 是本次回收的过期世代回执（含条数与残留错误；失败不阻塞本次运行）。
	RetiredScopes []EvalGenerationPurge `json:"retired_scopes,omitempty"`
	// PulseTokenizer 是本次使用的盲态脉冲切词臂（product | standard | trigram）。缺省空值
	// 只出现在历史卷的 manifest 里（那时全链是 standard 单臂）。
	PulseTokenizer string `json:"pulse_tokenizer,omitempty"`
	// PulseMode 是本次使用的评测脉冲模式（现役唯一值 local_plain；历史脉冲模式名
	// 只可能出现在旧卷 manifest 里，读取按只读兼容处理）。
	PulseMode string `json:"pulse_mode,omitempty"`
}

// LocalEngine is the benchmark-only strict local data plane. The JSON vault stores
// plaintext locally; ranking and memory computation are performed by the local
// engine (deterministic cluster collision kernel over the vault), zero network egress.
type LocalEngine struct {
	Store     *local.JSONFileStore
	Endpoint  string
	TenantID  string
	UserID    string
	Scope     string
	Suite     string
	StorePath string
	// Generation 是当前绑定的评测世代号（历史固定 scope 形态下为空）。
	Generation string
	// PulseTokenizer 是本臂使用的盲态脉冲切词器（product | standard | trigram），随 manifest 落盘。
	PulseTokenizer string
	// PulseMode 是本引擎绑定的脉冲运行模式（现役唯一值 local_plain）。
	PulseMode string

	cfg *config.Config
	// retired 是本次回收的过期世代回执，随 manifest 落盘供审计。
	retired []EvalGenerationPurge
	// credentialFP 是本次本地金库 pepper 的不可逆短指纹（仅审计定位，绝不含密钥明文）。
	credentialFP string
	// ownsGeneration 与 ownerToken 标记本引擎实例是否拥有当前世代的独占写租约。
	ownsGeneration bool
	ownerToken     string

	// localIdentityOnce 串行化本地模式的租户坐标准备：并行 Recall 会并发进入 CheckHealth，
	// 默认租户坐标只写一次（Once 自带内存屏障），此后 CheckHealth 只读。
	localIdentityOnce sync.Once

	localVaultMu      sync.Mutex
	localVaultDB      *sql.DB
	localVault        *localvault.Vault
	localVaultCount   int
	coreQueryMu       sync.Mutex
	coreEngine        *core.Engine
	coreTermExtractor *plain.TermExtractor
	coreWorkerPool    sync.Pool

	localRecallMu             sync.Mutex
	localRecallSurfaces       map[string]bool
	localRecallFailedSurfaces map[string]bool
}

// ScopeForSuite derives a dedicated benchmark scope below the configured tenant.
// 现役唯一模式 = 本地明文（local_plain）:
//
//	<user>-mem-eval-<locomo|atmbench>-local-plain
func ScopeForSuite(suite, userID string) string {
	part := strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return unicode.ToLower(r)
		}
		return '-'
	}, strings.TrimSpace(userID))
	part = strings.Trim(part, "-")
	if part == "" {
		part = defaultEvalUserID
	}
	numericTenant := true
	for _, r := range part {
		if !unicode.IsDigit(r) {
			numericTenant = false
			break
		}
	}
	if numericTenant {
		part = "u" + part
	}
	scopeSuite := strings.ToLower(strings.TrimSpace(suite))
	if scopeSuite == SuiteATM {
		scopeSuite = "atmbench"
	}
	return part + "-mem-eval-" + scopeSuite + "-local-plain"
}

// ScopeForGeneration derives this run's dedicated generation scope:
// <user>-mem-eval-<suite>-local-plain-<generation>. 每次新评测独立 scope，天然与其它运行（含上一次
// 评测的残留）隔离；不带世代号时退化为固定 scope 的历史形态。
func ScopeForGeneration(suite, userID, generation string) string {
	base := ScopeForSuite(suite, userID)
	segment := strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return unicode.ToLower(r)
		}
		return '-'
	}, strings.TrimSpace(generation))
	segment = strings.Trim(segment, "-")
	if segment == "" {
		return base
	}
	return base + "-" + segment
}

// SplitEvalScopeWithMode 解析评测 scope：返回 (suite, pulseMode, generation, ok)。
// 解析正本在 `internal/evalns`：产品侧（`internal/memory/facade`）用同一份判定把自动 L3 Noetic
// 结晶挡在评测空间之外，两边共用一份 grammar 才不会随代际漂移。
// 形态闭集（唯一合法）：
//   - <prefix>-mem-eval-<locomo|atmbench>-local-plain[-<世代号>]
//
// 历史形态在 evalns 中一律 fail-closed。
// 本函数只把线名 `atmbench` 映射回内部套件名 `atm`，其余语义一律委托。
func SplitEvalScopeWithMode(scope string) (string, string, string, bool) {
	suite, pulseMode, generation, ok := evalns.Split(scope)
	if !ok {
		return "", "", "", false
	}
	if suite == evalns.SuiteATMWire {
		suite = SuiteATM
	}
	return suite, pulseMode, generation, true
}

// SplitEvalScope 解析评测 scope：返回 (suite, generation, ok)。
// 保持向后兼容签名；模式轴可通过 EvalScopePulseMode 或 SplitEvalScopeWithMode 获取。
func SplitEvalScope(scope string) (string, string, bool) {
	suite, _, generation, ok := SplitEvalScopeWithMode(scope)
	return suite, generation, ok
}

// EvalScopePulseMode 解析评测 scope 所属的脉冲模式。现役唯一模式为 local_plain：
// 非评测 scope 或无法解析时一律返回 local_plain（历史脉冲模式名只可能来自旧制品读取）。
func EvalScopePulseMode(scope string) string {
	_, mode, _, ok := SplitEvalScopeWithMode(scope)
	if !ok {
		return PulseModeLocalPlain
	}
	return mode
}

// EvalScopeSuite 解析评测 scope 的套件（"locomo" / "atm"）。
func EvalScopeSuite(scope string) (string, bool) {
	suite, _, ok := SplitEvalScope(scope)
	return suite, ok
}

// EvalScopeGeneration 取评测 scope 的世代号（历史固定 scope 返回空串）。
func EvalScopeGeneration(scope string) string {
	_, generation, _ := SplitEvalScope(scope)
	return generation
}

// IsEvalScope reports whether scope belongs to the isolated benchmark namespace.
func IsEvalScope(scope string) bool {
	_, ok := EvalScopeSuite(scope)
	return ok
}

// evalUserID 解析评测用户命名空间（scope 前缀）：环境变量优先，缺省 defaultEvalUserID。
// 仅用于本地 scope 命名与世代租约身份，不参与任何远端请求。
func evalUserID() string {
	for _, key := range []string{"ROLLING_MEMORY_OPEN_MEMORY_USER", "ROLLING_MEMORY_USER_ID"} {
		if value := strings.TrimSpace(os.Getenv(key)); value != "" {
			return value
		}
	}
	return defaultEvalUserID
}

// evalLocalVaultKey 返回本地评测金库的确定性盲化 pepper。与历史本地臂逐字节同源：
// 早期实现是 evalVaultSecretFromKey("local-eval-vault-key-"+userID)，而该静态前缀并非
// `mem2.*` 数据面签名钥，剥 pepper 变换对静态密钥原样返回，故此处直接返回同一字节串。
func evalLocalVaultKey(userID string) []byte {
	return []byte("local-eval-vault-key-" + userID)
}

// evalKeyFingerprint 返回本地金库 pepper 的不可逆短指纹（SHA-256 前 4 字节 = 8 位十六进制），
// 只用于回执 / 诊断定位「本次用的是哪把钥」，绝不落密钥明文。
func evalKeyFingerprint(apiKey string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(apiKey)))
	return hex.EncodeToString(sum[:4])
}

// NewLocalEngine constructs the strict local data plane for one benchmark suite.
func NewLocalEngine(cfg *config.Config, suite, storePath string, options ...LocalEngineOption) (*LocalEngine, error) {
	opt := localEngineConfig{
		pulseTokenizer: PulseTokenizerProduct,
	}
	for _, apply := range options {
		if apply != nil {
			apply(&opt)
		}
	}
	pulseMode := PulseModeLocalPlain
	suite = strings.ToLower(strings.TrimSpace(suite))
	if suite != SuiteLoCoMo && suite != SuiteATM {
		return nil, fmt.Errorf("unsupported evaluation suite %q", suite)
	}
	if strings.TrimSpace(storePath) == "" {
		return nil, errors.New("evaluation requires an isolated plaintext store path")
	}
	userID := evalUserID()
	endpoint := "local://vault"
	tenantID := "user"
	// 本地金库的盲化 pepper 取确定性本地值（与历史本地臂逐字节一致），
	// 既有明文金库换版本后继续可读，不发生派生键漂移。
	store, err := local.NewJSONFileStore(storePath, evalLocalVaultKey(userID))
	if err != nil {
		return nil, err
	}
	applyPulseTokenizer(store, opt.pulseTokenizer, evalLocalVaultKey(userID))
	return &LocalEngine{
		Store: store, Endpoint: endpoint, TenantID: tenantID, UserID: userID,
		Scope: ScopeForSuite(suite, userID), Suite: suite, StorePath: storePath,
		PulseTokenizer: opt.pulseTokenizer, PulseMode: pulseMode, cfg: cfg,
		credentialFP: evalKeyFingerprint(string(evalLocalVaultKey(userID))),
	}, nil
}

// NewLocalEngineForScope reconstructs a suite data plane for an Admin/CLI recall worker.
// 接受评测命名空间内的任意 scope：历史固定 scope（u10-mem-eval-locomo）、本地独立 scope
// 与世代 scope 都允许解析；套件与脉冲模式由 scope 自身解析（历史脉冲 scope 会在构造时
// fail-closed 拒绝——现役唯一可运行模式 = local_plain）。store 路径由调用方解析。
func NewLocalEngineForScope(cfg *config.Config, scope, storePath string, options ...LocalEngineOption) (*LocalEngine, error) {
	suite, pulseMode, _, ok := SplitEvalScopeWithMode(scope)
	if !ok {
		return nil, fmt.Errorf("scope %q is not an isolated evaluation scope", scope)
	}
	if pulseMode != PulseModeLocalPlain {
		return nil, fmt.Errorf("evaluation scope %q pulse mode %s is not the local plaintext mode", scope, pulseMode)
	}
	engine, err := NewLocalEngine(cfg, suite, storePath, options...)
	if err != nil {
		return nil, err
	}
	if err := engine.BindScope(scope); err != nil {
		return nil, err
	}
	return engine, nil
}

// BindScope 把引擎绑定到指定评测 scope（必须属于同一套件的评测命名空间）。
func (e *LocalEngine) BindScope(scope string) error {
	if e == nil {
		return errors.New("evaluation engine is not initialized")
	}
	suite, pulseMode, generation, ok := SplitEvalScopeWithMode(scope)
	if !ok {
		return fmt.Errorf("scope %q is not an isolated evaluation scope", scope)
	}
	if e.Suite != "" && suite != e.Suite {
		return fmt.Errorf("evaluation scope %q does not belong to suite %q", scope, e.Suite)
	}
	// 本地模式只认 local_plain：任何非本地模式在此 fail-closed 拒绝。
	if pulseMode != e.PulseMode {
		return fmt.Errorf("evaluation scope %q pulse mode %s does not match engine mode %s", scope, pulseMode, e.PulseMode)
	}
	if scope != e.Scope {
		e.resetLocalRecallProvenance()
	}
	e.Scope = scope
	e.Generation = generation
	e.PulseMode = pulseMode
	return nil
}

// CheckHealth adopts the local tenant identity once (zero network egress).
func (e *LocalEngine) CheckHealth(_ context.Context) error {
	if e == nil {
		return errors.New("evaluation engine is not initialized")
	}
	e.localIdentityOnce.Do(func() {
		if e.TenantID == "" {
			e.TenantID = "user"
		}
	})
	return nil
}

// Metadata returns non-secret runtime provenance persisted with every summary.
func (e *LocalEngine) Metadata() map[string]any {
	meta := map[string]any{
		"transport":              LocalPlainTransport,
		"endpoint":               e.Endpoint,
		"tenant_id":              e.TenantID,
		"user_id":                e.UserID,
		"scope":                  e.Scope,
		"generation":             e.Generation,
		"suite":                  e.Suite,
		"store_path":             e.StorePath,
		"credential_fingerprint": e.credentialFP,
		"pulse_tokenizer":        normalizePulseTokenizer(e.PulseTokenizer),
		"pulse_mode":             e.PulseMode,
		"local_fallback":         false,
	}
	if e.localIndexSurface() == LocalIndexSurfaceProduct {
		indexSurface := "product-maxcap"
		if strings.EqualFold(evalHarmonicsName(), "off") {
			indexSurface = "product-plain"
		}
		meta["configured_local_index_surface"] = indexSurface
	} else {
		meta["configured_local_index_surface"] = "eval-" + normalizePulseTokenizer(e.PulseTokenizer)
	}
	e.localRecallMu.Lock()
	surfaces := make([]string, 0, len(e.localRecallSurfaces))
	for surface := range e.localRecallSurfaces {
		surfaces = append(surfaces, surface)
	}
	failedSurfaces := make([]string, 0, len(e.localRecallFailedSurfaces))
	for surface := range e.localRecallFailedSurfaces {
		failedSurfaces = append(failedSurfaces, surface)
	}
	e.localRecallMu.Unlock()
	sort.Strings(surfaces)
	indexSurfaces := make(map[string]bool)
	for _, surface := range surfaces {
		switch surface {
		case "core.engine.query":
			indexSurfaces["core-rm"] = true
		case "localvault.recall-with-options", "vault.search-client-sdk-default":
			indexSurfaces["localvault-vault"] = true
		default:
			indexSurfaces["unknown"] = true
		}
	}
	meta["local_index_surface"] = "unknown"
	if len(indexSurfaces) == 1 {
		for surface := range indexSurfaces {
			meta["local_index_surface"] = surface
		}
	} else if len(indexSurfaces) > 1 {
		meta["local_index_surface"] = "mixed"
	}
	meta["local_recall_surface"] = "unknown"
	// The localvault layer does not cover the complete Memory.Recall delivery pipeline.
	meta["product_path_equivalent"] = false
	if len(surfaces) == 1 {
		meta["local_recall_surface"] = surfaces[0]
	} else if len(surfaces) > 1 {
		meta["local_recall_surface"] = "mixed"
		meta["local_recall_surfaces"] = surfaces
	}
	if len(failedSurfaces) > 0 {
		sort.Strings(failedSurfaces)
		meta["local_recall_failed_surfaces"] = failedSurfaces
	}
	return meta
}

// resetLocalRecallProvenance starts a new evaluation snapshot; scope changes and
// preparation must not run concurrently with recall on the same engine.
func (e *LocalEngine) resetLocalRecallProvenance() {
	e.localRecallMu.Lock()
	defer e.localRecallMu.Unlock()
	e.localRecallSurfaces = nil
	e.localRecallFailedSurfaces = nil
}

func (e *LocalEngine) recordLocalRecallSurface(surface string, err error) {
	e.localRecallMu.Lock()
	defer e.localRecallMu.Unlock()
	if err != nil {
		if e.localRecallFailedSurfaces == nil {
			e.localRecallFailedSurfaces = make(map[string]bool)
		}
		e.localRecallFailedSurfaces[surface] = true
		return
	}
	if e.localRecallSurfaces == nil {
		e.localRecallSurfaces = make(map[string]bool)
	}
	e.localRecallSurfaces[surface] = true
}

// EvalCorpusDigest returns the canonical digest of source-faithful corpus documents.
func EvalCorpusDigest(documents []EvalDocument) (string, error) {
	corpusJSON, err := json.Marshal(documents)
	if err != nil {
		return "", fmt.Errorf("evaluation corpus digest failed: %w", err)
	}
	digest := sha256.Sum256(corpusJSON)
	return hex.EncodeToString(digest[:]), nil
}

// evalManifestIdentityError 为「manifest 身份与现役客户端不一致」提供稳定错误码：
// 复用不可能，但语料仍可按现役装配原地重灌（调用方据此进入自愈重灌，而不是 fail-closed）。
func evalManifestIdentityError(format string, args ...any) error {
	return &EvalVerifyError{
		Code: EvalVerifyCodeManifestIdentityMismatch,
		Msg:  fmt.Sprintf(format, args...),
	}
}

func (e *LocalEngine) validateManifestPulseMode(manifest EvalManifest) error {
	if strings.TrimSpace(manifest.PulseMode) == "" || manifest.PulseMode != e.PulseMode {
		return fmt.Errorf("evaluation manifest pulse mode %q does not match the active client %q (rebuild required)",
			manifest.PulseMode, e.PulseMode)
	}
	return nil
}

// VerifyPrepared proves exact manifest, corpus and stored-unit-set equality.
func (e *LocalEngine) VerifyPrepared(ctx context.Context, documents []EvalDocument) (EvalManifest, error) {
	manifest, err := ReadEvalManifest(e.StorePath)
	if err != nil {
		return manifest, err
	}
	if err := e.validateManifestPulseMode(manifest); err != nil {
		return manifest, err
	}
	if e.Generation == "" && manifest.Scope != "" && manifest.Suite == e.Suite {
		_, _ = e.adoptStoredGeneration()
	}
	digest, err := EvalCorpusDigest(documents)
	if err != nil {
		return manifest, err
	}
	if manifest.Scope != e.Scope || manifest.Suite != e.Suite || manifest.CorpusSHA256 != digest || manifest.Documents != len(documents) {
		return manifest, &EvalVerifyError{
			Code: EvalVerifyCodeManifestMismatch,
			Msg:  "evaluation manifest does not match the requested corpus",
		}
	}
	if len(documents) == 0 {
		return manifest, errors.New("evaluation cannot reuse an empty corpus")
	}
	if err := e.CheckHealth(ctx); err != nil {
		return manifest, err
	}
	if manifest.Endpoint != e.Endpoint || manifest.TenantID != e.TenantID || manifest.UserID != e.UserID {
		return manifest, evalManifestIdentityError("evaluation manifest endpoint/tenant/user does not match the active engine")
	}
	// 切词臂必须与金库脉冲逐位同源：UnitID 相同但脉冲词项不同（product vs standard）
	// 时复用会静默用错词表检索，必须 fail-closed 要求重建。历史卷 manifest 无该字段
	// （当年全链 standard 单臂），按 standard 对账。
	manifestTokenizer := strings.TrimSpace(manifest.PulseTokenizer)
	if manifestTokenizer == "" {
		manifestTokenizer = PulseTokenizerStandard
	}
	if normalizePulseTokenizer(manifestTokenizer) != normalizePulseTokenizer(e.PulseTokenizer) {
		return manifest, evalManifestIdentityError("evaluation manifest pulse tokenizer %q does not match the active client %q (rebuild required)",
			manifestTokenizer, normalizePulseTokenizer(e.PulseTokenizer))
	}
	// 世代在用：读者心跳续期（Admin 的 prepare 探针也走这里），保证并发回收不会清掉正在读的世代。
	touchEvalGeneration(e.Scope, evalGenerationRoleReader)
	// 本地金库文档全集即权威清单（本地臂零云端，无远端对账面）。
	storedIDs := e.localStoredUnitIDs()
	if len(storedIDs) != len(documents) {
		return manifest, fmt.Errorf("evaluation stored unit count %d does not match corpus %d", len(storedIDs), len(documents))
	}
	expected := make(map[string]bool, len(documents))
	for _, doc := range documents {
		if expected[doc.ID] {
			return manifest, fmt.Errorf("evaluation corpus contains duplicate unit ID %q", doc.ID)
		}
		expected[doc.ID] = true
	}
	for _, id := range storedIDs {
		if !expected[id] {
			return manifest, fmt.Errorf("evaluation store contains foreign unit %q", id)
		}
		delete(expected, id)
	}
	if len(expected) != 0 {
		return manifest, fmt.Errorf("evaluation store is missing %d corpus units", len(expected))
	}
	// ID 集合相等只证明「有哪些单元」，不证明「单元正文是什么」；本地索引由 store 正文
	// 重建，正文被替换时代码会静默用错语料检索，必须在这里 fail-closed。
	if err := e.verifyLocalPlainStoreBodies(documents); err != nil {
		return manifest, err
	}
	manifest.Reused = true
	return manifest, nil
}

// Prepare 选择本次评测的世代，并保证本地金库语料与请求语料完全一致：
//   - 本 store 既有 manifest 的 scope 是「这个 store 当前是谁」的真源，先采纳；
//   - 该世代在本地已被清空（store 为空）⇒ 复用无意义，自动新开独立世代并全量重灌
//     （「新测评清旧记忆、建新 scope」；这也修掉了「store 已被清、封条仍指旧库 ⇒ 静默 0 命中」的旧洞）；
//   - 显式 reuse 时按 manifest + 语料摘要 + 金库文档全集三者精确相等复用；不相等一律 fail-closed，
//     绝不盲灌；
//   - rebuild 永远新开世代：不动任何在用世代，过期世代按心跳周期自动回收。
func (e *LocalEngine) Prepare(ctx context.Context, documents []EvalDocument, rebuild, reuse bool, workers int, logf func(string, ...any)) (EvalManifest, error) {
	e.resetLocalRecallProvenance()
	if logf == nil {
		logf = func(string, ...any) {}
	}
	if len(documents) == 0 {
		return EvalManifest{}, errors.New("evaluation requires at least one source document")
	}
	if !rebuild {
		manifest, err := ReadEvalManifest(e.StorePath)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return manifest, err
		}
		if err == nil {
			if err := e.validateManifestPulseMode(manifest); err != nil {
				return manifest, err
			}
		}
		if adopted, ok := e.adoptStoredGeneration(); ok {
			empty, err := e.scopeEmpty(ctx)
			if err != nil {
				return EvalManifest{}, fmt.Errorf("评测世代探测失败（不盲灌、不复用）: %w", err)
			}
			if !empty {
				// 世代在用：读者心跳续期，保证并发回收不会把正在读的世代清掉。
				touchEvalGeneration(e.Scope, evalGenerationRoleReader)
				if reuse {
					manifest, err := e.VerifyPrepared(ctx, documents)
					if err == nil {
						manifest.Reused = true
						manifest.RetiredScopes = nil
						return manifest, nil
					}
					return manifest, fmt.Errorf("evaluation reuse verification failed; refusing blind re-ingest: %w", err)
				}
				// 暖库续灌的正文完整性闸：续灌只按「ID 是否存在」补缺，从不改写已存在单元；
				// store 正文一旦被替换（手工编辑、脚本、历史语料残留），索引会静默用错语料检索。
				// 这里按语料逐单元校验正文，被替换的单元删除后交回正常续灌重灌（写入侧同一
				// SDK 拼装函数，重灌即自愈）；语料外的孤儿单元 fail-closed 要求 --rebuild。
				if err := e.repairLocalPlainStoreBodies(documents, logf); err != nil {
					return EvalManifest{}, err
				}
				return e.Ingest(ctx, documents, false, workers, logf)
			}
			logf("[eval] 世代 %s 在本地为空（旧记忆已回收）：本次评测重建新世代", adopted.Scope)
		}
	}
	if err := e.startNewGeneration(ctx, logf); err != nil {
		return EvalManifest{}, err
	}
	manifest, err := e.Ingest(ctx, documents, true, workers, logf)
	if err != nil {
		e.finishGeneration()
	}
	return manifest, err
}

// rememberDocument 把一个 EvalDocument 就地编码进本地明文金库（本地评测臂唯一的写路径）：
//   - 无结构化元数据 ⇒ 正文即 PackedText，走 PackBlindPacket（与历史本地臂逐位一致）；
//   - 有结构化元数据 ⇒ 正文先经 FormatPlaintextWithMeta 拼装日期/作者/块前缀，再把说话人、
//     三时态、四维空间坐标（doc_h / block_h / line_start / line_end）编入同一脉冲包。
//
// 回执与历史本地臂一致：只含 UnitID / Layer / PulseMode，不含 AddressURI / Coord64 /
// KeptSentenceIndices（本地离线检索不产出这些云端字段）。
func (e *LocalEngine) rememberDocument(doc EvalDocument) error {
	layer := strings.TrimSpace(doc.Layer)
	if layer == "" {
		layer = local.LayerEpisodic
	}
	text := doc.PackedText()
	meta := doc.UnitMeta()
	fullText := text
	if doc.HasStructuredMeta() {
		fullText = local.FormatPlaintextWithMeta(text, meta)
	}
	occ := meta.OccurredAt
	if occ <= 0 && meta.NarratedAt > 0 {
		occ = meta.NarratedAt
	}
	pkt, sents := e.Store.PackBlindPacket(doc.ID, fullText, occ)
	if len(pkt.Slots) == 0 && strings.TrimSpace(pkt.AtomsB64) == "" {
		return fmt.Errorf("evaluation ingest %q: empty pulse packet", doc.ID)
	}
	if meta.NarratedAt > 0 {
		pkt.NarratedAt = meta.NarratedAt
	}
	if meta.IngestedAt > 0 {
		pkt.IngestedAt = meta.IngestedAt
	}
	if author := strings.ToLower(strings.TrimSpace(meta.Author)); author != "" {
		pkt.AuthorHash = e.Store.KeyedHash64(author)
	}
	pkt.DocHash = meta.DocHash
	if pkt.DocHash == 0 {
		if fileURL := strings.TrimSpace(meta.FileURL); fileURL != "" {
			pkt.DocHash = e.Store.KeyedHash64(fileURL)
		}
	}
	pkt.BlockHash = meta.BlockHash
	if pkt.BlockHash == 0 {
		if blockID := strings.TrimSpace(meta.BlockID); blockID != "" {
			pkt.BlockHash = e.Store.KeyedHash64(blockID)
		}
	}
	if meta.LineStart > 0 {
		pkt.LineStart = meta.LineStart
	}
	if meta.LineEnd > 0 {
		pkt.LineEnd = meta.LineEnd
	}
	if pkt.LineStart > 0 && pkt.LineEnd < pkt.LineStart {
		pkt.LineEnd = pkt.LineStart
	}
	pkt.Layer = layer
	resp := local.IngestResponse{UnitID: pkt.UnitID, Layer: pkt.Layer, PulseMode: local.ModeLocalPlain}
	return e.Store.SaveLocalPlaintext(doc.ID, occ, fullText, sents, resp)
}

// PurgeScopeUnits 清空本引擎 store 内的全部旧记忆（「每次运行都重算记忆」的清库半边）：
//   - 以本地金库的文档全集为权威清单（不借用任何局部命中或猜测替代全集）；
//   - 顺带销毁派生 SQLite 评测金库（下次按新 store 重建），零云端请求。
//
// 返回实际删除的 Unit 数；store 原本为空时返回 0（幂等）。
func (e *LocalEngine) PurgeScopeUnits(_ context.Context, logf func(string, ...any)) (int, error) {
	if e == nil {
		return 0, errors.New("evaluation engine is not initialized")
	}
	if logf == nil {
		logf = func(string, ...any) {}
	}
	e.localVaultMu.Lock()
	if e.localVaultDB != nil {
		_ = e.localVaultDB.Close()
		e.localVaultDB = nil
		e.localVault = nil
		e.localVaultCount = 0
	}
	_ = os.Remove(e.StorePath + ".local-vault.sqlite")
	_ = os.Remove(e.StorePath + ".local-vault.sqlite.meta.json")
	e.localVaultMu.Unlock()
	unitIDs := e.localStoredUnitIDs()
	deleted := e.Store.DeleteLocalUnits(unitIDs)
	return deleted, nil
}

// guardLocalPlainRebuildPurge 是本地评测库的清库来源闸（rebuild / 新开世代清库前调用）：
// store 里存在本次语料之外的文档、且没有任何评测来源封条（manifest / 断点续灌状态）时，
// 这本评测自己的库就不成立（更可能是用户自建的 SDK 金库），fail-closed 拒绝清库——
// 防「--eval-store 指向用户既有 store 被静默清空」。断点续灌（库内只有本语料文档）与
// 有封条的正常评测库不受影响。
func (e *LocalEngine) guardLocalPlainRebuildPurge(documents []EvalDocument) error {
	unitIDs := readLocalStoreUnitIDs(e.StorePath)
	if len(unitIDs) == 0 {
		return nil
	}
	if _, err := os.Stat(e.StorePath + ".manifest.json"); err == nil {
		return nil
	}
	if _, err := os.Stat(evalResumeStatePath(e.StorePath)); err == nil {
		return nil
	}
	corpus := make(map[string]bool, len(documents))
	for _, doc := range documents {
		corpus[doc.ID] = true
	}
	foreign := 0
	for _, id := range unitIDs {
		if !corpus[id] {
			foreign++
		}
	}
	if foreign == 0 {
		return nil
	}
	return fmt.Errorf("refusing to purge local evaluation store %s: it holds %d document(s) outside this corpus and carries no evaluation provenance (no manifest, no resume state); point --eval-store at a new path or delete that file deliberately", e.StorePath, foreign)
}

// evalResumeState 是本地明文金库的身份封条：只有 endpoint、金库钥指纹、切词臂、脉冲模式、
// scope 与语料摘要都与当前准备完全一致时，才允许按本地已有文档跳过 ingest。缺少该封条或
// 任一身份轴不一致时必须全量重灌——同一批 Unit ID 在换装配/换金库钥后脉冲词项不同，
// 跳过重灌会写出「documents=N 但金库仍是旧词表」的假回执。
type evalResumeState struct {
	Endpoint       string `json:"endpoint"`
	CredentialFP   string `json:"credential_fingerprint"`
	PulseTokenizer string `json:"pulse_tokenizer"`
	PulseMode      string `json:"pulse_mode"`
	Scope          string `json:"scope"`
	CorpusSHA256   string `json:"corpus_sha256"`
	Documents      int    `json:"documents"`
}

func evalResumeStatePath(storePath string) string {
	return storePath + ".resume.json"
}

func readEvalResumeState(storePath string) evalResumeState {
	var state evalResumeState
	raw, err := os.ReadFile(evalResumeStatePath(storePath))
	if err != nil {
		return state
	}
	_ = json.Unmarshal(raw, &state)
	return state
}

func writeEvalResumeState(storePath string, state evalResumeState) {
	_ = writeJSON(evalResumeStatePath(storePath), state)
}

// Ingest writes source-faithful units through the SDK, optionally replacing only
// the explicit unit IDs belonging to this corpus and isolated scope.
func (e *LocalEngine) Ingest(ctx context.Context, documents []EvalDocument, rebuild bool, workers int, logf func(string, ...any)) (EvalManifest, error) {
	manifest := EvalManifest{
		Transport: LocalPlainTransport, Endpoint: e.Endpoint, UserID: e.UserID,
		Scope: e.Scope, Suite: e.Suite, Documents: len(documents), LocalFallback: false,
		Generation: e.Generation, RetiredScopes: e.retired,
		PulseTokenizer: normalizePulseTokenizer(e.PulseTokenizer),
		PulseMode:      e.PulseMode,
	}
	if len(documents) == 0 {
		return manifest, errors.New("evaluation requires at least one source document")
	}
	corpusDigest, err := EvalCorpusDigest(documents)
	if err != nil {
		return manifest, err
	}
	manifest.CorpusSHA256 = corpusDigest
	if logf == nil {
		logf = func(string, ...any) {}
	}
	if err := e.CheckHealth(ctx); err != nil {
		return manifest, fmt.Errorf("evaluation health check failed: %w", err)
	}
	manifest.TenantID = e.TenantID
	if rebuild && len(documents) > 0 {
		// 「每次运行都重算记忆」：rebuild = 先清空本 store 的全部旧记忆（历次运行 / 历份语料的残留
		// 一并清掉），再全量重灌当前语料。只删“本语料自己的 Unit ID”会把历史残留留在库里，
		// 污染后续检索。清库前先撤销续灌封条：金库与本次语料的对应关系即刻失效，
		// 避免中途失败后下一次 Prepare 误用已失效的本地集合跳过 ingest。
		// 世代 scope 下这本就是新库（构造上为空）；此步是「复核并给回执」而不是猜测。
		// 另有一道来源闸：store 里若存在本次语料之外的文档且没有任何评测来源封条
		// （manifest / 断点续灌状态），说明这不是本评测自己的库（可能是用户自建的 SDK
		// 金库），fail-closed 拒绝清库（曾把 --eval-store 指向的用户 store 直接清空）。
		if err := e.guardLocalPlainRebuildPurge(documents); err != nil {
			return manifest, err
		}
		_ = os.Remove(evalResumeStatePath(e.StorePath))
		purged, err := e.PurgeScopeUnits(ctx, logf)
		if err != nil {
			return manifest, fmt.Errorf("evaluation isolated store reset failed: %w", err)
		}
		manifest.PurgedUnits = purged
	}
	if workers < 1 {
		workers = 1
	}
	// 断点续灌：仅当本地金库封条（endpoint + 金库钥指纹 + 切词臂 + 脉冲模式 + scope +
	// 语料摘要）与当前准备一致时，才把本地已有文档视为已成功写穿，只补缺失子集。
	// 中断因此可以低成本续跑；语料或装配/身份变化则必须全量重灌
	// （同一 Unit ID 换词表或换金库钥后旧脉冲不可复用）。
	resumeState := evalResumeState{
		Endpoint: e.Endpoint, Scope: e.Scope,
		CredentialFP:   evalKeyFingerprint(string(evalLocalVaultKey(e.UserID))),
		PulseTokenizer: normalizePulseTokenizer(e.PulseTokenizer),
		PulseMode:      e.PulseMode,
		CorpusSHA256:   manifest.CorpusSHA256, Documents: len(documents),
	}
	resumable := false
	if !rebuild {
		previous := readEvalResumeState(e.StorePath)
		resumable = previous.Endpoint == resumeState.Endpoint &&
			previous.CredentialFP == resumeState.CredentialFP &&
			previous.PulseTokenizer == resumeState.PulseTokenizer &&
			previous.PulseMode == resumeState.PulseMode &&
			previous.Scope == resumeState.Scope &&
			previous.CorpusSHA256 == resumeState.CorpusSHA256
	}
	targets := documents
	if resumable {
		pending := make([]EvalDocument, 0, len(documents))
		for _, doc := range documents {
			if _, _, _, lookupErr := e.Store.LoadLocalDocument(doc.ID); lookupErr == nil {
				continue
			}
			pending = append(pending, doc)
		}
		if len(pending) < len(documents) {
			logf("[eval] resume local vault: %d/%d documents already present, ingesting %d",
				len(documents)-len(pending), len(documents), len(pending))
		}
		targets = pending
	}
	paceC, stopPace := evalIngestPaceGate()
	defer stopPace()
	errs := ParallelMap(len(targets), workers, func(index int) error {
		if waitErr := waitIngestPace(ctx, paceC); waitErr != nil {
			return waitErr
		}
		doc := targets[index]
		if strings.TrimSpace(doc.ID) == "" || strings.TrimSpace(doc.Text) == "" {
			return fmt.Errorf("invalid evaluation document at index %d", index)
		}
		// 长时 ingest 期间持续续期世代租约：并发的「清旧记忆」据此判定本世代在用而跳过。
		if index%evalGenerationHeartbeatEveryDocs == 0 {
			touchEvalGeneration(e.Scope, evalGenerationRoleWriter)
		}
		return e.rememberDocument(doc)
	})
	for index, err := range errs {
		if err != nil {
			_ = e.Store.Flush()
			// 失败也要封条：本地金库此刻确实持有本引擎 × 本语料的部分文档，重试方可续灌。
			writeEvalResumeState(e.StorePath, resumeState)
			return manifest, fmt.Errorf("evaluation ingest %q failed: %w", targets[index].ID, err)
		}
	}
	// inserted_units = 真实写入数（全量重灌 = len(documents)；断点续灌 = 缺失补灌数；
	// 正文自愈删除的单元会回到 pending，因此同样计入 targets）。
	manifest.InsertedUnits = len(targets)
	if err := e.Store.Flush(); err != nil {
		return manifest, fmt.Errorf("evaluation plaintext vault flush failed: %w", err)
	}
	writeEvalResumeState(e.StorePath, resumeState)
	if err := writeJSON(e.StorePath+".manifest.json", manifest); err != nil {
		return manifest, err
	}
	// 租约登记本世代语料身份：Admin/审计据此判断「这个 scope 里装的是哪份语料、有多少条」。
	recordGenerationCorpus(e.Scope, e.StorePath, manifest)
	logf("[eval] local ingest complete (%s): suite=%s scope=%s documents=%d", e.PulseMode, e.Suite, e.Scope, len(documents))
	return manifest, nil
}

func (e *LocalEngine) localStoredUnitIDs() []string {
	if e == nil || e.Store == nil {
		return nil
	}
	return readLocalStoreUnitIDs(e.StorePath)
}

// readLocalStoreUnitIDs lists the document IDs persisted in one local SDK store
// file; a missing or unparsable file yields nil (callers treat that as "no
// evidence", never as "empty corpus").
func readLocalStoreUnitIDs(storePath string) []string {
	raw, err := os.ReadFile(storePath)
	if err != nil {
		return nil
	}
	var docs map[string]json.RawMessage
	if json.Unmarshal(raw, &docs) != nil {
		return nil
	}
	out := make([]string, 0, len(docs))
	for id := range docs {
		out = append(out, id)
	}
	return out
}

// verifyLocalPlainStoreBodies 证明本地 store 中影响索引的正文与请求语料逐字一致。
// ID 集合相等会接受「ID 不变、正文被替换」的 store，而 ensureLocalPlainVault 正是
// 用 store 正文重建 SQLite 索引；期望正文按写入时同一 SDK 拼装函数
// （FormatPlaintextWithMeta(PackedText, UnitMeta)）重建，避免另造一套变换。
func (e *LocalEngine) verifyLocalPlainStoreBodies(documents []EvalDocument) error {
	raw, err := os.ReadFile(e.StorePath)
	if err != nil {
		return fmt.Errorf("evaluation local store unreadable: %w", err)
	}
	var stored map[string]struct {
		DocID      string `json:"doc_id"`
		OccurredAt int64  `json:"occurred_at"`
		RawText    string `json:"raw_text"`
	}
	if err := json.Unmarshal(raw, &stored); err != nil {
		return fmt.Errorf("evaluation local store decode failed: %w", err)
	}
	if len(stored) != len(documents) {
		return fmt.Errorf("evaluation local store has %d units, corpus has %d", len(stored), len(documents))
	}
	for _, doc := range documents {
		rec, ok := stored[doc.ID]
		if !ok {
			return fmt.Errorf("evaluation local store is missing unit %q", doc.ID)
		}
		if rec.DocID != "" && rec.DocID != doc.ID {
			return fmt.Errorf("evaluation local store unit %q carries doc_id %q", doc.ID, rec.DocID)
		}
		meta := doc.UnitMeta()
		occurredAt := meta.OccurredAt
		if occurredAt <= 0 && meta.NarratedAt > 0 {
			occurredAt = meta.NarratedAt
		}
		if rec.OccurredAt != occurredAt {
			return fmt.Errorf("evaluation local store unit %q occurred_at %d does not match corpus %d",
				doc.ID, rec.OccurredAt, occurredAt)
		}
		// 写入侧有两条路径：无结构化元数据时 store 正文就是 PackedText；
		// 有元数据时才经 FormatPlaintextWithMeta 追加日期/作者/块前缀。
		expected := doc.PackedText()
		if doc.HasStructuredMeta() {
			expected = local.FormatPlaintextWithMeta(expected, meta)
		}
		if rec.RawText != expected {
			return fmt.Errorf("evaluation local store unit %q raw_text does not match corpus body", doc.ID)
		}
	}
	return nil
}

// repairLocalPlainStoreBodies 是暖库续灌路径的正文完整性闸（verifyLocalPlainStoreBodies 的自愈变体）。
// 断点续灌按「ID 是否存在」补缺，从不改写已存在单元；store 正文一旦被替换（手工编辑、脚本、
// 历史语料残留），续灌会静默用错语料检索。这里按语料逐单元校验正文：被替换的单元从 store
// 删除并交回正常续灌重灌（写回侧是同一 SDK 拼装函数，重灌即自愈）；缺单元属断点续灌常态，
// 照常放行；语料外的孤儿单元不属于本语料，fail-closed 要求 --rebuild，绝不静默带入索引。
func (e *LocalEngine) repairLocalPlainStoreBodies(documents []EvalDocument, logf func(string, ...any)) error {
	raw, err := os.ReadFile(e.StorePath)
	if err != nil {
		return fmt.Errorf("evaluation local store unreadable: %w", err)
	}
	var stored map[string]struct {
		DocID      string `json:"doc_id"`
		OccurredAt int64  `json:"occurred_at"`
		RawText    string `json:"raw_text"`
	}
	if err := json.Unmarshal(raw, &stored); err != nil {
		return fmt.Errorf("evaluation local store decode failed: %w", err)
	}
	inCorpus := make(map[string]bool, len(documents))
	tampered := make([]string, 0)
	for _, doc := range documents {
		inCorpus[doc.ID] = true
		rec, ok := stored[doc.ID]
		if !ok {
			continue // 缺单元：断点续灌本来就会补灌
		}
		bad := rec.DocID != "" && rec.DocID != doc.ID
		meta := doc.UnitMeta()
		occurredAt := meta.OccurredAt
		if occurredAt <= 0 && meta.NarratedAt > 0 {
			occurredAt = meta.NarratedAt
		}
		if !bad && rec.OccurredAt != occurredAt {
			bad = true
		}
		if !bad {
			expected := doc.PackedText()
			if doc.HasStructuredMeta() {
				expected = local.FormatPlaintextWithMeta(expected, meta)
			}
			bad = rec.RawText != expected
		}
		if bad {
			tampered = append(tampered, doc.ID)
		}
	}
	for id := range stored {
		if !inCorpus[id] {
			return fmt.Errorf("evaluation local store carries orphan unit %q (not in current corpus); rerun with --rebuild", id)
		}
	}
	if len(tampered) == 0 {
		return nil
	}
	if deleted := e.Store.DeleteLocalUnits(tampered); deleted != len(tampered) {
		return fmt.Errorf("evaluation local store body repair deleted %d/%d tampered units", deleted, len(tampered))
	}
	if err := e.Store.Flush(); err != nil {
		return fmt.Errorf("evaluation plaintext vault flush failed: %w", err)
	}
	logf("[eval] resume body integrity: %d unit(s) carried tampered/stale bodies, re-ingesting from corpus", len(tampered))
	return nil
}

// localIndexSurface 决定本地明文评测使用产品真实面还是历史评测诊断面。
// 默认 product 臂走产品面；standard / trigram 保留为诊断对照。
func (e *LocalEngine) localIndexSurface() string {
	if normalizePulseTokenizer(e.PulseTokenizer) == PulseTokenizerProduct {
		return LocalIndexSurfaceProduct
	}
	return LocalIndexSurfaceEval
}

func evalHarmonicsName() string {
	harmonics := strings.TrimSpace(os.Getenv("ROLLING_MEMORY_HARMONICS"))
	if harmonics == "" {
		return "max"
	}
	return harmonics
}

// localVaultMeta binds the derived SQLite vault to the exact store bytes and index surface.
// Admin 的逐题 recall 会为每题启动一个进程；没有该元数据时每个进程都会重灌 11k 篇。
type localVaultMeta struct {
	Schema       int    `json:"schema"`
	StoreSHA256  string `json:"store_sha256"`
	IndexSurface string `json:"index_surface"`
	Harmonics    string `json:"harmonics,omitempty"`
	Tokenizer    string `json:"tokenizer"`
	Ingestion    string `json:"ingestion"`
	Documents    int    `json:"documents"`
}

const localVaultMetaSchema = 1

func (e *LocalEngine) localVaultMetaFor(storeSHA string, documents int) localVaultMeta {
	meta := localVaultMeta{
		Schema:       localVaultMetaSchema,
		StoreSHA256:  storeSHA,
		IndexSurface: e.localIndexSurface(),
		Tokenizer:    normalizePulseTokenizer(e.PulseTokenizer),
		Documents:    documents,
	}
	meta.Ingestion = "direct-insert"
	if meta.IndexSurface == LocalIndexSurfaceProduct {
		meta.Harmonics = evalHarmonicsName()
		meta.Ingestion = "product-client"
	}
	return meta
}

// configureLocalVault applies the index configuration of the selected surface.
func (e *LocalEngine) configureLocalVault(v *localvault.Vault) error {
	if e.localIndexSurface() == LocalIndexSurfaceProduct {
		if strings.EqualFold(evalHarmonicsName(), "off") {
			v.SetCanonicalHarmonics(plain.CanonicalHarmonicOptions{})
			v.SetSegmenterOptions(plain.SegmenterPackOptions{})
			return nil
		}
		v.SetCanonicalHarmonics(plain.CanonicalHarmonicOptions{
			EnableStructuredDates: true,
			Expander:              plain.CanonicalCompanionTags,
		})
		v.SetSegmenterOptions(plain.SegmenterPackOptions{HarmonicOverlay: true})
		return nil
	}
	switch normalizePulseTokenizer(e.PulseTokenizer) {
	case PulseTokenizerStandard:
		v.SetCanonicalHarmonics(plain.CanonicalHarmonicOptions{})
		v.SetSegmenterOptions(plain.SegmenterPackOptions{})
	case PulseTokenizerTrigram:
		v.SetCanonicalHarmonics(plain.CanonicalHarmonicOptions{EnableStructuredDates: true})
		v.SetSegmenterOptions(plain.SegmenterPackOptions{
			Segmenter:       plain.FTS5TrigramSegmenter,
			HarmonicOverlay: true,
		})
	default:
		v.SetCanonicalHarmonics(plain.CanonicalHarmonicOptions{
			EnableStructuredDates: true,
			Expander:              plain.CanonicalCompanionTags,
		})
		v.SetSegmenterOptions(plain.SegmenterPackOptions{HarmonicOverlay: true})
	}
	return nil
}

func (e *LocalEngine) ensureLocalPlainVault() (*localvault.Vault, error) {
	e.localVaultMu.Lock()
	defer e.localVaultMu.Unlock()

	raw, err := os.ReadFile(e.StorePath)
	if err != nil {
		return nil, err
	}
	var docs map[string]struct {
		DocID      string `json:"doc_id"`
		OccurredAt int64  `json:"occurred_at"`
		RawText    string `json:"raw_text"`
		AddressURI string `json:"address_uri"`
		Coord64    uint64 `json:"coord64"`
	}
	if err := json.Unmarshal(raw, &docs); err != nil {
		return nil, err
	}
	if len(docs) == 0 {
		return nil, nil
	}
	if e.localVault != nil && e.localVaultCount == len(docs) {
		return e.localVault, nil
	}
	if e.localVaultDB != nil {
		_ = e.localVaultDB.Close()
		e.localVaultDB = nil
		e.localVault = nil
	}
	sqlitePath := e.StorePath + ".local-vault.sqlite"
	metaPath := sqlitePath + ".meta.json"
	secret := evalLocalVaultKey(e.UserID)
	storeSHA := sha256.Sum256(raw)
	wantMeta := e.localVaultMetaFor(hex.EncodeToString(storeSHA[:]), len(docs))

	// 复用派生金库：store 字节、索引面、谐波装配、切词臂与文档数全部一致才打开；
	// 任一不符即整库重建，避免「配置换了但检索仍跑旧索引」的静默漂移。
	if metaRaw, readErr := os.ReadFile(metaPath); readErr == nil {
		var have localVaultMeta
		if json.Unmarshal(metaRaw, &have) == nil && have == wantMeta {
			if db, openErr := sql.Open("sqlite", evalVaultDSN(sqlitePath)); openErr == nil {
				db.SetMaxOpenConns(1)
				db.SetMaxIdleConns(1)
				_, _ = db.Exec(`PRAGMA synchronous = OFF; PRAGMA journal_mode = MEMORY;`)
				var count int
				countErr := db.QueryRow(`SELECT COUNT(*) FROM rm_docs WHERE layer = 'episodic' OR layer = ''`).Scan(&count)
				v, vere := localvault.NewVault(db, secret)
				if countErr == nil && count == len(docs) && vere == nil && e.configureLocalVault(v) == nil {
					e.localVaultDB = db
					e.localVault = v
					e.localVaultCount = len(docs)
					return v, nil
				}
				_ = db.Close()
			}
		}
	}
	if err := os.Remove(sqlitePath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("remove derived evaluation vault: %w", err)
	}
	_ = os.Remove(metaPath)
	db, err := sql.Open("sqlite", evalVaultDSN(sqlitePath))
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	_, _ = db.Exec(`PRAGMA synchronous = OFF; PRAGMA journal_mode = MEMORY;`)
	v, err := localvault.NewVault(db, secret)
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := e.configureLocalVault(v); err != nil {
		_ = db.Close()
		return nil, err
	}
	ids := make([]string, 0, len(docs))
	for id := range docs {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	if e.localIndexSurface() == LocalIndexSurfaceProduct {
		// 产品面必须走产品摄入链（localvault.RememberEpisodic）：写入句子帧与 kept 索引，
		// 与 MCP/CLI 保存记忆同一路径。裸插入会丢句子帧，使本地排序的
		// 事件链/句窗能力失效，等于又把产品能力阉掉一半。
		tc := localvault.NewClient(v, nil)
		for _, id := range ids {
			rec := docs[id]
			if _, err := tc.RememberEpisodic(context.Background(), id, rec.RawText, rec.OccurredAt); err != nil {
				_ = db.Close()
				return nil, fmt.Errorf("product-surface vault ingest %q: %w", id, err)
			}
		}
	} else {
		tx, err := db.Begin()
		if err != nil {
			_ = db.Close()
			return nil, err
		}
		stmtDoc, _ := tx.Prepare(`INSERT OR REPLACE INTO rm_docs(unit_id, layer, occurred_at, raw_text) VALUES (?, 'episodic', ?, ?)`)
		stmtMeta, _ := tx.Prepare(`INSERT OR REPLACE INTO rm_unit_meta(unit_id, layer, sentences_json, kept_indices_json, address_uri, coord64, assistant_id, inserted_at, meta_rev) VALUES (?, 'episodic', '[]', '[]', ?, ?, 'default', ?, 1)`)
		for _, id := range ids {
			rec := docs[id]
			if stmtDoc != nil {
				_, _ = stmtDoc.Exec(id, rec.OccurredAt, rec.RawText)
			}
			if stmtMeta != nil {
				_, _ = stmtMeta.Exec(id, rec.AddressURI, int64(rec.Coord64), rec.OccurredAt)
			}
		}
		if stmtDoc != nil {
			_ = stmtDoc.Close()
		}
		if stmtMeta != nil {
			_ = stmtMeta.Close()
		}
		if err := tx.Commit(); err != nil {
			_ = db.Close()
			return nil, err
		}
	}
	if metaJSON, err := json.Marshal(wantMeta); err != nil {
		_ = db.Close()
		return nil, err
	} else if err := os.WriteFile(metaPath, metaJSON, 0o600); err != nil {
		_ = db.Close()
		return nil, err
	}
	e.localVaultDB = db
	e.localVault = v
	e.localVaultCount = len(docs)
	return v, nil
}

// evalVaultDSN 给派生评测金库补齐与产品侧同款的锁耐心：产品面摄入链
// （localvault.RememberEpisodic）内部含并行索引维护写入，零 busy_timeout 会把
// 普通写-写争用直接变成 SQLITE_BUSY（15000ms 与 db 存储 profile 默认值一致）。
// journal 保持 MEMORY（评测速度），连接池由调用方限制为单连接，并发写入排队而非竞争。
func evalVaultDSN(path string) string {
	dsnPath := path
	if strings.ContainsAny(dsnPath, "?#") {
		dsnPath = ids.SQLiteFileURI(dsnPath)
	}
	return dsnPath + "?_pragma=busy_timeout(15000)"
}

// recallLocalProduct uses the localvault recall layer without the full Memory.Recall delivery pipeline.
func (e *LocalEngine) recallLocalProduct(ctx context.Context, v *localvault.Vault, query string, topK int) ([]local.ResolvedHit, error) {
	tc := localvault.NewLocalClient(v)
	// 评测查询不注入墙钟 TimeRef：产品默认路径使用 now，但该读数不可复现；Hard-31 对照
	// TimeRef=0 与 now 同分，故固定 0（相对日期题如需 now 需显式登记）。
	hits, err := tc.RecallWithOptions(ctx, query, localvault.BlindQueryOptions{TopK: topK})
	if err != nil {
		return nil, err
	}
	if len(hits) > topK {
		hits = hits[:topK]
	}
	out := make([]local.ResolvedHit, 0, len(hits))
	for _, h := range hits {
		out = append(out, local.ResolvedHit{
			QueryHit: local.QueryHit{
				UnitID:         h.UnitID,
				Layer:          h.Layer,
				OccurredAt:     h.OccurredAt,
				AddressURI:     h.AddressURI,
				AddressCoord64: h.AddressCoord64,
				WaveScore:      h.WaveScore,
				ManifoldGain:   h.ManifoldGain,
				FinalWeight:    h.FinalWeight,
			},
			RawPlaintext:       h.RawPlaintext,
			DistilledPlaintext: h.DistilledPlaintext,
			KeptSentences:      h.KeptSentences,
		})
	}
	return out, nil
}

func (e *LocalEngine) ensureCoreRMEngine() (*core.Engine, *plain.TermExtractor, error) {
	e.localVaultMu.Lock()
	defer e.localVaultMu.Unlock()
	if e.coreEngine != nil && e.coreTermExtractor != nil {
		return e.coreEngine, e.coreTermExtractor, nil
	}
	raw, rErr := os.ReadFile(e.StorePath)
	if rErr != nil {
		return nil, nil, rErr
	}
	var storedDocs map[string]struct {
		DocID      string `json:"doc_id"`
		OccurredAt int64  `json:"occurred_at"`
		RawText    string `json:"raw_text"`
	}
	if err := json.Unmarshal(raw, &storedDocs); err != nil || len(storedDocs) == 0 {
		return nil, nil, errors.New("empty or invalid local store")
	}
	if len(storedDocs) < 64 {
		return nil, nil, errors.New("small synthetic fixture store uses standard vault path")
	}
	te := plain.NewTermExtractor(plain.DemoSecret)
	zcsrPath := e.StorePath + ".core-v5.zcsr"
	if eng, err := core.OpenDiskClusterEngine(zcsrPath); err == nil && eng != nil && eng.Index != nil && eng.Index.N == len(storedDocs) {
		e.coreEngine = eng
		e.coreTermExtractor = te
		return eng, te, nil
	}
	if e.Suite == SuiteATM {
		if atmDir, err := ResolveATMDataDir(""); err == nil {
			// RM 用户测评走本地 SGM 加载器（RB 实验层的 atmdata 不分发到本项目）。
			if items, _, _, lErr := LoadATMSGMItems(atmDir, "", 0); lErr == nil && len(items) == len(storedDocs) {
				rmDocs := make([]core.Doc, len(items))
				workers := 8
				var wg sync.WaitGroup
				step := (len(items) + workers - 1) / workers
				for w := 0; w < workers; w++ {
					start := w * step
					if start >= len(items) {
						break
					}
					end := start + step
					if end > len(items) {
						end = len(items)
					}
					wg.Add(1)
					go func(s, eIdx int) {
						defer wg.Done()
						localTe := plain.NewTermExtractor(plain.DemoSecret)
						for i := s; i < eIdx; i++ {
							d := items[i]
							text := d.FullText
							if strings.EqualFold(strings.TrimSpace(d.Modality), "email") {
								text = strings.Join([]string{
									"ID: " + d.ItemID,
									"Timestamp: " + d.Timestamp,
									"Subject: " + d.Subject,
									"Summary: " + d.Summary,
									"Detail: " + d.Detail,
								}, "\n")
							}
							rmDocs[i] = localTe.ExtractDoc(d.ItemID, d.OccurredAt, text)
						}
					}(start, end)
				}
				wg.Wait()
				idx := core.BuildIndex(rmDocs)
				eng := core.NewEngine(idx)
				_ = core.WriteDiskClusterCSR(zcsrPath, eng)
				e.coreEngine = eng
				e.coreTermExtractor = te
				return eng, te, nil
			}
		}
	}
	ids := make([]string, 0, len(storedDocs))
	for id := range storedDocs {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	rmDocs := make([]core.Doc, len(ids))
	workers := 8
	var wg sync.WaitGroup
	step := (len(ids) + workers - 1) / workers
	for w := 0; w < workers; w++ {
		start := w * step
		if start >= len(ids) {
			break
		}
		end := start + step
		if end > len(ids) {
			end = len(ids)
		}
		wg.Add(1)
		go func(s, eIdx int) {
			defer wg.Done()
			localTe := plain.NewTermExtractor(plain.DemoSecret)
			for i := s; i < eIdx; i++ {
				id := ids[i]
				rec := storedDocs[id]
				rmDocs[i] = localTe.ExtractDoc(id, rec.OccurredAt, rec.RawText)
			}
		}(start, end)
	}
	wg.Wait()
	idx := core.BuildIndex(rmDocs)
	eng := core.NewEngine(idx)
	_ = core.WriteDiskClusterCSR(zcsrPath, eng)
	e.coreEngine = eng
	e.coreTermExtractor = te
	return eng, te, nil
}

// queryCoreSnapshot runs one query on a per-caller engine copy. One Engine owns
// one query scratch; the shallow copy plus ResetScratch keeps that contract while
// the shared index and cluster state stay read-only.
//
// The shared engine must already be materialized (or disk-hydrated): the first
// cluster-state build writes derived CSR fields onto Index.
func queryCoreSnapshot(eng *core.Engine, az *plain.TermExtractor, query string, topK int) []core.Hit {
	worker := *eng
	worker.ResetScratch()
	return worker.Query(az, query, topK)
}

type coreQueryWorker struct {
	eng  core.Engine
	base *core.Engine
}

func (e *LocalEngine) acquireCoreWorker(eng *core.Engine) *coreQueryWorker {
	if raw := e.coreWorkerPool.Get(); raw != nil {
		w := raw.(*coreQueryWorker)
		if w.base == eng && w.eng.Index == eng.Index {
			return w
		}
	}
	w := &coreQueryWorker{eng: *eng, base: eng}
	w.eng.ResetScratch()
	return w
}

func (e *LocalEngine) releaseCoreWorker(w *coreQueryWorker, eng *core.Engine) {
	if w == nil || eng == nil || w.base != eng || w.eng.Index != eng.Index {
		return
	}
	e.coreWorkerPool.Put(w)
}

// queryCore runs one query on a pooled per-caller engine copy. Reusing the copy
// keeps the query-local day-cosine memo warm across the questions served by the
// same worker; the core bounds the memo and the index pointer check drops copies
// from a previous generation.
func (e *LocalEngine) queryCore(eng *core.Engine, az *plain.TermExtractor, query string, topK int) []core.Hit {
	w := e.acquireCoreWorker(eng)
	defer e.releaseCoreWorker(w, eng)
	return w.eng.Query(az, query, topK)
}

func (e *LocalEngine) recallLocalPlaintext(ctx context.Context, query string, topK int) ([]local.ResolvedHit, error) {
	if topK <= 0 {
		topK = 10
	}
	if e.localIndexSurface() == LocalIndexSurfaceProduct {
		if eng, az, zErr := e.ensureCoreRMEngine(); zErr == nil && eng != nil && az != nil {
			zHits := e.queryCore(eng, az, query, topK)
			// 可选零交付臂：刚性空洞凭证 + 整查询实质词几乎全为零记载时物理 K=0。
			if evalVoidFloorEnabled() && e.coreTermExtractor != nil && eng.Index != nil {
				certs := e.coreTermExtractor.ArchiveVoidProbe(eng.Index, query)
				report := e.coreTermExtractor.QueryCoverage(eng.Index, query, nil)
				if blockingVoidCertificate(certs, report) {
					zHits = nil
					evalVoidFloorFired.Add(1)
				}
			}
			out := make([]local.ResolvedHit, 0, len(zHits))
			for _, zh := range zHits {
				raw, dist, kept, lErr := e.Store.LoadLocalDocument(zh.UnitID)
				if lErr != nil {
					continue
				}
				out = append(out, local.ResolvedHit{
					QueryHit: local.QueryHit{
						UnitID:       zh.UnitID,
						Layer:        local.LayerEpisodic,
						WaveScore:    zh.Energy,
						FinalWeight:  zh.Energy,
						DirectWeight: zh.Direct,
					},
					RawPlaintext:       raw,
					DistilledPlaintext: dist,
					KeptSentences:      kept,
				})
			}
			e.recordLocalRecallSurface("core.engine.query", nil)
			return out, nil
		}
	}
	v, err := e.ensureLocalPlainVault()
	if err != nil {
		e.recordLocalRecallSurface("vault.initialize", err)
		return nil, err
	}
	if v == nil {
		e.recordLocalRecallSurface("vault.initialize", errors.New("local vault unavailable"))
		return nil, nil
	}
	if e.localIndexSurface() == LocalIndexSurfaceProduct {
		hits, err := e.recallLocalProduct(ctx, v, query, topK)
		e.recordLocalRecallSurface("localvault.recall-with-options", err)
		return hits, err
	}
	hits, err := v.SearchClientSDKDefault(query, topK)
	if err != nil {
		e.recordLocalRecallSurface("vault.search-client-sdk-default", err)
		return nil, err
	}
	out := make([]local.ResolvedHit, 0, len(hits))
	for _, h := range hits {
		raw, dist, kept, err := e.Store.LoadLocalDocument(h.UnitID)
		if err != nil {
			continue
		}
		out = append(out, local.ResolvedHit{
			QueryHit: local.QueryHit{
				UnitID:         h.UnitID,
				Layer:          h.Layer,
				CitedUnitIDs:   h.CitedUnitIDs,
				WaveScore:      h.WaveScore,
				FinalWeight:    h.FinalWeight,
				AddressURI:     h.AddressURI,
				AddressCoord64: h.AddressCoord64,
			},
			RawPlaintext:       raw,
			DistilledPlaintext: dist,
			KeptSentences:      kept,
		})
	}
	e.recordLocalRecallSurface("vault.search-client-sdk-default", nil)
	return out, nil
}

// Recall executes one local offline query. 本地臂零云端：没有远端分支、没有瞬时重试，
// 也不允许任何跨模式回退或补足。
func (e *LocalEngine) Recall(ctx context.Context, query string, topK int) ([]local.ResolvedHit, error) {
	if strings.TrimSpace(query) == "" {
		return nil, errors.New("evaluation recall requires a non-empty query")
	}
	if err := e.CheckHealth(ctx); err != nil {
		return nil, fmt.Errorf("evaluation health check failed: %w", err)
	}
	// 世代在用：读者心跳续期（Admin 批次逐题 recall 也走这里），保证并发「清旧记忆」跳过本世代。
	touchEvalGeneration(e.Scope, evalGenerationRoleReader)
	return e.recallLocalPlaintext(ctx, query, topK)
}

// ReadEvalManifest reads the isolated preparation receipt used by Admin readiness checks.
func ReadEvalManifest(storePath string) (EvalManifest, error) {
	var manifest EvalManifest
	raw, err := os.ReadFile(storePath + ".manifest.json")
	if err != nil {
		return manifest, err
	}
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return manifest, err
	}
	if !IsValidEvalTransport(manifest.Transport) || manifest.LocalFallback || !IsEvalScope(manifest.Scope) {
		return manifest, errors.New("invalid evaluation manifest")
	}
	return manifest, nil
}
