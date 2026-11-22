// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package cli

import (
	"fmt"
	"strings"

	"github.com/rocareer/rolling-memory/internal/updatecheck"
	"github.com/rocareer/rolling-memory/internal/updatesource"
)

// runUpdateSchedule manages the optional user-level periodic update job. The
// job runs `update --auto`, which follows the saved background policy: notify,
// apply, or skip when automatic updates are off.
func (a *application) runUpdateSchedule(action string) int {
	action = strings.ToLower(strings.TrimSpace(action))
	var (
		status updatecheck.ScheduleStatus
		err    error
	)
	switch action {
	case "install", "enable":
		base := resolveReleaseBase(a.args.String("release_base"))
		channel := updatecheck.ResolveChannel(a.args.String("channel"))
		if base == "" && channel != "dev" {
			return a.fail(fmt.Errorf("release channel is disabled (ROLLING_MEMORY_RELEASE_BASE is off); scheduled checks would have nothing to query"))
		}
		// The official release bases are resolved from update.source at run
		// time. Embedding one of them here would freeze the schedule on the
		// source that happened to be selected when it was installed.
		switch base {
		case updatesource.ReleaseBase(updatesource.GitHub), updatesource.ReleaseBase(updatesource.Gitee):
			base = ""
		}
		status, err = updatecheck.InstallSchedule("", base)
	case "remove", "uninstall", "disable":
		status, err = updatecheck.RemoveSchedule()
	case "status":
		status, err = updatecheck.ScheduleStatusInfo()
	default:
		return a.fail(fmt.Errorf("unknown --schedule action %q (expected install | remove | status)", action))
	}
	if err != nil {
		return a.fail(err)
	}
	result := map[string]any{
		"ok":        true,
		"action":    "update-schedule",
		"schedule":  action,
		"installed": status.Installed,
		"platform":  status.Platform,
		"kind":      status.Kind,
		"path":      status.Path,
		"detail":    status.Detail,
	}
	human := func() {
		if status.Installed {
			a.printf("update schedule: installed (%s)\n", status.Kind)
		} else {
			a.printf("update schedule: not installed (%s)\n", status.Kind)
		}
		if status.Path != "" {
			a.printf("  path: %s\n", status.Path)
		}
		if status.Detail != "" {
			a.printf("  %s\n", status.Detail)
		}
	}
	if a.args.Flag("quiet") {
		return 0
	}
	return emitOrFail(a, result, human)
}
