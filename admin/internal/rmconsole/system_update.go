package rollingmemory

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ai-go-hub/ai-go-admin/internal/kit/httpx"
	"github.com/gin-gonic/gin"
)

const defaultConsoleUpdateReleaseBase = "https://github.com/rocareer/" + Name + "/releases/latest/download"

const defaultConsoleGiteeReleaseBase = "https://gitee.com/rocareer/Rolling-Memory/releases/latest/download"

const (
	// updateStateCheckInterval 是控制台在线时共享版本缓存的刷新周期。
	// 控制台通过 rm/update 实时帧按此周期触发检查；CLI / MCP 仍使用
	// updatecheck.CheckInterval（6 小时），避免把轻量工具也变成高频网络客户端。
	updateStateCheckInterval = 10 * time.Minute
	updateStateRetryInterval = 30 * time.Minute
	// updateCheckMinInterval 合并多标签页 / 多组件同时发起的检查请求；
	// 是否“到期”由共享缓存状态决定（成功 10 分钟、失败 30 分钟）。
	updateCheckMinInterval = time.Minute
)

var (
	updateCheckMu      sync.Mutex
	updateCheckRunning bool
	updateCheckLast    time.Time
)

func updatePortableRoot() string {
	raw := strings.TrimSpace(os.Getenv("ROLLING_MEMORY_PORTABLE"))
	if raw != "" && raw != "0" && !strings.EqualFold(raw, "false") {
		if raw == "1" || strings.EqualFold(raw, "true") {
			if exe, err := os.Executable(); err == nil {
				if root := portableRootForExecutable(exe); root != "" {
					return root
				}
				return filepath.Dir(exe)
			}
			return ""
		}
		if strings.HasPrefix(raw, "~/") {
			if home, err := os.UserHomeDir(); err == nil && home != "" {
				return filepath.Join(home, strings.TrimPrefix(raw, "~/"))
			}
		}
		return raw
	}
	if exe, err := os.Executable(); err == nil {
		return portableRootForExecutable(exe)
	}
	return ""
}

func portableRootForExecutable(exe string) string {
	candidates := []string{exe}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil && resolved != "" && resolved != exe {
		candidates = append(candidates, resolved)
	}
	for _, candidate := range candidates {
		dir := filepath.Dir(candidate)
		parent := filepath.Dir(dir)
		if strings.EqualFold(filepath.Base(dir), "bin") && parent != dir {
			if info, err := os.Stat(filepath.Join(parent, ".portable")); err == nil && !info.IsDir() {
				return parent
			}
		}
		if info, err := os.Stat(filepath.Join(dir, ".portable")); err == nil && !info.IsDir() {
			return dir
		}
	}
	return ""
}

