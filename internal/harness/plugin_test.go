// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package harness

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/rocareer/rolling-memory/internal/ids"
)

func setupZCodePluginCache(t *testing.T, fx *fixture, version string) string {
	t.Helper()
	pluginDir := filepath.Join(fx.home, ".zcode", "cli", "plugins", "cache", "marketplace", DefaultName, version)
	if _, err := writeText(filepath.Join(pluginDir, ".zcode-plugin", "plugin.json"), `{
  "name": "rolling-memory",
  "version": "`+version+`",
  "skills": "./skills",
  "hooks": "./hooks/hooks.json",
  "mcpServers": {
    "rolling-memory": {
      "type": "stdio",
      "command": "/bin/bash",
      "args": ["${CLAUDE_PLUGIN_ROOT}/bin/rolling-memory-mcp"]
    }
  }
}`); err != nil {
		t.Fatalf("writeText plugin.json: %v", err)
	}
	if _, err := writeText(filepath.Join(pluginDir, "skills", DefaultName, "SKILL.md"), "---\nname: "+DefaultName+"\ndescription: synthetic test skill\n---\n\n# body\n"); err != nil {
		t.Fatalf("writeText SKILL.md: %v", err)
	}
	if _, err := writeText(filepath.Join(pluginDir, "hooks", "hooks.json"), `{"hooks":{"UserPromptSubmit":[{"hooks":[{"type":"command","command":"${CLAUDE_PLUGIN_ROOT}/bin/rolling-memory-hook recall"}]}]}}`); err != nil {
		t.Fatalf("writeText hooks.json: %v", err)
	}
	if _, err := writeText(filepath.Join(pluginDir, "bin", "rolling-memory-hook"), "#!/bin/sh\n"); err != nil {
		t.Fatal(err)
	}
	return pluginDir
}

func enableZCodePlugin(t *testing.T, fx *fixture) {
	t.Helper()
	data, err := loadJSONFile(fx.zcodeConfig())
	if err != nil {
		t.Fatal(err)
	}
	data, _ = jsonTableWith(data, []string{"plugins", "enabledPlugins"}, true, func(current ids.OrderedMap) ids.OrderedMap {
		return objectSet(current, "rolling-memory@marketplace", true)
	})
	raw, err := dumpJSON(data)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := writeText(fx.zcodeConfig(), raw); err != nil {
		t.Fatal(err)
	}
}

func TestInstallDedupesOwnedSurfacesWhenPluginProvidesThem(t *testing.T) {
	fx := newFixture(t)
	options := fx.options()
	options.Name = DefaultName
	options.SkillName = DefaultName
	options.Hooks = true
	zcode, err := SelectHarnesses(fx.registry(), []string{"zcode"}, false)
	if err != nil {
		t.Fatal(err)
	}
	assertNoFailures(t, Install(zcode, options))
	setupZCodePluginCache(t, fx, "0.7.4")
	enableZCodePlugin(t, fx)

	options.Dedupe = true
	report := Install(zcode, options)
	for _, component := range []string{"skill", "mcp", "hooks"} {
		action := firstAction(t, report, "zcode", component)
		if action.Status != StatusRemoved {
			t.Fatalf("%s dedupe = %#v, want removed", component, action)
		}
		if !strings.Contains(action.Detail, "plugin") {
			t.Fatalf("%s dedupe detail = %q", component, action.Detail)
		}
	}
	if fileExists(filepath.Join(fx.home, ".zcode", "skills", DefaultName, "SKILL.md")) {
		t.Fatalf("dedupe left the CLI skill in place")
	}
	data, err := loadJSONFile(fx.zcodeConfig())
	if err != nil {
		t.Fatal(err)
	}
	if _, found := jsonLookup(data, []string{"mcp", "servers", DefaultName}); found {
		t.Fatalf("dedupe left the CLI MCP entry in place")
	}
	if configHasGlobalHooks(zcode[0], options) {
		t.Fatalf("dedupe left the CLI hook in place")
	}
	assertNoFailures(t, Check(zcode, options, false))
}

func TestForceDoesNotDuplicatePluginOwnedSurfaces(t *testing.T) {
	fx := newFixture(t)
	options := fx.options()
	options.Name = DefaultName
	options.SkillName = DefaultName
	zcode, err := SelectHarnesses(fx.registry(), []string{"zcode"}, false)
	if err != nil {
		t.Fatal(err)
	}
	setupZCodePluginCache(t, fx, "0.7.4")
	enableZCodePlugin(t, fx)
	options.Force = true
	assertNoFailures(t, Install(zcode, options))
	if fileExists(filepath.Join(fx.home, ".zcode", "skills", DefaultName, "SKILL.md")) {
		t.Fatalf("--force wrote a duplicate skill beside the active plugin")
	}
}

func TestPluginCacheOrderUsesSemanticVersions(t *testing.T) {
	root := t.TempDir()
	for _, version := range []string{"0.9.0", "0.10.0", "0.0.6"} {
		manifest := filepath.Join(root, "marketplace", DefaultName, version, ".zcode-plugin", "plugin.json")
		if _, err := writeText(manifest, `{"name":"rolling-memory"}`); err != nil {
			t.Fatal(err)
		}
	}
	got := resolvePluginCacheDirFor(root, "marketplace", DefaultName, ".zcode-plugin")
	want := filepath.Join(root, "marketplace", DefaultName, "0.10.0")
	if got != want {
		t.Fatalf("resolved plugin cache = %q, want %q", got, want)
	}
	if comparePluginVersions("1.0.0-rc.1", "1.0.0") >= 0 {
		t.Fatalf("prerelease must sort before the release")
	}
	if comparePluginVersions("0.9.0", "0.10.0") >= 0 {
		t.Fatalf("0.9.0 must sort before 0.10.0")
	}
}

