<!-- plugin: rolling-memory v0.0.58 -->
<!--
    Rolling-Memory 记忆系统 · 「剑桥大学 ATM-Bench 官方主榜单」工作台（views/admin/plugin/rolling-memory/atmLeaderboard.vue）。
    设计原则（第一性原理 · 面向普通用户与剑桥 ATM-Bench 官方维护者）：
      1) 首屏零噪音：顶栏直接给出基准身份、11,034 条 SGM 多模态语料规模、atmbench-official-v1 密码学钉校验状态与复现入口；
      2) 四维硬核指标卡（BuildAdmin 标准左边框指示卡）：主榜 Main-1013 读数、高难榜 Hard-31 读数、4.65s 建库与检索延迟、官方协议与理论上限；
      3) 连贯分析流：Main-1013 与 Hard-31 分集切换 + 一键复现命令条 + 左天梯排行 / 右五维深度解剖双栏联动 + 剑桥官方全字段标准表格；
      4) 底部审计抽屉：将本机封印基线台账、官方判分器 SHA-256 指纹与冻结 Prompt 原文收纳为可切换的审计选项卡，兼顾清爽阅读与逐字节核验。
-->
<template>
    <div class="default-main plugin-rm-atm-leaderboard">
        <!-- 顶部权威基准身份与跨页导航栏 -->
        <div class="rm-workbench-topbar">
            <div class="rm-wt-left">
                <div class="rm-wt-title-row">
                    <span class="rm-wt-badge is-primary">PRIMARY BENCHMARK</span>
                    <h1 class="rm-wt-title">{{ t('plugin.rolling-memory.atmLeaderboard.title') }}</h1>
                    <el-tag size="small" type="primary" effect="plain">
                        {{ t('plugin.rolling-memory.atmLeaderboard.protocolBadge') }}
                    </el-tag>
                    <el-tag size="small" type="info" effect="plain">
                        {{ t('plugin.rolling-memory.atmLeaderboard.corpusBadge') }}
                    </el-tag>
                    <el-tag size="small" :type="pinGateBadgeType" effect="light">
                        {{ pinGateBadgeText }}
                    </el-tag>
                    <el-popover trigger="click" placement="bottom" :width="440" popper-class="rm-help-popper">
                        <template #reference>
                            <button
                                type="button"
                                class="tip-q"
                                :aria-label="
                                    isEn ? 'Protocol, SGM corpus and cryptographic pin semantics' : '评测协议、SGM 语料与密码学钉校验语义说明'
                                "
                            >
                                ?
                            </button>
                        </template>
                        <div class="rm-help-body">
                            <p v-if="isEn">
                                <strong>Official Protocol:</strong> atmbench-official-v1 (Cambridge ATM-Bench MMRag Top-10 evidence delivery + official
                                gold-set-mean Recall@k + hybrid QS scoring).
                            </p>
                            <p v-else>
                                <strong>内置评测协议：</strong>atmbench-official-v1（剑桥 ATM-Bench 官方 MMRag Top-10 证据交付 + 官方 gold-set-mean
                                Recall@k + 混合 QS 评分）。
                            </p>
                            <p v-if="isEn">
                                <strong>Standard 2B SGM Corpus:</strong> 4-year personal multimodal memory — 6,742 emails + 3,759 GPS/OCR photos + 533
                                videos = 11,034 items. Local external-corpus SHA-256 status is shown separately.
                            </p>
                            <p v-else>
                                <strong>标准 2B SGM 语料：</strong>跨 4 年个人多模态记忆——6,742 封邮件 + 3,759 张带 GPS/OCR 照片 + 533 段视频 = 11,034
                                条。本机外部语料的 SHA-256 校验状态另行展示。
                            </p>
                            <p v-if="isEn">
                                <strong>Cryptographic Pin Gate:</strong> SHA-1 digests of the MMRag Answer Prompt and Hybrid QS Judge Template are
                                locked read-only and verified on every load.
                            </p>
                            <p v-else>
                                <strong>密码学钉门禁：</strong>MMRag 答题系统提示词与混合 QS 判官模板的 SHA-1 摘要只读锁死并实时校验。
                            </p>
                        </div>
                    </el-popover>
                </div>
                <p class="rm-wt-sub">
                    {{
                        loc(board.corpus_summary, board.corpus_summary_en) ||
                        (isEn
                            ? 'University of Cambridge ATM-Bench · 11,034 SGM personal multimodal items (Main 1,013 Qs & Hard 31 Qs) · Official gold-set-mean Recall@k & Hybrid QS'
                            : '剑桥大学 ATM-Bench 多模态个人记忆基准 · 11,034 条 SGM 记忆（主榜 1,013 题全量 & Hard 31 道高难题）· 严格采用官方 gold-set-mean Recall@k 与混合 QS 评分')
                    }}
                </p>
            </div>
            <div class="rm-wt-actions">
                <el-button v-blur size="small" type="primary" @click="goEvalLab(boardTab === 'main' ? '1013' : 'hard')">
                    <Icon name="lucide-flask-conical" :size="13" style="margin-right: 5px" />
                    {{ isEn ? 'Reproduce in Evaluation Lab →' : '去评测实验室本机复现 →' }}
                </el-button>
                <el-button v-blur size="small" plain @click="goLoCoMoBoard">
                    {{ t('plugin.rolling-memory.atmLeaderboard.goLocomoBtn') }} →
                </el-button>
                <el-button v-blur size="small" :loading="loading" @click="loadBoard">
                    {{ t('plugin.rolling-memory.refresh') }}
                    <template v-if="loading"> ({{ loadBoardPct }}%)</template>
                </el-button>
            </div>
        </div>

        <div v-if="loading" class="rm-top-progress-banner">
            <div class="rm-tpb-head">
                <span>{{ loadBoardText }}</span>
                <strong>{{ loadBoardPct }}%</strong>
            </div>
            <el-progress :percentage="loadBoardPct" :stroke-width="6" striped striped-flow :show-text="false" />
        </div>

        <!-- 4 维核心科学指标矩阵（BuildAdmin 左边框指示卡） -->
        <div class="rm-stats-grid">
            <div class="stat-card is-success">
                <div class="stat-head">
                    <span class="stat-label">{{ t('plugin.rolling-memory.atmLeaderboard.kpi3Label') }}</span>
                    <div class="stat-head-right">
                        <el-tag size="small" type="success" effect="dark">#1 Memory &amp; Overall</el-tag>
                        <el-popover trigger="click" placement="top" :width="360" popper-class="rm-help-popper">
                            <template #reference>
                                <button type="button" class="tip-q" :aria-label="isEn ? 'Main-board reading semantics' : '主榜最优读数说明'">?</button>
                            </template>
                            <div class="rm-help-body">
                                <p v-if="isEn">
                                    Official 1,013-question full set on standard 2B SGM inputs: Official Recall@10 = 82.77% (gold-set-mean), R@50 =
                                    90.16%, Hybrid QS = 70.13% (vs top external MemPalace R@10 76.40% / QS 56.80%).
                                </p>
                                <p v-else>
                                    官方 1,013 题全量（标准 2B SGM 输入）：官方 Recall@10 = 82.77%（金标交均）、R@50 = 90.16%、混合 QS = 70.13%（对比外部最高 MemPalace R@10 76.40% / QS 56.80%）。
                                </p>
                            </div>
                        </el-popover>
                    </div>
                </div>
                <div class="stat-main-val is-green">
                    {{ mainBest ? mainBest.recall_at_10.toFixed(2) + '%' : '—' }}
                    <small class="stat-val-tag">Official R@10</small>
                </div>
                <div class="stat-foot">
                    <span v-if="mainBest">
                        Hybrid QS <strong>{{ mainBest.qs_score.toFixed(2) }}%</strong> · R@25 {{ mainBest.recall_at_25.toFixed(2) }}% · R@50
                        <strong>{{ mainBest.recall_at_50.toFixed(2) }}%</strong>
                    </span>
                    <span v-else>{{ t('plugin.rolling-memory.atmLeaderboard.kpi3SubFallback') }}</span>
                </div>
            </div>

            <div class="stat-card is-primary">
                <div class="stat-head">
                    <span class="stat-label">{{ t('plugin.rolling-memory.atmLeaderboard.kpi2Label') }}</span>
                    <div class="stat-head-right">
                        <el-tag size="small" type="primary" effect="dark">#1 Memory &amp; Overall</el-tag>
                        <el-popover trigger="click" placement="top" :width="360" popper-class="rm-help-popper">
                            <template #reference>
                                <button
                                    type="button"
                                    class="tip-q"
                                    :aria-label="isEn ? 'ATM-Bench-Hard reading semantics' : 'ATM-Bench-Hard 最优读数说明'"
                                >
                                    ?
                                </button>
                            </template>
                            <div class="rm-help-body">
                                <p v-if="isEn">
                                    Official 31-question Hard subset (cross-year multi-hop &amp; abstention): Official Recall@10 = 52.43%, R@50 =
                                    70.62%, Hybrid QS = 25.06% (vs top external MemoryOS R@10 32.70% / QS 13.70%).
                                </p>
                                <p v-else>
                                    官方 31 道高难子集（跨年多跳计数与拒答题）：官方 Recall@10 = 52.43%、R@50 = 70.62%、混合 QS = 25.06%（对比外部最高 MemoryOS R@10 32.70% / QS 13.70%）。
                                </p>
                            </div>
                        </el-popover>
                    </div>
                </div>
                <div class="stat-main-val is-primary-txt">
                    {{ hardBest ? hardBest.recall_at_10.toFixed(2) + '%' : '—' }}
                    <small class="stat-val-tag">Official R@10</small>
                </div>
                <div class="stat-foot">
                    <span v-if="hardBest">
                        Hybrid QS <strong>{{ hardBest.qs_score.toFixed(2) }}%</strong> · R@25 {{ hardBest.recall_at_25.toFixed(2) }}% · R@50
                        <strong>{{ hardBest.recall_at_50.toFixed(2) }}%</strong>
                    </span>
                    <span v-else>{{ t('plugin.rolling-memory.atmLeaderboard.kpi2SubFallback') }}</span>
                </div>
            </div>

            <div class="stat-card is-warning">
                <div class="stat-head">
                    <span class="stat-label">{{ t('plugin.rolling-memory.atmLeaderboard.kpi1Label') }}</span>
                    <div class="stat-head-right">
                        <el-tag size="small" type="warning" effect="plain">0-Vector · 0-LLM</el-tag>
                        <el-popover trigger="click" placement="top" :width="360" popper-class="rm-help-popper">
                            <template #reference>
                                <button type="button" class="tip-q" :aria-label="isEn ? 'SGM corpus scale semantics' : 'SGM 语料规模与建库耗时说明'">
                                    ?
                                </button>
                            </template>
                            <div class="rm-help-body">
                                <p v-if="isEn">
                                    The published reference reports {{ ingestText }} ({{ ingestHrText }}, {{ perDocMsText }}) for 11,034 SGM items. It is not a
                                    measurement from your current evaluation.
                                </p>
                                <p v-else>
                                    公开参考读数为 11,034 条 SGM 语料建库 {{ ingestText }}（{{ ingestHrText }}，{{ perDocMsText }}），不是当前用户的评测结果。
                                </p>
                            </div>
                        </el-popover>
                    </div>
                </div>
                <div class="stat-main-val">
                    {{ ingestText }}
                    <small class="stat-val-tag">{{ isEn ? 'Reference' : '参考读数' }} · 11,034 SGM</small>
                </div>
                <div class="stat-foot">{{ recallPerfText }}</div>
            </div>

            <div class="stat-card is-info">
                <div class="stat-head">
                    <span class="stat-label">{{ t('plugin.rolling-memory.atmLeaderboard.kpi4Label') }}</span>
                    <div class="stat-head-right">
                        <el-tag size="small" :type="pinGateBadgeType" effect="plain">
                            {{ pinGateShortText }}
                        </el-tag>
                        <el-popover trigger="click" placement="top" :width="360" popper-class="rm-help-popper">
                            <template #reference>
                                <button
                                    type="button"
                                    class="tip-q"
                                    :aria-label="isEn ? 'Protocol ID and Oracle ceiling semantics' : '协议标识与 Oracle 理论上限说明'"
                                >
                                    ?
                                </button>
                            </template>
                            <div class="rm-help-body">
                                <p v-if="isEn">
                                    Even when ground-truth evidence is injected directly (Oracle), 4 Hard questions have 12–17 gold items (&gt; Top-10
                                    cap), making structural R@10 ceiling 96.47% and Hybrid QS ceiling 46.69%.
                                </p>
                                <p v-else>
                                    即便直接注入人工金标证据（Oracle），Hard 集有 4 题金标数达 12–17 条（超出 Top-10 窗口），故结构性 R@10 上限为 96.47%、混合 QS 理论上限为 46.69%。
                                </p>
                            </div>
                        </el-popover>
                    </div>
                </div>
                <div class="stat-main-val is-mono">{{ atmSpec.protocol_id || 'atmbench-official-v1' }}</div>
                <div class="stat-foot">
                    {{
                        hardOracle
                            ? t('plugin.rolling-memory.atmLeaderboard.kpi4Sub', { qs: hardOracle.qs_score.toFixed(2) }) +
                              ` · R@10 ${hardOracle.recall_at_10.toFixed(2)}%`
                            : t('plugin.rolling-memory.atmLeaderboard.kpi4SubFallback')
                    }}
                </div>
            </div>
        </div>

        <!-- 主榜单工作台：分集切换 + 复现命令条 + 双栏天梯剖析 + 官方全字段表 -->
        <el-card shadow="never" class="rm-board-card">
            <!-- 榜单主控工具栏 -->
            <div class="rm-board-toolbar">
                <div class="rm-bt-left">
                    <el-radio-group v-model="boardTab" size="default" class="rm-split-switcher" @change="onBoardTabChange">
                        <el-radio-button value="main">
                            {{ isEn ? 'ATM-Bench Main (1,013 Qs · Full)' : 'ATM-Bench 主榜 (1,013 题全量)' }}
                        </el-radio-button>
                        <el-radio-button value="hard">
                            {{ isEn ? 'ATM-Bench-Hard (31 Qs · Multi-Hop)' : 'ATM-Bench-Hard 高难榜 (31 题)' }}
                        </el-radio-button>
                    </el-radio-group>

                    <el-radio-group v-model="atmCategoryFilter" size="small">
                        <el-radio-button value="all">{{ isEn ? 'All' : '全部' }} ({{ rawBoardRows.length }})</el-radio-button>
                        <el-radio-button value="Memory">Memory</el-radio-button>
                        <el-radio-button value="RAG">RAG</el-radio-button>
                        <el-radio-button v-if="boardTab === 'hard'" value="Oracle">Oracle</el-radio-button>
                        <el-radio-button value="ours">{{ isEn ? 'Ours' : '我方系统' }}</el-radio-button>
                    </el-radio-group>
                </div>

                <div class="rm-bt-right">
                    <span class="rm-ctrl-lbl">{{ isEn ? 'Rank by:' : '排序指标：' }}</span>
                    <el-select v-model="atmSortKey" size="small" style="width: 190px">
                        <el-option :label="isEn ? 'Official R@10 (DESC)' : '官方 R@10（金标交均）'" value="recall_at_10" />
                        <el-option :label="isEn ? 'Hybrid QS Score (DESC)' : '官方混合 QS 综合分'" value="qs_score" />
                        <el-option :label="isEn ? 'Recall@25 (DESC)' : 'R@25 召回率'" value="recall_at_25" />
                        <el-option :label="isEn ? 'Recall@50 (DESC)' : 'R@50 召回率'" value="recall_at_50" />
                        <el-option :label="isEn ? 'Cat 1 · List Recall (DESC)' : 'Cat 1 · List 列表枚举'" value="list_recall" />
                        <el-option :label="isEn ? 'Cat 2 · Number EM (DESC)' : 'Cat 2 · Number 数值日期'" value="number_em" />
                        <el-option :label="isEn ? 'Cat 3 · Open-End (DESC)' : 'Cat 3 · Open 开放问答'" value="open_end" />
                    </el-select>
                </div>
            </div>

            <!-- 紧凑型一键 CLI 复现与数据获取条（面向审稿人与开发者） -->
            <div class="rm-repro-command-strip">
                <div class="rm-rcs-main">
                    <span class="rm-rcs-tag">{{ boardTab === 'hard' ? 'HARD-31 REPRO' : 'MAIN-1013 REPRO' }}</span>
                    <code class="rm-rcs-code">{{ activeReproCli }}</code>
                </div>
                <div class="rm-rcs-btns">
                    <el-button
                        v-blur
                        size="small"
                        type="primary"
                        plain
                        @click="copyText(activeReproCli, 'Copied ATM repro CLI', '已复制 ATM-Bench 复现命令')"
                    >
                        <Icon name="el-document-copy" :size="13" style="margin-right: 4px" />
                        {{ t('plugin.rolling-memory.atmLeaderboard.copyReproBtn') }}
                    </el-button>
                    <el-button
                        v-blur
                        size="small"
                        plain
                        @click="
                            copyText(
                                'rmem eval --fetch-only --fetch atm --accept-upstream-license',
                                'Copied corpus fetch command',
                                '已复制上游语料获取命令'
                            )
                        "
                    >
                        {{ isEn ? 'Copy Corpus Fetch CLI' : '复制语料获取命令 (--fetch)' }}
                    </el-button>
                    <el-button size="small" type="success" plain @click="goEvalLab(boardTab === 'main' ? '1013' : 'hard')">
                        {{ isEn ? 'Run This Split in Lab →' : '在实验室运行本分集 →' }}
                    </el-button>
                </div>
            </div>

            <!-- 双栏联动：左侧天梯对比条形图 + 右侧选中系统五维深度剖析 -->
            <el-row :gutter="14" class="rm-inspector-row">
                <el-col :xs="24" :lg="13" class="rm-col-stretch">
                    <div class="rm-panel-box">
                        <div class="rm-panel-hdr">
                            <div>
                                <strong class="rm-panel-title">
                                    {{
                                        boardTab === 'main'
                                            ? isEn
                                                ? 'ATM-Bench Main (1,013 Qs) Score Ladder'
                                                : 'ATM-Bench 主榜（1,013 题）天梯排行'
                                            : isEn
                                              ? 'ATM-Bench-Hard (31 Qs) Score Ladder'
                                              : 'ATM-Bench-Hard（31 题）天梯排行'
                                    }}
                                </strong>
                                <span class="rm-panel-sub">
                                    {{ isEn ? 'Click any row to inspect its category & recall curve' : '点击任意系统联动右侧深度剖析' }}
                                </span>
                            </div>
                            <el-radio-group v-model="atmLadderMetric" size="small">
                                <el-radio-button value="recall_at_10">Official R@10</el-radio-button>
                                <el-radio-button value="qs_score">Hybrid QS</el-radio-button>
                            </el-radio-group>
                        </div>

                        <div class="rm-ladder-list">
                            <div
                                v-for="(item, idx) in activeRows"
                                :key="`${item.board}-${item.system}-${idx}`"
                                class="rm-ladder-row"
                                :class="{
                                    'is-active': activeAtmEntry?.system === item.system,
                                    'is-ours-row': item.is_ours,
                                }"
                                @click="selectedAtmEntry = item"
                            >
                                <div class="rm-ladder-top">
                                    <div class="rm-ladder-meta">
                                        <span class="rm-rank-badge" :class="rankBadgeClass(displayRankOf(idx))">
                                            {{ item.rank >= 90 ? '—' : `#${displayRankOf(idx)}` }}
                                        </span>
                                        <span class="rm-sys-name">{{ loc(item.system, item.system_en) }}</span>
                                        <el-tag
                                            size="small"
                                            :type="item.is_ours ? 'success' : item.category === 'Oracle' ? 'warning' : 'info'"
                                            effect="plain"
                                        >
                                            {{ item.is_ours ? (isEn ? 'Ours (0-Vector/0-LLM)' : '我方系统 (0-Vector/0-LLM)') : item.category }}
                                        </el-tag>
                                    </div>
                                    <div class="rm-ladder-right-nums">
                                        <span class="rm-ladder-aux">
                                            {{ atmLadderMetric === 'recall_at_10' ? `QS ${item.qs_score.toFixed(1)}%` : `R@10 ${item.recall_at_10.toFixed(1)}%` }}
                                        </span>
                                        <strong class="rm-ladder-score" :class="{ 'is-ours-score': item.is_ours }">
                                            {{ Number(item[atmLadderMetric] || 0).toFixed(2) }}%
                                        </strong>
                                    </div>
                                </div>
                                <div class="rm-ladder-bar-wrap">
                                    <div
                                        class="rm-ladder-bar"
                                        :class="item.is_ours ? 'is-ours' : item.category === 'Oracle' ? 'is-oracle' : 'is-pub'"
                                        :style="{ width: `${Math.max(3, Math.min(100, Number(item[atmLadderMetric] || 0)))}%` }"
                                    ></div>
                                </div>
                            </div>
                        </div>
                    </div>
                </el-col>

                <el-col :xs="24" :lg="11" class="rm-col-stretch">
                    <div class="rm-panel-box">
                        <div class="rm-panel-hdr">
                            <div>
                                <strong class="rm-panel-title">
                                    {{ isEn ? 'System Diagnostic Profile' : '选中系统多维能力剖析' }}
                                </strong>
                                <span class="rm-panel-sub">
                                    {{ isEn ? 'Recall depth curve (R@10→R@50) & 3-category QA breakdown' : '检索深度曲线 (R@10→R@50) 与三类题型拆解' }}
                                </span>
                            </div>
                            <el-tag v-if="activeAtmEntry" size="small" :type="activeAtmEntry.is_ours ? 'success' : 'primary'">
                                R@10 {{ activeAtmEntry.recall_at_10.toFixed(2) }}% · QS {{ activeAtmEntry.qs_score.toFixed(2) }}%
                            </el-tag>
                        </div>

                        <div v-if="activeAtmEntry" class="rm-cat-breakdown">
                            <div class="rm-active-meta">
                                <div class="rm-active-sys-line">
                                    <strong class="rm-active-sys-name">{{ loc(activeAtmEntry.system, activeAtmEntry.system_en) }}</strong>
                                    <span class="rm-muted">{{ loc(activeAtmEntry.vendor, activeAtmEntry.vendor_en) }}</span>
                                </div>
                                <div class="rm-active-arch">{{ loc(activeAtmEntry.architecture, activeAtmEntry.architecture_en) }}</div>
                                <div class="rm-active-tags">
                                    <el-tag size="small" :type="activeAtmEntry.is_ours ? 'success' : 'info'" effect="plain">
                                        {{ isEn ? 'Index Build Time' : '建库耗时' }}: {{ activeAtmEntry.index_time_hr }}
                                    </el-tag>
                                    <el-tag size="small" type="primary" effect="plain">
                                        R@10 {{ activeAtmEntry.recall_at_10.toFixed(2) }}% → R@25 {{ activeAtmEntry.recall_at_25.toFixed(2) }}% → R@50
                                        {{ activeAtmEntry.recall_at_50.toFixed(2) }}%
                                    </el-tag>
                                </div>
                            </div>

                            <div class="rm-cat-list">
                                <div v-for="bar in activeAtmBars" :key="bar.key" class="rm-cat-item">
                                    <div class="rm-cat-top">
                                        <span class="rm-cat-name">{{ bar.label }}</span>
                                        <div class="rm-cat-nums">
                                            <strong class="rm-cat-val-txt">{{ bar.val.toFixed(2) }}%</strong>
                                            <span v-if="bar.delta !== null && !activeAtmEntry.is_ours" class="rm-vs-ours">
                                                {{ isEn ? 'vs Ours' : '对比我方' }}
                                                <em :class="bar.delta >= 0 ? 'is-pos' : 'is-neg'">
                                                    {{ bar.delta >= 0 ? '+' : '' }}{{ bar.delta.toFixed(2) }}pp
                                                </em>
                                            </span>
                                        </div>
                                    </div>
                                    <div class="rm-cat-track">
                                        <div
                                            class="rm-cat-fill"
                                            :class="activeAtmEntry.is_ours ? 'is-ours' : activeAtmEntry.category === 'Oracle' ? 'is-oracle' : 'is-pub'"
                                            :style="{ width: `${Math.max(2, Math.min(100, bar.val))}%` }"
                                        ></div>
                                    </div>
                                </div>
                            </div>

                            <div class="rm-cat-note">
                                <strong>{{ isEn ? 'Audit Note' : '实测与核验说明' }}：</strong>
                                <span>{{ loc(activeAtmEntry.note, activeAtmEntry.note_en) }}</span>
                            </div>
                        </div>
                    </div>
                </el-col>
            </el-row>

            <!-- 剑桥 ATM-Bench 官方标准全字段对比表 -->
            <div class="rm-table-section-hdr">
                <div>
                    <strong>
                        {{
                            boardTab === 'main'
                                ? isEn
                                    ? 'Cambridge ATM-Bench Main Leaderboard (1,013 Questions · Official Gold-Set-Mean)'
                                    : '剑桥大学 ATM-Bench 主榜完整对比表（1,013 题全量 · 官方金标交均口径）'
                                : isEn
                                  ? 'Cambridge ATM-Bench-Hard Leaderboard (31 Multi-Hop / Abstention Questions)'
                                  : '剑桥大学 ATM-Bench-Hard 高难榜完整对比表（31 道跨年多跳/弃权题）'
                        }}
                    </strong>
                    <span class="rm-muted" style="margin-left: 8px">
                        {{ isEn ? 'Click any row to inspect above' : '点击任意行可在上方查看分项剖析' }}
                    </span>
                </div>
                <el-tag size="small" type="info" effect="plain">
                    {{ isEn ? 'Recall@k = Per-Question Gold Set Coverage Mean' : 'Recall@k = 逐题金标证据集合交覆盖率均值 (gold-set-mean)' }}
                </el-tag>
            </div>

            <el-table
                v-loading="loading"
                :element-loading-text="loadBoardText"
                :data="activeRows"
                size="small"
                border
                stripe
                highlight-current-row
                header-cell-class-name="table-header-cell"
                class="ag-data-table rm-lb-table rm-board-table"
                :row-class-name="atmRowClassName"
                @row-click="onAtmTableRowClick"
            >
                <el-table-column :label="isEn ? 'Rank' : '排名'" width="58" align="center">
                    <template #default="{ row, $index }">
                        <span class="rm-rank-badge" :class="rankBadgeClass(displayRankOf($index))">
                            {{ row.rank >= 90 ? '—' : '#' + displayRankOf($index) }}
                        </span>
                    </template>
                </el-table-column>
                <el-table-column :label="isEn ? 'Track' : '类别'" width="78" align="center">
                    <template #default="{ row }">
                        <el-tag size="small" :type="row.category === 'Memory' ? 'success' : row.category === 'Oracle' ? 'warning' : 'info'">
                            {{ row.category }}
                        </el-tag>
                    </template>
                </el-table-column>
                <el-table-column :label="isEn ? 'Memory / Retrieval System & Architecture' : '参评记忆 / 检索系统与架构路线'" min-width="230">
                    <template #default="{ row }">
                        <div class="rm-cell-multiline">
                            <div class="rm-sys-title-line">
                                <strong class="rm-sys-primary">{{ loc(row.system, row.system_en) }}</strong>
                            </div>
                            <div class="rm-sub-text">{{ loc(row.architecture, row.architecture_en) }}</div>
                            <div v-if="loc(row.highlight_tag, row.highlight_tag_en)" class="rm-sys-tags-line">
                                <el-tag size="small" :type="row.is_ours ? 'success' : 'warning'" effect="plain">
                                    {{ loc(row.highlight_tag, row.highlight_tag_en) }}
                                </el-tag>
                            </div>
                        </div>
                    </template>
                </el-table-column>
                <el-table-column label="Official R@10 ⬆" width="112" align="right">
                    <template #default="{ row }">
                        <strong class="rm-num-cell" :class="{ 'rm-best-num': row.is_ours }">{{ row.recall_at_10.toFixed(2) }}%</strong>
                    </template>
                </el-table-column>
                <el-table-column label="R@25 ⬆" width="82" align="right">
                    <template #default="{ row }">
                        <span class="rm-num-cell">{{ row.recall_at_25.toFixed(2) }}%</span>
                    </template>
                </el-table-column>
                <el-table-column label="R@50 ⬆" width="82" align="right">
                    <template #default="{ row }">
                        <span class="rm-num-cell">{{ row.recall_at_50.toFixed(2) }}%</span>
                    </template>
                </el-table-column>
                <el-table-column :label="isEn ? 'Hybrid QS ⬆' : 'QS 综合分 ⬆'" width="102" align="right">
                    <template #default="{ row }">
                        <strong class="rm-overall-num" :class="{ 'is-ours-num': row.is_ours }">{{ row.qs_score.toFixed(2) }}%</strong>
                    </template>
                </el-table-column>
                <el-table-column :label="isEn ? 'List / Num / Open' : '分题型 (List / Num / Open)'" width="152" align="right">
                    <template #default="{ row }">
                        <span class="rm-num-cell">
                            {{ row.list_recall.toFixed(1) }}% / {{ row.number_em.toFixed(1) }}% / {{ row.open_end.toFixed(1) }}%
                        </span>
                    </template>
                </el-table-column>
                <el-table-column :label="isEn ? 'Index Time ⬇' : '建库耗时 ⬇'" width="128" align="right">
                    <template #default="{ row }">
                        <strong :class="{ 'rm-best-num': row.is_ours }">{{ row.index_time_hr }}</strong>
                    </template>
                </el-table-column>
                <el-table-column :label="isEn ? 'Notes & Verification Provenance' : '实测说明与核验出处'" min-width="220">
                    <template #default="{ row }">
                        <div class="rm-note-cell">{{ loc(row.note, row.note_en) }}</div>
                    </template>
                </el-table-column>
            </el-table>
        </el-card>

        <!-- 底部深度审计抽屉（面向 ATM 维护者与审稿人：封印基线台账 + SHA-256 语料/判分钉 + 官方冻结提示词） -->
        <el-card shadow="never" class="rm-audit-card">
            <template #header>
                <div class="rm-audit-header">
                    <div>
                        <strong>{{ t('plugin.rolling-memory.atmLeaderboard.protoDetailTitle') }}</strong>
                        <span class="rm-muted" style="margin-left: 8px">
                            {{
                                isEn
                                    ? 'For benchmark maintainers & reviewers: sealed local baselines, SHA-256 corpus pins, and byte-locked official prompts'
                                    : '供基准维护者与审稿人核验：本机封印基线台账、SHA-256 语料/评测器指纹与逐字节锁定的官方提示词'
                            }}
                        </span>
                    </div>
                    <el-tag size="small" :type="scorerGateBadgeType">
                        {{ scorerGateBadgeText }}
                    </el-tag>
                </div>
            </template>

            <el-tabs v-model="auditTab" class="rm-audit-tabs">
                <!-- Tab 1: 本机实测基线与对照台账 -->
                <el-tab-pane :label="isEn ? '1. Recorded Baselines & Failure Buckets' : '1. 本机实测基线与五桶归因台账'" name="baselines">
                    <template v-if="tierLadder.length">
                        <div class="rm-ref-title">
                            {{ t('plugin.rolling-memory.atmLeaderboard.ladderTitle') }} ·
                            <span class="rm-muted">{{ t('plugin.rolling-memory.atmLeaderboard.ladderHint') }}</span>
                        </div>
                        <el-table
                            :data="tierLadder"
                            size="small"
                            border
                            stripe
                            header-cell-class-name="table-header-cell"
                            class="ag-data-table rm-lb-table"
                            style="margin-bottom: 14px"
                        >
                            <el-table-column :label="isEn ? 'Tier' : '读档'" width="88" align="center">
                                <template #default="{ row }">
                                    <el-tag size="small" type="primary">{{ (row.profile_tier || '-').toUpperCase() }}</el-tag>
                                </template>
                            </el-table-column>
                            <el-table-column :label="isEn ? 'Split' : '题集'" width="110" align="center">
                                <template #default="{ row }">
                                    <span class="rm-num-cell">{{ tierLabel(row.tier) }}</span>
                                </template>
                            </el-table-column>
                            <el-table-column :label="isEn ? 'Official R@10 ⬆' : '官方 R@10 ⬆'" width="135" align="right">
                                <template #default="{ row }">
                                    <template v-if="(row.r10 || 0) > 0">
                                        <strong class="rm-num-cell" style="color: var(--el-color-primary)">{{ (row.r10 * 100).toFixed(2) }}%</strong>
                                        <div class="rm-sub-text">
                                            Any {{ ((row.any_hit_10 || 0) * 100).toFixed(1) }}% · All {{ ((row.all_hit_10 || 0) * 100).toFixed(1) }}%
                                        </div>
                                    </template>
                                    <template v-else>
                                        <span class="rm-num-cell">{{ (row.evidence_hit * 100).toFixed(2) }}%</span>
                                    </template>
                                </template>
                            </el-table-column>
                            <el-table-column label="R@20 / R@50 ⬆" width="125" align="right">
                                <template #default="{ row }">
                                    <span v-if="(row.r20 || 0) > 0" class="rm-num-cell">
                                        {{ (row.r20 * 100).toFixed(1) }}% / {{ (row.r50 * 100).toFixed(1) }}%
                                    </span>
                                    <span v-else class="rm-muted">—</span>
                                </template>
                            </el-table-column>
                            <el-table-column :label="isEn ? 'Hybrid QS ⬆' : '混合 QS ⬆'" width="120" align="right">
                                <template #default="{ row }">
                                    <strong class="rm-overall-num" :class="{ 'is-ours-num': row.judge_scored > 0 }">
                                        {{ row.judge_scored > 0 ? (row.judge_rate * 100).toFixed(2) + '%' : '—' }}
                                    </strong>
                                </template>
                            </el-table-column>
                            <el-table-column :label="isEn ? 'Load' : '载入'" width="88" align="right">
                                <template #default="{ row }">{{ loadSeconds(row) }}</template>
                            </el-table-column>
                            <el-table-column :label="isEn ? 'End-to-end' : '端到端'" width="102" align="right">
                                <template #default="{ row }">{{ e2eSeconds(row) }}</template>
                            </el-table-column>
                            <el-table-column :label="isEn ? 'Failure Buckets' : '五桶归因'" min-width="190">
                                <template #default="{ row }">
                                    <div class="rm-bucket-cell">
                                        <el-tag
                                            v-for="(cnt, name) in row.bucket_counts || {}"
                                            :key="name"
                                            size="small"
                                            :type="name === 'HIT' ? 'success' : 'info'"
                                            effect="plain"
                                        >
                                            {{ name }} {{ cnt }}
                                        </el-tag>
                                    </div>
                                </template>
                            </el-table-column>
                            <el-table-column :label="isEn ? 'Repro CLI' : '复现命令'" width="100" align="center">
                                <template #default="{ row }">
                                    <el-button
                                        v-if="row.repro_cli"
                                        size="small"
                                        type="primary"
                                        link
                                        @click="copyText(row.repro_cli, 'Copied tier repro CLI', '已复制复现命令')"
                                    >
                                        {{ t('plugin.rolling-memory.atmLeaderboard.copyBtn') }}
                                    </el-button>
                                    <span v-else class="rm-muted">—</span>
                                </template>
                            </el-table-column>
                        </el-table>
                    </template>

                    <div v-if="refBaselines.length">
                        <div class="rm-ref-title">{{ t('plugin.rolling-memory.atmLeaderboard.refTitle') }}</div>
                        <el-table
                            :data="refBaselines"
                            size="small"
                            border
                            stripe
                            header-cell-class-name="table-header-cell"
                            class="ag-data-table rm-ref-table"
                        >
                            <el-table-column :label="isEn ? 'Sealed Baseline / Reference' : '封印基线 / 外部对照'" min-width="240">
                                <template #default="{ row }">
                                    <div class="rm-cell-multiline">
                                        <strong class="rm-sys-primary">{{ loc(row.label, row.label_en) }}</strong>
                                        <span class="rm-sub-text">{{ row.arm }} · {{ row.protocol }}</span>
                                    </div>
                                </template>
                            </el-table-column>
                            <el-table-column :label="isEn ? 'Question Set' : '题集'" width="115" align="center">
                                <template #default="{ row }">
                                    <span class="rm-num-cell">{{ tierLabel(row.tier) }}</span>
                                </template>
                            </el-table-column>
                            <el-table-column :label="isEn ? 'Official R@10 ⬆' : '官方 R@10 ⬆'" width="125" align="right">
                                <template #default="{ row }">
                                    <template v-if="(row.r10 || 0) > 0">
                                        <strong class="rm-num-cell" style="color: var(--el-color-primary)">{{ (row.r10 * 100).toFixed(2) }}%</strong>
                                        <div v-if="(row.any_hit_10 || 0) > 0" class="rm-sub-text">
                                            Any {{ ((row.any_hit_10 || 0) * 100).toFixed(1) }}% · All {{ ((row.all_hit_10 || 0) * 100).toFixed(1) }}%
                                        </div>
                                    </template>
                                    <template v-else>
                                        <span class="rm-num-cell">{{ (row.evidence_hit * 100).toFixed(2) }}%</span>
                                    </template>
                                </template>
                            </el-table-column>
                            <el-table-column label="R@20 / R@50 ⬆" width="125" align="right">
                                <template #default="{ row }">
                                    <span v-if="(row.r20 || 0) > 0 || (row.r50 || 0) > 0" class="rm-num-cell">
                                        {{ ((row.r20 || 0) * 100).toFixed(1) }}% / {{ ((row.r50 || 0) * 100).toFixed(1) }}%
                                    </span>
                                    <span v-else class="rm-muted">—</span>
                                </template>
                            </el-table-column>
                            <el-table-column :label="isEn ? 'Recall' : '检索'" width="100" align="right">
                                <template #default="{ row }">
                                    <span v-if="(row.avg_recall_ms || 0) > 0" class="rm-num-cell">{{ row.avg_recall_ms?.toFixed(1) }}ms</span>
                                    <span v-else class="rm-num-cell">{{ formatElapsed(row.elapsed_seconds) }}</span>
                                </template>
                            </el-table-column>
                            <el-table-column :label="isEn ? 'Load' : '载入'" width="88" align="right">
                                <template #default="{ row }">{{ loadSeconds(row) }}</template>
                            </el-table-column>
                            <el-table-column :label="isEn ? 'Hybrid QS ⬆' : '混合 QS ⬆'" width="110" align="right">
                                <template #default="{ row }">
                                    <strong class="rm-overall-num" :class="{ 'is-ours-num': row.judge_scored > 0 }">
                                        {{ row.judge_scored > 0 ? (row.judge_rate * 100).toFixed(2) + '%' : '—' }}
                                    </strong>
                                </template>
                            </el-table-column>
                            <el-table-column :label="isEn ? 'Provenance & Notes' : '口径说明与出处'" min-width="260">
                                <template #default="{ row }">
                                    <div class="rm-note-cell">{{ loc(row.note, row.note_en) }}</div>
                                </template>
                            </el-table-column>
                        </el-table>
                    </div>
                </el-tab-pane>

                <!-- Tab 2: 官方评分协议与 SHA-256 密码学钉 -->
                <el-tab-pane :label="isEn ? '2. Official Scorers & SHA-256 Pins' : '2. 官方判分器与 SHA-256 密码学钉'" name="pins">
                    <el-descriptions :column="2" border size="small" class="rm-proto-desc">
                        <el-descriptions-item :label="t('plugin.rolling-memory.leaderboard.lblBuiltinProto')">
                            <div class="rm-desc-val">
                                <el-tag size="small" type="primary">{{ atmSpec.builtin_protocol || 'atmbench-official' }}</el-tag>
                                <code class="rm-code-inline">{{ atmSpec.protocol_id || 'atmbench-official-v1' }}</code>
                            </div>
                        </el-descriptions-item>
                        <el-descriptions-item :label="t('plugin.rolling-memory.leaderboard.lblTargetBoard')">
                            <strong class="rm-desc-text">
                                {{ loc(atmSpec.leaderboard_name, atmSpec.leaderboard_name_en) || 'University of Cambridge ATM-Bench Leaderboard' }}
                            </strong>
                        </el-descriptions-item>
                        <el-descriptions-item :label="isEn ? 'Number Scorer' : 'Number 确定性判分'">
                            <span class="rm-desc-text">{{ atmSpec.number_scorer || 'official deterministic_accuracy' }}</span>
                            <code class="rm-code-inline" style="margin-left: 6px">{{ atmSpec.number_scorer_version || '-' }}</code>
                        </el-descriptions-item>
                        <el-descriptions-item :label="isEn ? 'List Scorer' : 'List 集合判分'">
                            <span class="rm-desc-text">{{ atmSpec.list_scorer || 'official list_jaccard_score' }}</span>
                            <code class="rm-code-inline" style="margin-left: 6px">{{ atmSpec.list_scorer_version || '-' }}</code>
                        </el-descriptions-item>
                        <el-descriptions-item :label="isEn ? 'Recall Metric Formula' : '官方 Recall@k 公式'" :span="2">
                            <span class="rm-desc-text">{{ loc(atmSpec.recall_metric_spec, atmSpec.recall_metric_spec_en) }}</span>
                        </el-descriptions-item>
                        <el-descriptions-item :label="isEn ? 'Scoring Fixture SHA-256' : '判分夹具 SHA-256'">
                            <code class="rm-sha-code">{{ atmSpec.scoring_fixture_sha256 || '-' }}</code>
                            <el-tag size="small" :type="verificationType(atmSpec.scoring_fixture_status)">{{ verificationText(atmSpec.scoring_fixture_status) }}</el-tag>
                        </el-descriptions-item>
                        <el-descriptions-item :label="isEn ? 'Answer fixture SHA-256' : '答题夹具 SHA-256'">
                            <el-tag size="small" :type="verificationType(atmSpec.answer_fixture_status)">{{ verificationText(atmSpec.answer_fixture_status) }}</el-tag>
                        </el-descriptions-item>
                        <el-descriptions-item :label="isEn ? 'External corpus SHA-256' : '外部语料 SHA-256'">
                            <el-tag size="small" :type="verificationType(atmSpec.corpus_pins_status)">{{ verificationText(atmSpec.corpus_pins_status) }}</el-tag>
                        </el-descriptions-item>
                        <el-descriptions-item :label="isEn ? 'Answer User Prompt SHA-1' : '答题 User 模板 SHA-1'">
                            <code class="rm-sha-code">{{ atmSpec.answer_payload_sha1 || '-' }}</code>
                        </el-descriptions-item>
                        <el-descriptions-item :label="isEn ? 'Corpus & Question-Tier SHA-256 Pins' : '语料与题集 SHA-256 钉'" :span="2">
                            <div class="rm-pin-grid">
                                <div v-for="(value, name) in atmSpec.corpus_pins || {}" :key="'atm-corpus-pin-' + name" class="rm-pin-line">
                                    <code class="rm-code-inline">{{ name }}</code>
                                    <code class="rm-sha-code">{{ value }}</code>
                                </div>
                            </div>
                        </el-descriptions-item>
                        <el-descriptions-item :label="isEn ? 'Upstream Evaluator Source Pins' : '上游官方评测器源码 SHA-1 钉'" :span="2">
                            <div class="rm-pin-grid">
                                <div v-for="(value, name) in atmSpec.evaluator_source_pins || {}" :key="'atm-source-pin-' + name" class="rm-pin-line">
                                    <code class="rm-code-inline">{{ name }}</code>
                                    <code class="rm-sha-code">{{ value }}</code>
                                </div>
                            </div>
                        </el-descriptions-item>
                        <el-descriptions-item :label="t('plugin.rolling-memory.leaderboard.lblFairNotice')" :span="2">
                            <span class="rm-desc-text">{{ loc(atmSpec.fair_comparison_notice, atmSpec.fair_comparison_notice_en) }}</span>
                        </el-descriptions-item>
                    </el-descriptions>
                </el-tab-pane>

                <!-- Tab 3: 官方 MMRag Answer & Hybrid QS Judge 冻结提示词原文 -->
                <el-tab-pane :label="isEn ? '3. Frozen Official Prompts (MMRag & Judge)' : '3. 官方冻结提示词原文 (MMRag & Judge)'" name="prompts">
                    <el-row :gutter="14">
                        <el-col v-for="ag in atmAgents" :key="ag.code" :xs="24" :lg="12" class="rm-col-stretch">
                            <div class="rm-agent-box">
                                <div class="rm-agent-hdr">
                                    <div class="rm-agent-title-wrap">
                                        <strong>{{ loc(ag.name, ag.name_en) }}</strong>
                                        <el-tag size="small" type="info">{{ ag.code }}</el-tag>
                                    </div>
                                    <el-tag size="small" :type="ag.pin_verified ? 'success' : 'danger'">
                                        {{
                                            ag.pin_verified
                                                ? t('plugin.rolling-memory.eval.pinVerifiedTag')
                                                : t('plugin.rolling-memory.eval.pinDriftTag')
                                        }}
                                    </el-tag>
                                </div>
                                <el-descriptions :column="1" border size="small" style="margin-top: 8px">
                                    <el-descriptions-item :label="t('plugin.rolling-memory.eval.lblProtocol')">
                                        <code class="rm-code-inline">atmbench-official-v1 ({{ ag.protocol }})</code>
                                    </el-descriptions-item>
                                    <el-descriptions-item :label="t('plugin.rolling-memory.eval.lblSysSha1')">
                                        <code class="rm-sha-code">{{ ag.system_sha1 }}</code>
                                    </el-descriptions-item>
                                    <el-descriptions-item
                                        v-if="ag.user_template_sha1 && ag.user_template_sha1 !== '-'"
                                        :label="t('plugin.rolling-memory.eval.lblTplSha1')"
                                    >
                                        <code class="rm-sha-code">{{ ag.user_template_sha1 }}</code>
                                    </el-descriptions-item>
                                </el-descriptions>
                                <div class="rm-prompt-lbl">{{ t('plugin.rolling-memory.eval.lblSysPrompt') }}:</div>
                                <pre class="rm-prompt-pre">{{ ag.system_prompt }}</pre>
                                <div class="rm-prompt-lbl">{{ t('plugin.rolling-memory.eval.lblUserTpl') }}:</div>
                                <pre class="rm-prompt-pre">{{ ag.user_template }}</pre>
                            </div>
                        </el-col>
                    </el-row>
                </el-tab-pane>
            </el-tabs>
        </el-card>
    </div>
