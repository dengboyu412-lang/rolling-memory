// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package mcp

import (
	"strings"
	"testing"
)

// http.Error handlers answer with plain text, and that body is the only
// diagnostic a non-200 graph/hydrate reply carries.
func TestUnixDaemonStatusErrorKeepsPlainTextBody(t *testing.T) {
	err := unixDaemonStatusError("/v1/graph", 503, []byte("localvault: empty corpus snapshot\n"))
	if err == nil {
		t.Fatal("expected an error")
	}
	message := err.Error()
	if !strings.Contains(message, "status 503") {
		t.Fatalf("status missing from %q", message)
	}
	if !strings.Contains(message, "localvault: empty corpus snapshot") {
		t.Fatalf("plain-text detail missing from %q", message)
	}
}

func TestUnixDaemonStatusErrorPrefersJSONEnvelope(t *testing.T) {
	err := unixDaemonStatusError("/v1/recall", 500, []byte(`{"error":"engine offline"}`))
	if err == nil || !strings.Contains(err.Error(), "engine offline") {
		t.Fatalf("JSON envelope not surfaced: %v", err)
	}
}

func TestUnixDaemonStatusErrorBoundsPlainText(t *testing.T) {
	err := unixDaemonStatusError("/v1/graph", 503, []byte(strings.Repeat("x", 4096)))
	if err == nil {
		t.Fatal("expected an error")
	}
	if runes := len([]rune(err.Error())); runes > 400 {
		t.Fatalf("error text not bounded: %d runes", runes)
	}
}

func TestUnixDaemonStatusErrorBareStatus(t *testing.T) {
	err := unixDaemonStatusError("/v1/graph", 503, nil)
	if err == nil || err.Error() != "daemon /v1/graph failed with status 503" {
		t.Fatalf("bare status shape changed: %v", err)
	}
}