func TestPluginSameNameDoesNotProveUsableMCP(t *testing.T) {
	fx := newFixture(t)
	options := fx.options()
	options.Name = DefaultName
	options.SkillName = DefaultName
	zcode, err := SelectHarnesses(fx.registry(), []string{"zcode"}, false)
	if err != nil {
		t.Fatal(err)
	}
	assertNoFailures(t, Install(zcode, options))
	pluginDir := setupZCodePluginCache(t, fx, "0.7.4")
	if _, err := writeText(filepath.Join(pluginDir, ".zcode-plugin", "plugin.json"), `{"name":"rolling-memory","mcpServers":{"rolling-memory":{"type":"stdio","command":"/missing/rolling-memory","args":["mcp"]}}}`); err != nil {
		t.Fatal(err)
	}
	data, err := loadJSONFile(fx.zcodeConfig())
	if err != nil {
		t.Fatal(err)
	}
	data, _ = jsonTableWith(data, []string{"plugins", "enabledPlugins"}, true, func(current ids.OrderedMap) ids.OrderedMap {
		return objectSet(current, "rolling-memory@marketplace", true)
	})
	config, err := dumpJSON(data)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := writeText(fx.zcodeConfig(), config); err != nil {
		t.Fatal(err)
	}
	plugin, found := activeNativePlugin(zcode[0], options)
	if !found || !plugin.HasMCP {
		t.Fatalf("plugin declaration and verification = %#v, found=%v", plugin, found)
	}
	Install(zcode, options)
	data, err = loadJSONFile(fx.zcodeConfig())
	if err != nil {
		t.Fatal(err)
	}
	if _, found := jsonLookup(data, []string{"mcp", "servers", options.Name}); !found {
		t.Fatal("unverified plugin removed the existing MCP registration")
	}
}

func TestPluginDeclarationWithoutRuntimeEvidence(t *testing.T) {
	fx := newFixture(t)
	options := fx.options()
	options.Name = DefaultName
	options.SkillName = DefaultName
	zcode, err := SelectHarnesses(fx.registry(), []string{"zcode"}, false)
	if err != nil {
		t.Fatal(err)
	}
	setupZCodePluginCache(t, fx, "0.7.4")
	if _, err := writeText(fx.zcodeConfig(), `{"plugins":{"enabledPlugins":{"rolling-memory@marketplace":true}}}`); err != nil {
		t.Fatal(err)
	}
	plugin, found := activeNativePlugin(zcode[0], options)
	if !found || !plugin.HasSkill || !plugin.HasMCP || !plugin.HasHooks {
		t.Fatalf("plugin evidence = %#v, found=%v", plugin, found)
	}
	if installed := InstalledHarnesses(zcode, options); len(installed) != 1 {
		t.Fatalf("enabled plugin declaration not discovered: %#v", installed)
	}
	for _, action := range Check(zcode, options, false).Actions {
		if (action.Component == "skill" || action.Component == "mcp" || action.Component == "hooks") && action.Status == StatusOK {
			t.Fatalf("unverified plugin reported healthy: %#v", action)
		}
	}
}

func TestPluginHooksRequireCommand(t *testing.T) {
	tests := []struct {
		name string
		data string
		want bool
	}{
		{"empty", `{"hooks":{}}`, false},
		{"empty event", `{"hooks":{"UserPromptSubmit":[]}}`, false},
		{"unrelated command", `{"hooks":{"UserPromptSubmit":[{"hooks":[{"type":"command","command":"other"}]}]}}`, false},
		{"disabled command", `{"hooks":{"UserPromptSubmit":[{"hooks":[{"type":"command","enabled":false,"command":"${CLAUDE_PLUGIN_ROOT}/bin/rolling-memory-hook recall"}]}]}}`, false},
		{"wrong type", `{"hooks":{"UserPromptSubmit":[{"hooks":[{"type":"prompt","command":"${CLAUDE_PLUGIN_ROOT}/bin/rolling-memory-hook recall"}]}]}}`, false},
		{"unrelated suffix", `{"hooks":{"UserPromptSubmit":[{"hooks":[{"type":"command","command":"${CLAUDE_PLUGIN_ROOT}/bin/rolling-memory-hook recall-extra"}]}]}}`, false},
		{"valid", `{"hooks":{"UserPromptSubmit":[{"hooks":[{"type":"command","command":"${CLAUDE_PLUGIN_ROOT}/bin/rolling-memory-hook recall"}]}]}}`, true},
		{"valid json envelope", `{"hooks":{"UserPromptSubmit":[{"hooks":[{"type":"command","command":"${CLAUDE_PLUGIN_ROOT}/bin/rolling-memory-hook recall --json"}]}]}}`, true},
		{"quoted path", `{"hooks":{"UserPromptSubmit":[{"hooks":[{"type":"command","command":"\"${CLAUDE_PLUGIN_ROOT}/bin/rolling-memory-hook\" recall --json"}]}]}}`, true},
		{"quoted whole command", `{"hooks":{"UserPromptSubmit":[{"hooks":[{"type":"command","command":"\"${CLAUDE_PLUGIN_ROOT}/bin/rolling-memory-hook recall --json\""}]}]}}`, false},
		{"quoted path with shell suffix", `{"hooks":{"UserPromptSubmit":[{"hooks":[{"type":"command","command":"\"${CLAUDE_PLUGIN_ROOT}/bin/rolling-memory-hook\" recall --json; other"}]}]}}`, false},
		{"json suffix on unrelated sub", `{"hooks":{"UserPromptSubmit":[{"hooks":[{"type":"command","command":"${CLAUDE_PLUGIN_ROOT}/bin/rolling-memory-hook start --json"}]}]}}`, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "hooks.json")
			if _, err := writeText(path, tt.data); err != nil {
				t.Fatal(err)
			}
			if _, err := writeText(filepath.Join(filepath.Dir(path), "bin", "rolling-memory-hook"), "#!/bin/sh\n"); err != nil {
				t.Fatal(err)
			}
			if got := pluginHasHooks(path, filepath.Dir(path)); got != tt.want {
				t.Fatalf("pluginHasHooks() = %v, want %v", got, tt.want)
			}
			if tt.want {
				if err := os.Remove(filepath.Join(filepath.Dir(path), "bin", "rolling-memory-hook")); err != nil {
					t.Fatal(err)
				}
				if pluginHasHooks(path, filepath.Dir(path)) {
					t.Fatal("missing launcher must not be considered a usable hook")
				}
			}
		})
	}
}

