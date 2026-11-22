<!-- plugin: rolling-memory v0.0.58 -->
<!--
    Rolling-Memory 记忆系统 · 「LoCoMo-Backboard 对照榜单」工作台（views/admin/plugin/rolling-memory/leaderboard.vue）。
    设计原则（第一性原理 · 与剑桥 ATM-Bench 主榜单页 100% 视觉与交互同构）：
      1) 首屏直接交代对照基准身份：内置 locomo-backboard（locomo-frozen-2026-10-11）三枚 SHA-1 协议钉只读锁死，对标 ACL 2024 LoCoMo 1,540 题商业记忆榜单；
      2) 4 维核心指标卡（左边框指示卡）：本机 0-Vector/0-LLM 检索核 + LLM 答器/判官（Top-96）1,540 全量最新读数（85.26% · 1313/1540）、四维题型分项（单跳 90.96% 超越外部榜首 Backboard 89.36%）、历史重型架构存档对照（92.79%）、冻结协议状态；
      3) 连贯分析流：分类过滤与排序控制栏 + 一键 CLI 复现条 + 左总分天梯图（含 90.00% 外部榜首线）/ 右四维题型剖析双栏联动 + 完整榜单明细表（显式展示判官模型列）；
      4) 底部审计抽屉：将协议说明、三枚 SHA-1 密码学钉与官方 Answer/Judge 冻结提示词原文收纳为清爽的审计选项卡。
