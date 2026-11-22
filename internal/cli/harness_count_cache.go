// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package cli

// Harness turn-count cache.
//
// `sync status` used to rescan every harness transcript (five harnesses, up to 2000
// turns each, JSON-decoding tens of thousands of rows) on every call, and a reconcile
// pass did it twice more. Counts are memoised in `rm_meta` with a store
// fingerprint and vault count. A cache hit can be stale within the TTL; the vault
// count cannot detect replacement of one unit by another at the same total.

import (
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/rocareer/rolling-memory/internal/harness"
)

const (
	harnessCountCachePrefix = "harness_count_cache:"
	// Version 5 applies the ingest bound inside the scan page (out-of-scope
	// turns no longer consume the budget or poison Complete), so rows written by
	// the previous scan semantics cannot be reused.
	harnessCountCacheVersion = 5
)

type harnessCountCache struct {
	Version      int   `json:"version"`
	Fingerprint  int64 `json:"fp"`
	VaultUnits   int   `json:"vault"`
	Since        int64 `json:"since"`
	ComputedAt   int64 `json:"computed_at"`
	Sessions     int   `json:"sessions"`
	LocalTurns   int   `json:"local_turns"`
	Synced       int   `json:"synced"`
	Unsynced     int   `json:"unsynced"`
	OutOfScope   int   `json:"out_of_scope"`
	Incomplete   int   `json:"incomplete"`
	LastActiveAt int64 `json:"last_active_at"`
	Complete     bool  `json:"complete"`
}

// harnessStoreFingerprint summarises "has this harness store changed" without
// reading any content: the newest mtime across the store database, its WAL, and the
// transcript files the scanner would read.
func harnessStoreFingerprint(home, harnessID string) int64 {
	patterns := harness.TranscriptGlobs(home, harnessID)
	if harnessID == "zcode" {
		// ZCode keeps interactive sessions in SQLite; its transcript files alone
		// would miss a turn written straight to the database.
		patterns = append([]string{
			filepath.Join(home, ".zcode", "cli", "db", "db.sqlite"),
			filepath.Join(home, ".zcode", "cli", "db", "db.sqlite-wal"),
		}, patterns...)
	}
	if len(patterns) == 0 {
		return 0
	}
	newest := int64(0)
	for _, pattern := range patterns {
		matches, err := filepath.Glob(pattern)
		if err != nil {
			return 0
		}
		for _, path := range matches {
			info, statErr := os.Stat(path)
			if statErr != nil || info.IsDir() {
				continue
			}
			if stamp := info.ModTime().UnixNano(); stamp > newest {
				newest = stamp
			}
		}
	}
	return newest
}

func loadHarnessCountCache(db *sql.DB, harnessID, home string, vaultUnits int, since int64) (harnessCountCache, bool) {
	if db == nil || vaultUnits <= 0 {
		return harnessCountCache{}, false
	}
	var raw []byte
	if err := db.QueryRow(`SELECT v FROM rm_meta WHERE k = ?`, harnessCountCachePrefix+harnessID).Scan(&raw); err != nil {
		return harnessCountCache{}, false
	}
	var cached harnessCountCache
	if err := json.Unmarshal(raw, &cached); err != nil || cached.Version != harnessCountCacheVersion {
		return harnessCountCache{}, false
	}
	// The scan classifies every importable turn as synced or unsynced, so the
	// cached turn total must equal their sum; a row written before that total was
	// memoised fails this identity and is recomputed instead of surfacing as zero.
	if cached.LocalTurns != cached.Synced+cached.Unsynced {
		return harnessCountCache{}, false
	}
	if cached.VaultUnits != vaultUnits || cached.Since != since {
		return harnessCountCache{}, false
	}
	// Check the store fingerprint on every read, not only after the TTL: a new
	// transcript (or a new turn appended to an existing one) changes the counts
	// immediately, and serving the TTL-cached numbers would hide an unsynced
	// host until the timer expired. Stat/glob is cheap compared with the scan.
	fingerprint := harnessStoreFingerprint(home, harnessID)
	if fingerprint == 0 || cached.Fingerprint != fingerprint {
		return harnessCountCache{}, false
	}
	return cached, true
}

func storeHarnessCountCache(db *sql.DB, harnessID, home string, vaultUnits int, since int64, sessions, synced, unsynced, localTurns, outOfScope, incomplete int, lastActiveAt int64, complete bool) {
	if db == nil {
		return
	}
	fingerprint := harnessStoreFingerprint(home, harnessID)
	if fingerprint == 0 {
		return
	}
	raw, err := json.Marshal(harnessCountCache{
		Version: harnessCountCacheVersion, Fingerprint: fingerprint, VaultUnits: vaultUnits, Since: since,
		ComputedAt: time.Now().Unix(),
		Sessions:   sessions, LocalTurns: localTurns, Synced: synced, Unsynced: unsynced,
		OutOfScope: outOfScope, Incomplete: incomplete, LastActiveAt: lastActiveAt,
		Complete: complete,
	})
	if err != nil {
		return
	}
	_, _ = db.Exec(`INSERT OR REPLACE INTO rm_meta(k, v) VALUES (?, ?)`,
		harnessCountCachePrefix+harnessID, []byte(strings.TrimSpace(string(raw))))
}
