// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package harness

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/rocareer/rolling-memory/internal/ids"
)

// The hooks surface (zcode / claude-code): an event -> groups JSON map where
// each group carries a command hook. The marker for "ours" is a " hook "
// command that references the launcher.

type hookCommand struct {
	Event   string
	Command string
	Timeout int
}

func hookCommands(options InstallOptions) []hookCommand {
	binary := shlexQuote(options.ResolvedBinary())
	commands := make([]hookCommand, 0, len(HookEvents))
	for _, event := range HookEvents {
		commands = append(commands, hookCommand{
			Event:   event.Event,
			Command: fmt.Sprintf("%s hook %s --json", binary, event.Sub),
			Timeout: event.Timeout,
		})
	}
	return commands
}

func hookGroup(command string, timeout int, extras map[string]any) ids.OrderedMap {
	entry := ids.OrderedMap{
		{Key: "type", Value: "command"},
		{Key: "command", Value: command},
		{Key: "timeout", Value: timeout},
	}
	keys := make([]string, 0, len(extras))
	for key := range extras {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		entry = append(entry, ids.JSONPair{Key: key, Value: extras[key]})
	}
	return ids.OrderedMap{{Key: "hooks", Value: []any{entry}}}
}

func hookCommandOf(hook any) string {
	object, ok := asObject(hook)
	if !ok {
		return ""
	}
	value, found := objectGet(object, "command")
	if !found || !pyTruthy(value) {
		return ""
	}
	return pyString(value)
}

// pyTruthy mirrors the subset of standard truthiness the hook reader needs.
func pyTruthy(value any) bool {
	switch typed := value.(type) {
	case nil:
		return false
	case string:
		return typed != ""
	}
	if number, ok := numericValue(value); ok {
		return number != 0
	}
	return true
}

func groupHooks(group any) []any {
	object, ok := asObject(group)
	if !ok {
		return nil
	}
	value, found := objectGet(object, "hooks")
	if !found {
		return nil
	}
	hooks, _ := asArray(value)
	return hooks
}

func groupCommandStatus(group any, command string) (int, int) {
	object, ok := asObject(group)
	if !ok {
		return 0, 0
	}
	validGroup := true
	if matcher, found := objectGet(object, "matcher"); found {
		value, ok := matcher.(string)
		validGroup = ok && (value == "" || value == "*")
	}
	valid := 0
	invalid := 0
	for _, hook := range groupHooks(group) {
		if hookCommandOf(hook) != command {
			continue
		}
		entry, ok := asObject(hook)
		if !ok {
			invalid++
			continue
		}
		kind, _ := objectGet(entry, "type")
		if kind != "command" || !validGroup {
			invalid++
			continue
		}
		if enabled, exists := objectGet(entry, "enabled"); exists && enabled != true {
			invalid++
			continue
		}
		valid++
	}
	return valid, invalid
}

func groupIsOurs(group any, options InstallOptions) bool {
	for _, hook := range groupHooks(group) {
		if hookIsOurs(hookCommandOf(hook), options) {
			return true
		}
	}
	return false
}

func hooksTargetPath(harness Harness, options InstallOptions) (string, bool) {
	if harness.HooksKind == "" || harness.HooksPath == nil {
		return "", false
	}
	if !options.Project {
		return *harness.HooksPath, true
	}
	switch harness.ID {
	case "claude-code":
		return filepath.Join(options.Cwd, ".claude", "settings.json"), true
	case "codex":
		// Codex reads a project-level .codex/hooks.json alongside the user file.
		return filepath.Join(options.Cwd, ".codex", "hooks.json"), true
	default:
		return "", false
	}
}

func removeOwnedHooks(group any, options InstallOptions) (any, int, bool) {
	object, ok := asObject(group)
	if !ok {
		return group, 0, true
	}
	hooks := groupHooks(group)
	kept := make([]any, 0, len(hooks))
	removed := 0
	for _, hook := range hooks {
		if hookIsOurs(hookCommandOf(hook), options) {
			removed++
		} else {
			kept = append(kept, hook)
		}
	}
	if removed == 0 {
		return group, 0, true
	}
	if len(kept) == 0 && len(object) == 1 {
		return nil, removed, false
	}
	return objectSet(object, "hooks", kept), removed, true
}