func TestZCodePluginPreservesExistingInstallUntilVerified(t *testing.T) {
	fx := newFixture(t)
	options := fx.options()
	options.Name = DefaultName
	options.SkillName = DefaultName
	options.Hooks = true

	// 1. First install via CLI (no plugin enabled yet) -> writes global skill, MCP and hooks.
	zcodeOnly, err := SelectHarnesses(fx.registry(), []string{"zcode"}, false)
	if err != nil {
		t.Fatalf("SelectHarnesses: %v", err)
	}
	assertNoFailures(t, Install(zcodeOnly, options))

	// 2. Now enable the self-contained ZCode plugin while the global wiring is still on disk.
	setupZCodePluginCache(t, fx, "0.7.4")
	data, err := loadJSONFile(fx.zcodeConfig())
	if err != nil {
		t.Fatalf("loadJSONFile: %v", err)
	}
	data, _ = jsonTableWith(data, []string{"plugins", "enabledPlugins"}, true, func(current ids.OrderedMap) ids.OrderedMap {
		return objectSet(current, "rolling-memory@marketplace", true)
	})
	rawCfg, err := dumpJSON(data)
	if err != nil {
		t.Fatalf("dumpJSON: %v", err)
	}
	if _, err := writeText(fx.zcodeConfig(), rawCfg); err != nil {
		t.Fatalf("writeText config.json: %v", err)
	}

	// 3. Check must detect the duplicate wiring across skill, mcp, and hooks.
	checkDup := Check(zcodeOnly, options, false)
	if len(checkDup.Failures()) != 3 {
		t.Fatalf("expected 3 duplicate conflicts (skill, mcp, hooks), got %d: %#v", len(checkDup.Failures()), checkDup.Failures())
	}
	for _, action := range checkDup.Failures() {
		if action.Status != StatusConflict || !strings.Contains(action.Detail, "duplicate") {
			t.Fatalf("unexpected conflict action: %#v", action)
		}
	}

	install := Install(zcodeOnly, options)
	assertPluginSurfacesPreserved(t, install, zcodeOnly[0], options, "skill", "mcp", "hooks")
	check := Check(zcodeOnly, options, false)
	if len(check.Failures()) == 0 {
		t.Fatalf("unverified duplicate surfaces require review: %#v", check.Actions)
	}
}

func assertPluginSurfacesPreserved(t *testing.T, report *Report, h Harness, options InstallOptions, components ...string) {
	t.Helper()
	for _, component := range components {
		found := false
		for _, action := range report.Actions {
			if action.Harness == h.ID && action.Component == component {
				if action.Status == StatusRemoved {
					t.Fatalf("unverified plugin removed %s: %#v", component, action)
				}
				if action.Status == StatusConflict {
					found = true
				}
			}
		}
		if !found {
			t.Fatalf("expected %s conflict for unverified plugin: %#v", component, report.Actions)
		}
	}
	for _, component := range components {
		switch component {
		case "skill":
			for _, path := range skillTargets(h, options) {
				if !fileExists(path) {
					t.Fatalf("existing skill removed: %s", path)
				}
			}
		case "mcp":
			for _, path := range mcpTargetPaths(h, options) {
				data, err := loadJSONFile(path)
				if err != nil {
					t.Fatal(err)
				}
				keys := append(append([]string{}, h.MCPKeys...), options.Name)
				if _, found := jsonLookup(data, keys); !found {
					t.Fatalf("existing MCP entry removed: %s", path)
				}
			}
		case "hooks":
			if !configHasGlobalHooks(h, options) {
				t.Fatal("existing hook removed")
			}
		}
	}
}

func TestEmptyPluginHooksKeepGlobalHook(t *testing.T) {
	fx := newFixture(t)
	options := fx.options()
	options.Hooks = true
	zcodeOnly, err := SelectHarnesses(fx.registry(), []string{"zcode"}, false)
	if err != nil {
		t.Fatal(err)
	}
	assertNoFailures(t, Install(zcodeOnly, options))
	pluginDir := setupZCodePluginCache(t, fx, "0.7.4")
	if _, err := writeText(filepath.Join(pluginDir, "hooks", "hooks.json"), `{"hooks":{}}`); err != nil {
		t.Fatal(err)
	}
	data, err := loadJSONFile(fx.zcodeConfig())
	if err != nil {
		t.Fatal(err)
	}
	data, _ = jsonTableWith(data, []string{"plugins", "enabledPlugins"}, true, func(current ids.OrderedMap) ids.OrderedMap {
		return objectSet(current, "rolling-memory@marketplace", true)
	})
	config, err := dumpJSON(data)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := writeText(fx.zcodeConfig(), config); err != nil {
		t.Fatal(err)
	}
	assertNoFailures(t, Install(zcodeOnly, options))
	if !configHasGlobalHooks(zcodeOnly[0], options) {
		t.Fatal("empty plugin hooks removed the existing global hook")
	}
	assertNoFailures(t, Check(zcodeOnly, options, false))
}

