// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package harness

import (
	"database/sql"
	"os"
	"strings"
)

// InFlightWorkspace returns the workspace directory of a host session that is
// currently running (or just ran) a Rolling-Memory call. The host id selects the
// adapter; hosts without such a record return "". Callers never need to know
// which adapter can answer.
func InFlightWorkspace(harnessID, sessionID string, nowSec int64) string {
	switch harnessID {
	case "zcode":
		return zcodeInFlightWorkspace(sessionID, nowSec)
	default:
		return ""
	}
}

// zcodeInFlightWorkspace reads the host's own tool_usage record for the running
// or just-finished call. The session id, when known, narrows the query to this
// window instead of any other window on the machine.
func zcodeInFlightWorkspace(sessionID string, nowSec int64) string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	store := SessionStorePath(home, "zcode")
	if store == "" {
		return ""
	}
	if _, err := os.Stat(store); err != nil {
		return ""
	}
	db, err := sql.Open("sqlite", store+"?mode=ro&_pragma=busy_timeout(600)")
	if err != nil {
		return ""
	}
	defer db.Close()
	runningCutoffMS := (nowSec - 180) * 1000
	recentCutoffMS := (nowSec - 30) * 1000
	query := `
		SELECT coalesce(s.directory, '')
		FROM tool_usage t
		JOIN session s ON s.id = t.session_id
		WHERE t.tool_name LIKE '%rolling-memory%'
		  AND t.tool_name LIKE '%recall%'
		  AND ((t.status = 'running' AND t.started_at >= ?) OR t.started_at >= ?)
		  AND coalesce(s.directory, '') != ''`
	args := []any{runningCutoffMS, recentCutoffMS}
	if session := strings.TrimSpace(sessionID); session != "" {
		query += ` AND s.id = ?`
		args = append(args, session)
	}
	query += `
		ORDER BY (t.status = 'running') DESC, t.started_at DESC
		LIMIT 1`
	var dir string
	_ = db.QueryRow(query, args...).Scan(&dir)
	return strings.TrimSpace(dir)
}