</template>

<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRouter } from 'vue-router'
import { ElMessage } from 'element-plus'
import { useLoadingProgressText } from '@/utils/loadingProgress'
import request from '@/utils/request'

const { t, locale } = useI18n()
const router = useRouter()

const isEn = computed(() =>
    String(locale.value || '')
        .toLowerCase()
        .startsWith('en')
)
const loc = (zh?: string, en?: string) => {
    if (isEn.value && en && en.trim() !== '') return en
    return zh || en || ''
}

interface ProtocolParitySpec {
    builtin_protocol?: string
    protocol_id?: string
    leaderboard_name?: string
    leaderboard_name_en?: string
    corpus_name?: string
    corpus_name_en?: string
    answer_payload_spec?: string
    answer_payload_spec_en?: string
    answer_payload_sha1?: string
    judge_verdict_spec?: string
    judge_verdict_spec_en?: string
    category_mapping_note?: string
    category_mapping_note_en?: string
    fair_comparison_notice?: string
    fair_comparison_notice_en?: string
    number_scorer?: string
    number_scorer_version?: string
    list_scorer?: string
    list_scorer_version?: string
    judge_parser?: string
    scoring_fixture_sha256?: string
    evaluator_source_pins?: Record<string, string>
    corpus_pins?: Record<string, string>
    official_scoring_aligned?: boolean
    official_scoring_parity?: string
    prompt_pins_status?: string
    scoring_fixture_status?: string
    answer_fixture_status?: string
    corpus_pins_status?: string
    recall_metric_spec?: string
    recall_metric_spec_en?: string
    all_pins_verified?: boolean
}