func TestClaudePluginPreservesExistingInstallUntilVerified(t *testing.T) {
	fx := newFixture(t)
	options := fx.options()
	options.Name = DefaultName
	options.SkillName = DefaultName
	options.Hooks = true

	claudeOnly, err := SelectHarnesses(fx.registry(), []string{"claude-code"}, false)
	if err != nil {
		t.Fatalf("SelectHarnesses: %v", err)
	}
	assertNoFailures(t, Install(claudeOnly, options))

	pluginDir := filepath.Join(fx.home, ".claude", "plugins", "cache", "marketplace", DefaultName, "0.7.4")
	if _, err := writeText(filepath.Join(pluginDir, ".claude-plugin", "plugin.json"), `{
  "name": "rolling-memory",
  "version": "0.7.4",
  "skills": "./skills",
  "hooks": "./hooks/hooks.json",
  "mcpServers": {
    "rolling-memory": {
      "type": "stdio",
      "command": "/bin/bash",
      "args": ["${CLAUDE_PLUGIN_ROOT}/bin/rolling-memory-mcp"]
    }
  }
}`); err != nil {
		t.Fatalf("writeText plugin.json: %v", err)
	}
	if _, err := writeText(filepath.Join(pluginDir, "skills", DefaultName, "SKILL.md"), "---\nname: "+DefaultName+"\ndescription: synthetic test skill\n---\n\n# body\n"); err != nil {
		t.Fatalf("writeText SKILL.md: %v", err)
	}
	if _, err := writeText(filepath.Join(pluginDir, "hooks", "hooks.json"), `{"hooks":{"UserPromptSubmit":[{"hooks":[{"type":"command","command":"${CLAUDE_PLUGIN_ROOT}/bin/rolling-memory-hook recall"}]}]}}`); err != nil {
		t.Fatalf("writeText hooks.json: %v", err)
	}

	if _, err := writeText(filepath.Join(pluginDir, "bin", "rolling-memory-hook"), "#!/bin/sh\n"); err != nil {
		t.Fatal(err)
	}

	settingsPath := filepath.Join(fx.home, ".claude", "settings.json")
	data, err := loadJSONFile(settingsPath)
	if err != nil {
		t.Fatalf("loadJSONFile: %v", err)
	}
	data, _ = jsonTableWith(data, []string{"enabledPlugins"}, true, func(current ids.OrderedMap) ids.OrderedMap {
		return objectSet(current, "rolling-memory@marketplace", true)
	})
	rawCfg, err := dumpJSON(data)
	if err != nil {
		t.Fatalf("dumpJSON: %v", err)
	}
	if _, err := writeText(settingsPath, rawCfg); err != nil {
		t.Fatalf("writeText settings.json: %v", err)
	}

	install := Install(claudeOnly, options)
	assertPluginSurfacesPreserved(t, install, claudeOnly[0], options, "skill", "mcp", "hooks")
}

func TestExtendedHarnessesCursorWindsurfCline(t *testing.T) {
	fx := newFixture(t)
	options := fx.options()
	ext, err := SelectHarnesses(fx.registry(), []string{"cursor", "windsurf", "cline"}, false)
	if err != nil {
		t.Fatalf("SelectHarnesses extended: %v", err)
	}
	if len(ext) != 3 {
		t.Fatalf("expected 3 extended harnesses, got %d", len(ext))
	}
	assertNoFailures(t, Install(ext, options))
	assertNoFailures(t, Check(ext, options, false))
	assertNoFailures(t, Uninstall(ext, options))
}

func TestExtendedHarnessesWorkBuddyAndDoubao(t *testing.T) {
	fx := newFixture(t)
	options := fx.options()
	ext, err := SelectHarnesses(fx.registry(), []string{"workbuddy", "doubao"}, false)
	if err != nil {
		t.Fatalf("SelectHarnesses extended: %v", err)
	}
	if len(ext) != 2 {
		t.Fatalf("expected 2 extended harnesses, got %d", len(ext))
	}
	report := Install(ext, options)
	assertNoFailures(t, report)

	// WorkBuddy: the host's user config is ~/.workbuddy/mcp.json (WorkBuddy's
	// own connector docs correct the earlier dotted reading); the dotted
	// ~/.workbuddy/.mcp.json is kept as the legacy carrier for host builds that
	// already read it. The runtime never expands shell variables, so the written
	// command must be an absolute, already-resolved path.
	wbConfig := filepath.Join(fx.home, ".workbuddy", "mcp.json")
	wbLegacyConfig := filepath.Join(fx.home, ".workbuddy", ".mcp.json")
	wbSkill := filepath.Join(fx.home, ".workbuddy", "skills", options.SkillName, "SKILL.md")
	if !fileExists(wbSkill) {
		t.Fatalf("workbuddy skill not written: %s", wbSkill)
	}
	for _, target := range []string{wbConfig, wbLegacyConfig} {
		data, err := loadJSONFile(target)
		if err != nil {
			t.Fatalf("load workbuddy mcp config %s: %v", target, err)
		}
		entry, found := jsonLookup(data, []string{"mcpServers", options.Name})
		if !found {
			t.Fatalf("workbuddy mcp entry missing in %s: %#v", target, data)
		}
		command := ""
		if object, ok := asObject(entry); ok {
			if value, ok := objectGet(object, "command"); ok {
				command, _ = value.(string)
			}
		}
		if !filepath.IsAbs(command) || strings.Contains(command, "${") || strings.Contains(command, "~") {
			t.Fatalf("workbuddy command %q is not an absolute resolved path", command)
		}
	}

	// Doubao: dual skill roots, and the MCP surface reports a truthful skip.
	doubaoSkills := []string{
		filepath.Join(fx.home, "Doubao", "skills", options.SkillName, "SKILL.md"),
		filepath.Join(fx.home, ".agents", "skills", options.SkillName, "SKILL.md"),
	}
	for _, target := range doubaoSkills {
		if !fileExists(target) {
			t.Fatalf("doubao skill root not written: %s", target)
		}
	}
	sawSkip := false
	for _, action := range report.Actions {
		if action.Harness == "doubao" && action.Component == "mcp" {
			if action.Status != StatusSkipped || !strings.Contains(action.Detail, "address-style connector") {
				t.Fatalf("doubao mcp action = %#v", action)
			}
			sawSkip = true
		}
	}
	if !sawSkip {
		t.Fatalf("doubao mcp action missing from install report: %#v", report.Actions)
	}

	assertNoFailures(t, Check(ext, options, false))
	assertNoFailures(t, Uninstall(ext, options))
	// Skill files are removed; the workbuddy config file stays on disk (it
	// belongs to the host) but must no longer carry our entry.
	for _, target := range append([]string{wbSkill}, doubaoSkills...) {
		if fileExists(target) {
			t.Fatalf("uninstall left %s", target)
		}
	}
	for _, target := range []string{wbConfig, wbLegacyConfig} {
		if remaining, err := loadJSONFile(target); err == nil {
			if _, found := jsonLookup(remaining, []string{"mcpServers", options.Name}); found {
				t.Fatalf("uninstall left the workbuddy mcp entry in %s: %#v", target, remaining)
			}
		}
	}
}

