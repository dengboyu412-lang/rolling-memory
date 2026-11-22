# Multi-Harness Integration Guide

**[English](harness.md) · [简体中文](zh-CN/harness.md)**

> This document describes the multi-harness architecture, registration matrix, launcher shims, and verification gates of `rolling-memory install`.

## 1. Design Goal

Rolling Memory's mechanical core is completely harness-agnostic. Coding agent harnesses differ in only four integration surfaces: **Skill Root**, **MCP Server Registration File**, **Lifecycle Hooks**, and **Instruction Files (`AGENTS.md` / `CLAUDE.md`)**.

`rolling-memory install` brings these surfaces into a single registry, with backups and idempotent updates. `install --check` inspects registrations and separately probes the selected launcher.

---

## 2. Integration Approaches Compared

| Approach | Typical Implementation | Operational Trade-Offs |
|---|---|---|
| **MCP-Only + Manual Config Copy-Paste** | Ships a stdio MCP server; users manually edit each client's JSON or TOML file | Users shoulder path differences, escaping rules, and cropped GUI `PATH` issues; no backups or conflict protection |
| **Single-Harness Exclusive Plugin** | Hard-codes one IDE's extension API and proprietary lifecycle hooks | Memories and workflows are siloed within one tool and cannot be shared across different coding agents |
| **`install` Registry and Native Plugins (Rolling Memory)** | Stdio MCP (single `recall` tool), multi-harness configuration, and a separate launcher probe | Configures detected harnesses; review conflicts and verify host loading in a session |

---

## 3. Layered Contract

```text
Harness Agent + Skill        ← Agent follows SKILL.md & instructions to call recall
        │  MCP tools/call
rolling-memory install       ← Harness adapter: registry + conservative writers (JSON / TOML / YAML) + check
        │  writes launcher shim & registrations
MCP stdio · CLI · Hooks      ← Mechanical core: zero harness coupling, standard process protocols
        │
SQLite (WAL)                 ← Storage layer: local plaintext vault
```

- **Zero Harness Coupling in the Core (`facade` / `mcp` / `cli` / `hooks`)**: Communicates strictly over standard process pipes (newline-delimited JSON-RPC 2.0 for MCP, argv/stdin for CLI, JSON stdin → JSON stdout for Hooks).
- **Conservative Wiring in the Adapter (`internal/harness`)**: Writes launcher commands into harness configs, copies `SKILL.md`, and optionally attaches hooks and instruction blocks without touching memory semantics.
- **Reminder Hooks and Agent-Driven Retrieval**: The optional per-prompt hook (`--with-hooks`) does not automatically retrieve memory. The agent decides when to call `recall`. Hooks and background scanning can import supported local sources; they do not guarantee that every conversation is captured.
- **Turn-End Recall Review & Customizing `SKILL.md`**: At the very bottom of `SKILL.md` (`§5 Wrap-up Review`), a standalone optional section instructs the agent to append a brief wrap-up note whenever `recall` was called in that turn — reporting how many times `recall` was called, estimated round-trips saved with concrete basis, and whether the recalled memory actually helped. You can edit or delete that bottom section in `<skill_root>/rolling-memory/SKILL.md` directly (or pass `--skill-file <path>` to `rolling-memory install`) at any time.

---

## 4. Supported Harness Matrix

### 4.1 Harness Registration Locations

