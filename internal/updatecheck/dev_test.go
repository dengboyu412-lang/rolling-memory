// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package updatecheck

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime/debug"
	"testing"
	"time"

	"github.com/rocareer/rolling-memory/internal/updatechannel"
)

func TestCheckDevChannelTracksMainCommit(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/repos/acme/rolling-memory/commits/main" {
			_, _ = w.Write([]byte(`{"sha":"abcdef1234567890","commit":{"message":"head","author":{"date":"2026-11-19T00:00:00Z"}}}`))
			return
		}
		http.NotFound(w, r)
	}))
	defer server.Close()

	data := t.TempDir()
	t.Setenv("XDG_DATA_HOME", data)
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	installDir := filepath.Join(data, "rolling-memory")
	if err := os.MkdirAll(installDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(installDir, "install.json"), []byte(`{"method":"source","commit":"oldcommit"}`), 0o644); err != nil {
		t.Fatal(err)
	}

	result, err := Check(context.Background(), Options{
		Channel:        updatechannel.Dev,
		APIBase:        server.URL,
		Repo:           "acme/rolling-memory",
		CurrentVersion: "0.0.6",
		Timeout:        2 * time.Second,
	})
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if result.Channel != updatechannel.Dev || result.LatestCommitShort != "abcdef123456" {
		t.Fatalf("unexpected dev result: %#v", result.State)
	}
	if !result.UpdateAvailable || result.Installable {
		t.Fatalf("dev update flags = %#v", result.State)
	}

	if err := os.WriteFile(filepath.Join(installDir, "install.json"), []byte(`{"method":"source","commit":"abcdef1234567890"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	current, err := Check(context.Background(), Options{
		Channel:        updatechannel.Dev,
		APIBase:        server.URL,
		Repo:           "acme/rolling-memory",
		CurrentVersion: "0.0.6",
		Timeout:        2 * time.Second,
	})
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if current.UpdateAvailable {
		t.Fatalf("matching main commit must be up to date: %#v", current.State)
	}
}

func TestDevWithoutSourceCommitDoesNotHitTheNetwork(t *testing.T) {
	data := t.TempDir()
	t.Setenv("XDG_DATA_HOME", data)
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	result, err := Check(context.Background(), Options{
		Channel:        updatechannel.Dev,
		APIBase:        "http://127.0.0.1:1",
		Repo:           "acme/rolling-memory",
		CurrentVersion: "0.0.6",
		Timeout:        time.Second,
	})
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if !result.NeedsSourceBaseline || result.UpdateAvailable {
		t.Fatalf("expected baseline-only state: %#v", result.State)
	}
	if result.Source != "baseline-required" {
		t.Fatalf("source = %q, want baseline-required", result.Source)
	}
}

func TestCheckDevChannelUsesCommitDateToAvoidAheadFalsePositive(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/repos/acme/rolling-memory/commits/main" {
			_, _ = w.Write([]byte(`{"sha":"abcdef1234567890","commit":{"message":"head","author":{"date":"2026-11-19T00:00:00Z"}}}`))
			return
		}
		http.NotFound(w, r)
	}))
	defer server.Close()

	data := t.TempDir()
	t.Setenv("XDG_DATA_HOME", data)
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	installDir := filepath.Join(data, "rolling-memory")
	if err := os.MkdirAll(installDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeMetadata := func(commitDate string) {
		raw := `{"method":"source","commit":"localcommit","commit_date":"` + commitDate + `"}`
		if err := os.WriteFile(filepath.Join(installDir, "install.json"), []byte(raw), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	check := func() Result {
		result, err := Check(context.Background(), Options{
			Channel:        updatechannel.Dev,
			APIBase:        server.URL,
			Repo:           "acme/rolling-memory",
			CurrentVersion: "0.0.6",
			Timeout:        2 * time.Second,
		})
		if err != nil {
			t.Fatalf("Check: %v", err)
		}
		return result
	}

	// 本地提交时间晚于 main：这是本地领先，不应提示“可更新”。
	writeMetadata("2026-11-20T00:00:00Z")
	if result := check(); result.UpdateAvailable {
		t.Fatalf("ahead local commit must not be reported as update: %#v", result.State)
	}

	// 本地提交时间早于 main：确实落后，必须提示。
	writeMetadata("2026-11-18T00:00:00Z")
	if result := check(); !result.UpdateAvailable {
		t.Fatalf("behind local commit must be reported as update: %#v", result.State)
	}
}

func TestVCSFromBuildInfo(t *testing.T) {
	info := &debug.BuildInfo{Settings: []debug.BuildSetting{
		{Key: "vcs.revision", Value: " abcdef123456 "},
		{Key: "vcs.time", Value: "2026-11-22T06:31:23Z"},
	}}
	revision, commitTime := vcsFromBuildInfo(info)
	if revision != "abcdef123456" || commitTime != "2026-11-22T06:31:23Z" {
		t.Fatalf("vcsFromBuildInfo = (%q, %q)", revision, commitTime)
	}
	if revision, commitTime := vcsFromBuildInfo(nil); revision != "" || commitTime != "" {
		t.Fatalf("nil build info must be empty, got (%q, %q)", revision, commitTime)
	}
}