-->
<template>
    <div class="default-main plugin-rm-leaderboard">
        <!-- 顶部基准身份与跨页导航栏 -->
        <div class="rm-workbench-topbar">
            <div class="rm-wt-left">
                <div class="rm-wt-title-row">
                    <span class="rm-wt-badge">COMPARATIVE REFERENCE</span>
                    <h1 class="rm-wt-title">{{ t('plugin.rolling-memory.leaderboard.title') }}</h1>
                    <el-tag size="small" type="primary" effect="plain">
                        {{ t('plugin.rolling-memory.leaderboard.protocolBadge') }}
                    </el-tag>
                    <el-tag size="small" type="warning" effect="plain">
                        {{ t('plugin.rolling-memory.leaderboard.boardBadge') }}
                    </el-tag>
                    <el-tag
                        size="small"
                        :type="protocolSpec.all_pins_verified === true ? 'success' : protocolSpec.all_pins_verified === false ? 'danger' : 'info'"
                        effect="light"
                    >
                        {{
                            protocolSpec.all_pins_verified === true
                                ? t('plugin.rolling-memory.leaderboard.pinsVerifiedBadge')
                                : protocolSpec.all_pins_verified === false
                                  ? isEn
                                      ? '✗ SHA1 Pin Drift'
                                      : '✗ 协议钉漂移'
                                  : isEn
                                    ? 'Checking SHA1 Pins…'
                                    : '协议钉待核验'
                        }}
                    </el-tag>
                    <el-popover trigger="click" placement="bottom" :width="440" popper-class="rm-help-popper">
                        <template #reference>
                            <button
                                type="button"
                                class="tip-q"
                                :aria-label="
                                    isEn
                                        ? 'Evaluation protocol, benchmark and cryptographic pin semantics'
                                        : '评测协议、对标基准与密码学钉校验语义说明'
                                "
                            >
                                ?
                            </button>
                        </template>
                        <div class="rm-help-body">
                            <p v-if="isEn">
                                <strong>Built-in Protocol:</strong> locomo-backboard (locomo-frozen-2026-10-11), byte-identical to the Backboard public
                                benchmark harness.
                            </p>
                            <p v-else>
                                <strong>内置评测协议：</strong>locomo-backboard（locomo-frozen-2026-10-11），与商业记忆榜单官方评测套件逐字节同构。
                            </p>
                            <p v-if="isEn">
                                <strong>Target Benchmark:</strong> LoCoMo 1,540-question long-term multi-session conversational memory leaderboard
                                (ACL 2024, 5,882 dialogue turns).
                            </p>
                            <p v-else>
                                <strong>对标行业基准：</strong>LoCoMo（ACL 2024，5,882 轮跨月对话）1,540 题长程多会话记忆权威排行榜。
                            </p>
                            <p v-if="isEn">
                                <strong>Cryptographic 3-Pin Verification:</strong> SHA-1 digests of Answer System Prompt, Judge System Prompt, and
                                Judge User Template are locked read-only and verified.
                            </p>
                            <p v-else>
                                <strong>三枚 SHA-1 密码学钉校验：</strong>Answer 系统提示词、Judge 系统提示词与 Judge 用户模板均只读锁死并实时核验。
                            </p>
                        </div>
                    </el-popover>
                </div>
                <p class="rm-wt-sub">
                    {{ t('plugin.rolling-memory.leaderboard.bannerDesc') }}
                </p>
            </div>
            <div class="rm-wt-actions">
                <el-button v-blur size="small" type="primary" @click="goEvalLab('1540')">
                    <Icon name="lucide-flask-conical" :size="13" style="margin-right: 5px" />
                    {{ isEn ? 'Run Official 1,540 in Lab →' : '在评测实验室运行 1,540 全量 →' }}
                </el-button>
                <el-button v-blur size="small" type="warning" plain @click="goATMLeaderboard">
                    {{ isEn ? 'Cambridge ATM-Bench (Primary) →' : '剑桥 ATM-Bench 主榜单 →' }}
                </el-button>
                <el-button v-blur size="small" :loading="loading" @click="loadLeaderboard">
                    {{ t('plugin.rolling-memory.refresh') }}
                    <template v-if="loading"> ({{ loadLbPct }}%)</template>
                </el-button>
            </div>
        </div>

        <div v-if="loading" class="rm-top-progress-banner">
            <div class="rm-tpb-head">
                <span>{{ loadLbText }}</span>
                <strong>{{ loadLbPct }}%</strong>
            </div>
            <el-progress :percentage="loadLbPct" :stroke-width="6" striped striped-flow :show-text="false" />
        </div>

        <!-- 4 维核心科学指标矩阵（BuildAdmin 左边框指示卡） -->
        <div class="rm-stats-grid">
            <div class="stat-card is-success">
                <div class="stat-head">
                    <span class="stat-label">{{ t('plugin.rolling-memory.leaderboard.kpi2Label') }}</span>
                    <div class="stat-head-right">
                        <el-tag size="small" type="success" effect="dark">0-Vector · 0-LLM</el-tag>
                        <el-popover trigger="click" placement="top" :width="360" popper-class="rm-help-popper">
                            <template #reference>
                                <button
                                    type="button"
                                    class="tip-q"
                                    :aria-label="
                                        isEn ? 'Latest on-device 1,540 full-set reading and judge ruler' : '本机最新 1,540 全量读数与判官口径说明'
                                    "
                                >
                                    ?
                                </button>
                            </template>
                            <div class="rm-help-body">
                                <p v-if="isEn">
                                    Latest on-device reading: the local_plain retrieval kernel (0-Vector / 0-LLM) delivers a Top-96 context to an
                                    LLM answerer and judge, scoring 85.26% (1313/1540) end-to-end on the official Cat 1–4 full set under the frozen
                                    prompt protocol. The 0-LLM label applies to the retrieval kernel only; the answerer and judge are
                                    deepseek-v4.1-flash (compare row by row against the judge column).
                                </p>
                                <p v-else>
                                    本机最新读数：local_plain 检索核（0-Vector / 0-LLM）以 Top-96 交付将证据交给 LLM 答器与判官，在官方 cat1-4 全集（1,540 题）上取得端到端判对率 85.26%（1313/1540），冻结提示词协议。“0-LLM” 仅指检索核；答器与判官均为 deepseek-v4.1-flash（请按行内判官列逐行对照）。
                                </p>
                            </div>
                        </el-popover>
                    </div>
                </div>
                <div class="stat-main-val is-green">{{ t('plugin.rolling-memory.leaderboard.kpi2Value') }}</div>
                <div class="stat-foot">{{ t('plugin.rolling-memory.leaderboard.kpi2Sub') }}</div>
            </div>

            <div class="stat-card is-primary">
                <div class="stat-head">
                    <span class="stat-label">{{ t('plugin.rolling-memory.leaderboard.kpi3Label') }}</span>
                    <div class="stat-head-right">
                        <el-tag size="small" type="primary" effect="plain">Cat 4 Single 90.96%</el-tag>
                        <el-popover trigger="click" placement="top" :width="360" popper-class="rm-help-popper">
                            <template #reference>
                                <button
                                    type="button"
                                    class="tip-q"
                                    :aria-label="isEn ? 'Category breakdown definition' : '题型分项得分说明'"
                                >
                                    ?
                                </button>
                            </template>
                            <div class="rm-help-body">
                                <p v-if="isEn">
                                    Category breakdown of the latest on-device 1,540 run: Cat 4 Single-hop 90.96% (beats Backboard 89.36%), Cat 2
                                    Temporal 87.23%, Cat 1 Multi-hop 70.57%, Cat 3 Open-domain 71.88%.
                                </p>
                                <p v-else>
                                    本机最新 1,540 全量运行的题型分项：Cat 4 单跳 90.96%（超越外部榜首 Backboard 89.36%）· Cat 2 时序 87.23% · Cat 1 多跳 70.57% · Cat 3 开放域 71.88%。
                                </p>
                            </div>
                        </el-popover>
                    </div>
                </div>
                <div class="stat-main-val is-primary-txt">{{ t('plugin.rolling-memory.leaderboard.kpi3Value') }}</div>
                <div class="stat-foot">{{ t('plugin.rolling-memory.leaderboard.kpi3Sub') }}</div>
            </div>

            <div class="stat-card is-warning">
                <div class="stat-head">
                    <span class="stat-label">{{ t('plugin.rolling-memory.leaderboard.kpi4Label') }}</span>
                    <div class="stat-head-right">
                        <el-tag size="small" type="warning" effect="plain">{{ isEn ? 'Archived' : '历史存档' }}</el-tag>
                        <el-popover trigger="click" placement="top" :width="360" popper-class="rm-help-popper">
                            <template #reference>
                                <button
                                    type="button"
                                    class="tip-q"
                                    :aria-label="isEn ? 'Historical LLM + vector architecture result' : '历史 LLM + 向量架构成绩说明'"
                                >
                                    ?
                                </button>
                            </template>
                            <div class="rm-help-body">
                                <p v-if="isEn">
                                    Historical result: our early LLM + vector heavyweight architecture scored 92.79% on the full 1,540-question set
                                    (GPT-4.1 temp 0.1 judge). Kept as an architecture-evolution reference; this route is no longer provided.
                                </p>
                                <p v-else>
                                    历史成绩：我方早期 LLM + 向量重型架构在 1,540 题全量上的存档读数 92.79%（GPT-4.1 temp 0.1 判官），仅作架构演进对照；该路线不再提供。
                                </p>
                            </div>
                        </el-popover>
                    </div>
                </div>
                <div class="stat-main-val">{{ t('plugin.rolling-memory.leaderboard.kpi4Value') }}</div>
                <div class="stat-foot">{{ t('plugin.rolling-memory.leaderboard.kpi4Sub') }}</div>
            </div>

            <div class="stat-card is-info">
                <div class="stat-head">
                    <span class="stat-label">{{ t('plugin.rolling-memory.leaderboard.kpi1Label') }}</span>
                    <div class="stat-head-right">
                        <el-tag
                            size="small"
                            :type="protocolSpec.all_pins_verified === true ? 'success' : protocolSpec.all_pins_verified === false ? 'danger' : 'info'"
                            effect="plain"
                        >
                            {{
                                protocolSpec.all_pins_verified === true
                                    ? isEn
                                        ? '3 PINS PASS'
                                        : '三钉通过'
                                    : protocolSpec.all_pins_verified === false
                                      ? 'DRIFT'
                                      : 'UNVERIFIED'
                            }}
                        </el-tag>
                        <el-popover trigger="click" placement="top" :width="360" popper-class="rm-help-popper">
                            <template #reference>
                                <button type="button" class="tip-q" :aria-label="isEn ? 'Protocol ID and SHA-1 pin status' : '冻结评测协议标识与哈希钉状态说明'">
                                    ?
                                </button>
                            </template>
                            <div class="rm-help-body">
                                <p v-if="isEn">
                                    Frozen evaluation protocol ID and cryptographic SHA-1 pin status ensuring strict apples-to-apples comparison.
                                </p>
                                <p v-else>冻结评测协议标识与三枚 SHA-1 哈希钉状态，确保与外部商业榜单采用完全一致的裁判标准。</p>
                            </div>
                        </el-popover>
                    </div>
                </div>
                <div class="stat-main-val is-mono">{{ t('plugin.rolling-memory.leaderboard.kpi1Value') }}</div>
                <div class="stat-foot">{{ t('plugin.rolling-memory.leaderboard.kpi1Sub') }}</div>
            </div>
        </div>

        <!-- 主榜单工作台：筛选与排序栏 + 复现命令条 + 双栏天梯剖析 + 完整榜单明细表 -->
        <el-card shadow="never" class="rm-board-card">
            <div class="rm-board-toolbar">
                <div class="rm-bt-left">
                    <el-radio-group v-model="filterMode" size="default">
                        <el-radio-button value="all">
                            {{ t('plugin.rolling-memory.leaderboard.filterAll') }}
                        </el-radio-button>
                        <el-radio-button value="public">
                            {{ t('plugin.rolling-memory.leaderboard.filterPublic') }}
                        </el-radio-button>
                        <el-radio-button value="ours">
                            {{ t('plugin.rolling-memory.leaderboard.filterOurs') }}
                        </el-radio-button>
                    </el-radio-group>
                </div>

                <div class="rm-bt-right">
                    <span class="rm-ctrl-lbl">{{ isEn ? 'Rank by:' : '排序指标：' }}</span>
                    <el-select v-model="sortKey" size="small" class="rm-sort-select">
                        <el-option :label="t('plugin.rolling-memory.leaderboard.sortOverall')" value="overall" />
                        <el-option :label="t('plugin.rolling-memory.leaderboard.sortSingleHop')" value="single_hop" />
                        <el-option :label="t('plugin.rolling-memory.leaderboard.sortMultiHop')" value="multi_hop" />
                        <el-option :label="t('plugin.rolling-memory.leaderboard.sortOpenDomain')" value="open_domain" />
                        <el-option :label="t('plugin.rolling-memory.leaderboard.sortTemporal')" value="temporal" />
                    </el-select>
                </div>
            </div>

            <!-- 紧凑型一键 CLI 复现与数据获取条 -->
            <div class="rm-repro-command-strip">
                <div class="rm-rcs-main">
                    <span class="rm-rcs-tag">LOCOMO-1540 REPRO</span>
                    <code class="rm-rcs-code">rmem eval --suite locomo --tier 1540 --top-k 96 --answerer llm --judge llm</code>
                </div>
                <div class="rm-rcs-btns">
                    <el-button
                        v-blur
                        size="small"
                        type="primary"
                        plain
                        @click="
                            copyText(
                                'rmem eval --suite locomo --tier 1540 --top-k 96 --answerer llm --judge llm',
                                'Copied LoCoMo repro CLI',
                                '已复制 LoCoMo 1,540 复现命令'
                            )
                        "
                    >
                        <Icon name="el-document-copy" :size="13" style="margin-right: 4px" />
                        {{ isEn ? 'Copy Repro CLI' : '复制复现命令' }}
                    </el-button>
                    <el-button
                        v-blur
                        size="small"
                        plain
                        @click="
                            copyText(
                                'rmem eval --fetch-only --fetch locomo --accept-upstream-license',
                                'Copied LoCoMo fetch CLI',
                                '已复制 LoCoMo 语料获取命令'
                            )
                        "
                    >
                        {{ isEn ? 'Copy Corpus Fetch CLI' : '复制语料获取命令 (--fetch)' }}
                    </el-button>
                    <el-button size="small" type="success" plain @click="goEvalLab('1540')">
                        {{ isEn ? 'Open in Evaluation Lab →' : '在评测实验室运行 →' }}
                    </el-button>
                </div>
            </div>

            <!-- 双栏联动：左侧总分天梯对比 + 右侧四维题型剖析 -->
            <el-row :gutter="14" class="rm-inspector-row">
                <el-col :xs="24" :lg="13" class="rm-col-stretch">
                    <div class="rm-panel-box">
                        <div class="rm-panel-hdr">
                            <div>
                                <strong class="rm-panel-title">{{ t('plugin.rolling-memory.leaderboard.chartLeftTitle') }}</strong>
                            </div>
                            <div class="rm-chart-legend">
                                <span class="rm-leg-item">
                                    <span class="rm-leg-dot is-ours"></span>
                                    <span>{{ t('plugin.rolling-memory.leaderboard.legendOurs') }}</span>
                                </span>
                                <span class="rm-leg-item">
                                    <span class="rm-leg-dot is-pub"></span>
                                    <span>{{ t('plugin.rolling-memory.leaderboard.legendPublic') }}</span>
                                </span>
                                <el-popover trigger="click" placement="bottom" :width="320" popper-class="rm-help-popper">
                                    <template #reference>
                                        <button type="button" class="tip-q" :aria-label="isEn ? 'Reference line semantics' : '参考线语义说明'">
                                            ?
                                        </button>
                                    </template>
                                    <div class="rm-help-body">
                                        <p>{{ t('plugin.rolling-memory.leaderboard.leaderLineTip') }}</p>
                                    </div>
                                </el-popover>
                            </div>
                        </div>

                        <div class="rm-ladder-list">
                            <div
                                v-for="(item, idx) in filteredEntries"
                                :key="item.id"
                                class="rm-ladder-row"
                                :class="{
                                    'is-active': activeEntry?.id === item.id,
                                    'is-ours-row': item.is_ours,
                                }"
                                @click="selectedEntry = item"
                            >
                                <div class="rm-ladder-top">
                                    <div class="rm-ladder-meta">
                                        <span class="rm-rank-badge" :class="rankBadgeClass(item.not_ranked ? 0 : idx + 1)">
                                            {{ item.not_ranked ? '—' : `#${idx + 1}` }}
                                        </span>
                                        <span class="rm-sys-name">{{ loc(item.system, item.system_en) }}</span>
                                        <el-tag size="small" :type="item.is_ours ? 'success' : 'info'" effect="plain" class="rm-mini-tag">
                                            {{
                                                item.is_ours
                                                    ? t('plugin.rolling-memory.leaderboard.tagOurs')
                                                    : t('plugin.rolling-memory.leaderboard.tagPublic')
                                            }}
                                        </el-tag>
                                        <el-tag
                                            v-if="loc(item.highlight_tag, item.highlight_tag_en)"
                                            size="small"
                                            :type="item.is_ours ? 'warning' : 'info'"
                                            effect="light"
                                            class="rm-mini-tag"
                                        >
                                            {{ loc(item.highlight_tag, item.highlight_tag_en) }}
                                        </el-tag>
                                    </div>
                                    <div class="rm-ladder-score" :class="{ 'is-ours-score': item.is_ours }">{{ item.overall.toFixed(2) }}%</div>
                                </div>
                                <div class="rm-ladder-bar-wrap">
                                    <div
                                        class="rm-ladder-bar"
                                        :class="item.is_ours ? 'is-ours' : 'is-pub'"
                                        :style="{ width: `${Math.max(4, Math.min(100, item.overall))}%` }"
                                    ></div>
                                    <div class="rm-ladder-ref-line" style="left: 90%"></div>
                                </div>
                            </div>
                        </div>
                    </div>
                </el-col>

                <el-col :xs="24" :lg="11" class="rm-col-stretch">
                    <div class="rm-panel-box">
                        <div class="rm-panel-hdr">
                            <div>
                                <strong class="rm-panel-title">{{ t('plugin.rolling-memory.leaderboard.chartRightTitle') }}</strong>
                            </div>
                            <el-tag v-if="activeEntry" size="small" :type="activeEntry.is_ours ? 'success' : 'primary'">
                                {{ activeEntry.overall.toFixed(2) }}%
                            </el-tag>
                        </div>

                        <div v-if="activeEntry" class="rm-cat-breakdown">
                            <div class="rm-active-meta">
                                <div class="rm-active-sys-line">
                                    <strong class="rm-active-sys-name">{{ loc(activeEntry.system, activeEntry.system_en) }}</strong>
                                    <span class="rm-muted">{{ loc(activeEntry.vendor, activeEntry.vendor_en) }}</span>
                                </div>
                                <div class="rm-active-arch">
                                    {{ loc(activeEntry.architecture, activeEntry.architecture_en) }}
                                </div>
                                <div class="rm-active-tags">
                                    <el-tag size="small" type="primary" effect="plain">
                                        {{ loc(activeEntry.protocol, activeEntry.protocol_en) }}
                                    </el-tag>
                                    <el-tag size="small" type="info">
                                        {{ loc(activeEntry.question_set, activeEntry.question_set_en) }}
                                    </el-tag>
                                    <el-tag size="small" type="warning">
                                        {{ t('plugin.rolling-memory.leaderboard.judgePrefix') }}:
                                        {{ loc(activeEntry.judge_model, activeEntry.judge_model_en) }}
                                    </el-tag>
                                </div>
                            </div>

                            <div class="rm-cat-list">
                                <div v-for="cat in activeCategoryBars" :key="cat.key" class="rm-cat-item">
                                    <div class="rm-cat-top">
                                        <div class="rm-cat-label-wrap">
                                            <span class="rm-cat-name">{{ cat.label }}</span>
                                            <el-tag v-if="cat.val >= cat.leaderVal" size="small" type="success" effect="plain">
                                                {{ t('plugin.rolling-memory.leaderboard.catLeaderBadge') }}
                                            </el-tag>
                                        </div>
                                        <div class="rm-cat-nums">
                                            <strong class="rm-cat-val-txt">{{ cat.val.toFixed(2) }}%</strong>
                                            <span class="rm-vs-leader">
                                                {{ t('plugin.rolling-memory.leaderboard.vsLeaderPrefix') }}
                                                <em :class="cat.val >= cat.leaderVal ? 'is-pos' : 'is-neg'">
                                                    {{ cat.val >= cat.leaderVal ? '+' : '' }}{{ (cat.val - cat.leaderVal).toFixed(2) }}pp
                                                </em>
                                            </span>
                                        </div>
                                    </div>
                                    <div class="rm-cat-track">
                                        <div
                                            class="rm-cat-fill"
                                            :class="activeEntry.is_ours ? 'is-ours' : 'is-pub'"
                                            :style="{ width: `${Math.max(2, Math.min(100, cat.val))}%` }"
                                        ></div>
                                        <div
                                            class="rm-cat-marker"
                                            :style="{ left: `${Math.max(0, Math.min(100, cat.leaderVal))}%` }"
                                            :title="`${cat.leaderVal.toFixed(2)}%`"
                                        ></div>
                                    </div>
                                </div>
                            </div>

                            <div class="rm-cat-note">
                                <strong>{{ t('plugin.rolling-memory.eval.notePrefix') }}：</strong>
                                <span>{{ loc(activeEntry.note, activeEntry.note_en) }}</span>
                            </div>
                        </div>
                    </div>
                </el-col>
            </el-row>

            <!-- BuildAdmin 标准 .table-header 筛选与排序工具栏 + 完整榜单明细表 -->
            <div class="rm-filter-bar table-header">
                <div class="rm-filter-left">
                    <el-tooltip :content="t('common.refresh')" placement="top" :enterable="false" :hide-after="0" :show-after="250">
                        <el-button v-blur color="#40485b" type="info" size="small" :loading="loading" @click="loadLeaderboard">
                            <Icon size="14" name="lucide-refresh-cw" color="#ffffff" />
                        </el-button>
                    </el-tooltip>
                    <strong style="margin-left: 10px">{{ t('plugin.rolling-memory.leaderboard.tableSectionTitle') }}</strong>
                </div>
                <div class="rm-filter-right">
                    <el-tag size="small" type="info" effect="plain">
                        {{ isEn ? 'Compare row by row against the Judge column' : '横向比较请按「判官模型」列逐行对照' }}
                    </el-tag>
                </div>
            </div>

            <el-table
                v-loading="loading"
                :element-loading-text="loadLbText"
                :data="filteredEntries"
                size="small"
                border
                stripe
                highlight-current-row
                class="ag-data-table rm-lb-table"
                header-cell-class-name="table-header-cell"
                :row-class-name="tableRowClassName"
                @row-click="onLbTableRowClick"
            >
                <el-table-column :label="t('plugin.rolling-memory.leaderboard.colRank')" width="55" align="center">
                    <template #default="{ $index, row }">
                        <span class="rm-rank-badge" :class="rankBadgeClass(row.not_ranked ? 0 : $index + 1)">
                            {{ row.not_ranked ? '—' : `#${$index + 1}` }}
                        </span>
                    </template>
                </el-table-column>
                <el-table-column :label="t('plugin.rolling-memory.leaderboard.colSystem')" min-width="175">
                    <template #default="{ row }">
                        <div class="rm-cell-multiline">
                            <div class="rm-sys-title-line">
                                <strong class="rm-sys-primary">{{ loc(row.system, row.system_en) }}</strong>
                            </div>
                            <div class="rm-sys-tags-line">
                                <el-tag size="small" :type="row.is_ours ? 'success' : 'info'">
                                    {{
                                        row.is_ours
                                            ? t('plugin.rolling-memory.leaderboard.tagOurs')
                                            : t('plugin.rolling-memory.leaderboard.tagPublic')
                                    }}
                                </el-tag>
                                <el-tag v-if="loc(row.highlight_tag, row.highlight_tag_en)" size="small" type="warning" effect="plain">
                                    {{ loc(row.highlight_tag, row.highlight_tag_en) }}
                                </el-tag>
                            </div>
                        </div>
                    </template>
                </el-table-column>
                <el-table-column :label="t('plugin.rolling-memory.leaderboard.colVendor')" min-width="145">
                    <template #default="{ row }">
                        <div class="rm-cell-multiline">
                            <div class="rm-vendor-primary">{{ loc(row.vendor, row.vendor_en) }}</div>
                            <div class="rm-sub-text">{{ loc(row.architecture, row.architecture_en) }}</div>
                        </div>
                    </template>
                </el-table-column>
                <el-table-column :label="t('plugin.rolling-memory.leaderboard.colProtocol')" min-width="150">
                    <template #default="{ row }">
                        <div class="rm-cell-multiline">
                            <div>
                                <code class="rm-code-inline">{{ loc(row.protocol, row.protocol_en) }}</code>
                            </div>
                            <div class="rm-sub-text">{{ loc(row.question_set, row.question_set_en) }}</div>
                            <div class="rm-sub-text">
                                {{ t('plugin.rolling-memory.leaderboard.judgePrefix') }}: <strong>{{ loc(row.judge_model, row.judge_model_en) }}</strong>
                            </div>
                        </div>
                    </template>
                </el-table-column>
                <el-table-column :label="t('plugin.rolling-memory.leaderboard.colSingleHop')" width="88" align="right">
                    <template #header>
                        <span>
                            {{ t('plugin.rolling-memory.leaderboard.colSingleHop') }}
                            <el-popover trigger="click" placement="top" :width="340" popper-class="rm-help-popper">
                                <template #reference>
                                    <button type="button" class="tip-q" :aria-label="isEn ? 'Cat 4 Single-Hop semantics' : '单跳事实（Cat 4）语义说明'">
                                        ?
                                    </button>
                                </template>
                                <div class="rm-help-body">
                                    <p v-if="isEn">Cat 4 Single-Hop: Direct factual retrieval from a single conversation session</p>
                                    <p v-else>单跳事实（Cat 4 · Single-Hop）：单轮对话内直接显式事实的精准定位与回答</p>
                                </div>
                            </el-popover>
                        </span>
                    </template>
                    <template #default="{ row }">
                        <span class="rm-num-cell" :class="{ 'rm-best-num': row.single_hop >= 89.1 }"> {{ row.single_hop.toFixed(2) }}% </span>
                    </template>
                </el-table-column>
                <el-table-column :label="t('plugin.rolling-memory.leaderboard.colMultiHop')" width="88" align="right">
                    <template #header>
                        <span>
                            {{ t('plugin.rolling-memory.leaderboard.colMultiHop') }}
                            <el-popover trigger="click" placement="top" :width="340" popper-class="rm-help-popper">
                                <template #reference>
                                    <button type="button" class="tip-q" :aria-label="isEn ? 'Cat 1 Multi-Hop semantics' : '多跳推理（Cat 1）语义说明'">
                                        ?
                                    </button>
                                </template>
                                <div class="rm-help-body">
                                    <p v-if="isEn">Cat 1 Multi-Hop: Synthesizing multiple facts scattered across different sessions/turns</p>
                                    <p v-else>多跳推理（Cat 1 · Multi-Hop）：跨多个会话轮次串联多条离散事实的综合推理</p>
                                </div>
                            </el-popover>
                        </span>
                    </template>
                    <template #default="{ row }">
                        <span class="rm-num-cell" :class="{ 'rm-best-num': row.multi_hop >= 75.0 }"> {{ row.multi_hop.toFixed(2) }}% </span>
                    </template>
                </el-table-column>
                <el-table-column :label="t('plugin.rolling-memory.leaderboard.colOpenDomain')" width="88" align="right">
                    <template #header>
                        <span>
                            {{ t('plugin.rolling-memory.leaderboard.colOpenDomain') }}
                            <el-popover trigger="click" placement="top" :width="340" popper-class="rm-help-popper">
                                <template #reference>
                                    <button type="button" class="tip-q" :aria-label="isEn ? 'Cat 3 Open-Domain semantics' : '开放域推断（Cat 3）语义说明'">
                                        ?
                                    </button>
                                </template>
                                <div class="rm-help-body">
                                    <p v-if="isEn">Cat 3 Open-Domain: Combining conversational memory with external world knowledge</p>
                                    <p v-else>开放域推断（Cat 3 · Open-Domain）：结合对话记忆与外部世界常识的推理题</p>
                                </div>
                            </el-popover>
                        </span>
                    </template>
                    <template #default="{ row }">
                        <span class="rm-num-cell" :class="{ 'rm-best-num': row.open_domain >= 81.3 }"> {{ row.open_domain.toFixed(2) }}% </span>
                    </template>
                </el-table-column>
                <el-table-column :label="t('plugin.rolling-memory.leaderboard.colTemporal')" width="88" align="right">
                    <template #header>
                        <span>
                            {{ t('plugin.rolling-memory.leaderboard.colTemporal') }}
                            <el-popover trigger="click" placement="top" :width="340" popper-class="rm-help-popper">
                                <template #reference>
                                    <button type="button" class="tip-q" :aria-label="isEn ? 'Cat 2 Temporal semantics' : '时序推理（Cat 2）语义说明'">
                                        ?
                                    </button>
                                </template>
                                <div class="rm-help-body">
                                    <p v-if="isEn">
                                        Cat 2 Temporal Reasoning: Relative time anchoring, chronological ordering, and duration calculation
                                    </p>
                                    <p v-else>时序推理（Cat 2 · Temporal）：相对时间消解、事件先后顺序与时间跨度推算</p>
                                </div>
                            </el-popover>
                        </span>
                    </template>
                    <template #default="{ row }">
                        <span class="rm-num-cell" :class="{ 'rm-best-num': row.temporal >= 87.9 }"> {{ row.temporal.toFixed(2) }}% </span>
                    </template>
                </el-table-column>
                <el-table-column :label="t('plugin.rolling-memory.leaderboard.colOverall')" width="96" align="right">
                    <template #header>
                        <span>
                            {{ t('plugin.rolling-memory.leaderboard.colOverall') }}
                            <el-popover trigger="click" placement="top" :width="340" popper-class="rm-help-popper">
                                <template #reference>
                                    <button type="button" class="tip-q" :aria-label="isEn ? 'Overall accuracy semantics' : '综合总判对率语义说明'">
                                        ?
                                    </button>
                                </template>
                                <div class="rm-help-body">
                                    <p v-if="isEn">Overall Accuracy across all scored questions under the locomo-backboard Judge</p>
                                    <p v-else>在 locomo-backboard 冻结裁判下的全题集综合总判对率（Overall Accuracy）</p>
                                </div>
                            </el-popover>
                        </span>
                    </template>
                    <template #default="{ row }">
                        <strong class="rm-overall-num" :class="{ 'is-ours-num': row.is_ours }"> {{ row.overall.toFixed(2) }}% </strong>
                    </template>
                </el-table-column>
                <el-table-column :label="t('plugin.rolling-memory.leaderboard.colNote')" min-width="190">
                    <template #default="{ row }">
                        <div class="rm-note-cell">{{ loc(row.note, row.note_en) }}</div>
                    </template>
                </el-table-column>
            </el-table>
        </el-card>

        <!-- 底部：内置 locomo-backboard 协议栈与冻结 Prompt 审计选项卡 -->
        <el-card shadow="never" class="rm-audit-card">
            <template #header>
                <div class="rm-audit-header">
                    <div>
                        <strong>{{ t('plugin.rolling-memory.leaderboard.protoDetailTitle') }}</strong>
                        <span class="rm-muted" style="margin-left: 8px">
                            {{
                                isEn
                                    ? '1:1 protocol parity with Backboard-Locomo-Benchmark · 3 read-only SHA-1 prompt pins'
                                    : '与 Backboard-Locomo-Benchmark 1:1 协议同构 · 三枚只读 SHA-1 提示词指纹'
                            }}
                        </span>
                    </div>
                    <el-button v-blur size="small" type="warning" plain @click="goATMLeaderboard">
                        {{ t('plugin.rolling-memory.leaderboard.siblingAtmBtn') }}
                    </el-button>
                </div>
            </template>

            <el-tabs v-model="auditTab" class="rm-audit-tabs">
                <el-tab-pane :label="isEn ? '1. Protocol Parity & Methodology' : '1. 协议同构与对标说明'" name="spec">
                    <el-row :gutter="12" style="margin-bottom: 14px">
                        <el-col :xs="24" :md="8" class="rm-col-stretch">
                            <div class="rm-pillar-box">
                                <div class="rm-pillar-hdr">
                                    <strong>{{ t('plugin.rolling-memory.leaderboard.card1Title') }}</strong>
                                    <el-tag size="small" type="primary">{{ t('plugin.rolling-memory.leaderboard.card1Tag') }}</el-tag>
                                </div>
                                <div class="rm-pillar-body">{{ t('plugin.rolling-memory.leaderboard.card1Body') }}</div>
                            </div>
                        </el-col>
                        <el-col :xs="24" :md="8" class="rm-col-stretch">
                            <div class="rm-pillar-box">
                                <div class="rm-pillar-hdr">
                                    <strong>{{ t('plugin.rolling-memory.leaderboard.card2Title') }}</strong>
                                    <el-tag size="small" type="success">{{ t('plugin.rolling-memory.leaderboard.card2Tag') }}</el-tag>
                                </div>
                                <div class="rm-pillar-body">{{ t('plugin.rolling-memory.leaderboard.card2Body') }}</div>
                            </div>
                        </el-col>
                        <el-col :xs="24" :md="8" class="rm-col-stretch">
                            <div class="rm-pillar-box">
                                <div class="rm-pillar-hdr">
                                    <strong>{{ t('plugin.rolling-memory.leaderboard.card3Title') }}</strong>
                                    <el-tag size="small" type="warning">{{ t('plugin.rolling-memory.leaderboard.card3Tag') }}</el-tag>
                                </div>
                                <div class="rm-pillar-body">{{ t('plugin.rolling-memory.leaderboard.card3Body') }}</div>
                            </div>
                        </el-col>
                    </el-row>

                    <el-descriptions :column="2" border size="small" class="rm-proto-desc">
                        <el-descriptions-item :label="t('plugin.rolling-memory.leaderboard.lblBuiltinProto')">
                            <div class="rm-desc-val">
                                <el-tag size="small" type="primary">{{ protocolSpec.builtin_protocol || 'locomo-backboard' }}</el-tag>
                                <code class="rm-code-inline">{{ protocolSpec.protocol_id || 'locomo-frozen-2026-10-11' }}</code>
                            </div>
                        </el-descriptions-item>
                        <el-descriptions-item :label="t('plugin.rolling-memory.leaderboard.lblTargetBoard')">
                            <strong class="rm-desc-text">
                                {{ loc(protocolSpec.leaderboard_name, protocolSpec.leaderboard_name_en) || 'locomo-backboard' }}
                            </strong>
                        </el-descriptions-item>
                        <el-descriptions-item :label="t('plugin.rolling-memory.leaderboard.lblCorpusSpec')" :span="2">
                            <span class="rm-desc-text">{{ loc(protocolSpec.corpus_name, protocolSpec.corpus_name_en) }}</span>
                        </el-descriptions-item>
                        <el-descriptions-item :label="t('plugin.rolling-memory.leaderboard.lblAnswerSpec')" :span="2">
                            <span class="rm-desc-text">{{ loc(protocolSpec.answer_payload_spec, protocolSpec.answer_payload_spec_en) }}</span>
                        </el-descriptions-item>
                        <el-descriptions-item :label="t('plugin.rolling-memory.leaderboard.lblJudgeSpec')" :span="2">
                            <span class="rm-desc-text">{{ loc(protocolSpec.judge_verdict_spec, protocolSpec.judge_verdict_spec_en) }}</span>
                        </el-descriptions-item>
                        <el-descriptions-item :label="t('plugin.rolling-memory.leaderboard.lblCatMap')" :span="2">
                            <span class="rm-desc-text">{{ loc(protocolSpec.category_mapping_note, protocolSpec.category_mapping_note_en) }}</span>
                        </el-descriptions-item>
                        <el-descriptions-item :label="t('plugin.rolling-memory.leaderboard.lblFairNotice')" :span="2">
                            <span class="rm-desc-text">{{ loc(protocolSpec.fair_comparison_notice, protocolSpec.fair_comparison_notice_en) }}</span>
                        </el-descriptions-item>
                    </el-descriptions>
                </el-tab-pane>

                <el-tab-pane :label="isEn ? '2. Frozen Official Prompts & SHA-1 Pins' : '2. 官方冻结提示词原文与 SHA-1 钉'" name="prompts">
                    <el-row :gutter="14">
                        <el-col v-for="ag in agents" :key="ag.code" :xs="24" :lg="12" class="rm-col-stretch">
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
                                        <code class="rm-code-inline">locomo-backboard ({{ ag.protocol }})</code>
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

