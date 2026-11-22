# Installation & Upgrade Guide

**[English](install-upgrade.md) · [简体中文](zh-CN/install-upgrade.md) · [Documentation Hub](README.md)**

This guide covers installation, update channels, every supported upgrade path, and what to do before a breaking or incompatible upgrade.

---

## 0. Upgrade defaults: published installs follow `stable`

The installer seeds update preferences that match how the binary arrived, so most users never have to choose a channel:

- **published install** → `stable` channel and `apply` background policy. The CLI and console update themselves from verified release artifacts, and the console restarts automatically after the swap.
- **source install** → `dev` channel and `notify` policy. A source build tracks `main` and reports new commits, but never replaces the binary behind the developer's back.

Both choices are saved preferences and can be changed at any time. `beta` is available for testing release candidates. The memory vault (`memory.db`) is never modified by an update; only the CLI binary, console backend, web assets and host integrations are replaced.

---

## 1. Installation methods

### 1.1 Prebuilt installer (recommended for most users)

```bash
# macOS / Linux
curl -fsSL https://github.com/rocareer/rolling-memory/raw/main/install.sh | bash
```

```powershell
# Windows
powershell -ExecutionPolicy Bypass -File install.ps1
```

The installer tries the release channel first and verifies every artifact against `SHA256SUMS`. If the release channel has no verifiable artifact for the platform, it falls back to a source clone and build (requires Git + Go).

Installed files:

- CLI: `~/.local/share/rolling-memory/bin/rolling-memory` (Windows: `%LOCALAPPDATA%\rolling-memory\bin`);
- provenance: `~/.local/share/rolling-memory/install.json` (`method`, `ref`, `commit`, `asset`);
- console: `~/.local/share/rolling-memory/admin/` after `rolling-memory panel` or `update --with-console`.

### 1.2 Source installation from a tag or `main`

```bash
# Latest development main
ROLLING_MEMORY_SOURCE_REF=main bash install.sh
# Or a specific release tag
ROLLING_MEMORY_SOURCE_REF=v0.0.7 bash install.sh
# Equivalent flag
bash install.sh --ref v0.0.7
```

```powershell
powershell -ExecutionPolicy Bypass -File install.ps1 --ref main
```

Source installs require Git and Go. The installer records the source commit in `install.json`, which is what lets the `dev` channel compare `main` HEAD against the installed commit.

### 1.3 Offline / portable

Use the offline package and portable layout described in [usb-portable.md](usb-portable.md). Offline installs keep working without network access; update checks simply report that the channel is unreachable until network is restored.

---

## 2. Update channels

| Channel | Source | Best for |
|---|---|---|
| `stable` | Published non-prerelease tags / releases | Production use; maximum predictability |
| `beta` | Published tags including prereleases | Testing release candidates while keeping SHA-256 verification |
| `dev` | Latest `main` commit | Source builds; early adopters; fastest fixes |

Switch the saved preference:

```bash
rolling-memory config set update.channel stable
rolling-memory config set update.channel beta
rolling-memory config set update.channel dev
```

Or set it for one process:

```bash
export ROLLING_MEMORY_UPDATE_CHANNEL=stable
```

Or switch it in the web console: **Version & Update → Update Channel**.

Override a single check without saving:

```bash
rolling-memory update --check --channel stable
```

Defaults:

- no config file and no environment variable → `stable`;
- existing config files without an `[update]` section → `stable`;
- the installer writes `dev` explicitly for source builds, so developers keep tracking `main`;
- `beta`/`stable` are always available as explicit choices.

### 2.1 What `dev` means in practice

`dev` compares the installed source commit with the current `main` HEAD:

- source/local install with a recorded commit → reports `main@<commit>` and whether it differs;
- release install without a source commit → reports the current `main@<commit>` and asks for **one** source baseline; it does not nag forever and does not pretend a release binary is a source build.

`dev` never downloads or swaps a binary automatically. The console disables "Update Now" and shows the exact rebuild command:

```bash
ROLLING_MEMORY_SOURCE_REF=main bash install.sh
```

### 2.2 What `stable` and `beta` mean in practice

Both channels use published release artifacts and verify them against `SHA256SUMS` before replacement. `beta` additionally accepts prerelease tags. A tag without attached artifacts is reported as a source-only update rather than a broken download.

### 2.3 Update source