func claudeHooksDisabled(harness Harness, options InstallOptions, data ids.OrderedMap) (bool, error) {
	if harness.ID != "claude-code" {
		return false, nil
	}
	settings := []ids.OrderedMap{data}
	if options.Project && harness.HooksPath != nil {
		user, err := loadJSONFile(*harness.HooksPath)
		if err != nil {
			return false, err
		}
		settings = append(settings, user)
	}
	for _, config := range settings {
		if value, found := objectGet(config, "disableAllHooks"); found {
			disabled, ok := value.(bool)
			if !ok {
				return false, fmt.Errorf("disableAllHooks is not a boolean")
			}
			if disabled {
				return true, nil
			}
		}
	}
	return false, nil
}

func hooksEnabled(harness Harness, options InstallOptions, data ids.OrderedMap, path string, report *Report) bool {
	disabled, err := claudeHooksDisabled(harness, options, data)
	if err != nil || disabled {
		detail := "Claude hooks are disabled"
		if err != nil {
			detail = "cannot determine Claude hook setting: " + err.Error()
		}
		report.Add(harness.ID, "hooks", path, StatusConflict, detail)
		return false
	}
	if len(harness.HooksEnabledKeys) > 0 {
		if enabled, found := jsonLookup(data, harness.HooksEnabledKeys); found {
			if flag, ok := enabled.(bool); !ok || !flag {
				report.Add(harness.ID, "hooks", path, StatusConflict, "hooks are disabled or the enable switch is invalid")
				return false
			}
		}
	}
	return true
}

func installHooks(harness Harness, options InstallOptions, report *Report) {
	path, supported := hooksTargetPath(harness, options)
	if !supported {
		report.Add(harness.ID, "hooks", "-", StatusSkipped,
			"no hook surface; agent-driven remember/recall only")
		return
	}
	data, err := loadJSONFile(path)
	if err != nil {
		report.Add(harness.ID, "hooks", path, StatusError, "cannot parse: "+err.Error())
		return
	}
	if !hooksEnabled(harness, options, data, path, report) {
		return
	}
	if plugin, ok := activeNativePlugin(harness, options); ok && plugin.HasHooks {
		if configHasGlobalHooks(harness, options) {
			if options.Dedupe {
				start := len(report.Actions)
				uninstallHooks(harness, options, report)
				for index := start; index < len(report.Actions); index++ {
					action := &report.Actions[index]
					if action.Component == "hooks" && (action.Status == StatusRemoved || action.Status == StatusPlanned) {
						action.Detail = "deduplicated; plugin " + plugin.ID + " provides this hook"
					}
				}
				return
			}
			report.Add(harness.ID, "hooks", path, StatusConflict,
				"existing hooks retained; plugin "+plugin.ID+" is also active; run `rolling-memory install "+harness.ID+" --with-hooks --dedupe` to remove this tool's hook")
			return
		}
		report.Add(harness.ID, "hooks", path, StatusUnchanged, "1 event (declared by plugin "+plugin.ID+")")
		return
	}
	if !jsonPathIsObject(data, harness.HooksKeys) {
		report.Add(harness.ID, "hooks", path, StatusConflict, "hooks path contains a non-object value")
		return
	}
	events, data, ok := jsonTable(data, harness.HooksKeys, true)
	if !ok {
		report.Add(harness.ID, "hooks", path, StatusError, "cannot parse: missing hooks table")
		return
	}
	dirty := false
	problems := make([]string, 0)
	for _, command := range hookCommands(options) {
		groupsValue, found := objectGet(events, command.Event)
		var groups []any
		if !found || groupsValue == nil {
			groups = []any{}
			events = objectSet(events, command.Event, groups)
		} else {
			var isArray bool
			groups, isArray = asArray(groupsValue)
			if !isArray {
				problems = append(problems, command.Event+": not a list")
				continue
			}
		}
		malformed := false
		for _, group := range groups {
			object, ok := asObject(group)
			if !ok {
				malformed = true
				break
			}
			value, found := objectGet(object, "hooks")
			if _, ok := asArray(value); !found || !ok {
				malformed = true
				break
			}
		}
		if malformed {
			problems = append(problems, command.Event+": invalid hook group")
			continue
		}
		valid := 0
		invalid := 0
		stale := false
		for _, group := range groups {
			groupValid, groupInvalid := groupCommandStatus(group, command.Command)
			valid += groupValid
			invalid += groupInvalid
			if groupIsOurs(group, options) {
				stale = true
			}
		}
		if invalid > 0 {
			problems = append(problems, command.Event+": matching hook is disabled or has incompatible type/matcher")
			continue
		}
		if valid > 1 {
			problems = append(problems, command.Event+": duplicate matching hooks")
			continue
		}
		if valid == 1 {
			continue
		}
		if stale && !options.Force {
			problems = append(problems, command.Event+": an entry with our name differs (use --force)")
			continue
		}
		kept := make([]any, 0, len(groups)+1)
		for _, group := range groups {
			updated, _, keep := removeOwnedHooks(group, options)
			if keep {
				kept = append(kept, updated)
			}
		}
		kept = append(kept, hookGroup(command.Command, command.Timeout, harness.HooksExtras))
		events = objectSet(events, command.Event, kept)
		dirty = true
	}
	if len(problems) > 0 {
		report.Add(harness.ID, "hooks", path, StatusConflict, strings.Join(problems, "; "))
		return
	}
	if !dirty {
		report.Add(harness.ID, "hooks", path, StatusUnchanged, "1 event")
		return
	}
	data, _ = jsonTableWith(data, harness.HooksKeys, true, func(ids.OrderedMap) ids.OrderedMap {
		return events
	})
	if options.DryRun {
		report.Add(harness.ID, "hooks", path, StatusPlanned, "1 event")
		return
	}
	text, err := dumpJSON(data)
	if err != nil {
		report.Add(harness.ID, "hooks", path, StatusError, "cannot encode: "+err.Error())
		return
	}
	if _, err := writeText(path, text); err != nil {
		report.Add(harness.ID, "hooks", path, StatusError, "cannot write: "+err.Error())
		return
	}
	report.Add(harness.ID, "hooks", path, StatusInstalled, "UserPromptSubmit")
}