interface CategoryScores {
    cat1: number
    cat2: number
    cat3: number
    cat4: number
}

interface LeaderboardEntry {
    rank: number
    not_ranked?: boolean
    id: string
    system: string
    system_en?: string
    vendor: string
    vendor_en?: string
    architecture: string
    architecture_en?: string
    protocol: string
    protocol_en?: string
    question_set: string
    question_set_en?: string
    judge_model: string
    judge_model_en?: string
    single_hop: number
    multi_hop: number
    open_domain: number
    temporal: number
    overall: number
    evidence_hit: number
    is_ours: boolean
    is_public_board: boolean
    highlight_tag: string
    highlight_tag_en?: string
    note: string
    note_en?: string
    categories: CategoryScores
}

interface ProtocolParitySpec {
    builtin_protocol: string
    protocol_id: string
    leaderboard_name: string
    leaderboard_name_en?: string
    corpus_name: string
    corpus_name_en?: string
    answer_system_prompt: string
    answer_system_sha1: string
    answer_payload_spec: string
    answer_payload_spec_en?: string
    judge_system_prompt: string
    judge_system_sha1: string
    judge_template_sha1: string
    judge_template_bytes: number
    judge_verdict_spec: string
    judge_verdict_spec_en?: string
    all_pins_verified: boolean
    category_mapping_note: string
    category_mapping_note_en?: string
    fair_comparison_notice: string
    fair_comparison_notice_en?: string
}