// TestWorkBuddyWritesHostReadPath pins the WorkBuddy write point to the user
// config path the host's own connector documentation names (mcp.json, no dot)
// and keeps the dotted legacy carrier alongside it.
func TestWorkBuddyWritesHostReadPath(t *testing.T) {
	fx := newFixture(t)
	options := fx.options()
	workbuddy, err := SelectHarnesses(fx.registry(), []string{"workbuddy"}, false)
	if err != nil {
		t.Fatalf("SelectHarnesses: %v", err)
	}
	if len(workbuddy) != 1 {
		t.Fatalf("expected 1 workbuddy harness, got %d", len(workbuddy))
	}
	harness := workbuddy[0]
	wantPrimary := filepath.Join(fx.home, ".workbuddy", "mcp.json")
	legacyPath := filepath.Join(fx.home, ".workbuddy", ".mcp.json")
	if harness.MCPPath != wantPrimary {
		t.Fatalf("workbuddy MCPPath = %q, want %q", harness.MCPPath, wantPrimary)
	}
	if got, ok := mcpTargetPath(harness, options); !ok || got != wantPrimary {
		t.Fatalf("mcpTargetPath = %q (ok=%v), want %q", got, ok, wantPrimary)
	}
	paths := mcpTargetPaths(harness, options)
	if len(paths) != 2 || paths[0] != wantPrimary || paths[1] != legacyPath {
		t.Fatalf("mcpTargetPaths = %#v", paths)
	}
	assertNoFailures(t, Install(workbuddy, options))
	for _, target := range paths {
		data, err := loadJSONFile(target)
		if err != nil {
			t.Fatalf("host read path not written %s: %v", target, err)
		}
		if _, found := jsonLookup(data, []string{"mcpServers", options.Name}); !found {
			t.Fatalf("no mcp entry in %s: %#v", target, data)
		}
	}
	assertNoFailures(t, Uninstall(workbuddy, options))
}

// TestWorkBuddyNeverClobbersHostEntries pins the second nail: install and
// uninstall merge into the WorkBuddy files and remove only entries recognised
// as ours, so whatever the host itself keeps there (e.g. its connector-proxy
// placeholder) is preserved.
func TestWorkBuddyNeverClobbersHostEntries(t *testing.T) {
	fx := newFixture(t)
	options := fx.options()
	workbuddy, err := SelectHarnesses(fx.registry(), []string{"workbuddy"}, false)
	if err != nil {
		t.Fatalf("SelectHarnesses: %v", err)
	}
	primaryPath := filepath.Join(fx.home, ".workbuddy", "mcp.json")
	legacyPath := filepath.Join(fx.home, ".workbuddy", ".mcp.json")
	hostEntries := map[string]string{
		primaryPath: `{"mcpServers":{"connector-proxy":{"type":"stdio","command":"/host/connector-proxy"}}}`,
		legacyPath:  `{"connector-proxy":{"type":"stdio","command":"/host/connector-proxy"}}`,
	}
	for path, text := range hostEntries {
		if _, err := writeText(path, text); err != nil {
			t.Fatalf("seed host entry %s: %v", path, err)
		}
	}
	assertNoFailures(t, Install(workbuddy, options))
	for path := range hostEntries {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		if !strings.Contains(string(raw), "connector-proxy") {
			t.Fatalf("install clobbered the host entry in %s: %s", path, raw)
		} // The legacy file is the host's own: its root-level connector table must
		// survive the merge while our entry lands under mcpServers.
		if path == legacyPath {
			data, err := loadJSONFile(path)
			if err != nil {
				t.Fatalf("parse %s: %v", path, err)
			}
			hostEntry, found := jsonLookup(data, []string{"connector-proxy", "command"})
			if !found || pyString(hostEntry) != "/host/connector-proxy" {
				t.Fatalf("host connector row lost in %s: %#v", path, data)
			}
			if _, found := jsonLookup(data, []string{"mcpServers", options.Name}); !found {
				t.Fatalf("our entry missing from %s: %#v", path, data)
			}
		}
	}
	assertNoFailures(t, Uninstall(workbuddy, options))
	for path := range hostEntries {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read after uninstall %s: %v", path, err)
		}
		if !strings.Contains(string(raw), "connector-proxy") {
			t.Fatalf("uninstall clobbered the host entry in %s: %s", path, raw)
		}
	}
}