func sharedHookInUse(harness Harness, options InstallOptions) (bool, error) {
	paths := mcpTargetPaths(harness, options)
	if len(paths) == 0 {
		return false, nil
	}
	for _, path := range paths {
		data, err := loadJSONFile(path)
		if err != nil {
			return false, err
		}
		if !jsonPathIsObject(data, harness.MCPKeys) {
			return false, fmt.Errorf("MCP path contains a non-object value")
		}
		table, _, ok := jsonTable(data, harness.MCPKeys, false)
		if !ok {
			continue
		}
		for _, pair := range table {
			if entryIsOurs(pair.Value, options) && (pair.Key != options.Name || !options.DryRun) {
				return true, nil
			}
		}
	}
	return false, nil
}

func uninstallHooks(harness Harness, options InstallOptions, report *Report) {
	path, supported := hooksTargetPath(harness, options)
	if !supported {
		report.Add(harness.ID, "hooks", "-", StatusSkipped, "no hook surface for this scope")
		return
	}
	if !fileExists(path) {
		report.Add(harness.ID, "hooks", path, StatusSkipped, "config file not present")
		return
	}
	inUse, err := sharedHookInUse(harness, options)
	if err != nil {
		report.Add(harness.ID, "hooks", path, StatusConflict, "cannot determine shared hook users: "+err.Error())
		return
	}
	if inUse {
		report.Add(harness.ID, "hooks", path, StatusSkipped, "shared hook retained for another MCP registration")
		return
	}
	data, err := loadJSONFile(path)
	if err != nil {
		report.Add(harness.ID, "hooks", path, StatusError, "cannot parse: "+err.Error())
		return
	}
	events, _, ok := jsonTable(data, harness.HooksKeys, false)
	if !ok || len(events) == 0 {
		report.Add(harness.ID, "hooks", path, StatusSkipped, "no hooks section")
		return
	}
	removed := 0
	for _, event := range HookEvents {
		groupsValue, found := objectGet(events, event.Event)
		if !found {
			continue
		}
		groups, isArray := asArray(groupsValue)
		if !isArray {
			report.Add(harness.ID, "hooks", path, StatusConflict, event.Event+": not a list")
			return
		}
		for _, group := range groups {
			object, ok := asObject(group)
			if !ok {
				report.Add(harness.ID, "hooks", path, StatusConflict, event.Event+": invalid hook group")
				return
			}
			value, found := objectGet(object, "hooks")
			if _, ok := asArray(value); !found || !ok {
				report.Add(harness.ID, "hooks", path, StatusConflict, event.Event+": invalid hook group")
				return
			}
		}
		kept := make([]any, 0, len(groups))
		for _, group := range groups {
			updated, count, keep := removeOwnedHooks(group, options)
			removed += count
			if keep {
				kept = append(kept, updated)
			}
		}
		if len(kept) > 0 {
			events = objectSet(events, event.Event, kept)
		} else if len(groups) > 0 {
			events = objectDelete(events, event.Event)
		}
	}
	if removed == 0 {
		report.Add(harness.ID, "hooks", path, StatusSkipped, "no entries of ours")
		return
	}
	if options.DryRun {
		report.Add(harness.ID, "hooks", path, StatusPlanned, fmt.Sprintf("%d entries", removed))
		return
	}
	data, ok = jsonTableWith(data, harness.HooksKeys, false, func(ids.OrderedMap) ids.OrderedMap {
		return events
	})
	if !ok {
		report.Add(harness.ID, "hooks", path, StatusSkipped, "no hooks section")
		return
	}
	text, err := dumpJSON(data)
	if err != nil {
		report.Add(harness.ID, "hooks", path, StatusError, "cannot encode: "+err.Error())
		return
	}
	if _, err := writeText(path, text); err != nil {
		report.Add(harness.ID, "hooks", path, StatusError, "cannot write: "+err.Error())
		return
	}
	report.Add(harness.ID, "hooks", path, StatusRemoved, fmt.Sprintf("%d entries", removed))
}