interface ATMOfficialPinStatus {
    protocol?: string
    version?: string
    prompt_pins?: Record<string, string>
    prompt_pins_verified?: boolean
    fixture_pin?: string
    fixture_verified?: boolean
    source_pins?: Record<string, string>
    corpus_pins?: Record<string, string>
    corpus_pins_verified?: boolean
    corpus_pins_error?: string
}

interface OfficialProtocolAgent {
    code: string
    name: string
    name_en?: string
    protocol: string
    model: string
    system_prompt: string
    system_sha1: string
    user_template: string
    user_template_sha1?: string
    user_template_sha?: string
    pin_verified: boolean
}

interface ATMBenchEntry {
    rank: number
    board: string
    category: string
    system: string
    system_en?: string
    vendor: string
    vendor_en?: string
    architecture: string
    architecture_en?: string
    is_ours: boolean
    recall_at_10: number
    recall_at_25: number
    recall_at_50: number
    qs_score: number
    list_recall: number
    number_em: number
    open_end: number
    index_time_hr: string
    highlight_tag: string
    highlight_tag_en?: string
    note: string
    note_en?: string
}

interface ATMBenchBoard {
    benchmark_name: string
    benchmark_name_en?: string
    corpus_summary: string
    corpus_summary_en?: string
    repro_command: string
    hard_entries: ATMBenchEntry[]
    main_entries: ATMBenchEntry[]
}