| Harness | User Skill Root | MCP Registration Location | Lifecycle Hooks | Instruction Files (User / Project) |
|---|---|---|---|---|
| **ZCode** | `~/.zcode/skills/` | `~/.zcode/cli/config.json` → `mcp.servers` | ✅ `hooks.events` (`hooks.enabled=true`) | `~/.zcode/AGENTS.md` / `AGENTS.md` |
| **Claude Code** | `~/.claude/skills/` | `~/.claude.json` → `mcpServers` (project `.mcp.json`) | ✅ `~/.claude/settings.json` → `hooks` | `~/.claude/CLAUDE.md` / `CLAUDE.md` |
| **Codex** | `~/.codex/skills/` | `~/.codex/config.toml` → `[mcp_servers.<name>]` | ✅ `~/.codex/hooks.json` (optional, `--with-hooks`; first run needs review/trust inside Codex) | `~/.codex/AGENTS.md` / `AGENTS.md` |
| **OpenCode** | `~/.config/opencode/skills/` | `~/.config/opencode/opencode.json(c)` → `mcp.servers` (project `.opencode/opencode.json`) | — (Skill-driven) | — / `AGENTS.md` |
| **DeepSeek Harness (`dsh`)** | `~/.dsh/skills/` (`$DSH_HOME`) | `~/.dsh/profiles/<profile>/cordis.patch.yml` → `dsh-mcp-client` | — (Skill-driven) | `~/.dsh/AGENTS.md` / `AGENTS.md` |
| **CodeBuddy** | `~/.codebuddy/skills/` | `~/.codebuddy/mcp.json` → `mcpServers` | — (Skill-driven) | — / `CODEBUDDY.md` |

### 4.2 Other Harnesses & Native Plugins

- **Other Targets**: The installer also detects **Cursor**, **Windsurf**, **Cline / Roo Code**, **WorkBuddy**, and **Doubao**. To select a specific target, pass its harness ID (`rolling-memory install <id>`). Check the installation output and your host's current settings location: host versions can use different paths. WorkBuddy's plugin includes a skill, an MCP registration, and a prompt hook, but requires a separately installed local CLI. If the host does not provide the plugin root, the MCP launcher uses that installed CLI; without it the server cannot start and does not download one automatically. Doubao's MCP connector requires manual setup (see §4.4).
- **Native Plugins**: When an enabled plugin declares the same skill or MCP server, `install` never creates a second copy beside it. An existing copy written by this tool is reported as a duplicate; run `rolling-memory install <id> --dedupe` to remove only this tool's owned copy. Review the host configuration and plugin state before resolving duplicates; a plugin declaration does not confirm that the host loaded it.

### 4.3 Three Safe Config Writers

| Format | Target Harnesses | Write & Verification Strategy |
|---|---|---|
| **JSON Deep Merge** | ZCode / Claude Code / CodeBuddy / OpenCode / Cursor / Windsurf / Cline / WorkBuddy | Parses existing JSON, updates only the target server/hook entry, and preserves all other keys and ordering |
| **TOML Section Surgery** | Codex | Replaces or appends `[mcp_servers.<name>]` (and `.env` subtable) while keeping comments intact; re-parses with `go-toml` before writing |
| **YAML Marker Block** | `dsh` | Appends or updates a `# >>> rolling-memory (<name>) >>>` block; uninstall removes only the marked block |

### 4.4 Doubao Onboarding: From Zero to Working

For first-time users wiring this tool into the Doubao desktop app: **the skill plane is one command**; the MCP tool plane needs one step inside the app.

**Step 0 · Prerequisites**: Doubao desktop installed and launched at least once (first launch creates the user workspace `~/Doubao`); this tool installed (`rolling-memory --version` prints a version).

**Step 1 · Write skills** (dual roots, i.e. the client-reported `local_skill_paths`):

```bash
rolling-memory install doubao            # writes both skill roots below
rolling-memory install doubao --check    # verify: two "skill ok" lines
```

| Skill root | Purpose |
|---|---|
| `~/Doubao/skills/<name>/SKILL.md` | Doubao user workspace (sibling of `chats/`), created on first launch |
| `~/.agents/skills/<name>/SKILL.md` | Cross-agent convention root; same-name skills here take precedence over additional roots |

**Step 2 · Let Doubao discover it**: Restart Doubao or open a new session to trigger the local skill scan (background `background_sync`). The session skill list should show `rolling-memory`; scanned USER skills are packaged and uploaded to your Doubao account for cross-device sync — **no developer account and no marketplace review required**.

**Step 3 · Connect the MCP tool plane (address-style)**: Doubao's connector accepts **address-style** MCP (`mcp_transport`: Streamable HTTP / SSE) but rejects arbitrary local command paths (`mcp_command_path_policy` rejects wrapper scripts with `STDIO_COMMAND_ABSOLUTE_NOT_ALLOWED`). Start a local URL service and paste the address into Doubao:

```bash
rolling-memory mcp --http                                    # default http://127.0.0.1:8765/mcp (SSE endpoint: /sse)
rolling-memory mcp --http --addr 127.0.0.1:9000 --path /mcp  # custom port / path
```

In the Doubao connector form choose **Streamable HTTP** and enter `http://127.0.0.1:8765/mcp` (if only SSE is offered, enter `http://127.0.0.1:8765/sse`). Save, open a new session, and the tool list should show `recall`. Connecting the tool does not automatically import Doubao conversations; see the collection limits below.

- The service binds loopback only (default `127.0.0.1`), caps request bodies at 8 MiB, and rejects cross-origin requests; keep it running, preferably under a system service manager.
- The MCP line from `install doubao` still reports `skipped` — that is truthful: registration lives on the Doubao side (encrypted credential storage + manifest projection) and cannot be written from the CLI, so you paste the address in the app.

**Troubleshooting**: If the skill does not appear, first confirm `SKILL.md` exists under both skill roots; then inspect the Doubao runtime log `~/Library/Application Support/Doubao/sdk_storage/log/saman_<date>.log` for `local_skill_paths` (client-reported roots), `scan_local_skills` (scan results and `item_count`), and `[MCP_SERVICE] get all tools … connector_count=` (MCP loading). If the address connector does not work, self-test the local service first: `curl -s http://127.0.0.1:8765/mcp -H 'content-type: application/json' -d '{"jsonrpc":"2.0","id":1,"method":"tools/list"}'`.

**Uninstall**: `rolling-memory install doubao --uninstall` (removes only files written by this tool; the Doubao account copy must be removed in the app).

### 4.5 Conversation Collection and Manual Import

Installing an MCP server or skill is separate from collecting conversation history. The background scanner reads the local session databases of ZCode and OpenCode, plus selected transcript locations for ZCode, Claude Code, Codex, DSH, CodeBuddy, and WorkBuddy. OpenCode's store is read as SQLite: current versions keep sessions in `session_v2` and messages in `session_message`, older versions expose only the `session` inventory. A turn is imported only when its final assistant step completed without an error and without a tool call; assistant steps that invoke tools are working narration and do not become memory. It supports Codex `.jsonl.zst` archives and DSH compressed `.jsonl.zstd` sessions in the observed v4 format: user messages must have `source.kind: user`, and a turn is imported only when its matching `turn/start` and `turn/end` events establish successful completion (`reason.kind: completed`). Unknown formats and turns without confirmed successful completion are not imported; subagent coverage is not guaranteed. CodeBuddy and WorkBuddy's configured JSONL locations may not match a host's actual store. Cursor, Windsurf, and Cline have no automatic conversation scanner. Doubao does not read the app's encrypted live store; local JSONL conversation exports placed in `~/Doubao/exports/` (one file per conversation) are imported with the same completion rules. Hooks, where installed and supported, do not make these sources complete.

For other sources, export or prepare a supported JSONL transcript and import it explicitly with `rolling-memory import /path/to/transcript`. File imports accept only `shared` or `public` audiences; restricted or unknown audiences are rejected because automatic scanning cannot enforce file-level access controls. Keep sensitive files outside scanned directories. Existing copies are not removed automatically. Review the result; import and background scanning do not guarantee complete or lossless capture. See [mcp.md](mcp.md) for the tool contract. Retrieval and storage run locally in `local_plain`; installing or updating software is a separate action that may require network access.

---

## 5. Common Commands

