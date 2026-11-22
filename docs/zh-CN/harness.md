# 多宿主一键接入指南（Multi-Harness Integration）

**[简体中文](harness.md) · [English](../harness.md)**

> 本文件描述 `rolling-memory install` 的多宿主（Harness）适配架构、注册表落点矩阵、启动器机制与验收判据。

## 1. 设计目标

Rolling Memory 的机械记忆核本身与具体 Coding Agent 宿主完全解耦；不同宿主之间的差异仅在于四项配置落点：**技能根目录（Skill Root）**、**MCP 服务注册文件**、**生命周期 Hooks 事件**与**全局/项目级指令文件**。

`rolling-memory install` 为多种宿主提供统一配置入口，支持备份与幂等写入。`install --check` 检查注册项，并单独探测所选启动器。

---

## 2. 行业接入形态对比

| 接入形态 | 典型做法 | 维护与使用成本 |
|---|---|---|
| **仅提供 MCP + 手工贴配置** | 提供 stdio MCP server，由用户自行查找各宿主的 JSON / TOML 路径并手写配置片段 | 用户需自行处理路径差异、转义格式与 PATH 裁剪问题；无自动备份、无冲突保护 |
| **单一宿主专用插件** | 绑定单一 IDE 或 CLI 的专有扩展接口与专有命令格式 | 跨工具切换时记忆与习惯割裂，无法在多款 Agent 之间共用同一本地记忆库 |
| **`install` 注册表与原生插件** | stdio MCP（单工具 `recall` · 回忆）、多宿主配置及独立启动器探测 | 配置已探测的宿主；遇冲突需人工核查，并在宿主会话中验证加载情况 |

---

## 3. 分层契约

```text
Harness Agent + Skill        ← 语义理解层：依据技能（SKILL.md）与指令文件按需调用 recall（回忆即记忆）
        │  MCP tools/call
rolling-memory install       ← 宿主适配层：注册表 + 保守写手（JSON / TOML / YAML patch）+ 握手核验
        │  写入启动器与注册项
MCP stdio · CLI · Hooks      ← 机械记忆核：零宿主耦合，标准进程通信契约
        │
SQLite 明文金库 (WAL)         ← 本地存储层：本地明文金库
```

- **机械核（`facade` / `mcp` / `cli` / `hooks`）零宿主耦合**：不依赖任何特定宿主 SDK，仅通过标准进程协议通信（MCP 为逐行 JSON-RPC 2.0，CLI 为 argv/stdin，Hooks 为 JSON stdin → JSON stdout）。
- **适配层（`internal/harness`）只做保守接线**：负责将启动器路径写入宿主配置文件、同步 `SKILL.md`、按需挂载 Hooks 与指令块，不介入任何记忆语义。
- **提醒型钩子与 Agent 主动检索**：可选的逐轮钩子（`--with-hooks`）不自动检索记忆，由 Agent 决定何时调用 `recall`。钩子和后台扫描可导入受支持的本地来源，但不保证捕获全部对话。
- **收口 `recall` 自评与自定义 `SKILL.md`**：`SKILL.md` 最末尾设有独立可选区块（`§5 收口如实评价`），约定 Agent 在本轮调用过 `recall` 时，于最终回复收尾附一行简短复盘（报告本轮调用次数、预计节省的盲搜/排障往返及具体依据、以及召回内容是否真正有帮助）。用户可随时打开已安装的 `<技能根>/rolling-memory/SKILL.md` 直接修改或删去末尾这一区块（或在 `rolling-memory install` 时传入 `--skill-file <路径>`）。

---

## 4. 支持的宿主矩阵

### 4.1 宿主注册位置