// updateStatePath mirrors the CLI cache location (XDG_STATE_HOME on POSIX,
// <portable>/state in portable mode).
func updateStatePath(home string) string {
	if root := updatePortableRoot(); root != "" {
		return filepath.Join(root, "state", "update-state.json")
	}
	base := strings.TrimSpace(os.Getenv("XDG_STATE_HOME"))
	if base == "" {
		base = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(base, "rolling-memory", "update-state.json")
}

func readUpdateState(home string) (map[string]any, bool) {
	raw, err := os.ReadFile(updateStatePath(home))
	if err != nil {
		return nil, false
	}
	var state map[string]any
	if err := json.Unmarshal(raw, &state); err != nil {
		return nil, false
	}
	return state, true
}

func stateCheckedUnix(state map[string]any) int64 {
	if state == nil {
		return 0
	}
	switch value := state["checked_at_unix"].(type) {
	case float64:
		return int64(value)
	case int64:
		return value
	case json.Number:
		parsed, _ := value.Int64()
		return parsed
	}
	return 0
}

func stateUpdateAvailable(state map[string]any) bool {
	if state == nil {
		return false
	}
	value, _ := state["update_available"].(bool)
	return value
}

func updateStateStale(home string) (map[string]any, bool) {
	state, ok := readUpdateState(home)
	if !ok {
		return nil, true
	}
	checked := stateCheckedUnix(state)
	if checked == 0 {
		return state, true
	}
	interval := updateStateCheckInterval
	if failed, _ := state["ok"].(bool); !failed {
		interval = updateStateRetryInterval
	}
	return state, time.Since(time.Unix(checked, 0)) >= interval
}

// updateCheckRecentlyFailed reports whether the last attempt failed inside the
// retry window. Explicit dashboard refreshes still respect this backoff so a
// transient outage does not turn every open tab into a retry loop.
func updateCheckRecentlyFailed(home string) bool {
	state, ok := readUpdateState(home)
	if !ok {
		return false
	}
	if failed, _ := state["ok"].(bool); failed {
		return false
	}
	checked := stateCheckedUnix(state)
	return checked > 0 && time.Since(time.Unix(checked, 0)) < updateStateRetryInterval
}

func updateCheckAutoEnabled() bool {
	if strings.HasSuffix(os.Args[0], ".test") || strings.Contains(os.Args[0], ".test.") {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(os.Getenv("ROLLING_MEMORY_UPDATE_CHECK"))) {
	case "off", "none", "0", "false", "disabled":
		return false
	default:
		return true
	}
}

func updateCheckIsRunning() bool {
	updateCheckMu.Lock()
	defer updateCheckMu.Unlock()
	return updateCheckRunning
}

// triggerUpdateCheckAsync refreshes the shared cache without blocking the
// request. Concurrent callers are coalesced; the caller decides whether the
// cached snapshot is due via updateStateStale (or an explicit force refresh).
func triggerUpdateCheckAsync(home, releaseBase string) {
	if !updateCheckAutoEnabled() {
		return
	}
	base := strings.TrimSpace(releaseBase)
	if base == "" {
		base = resolveConsoleReleaseBase("")
	}
	if base == "" {
		return
	}
	updateCheckMu.Lock()
	if updateCheckRunning || time.Since(updateCheckLast) < updateCheckMinInterval {
		updateCheckMu.Unlock()
		return
	}
	updateCheckRunning = true
	updateCheckLast = time.Now()
	updateCheckMu.Unlock()

	go func() {
		defer func() {
			updateCheckMu.Lock()
			updateCheckRunning = false
			updateCheckMu.Unlock()
		}()
		bin := resolveRealCLIBinary(home)
		if bin == "" {
			writeUpdateStateError(home, "rolling-memory CLI binary not found")
			return
		}
		args := []string{"update", "--check", "--with-console", "--json"}
		if strings.TrimSpace(base) != "" {
			args = append(args, "--release-base", strings.TrimSpace(base))
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		out, err := exec.CommandContext(ctx, bin, args...).CombinedOutput()
		if err == nil {
			return
		}
		if info, statErr := os.Stat(updateStatePath(home)); statErr == nil && time.Since(info.ModTime()) < 2*time.Minute {
			return
		}
		message := strings.TrimSpace(string(out))
		if message == "" {
			message = err.Error()
		}
		writeUpdateStateError(home, message)
	}()
}

func writeUpdateStateError(home, message string) {
	state, _ := readUpdateState(home)
	if state == nil {
		state = map[string]any{}
	}
	now := time.Now().UTC()
	state["ok"] = false
	state["error"] = message
	state["checked_at"] = now.Format(time.RFC3339)
	state["checked_at_unix"] = now.Unix()
	state["install_method"] = installMethodFromDisk(home)
	raw, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return
	}
	path := updateStatePath(home)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return
	}
	temporary := path + ".tmp." + strconv.Itoa(os.Getpid())
	if err := os.WriteFile(temporary, append(raw, '\n'), 0o644); err != nil {
		return
	}
	_ = os.Rename(temporary, path)
}

func updateDataDir(home string) string {
	if root := updatePortableRoot(); root != "" {
		return filepath.Join(root, "data")
	}
	if xdg := strings.TrimSpace(os.Getenv("XDG_DATA_HOME")); xdg != "" {
		return filepath.Join(xdg, "rolling-memory")
	}
	return filepath.Join(home, ".local", "share", "rolling-memory")
}