```bash
# 1. Inspect detected harnesses and target registration paths
rolling-memory install --list

# 2. Preview planned changes without modifying any files
rolling-memory install --dry-run

# 3. Wire all detected harnesses (skills + MCP + lifecycle hooks + instructions)
rolling-memory install --with-hooks --with-instructions

# 4. Project-scoped installation (defaults to ./.rolling-memory/memory.db and scope project:<dir>)
cd /path/to/project && rolling-memory install --project zcode claude-code

# 5. Coexist with another instance using a custom server/skill name
rolling-memory install --name rmem --skill-name rmem

# 6. Wire specific harnesses or a specific dsh profile
rolling-memory install zcode dsh --profile web

# OpenCode: install the Skill and local MCP server in the user config
rolling-memory install opencode
rolling-memory install opencode --check

# 7. Verify static registrations + run a live MCP initialize / tools/list handshake
rolling-memory install --check

# 8. Safely uninstall all entries written by Rolling Memory (preserves foreign entries)
rolling-memory install --uninstall

# 9. Remove this tool's duplicate copy when a native plugin provides the same surface
rolling-memory install zcode --dedupe
```

---

## 6. Four-Tier Verification Gate

`rolling-memory install --check` automatically verifies Tiers 1–3; Tier 4 is observed in a new agent session:

| Tier | Check | Verification Method |
|---|---|---|
| **Tier 1: Skill Current** | `SKILL.md` exists under the harness skill root, carries valid frontmatter, and matches the current release; an older owned copy reports `outdated`, a user-modified copy reports `conflict` | `rolling-memory install --check` |
| **Tier 2: MCP Registered** | Valid `rolling-memory` server entry exists in the harness configuration file; the launcher is present and current | `rolling-memory install --check` |
| **Tier 3: Live MCP Handshake** | Spawns the launcher, sends `initialize` and `tools/list`, and confirms the single `recall` tool | `install --check` (`server ok · rolling-memory ... · tools: recall`) |
| **Tier 4: Session Behavior** | Check whether the agent calls `recall` and whether the intended source was imported | Ask a history question in a new session and inspect the local records; source coverage is limited as described in §4.5 |

---

## 7. Launcher Shim & Portable USB Host Anchor

Because GUI IDEs frequently spawn MCP servers and hooks with a cropped `PATH`, `install` generates an absolute-path launcher shim:

```sh
# ~/.local/share/rolling-memory/bin/rolling-memory.sh (0755)
#!/bin/sh
# Rolling-Memory launcher (generated by `rolling-memory install`).
# Absolute path on purpose: harnesses often spawn MCP servers and hooks with a cropped PATH.
exec "<absolute>/bin/rolling-memory" "$@"
```

- **Self-Contained Execution**: The Go binary embeds SQLite and all runtime logic; the shim performs a single `exec`.
- **Windows Support**: Generates `%LOCALAPPDATA%\rolling-memory\bin\rolling-memory.cmd` (`@echo off` + absolute path + `%*`).
- **Portable USB Host Anchor**: When `install` is executed from a `.portable` USB payload, the generated launcher automatically upgrades to a **mount-drift-healing Host Anchor** — executing the original path when mounted at the same location, or scanning removable-media mount roots (`/Volumes/*`, `/media/*`, `/run/media/*`, `/mnt/*`, and Windows `D:..Z:`) when mount names or drive letters shift (see [usb-portable.md](usb-portable.md)).
- **Ownership Guard**: Shims carry the exact generated header comment. A later `install` adopts its own launcher even when the absolute binary path changed, backing up the previous file first; foreign scripts still report `conflict`.

---

## 8. Backups, Idempotency & Rollback

- **Automatic Pre-Write Backups**: Every modified configuration file is backed up to `<file>.rolling-memory.<YYYYmmddHHMMSS>.bak` before writing.
- **Strict Idempotency**: Re-running `install` when nothing changed reports `unchanged` without creating redundant backups.
- **Conflict Protection**: Existing entries not owned by this instance report `conflict` unless `--force` is explicitly supplied. A native-plugin duplicate is resolved only by `--dedupe`, which removes this tool's owned copy; `--force` never creates a second copy beside an active plugin.
- **One-Command Rollback**: Restore any `.bak` file or run `rolling-memory install --uninstall` to remove all registered entries cleanly.

---

## 9. Online Self-Update & Vault Safety (`rolling-memory update`)

> For the full installation, channel policy, breaking-change and rollback guide, see [install-upgrade.md](install-upgrade.md).

End users can keep the CLI binary, the local web console, and all wired harness integrations up to date without `git` or a Go toolchain via `rolling-memory update`:

```bash
# 1. Compare local SHA-256 digests against the release channel's SHA256SUMS (read-only)
rolling-memory update --check

# 2. Update the CLI binary, refresh wired harnesses, and update the console if installed
rolling-memory update

# 3. Also download/install or update the local web console (rolling-admin + web-dist.tar.gz)
rolling-memory update --with-console

# 4. Use a custom release mirror URL (or set ROLLING_MEMORY_RELEASE_BASE)
rolling-memory update --release-base https://mirror.example.com/rolling-memory/releases/latest/download

# 5. Machine-readable JSON output for scripts or admin tooling
rolling-memory update --check --json

# 6. Resolve only the newest tag (no artifact download or digest comparison)
rolling-memory update --check --tags-only

# 7. Optional user-level periodic check: cache refresh + desktop notification only
rolling-memory update --schedule install
rolling-memory update --schedule status
rolling-memory update --schedule remove
```

- **Version Check vs Installability**: `update --check` reports `update_available` from the newest tag and `installable` from the release channel's `SHA256SUMS`. A tag without attached binaries is a valid new version with `installable=false`; the console then explains the source-update path instead of offering a broken download.
- **Update Source**: `update.source` selects `github` (official GitHub Releases, default) or `gitee` (the Gitee mirror for networks that reach Gitee faster). Choose it in the console's Version & Update dialog, save it with `rmem config set update.source gitee`, or override one run with `rmem update --source gitee`. An explicit `--release-base` URL or `ROLLING_MEMORY_RELEASE_BASE` still wins. Gitee tags without attached binaries are reported as source-only updates, with a source rebuild hint that points at the Gitee repository.
- **Background Version Awareness (Low-Frequency Checks + Live Push)**: entering the dashboard checks immediately; while the console stays open the cache refreshes every ten minutes, and results are pushed over the same local real-time connection. The frontend does not poll at high frequency. CLI `status` and `doctor` read the same cache without a network request. An MCP session refreshes a stale cache in the background, so users who only work through an agent see the new version on the next console visit or status call.
- **Long Absence**: the cache keeps the last result (six hours fresh for CLI / MCP, ten-minute refresh while the console is open, thirty minutes after a failure). When the console, an MCP session, or a CLI command runs again after a longer gap, the cache is refreshed automatically; the console's first frame already carries the last known result and updates in place when the refresh completes. If no Rolling Memory process runs at all, no client can discover an update; install the built-in periodic job with `rolling-memory update --schedule install` (or pair `update --auto` with your own scheduler) so the saved policy keeps running while the product is idle.
- **Source Installations**: if the installation is source-based or a local build, the console marks the release as source-only when the channel has no matching binary and shows the tag-based update command instead of enabling the one-click updater. Prebuilt installations keep the existing SHA-256 verified in-place update.
- **Update Channels**: `stable` (default for published installs) accepts published non-prerelease tags only; `beta` also accepts prerelease tags; `dev` (written explicitly by source installs) tracks the latest commit on `main`. Set the preference with `rolling-memory config set update.channel beta` (or `ROLLING_MEMORY_UPDATE_CHANNEL=beta`), switch it in the console's Version & Update dialog, or override one check with `rolling-memory update --check --channel dev`. The scheduled check follows the saved preference. `dev` never replaces a binary automatically: it reports `main@<commit>` and requires a source rebuild (`ROLLING_MEMORY_SOURCE_REF=main bash install.sh`).
- **OS-Level Periodic Update**: `rolling-memory update --schedule install` registers a user-level launchd agent (macOS), systemd user timer (Linux), or Windows Task Scheduler entry that runs `update --auto` every six hours. The saved `update.auto` policy decides whether that run only refreshes the cache and notifies (`notify`), installs a verified update (`apply`), or does nothing (`off`). `--schedule status` reports the registration and `--schedule remove` removes it.
- **Source Tag Rebuild**: source installs can rebuild from a specific tag with `ROLLING_MEMORY_SOURCE_REF=v0.0.7 bash install.sh` (or `bash install.sh --ref v0.0.7`). On Windows use `powershell -ExecutionPolicy Bypass -File install.ps1 --ref v0.0.7`. The installer clones that tag, builds the CLI, and records the source provenance; it does not overwrite the vault.
- **Automatic-Check Controls**: set `ROLLING_MEMORY_UPDATE_CHECK=off` to disable background checks, or `ROLLING_MEMORY_UPDATE_API_BASE` to use an enterprise mirror for tag discovery. `update --check` remains available explicitly.
- **Fail-Closed SHA-256 Verification**: Downloaded artifacts are checked against the release channel's `SHA256SUMS` before replacing local files; a digest mismatch aborts the update.
- **Local Daemon Safety**: A shared local daemon can be controlled only after its user, identity, and configuration are verified. When an upgrade leaves an older daemon serving the previous binary, ordinary requests fail closed; `rolling-memory daemon restart` authenticates that same-owner socket by vault path and protocol identity, retires it, and starts the current binary. `install` prints that command when it detects the condition. If a request fails after being sent, it is not automatically retried. The shared daemon assumes a trusted local user and does not protect against arbitrary code running as that same user.
- **Vault Safety Guarantee**: Updating never modifies, truncates, or overwrites your local SQLite memory vault (`~/.local/share/rolling-memory/memory.db`); schema migrations are applied idempotently in place when the database is opened.
- **Zero-Environment Design (No Git / Go / Node.js / Python Required)**:
  - Both `install.sh` and `install.ps1` fetch prebuilt static binaries verified against `SHA256SUMS` (supporting `curl` or `wget` on POSIX, native x64 and ARM64 on Windows, and automatic release-mirror fallback when the primary channel is unreachable).
  - The installer adds `~/.local/bin` (or `%LOCALAPPDATA%\rolling-memory\bin` on Windows) to your user `PATH`, idempotently and inside a marked block: zsh edits `~/.zshrc`, bash covers both its login profile and `~/.bashrc`, fish edits `~/.config/fish/config.fish`, and Windows writes the user-level `PATH` registry value. Set `ROLLING_MEMORY_NO_MODIFY_PATH=1` to opt out; if a profile cannot be written, the installer prints the exact `export PATH=...` line to add by hand.
  - The local Web Console (`rolling-memory panel`) serves its static frontend directly from the Go binary (`--serve-web`), requiring neither Node.js nor Python on the user's machine. Pass `--desktop-shortcut` to `rolling-memory install` to create a double-clickable desktop launcher.