| 宿主 | 技能根（用户级） | MCP 注册位置 | 生命周期 Hooks | 指令文件（用户级 / 项目级） |
|---|---|---|---|---|
| **ZCode** | `~/.zcode/skills/` | `~/.zcode/cli/config.json` → `mcp.servers` | ✅ `hooks.events`（需 `hooks.enabled=true`） | `~/.zcode/AGENTS.md` / `AGENTS.md` |
| **Claude Code** | `~/.claude/skills/` | `~/.claude.json` → `mcpServers`（项目级 `.mcp.json`） | ✅ `~/.claude/settings.json` → `hooks` | `~/.claude/CLAUDE.md` / `CLAUDE.md` |
| **Codex** | `~/.codex/skills/` | `~/.codex/config.toml` → `[mcp_servers.<name>]` | ✅ `~/.codex/hooks.json`（可选，`--with-hooks`；首次需在 Codex 内评审信任） | `~/.codex/AGENTS.md` / `AGENTS.md` |
| **OpenCode** | `~/.config/opencode/skills/` | `~/.config/opencode/opencode.json(c)` → `mcp.servers`（项目级 `.opencode/opencode.json`） | —（由 Skill 驱动） | — / `AGENTS.md` |
| **DeepSeek Harness (`dsh`)** | `~/.dsh/skills/`（`$DSH_HOME`） | `~/.dsh/profiles/<profile>/cordis.patch.yml` → `dsh-mcp-client` | —（由 Skill 驱动） | `~/.dsh/AGENTS.md` / `AGENTS.md` |
| **CodeBuddy** | `~/.codebuddy/skills/` | `~/.codebuddy/mcp.json` → `mcpServers` | —（由 Skill 驱动） | — / `CODEBUDDY.md` |

### 4.2 其他宿主与原生插件

- **其他宿主**：安装器也会探测 **Cursor**、**Windsurf**、**Cline / Roo Code**、**WorkBuddy** 和**豆包**。如需只选择特定宿主，可通过 `rolling-memory install <id>` 指定。请检查安装输出和宿主当前的配置位置：不同宿主版本使用的路径可能不同。WorkBuddy 插件包含技能、MCP 注册项和提示钩子，但需要先单独安装本地 CLI；宿主不提供插件根路径时，MCP 启动器使用已安装的本地 CLI，缺少 CLI 则无法启动，也不会自动下载。豆包的 MCP 连接器需手工配置（见 §4.4）。
- **原生插件**：启用的插件声明了同名技能或 MCP 服务时，`install` 不会在旁边再写一份。已有副本若由本工具写入，会报告为重复项；运行 `rolling-memory install <id> --dedupe` 只移除本工具拥有并写入的那一份。请核查宿主配置和插件状态；插件声明不代表宿主已加载。

### 4.3 三类安全配置文件写手

| 格式 | 适用宿主 | 写入与校验策略 |
|---|---|---|
| **JSON 深合并** | ZCode / Claude Code / CodeBuddy / Cursor / Windsurf / Cline / WorkBuddy | 解析既有 JSON → 沿键路径更新目标条目 → 其余配置项与键顺序原样保留 |
| **TOML 段落替换** | Codex | 定位 `[mcp_servers.<name>]`（含 `.env` 子表）精准替换或追加，保留注释与其余段落；写入前经 `go-toml` 完整语法回验 |
| **YAML 标记块追加** | `dsh` | 以 `# >>> rolling-memory (<name>) >>>` 闭合标记块追加或替换；卸载时仅精准移除标记块内部内容 |

### 4.4 豆包（Doubao）新用户接入：从零到可用

面向第一次把本工具接进豆包桌面端的用户：**技能面一条命令**，MCP 工具面需在 App 内补一步。

**第 0 步 · 前置**：豆包桌面端已安装并启动过至少一次（首启会建用户工作区 `~/Doubao`）；本工具已安装（`rolling-memory --version` 有输出）。

**第 1 步 · 写入技能**（双根写入，即豆包客户端自报的 `local_skill_paths`）：

```bash
rolling-memory install doubao            # 同时写入下面两个技能根
rolling-memory install doubao --check    # 复核：应见两行 skill ok
```