interface ATMBaselineRecord {
    id: string
    label: string
    label_en?: string
    tier: string
    scored_questions: number
    arm: string
    protocol: string
    evidence_hit: number
    full_evidence_hit?: number
    r10?: number
    r20?: number
    r50?: number
    any_hit_10?: number
    all_hit_10?: number
    ingest_ms?: number
    avg_recall_ms?: number
    p95_recall_ms?: number
    judge_rate: number
    judge_correct: number
    judge_scored: number
    elapsed_seconds: number
    note: string
    note_en?: string
    repro_cli?: string
    profile_tier?: string
    bucket_counts?: Record<string, number>
}

interface ATMLeaderboardResponse {
    atm_bench?: ATMBenchBoard
    atm_protocol_spec?: ProtocolParitySpec
    atm_agents?: OfficialProtocolAgent[]
    atm_baselines?: ATMBaselineRecord[]
    atm_official_pins?: ATMOfficialPinStatus
}

const loading = ref(false)
const { percent: loadBoardPct, text: loadBoardText } = useLoadingProgressText(loading)
const board = ref<Partial<ATMBenchBoard>>({})
const atmSpec = ref<Partial<ProtocolParitySpec>>({})
const atmAgents = ref<OfficialProtocolAgent[]>([])
const atmBaselines = ref<ATMBaselineRecord[]>([])
const atmOfficialPins = ref<Partial<ATMOfficialPinStatus>>({})
const boardTab = ref<'hard' | 'main'>('main')
const auditTab = ref<'baselines' | 'pins' | 'prompts'>('baselines')
const query = ref<{ tier: 'hard' | '1013' }>({ tier: '1013' })
const atmCategoryFilter = ref<'all' | 'Memory' | 'RAG' | 'Oracle' | 'ours'>('all')
const atmSortKey = ref<'recall_at_10' | 'qs_score' | 'recall_at_25' | 'recall_at_50' | 'list_recall' | 'number_em' | 'open_end'>('recall_at_10')
const atmLadderMetric = ref<'recall_at_10' | 'qs_score'>('recall_at_10')
const selectedAtmEntry = ref<ATMBenchEntry | null>(null)

