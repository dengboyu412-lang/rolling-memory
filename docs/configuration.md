# Configuration Reference

**[English](configuration.md) · [简体中文](zh-CN/configuration.md)**

Rolling Memory keeps one human-editable configuration file and a small set of environment overrides. This page collects the settings most users touch — storage paths, the recall delivery window, the background engine's memory cap, console ports, and portable mode — and shows the equivalent controls in the web console's **System → Memory Service** page.

---

## 1. Where configuration lives

| Item | Default location | Notes |
|---|---|---|
| Configuration file | `~/.config/rolling-memory/config.toml` | TOML; created on first run with commented defaults (permissions `0600`) |
| Vault database | `~/.local/share/rolling-memory/memory.db` | Local plaintext SQLite vault |
| Log file | `~/.local/state/rolling-memory/rolling-memory.log` | |
| Engine socket | `~/.local/state/rolling-memory/engine.sock` | Local Unix socket for the background engine daemon |

Portable (USB) payloads automatically use relative paths and the `usb` storage profile; see [USB Portable Mode](usb-portable.md).

**Precedence:** command-line flags → environment variables → `config.toml` → built-in defaults.

Inspect the file and the resolved values:

```bash
rmem config path          # print the active config file path
rmem config show          # dump resolved configuration as JSON
rmem config set <section.key> <value>
```

---

## 2. Common settings

### 2.1 Storage and identity — `[core]`

| Key | Default | Meaning |
|---|---|---|
| `core.db` | `~/.local/share/rolling-memory/memory.db` | Vault database path |
| `core.scope` | `default` | Memory scope; isolated evaluation runs use their own scope |
| `core.reader` | `deliverable` | Default audience view (`owner` / `deliverable`) |
| `core.log` | `~/.local/state/rolling-memory/rolling-memory.log` | Log file path |

### 2.2 Recall delivery — `[recall]`

| Key | Default | Meaning |
|---|---|---|
| `recall.delivery_window` | `auto` (resolves to `0-16`) | Adaptive delivery window: `auto`, `0-16`, `0-32`, or `0-64`. Larger windows give more evidence in one recall |
| `recall.chars` | `16000` | Character budget per recall |
| `recall.budget_ms` | `500` | Latency budget hint |

The delivery window is also switchable from the console header of the overview page.

### 2.3 Background engine — `[daemon]`

| Key | Default | Meaning |
|---|---|---|
| `daemon.memory_limit` | `auto` | Soft heap limit for the background engine daemon |
| `daemon.snapshot_interval` | `60s` | Fixed cadence of the background published generation (`10s–1h`) |

Accepted values:

| Value | Behaviour |
|---|---|
| `auto` | `1/8` of physical RAM, clamped to `1GiB–4GiB` (a 16 GB machine resolves to 2 GiB) |
| `off` | No explicit cap |
| An explicit size | `B` / `KiB` / `MiB` / `GiB` / `TiB`, e.g. `512MiB`, `2GiB`; accepted range `64MiB–1TiB` |

- The cap takes effect when the engine daemon starts; after saving a new value, restart the daemon (console button, or `rmem daemon restart`).
- `rmem daemon status --json` reports the effective cap and where it came from (`auto` / `explicit` / `env` / `off` / `unavailable`).
- If the `GOMEMLIMIT` environment variable is set, it wins over the configuration file (standard Go runtime semantics).
- If the physical-memory probe fails, no cap is applied — the daemon keeps the runtime default rather than guessing.

**Published-generation cadence.** The background builder publishes pending changes on a fixed cadence (default 60 seconds). A write is durable immediately but becomes searchable after the next publish. Daily harness work rarely needs immediate visibility: the current turn — the file just read, the command just run, the message just exchanged — is already in the model's context, and memory retrieval is for older material that fell out of context. A short publish delay is therefore a deliberate trade for stable recall during long sessions and large imports. Set `snapshot_interval = "30s"` for faster visibility, or a larger value such as `"5m"` to reduce background work. The first generation is built immediately; a failed build retries after a two-minute backoff. The value accepts Go durations (`60s`, `5m`) or plain seconds (`60`).

### 2.4 Storage profile — `[storage]`