func configuredUpdatePrefs(home string) (channel, source, auto string) {
	bin := resolveRealCLIBinary(home)
	if bin == "" {
		return "", "", ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, bin, "config", "show", "--json").Output()
	if err != nil {
		return "", "", ""
	}
	var payload struct {
		Update struct {
			Channel string `json:"channel"`
			Source  string `json:"source"`
			Auto    string `json:"auto"`
		} `json:"update"`
	}
	if err := json.Unmarshal(out, &payload); err != nil {
		return "", "", ""
	}
	return strings.TrimSpace(payload.Update.Channel), strings.TrimSpace(payload.Update.Source), strings.TrimSpace(payload.Update.Auto)
}

func configuredUpdateChannel(home string) string {
	channel, _, _ := configuredUpdatePrefs(home)
	return channel
}

func configuredUpdateSource(home string) string {
	_, source, _ := configuredUpdatePrefs(home)
	return strings.ToLower(strings.TrimSpace(source))
}

func configuredUpdateAuto(home string) string {
	_, _, auto := configuredUpdatePrefs(home)
	return strings.ToLower(strings.TrimSpace(auto))
}

func installMethodFromDisk(home string) string {
	raw, err := os.ReadFile(filepath.Join(updateDataDir(home), "install.json"))
	if err != nil {
		return "unknown"
	}
	var meta struct {
		Method string `json:"method"`
	}
	if err := json.Unmarshal(raw, &meta); err != nil {
		return "unknown"
	}
	switch strings.ToLower(strings.TrimSpace(meta.Method)) {
	case "release", "source", "local", "go-install":
		return strings.ToLower(strings.TrimSpace(meta.Method))
	default:
		return "unknown"
	}
}

type systemUpdateRequest struct {
	ReleaseBase string `json:"release_base"`
	Channel     string `json:"channel"`
	Source      string `json:"source"`
	Auto        string `json:"auto"`
	Force       bool   `json:"force"`
	WithConsole *bool  `json:"with_console"`
	NoInstall   bool   `json:"no_install"`
}

func resolveConsoleReleaseBase(override string) string {
	cand := strings.TrimSpace(override)
	if cand == "" {
		if env, ok := os.LookupEnv("ROLLING_MEMORY_RELEASE_BASE"); ok {
			cand = strings.TrimSpace(env)
			if cand == "" {
				return ""
			}
		} else if home, _ := os.UserHomeDir(); configuredUpdateSource(home) == "gitee" {
			cand = defaultConsoleGiteeReleaseBase
		} else {
			cand = defaultConsoleUpdateReleaseBase
		}
	}
	switch strings.ToLower(cand) {
	case "", "off", "none", "0":
		return ""
	}
	return cand
}

func validateReleaseBaseURL(raw string) error {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil
	}
	if strings.HasPrefix(trimmed, "-") {
		return errors.New("invalid release_base: must not start with '-'")
	}
	low := strings.ToLower(trimmed)
	if low == "off" || low == "none" || low == "0" {
		return nil
	}
	parsed, err := url.Parse(trimmed)
	if err != nil || parsed.Scheme == "" {
		return errors.New("invalid release_base: malformed URL")
	}
	switch strings.ToLower(parsed.Scheme) {
	case "https":
		if strings.TrimSpace(parsed.Hostname()) == "" {
			return errors.New("invalid release_base: https:// URL requires a hostname")
		}
		return nil
	case "http":
		if isStrictLoopbackHost(parsed.Hostname()) {
			return nil
		}
		return errors.New("invalid release_base: http:// requires a loopback host; use https:// for remote hosts")
	default:
		return errors.New("invalid release_base: must use https:// (or http:// on loopback)")
	}
}

func isStrictLoopbackHost(host string) bool {
	h := strings.TrimSpace(strings.TrimSuffix(host, "."))
	if strings.EqualFold(h, "localhost") {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && ip.IsLoopback()
}

func normalizeUpdateChannel(raw string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", "stable":
		return "stable", nil
	case "beta":
		return "beta", nil
	case "dev":
		return "dev", nil
	default:
		return "", fmt.Errorf("update channel must be one of stable, beta, dev")
	}
}