// WorkBuddy plugin declarations do not prove an existing registration is replaceable.
func TestWorkBuddyPluginPreservesExistingInstall(t *testing.T) {
	fx := newFixture(t)
	options := fx.options()
	options.Name = DefaultName
	options.SkillName = DefaultName

	workbuddy, err := SelectHarnesses(fx.registry(), []string{"workbuddy"}, false)
	if err != nil {
		t.Fatalf("SelectHarnesses: %v", err)
	}
	assertNoFailures(t, Install(workbuddy, options))

	pluginDir := filepath.Join(fx.home, ".workbuddy", "plugins", "cache", "marketplace", DefaultName, "0.7.4")
	if _, err := writeText(filepath.Join(pluginDir, ".codebuddy-plugin", "plugin.json"), `{
  "name": "rolling-memory",
  "version": "0.7.4",
  "skills": ["./skills/rolling-memory"]
}`); err != nil {
		t.Fatalf("writeText plugin.json: %v", err)
	}
	if _, err := writeText(filepath.Join(pluginDir, "skills", DefaultName, "SKILL.md"), "---\nname: "+DefaultName+"\ndescription: synthetic test skill\n---\n\n# body\n"); err != nil {
		t.Fatalf("writeText SKILL.md: %v", err)
	}
	if _, err := writeText(filepath.Join(pluginDir, ".mcp.json"), `{"mcpServers":{"rolling-memory":{"type":"stdio","command":"/bin/sh","args":["-c","true"]}}}`); err != nil {
		t.Fatalf("writeText .mcp.json: %v", err)
	}
	if _, err := writeText(filepath.Join(fx.home, ".workbuddy", "settings.json"), `{"enabledPlugins":{"rolling-memory@marketplace":true}}`); err != nil {
		t.Fatalf("writeText settings.json: %v", err)
	}

	install := Install(workbuddy, options)
	assertPluginSurfacesPreserved(t, install, workbuddy[0], options, "skill", "mcp")
}

// The same preservation rule applies to directory-marketplace plugins.
func TestWorkBuddyMarketplacePluginPreservesExistingInstall(t *testing.T) {
	fx := newFixture(t)
	options := fx.options()
	options.Name = DefaultName
	options.SkillName = DefaultName

	workbuddy, err := SelectHarnesses(fx.registry(), []string{"workbuddy"}, false)
	if err != nil {
		t.Fatalf("SelectHarnesses: %v", err)
	}
	assertNoFailures(t, Install(workbuddy, options))

	marketDir := filepath.Join(fx.home, ".workbuddy", "plugins", "marketplaces", "rolling-memory-plugins")
	if _, err := writeText(filepath.Join(marketDir, ".codebuddy-plugin", "marketplace.json"), `{
  "name": "rolling-memory-plugins",
  "plugins": [
    {"name": "rolling-memory", "source": "."}
  ]
}`); err != nil {
		t.Fatalf("writeText marketplace.json: %v", err)
	}
	if _, err := writeText(filepath.Join(marketDir, ".codebuddy-plugin", "plugin.json"), `{
  "name": "rolling-memory",
  "version": "0.7.4",
  "skills": ["./skills/rolling-memory"]
}`); err != nil {
		t.Fatalf("writeText plugin.json: %v", err)
	}
	if _, err := writeText(filepath.Join(marketDir, "skills", DefaultName, "SKILL.md"), "---\nname: "+DefaultName+"\ndescription: synthetic test skill\n---\n\n# body\n"); err != nil {
		t.Fatalf("writeText SKILL.md: %v", err)
	}
	if _, err := writeText(filepath.Join(marketDir, ".mcp.json"), `{"mcpServers":{"rolling-memory":{"type":"stdio","command":"node","args":["-e","0"]}}}`); err != nil {
		t.Fatalf("writeText .mcp.json: %v", err)
	}
	if _, err := writeText(filepath.Join(fx.home, ".workbuddy", "settings.json"), `{"enabledPlugins":{"rolling-memory@rolling-memory-plugins":true}}`); err != nil {
		t.Fatalf("writeText settings.json: %v", err)
	}

	install := Install(workbuddy, options)
	assertPluginSurfacesPreserved(t, install, workbuddy[0], options, "skill", "mcp")
}

// TestRepositoryPluginBundleHooksContract checks the shipped hook declaration;
// it does not assert that a host loaded or ran the hook.
func TestRepositoryPluginBundleHooksContract(t *testing.T) {
	repoRoot := filepath.Join("..", "..")
	hookPath := filepath.Join(repoRoot, "hooks", "hooks.json")
	if !fileExists(hookPath) {
		t.Fatalf("bundle hooks manifest missing: %s", hookPath)
	}
	if !fileExists(filepath.Join(repoRoot, "bin", "rolling-memory-hook")) {
		t.Fatal("bundle hook launcher missing: bin/rolling-memory-hook")
	}
	if !pluginHasHooks(hookPath, repoRoot) {
		t.Fatalf("pluginHasHooks must recognise the shipped hooks manifest at %s", hookPath)
	}
}

// TestRepositoryPluginBundleMCPContract pins the shipped MCP wiring: the bundle
// .mcp.json must not depend on host variable expansion (the CodeBuddy-family
// main process expands nothing), must run through the host-managed Node
// runtime, and its inline locator must hand off to the bundled JS launcher.
func TestRepositoryPluginBundleMCPContract(t *testing.T) {
	repoRoot := filepath.Join("..", "..")
	launcher := filepath.Join(repoRoot, "bin", "rolling-memory-mcp.js")
	if !fileExists(launcher) {
		t.Fatalf("bundle node launcher missing: %s", launcher)
	}
	data, err := loadJSONFile(filepath.Join(repoRoot, ".mcp.json"))
	if err != nil {
		t.Fatalf("load bundle .mcp.json: %v", err)
	}
	entry, found := jsonLookup(data, []string{"mcpServers", DefaultName})
	if !found {
		t.Fatalf("bundle .mcp.json misses the %s server entry: %#v", DefaultName, data)
	}
	object, ok := asObject(entry)
	if !ok {
		t.Fatalf("bundle server entry is not an object: %#v", entry)
	}
	command, _ := objectGet(object, "command")
	if command != "node" {
		t.Fatalf("bundle command = %#v, want node (host-managed runtime)", command)
	}
	if runtimeValue, ok := objectGet(object, "runtime"); !ok {
		t.Fatal("bundle entry must request the host-managed node runtime")
	} else if runtimeObject, isObject := asObject(runtimeValue); !isObject {
		t.Fatalf("bundle runtime field is not an object: %#v", runtimeValue)
	} else if runtimeType, _ := objectGet(runtimeObject, "type"); runtimeType != "node" {
		t.Fatalf("bundle runtime.type = %#v, want node", runtimeType)
	}
	argsValue, _ := objectGet(object, "args")
	args, ok := asArray(argsValue)
	if !ok || len(args) != 2 {
		t.Fatalf("bundle args = %#v, want exactly [-e, <inline locator>]", argsValue)
	}
	if first, _ := args[0].(string); first != "-e" {
		t.Fatalf("bundle args[0] = %#v, want -e", args[0])
	}
	script, _ := args[1].(string)
	if strings.Contains(script, "${") {
		t.Fatalf("bundle inline locator must not depend on host variable expansion: %s", script)
	}
	if !strings.Contains(script, "rolling-memory-mcp.js") {
		t.Fatalf("bundle inline locator must hand off to the bundled node launcher: %s", script)
	}
}

