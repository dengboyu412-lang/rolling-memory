// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package harness

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
)

// nativePluginInfo describes an enabled self-contained harness plugin for Rolling-Memory.
type nativePluginInfo struct {
	ID        string
	Dir       string
	SkillMD   string
	HasSkill  bool
	HasMCP    bool
	MCPUsable bool
	MCPDetail string
	HasHooks  bool
}

// activeNativePlugin discovers enabled native plugin declarations. Has* fields
// describe local files or registrations, not verified host runtime state.
func activeNativePlugin(harness Harness, options InstallOptions) (nativePluginInfo, bool) {
	if options.Project {
		return nativePluginInfo{}, false
	}
	switch harness.ID {
	case "zcode":
		return activePluginFromConfig(
			harness.MCPPath,
			[]string{"plugins", "enabledPlugins"},
			filepath.Join(harness.Detect, "cli", "plugins", "cache"),
			".zcode-plugin",
			options,
		)
	case "claude-code":
		if harness.HooksPath == nil {
			return nativePluginInfo{}, false
		}
		return activePluginFromConfig(
			*harness.HooksPath,
			[]string{"enabledPlugins"},
			filepath.Join(harness.Detect, "plugins", "cache"),
			".claude-plugin",
			options,
		)
	case "workbuddy":
		// WorkBuddy records enabled plugins in settings.json and materialises
		// them under plugins/cache/<marketplace>/<plugin>/<version> with a
		// .codebuddy-plugin manifest (the CLI host merges the plugin's own
		// .mcp.json for the MCP surface); directory marketplaces additionally
		// load in place from plugins/marketplaces/<name>.
		return activeWorkBuddyPlugin(harness, options)
	default:
		return nativePluginInfo{}, false
	}
}

func activePluginFromConfig(configPath string, enabledPath []string, cacheRoot, manifestDir string, options InstallOptions) (nativePluginInfo, bool) {
	if configPath == "" || !fileExists(configPath) {
		return nativePluginInfo{}, false
	}
	data, err := loadJSONFile(configPath)
	if err != nil {
		return nativePluginInfo{}, false
	}
	enabledVal, found := jsonLookup(data, enabledPath)
	if !found || enabledVal == nil {
		return nativePluginInfo{}, false
	}
	enabledMap, ok := asObject(enabledVal)
	if !ok {
		return nativePluginInfo{}, false
	}
	prefix := options.Name + "@"
	pluginID := ""
	market := ""
	for _, pair := range enabledMap {
		if pair.Key == options.Name || strings.HasPrefix(pair.Key, prefix) {
			if flag, isBool := pair.Value.(bool); isBool && flag {
				pluginID = pair.Key
				if idx := strings.IndexByte(pair.Key, '@'); idx >= 0 {
					market = pair.Key[idx+1:]
				}
				break
			}
		}
	}
	if pluginID == "" {
		return nativePluginInfo{}, false
	}
	pluginDir := resolvePluginCacheDirFor(cacheRoot, market, options.Name, manifestDir)
	if pluginDir == "" {
		return nativePluginInfo{}, false
	}
	return inspectPluginDir(pluginDir, manifestDir, pluginID, options)
}

// inspectPluginDir reports declarations separately from verified replacements.
func inspectPluginDir(pluginDir, manifestDir, pluginID string, options InstallOptions) (nativePluginInfo, bool) {
	manifest, err := loadJSONFile(filepath.Join(pluginDir, manifestDir, "plugin.json"))
	if err != nil {
		return nativePluginInfo{}, false
	}
	skillMD := filepath.Join(pluginDir, "skills", options.SkillName, "SKILL.md")
	hasSkill := fileExists(skillMD)
	var mcpEntry any
	hasMCP := false
	if entry, found := jsonLookup(manifest, []string{"mcpServers", options.Name}); found {
		hasMCP = true
		mcpEntry = entry
	}
	if !hasMCP {
		if data, err := loadJSONFile(filepath.Join(pluginDir, ".mcp.json")); err == nil {
			if entry, found := jsonLookup(data, []string{"mcpServers", options.Name}); found {
				hasMCP = true
				mcpEntry = entry
			}
		}
	}
	mcpUsable, mcpDetail := false, ""
	if hasMCP {
		mcpUsable, mcpDetail = pluginMCPCommandUsable(mcpEntry, pluginDir, options)
	}
	hasHooks := pluginHasHooks(filepath.Join(pluginDir, "hooks", "hooks.json"), pluginDir)
	if !hasSkill && !hasMCP && !hasHooks {
		return nativePluginInfo{}, false
	}
	// Local files cannot establish that the host loaded the plugin. None of the
	// declared surfaces is a verified replacement for an existing registration.
	return nativePluginInfo{
		ID:        pluginID,
		Dir:       pluginDir,
		SkillMD:   skillMD,
		HasSkill:  hasSkill,
		HasMCP:    hasMCP,
		MCPUsable: mcpUsable,
		MCPDetail: mcpDetail,
		HasHooks:  hasHooks,
	}, true
}