**If `rolling-memory` is missing in a terminal that is already open**: the `PATH` entry only applies to shells started after installation, and the installer prints the same reminder whenever it writes a profile. Copy the line for your shell, then confirm with `rolling-memory --version`:

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
# Windows — reopen the window, or update the current session only
$env:Path = "$env:LOCALAPPDATA\rolling-memory\bin;$env:Path"
rolling-memory --version
```

## 10. Upgrading from an Older Version

Replacing an older build does not require uninstalling it or re-importing memories:

1. **Stop the consumers.** Quit the desktop app and any agent session that is using the memory tool. The updater stops the shared local daemon itself.
2. **Replace the program.** Run `rolling-memory update`, re-run `install.sh` / `install.ps1`, or replace the application bundle. The binary is swapped in place and the host wiring is refreshed; installed harness configuration files are backed up first.
3. **Reopen the host application.** Sessions that were already running keep the old process; new sessions launch the new build.
4. **Verify.** `rolling-memory doctor --json` reports paths and integrity, and `rolling-memory status` reports the vault and mode.

Schema and index updates run the first time the upgraded build opens the vault, not while the files are being replaced. The build applies them idempotently in place, so existing memories stay attached to the same vault file. A large vault can take several minutes; do not interrupt it.

For a vault that matters, keep a copy before upgrading: stop the daemon, run SQLite `.backup`, and preserve the referenced transcripts and mounted directories. A snapshot contains the same private content as the vault and needs the same protection.

If an upgrade cannot finish, do not delete the database; retry with the current build, or restore the pre-upgrade copy. Do not open the vault with an older build: a build refuses a vault written by a newer schema.