interface OfficialProtocolAgent {
    code: string
    name: string
    name_en?: string
    role: string
    protocol: string
    model: string
    system_prompt: string
    system_sha1: string
    user_template: string
    user_template_sha1: string
    pin_verified: boolean
}

interface EvalLeaderboardResponse {
    protocol_spec: ProtocolParitySpec
    entries: LeaderboardEntry[]
    agents: OfficialProtocolAgent[]
}

const loading = ref(false)
const { percent: loadLbPct, text: loadLbText } = useLoadingProgressText(loading)
const filterMode = ref<'all' | 'public' | 'ours'>('all')
const sortKey = ref<'overall' | 'single_hop' | 'multi_hop' | 'open_domain' | 'temporal'>('overall')
const auditTab = ref<'spec' | 'prompts'>('spec')
const entries = ref<LeaderboardEntry[]>([])
const protocolSpec = ref<Partial<ProtocolParitySpec>>({})
const agents = ref<OfficialProtocolAgent[]>([])
const selectedEntry = ref<LeaderboardEntry | null>(null)

const filteredEntries = computed<LeaderboardEntry[]>(() => {
    const list = entries.value.filter((item) => {
        if (filterMode.value === 'public') return item.is_public_board
        if (filterMode.value === 'ours') return item.is_ours
        return true
    })
    const key = sortKey.value
    return [...list].sort((a, b) => Number(b[key] || 0) - Number(a[key] || 0))
})