| 技能根 | 说明 |
|---|---|
| `~/Doubao/skills/<name>/SKILL.md` | 豆包用户工作区（与 `chats/` 同级），App 首启自建 |
| `~/.agents/skills/<name>/SKILL.md` | 跨 agent 约定根；同名技能优先于 additional 根 |

**第 2 步 · 让豆包扫到**：重启豆包或新开一个会话，即触发本地技能扫描（后台 `background_sync`）。会话技能列表应出现 `rolling-memory`；被扫到的 USER skill 会被豆包打包上传到你的账号做跨设备同步——**无需开发者账号，也无上架审核**。

**第 3 步 · 接上 MCP 工具面（走地址）**：豆包的连接器接受**地址**式 MCP（`mcp_transport`: Streamable HTTP / SSE），但不接受任意绝对路径的本地命令（`mcp_command_path_policy` 会以 `STDIO_COMMAND_ABSOLUTE_NOT_ALLOWED` 拒掉包装脚本）。所以由本工具起一个本地 URL 服务，把地址填进豆包：

```bash
rolling-memory mcp --http                                    # 默认 http://127.0.0.1:8765/mcp（另有旧式 SSE: /sse）
rolling-memory mcp --http --addr 127.0.0.1:9000 --path /mcp  # 需要换端口 / 路径时
```

在豆包的连接器表单里选 **Streamable HTTP**，填 `http://127.0.0.1:8765/mcp`（若表单只给 SSE 选项，填 `http://127.0.0.1:8765/sse`）。保存后新开会话，工具列表应出现 `recall`（回忆）。连接工具不会自动导入豆包对话；采集范围见下节。

- 服务只绑回环地址（默认 `127.0.0.1`）、请求体上限 8 MiB、跨源请求拒绝；进程需常驻，建议交给系统服务托管。
- `install doubao` 的 MCP 一行仍显示 `skipped` 属**如实反映**：注册面在豆包侧（凭证加密存储 + manifest 下发），命令行写不进去，只能由你在 App 内填地址。

**排查**：技能未出现时，先确认两个技能根下都有 `SKILL.md`；再查豆包运行期日志 `~/Library/Application Support/Doubao/sdk_storage/log/saman_<日期>.log`，搜 `local_skill_paths`（客户端自报技能根）、`scan_local_skills`（扫描结果与 `item_count`）、`[MCP_SERVICE] get all tools … connector_count=`（MCP 装载实况）。地址式连接器未生效时先本地验证服务：`curl -s http://127.0.0.1:8765/mcp -H 'content-type: application/json' -d '{"jsonrpc":"2.0","id":1,"method":"tools/list"}'`。

**卸载**：`rolling-memory install doubao --uninstall`（只删本工具写入的技能文件，豆包侧账号副本需在 App 内移除）。

### 4.5 对话采集范围与手工导入

安装 MCP 服务或技能与采集对话历史是两件事。后台扫描器读取 ZCode 与 OpenCode 的本地会话数据库，以及 ZCode、Claude Code、Codex、DSH、CodeBuddy 和 WorkBuddy 的部分转录文件位置。OpenCode 的存储按 SQLite 读取：当前版本把会话放在 `session_v2`、消息放在 `session_message`，旧版本只有用于清点会话的 `session`。只有最后一个助手步骤已完成、未报错且未调用工具的轮次才会导入；调用工具的助手步骤属于过程旁白，不进入记忆。它支持 Codex 的 `.jsonl.zst` 归档，以及已观察到的 v4 格式 DSH 压缩 `.jsonl.zstd` 会话：用户消息须具有 `source.kind: user`，只有匹配的 `turn/start` 与 `turn/end` 事件确认成功完成（`reason.kind: completed`）的轮次才会导入。未知格式和无法确认成功完成的轮次不导入；不保证覆盖子代理对话。CodeBuddy 和 WorkBuddy 的预设 JSONL 位置可能与宿主实际存储不一致。Cursor、Windsurf、Cline 没有自动对话扫描器。豆包不直接读取应用内加密的实时存储；放到 `~/Doubao/exports/` 的本地 JSONL 对话导出（每个会话一个文件）会按同一套完成语义导入。已安装且受宿主支持的钩子也不能保证这些来源完整。