| Key | Default | Meaning |
|---|---|---|
| `storage.profile` | `local` | `local` for normal installs; `usb` is selected automatically for portable payloads |

### 2.5 Update channel — `[update]`

| Key | Default | Meaning |
|---|---|---|
| `update.channel` | `stable` | `stable` accepts published release tags (default); `beta` also accepts prerelease tags; `dev` tracks the latest `main` commit for source builds |
| `update.source` | `github` | `github` uses the official GitHub Releases channel; `gitee` uses the Gitee mirror for networks that reach Gitee faster |
| `update.auto` | `notify` | `off` disables background checks; `notify` checks every six hours and notifies; `apply` downloads, verifies and installs automatically |

`dev` reports `main@<commit>` and requires a source rebuild; it never replaces a binary automatically. Save the preference with `rmem config set update.channel beta`, switch it in the console's Version & Update dialog, or override one check with `rmem update --check --channel dev`. The installer writes `stable` for published installs and `dev` for source builds.

`update.auto` is saved with `rmem config set update.auto apply` or selected in **Version & Update → Background Updates**. Published installs default to `apply`; source installs default to `notify`. Run the same policy manually with `rmem update --auto`.

The source is saved with `rmem config set update.source gitee` and can be selected in the console's Version & Update dialog. An explicit `--release-base` URL or `ROLLING_MEMORY_RELEASE_BASE` still wins for one run. When the Gitee mirror has no prebuilt artifacts for a tag, the check reports a source-only update instead of offering a download that cannot succeed.

---

## 3. Environment variables (common subset)

Every `config.toml` key has an environment counterpart that wins over the file; flags win over both.

| Variable | Overrides |
|---|---|
| `ROLLING_MEMORY_CONFIG` | Configuration file path itself |
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
| `ROLLING_MEMORY_DAEMON_SOCKET` | Engine socket path |
| `ROLLING_MEMORY_PORTABLE` | Force portable mode with an explicit payload root |
| `GOMEMLIMIT` | Go runtime soft memory limit (wins over `daemon.memory_limit` when set) |
| `RM_PANEL_API_PORT` / `RM_PANEL_WEB_PORT` | Console backend / web ports (defaults `9006` / `8008`) |

---

## 4. Equivalent controls in the web console

Open the console and choose **System → Memory Service** in the sidebar:

| Card | Does | CLI equivalent |
|---|---|---|
| Engine Daemon | Status (PID, uptime, heap, effective cap and its source); start / stop / restart | `rmem daemon status\|start\|stop\|restart` |
| Memory Cap | Read and write `daemon.memory_limit`; prompts for a restart to apply | `rmem config set daemon.memory_limit 2GiB` |
| Cache | Drop in-memory index snapshots and read caches (regenerable; stored memory is untouched) | `rmem engine invalidate` |
| Disk Maintenance | Read-only scan, backup/cache cleanup, vault compaction | `rmem maintain status\|clean\|compact` |

Disk maintenance keeps the engine's on-disk index cache by default. A missing cache makes the next recall rebuild the whole index (minutes of work and a multi-GB memory peak), so freeing that space is an explicit opt-in: `rmem maintain clean --purge-engine-cache --apply --yes`.

Backup cleanup covers vault database backups, launcher backups in `~/.local/bin`, and the console's previous `web-dist` directories, keeping the newest copies per family. Evaluation history under the state directory (`eval/`, `atmbench/`) is reported with its size but never auto-deleted; removing experiment evidence is a separate user decision.

Stopping the daemon blocks nothing: the next recall or background sync starts it again automatically.

---

## 5. Verifying a change

```bash
rmem config show            # resolved configuration, including daemon.memory_limit
rmem daemon status --json   # live daemon: pid, uptime, heap, effective memory cap and source
rmem doctor                 # environment and integrity diagnostics
```

If a value is rejected, the write fails with a message naming the accepted values; the file is left unchanged.

---

## 6. Related documents

- [MCP contract](mcp.md) — per-call parameters (including the `delivery_window` override)
- [Multi-harness guide](harness.md) — wiring Rolling Memory into each coding agent
- [USB portable mode](usb-portable.md) — relative paths and the `usb` profile
- [Web console guide](plugin-console.md) — the rest of the console
- [Privacy and local data](privacy.md) — what stays on your machine