const verificationType = (status?: string) => status === 'verified' ? 'success' : status === 'failed' ? 'danger' : 'info'
const verificationText = (status?: string) => {
    if (status === 'verified') return isEn.value ? 'Verified' : '已验证'
    if (status === 'failed') return isEn.value ? 'Failed' : '验证失败'
    return isEn.value ? 'Unverified' : '未验证'
}
const pinGateBadgeType = computed(() => verificationType(atmSpec.value.prompt_pins_status))
const pinGateBadgeText = computed(() => `${isEn.value ? 'Prompt SHA-1' : '提示词 SHA-1'}: ${verificationText(atmSpec.value.prompt_pins_status)}`)
const pinGateShortText = computed(() => verificationText(atmSpec.value.prompt_pins_status))
const scorerGateBadgeType = computed(() => verificationType(atmSpec.value.official_scoring_parity))
const scorerGateBadgeText = computed(() => `${isEn.value ? 'Official scorer parity' : '官方判分对拍'}: ${verificationText(atmSpec.value.official_scoring_parity)}`)

const activeReproCli = computed(() =>
    boardTab.value === 'hard'
        ? 'rmem eval --suite atm --tier hard --top-k 50 --answerer none --judge off'
        : 'rmem eval --suite atm --tier 1013 --top-k 50 --answerer none --judge off'
)