func checkHooks(harness Harness, options InstallOptions, report *Report) {
	path, supported := hooksTargetPath(harness, options)
	if !supported {
		report.Add(harness.ID, "hooks", "-", StatusSkipped, "not supported for this scope")
		return
	}
	// A check reports the wiring that is on disk, not whether --with-hooks was
	// passed to this check run.
	data, err := loadJSONFile(path)
	if err != nil {
		report.Add(harness.ID, "hooks", path, StatusError, "cannot parse: "+err.Error())
		return
	}
	if !hooksEnabled(harness, options, data, path, report) {
		return
	}
	if plugin, ok := activeNativePlugin(harness, options); ok && plugin.HasHooks {
		if configHasGlobalHooks(harness, options) {
			report.Add(harness.ID, "hooks", path, StatusConflict,
				"duplicate hooks: both config.json and plugin "+plugin.ID+" active (run `rolling-memory install "+harness.ID+" --with-hooks` to deduplicate)")
			return
		}
		report.Add(harness.ID, "hooks", path, StatusOK, "1 event · plugin "+plugin.ID)
		return
	}
	if !fileExists(path) {
		report.Add(harness.ID, "hooks", path, StatusSkipped, "not wired")
		return
	}
	events, _, _ := jsonTable(data, harness.HooksKeys, false)
	missing := make([]string, 0)
	invalid := make([]string, 0)
	duplicates := make([]string, 0)
	for _, command := range hookCommands(options) {
		groupsValue, found := objectGet(events, command.Event)
		groups, _ := asArray(groupsValue)
		valid := 0
		invalidCount := 0
		if found {
			for _, group := range groups {
				groupValid, groupInvalid := groupCommandStatus(group, command.Command)
				valid += groupValid
				invalidCount += groupInvalid
			}
		}
		if invalidCount > 0 {
			invalid = append(invalid, command.Event)
		}
		if valid > 1 {
			duplicates = append(duplicates, command.Event)
		}
		if valid == 0 {
			missing = append(missing, command.Event)
		}
	}
	if len(duplicates) > 0 {
		report.Add(harness.ID, "hooks", path, StatusConflict, "duplicate matching hooks ("+strings.Join(duplicates, ", ")+")")
		return
	}
	if len(invalid) > 0 {
		report.Add(harness.ID, "hooks", path, StatusConflict, "matching hooks disabled or incompatible ("+strings.Join(invalid, ", ")+")")
		return
	}
	if len(missing) == 0 {
		detail := "1 event"
		if len(harness.HooksEnabledKeys) > 0 {
			if _, found := jsonLookup(data, harness.HooksEnabledKeys); !found {
				// ZCode 的配置文件钩子默认关闭：如实提示总开关未设置（宿主仍会跳过
				// 这些钩子）。状态语义保持「检查只报告我们写下的接线」，也绝不替
				// 用户翻转宿主全局开关；需要真正运行时在宿主里打开该开关。
				detail = "1 event · " + strings.Join(harness.HooksEnabledKeys, ".") +
					" is not set; configuration-file hooks stay disabled until the host switch is on"
			}
		}
		report.Add(harness.ID, "hooks", path, StatusOK, detail)
		return
	}
	report.Add(harness.ID, "hooks", path, StatusSkipped, "not wired ("+strings.Join(missing, ", ")+")")
}