// resolveRealCLIBinary returns the underlying compiled rolling-memory binary
// rather than the .sh launcher shim when calculating local SHA-256 digests.
func resolveRealCLIBinary(home string) string {
	ext := ""
	if runtime.GOOS == "windows" {
		ext = ".exe"
	}
	installBin := filepath.Join(home, ".local", "share", "rolling-memory", "bin", "rolling-memory"+ext)
	if info, err := os.Stat(installBin); err == nil && info.Mode().IsRegular() {
		return installBin
	}
	bin := resolveBinary(home)
	if strings.HasSuffix(bin, ".sh") {
		trimmed := strings.TrimSuffix(bin, ".sh") + ext
		if info, err := os.Stat(trimmed); err == nil && info.Mode().IsRegular() {
			return trimmed
		}
	}
	if resolved, err := filepath.EvalSymlinks(bin); err == nil && resolved != "" {
		if info, sErr := os.Stat(resolved); sErr == nil && info.Mode().IsRegular() {
			return resolved
		}
	}
	return bin
}

func consoleInstallPaths(home string) (adminBin, webDist string) {
	ext := ""
	if runtime.GOOS == "windows" {
		ext = ".exe"
	}
	base := filepath.Join(home, ".local", "share", "rolling-memory", "admin")
	if xdg := strings.TrimSpace(os.Getenv("XDG_DATA_HOME")); xdg != "" {
		base = filepath.Join(xdg, "rolling-memory", "admin")
	}
	adminBin = filepath.Join(base, "bin", "rolling-admin"+ext)
	if _, err := os.Stat(adminBin); err != nil {
		altBin := filepath.Join(base, "rolling-admin"+ext)
		if info, aErr := os.Stat(altBin); aErr == nil && info.Mode().IsRegular() {
			adminBin = altBin
		}
	}
	webDist = filepath.Join(base, "web-dist")
	return adminBin, webDist
}

type fileSHA256CacheEntry struct {
	size    int64
	mtimeNS int64
	shaHex  string
}

var (
	fileSHA256Mu    sync.Mutex
	fileSHA256Cache = map[string]fileSHA256CacheEntry{}
)

func computeFileSHA256Hex(path string) string {
	clean := filepath.Clean(strings.TrimSpace(path))
	if clean == "" || clean == "." {
		return ""
	}
	info, err := os.Stat(clean)
	if err != nil || !info.Mode().IsRegular() {
		return ""
	}
	sz := info.Size()
	mt := info.ModTime().UnixNano()
	fileSHA256Mu.Lock()
	if cached, ok := fileSHA256Cache[clean]; ok && cached.size == sz && cached.mtimeNS == mt && cached.shaHex != "" {
		fileSHA256Mu.Unlock()
		return cached.shaHex
	}
	fileSHA256Mu.Unlock()

	f, err := os.Open(clean)
	if err != nil {
		return ""
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return ""
	}
	digest := hex.EncodeToString(h.Sum(nil))
	fileSHA256Mu.Lock()
	if len(fileSHA256Cache) > 64 {
		fileSHA256Cache = map[string]fileSHA256CacheEntry{}
	}
	fileSHA256Cache[clean] = fileSHA256CacheEntry{size: sz, mtimeNS: mt, shaHex: digest}
	fileSHA256Mu.Unlock()
	return digest
}

func readMarkerFileSHA256(dir string) string {
	raw, err := os.ReadFile(filepath.Join(dir, ".sha256"))
	if err != nil {
		return ""
	}
	return strings.ToLower(strings.TrimSpace(string(raw)))
}