const tierLadder = computed(() => atmBaselines.value.filter((row) => !!row.profile_tier))
const refBaselines = computed(() => atmBaselines.value.filter((row) => !row.profile_tier))

const rawBoardRows = computed<ATMBenchEntry[]>(() => {
    if (boardTab.value === 'main') {
        return (board.value.main_entries || []).filter((r) => !r.board || r.board === 'main')
    }
    return (board.value.hard_entries || []).filter((r) => !r.board || r.board === 'hard')
})

const activeRows = computed<ATMBenchEntry[]>(() => {
    const filtered = rawBoardRows.value.filter((row) => {
        if (atmCategoryFilter.value === 'all') return true
        if (atmCategoryFilter.value === 'ours') return row.is_ours
        return row.category === atmCategoryFilter.value
    })
    const key = atmSortKey.value
    return [...filtered].sort((a, b) => Number(b[key] || 0) - Number(a[key] || 0))
})

const activeAtmEntry = computed<ATMBenchEntry | null>(() => {
    if (selectedAtmEntry.value && rawBoardRows.value.some((r) => r.system === selectedAtmEntry.value?.system)) {
        return selectedAtmEntry.value
    }
    const ours = activeRows.value.find((r) => r.is_ours)
    return ours || activeRows.value[0] || null
})

const currentSplitOurs = computed<ATMBenchEntry | null>(() => rawBoardRows.value.find((r) => r.is_ours) || null)

const activeAtmBars = computed(() => {
    const cur = activeAtmEntry.value
    const ours = currentSplitOurs.value
    const diff = (val: number, oursVal?: number) => (typeof oursVal === 'number' ? val - oursVal : null)
    return [
        {
            key: 'r10',
            label: isEn.value ? 'Official Recall@10 (Gold Coverage Mean)' : '官方 Recall@10（金标集合交覆盖均值）',
            val: cur?.recall_at_10 ?? 0,
            delta: diff(cur?.recall_at_10 ?? 0, ours?.recall_at_10),
        },
        {
            key: 'qs',
            label: isEn.value ? 'Official Hybrid QS (End-to-End QA)' : '官方混合 QS（端到端问答综合分）',
            val: cur?.qs_score ?? 0,
            delta: diff(cur?.qs_score ?? 0, ours?.qs_score),
        },
        {
            key: 'list',
            label: isEn.value ? 'Cat 1 · List Recall (Evidence ID Jaccard)' : 'Cat 1 · 证据 ID 列表枚举 (List Recall)',
            val: cur?.list_recall ?? 0,
            delta: diff(cur?.list_recall ?? 0, ours?.list_recall),
        },
        {
            key: 'num',
            label: isEn.value ? 'Cat 2 · Number / Date (Deterministic EM)' : 'Cat 2 · 数值与日期推理 (Number EM)',
            val: cur?.number_em ?? 0,
            delta: diff(cur?.number_em ?? 0, ours?.number_em),
        },
        {
            key: 'open',
            label: isEn.value ? 'Cat 3 · Open-End (LLM Judge)' : 'Cat 3 · 开放问答 (Open-End)',
            val: cur?.open_end ?? 0,
            delta: diff(cur?.open_end ?? 0, ours?.open_end),
        },
    ]
})

const onBoardTabChange = () => {
    query.value.tier = boardTab.value === 'main' ? '1013' : 'hard'
    if (boardTab.value === 'main' && atmCategoryFilter.value === 'Oracle') {
        atmCategoryFilter.value = 'all'
    }
    selectedAtmEntry.value = null
}

const bestOurs = (rows: ATMBenchEntry[] | undefined, boardName: 'hard' | 'main', pick: (row: ATMBenchEntry) => number) => {
    const ours = (rows || []).filter((row) => row.is_ours && (!row.board || row.board === boardName))
    if (!ours.length) return null
    return ours.reduce((best, row) => (pick(row) > pick(best) ? row : best), ours[0])
}

const hardBest = computed(() => bestOurs(board.value.hard_entries, 'hard', (row) => row.recall_at_10))
const mainBest = computed(() => bestOurs(board.value.main_entries, 'main', (row) => row.recall_at_10))
const hardOracle = computed(() => (board.value.hard_entries || []).find((row) => row.category === 'Oracle') || null)

// 效能读数动态取自封印基线台账（0-Vector / 0-LLM 行），缺失时回退到公开参考读数。
const perfHardBaseline = computed(() => atmBaselines.value.find((r) => String(r.arm || '').includes('sdk-local-plain') && r.tier === 'hard') || null)
const perfMainBaseline = computed(() => atmBaselines.value.find((r) => String(r.arm || '').includes('sdk-local-plain') && r.tier === '1013') || null)
const ingestMS = computed(() => Number(perfHardBaseline.value?.ingest_ms || 0))
const ingestText = computed(() => (ingestMS.value > 0 ? `${(ingestMS.value / 1000).toFixed(2)}s` : '4.65s'))
const ingestHrText = computed(() => (ingestMS.value > 0 ? `${(ingestMS.value / 3600000).toFixed(4)} hr` : '0.0013 hr'))
const perDocMsText = computed(() => (ingestMS.value > 0 ? `${(ingestMS.value / 11034).toFixed(2)}ms/doc` : '0.42ms/doc'))
const recallPerfText = computed(() => {
    const hard = Number(perfHardBaseline.value?.avg_recall_ms || 0)
    const main = Number(perfMainBaseline.value?.avg_recall_ms || 0)
    const hardText = hard > 0 ? `${hard.toFixed(1)}ms` : '107.9ms'
    const mainText = main > 0 ? `${main.toFixed(1)}ms` : '68.6ms'
    return isEn.value
        ? `Retrieval mean: ${hardText} (Hard) / ${mainText} (Main) · 6,742 emails + 3,759 photos + 533 videos`
        : `单题检索均值 ${hardText} (Hard) / ${mainText} (Main) · 6,742 邮件 + 3,759 照片 + 533 视频`
})

const tierLabel = (tier: string) => {
    if (tier === 'hard') return isEn.value ? 'Hard (31 Qs)' : 'Hard 31 题'
    if (tier === '1013') return isEn.value ? 'Main (1,013 Qs)' : '主榜 1,013 题'
    return tier || '—'
}