func TestNodeLauncherMissingBinaryDoesNotDownload(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed")
	}
	launcher, err := filepath.Abs(filepath.Join("..", "..", "bin", "rolling-memory-mcp.js"))
	if err != nil {
		t.Fatal(err)
	}
	var requests atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()
	home := t.TempDir()
	cmd := exec.Command(node, launcher)
	cmd.Env = []string{
		"HOME=" + home,
		"USERPROFILE=" + home,
		"XDG_DATA_HOME=" + filepath.Join(home, "xdg"),
		"LOCALAPPDATA=" + filepath.Join(home, "appdata"),
		"PATH=" + os.Getenv("PATH"),
		"ROLLING_MEMORY_RELEASE_BASE=" + srv.URL,
	}
	out, err := cmd.CombinedOutput()
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) || exitErr.ExitCode() != 127 {
		t.Fatalf("expected exit 127, got %v: %s", err, out)
	}
	if !strings.Contains(string(out), "no usable local Rolling-Memory binary found") {
		t.Fatalf("missing diagnostic: %s", out)
	}
	if got := requests.Load(); got != 0 {
		t.Fatalf("launcher made %d release requests", got)
	}
}

// TestInstallScriptNonDevEnvironmentContract verifies install.sh in an isolated
// environment with NO go and NO git on PATH:
//  1. Verified prebuilt install + automatic idempotent ~/.zshrc PATH persistence
//     + clean exit 0 when no harness is detected yet;
//  2. Re-running does not duplicate the PATH marker block;
//  3. Tampered binary SHA256 is rejected fail-closed and never written to disk;
//  4. Unreachable release channel with no git/go prints a clear end-user
//     diagnostic instead of a misleading toolchain error.
func TestInstallScriptNonDevEnvironmentContract(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("install.sh contract test runs on POSIX platforms")
	}
	repoRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	installSh := filepath.Join(repoRoot, "install.sh")

	// Build a minimal isolated PATH directory without `go` or `git`.
	sysBin := filepath.Join(t.TempDir(), "sysbin")
	if err := os.MkdirAll(sysBin, 0o755); err != nil {
		t.Fatal(err)
	}
	tools := []string{
		"bash", "sh", "uname", "tr", "mkdir", "chmod", "mv", "rm", "ln",
		"cat", "grep", "awk", "mktemp", "dirname", "basename", "curl", "shasum", "sha256sum",
	}
	for _, name := range tools {
		if resolved, lookErr := exec.LookPath(name); lookErr == nil {
			_ = os.Symlink(resolved, filepath.Join(sysBin, name))
		}
	}

	assetName := fmt.Sprintf("rolling-memory-%s-%s", runtime.GOOS, runtime.GOARCH)
	validBody := []byte("#!/bin/sh\nif [ \"$1\" = \"init\" ]; then exit 0; fi\nif [ \"$1\" = \"install\" ]; then echo \"no harness detected\" >&2; exit 1; fi\nexit 0\n")
	validSum := sha256.Sum256(validBody)
	validHex := hex.EncodeToString(validSum[:])

	releaseDir := filepath.Join(t.TempDir(), "release")
	if err := os.MkdirAll(releaseDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(releaseDir, assetName), validBody, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(releaseDir, "SHA256SUMS"), []byte(fmt.Sprintf("%s  %s\n", validHex, assetName)), 0o644); err != nil {
		t.Fatal(err)
	}

	// Copy install.sh into an isolated directory (not inside the repo checkout)
	// so it behaves exactly like `curl ... | bash` on a user machine.
	standaloneInstallSh := filepath.Join(t.TempDir(), "install.sh")
	rawInstall, err := os.ReadFile(installSh)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(standaloneInstallSh, rawInstall, 0o755); err != nil {
		t.Fatal(err)
	}

	home1 := filepath.Join(t.TempDir(), "home1")
	if err := os.MkdirAll(home1, 0o755); err != nil {
		t.Fatal(err)
	}
	env1 := []string{
		"HOME=" + home1,
		"SHELL=/bin/zsh",
		"PATH=" + sysBin,
		"ROLLING_MEMORY_RELEASE_BASE=file://" + releaseDir,
	}

	// Phase 1 & 2: Valid install + idempotent PATH block in ~/.zshrc
	for run := 1; run <= 2; run++ {
		cmd := exec.Command(filepath.Join(sysBin, "bash"), standaloneInstallSh)
		cmd.Env = env1
		out, runErr := cmd.CombinedOutput()
		if runErr != nil {
			t.Fatalf("run %d failed: %v\n%s", run, runErr, string(out))
		}
	}
	zshrcBytes, err := os.ReadFile(filepath.Join(home1, ".zshrc"))
	if err != nil {
		t.Fatalf("expected ~/.zshrc to be created with PATH entry: %v", err)
	}
	if got := strings.Count(string(zshrcBytes), "# >>> rolling-memory PATH >>>"); got != 1 {
		t.Fatalf("expected exactly 1 PATH block in ~/.zshrc after 2 runs, got %d:\n%s", got, string(zshrcBytes))
	}

	// Phase 3: Tampered binary rejected fail-closed
	tamperedDir := filepath.Join(t.TempDir(), "tampered")
	if err := os.MkdirAll(tamperedDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tamperedDir, assetName), append(validBody, []byte("# tampered\n")...), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tamperedDir, "SHA256SUMS"), []byte(fmt.Sprintf("%s  %s\n", validHex, assetName)), 0o644); err != nil {
		t.Fatal(err)
	}
	home3 := filepath.Join(t.TempDir(), "home3")
	if err := os.MkdirAll(home3, 0o755); err != nil {
		t.Fatal(err)
	}
	cmd3 := exec.Command(filepath.Join(sysBin, "bash"), standaloneInstallSh)
	cmd3.Env = []string{
		"HOME=" + home3,
		"SHELL=/bin/zsh",
		"PATH=" + sysBin,
		"ROLLING_MEMORY_RELEASE_BASE=file://" + tamperedDir,
	}
	out3, err3 := cmd3.CombinedOutput()
	if err3 == nil {
		t.Fatalf("expected tampered binary to fail, but succeeded:\n%s", string(out3))
	}
	if !strings.Contains(string(out3), "SHA256 mismatch") {
		t.Fatalf("expected SHA256 mismatch error, got:\n%s", string(out3))
	}
	if fileExists(filepath.Join(home3, ".local", "share", "rolling-memory", "bin", "rolling-memory")) {
		t.Fatal("tampered binary must not be installed to disk")
	}

	// Phase 4: Unreachable channel + no Go/Git prints friendly non-dev guidance
	home4 := filepath.Join(t.TempDir(), "home4")
	if err := os.MkdirAll(home4, 0o755); err != nil {
		t.Fatal(err)
	}
	cmd4 := exec.Command(filepath.Join(sysBin, "bash"), standaloneInstallSh)
	cmd4.Env = []string{
		"HOME=" + home4,
		"SHELL=/bin/zsh",
		"PATH=" + sysBin,
		"ROLLING_MEMORY_RELEASE_BASE=off",
		"ROLLING_MEMORY_RELEASE_MIRROR=off",
	}
	out4, err4 := cmd4.CombinedOutput()
	if err4 == nil {
		t.Fatalf("expected offline no-toolchain run to fail, got:\n%s", string(out4))
	}
	if !strings.Contains(string(out4), "普通用户（免装 Git/Go）") {
		t.Fatalf("expected end-user friendly guidance in output, got:\n%s", string(out4))
	}
}