其他来源可导出或准备受支持的 JSONL 对话文件，用 `rolling-memory import /path/to/transcript` 手工导入并检查结果。文件导入仅接受 `shared` 或 `public` 受众；由于后台扫描无法执行文件级访问控制，受限或未知受众会被拒绝。请勿将敏感文件放入扫描目录；已有副本不会自动删除。手工导入和后台扫描均不保证完整或无损采集。工具契约见 [mcp.md](mcp.md)。检索与存储使用本地明文 `local_plain`；安装或更新软件是单独的操作，可能需要联网。

---

## 5. 常用命令

```bash
# 1. 查看宿主探测状态与目标注册路径
rolling-memory install --list

# 2. 预演安装（不修改任何文件，仅列出计划变更）
rolling-memory install --dry-run

# 3. 一键接入所有已探测到的宿主（含技能、MCP 与生命周期 Hooks）
rolling-memory install --with-hooks --with-instructions

# 4. 项目级隔离安装（数据库默认落在 ./.rolling-memory/memory.db，scope 默认 project:<目录名>）
cd /path/to/project && rolling-memory install --project zcode claude-code

# 5. 多实例共存安装（自定义 MCP 服务名与技能名）
rolling-memory install --name rmem --skill-name rmem

# 6. 指定特定宿主或 dsh 配置文件
rolling-memory install zcode dsh --profile web

# OpenCode：在用户配置中安装 Skill 和本地 MCP 服务
rolling-memory install opencode
rolling-memory install opencode --check

# 7. 静态配置核验 + 实时拉起 MCP 进程验证 initialize / tools/list 握手
rolling-memory install --check

# 8. 安全卸载（仅移除本工具写入的条目，外来配置原样保留）
rolling-memory install --uninstall

# 9. 原生插件已提供同一面时，移除本工具写入的重复副本
rolling-memory install zcode --dedupe
```

---

## 6. 四层验收判据

`rolling-memory install --check` 自动执行第 1–3 层机械核验；第 4 层可在新开启的 Agent 会话中验证：

| 层级 | 验收项 | 核验方式 |
|---|---|---|
| **第 1 层：技能为当前版本** | 宿主技能目录存在 `SKILL.md`、frontmatter 完整，且内容与当前发行版一致；旧的已拥有副本报 `outdated`，用户改过的副本报 `conflict` | 运行 `rolling-memory install --check`；新开会话可见对应技能名称 |
| **第 2 层：MCP 已注册** | 宿主配置文件中存在合法的 `rolling-memory` 服务条目，且启动器存在并为当前版本 | 运行 `rolling-memory install --check` 自动解析校验 |
| **第 3 层：进程握手通过** | 真实拉起 MCP 子进程，校验 `initialize` 与 `tools/list` 恰返回 `recall` 单工具 | `install --check` 顶部 `server ok · rolling-memory ... · tools: recall` |
| **第 4 层：会话行为** | 检查 Agent 是否调用 `recall`，以及预期来源是否已导入 | 在新会话中追问历史并检查本地记录；来源覆盖范围见 §4.5 |

---

## 7. 启动器 Shim 与 U 盘便携自愈锚点

GUI 类 IDE 在启动 MCP 子进程或 Hooks 时常会裁剪 `PATH` 环境变量，导致裸命令名无法找到可执行文件。为此，`install` 默认在宿主侧生成包含绝对路径的启动器 Shim：

```sh
# ~/.local/share/rolling-memory/bin/rolling-memory.sh（0755）
#!/bin/sh
# Rolling-Memory launcher (generated by `rolling-memory install`).
# Absolute path on purpose: harnesses often spawn MCP servers and hooks with a cropped PATH.
exec "<absolute>/bin/rolling-memory" "$@"
```