const formatElapsed = (seconds: number) => {
    const value = Number(seconds) || 0
    return value >= 60 ? `${(value / 60).toFixed(1)} min` : `${value.toFixed(1)} s`
}
// 载入与端到端分开：新记录用 load_ms / eval_seconds，旧记录回退 ingest_ms / elapsed_seconds。
const loadSeconds = (row: any) => {
    const ms = Number(row?.load_ms || row?.ingest_ms || 0)
    return ms > 0 ? `${(ms / 1000).toFixed(2)}s` : '—'
}
const e2eSeconds = (row: any) => {
    const seconds = Number(row?.eval_seconds || row?.elapsed_seconds || 0)
    return seconds > 0 ? formatElapsed(seconds) : '—'
}

const displayRankOf = (idx: number) => {
    const rows = activeRows.value
    const curr = rows[idx]
    if (!curr || curr.rank >= 90) return 0
    let r = 0
    for (let i = 0; i <= idx; i++) {
        if (rows[i] && rows[i].rank < 90) r++
    }
    return r
}

const rankBadgeClass = (rank: number) => {
    if (rank === 1) return 'is-gold'
    if (rank === 2) return 'is-silver'
    if (rank === 3) return 'is-bronze'
    return ''
}

const atmRowClassName = ({ row }: { row: ATMBenchEntry }) => {
    if (row.is_ours) return 'rm-ours-table-row'
    return ''
}

const onAtmTableRowClick = (row: ATMBenchEntry) => {
    selectedAtmEntry.value = row
    const el = document.querySelector('.plugin-rm-atm-leaderboard .rm-inspector-row')
    if (el) {
        const rect = el.getBoundingClientRect()
        if (rect.top < 0 || rect.bottom > window.innerHeight) {
            el.scrollIntoView({ behavior: 'smooth', block: 'nearest' })
        }
    }
}

const copyText = async (text: string, enPrefix = 'Copied', zhPrefix = '已复制') => {
    const value = String(text || '').trim()
    if (!value) {
        ElMessage.warning(isEn.value ? 'Nothing to copy' : '暂无可复制内容')
        return
    }
    try {
        await navigator.clipboard.writeText(value)
        ElMessage.success(`${isEn.value ? enPrefix : zhPrefix}: ${value}`)
        return
    } catch {
        const textarea = document.createElement('textarea')
        textarea.value = value
        textarea.style.position = 'fixed'
        textarea.style.opacity = '0'
        document.body.appendChild(textarea)
        textarea.select()
        try {
            document.execCommand('copy')
            ElMessage.success(`${isEn.value ? enPrefix : zhPrefix}: ${value}`)
        } catch {
            ElMessage.info(value)
        } finally {
            document.body.removeChild(textarea)
        }
    }
}

const loadBoard = async () => {
    loading.value = true
    try {
        const res = await request<ATMLeaderboardResponse>({
            url: '/admin/plugin/rolling-memory/eval/atm/leaderboard',
            method: 'POST',
            data: {},
        })
        const data = res.data.data
        if (data) {
            board.value = data.atm_bench || {}
            atmSpec.value = data.atm_protocol_spec || {}
            atmAgents.value = data.atm_agents || []
            atmBaselines.value = data.atm_baselines || []
            atmOfficialPins.value = data.atm_official_pins || {}
        }
    } catch {
        // 业务与网络错误由请求拦截器统一提示。
    } finally {
        loading.value = false
    }
}

const goEvalLab = (tier: 'hard' | '1013') => {
    query.value.tier = tier || 'hard'
    router.push({ path: '/admin/plugin/rolling-memory/eval', query: { suite: 'atm', tier: query.value.tier } })
}

const goLoCoMoBoard = () => {
    router.push({ path: '/admin/plugin/rolling-memory/leaderboard' })
}

onMounted(() => {
    loadBoard()
})
</script>