func TestDedupeKeepsWorkingMCPWhenPluginCommandUnavailable(t *testing.T) {
	fx := newFixture(t)
	options := fx.options()
	options.Name = DefaultName
	options.SkillName = DefaultName
	zcode, err := SelectHarnesses(fx.registry(), []string{"zcode"}, false)
	if err != nil {
		t.Fatal(err)
	}
	assertNoFailures(t, Install(zcode, options))
	pluginDir := setupZCodePluginCache(t, fx, "0.7.4")
	if _, err := writeText(filepath.Join(pluginDir, ".zcode-plugin", "plugin.json"),
		`{"name":"rolling-memory","mcpServers":{"rolling-memory":{"type":"stdio","command":"/missing/rolling-memory","args":["mcp"]}}}`); err != nil {
		t.Fatal(err)
	}
	enableZCodePlugin(t, fx)
	options.Dedupe = true
	report := Install(zcode, options)
	action := firstAction(t, report, "zcode", "mcp")
	if action.Status != StatusConflict || !strings.Contains(action.Detail, "unavailable") {
		t.Fatalf("dedupe with an unusable plugin command = %#v", action)
	}
	data, err := loadJSONFile(fx.zcodeConfig())
	if err != nil {
		t.Fatal(err)
	}
	if _, found := jsonLookup(data, []string{"mcp", "servers", DefaultName}); !found {
		t.Fatalf("working MCP entry was removed for an unusable plugin command")
	}
}

func TestPluginSkillMustMatchReleaseBeforeDedupe(t *testing.T) {
	t.Run("check reports an old plugin copy", func(t *testing.T) {
		fx := newFixture(t)
		options := fx.options()
		options.Name = DefaultName
		options.SkillName = DefaultName
		zcode, err := SelectHarnesses(fx.registry(), []string{"zcode"}, false)
		if err != nil {
			t.Fatal(err)
		}
		pluginDir := setupZCodePluginCache(t, fx, "0.7.4")
		enableZCodePlugin(t, fx)
		if _, err := writeText(filepath.Join(pluginDir, "skills", DefaultName, "SKILL.md"),
			"---\nname: "+DefaultName+"\ndescription: old plugin skill\n---\n"); err != nil {
			t.Fatal(err)
		}
		action := firstAction(t, Check(zcode, options, false), "zcode", "skill")
		if action.Status != StatusOutdated || !strings.Contains(action.Detail, "plugin") {
			t.Fatalf("old plugin skill check = %#v", action)
		}
	})
	t.Run("dedupe keeps the current CLI copy", func(t *testing.T) {
		fx := newFixture(t)
		options := fx.options()
		options.Name = DefaultName
		options.SkillName = DefaultName
		zcode, err := SelectHarnesses(fx.registry(), []string{"zcode"}, false)
		if err != nil {
			t.Fatal(err)
		}
		assertNoFailures(t, Install(zcode, options))
		pluginDir := setupZCodePluginCache(t, fx, "0.7.4")
		enableZCodePlugin(t, fx)
		if _, err := writeText(filepath.Join(pluginDir, "skills", DefaultName, "SKILL.md"),
			"---\nname: "+DefaultName+"\ndescription: old plugin skill\n---\n"); err != nil {
			t.Fatal(err)
		}
		options.Dedupe = true
		action := firstAction(t, Install(zcode, options), "zcode", "skill")
		if action.Status != StatusConflict || !strings.Contains(action.Detail, "differs") {
			t.Fatalf("dedupe with an old plugin skill = %#v", action)
		}
		if !fileExists(filepath.Join(fx.home, ".zcode", "skills", DefaultName, "SKILL.md")) {
			t.Fatalf("dedupe removed the CLI skill while the plugin copy was old")
		}
	})
}
