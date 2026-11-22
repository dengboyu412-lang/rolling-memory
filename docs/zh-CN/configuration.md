# 配置参考

**[简体中文](configuration.md) · [English](../configuration.md)**

Rolling Memory 只有一份可手工编辑的配置文件，外加少量环境变量覆盖。本页把普通用户最常调整的配置收拢在一处——存储路径、召回交付窗口、后台引擎内存上限、控制台端口与便携模式——并给出后台控制台 **系统管理 → 记忆服务** 页里的等价操作。

---

## 1. 配置文件位置

| 项目 | 默认位置 | 说明 |
|---|---|---|
| 配置文件 | `~/.config/rolling-memory/config.toml` | TOML；首次运行时生成带注释的默认值（权限 `0600`） |
| 金库数据库 | `~/.local/share/rolling-memory/memory.db` | 本地明文 SQLite 金库 |
| 日志文件 | `~/.local/state/rolling-memory/rolling-memory.log` | |
| 引擎 socket | `~/.local/state/rolling-memory/engine.sock` | 后台引擎守护进程的本地 Unix socket |

便携（U 盘）形态会自动使用相对路径与 `usb` 存储档，见 [USB 便携模式](usb-portable.md)。

**优先级：** 命令行参数 → 环境变量 → `config.toml` → 内置默认值。

查看文件位置与解析结果：

```bash
rmem config path          # 打印当前配置文件路径
rmem config show          # 以 JSON 输出解析后的配置
rmem config set <section.key> <value>
```

---

## 2. 常用配置

### 2.1 存储与身份 —— `[core]`

| 键 | 默认 | 含义 |
|---|---|---|
| `core.db` | `~/.local/share/rolling-memory/memory.db` | 金库数据库路径 |
| `core.scope` | `default` | 记忆分区；隔离评测使用独立分区 |
| `core.reader` | `deliverable` | 默认受众视图（`owner` / `deliverable`） |
| `core.log` | `~/.local/state/rolling-memory/rolling-memory.log` | 日志文件路径 |

### 2.2 召回交付 —— `[recall]`

| 键 | 默认 | 含义 |
|---|---|---|
| `recall.delivery_window` | `auto`（解析为 `0-16`） | 智能交付窗口：`auto`、`0-16`、`0-32`、`0-64`；窗口越大，单次召回给出的证据越多 |
| `recall.chars` | `16000` | 单次召回的字符预算 |
| `recall.budget_ms` | `500` | 延迟预算参考值 |

交付窗口也可以在「记忆总览」页页眉直接切换。

### 2.3 后台引擎 —— `[daemon]`

| 键 | 默认 | 含义 |
|---|---|---|
| `daemon.memory_limit` | `auto` | 后台引擎守护进程的堆内存软上限 |
| `daemon.snapshot_interval` | `60s` | 后台已发布代的固定节拍（`10s–1h`） |

可选值：

| 值 | 行为 |
|---|---|
| `auto` | 取物理内存的 `1/8`，钳位在 `1GiB–4GiB`（16 GB 机器解析为 2 GiB） |
| `off` | 不设显式上限 |
| 明确尺寸 | `B` / `KiB` / `MiB` / `GiB` / `TiB`，如 `512MiB`、`2GiB`；可接受范围 `64MiB–1TiB` |

- 上限在引擎守护进程启动时生效；保存新值后需重启守护进程（控制台按钮或 `rmem daemon restart`）。
- `rmem daemon status --json` 会报告生效上限及其来源（`auto` / `explicit` / `env` / `off` / `unavailable`）。
- 若设置了 `GOMEMLIMIT` 环境变量，它优先于配置文件（Go 运行时标准语义）。
- 物理内存探测失败时不设上限——守护进程保持运行时默认值，而不是猜一个数。

**已发布代节拍。** 后台构建器按固定节拍发布待发布变更（默认 60 秒）。写入会立即持久化，但在下一代发布后才可检索。日常 harness 工作通常不需要即时可见：当前这一轮——刚读的文件、刚跑的命令、刚交换的消息——本来就在模型上下文中，记忆检索服务的是已经掉出上下文的旧材料。因此，短暂的发布延迟是为长会话与大导入期间的稳定召回做的刻意取舍。需要更快可见时设 `snapshot_interval = "30s"`；想减少后台工作量可设更大的值，如 `"5m"`。第一代立即构建；构建失败后按两分钟退避重试。该值接受 Go 时长（`60s`、`5m`）或纯秒数（`60`）。

### 2.4 存储档位 —— `[storage]`

| 键 | 默认 | 含义 |
|---|---|---|
| `storage.profile` | `local` | 常规安装为 `local`；便携形态自动选择 `usb` |

### 2.5 更新通道 —— `[update]`

| 配置项 | 默认值 | 含义 |
|---|---|---|
| `update.channel` | `stable` | `stable` 只接受正式发布标签（默认）；`beta` 同时接受预发布标签；`dev` 跟踪 `main` 最新提交，适合源码构建 |
| `update.source` | `github` | `github` 使用 GitHub Releases 官方渠道；`gitee` 使用 Gitee 镜像，适合访问 GitHub 较慢的网络 |
| `update.auto` | `notify` | `off` 不做后台检查；`notify` 每六小时检查并通知；`apply` 自动下载、校验并安装 |