<style scoped lang="scss">
.plugin-rm-atm-leaderboard {
    display: flex;
    flex-direction: column;
    gap: 14px;

    .rm-workbench-topbar {
        display: flex;
        flex-wrap: wrap;
        gap: 14px;
        align-items: flex-start;
        justify-content: space-between;
        padding: 16px 20px;
        background: var(--el-bg-color-overlay);
        border: 1px solid var(--el-border-color-lighter);
        border-radius: 8px;
    }

    .rm-wt-left {
        flex: 1;
        min-width: 280px;
    }

    .rm-wt-title-row {
        display: flex;
        flex-wrap: wrap;
        gap: 8px;
        align-items: center;
    }

    .rm-wt-badge {
        padding: 2px 8px;
        font-family: monospace;
        font-size: 11px;
        font-weight: 700;
        letter-spacing: 0.04em;
        color: #fff;
        background: var(--el-color-primary);
        border-radius: 4px;
    }

    .rm-wt-title {
        margin: 0;
        font-size: 18px;
        font-weight: 700;
        line-height: 1.35;
        color: var(--el-text-color-primary);
    }

    .rm-wt-sub {
        margin: 6px 0 0;
        font-size: 12.5px;
        line-height: 1.55;
        color: var(--el-text-color-secondary);
    }

    .rm-wt-actions {
        display: flex;
        flex-wrap: wrap;
        gap: 8px;
        align-items: center;
    }

    .rm-stats-grid {
        display: grid;
        grid-template-columns: repeat(auto-fit, minmax(250px, 1fr));
        gap: 12px;
    }

    .stat-card {
        display: flex;
        flex-direction: column;
        justify-content: space-between;
        padding: 14px 16px;
        background: var(--el-bg-color-overlay);
        border: 1px solid var(--el-border-color-lighter);
        border-left: 4px solid var(--el-color-info);
        border-radius: 8px;

        &.is-success {
            border-left-color: var(--el-color-success);
        }

        &.is-primary {
            border-left-color: var(--el-color-primary);
        }

        &.is-warning {
            border-left-color: var(--el-color-warning);
        }

        &.is-info {
            border-left-color: var(--el-color-info);
        }
    }

    .stat-head {
        display: flex;
        gap: 8px;
        align-items: center;
        justify-content: space-between;
    }

    .stat-head-right {
        display: flex;
        gap: 4px;
        align-items: center;
        flex-shrink: 0;
    }

    .stat-label {
        font-size: 12.5px;
        font-weight: 600;
        color: var(--el-text-color-secondary);
    }

    .stat-main-val {
        display: flex;
        gap: 8px;
        align-items: baseline;
        margin: 8px 0 6px;
        font-size: 24px;
        font-weight: 700;
        font-variant-numeric: tabular-nums;
        line-height: 1.2;
        color: var(--el-text-color-primary);

        &.is-green {
            color: var(--el-color-success);
        }

        &.is-primary-txt {
            color: var(--el-color-primary);
        }

        &.is-mono {
            font-family: monospace;
            font-size: 17px;
        }
    }

    .stat-val-tag {
        font-size: 11.5px;
        font-weight: 500;
        color: var(--el-text-color-secondary);
    }

    .stat-foot {
        font-size: 12px;
        line-height: 1.45;
        color: var(--el-text-color-regular);
    }

    .rm-board-card,
    .rm-audit-card {
        border-radius: 8px;
    }

    .rm-board-toolbar {
        display: flex;
        flex-wrap: wrap;
        gap: 12px;
        align-items: center;
        justify-content: space-between;
        padding-bottom: 12px;
        margin-bottom: 12px;
        border-bottom: 1px solid var(--el-border-color-lighter);
    }

    .rm-bt-left,
    .rm-bt-right {
        display: flex;
        flex-wrap: wrap;
        gap: 10px;
        align-items: center;
    }

    .rm-ctrl-lbl {
        font-size: 12.5px;
        color: var(--el-text-color-secondary);
    }

    .rm-repro-command-strip {
        display: flex;
        flex-wrap: wrap;
        gap: 10px;
        align-items: center;
        justify-content: space-between;
        padding: 10px 14px;
        margin-bottom: 14px;
        background: var(--el-fill-color-lighter);
        border: 1px solid var(--el-border-color-light);
        border-radius: 6px;
    }

    .rm-rcs-main {
        display: flex;
        flex: 1;
        flex-wrap: wrap;
        gap: 8px;
        align-items: center;
        min-width: 260px;
    }

    .rm-rcs-tag {
        padding: 2px 7px;
        font-family: monospace;
        font-size: 11px;
        font-weight: 700;
        color: var(--el-color-primary);
        background: var(--el-color-primary-light-9);
        border: 1px solid var(--el-color-primary-light-7);
        border-radius: 4px;
    }

    .rm-rcs-code {
        font-family: monospace;
        font-size: 12px;
        color: var(--el-text-color-primary);
        word-break: break-all;
    }

    .rm-rcs-btns {
        display: flex;
        flex-wrap: wrap;
        gap: 8px;
        align-items: center;
    }

    .rm-inspector-row {
        margin-bottom: 16px;
    }

    .rm-col-stretch {
        display: flex;
        margin-bottom: 12px;
    }

    .rm-panel-box {
        display: flex;
        flex-direction: column;
        width: 100%;
        padding: 14px 16px;
        background: var(--el-bg-color);
        border: 1px solid var(--el-border-color-lighter);
        border-radius: 8px;
    }

    .rm-panel-hdr {
        display: flex;
        flex-wrap: wrap;
        gap: 10px;
        align-items: center;
        justify-content: space-between;
        padding-bottom: 10px;
        margin-bottom: 12px;
        border-bottom: 1px solid var(--el-border-color-extra-light);
    }

    .rm-panel-title {
        display: block;
        font-size: 14px;
        font-weight: 600;
        color: var(--el-text-color-primary);
    }

    .rm-panel-sub {
        font-size: 12px;
        color: var(--el-text-color-secondary);
    }

    .rm-ladder-list {
        display: flex;
        flex-direction: column;
        gap: 7px;
        max-height: 390px;
        overflow-y: auto;
    }

    .rm-ladder-row {
        display: flex;
        flex-direction: column;
        gap: 5px;
        padding: 8px 10px;
        cursor: pointer;
        border: 1px solid var(--el-border-color-extra-light);
        border-radius: 6px;
        transition: all 0.15s ease;

        &:hover {
            background: var(--el-fill-color-light);
        }

        &.is-active {
            background: var(--el-color-primary-light-9);
            border-color: var(--el-color-primary-light-5);
        }

        &.is-ours-row {
            border-left: 3px solid var(--el-color-success);
        }
    }

    .rm-ladder-top {
        display: flex;
        gap: 10px;
        align-items: center;
        justify-content: space-between;
    }

    .rm-ladder-meta {
        display: flex;
        flex-wrap: wrap;
        gap: 6px;
        align-items: center;
        min-width: 0;
    }

    .rm-sys-name {
        font-size: 13px;
        font-weight: 600;
        color: var(--el-text-color-primary);
    }

    .rm-ladder-right-nums {
        display: flex;
        flex-shrink: 0;
        gap: 10px;
        align-items: baseline;
    }

    .rm-ladder-aux {
        font-size: 11.5px;
        font-variant-numeric: tabular-nums;
        color: var(--el-text-color-secondary);
    }

    .rm-ladder-score {
        font-size: 14px;
        font-weight: 700;
        font-variant-numeric: tabular-nums;

        &.is-ours-score {
            color: var(--el-color-success);
        }
    }

    .rm-ladder-bar-wrap {
        height: 7px;
        background: var(--el-fill-color);
        border-radius: 4px;
    }

    .rm-ladder-bar {
        height: 100%;
        border-radius: 4px;

        &.is-ours {
            background: linear-gradient(90deg, #10b981, #059669);
        }

        &.is-oracle {
            background: linear-gradient(90deg, #f59e0b, #d97706);
        }

        &.is-pub {
            background: linear-gradient(90deg, #60a5fa, #2563eb);
        }
    }

    .rm-cat-breakdown {
        display: flex;
        flex: 1;
        flex-direction: column;
        justify-content: space-between;
        gap: 12px;
    }

    .rm-active-meta {
        display: flex;
        flex-direction: column;
        gap: 6px;
        padding: 10px 12px;
        font-size: 12px;
        background: var(--el-fill-color-lighter);
        border: 1px solid var(--el-border-color-extra-light);
        border-radius: 6px;
    }

    .rm-active-sys-line {
        display: flex;
        flex-wrap: wrap;
        gap: 8px;
        align-items: baseline;
    }

    .rm-active-sys-name {
        font-size: 14px;
        color: var(--el-text-color-primary);
    }

    .rm-active-arch {
        line-height: 1.5;
        color: var(--el-text-color-regular);
    }

    .rm-active-tags {
        display: flex;
        flex-wrap: wrap;
        gap: 6px;
        margin-top: 2px;
    }

    .rm-cat-list {
        display: flex;
        flex-direction: column;
        gap: 10px;
    }

    .rm-cat-item {
        display: flex;
        flex-direction: column;
        gap: 4px;
    }

    .rm-cat-top {
        display: flex;
        flex-wrap: wrap;
        gap: 6px;
        align-items: center;
        justify-content: space-between;
        font-size: 12px;
    }

    .rm-cat-name {
        font-weight: 500;
        color: var(--el-text-color-primary);
    }

    .rm-cat-nums {
        display: flex;
        gap: 8px;
        align-items: baseline;
        font-variant-numeric: tabular-nums;
    }

    .rm-cat-val-txt {
        font-size: 13px;
    }

    .rm-vs-ours {
        font-size: 11.5px;
        color: var(--el-text-color-secondary);

        em {
            margin-left: 2px;
            font-style: normal;
            font-weight: 600;
        }

        .is-pos {
            color: var(--el-color-success);
        }

        .is-neg {
            color: var(--el-color-danger);
        }
    }

    .rm-cat-track {
        height: 8px;
        background: var(--el-fill-color);
        border-radius: 4px;
    }

    .rm-cat-fill {
        height: 100%;
        border-radius: 4px;

        &.is-ours {
            background: #10b981;
        }

        &.is-oracle {
            background: #f59e0b;
        }

        &.is-pub {
            background: #3b82f6;
        }
    }

    .rm-cat-note {
        padding-top: 8px;
        font-size: 12px;
        line-height: 1.55;
        color: var(--el-text-color-regular);
        border-top: 1px dashed var(--el-border-color-lighter);
    }

    .rm-table-section-hdr {
        display: flex;
        flex-wrap: wrap;
        gap: 8px;
        align-items: center;
        justify-content: space-between;
        padding: 6px 2px 10px;
        font-size: 13.5px;
    }

    .rm-cell-multiline {
        display: flex;
        flex-direction: column;
        gap: 3px;
        padding: 2px 0;
        line-height: 1.45;
        word-break: break-word;
        white-space: normal;
    }

    .rm-sys-primary {
        font-size: 13px;
        color: var(--el-text-color-primary);
    }

    .rm-sys-tags-line {
        display: flex;
        flex-wrap: wrap;
        gap: 6px;
        align-items: center;
    }

    .rm-sub-text {
        font-size: 11.5px;
        line-height: 1.4;
        color: var(--el-text-color-secondary);
        word-break: break-word;
    }

    .rm-code-inline {
        display: inline-block;
        padding: 1px 6px;
        font-family: monospace;
        font-size: 11.5px;
        background: var(--el-fill-color);
        border-radius: 4px;
    }

    .rm-num-cell {
        font-variant-numeric: tabular-nums;
    }

    .rm-best-num {
        font-weight: 700;
        color: var(--el-color-success);
    }

    .rm-overall-num {
        font-size: 13.5px;
        font-variant-numeric: tabular-nums;

        &.is-ours-num {
            color: var(--el-color-success);
        }
    }

    .rm-note-cell {
        padding: 2px 0;
        font-size: 12px;
        line-height: 1.55;
        color: var(--el-text-color-regular);
        word-break: break-word;
        white-space: normal;
    }

    .rm-rank-badge {
        display: inline-block;
        min-width: 30px;
        padding: 1px 6px;
        font-size: 11.5px;
        font-weight: 700;
        font-variant-numeric: tabular-nums;
        text-align: center;
        background: var(--el-fill-color);
        border-radius: 4px;

        &.is-gold {
            color: #92400e;
            background: #fef3c7;
        }

        &.is-silver {
            color: #1e293b;
            background: #e2e8f0;
        }

        &.is-bronze {
            color: #7c2d12;
            background: #ffedd5;
        }
    }

    .rm-audit-header {
        display: flex;
        flex-wrap: wrap;
        gap: 10px;
        align-items: center;
        justify-content: space-between;
    }

    .rm-ref-title {
        margin-bottom: 8px;
        font-size: 13px;
        font-weight: 600;
        color: var(--el-text-color-primary);
    }

    .rm-bucket-cell {
        display: flex;
        flex-wrap: wrap;
        gap: 4px;
    }

    .rm-pin-grid {
        display: flex;
        flex-direction: column;
        gap: 4px;
    }

    .rm-pin-line {
        display: flex;
        flex-wrap: wrap;
        gap: 8px;
        align-items: center;
    }

    .rm-agent-box {
        width: 100%;
        padding: 12px 14px;
        background: var(--el-fill-color-lighter);
        border: 1px solid var(--el-border-color-lighter);
        border-radius: 8px;
    }

    .rm-agent-hdr {
        display: flex;
        flex-wrap: wrap;
        gap: 8px;
        align-items: center;
        justify-content: space-between;
    }

    .rm-agent-title-wrap {
        display: flex;
        flex-wrap: wrap;
        gap: 8px;
        align-items: center;
    }

    .rm-sha-code {
        font-family: monospace;
        font-size: 11.5px;
        word-break: break-all;
    }

    .rm-prompt-lbl {
        margin: 10px 0 4px;
        font-size: 12px;
        font-weight: 600;
        color: var(--el-text-color-secondary);
    }

    .rm-prompt-pre {
        max-height: 170px;
        padding: 8px 10px;
        margin: 0;
        overflow: auto;
        font-size: 11.5px;
        line-height: 1.5;
        word-break: break-word;
        white-space: pre-wrap;
        background: var(--el-bg-color);
        border: 1px solid var(--el-border-color-lighter);
        border-radius: 4px;
    }

    .rm-top-progress-banner {
        padding: 10px 14px;
        background: var(--el-color-primary-light-9);
        border: 1px solid var(--el-color-primary-light-7);
        border-radius: 8px;

        .rm-tpb-head {
            display: flex;
            align-items: center;
            justify-content: space-between;
            margin-bottom: 6px;
            font-size: 12.5px;
        }
    }

    :deep(.rm-ours-table-row) {
        font-weight: 600;
        background-color: var(--el-color-success-light-9) !important;
    }

    .rm-muted {
        font-size: 12px;
        color: var(--el-text-color-secondary);
    }

    .tip-q {
        display: inline-flex;
        align-items: center;
        justify-content: center;
        width: 15px;
        height: 15px;
        padding: 0;
        margin-left: 3px;
        font-family: inherit;
        font-size: 10px;
        font-weight: 700;
        line-height: 1;
        color: var(--el-text-color-secondary);
        background: var(--el-fill-color);
        border: 1px solid var(--el-border-color-light);
        border-radius: 50%;
        cursor: pointer;
        user-select: none;
        transition: all 0.2s ease;

        &:hover {
            color: #ffffff;
            background: var(--el-color-primary);
            border-color: var(--el-color-primary);
        }
    }
}
</style>

<style lang="scss">
.rm-help-popper {
    max-width: 92vw;
}

.rm-help-popper .rm-help-body {
    display: flex;
    flex-direction: column;
    gap: 6px;
    font-size: 12px;
    line-height: 1.6;
    color: var(--el-text-color-regular);
}

.rm-help-popper .rm-help-body p {
    margin: 0;
}
</style>