`update.source` chooses the repository that supplies release tags and artifacts:

| Source | Use |
|---|---|
| `github` (default) | Official GitHub Releases |
| `gitee` | Gitee mirror for networks that reach Gitee faster than GitHub |

Save it with `rolling-memory config set update.source gitee`, or choose it in **Version & Update → Update Source**. A one-off check can use `--source gitee`. An explicit `--release-base` URL or `ROLLING_MEMORY_RELEASE_BASE` still wins. If Gitee has tags but no attached artifacts yet, the check reports a source-only update and never offers a download that cannot succeed.

### 2.4 Background updates

`update.auto` controls what happens outside the update dialog:

| Policy | Behavior |
|---|---|
| `off` | No background checks. The dialog still checks on demand. |
| `notify` (default) | Checks every six hours and notifies once per new version; installation stays a user action. |
| `apply` | Checks, downloads, verifies against `SHA256SUMS`, installs, and restarts the console automatically. |

Change it with:

```bash
rolling-memory config set update.auto notify
rolling-memory config set update.auto apply
rolling-memory config set update.auto off
```

Or choose it in **Version & Update → Background Updates**. The same policy can be exercised manually with `rolling-memory update --auto`; this is the command the scheduled task runs.

Published installs are seeded with `apply`; source installs are seeded with `notify`, because `dev` updates need a rebuild rather than a binary swap. Background updates are SHA-256 verified, never touch `memory.db`, and only act when the channel actually carries installable artifacts. A version without artifacts is recorded as a source-only update instead of being retried as a download failure.

---

## 3. Upgrade methods

### 3.1 Command line

```bash
# Read-only check
rolling-memory update --check

# Run the saved background policy once (off / notify / apply)
rolling-memory update --auto

# Check the newest tag without downloading artifacts
rolling-memory update --check --tags-only

# Check a specific channel
rolling-memory update --check --channel beta

# Install the update (CLI + console if installed + host wiring)
rolling-memory update

# Also install or update the local web console
rolling-memory update --with-console

# Machine-readable output
rolling-memory update --check --json
```

Updating never modifies, truncates, or overwrites the local SQLite vault (`memory.db`). Use `--source gitee` to read release tags from the Gitee mirror, or pass an explicit `--release-base` URL.

### 3.2 Web console

Open **Version & Update** from the memory overview or the memory service page:

1. the button shows a red dot when a newer version is available;
2. the dialog shows the cached channel, latest version, component digests, and installability;
3. **Check** performs an explicit check;
4. **Update Now** runs the same SHA-256 verified update as the CLI;
5. if the console itself is replaced, it restarts and reloads the page.

Opening the console (entering the dashboard) triggers a version check immediately; while the console stays open it checks every 10 minutes, and results are pushed in place over the local real-time channel. If the console is closed, the next open shows the cached state immediately and refreshes it in the background.

### 3.3 Optional scheduled check

```bash
rolling-memory update --schedule install
rolling-memory update --schedule status
rolling-memory update --schedule remove
```

This registers a user-level launchd agent (macOS), systemd user timer (Linux), or Windows scheduled task. Every six hours it runs `update --auto`, which follows the saved `update.auto` policy: it may only refresh the cache and notify, or it may download and install a verified update. The job also follows the saved `update.channel` and `update.source` preferences.

### 3.4 Re-running the installer

For source installs, re-running the installer is the supported rebuild path:

```bash
ROLLING_MEMORY_SOURCE_REF=main bash install.sh
# or a specific tag
ROLLING_MEMORY_SOURCE_REF=v0.0.7 bash install.sh
```

For prebuilt installs, re-running the installer is useful after the release channel publishes a new artifact or after changing the install directory.

### 3.5 `go install` installations

If the CLI was installed with `go install` rather than the installer:

```bash
go install github.com/rocareer/rolling-memory/cmd/rolling-memory@main
# or a specific tag
go install github.com/rocareer/rolling-memory/cmd/rolling-memory@v0.0.7
```

`install.json` may not exist for this method. When the Go build information carries a source commit, the console uses it as a read-only baseline for the `dev` channel; only when no commit is available does it treat the channel as lacking a source baseline and ask for an explicit rebuild.

---

## 4. Breaking and incompatible upgrades

### 4.1 Before upgrading