- **零外部运行时依赖**：Go 编译产物自包含 SQLite 与全部运行逻辑，启动器仅执行一次轻量 `exec`。
- **Windows 原生支持**：在 Windows 下自动生成 `%LOCALAPPDATA%\rolling-memory\bin\rolling-memory.cmd`（`@echo off` + 绝对路径 + `%*`）。
- **U 盘便携模式（Portable Host Anchor）**：当从带有 `.portable` 标记的 U 盘便携载荷执行 `install` 时，生成的启动器自动升级为**跨挂载点自愈锚点**——优先直连原路径；若发生盘符或挂载点名称漂移（如 macOS `/Volumes/USB 1` 或 Windows `F:\`），则自动扫描可移动介质挂载根寻回 `.portable` 载荷（完整制盘与安全拔盘规程见 [usb-portable.md](usb-portable.md)）。
- **所有权识别**：启动器头部包含精确的生成签名注释。后续 `install` 即使发现绝对二进制路径变化，也会先备份旧文件再接管自己的启动器；非本工具生成的同名脚本仍报告 `conflict` 并拒绝覆盖。

---

## 8. 备份、幂等与回滚保证

- **改前自动备份**：修改任何既有宿主配置文件前，自动生成带时间戳的备份文件 `<file>.rolling-memory.<YYYYmmddHHMMSS>.bak`。
- **严格幂等**：配置未发生变化时，重复执行 `install` 报告 `unchanged`，不产生多余写入或重复备份。
- **冲突保护**：若目标配置中已存在非本实例管理的同名条目，默认报告 `conflict` 而不覆盖；仅当显式传入 `--force` 时才执行替换。原生插件造成的重复只用 `--dedupe` 处理，它只移除本工具拥有并写入的副本；`--force` 不会在启用中的插件旁边再写一份。
- **一键回滚**：可将 `.bak` 备份文件直接还原，或执行 `rolling-memory install --uninstall` 精准移除全部已注册条目。

---

## 9. 在线自更新与金库安全契约（`rolling-memory update`）

> 完整安装、通道策略、破坏性变更与回滚说明见 [install-upgrade.md](install-upgrade.md)。

无需安装 `git` 或 Go 编译环境，即可通过 `rolling-memory update`一站式升级 CLI 二进制、本地 Web 控制台与全机宿主接线：

```bash
# 1. 只读检查：拉取发布渠道 SHA256SUMS 并比对本地组件 SHA-256 指纹（不修改任何文件）
rolling-memory update --check

# 2. 一键升级 CLI 二进制、刷新已接入宿主配置，并在已安装控制台时同步升级控制台
rolling-memory update

# 3. 同时下载安装或升级本地 Web 控制台（rolling-admin 后端 + web-dist.tar.gz 前端）
rolling-memory update --with-console

# 4. 指定自定义发布镜像源 URL（也可设置环境变量 ROLLING_MEMORY_RELEASE_BASE）
rolling-memory update --release-base https://mirror.example.com/rolling-memory/releases/latest/download

# 5. 输出结构化 JSON 结果供脚本或后台调用
rolling-memory update --check --json

# 6. 只解析最新版本标签（不下载产物、不比对指纹）
rolling-memory update --check --tags-only

