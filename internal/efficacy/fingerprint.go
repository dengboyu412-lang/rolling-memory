// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package efficacy

import (
	"database/sql"
	"strings"

	"github.com/rocareer/rolling-memory/internal/ids"
)

// readDBIdentity returns the immutable creation metadata of one local vault.
// The fingerprint must not depend on mutable corpus rows: background ingestion
// can insert rows with occurred_at = 0 and change the "oldest" document at any
// time, which used to rotate the ledger and hide its direct recall events.
func readDBIdentity(db *sql.DB) (createdAt, engine string) {
	if db == nil {
		return "", ""
	}
	_ = db.QueryRow(`SELECT coalesce(value, '') FROM db_meta WHERE key = 'created_at'`).Scan(&createdAt)
	_ = db.QueryRow(`SELECT coalesce(value, '') FROM db_meta WHERE key = 'engine'`).Scan(&engine)
	return strings.TrimSpace(createdAt), strings.TrimSpace(engine)
}

// ComputeDBFingerprint derives the stable identity of one local vault from its
// immutable creation metadata. It deliberately ignores the corpus: the ledger
// is bound to the vault, not to whichever row happens to sort first today. The
// oldest unit is still read as a readiness gate so an empty vault does not mint
// an unbound sidecar ledger before its first document exists.
func ComputeDBFingerprint(db *sql.DB, scope string) string {
	createdAt, engine := readDBIdentity(db)
	if createdAt == "" {
		return ""
	}
	var firstDocID string
	_ = db.QueryRow(`SELECT coalesce(unit_id, '') FROM rm_docs ORDER BY occurred_at ASC, rowid ASC LIMIT 1`).Scan(&firstDocID)
	if strings.TrimSpace(firstDocID) == "" {
		_ = db.QueryRow(`SELECT coalesce(noetic_id, '') FROM rm_noetic ORDER BY occurred_at ASC, rowid ASC LIMIT 1`).Scan(&firstDocID)
	}
	if strings.TrimSpace(firstDocID) == "" {
		return ""
	}
	return ids.SHA256Hex(createdAt + "|" + engine)[:24]
}

// LegacyDBFingerprint reproduces the previous identity formula, which anchored
// on the oldest canonical unit. It exists only to recognise ledgers written by
// older builds so they can be rebound in place instead of rotated aside.
func LegacyDBFingerprint(db *sql.DB) string {
	if db == nil {
		return ""
	}
	createdAt, engine := readDBIdentity(db)
	var firstDocID string
	_ = db.QueryRow(`SELECT coalesce(unit_id, '') FROM rm_docs ORDER BY occurred_at ASC, rowid ASC LIMIT 1`).Scan(&firstDocID)
	if strings.TrimSpace(firstDocID) == "" {
		_ = db.QueryRow(`SELECT coalesce(noetic_id, '') FROM rm_noetic ORDER BY occurred_at ASC, rowid ASC LIMIT 1`).Scan(&firstDocID)
	}
	if createdAt == "" || strings.TrimSpace(firstDocID) == "" {
		return ""
	}
	return ids.SHA256Hex(createdAt + "|" + engine + "|" + strings.TrimSpace(firstDocID))[:24]
}