`dev` 只报告 `main@<commit>`，更新需要源码重建，不会自动替换二进制。可用 `rmem config set update.channel beta` 保存偏好，也可在控制台「版本与更新」弹窗切换，或用 `rmem update --check --channel dev` 单次覆盖。预编译安装由安装器写入 `stable`，源码安装写入 `dev`。

`update.auto` 可用 `rmem config set update.auto apply` 保存，也可在控制台「版本与更新 → 后台更新」选择。预编译安装默认 `apply`，源码安装默认 `notify`。手动执行一次同样的策略：`rmem update --auto`。

更新源可用 `rmem config set update.source gitee` 保存，也可在控制台「版本与更新」弹窗选择。显式的 `--release-base` URL 或 `ROLLING_MEMORY_RELEASE_BASE` 仍只对本次运行生效并优先。Gitee 镜像尚未附带某个版本的预编译包时，检查结果会标记为“仅源码更新”，不会给出必然失败的下载按钮。

---

## 3. 环境变量（常用子集）

每个 `config.toml` 键都有对应环境变量，且优先于文件；命令行参数优先于两者。

| 变量 | 覆盖 |
|---|---|
| `ROLLING_MEMORY_CONFIG` | 配置文件路径本身 |
| `ROLLING_MEMORY_DB` | `core.db` |
| `ROLLING_MEMORY_LOG` | `core.log` |
| `ROLLING_MEMORY_SCOPE` | `core.scope` |
| `ROLLING_MEMORY_READER` | `core.reader` |
| `ROLLING_MEMORY_DELIVERY_WINDOW` | `recall.delivery_window` |
| `ROLLING_MEMORY_UPDATE_CHANNEL` | `update.channel` |
| `ROLLING_MEMORY_UPDATE_SOURCE` | `update.source` |
| `ROLLING_MEMORY_UPDATE_AUTO` | `update.auto` |
| `ROLLING_MEMORY_DAEMON_MEMORY_LIMIT` | `daemon.memory_limit` |
| `ROLLING_MEMORY_SNAPSHOT_INTERVAL` | `daemon.snapshot_interval` |
| `ROLLING_MEMORY_DAEMON_SOCKET` | 引擎 socket 路径 |
| `ROLLING_MEMORY_PORTABLE` | 强制便携模式并指定载荷根目录 |
| `GOMEMLIMIT` | Go 运行时软内存上限（设置后优先于 `daemon.memory_limit`） |
| `RM_PANEL_API_PORT` / `RM_PANEL_WEB_PORT` | 控制台后端 / 前端端口（默认 `9006` / `8008`） |

---

## 4. 后台控制台的等价操作

打开控制台，在侧栏选择 **系统管理 → 记忆服务**：

| 卡片 | 功能 | 命令行等价 |
|---|---|---|
| 引擎守护进程 | 状态（进程号、运行时长、堆内存、生效上限及来源）；启动 / 停止 / 重启 | `rmem daemon status\|start\|stop\|restart` |
| 内存上限 | 读写 `daemon.memory_limit`；保存后提示重启生效 | `rmem config set daemon.memory_limit 2GiB` |
| 缓存 | 丢弃内存索引快照与读缓存（可再生成，记忆原文不动） | `rmem engine invalidate` |
| 磁盘维护 | 只读扫描、备份与缓存清理、金库瘦身 | `rmem maintain status\|clean\|compact` |

磁盘维护默认保留引擎的磁盘索引缓存：缓存缺失时下一次检索要重建整个索引（分钟级耗时、数 GB 内存峰值），因此释放这部分空间需显式开启：`rmem maintain clean --purge-engine-cache --apply --yes`。

备份清理覆盖金库数据库备份、`~/.local/bin` 下的启动器备份，以及控制台旧的 `web-dist` 目录，按族保留最新副本。状态目录下的评测历史（`eval/`、`atmbench/`）只报告占用，永不自动删除；移除实验证据是单独的用户决定。

停止守护进程不会阻塞任何操作：下一次检索或后台同步会自动把它拉起。

---

## 5. 验证一次改动

```bash
rmem config show            # 解析后的完整配置，含 daemon.memory_limit
rmem daemon status --json   # 在线守护进程：进程号、运行时长、堆内存、生效内存上限与来源
rmem doctor                 # 环境与完整性体检
```

值不合法时写入失败，报错会列出可接受值，原文件保持不变。

---

## 6. 相关文档

- [MCP 契约](mcp.md) —— 每次调用的参数（含 `delivery_window` 覆盖）
- [多宿主接入指南](harness.md) —— 为各编码助手接好 Rolling Memory
- [USB 便携模式](usb-portable.md) —— 相对路径与 `usb` 档
- [Web 控制台指南](plugin-console.md) —— 控制台其余频道
- [隐私与本地数据](privacy.md) —— 哪些数据始终留在本机