# 7. 可选的本机定时检查：只刷新缓存并发送桌面通知
rolling-memory update --schedule install
rolling-memory update --schedule status
rolling-memory update --schedule remove
```

- **版本发现与可安装性分离**：`update --check` 的 `update_available` 由最新标签决定，`installable` 由发布渠道的 `SHA256SUMS` 与实际产物决定。只有标签、没有附件的版本会如实标记为“有新版本但暂不可一键安装”，控制台改为展示源码更新方式，而不是给出一个必然失败的升级按钮。
- **更新源**：`update.source` 可选 `github`（GitHub Releases 官方渠道，默认）或 `gitee`（Gitee 镜像，适合访问 GitHub 较慢的网络）。可在控制台「版本与更新」弹窗选择，用 `rmem config set update.source gitee` 保存，或用 `rmem update --source gitee` 单次覆盖。显式的 `--release-base` URL 或 `ROLLING_MEMORY_RELEASE_BASE` 仍然优先。Gitee 上只有标签、没有附带二进制时，会如实报告为“仅源码更新”，并给出指向 Gitee 仓库的源码重建提示。
- **后台版本感知（低频检查 + 实时推送）**：进入仪表盘时立即检查一次；控制台保持打开时每十分钟刷新一次缓存，结果沿同一条本地实时连接推回，前端不做高频轮询。CLI 的 `status` 与 `doctor` 读取同一份缓存，不发起网络请求。MCP 会话在后台刷新过期缓存，因此只用 Agent、不常开控制台的用户，也会在下一次查看状态时看到新版本。
- **长时间不使用控制台**：缓存保留最近一次结果（CLI / MCP 成功六小时内视为新鲜；控制台打开时每十分钟刷新；失败后三十分钟重试）。无论隔多久，只要控制台、MCP 会话或 CLI 再次运行，都会自动补一次检查；控制台首帧先显示上一次结果，刷新完成后原位更新。若产品完全没有任何进程在运行，客户端无法凭空发现更新；用 `rolling-memory update --schedule install` 注册内置定时任务（或用自己的计划任务执行 `update --auto`），已保存的策略就会在空闲期继续生效。
- **源码安装**：源码或本地构建安装会在发布渠道没有对应二进制时标记为“仅源码更新”，并展示按标签更新的命令，不启用一键升级；预编译安装继续走原有的 SHA-256 校验原位更新。
- **更新通道**：`stable`（预编译安装默认）只接受正式发布标签；`beta` 同时接受预发布标签；`dev`（源码安装显式写入）跟踪 `main` 最新提交。可用 `rolling-memory config set update.channel beta` 保存偏好（或设置 `ROLLING_MEMORY_UPDATE_CHANNEL=beta`），也可在控制台「版本与更新」弹窗切换，或用 `rolling-memory update --check --channel dev` 单次覆盖。定时检查会沿用已保存的通道。`dev` 不会自动替换二进制：只报告 `main@<commit>`，并要求源码重建（`ROLLING_MEMORY_SOURCE_REF=main bash install.sh`）。
- **本机定时更新**：`rolling-memory update --schedule install` 会注册当前用户级的定时任务（macOS launchd、Linux systemd user timer、Windows 任务计划程序），每六小时执行一次 `update --auto`。已保存的 `update.auto` 策略决定这次运行只刷新缓存并通知（`notify`）、安装经过校验的更新（`apply`），还是什么都不做（`off`）。`--schedule status` 查看状态，`--schedule remove` 移除。
- **源码标签重建**：源码安装可用 `ROLLING_MEMORY_SOURCE_REF=v0.0.7 bash install.sh`（或 `bash install.sh --ref v0.0.7`）从指定标签重新克隆并编译；Windows 可用 `powershell -ExecutionPolicy Bypass -File install.ps1 --ref v0.0.7`。安装元数据会记录来源；不会改动本地金库。
- **自动检查开关**：设置 `ROLLING_MEMORY_UPDATE_CHECK=off` 可关闭后台检查；设置 `ROLLING_MEMORY_UPDATE_API_BASE` 可改用企业镜像做版本发现。显式执行 `update --check` 始终可用。
- **SHA-256 强校验（Fail-Closed）**：每个下载产物（`rolling-memory-<os>-<arch>`、`rolling-admin-<os>-<arch>`、`web-dist.tar.gz`）在替换本地文件前均强制比对发布渠道的 `SHA256SUMS`；若校验缺失或不匹配则立即中止，绝不污染既有二进制。
- **本地守护进程安全边界**：共享的本地守护进程只有在用户、身份和配置核验通过后才能被控制。升级后若旧守护进程仍加载上一版二进制，普通请求会失败关闭；`rolling-memory daemon restart` 会按套接字所有权、金库路径与协议身份核验该同用户实例，令其退出并启动当前二进制。`install` 检测到该情况时会直接打印这条命令。请求发送后若发生失败，不会自动重试。共享守护进程以可信的本机用户为边界，不能保证抵御同一用户运行的任意代码。
- **本地金库零触碰安全承诺**：升级全过程仅替换可执行文件、前端静态资源与宿主配置项，**永不修改、清空或覆盖用户的本地 SQLite 明文金库（`~/.local/share/rolling-memory/memory.db`）**；数据库表结构演进均在打开时原位幂等迁移。
- **零开发环境依赖（免装 Git / Go / Node.js / Python）**：
  - `install.sh` 与 `install.ps1` 默认直接拉取经 `SHA256SUMS` 强校验的预编译静态二进制（POSIX 下自适应 `curl` 或 `wget`，Windows 下原生支持 x64 与 ARM64，并在主发布源不可达时自动回退备用镜像源）。
  - 安装脚本会把 `~/.local/bin`（Windows 为 `%LOCALAPPDATA%\rolling-memory\bin`）加入当前用户的 `PATH`，改动幂等并带标记块：zsh 写入 `~/.zshrc`，bash 同时写入登录配置文件与 `~/.bashrc`，fish 写入 `~/.config/fish/config.fish`，Windows 写入用户级 `PATH` 注册表项。设置 `ROLLING_MEMORY_NO_MODIFY_PATH=1` 可跳过；配置文件写不进去时，安装脚本会打印需要手工添加的 `export PATH=...` 行。
  - 本地 Web 控制台（`rolling-memory panel`）由 Go 单二进制内置静态服务（`--serve-web`）直接托管前端页面，无需在电脑上安装 Node.js 或 Python；在 `rolling-memory install` 时加上 `--desktop-shortcut` 即可在桌面生成双击启动图标。

**已经打开的终端里找不到 `rolling-memory` 命令？** `PATH` 条目只对安装之后新启动的 shell 生效；安装脚本每次写入配置文件时也会打印同样的提醒。复制下面属于你的那一行，再用 `rolling-memory --version` 确认：

```bash
# bash / zsh
export PATH="$HOME/.local/bin:$PATH"
rolling-memory --version
```

```fish
# fish
fish_add_path ~/.local/bin
rolling-memory --version
```

```powershell
# Windows —— 重开窗口，或只更新当前会话
$env:Path = "$env:LOCALAPPDATA\rolling-memory\bin;$env:Path"
rolling-memory --version
```

## 10. 从旧版本升级

替换旧版本不需要先卸载，也不需要导出再重导记忆：

1. **先停掉使用方。** 退出桌面应用和正在使用记忆工具的 Agent 会话；更新器会自行停止共享的本地守护进程。
2. **替换程序。** 运行 `rolling-memory update`、重新运行 `install.sh` / `install.ps1`，或直接覆盖应用包。二进制会原地替换，宿主接线会刷新；已安装的宿主配置文件会先备份。
3. **重新打开宿主应用。** 已经打开的会话仍使用旧进程，新会话才会加载新版本。
4. **确认状态。** `rolling-memory doctor --json` 查看路径与完整性，`rolling-memory status` 查看金库与运行模式。

表结构与索引更新发生在升级后的版本第一次打开金库时，而不是替换文件时。升级版本会就地幂等应用更新，已有记忆仍留在同一个金库文件中。金库较大时可能需要几分钟，请不要中断。

重要金库升级前建议另存一份：停止守护进程，用 SQLite `.backup` 生成副本，并保留被引用的会话记录与挂载目录。快照与金库含同样的私密内容，需要同等保护。

升级未能完成时，不要删除数据库；请用当前版本重试，或恢复升级前的副本。不要用旧版本继续打开这个金库：旧版本会拒绝打开由新版结构写入的金库。


