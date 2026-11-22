// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

// Package updatechannel defines the closed set of release channels used by the
// CLI, the web console, and the optional scheduled check.
package updatechannel

import (
	"fmt"
	"strings"
)

const (
	Stable = "stable"
	Beta   = "beta"
	Dev    = "dev"

	// AutoOff never checks for updates in the background.
	AutoOff = "off"
	// AutoNotify checks in the background and notifies; installation stays a
	// deliberate user action.
	AutoNotify = "notify"
	// AutoApply checks in the background and installs verified updates.
	AutoApply = "apply"

	// Default is the channel used when no explicit preference is saved.
	// Published installs follow stable releases; a source checkout is switched
	// to dev by the installer so developers keep tracking main.
	Default = Stable
	// AutoDefault leaves installation to the user while keeping the shared
	// update snapshot fresh.
	AutoDefault = AutoNotify
)

var valid = []string{Stable, Beta, Dev}
var validAuto = []string{AutoOff, AutoNotify, AutoApply}

// Normalize returns the canonical channel name.
func Normalize(raw string) (string, error) {
	value := strings.ToLower(strings.TrimSpace(raw))
	if value == "" {
		return Default, nil
	}
	for _, candidate := range valid {
		if value == candidate {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("update channel must be one of stable, beta, dev; got %q", raw)
}

// NormalizeAuto returns the canonical background update policy.
func NormalizeAuto(raw string) (string, error) {
	value := strings.ToLower(strings.TrimSpace(raw))
	if value == "" {
		return AutoDefault, nil
	}
	for _, candidate := range validAuto {
		if value == candidate {
			return value, nil
		}
	}
	return "", fmt.Errorf("update auto mode must be one of off, notify, apply; got %q", raw)
}