// systemVersionHandler returns local version and SHA-256 digest telemetry with
// zero external network requests.
func systemVersionHandler(c *gin.Context) {
	home, _ := os.UserHomeDir()
	cliBin := resolveRealCLIBinary(home)
	adminBin, webDist := consoleInstallPaths(home)
	adminSHA := computeFileSHA256Hex(adminBin)
	if adminSHA == "" {
		if self, err := os.Executable(); err == nil {
			adminSHA = computeFileSHA256Hex(self)
			adminBin = self
		}
	}
	webSHA := readMarkerFileSHA256(webDist)
	_, adminStatErr := os.Stat(adminBin)
	_, webStatErr := os.Stat(filepath.Join(webDist, "index.html"))
	releaseBase := resolveConsoleReleaseBase("")
	updateState, _ := readUpdateState(home)
	updateChannel, updateSource, updateAuto := configuredUpdatePrefs(home)
	if updateChannel == "" {
		updateChannel, _ = updateState["channel"].(string)
	}
	if updateSource == "" {
		updateSource = "github"
	}

	httpx.Success(c, httpx.WithData(gin.H{
		"ok":                      true,
		"current_version":         liveSoftwareVersion(),
		"core_version":            CoreVersion,
		"core_codename":           CoreCodename,
		"os":                      runtime.GOOS,
		"arch":                    runtime.GOARCH,
		"release_base":            releaseBase,
		"release_channel_enabled": releaseBase != "",
		"install_method":          installMethodFromDisk(home),
		"update_channel":          updateChannel,
		"update_source":           updateSource,
		"update_auto":             updateAuto,
		"update_state":            updateState,
		"cli": gin.H{
			"path":         cliBin,
			"local_sha256": computeFileSHA256Hex(cliBin),
		},
		"console": gin.H{
			"installed":       adminStatErr == nil || webStatErr == nil,
			"admin_path":      adminBin,
			"admin_local_sha": adminSHA,
			"web_dist_path":   webDist,
			"web_local_sha":   webSHA,
		},
	}))
}

// systemUpdateStatusHandler returns the cached update snapshot immediately and
// schedules one asynchronous refresh when the snapshot is stale. It never
// blocks on the network; the web console receives the refreshed frame over the
// rm/update WebSocket room.
func systemUpdateStatusHandler(c *gin.Context) {
	var req systemUpdateRequest
	_ = c.ShouldBindJSON(&req)
	home, _ := os.UserHomeDir()
	state, stale := updateStateStale(home)
	force := req.Force && !updateCheckRecentlyFailed(home)
	if force || stale {
		triggerUpdateCheckAsync(home, "")
	}
	httpx.Success(c, httpx.WithData(gin.H{
		"ok":             true,
		"state":          state,
		"available":      state != nil,
		"stale":          stale,
		"checking":       updateCheckIsRunning(),
		"forced":         force,
		"install_method": installMethodFromDisk(home),
	}))
}

// systemUpdateChannelHandler persists the release channel through the CLI config
// command and kicks off one refresh of the shared update cache.
func systemUpdateChannelHandler(c *gin.Context) {
	var req systemUpdateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		httpx.Fail(c, httpx.WithMessage("invalid request body"))
		return
	}
	channel, err := normalizeUpdateChannel(req.Channel)
	if err != nil || strings.TrimSpace(req.Channel) == "" {
		httpx.Fail(c, httpx.WithMessage("channel must be stable, beta, or dev"))
		return
	}
	home, _ := os.UserHomeDir()
	bin := resolveRealCLIBinary(home)
	if bin == "" {
		httpx.Fail(c, httpx.WithMessage("rolling-memory CLI binary not found"))
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 15*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, bin, "config", "set", "update.channel", channel).CombinedOutput()
	if err != nil {
		message := strings.TrimSpace(string(out))
		if message == "" {
			message = err.Error()
		}
		httpx.Fail(c, httpx.WithMessage(message))
		return
	}
	if configuredUpdateAuto(home) != "off" {
		_ = installUpdateSchedule(ctx, bin)
	}
	triggerUpdateCheckAsync(home, "")
	httpx.Success(c, httpx.WithData(gin.H{"ok": true, "channel": channel}))
}

// installUpdateSchedule refreshes the user-level task so it carries the
// current channel/source release base. Errors are returned to the caller for
// reporting but never block saving the preference.
func installUpdateSchedule(ctx context.Context, bin string) string {
	out, err := exec.CommandContext(ctx, bin, "update", "--schedule", "install", "--quiet").CombinedOutput()
	if err == nil {
		return ""
	}
	message := strings.TrimSpace(string(out))
	if message == "" {
		message = err.Error()
	}
	return message
}