- Read `CHANGELOG.md`, especially **Changed**, **Removed**, and migration notes.
- Back up the vault:

```bash
cp ~/.local/share/rolling-memory/memory.db \
   ~/.local/share/rolling-memory/memory.db.bak-$(date +%Y%m%d-%H%M%S)
```

- Keep a copy of the current binary if you may need to roll back:

```bash
cp ~/.local/share/rolling-memory/bin/rolling-memory \
   ~/.local/share/rolling-memory/bin/rolling-memory.previous
```

### 4.2 Schema and data compatibility

- Schema migrations run idempotently when the vault is opened.
- A newer schema cannot be read by an older build; the CLI refuses with `database schema vN is newer than this build; upgrade Rolling-Memory`. Update the binary rather than downgrading the file.
- Downgrading the binary may require restoring the pre-upgrade vault backup if the newer build already migrated the schema.
- Updating never deletes the vault. Background files, episodic history, and crystallized memories remain in the same local file.

### 4.3 Major versions and breaking changes

The project follows Semantic Versioning. A major version may change CLI flags, configuration keys, on-disk schema, or host wiring. Such releases are called out in `CHANGELOG.md` and the release notes. For a major upgrade:

1. back up the vault and binary;
2. read the migration notes;
3. update the CLI first, then the console;
4. open the vault once and let migrations finish;
5. run `rolling-memory doctor` and `rolling-memory check`.

### 4.4 Downgrades

`update` refuses to replace a newer local build with an older channel version. `--force` is an explicit override for recovery and testing:

```bash
rolling-memory update --force --release-base https://github.com/rocareer/rolling-memory/releases/download/v0.0.5
```

For source installs, rebuild the desired tag instead:

```bash
ROLLING_MEMORY_SOURCE_REF=v0.0.5 bash install.sh
```

Downgrades are not a substitute for restoring a vault backup after an incompatible migration.

---

## 5. Verification and security boundaries

- `stable` / `beta`: every downloaded artifact is checked against the channel's `SHA256SUMS`; a mismatch aborts the update.
- `dev`: there is no release checksum; the Git commit is the anchor. `dev` is intentionally a source-build path and never silently replaces a binary.
- The local daemon and console binaries are replaced atomically; a failed download leaves the previous binary in place.
- A running daemon may still map the previous binary after the file is replaced. `rolling-memory install` reports that state and prints the recovery command; `rolling-memory daemon restart` authenticates the same-owner socket and starts the current binary.
- Update checks are the only network egress in this flow; memory read/write, recall, crystallization, and the graph remain offline.
- Set `ROLLING_MEMORY_UPDATE_CHECK=off` to disable background checks. Explicit `update --check` still works.
- Set `ROLLING_MEMORY_UPDATE_API_BASE` to use an enterprise mirror for tag discovery; set `ROLLING_MEMORY_RELEASE_BASE` to use a mirror for artifacts.

- **One physical corpus**: this release uses only `rm_docs`/`rm_unit_meta` plus the read-only `rm_units` model. Pre-canonical vault schemas are not migrated; start from a fresh vault when upgrading from an early build.

---

## 6. Troubleshooting

| Symptom | Meaning | Action |
|---|---|---|
| `release assets are unavailable` | The tag exists but the channel has no binary for this platform | Switch to `stable`/`beta` after assets are published, or rebuild from source |
| `needs_source_baseline` | A release install is on the `dev` channel and has no source commit | Run `ROLLING_MEMORY_SOURCE_REF=main bash install.sh` once |
| `dev channel requires a source rebuild` | `dev` never auto-replaces binaries | Use the printed installer command |
| `local version is newer than channel` | The local build is ahead of the selected channel | Keep it, or use `--force` only for an intentional downgrade |
| `database schema is newer than this build` | The vault was migrated by a newer build | Update the binary; restore the vault backup if downgrading |
| scheduled check did not run | The OS scheduler is unavailable or the task was removed | `rolling-memory update --schedule status` and reinstall if needed |

---

## 7. Summary

- Default: `stable` for published installs, `dev` for source builds.
- Stable/beta: published tags, SHA-256 verified; the console restarts automatically after a swap.
- `update.auto`: `off`, `notify` or `apply`; published installs default to `apply`.
- Every upgrade path stays available: CLI, console, background policy, or installer.
- Back up the vault and binary before a major or incompatible upgrade.