const activeEntry = computed<LeaderboardEntry | null>(() => {
    if (selectedEntry.value) return selectedEntry.value
    const ours = entries.value.find((e) => e.id === 'lb-rm-current-local-plain')
    return ours || entries.value[0] || null
})

// 外部商业榜首 Backboard 四项基准分：Single-hop 89.36, Multi-hop 75.00, Open-domain 91.20, Temporal 91.90
const activeCategoryBars = computed(() => {
    const cur = activeEntry.value
    return [
        {
            key: 'single_hop',
            label: t('plugin.rolling-memory.eval.cat4Label'),
            val: cur?.single_hop ?? 0,
            leaderVal: 89.36,
        },
        {
            key: 'multi_hop',
            label: t('plugin.rolling-memory.eval.cat1Label'),
            val: cur?.multi_hop ?? 0,
            leaderVal: 75.0,
        },
        {
            key: 'open_domain',
            label: t('plugin.rolling-memory.eval.cat3Label'),
            val: cur?.open_domain ?? 0,
            leaderVal: 91.2,
        },
        {
            key: 'temporal',
            label: t('plugin.rolling-memory.eval.cat2Label'),
            val: cur?.temporal ?? 0,
            leaderVal: 91.9,
        },
    ]
})

const rankBadgeClass = (rank: number) => {
    if (rank === 1) return 'is-gold'
    if (rank === 2) return 'is-silver'
    if (rank === 3) return 'is-bronze'
    return ''
}