// systemUpdateSourceHandler persists the release source (github or gitee)
// through the CLI config command and refreshes the shared update cache.
func systemUpdateSourceHandler(c *gin.Context) {
	var req systemUpdateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		httpx.Fail(c, httpx.WithMessage("invalid request body"))
		return
	}
	source := strings.ToLower(strings.TrimSpace(req.Source))
	if source != "github" && source != "gitee" {
		httpx.Fail(c, httpx.WithMessage("source must be github or gitee"))
		return
	}
	home, _ := os.UserHomeDir()
	bin := resolveRealCLIBinary(home)
	if bin == "" {
		httpx.Fail(c, httpx.WithMessage("rolling-memory CLI binary not found"))
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 15*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, bin, "config", "set", "update.source", source).CombinedOutput()
	if err != nil {
		message := strings.TrimSpace(string(out))
		if message == "" {
			message = err.Error()
		}
		httpx.Fail(c, httpx.WithMessage(message))
		return
	}
	releaseBase := resolveConsoleReleaseBase("")
	if configuredUpdateAuto(home) != "off" {
		_ = installUpdateSchedule(ctx, bin)
	}
	triggerUpdateCheckAsync(home, "")
	httpx.Success(c, httpx.WithData(gin.H{"ok": true, "source": source, "release_base": releaseBase}))
}

// systemUpdateAutoHandler persists the background update policy and keeps the
// OS-level schedule in sync. "off" removes the schedule; notify/apply install
// it so the policy is applied without the console being open.
func systemUpdateAutoHandler(c *gin.Context) {
	var req systemUpdateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		httpx.Fail(c, httpx.WithMessage("invalid request body"))
		return
	}
	auto := strings.ToLower(strings.TrimSpace(req.Auto))
	if auto != "off" && auto != "notify" && auto != "apply" {
		httpx.Fail(c, httpx.WithMessage("auto must be off, notify or apply"))
		return
	}
	home, _ := os.UserHomeDir()
	bin := resolveRealCLIBinary(home)
	if bin == "" {
		httpx.Fail(c, httpx.WithMessage("rolling-memory CLI binary not found"))
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 20*time.Second)
	defer cancel()
	if out, err := exec.CommandContext(ctx, bin, "config", "set", "update.auto", auto).CombinedOutput(); err != nil {
		message := strings.TrimSpace(string(out))
		if message == "" {
			message = err.Error()
		}
		httpx.Fail(c, httpx.WithMessage(message))
		return
	}
	action := "install"
	if auto == "off" {
		action = "remove"
	}
	scheduleErr := ""
	if out, err := exec.CommandContext(ctx, bin, "update", "--schedule", action, "--quiet").CombinedOutput(); err != nil {
		scheduleErr = strings.TrimSpace(string(out))
		if scheduleErr == "" {
			scheduleErr = err.Error()
		}
	}
	triggerUpdateCheckAsync(home, "")
	httpx.Success(c, httpx.WithData(gin.H{
		"ok": true, "auto": auto, "schedule_action": action, "schedule_error": scheduleErr,
	}))
}

// systemUpdateCheckHandler runs `rolling-memory update --check --with-console --json`
// on explicit user request to compare local digests against the release channel's SHA256SUMS.
func systemUpdateCheckHandler(c *gin.Context) {
	var req systemUpdateRequest
	_ = c.ShouldBindJSON(&req)
	if err := validateReleaseBaseURL(req.ReleaseBase); err != nil {
		httpx.Fail(c, httpx.WithMessage(err.Error()))
		return
	}
	home, _ := os.UserHomeDir()
	bin := resolveRealCLIBinary(home)
	if bin == "" {
		httpx.Fail(c, httpx.WithMessage("rolling-memory CLI binary not found"))
		return
	}

	args := []string{"update", "--check", "--with-console", "--json"}
	if strings.TrimSpace(req.ReleaseBase) != "" {
		args = append(args, "--release-base", strings.TrimSpace(req.ReleaseBase))
	}
	if source := strings.ToLower(strings.TrimSpace(req.Source)); source == "github" || source == "gitee" {
		args = append(args, "--source", source)
	}
	if channel, err := normalizeUpdateChannel(req.Channel); err == nil && strings.TrimSpace(req.Channel) != "" {
		args = append(args, "--channel", channel)
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), 25*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, args...)
	out, err := cmd.CombinedOutput()
	payload := parseCLIJSONOutput(out)
	if err != nil {
		msg := extractCLIErrorMessage(payload, out, err)
		httpx.Fail(c, httpx.WithMessage(msg))
		return
	}
	if payload == nil {
		httpx.Fail(c, httpx.WithMessage("unexpected empty JSON output from rolling-memory update --check"))
		return
	}
	httpx.Success(c, httpx.WithData(payload))
}