// pluginMCPCommandUsable checks the declared command itself. Interpreter
// arguments are not resolved: a plugin may ship a launcher that only exists in
// the host's materialised copy, and the host still performs its own check.
func pluginMCPCommandUsable(entry any, pluginDir string, options InstallOptions) (bool, string) {
	object, ok := asObject(entry)
	if !ok {
		return false, "entry is not an object"
	}
	commandValue, found := objectGet(object, "command")
	if !found {
		return false, "missing command"
	}
	command := ""
	if items, isArray := asArray(commandValue); isArray {
		if len(items) == 0 {
			return false, "empty command array"
		}
		command = pyString(items[0])
	} else {
		command = pyString(commandValue)
	}
	command = expandPluginRoot(strings.TrimSpace(command), pluginDir)
	if command == "" {
		return false, "empty command"
	}
	if isOurBinary(command, options) {
		return true, ""
	}
	if !strings.ContainsAny(command, `/\`) {
		if _, err := exec.LookPath(command); err == nil {
			return true, ""
		}
		return false, "command " + command + " is not on PATH"
	}
	path := command
	if !filepath.IsAbs(path) {
		path = filepath.Join(pluginDir, path)
	}
	info, err := os.Stat(path)
	if err != nil {
		return false, "command " + command + " is not available"
	}
	if info.IsDir() {
		return false, "command " + command + " is a directory"
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o111 == 0 {
		return false, "command " + command + " is not executable"
	}
	return true, ""
}

func expandPluginRoot(value, root string) string {
	for _, token := range []string{
		"${CLAUDE_PLUGIN_ROOT}",
		"${ZCODE_PLUGIN_ROOT}",
		"${CURSOR_PLUGIN_ROOT}",
		"${CODEBUDDY_PLUGIN_ROOT}",
	} {
		value = strings.ReplaceAll(value, token, root)
	}
	return value
}

// activeWorkBuddyPlugin resolves the enabled native plugin for WorkBuddy. The
// host materialises plugins into plugins/cache/ on install, but directory
// marketplaces also load in place, so the marketplace directory is a fallback.
func activeWorkBuddyPlugin(harness Harness, options InstallOptions) (nativePluginInfo, bool) {
	if info, ok := activePluginFromConfig(
		filepath.Join(harness.Detect, "settings.json"),
		[]string{"enabledPlugins"},
		filepath.Join(harness.Detect, "plugins", "cache"),
		".codebuddy-plugin",
		options,
	); ok {
		return info, true
	}
	return activePluginFromMarketplaceDir(harness.Detect, options)
}

// activePluginFromMarketplaceDir resolves an enabled plugin whose files live in
// the marketplace directory itself (plugins/marketplaces/<name>/<source>).
func activePluginFromMarketplaceDir(detect string, options InstallOptions) (nativePluginInfo, bool) {
	settings, err := loadJSONFile(filepath.Join(detect, "settings.json"))
	if err != nil {
		return nativePluginInfo{}, false
	}
	enabledValue, found := jsonLookup(settings, []string{"enabledPlugins"})
	if !found {
		return nativePluginInfo{}, false
	}
	enabledPlugins, ok := asObject(enabledValue)
	if !ok {
		return nativePluginInfo{}, false
	}
	marketplacesRoot := filepath.Join(detect, "plugins", "marketplaces")
	entries, err := os.ReadDir(marketplacesRoot)
	if err != nil {
		return nativePluginInfo{}, false
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		marketDir := filepath.Join(marketplacesRoot, entry.Name())
		manifestPath := ""
		for _, candidate := range []string{
			filepath.Join(marketDir, ".codebuddy-plugin", "marketplace.json"),
			filepath.Join(marketDir, ".workbuddy-plugin", "marketplace.json"),
			filepath.Join(marketDir, "marketplace.json"),
		} {
			if fileExists(candidate) {
				manifestPath = candidate
				break
			}
		}
		if manifestPath == "" {
			continue
		}
		manifest, err := loadJSONFile(manifestPath)
		if err != nil {
			continue
		}
		marketNames := []string{entry.Name()}
		if nameValue, ok := jsonLookup(manifest, []string{"name"}); ok {
			if name, isString := nameValue.(string); isString && name != "" && name != entry.Name() {
				marketNames = append(marketNames, name)
			}
		}
		pluginsValue, found := jsonLookup(manifest, []string{"plugins"})
		if !found {
			continue
		}
		plugins, ok := asArray(pluginsValue)
		if !ok {
			continue
		}
		for _, pluginValue := range plugins {
			pluginObject, ok := asObject(pluginValue)
			if !ok {
				continue
			}
			nameValue, _ := objectGet(pluginObject, "name")
			if name, _ := nameValue.(string); name != options.Name {
				continue
			}
			pluginID := ""
			for _, marketName := range marketNames {
				key := options.Name + "@" + marketName
				if flag, ok := objectGet(enabledPlugins, key); ok {
					if enabled, isBool := flag.(bool); isBool && enabled {
						pluginID = key
						break
					}
				}
			}
			if pluginID == "" {
				continue
			}
			relative := ""
			sourceValue, _ := objectGet(pluginObject, "source")
			switch source := sourceValue.(type) {
			case string:
				relative = source
			default:
				if sourceObject, isObject := asObject(source); isObject {
					if pathValue, ok := objectGet(sourceObject, "path"); ok {
						relative, _ = pathValue.(string)
					}
				}
			}
			if relative == "" {
				continue
			}
			pluginDir := filepath.Join(marketDir, filepath.FromSlash(strings.TrimPrefix(relative, "./")))
			absDir, err := filepath.Abs(pluginDir)
			if err != nil {
				continue
			}
			baseDir, err := filepath.Abs(marketDir)
			if err != nil {
				continue
			}
			if absDir != baseDir && !strings.HasPrefix(absDir, baseDir+string(filepath.Separator)) {
				continue
			}
			return inspectPluginDir(absDir, ".codebuddy-plugin", pluginID, options)
		}
	}
	return nativePluginInfo{}, false
}

// pluginHasHooks identifies the expected hook declaration. File and command
// presence do not establish that the host loaded or can run it.
func pluginHasHooks(path, pluginDir string) bool {
	data, err := loadJSONFile(path)
	if err != nil {
		return false
	}
	value, found := jsonLookup(data, []string{"hooks"})
	if !found {
		return false
	}
	events, ok := asObject(value)
	if !ok {
		return false
	}
	for _, event := range HookEvents {
		value, found := objectGet(events, event.Event)
		if !found {
			return false
		}
		groups, ok := asArray(value)
		if !ok {
			return false
		}
		foundCommand := false
		for _, group := range groups {
			for _, hook := range groupHooks(group) {
				object, ok := asObject(hook)
				if !ok {
					continue
				}
				typeValue, _ := objectGet(object, "type")
				if kind, ok := typeValue.(string); !ok || kind != "command" {
					continue
				}
				if enabledValue, found := objectGet(object, "enabled"); found {
					if enabled, ok := enabledValue.(bool); !ok || !enabled {
						continue
					}
				}
				command := strings.TrimSpace(hookCommandOf(hook))
				for _, root := range []string{"${CLAUDE_PLUGIN_ROOT}", "${ZCODE_PLUGIN_ROOT}"} {
					for _, launcher := range []string{"bin/rolling-memory-hook", "bin/rolling-memory-hook.cmd"} {
						path := root + "/" + launcher
						for _, quoted := range []string{path, `"` + path + `"`} {
							base := quoted + " " + event.Sub
							if (command == base || command == base+" --json") && fileExists(filepath.Join(pluginDir, filepath.FromSlash(launcher))) {
								foundCommand = true
							}
						}
					}
				}
			}
		}
		if !foundCommand {
			return false
		}
	}
	return len(HookEvents) > 0
}

func resolvePluginCacheDirFor(cacheRoot, market, pluginName, manifestDir string) string {
	var markets []string
	if market != "" {
		markets = []string{market}
	} else {
		entries, err := os.ReadDir(cacheRoot)
		if err != nil {
			return ""
		}
		for _, entry := range entries {
			if info, statErr := os.Stat(filepath.Join(cacheRoot, entry.Name())); statErr == nil && info.IsDir() {
				markets = append(markets, entry.Name())
			}
		}
		sort.Strings(markets)
	}
	for _, mkt := range markets {
		base := filepath.Join(cacheRoot, mkt, pluginName)
		entries, err := os.ReadDir(base)
		if err != nil {
			continue
		}
		versions := make([]string, 0, len(entries))
		for _, entry := range entries {
			if info, statErr := os.Stat(filepath.Join(base, entry.Name())); statErr == nil && info.IsDir() {
				versions = append(versions, entry.Name())
			}
		}
		if len(versions) == 0 {
			continue
		}
		sort.Slice(versions, func(left, right int) bool {
			return comparePluginVersions(versions[left], versions[right]) < 0
		})
		for i := len(versions) - 1; i >= 0; i-- {
			candidate := filepath.Join(base, versions[i])
			if fileExists(filepath.Join(candidate, manifestDir, "plugin.json")) {
				return candidate
			}
		}
	}
	return ""
}

func configHasGlobalHooks(harness Harness, options InstallOptions) bool {
	if harness.HooksPath == nil || !fileExists(*harness.HooksPath) {
		return false
	}
	data, err := loadJSONFile(*harness.HooksPath)
	if err != nil {
		return false
	}
	events, _, ok := jsonTable(data, harness.HooksKeys, false)
	if !ok {
		return false
	}
	for _, event := range HookEvents {
		groupsValue, found := objectGet(events, event.Event)
		if !found {
			continue
		}
		groups, _ := asArray(groupsValue)
		for _, group := range groups {
			if groupIsOurs(group, options) {
				return true
			}
		}
	}
	return false
}

// comparePluginVersions orders cached plugin directories by semantic version
// rather than by byte order. Host caches use plain "0.9.0" / "0.10.0" names; a
// leading "v" and a prerelease suffix are accepted.
func comparePluginVersions(left, right string) int {
	leftCore, leftPre, leftOK := parsePluginVersion(left)
	rightCore, rightPre, rightOK := parsePluginVersion(right)
	if !leftOK || !rightOK {
		return strings.Compare(left, right)
	}
	for index := 0; index < len(leftCore) || index < len(rightCore); index++ {
		var a, b int
		if index < len(leftCore) {
			a = leftCore[index]
		}
		if index < len(rightCore) {
			b = rightCore[index]
		}
		if a != b {
			if a < b {
				return -1
			}
			return 1
		}
	}
	if leftPre == rightPre {
		return strings.Compare(left, right)
	}
	if leftPre == "" {
		return 1
	}
	if rightPre == "" {
		return -1
	}
	return strings.Compare(leftPre, rightPre)
}

func parsePluginVersion(value string) ([]int, string, bool) {
	trimmed := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(value), "v"))
	core, pre, _ := strings.Cut(trimmed, "-")
	parts := strings.Split(core, ".")
	if len(parts) == 0 {
		return nil, "", false
	}
	numbers := make([]int, 0, len(parts))
	for _, part := range parts {
		number, err := strconv.Atoi(part)
		if err != nil {
			return nil, "", false
		}
		numbers = append(numbers, number)
	}
	return numbers, pre, true
}