const tableRowClassName = ({ row }: { row: LeaderboardEntry }) => {
    if (row.is_ours) return 'rm-ours-table-row'
    return ''
}

const onLbTableRowClick = (row: LeaderboardEntry) => {
    selectedEntry.value = row
    const el = document.querySelector('.plugin-rm-leaderboard .rm-inspector-row')
    if (el) {
        const rect = el.getBoundingClientRect()
        if (rect.top < 0 || rect.bottom > window.innerHeight) {
            el.scrollIntoView({ behavior: 'smooth', block: 'nearest' })
        }
    }
}

const copyText = async (text: string, enPrefix = 'Copied', zhPrefix = '已复制') => {
    const value = String(text || '').trim()
    if (!value) return
    try {
        await navigator.clipboard.writeText(value)
        ElMessage.success(`${isEn.value ? enPrefix : zhPrefix}: ${value}`)
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

const loadLeaderboard = async () => {
    loading.value = true
    try {
        const res = await request<EvalLeaderboardResponse>({
            url: '/admin/plugin/rolling-memory/eval/leaderboard',
            method: 'POST',
            data: {},
        })
        const data = res.data.data
        if (data) {
            entries.value = data.entries || []
            protocolSpec.value = data.protocol_spec || {}
            agents.value = data.agents || []
            if (!selectedEntry.value && entries.value.length > 0) {
                selectedEntry.value = entries.value.find((e) => e.id === 'lb-rm-current-local-plain') || entries.value[0]
            }
        }
    } catch {
        // 业务与网络错误由请求拦截器统一提示。
    } finally {
        loading.value = false
    }
}

const goEvalLab = (tier?: string | Event) => {
    void tier
    router.push({ path: '/admin/plugin/rolling-memory/eval', query: { suite: 'locomo', tier: '1540' } })
}

const goATMLeaderboard = () => {
    router.push({ path: '/admin/plugin/rolling-memory/atm-leaderboard' })
}

onMounted(() => {
    loadLeaderboard()
})
</script>

<style scoped lang="scss">
.plugin-rm-leaderboard {
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
        background: var(--el-color-warning);
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
        flex-shrink: 0;
        gap: 4px;
        align-items: center;
    }

    .stat-label {
        font-size: 12.5px;
        font-weight: 600;
        color: var(--el-text-color-secondary);
    }

    .stat-main-val {
        margin: 8px 0 6px;
        font-size: 22px;
        font-weight: 700;
        font-variant-numeric: tabular-nums;
        line-height: 1.25;
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

    .rm-sort-select {
        width: 240px;
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
        font-size: 13.5px;
        font-weight: 600;
        color: var(--el-text-color-primary);
    }

    .rm-chart-legend {
        display: flex;
        flex-wrap: wrap;
        gap: 12px;
        align-items: center;
        font-size: 12px;
    }

    .rm-leg-item {
        display: inline-flex;
        gap: 6px;
        align-items: center;
    }

    .rm-leg-dot {
        display: inline-block;
        width: 10px;
        height: 10px;
        border-radius: 2px;

        &.is-ours {
            background: #10b981;
        }

        &.is-pub {
            background: #3b82f6;
        }
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
        gap: 6px;
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
        align-items: flex-start;
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
        line-height: 1.4;
        color: var(--el-text-color-primary);
    }

    .rm-mini-tag {
        flex-shrink: 0;
    }

    .rm-ladder-score {
        flex-shrink: 0;
        font-size: 14px;
        font-weight: 700;
        font-variant-numeric: tabular-nums;

        &.is-ours-score {
            color: var(--el-color-success);
        }
    }

    .rm-ladder-bar-wrap {
        position: relative;
        height: 8px;
        background: var(--el-fill-color);
        border-radius: 4px;
    }

    .rm-ladder-bar {
        height: 100%;
        border-radius: 4px;

        &.is-ours {
            background: linear-gradient(90deg, #10b981, #059669);
        }

        &.is-pub {
            background: linear-gradient(90deg, #60a5fa, #2563eb);
        }
    }

    .rm-ladder-ref-line {
        position: absolute;
        top: -2px;
        width: 2px;
        height: 12px;
        background: #ef4444;
    }

    .rm-rank-badge {
        display: inline-block;
        flex-shrink: 0;
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

    .rm-cat-breakdown {
        display: flex;
        flex: 1;
        flex-direction: column;
        justify-content: space-between;
        gap: 14px;
    }

    .rm-active-meta {
        display: flex;
        flex-direction: column;
        gap: 6px;
        padding: 10px 12px;
        font-size: 12.5px;
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
        font-size: 12px;
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
        gap: 12px;
    }

    .rm-cat-item {
        display: flex;
        flex-direction: column;
        gap: 5px;
    }

    .rm-cat-top {
        display: flex;
        flex-wrap: wrap;
        gap: 6px;
        align-items: center;
        justify-content: space-between;
        font-size: 12.5px;
    }

    .rm-cat-label-wrap {
        display: flex;
        flex-wrap: wrap;
        gap: 6px;
        align-items: center;
    }

    .rm-cat-name {
        font-weight: 500;
    }

    .rm-cat-nums {
        display: flex;
        flex-wrap: wrap;
        gap: 6px;
        align-items: baseline;
        font-variant-numeric: tabular-nums;
    }

    .rm-cat-val-txt {
        font-size: 13.5px;
    }

    .rm-vs-leader {
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
            color: var(--el-color-warning);
        }
    }

    .rm-cat-track {
        position: relative;
        height: 9px;
        background: var(--el-fill-color);
        border-radius: 5px;
    }

    .rm-cat-fill {
        height: 100%;
        border-radius: 5px;

        &.is-ours {
            background: #10b981;
        }

        &.is-pub {
            background: #3b82f6;
        }
    }

    .rm-cat-marker {
        position: absolute;
        top: -2px;
        width: 2px;
        height: 13px;
        background: #ef4444;
    }

    .rm-cat-note {
        padding-top: 10px;
        font-size: 12px;
        line-height: 1.55;
        color: var(--el-text-color-regular);
        border-top: 1px dashed var(--el-border-color-lighter);
    }

    .rm-filter-bar {
        display: flex;
        flex-wrap: wrap;
        gap: 12px;
        align-items: center;
        justify-content: space-between;
        padding: 12px 14px;
        margin-bottom: 0;
        font-size: 13.5px;
        background-color: var(--ag-bg-color-overlay);
        border: 1px solid var(--ag-border-color);
        border-bottom: none;
    }

    .rm-filter-right {
        display: flex;
        flex-wrap: wrap;
        gap: 10px;
        align-items: center;
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

    .rm-cell-multiline {
        display: flex;
        flex-direction: column;
        gap: 4px;
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

    .rm-vendor-primary {
        font-size: 12.5px;
        font-weight: 500;
        color: var(--el-text-color-primary);
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

    .rm-audit-header {
        display: flex;
        flex-wrap: wrap;
        gap: 10px;
        align-items: center;
        justify-content: space-between;
    }

    .rm-pillar-box {
        display: flex;
        flex-direction: column;
        width: 100%;
        padding: 12px 14px;
        background: var(--el-fill-color-lighter);
        border: 1px solid var(--el-border-color-lighter);
        border-radius: 8px;
    }

    .rm-pillar-hdr {
        display: flex;
        gap: 8px;
        align-items: center;
        justify-content: space-between;
        margin-bottom: 6px;
        font-size: 13px;
    }

    .rm-pillar-body {
        font-size: 12px;
        line-height: 1.6;
        color: var(--el-text-color-regular);
    }

    .rm-desc-val {
        display: flex;
        flex-wrap: wrap;
        gap: 8px;
        align-items: center;
    }

    .rm-desc-text {
        line-height: 1.6;
        word-break: break-word;
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