// systemUpdateApplyHandler executes an in-place SHA-256 verified self-update via
// `rolling-memory update --with-console --defer-console-restart --json`.
func systemUpdateApplyHandler(c *gin.Context) {
	var req systemUpdateRequest
	_ = c.ShouldBindJSON(&req)
	if err := validateReleaseBaseURL(req.ReleaseBase); err != nil {
		httpx.Fail(c, httpx.WithMessage(err.Error()))
		return
	}
	home, _ := os.UserHomeDir()
	bin := resolveRealCLIBinary(home)
	if bin == "" {
		httpx.Fail(c, httpx.WithMessage("rolling-memory CLI binary not found"))
		return
	}

	withConsole := true
	if req.WithConsole != nil {
		withConsole = *req.WithConsole
	}
	args := []string{"update", "--defer-console-restart", "--json"}
	if source := strings.ToLower(strings.TrimSpace(req.Source)); source == "github" || source == "gitee" {
		args = append(args, "--source", source)
	}
	if channel, err := normalizeUpdateChannel(req.Channel); err == nil && strings.TrimSpace(req.Channel) != "" {
		args = append(args, "--channel", channel)
	}
	if withConsole {
		args = append(args, "--with-console")
	} else {
		args = append(args, "--no-console")
	}
	if req.Force {
		args = append(args, "--force")
	}
	if req.NoInstall {
		args = append(args, "--no-install")
	}
	if strings.TrimSpace(req.ReleaseBase) != "" {
		args = append(args, "--release-base", strings.TrimSpace(req.ReleaseBase))
	}

	ctx, cancel := context.WithTimeout(c.Request.Context(), 180*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, args...)
	out, err := cmd.CombinedOutput()
	payload := parseCLIJSONOutput(out)
	if err != nil {
		msg := extractCLIErrorMessage(payload, out, err)
		httpx.Fail(c, httpx.WithMessage(msg))
		return
	}
	if payload == nil {
		httpx.Fail(c, httpx.WithMessage("unexpected empty JSON output from rolling-memory update"))
		return
	}

	// If the console binary or web assets were updated while the console was
	// running, schedule an asynchronous console restart after the HTTP response
	// has been flushed to the browser.
	if deferred, _ := payload["console_restart_deferred"].(bool); deferred {
		updatedBin := resolveRealCLIBinary(home)
		if updatedBin != "" {
			logPath := filepath.Join(home, ".local", "state", "rolling-memory", "console-restart.log")
			// The helper must outlive the backend it stops: a goroutine inside
			// this process disappears together with the process, leaving the
			// console stopped. A detached new session survives and completes
			// the stop/start cycle.
			go func(cliBinary, logPath string) {
				time.Sleep(600 * time.Millisecond)
				_ = startDetached(logPath, cliBinary, "panel", "restart", "--no-open")
			}(updatedBin, logPath)
		}
	}

	httpx.Success(c, httpx.WithData(payload))
}

func parseCLIJSONOutput(raw []byte) map[string]any {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" {
		return nil
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(trimmed), &payload); err == nil {
		return payload
	}
	// Fallback: locate the last JSON object line in case hooks or warnings printed to stderr.
	lines := strings.Split(trimmed, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if strings.HasPrefix(line, "{") && strings.HasSuffix(line, "}") {
			if err := json.Unmarshal([]byte(line), &payload); err == nil {
				return payload
			}
		}
	}
	return nil
}

func extractCLIErrorMessage(payload map[string]any, raw []byte, runErr error) string {
	if payload != nil {
		if errStr, ok := payload["error"].(string); ok && strings.TrimSpace(errStr) != "" {
			return strings.TrimSpace(errStr)
		}
	}
	text := strings.TrimSpace(string(raw))
	if text != "" {
		return text
	}
	if runErr != nil {
		return fmt.Sprintf("update command failed: %v", runErr)
	}
	return "update command failed"
}
