// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestUpdateChannelFromEnvAndConfig(t *testing.T) {
	t.Setenv("ROLLING_MEMORY_UPDATE_CHANNEL", "beta")
	t.Setenv("ROLLING_MEMORY_UPDATE_SOURCE", "")
	cfg, err := Load(LoadOptions{})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Update.Channel != "beta" {
		t.Fatalf("env channel = %q, want beta", cfg.Update.Channel)
	}

	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte("[update]\nchannel = \"dev\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ROLLING_MEMORY_UPDATE_CHANNEL", "")
	cfg, err = Load(LoadOptions{Path: &path})
	if err != nil {
		t.Fatalf("Load file: %v", err)
	}
	if cfg.Update.Channel != "dev" {
		t.Fatalf("file channel = %q, want dev", cfg.Update.Channel)
	}
	if cfg.Update.Source != "github" {
		t.Fatalf("default source = %q, want github", cfg.Update.Source)
	}
}

func TestUpdateAutoFromEnvAndConfig(t *testing.T) {
	t.Setenv("ROLLING_MEMORY_UPDATE_AUTO", "apply")
	cfg, err := Load(LoadOptions{})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Update.Auto != "apply" {
		t.Fatalf("env auto = %q, want apply", cfg.Update.Auto)
	}

	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte("[update]\nauto = \"off\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ROLLING_MEMORY_UPDATE_AUTO", "")
	cfg, err = Load(LoadOptions{Path: &path})
	if err != nil {
		t.Fatalf("Load file: %v", err)
	}
	if cfg.Update.Auto != "off" {
		t.Fatalf("file auto = %q, want off", cfg.Update.Auto)
	}

	if _, err := SetConfigValue(path, "update.auto", "ALWAYS"); err == nil {
		t.Fatal("invalid auto mode must be rejected")
	}
	if _, err := SetConfigValue(path, "update.auto", "APPLY"); err != nil {
		t.Fatalf("SetConfigValue auto: %v", err)
	}
	cfg, err = Load(LoadOptions{Path: &path})
	if err != nil {
		t.Fatalf("Load after set: %v", err)
	}
	if cfg.Update.Auto != "apply" {
		t.Fatalf("auto after set = %q, want apply", cfg.Update.Auto)
	}
}

func TestSetUpdateSourceValidatesAndNormalizes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	if _, err := SetConfigValue(path, "update.source", "GITEE"); err != nil {
		t.Fatalf("SetConfigValue: %v", err)
	}
	cfg, err := Load(LoadOptions{Path: &path})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Update.Source != "gitee" {
		t.Fatalf("source = %q, want gitee", cfg.Update.Source)
	}
	if _, err := SetConfigValue(path, "update.source", "gitlab"); err == nil {
		t.Fatal("invalid source must be rejected")
	}
}

func TestUpdateSourceFromEnvAndConfig(t *testing.T) {
	t.Setenv("ROLLING_MEMORY_UPDATE_SOURCE", "gitee")
	cfg, err := Load(LoadOptions{})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Update.Source != "gitee" {
		t.Fatalf("env source = %q, want gitee", cfg.Update.Source)
	}

	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte("[update]\nsource = \"gitee\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ROLLING_MEMORY_UPDATE_SOURCE", "")
	cfg, err = Load(LoadOptions{Path: &path})
	if err != nil {
		t.Fatalf("Load file: %v", err)
	}
	if cfg.Update.Source != "gitee" {
		t.Fatalf("file source = %q, want gitee", cfg.Update.Source)
	}

	path = filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte("[update]\nsource = \"gitlab\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(LoadOptions{Path: &path}); err == nil {
		t.Fatal("unknown update source must be rejected")
	}
}
