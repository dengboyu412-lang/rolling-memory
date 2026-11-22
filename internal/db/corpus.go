// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package db

import "database/sql"

// CorpusCounts are the physical counters of the canonical memory corpus
// (`rm_docs` plus active `rm_noetic` entries). They are the only counts the
// product reports; the legacy assertion graph is not part of a shipped vault.
type CorpusCounts struct {
	Episodic   int
	Background int
	Noetic     int
}

// MemoryUnits returns the number of stored documents plus active L3 entries.
func (c CorpusCounts) MemoryUnits() int {
	return c.Episodic + c.Background + c.Noetic
}

// Map returns the JSON-facing corpus block.
func (c CorpusCounts) Map() map[string]int {
	return map[string]int{
		"memories":     c.MemoryUnits(),
		"episodic":     c.Episodic,
		"background":   c.Background,
		"crystallized": c.Noetic,
	}
}

// ReadCorpusCounts reads the canonical corpus counters. A database without the
// canonical tables (a legacy assertion-graph vault) reports zero counts.
func ReadCorpusCounts(connection *sql.DB) (CorpusCounts, error) {
	var counts CorpusCounts
	if connection == nil {
		return counts, nil
	}
	var present int
	if err := connection.QueryRow(
		`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'rm_docs'`,
	).Scan(&present); err != nil {
		return counts, err
	}
	if present == 0 {
		return counts, nil
	}
	rows, err := connection.Query(`SELECT layer, COUNT(*) FROM rm_docs GROUP BY layer`)
	if err != nil {
		return counts, err
	}
	defer rows.Close()
	for rows.Next() {
		var layer string
		var count int
		if err := rows.Scan(&layer, &count); err != nil {
			return counts, err
		}
		switch layer {
		case "background":
			counts.Background += count
		case "noetic":
			// L3 entries live in rm_noetic; a stray rm_docs row is not a
			// retrievable memory unit.
		default:
			counts.Episodic += count
		}
	}
	if err := rows.Err(); err != nil {
		return counts, err
	}
	if err := connection.QueryRow(
		`SELECT COUNT(*) FROM rm_noetic WHERE status = 'active'`,
	).Scan(&counts.Noetic); err != nil && err != sql.ErrNoRows {
		return counts, err
	}
	return counts, nil
}

// CountCorpusTerms returns the number of stored term postings (rm_terms). It is
// a doctor-only size probe, not a retrieval input.
func CountCorpusTerms(connection *sql.DB) (int, error) {
	var present, count int
	if err := connection.QueryRow(
		`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'rm_terms'`,
	).Scan(&present); err != nil {
		return 0, err
	}
	if present == 0 {
		return 0, nil
	}
	if err := connection.QueryRow(`SELECT COUNT(*) FROM rm_terms`).Scan(&count); err != nil {
		return 0, err
	}
	return count, nil
}

// TimeConcentration describes how many memory units share one occurred_at
// second. The cluster pipeline pairs units inside a spacetime bucket, so a
// corpus whose event times collapse onto a few seconds carries a quadratic
// neighbour bill: SameInstantPairs is the exact number of pairs such a corpus
// presents to the bucket step, and PairsPerDoc is the multiplier it puts on the
// neighbour layer. Units without a clock (occurred_at = 0, such as background
// file slices) never enter a spacetime bucket and are excluded from the
// distribution.
type TimeConcentration struct {
	Docs             int64   `json:"docs"`
	DistinctInstants int64   `json:"distinct_instants"`
	LargestGroup     int64   `json:"largest_group"`
	SameInstantPairs int64   `json:"same_instant_pairs"`
	PairsPerDoc      float64 `json:"pairs_per_doc"`
}

// Collapsed reports whether the corpus concentrates on one instant hard enough
// to dominate storage. The 100x line is the point where the neighbour layer is
// already two orders of magnitude larger than the unit count; below it the
// absolute pair count stays in the thousands and cannot move a snapshot.
func (c TimeConcentration) Collapsed() bool {
	return c.PairsPerDoc >= 100
}

// ReadTimeConcentration reads the occurred_at distribution. A database without
// the canonical table reports a zero value.
func ReadTimeConcentration(connection *sql.DB) (TimeConcentration, error) {
	var out TimeConcentration
	if connection == nil {
		return out, nil
	}
	var present int
	if err := connection.QueryRow(
		`SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'rm_docs'`,
	).Scan(&present); err != nil {
		return out, err
	}
	if present == 0 {
		return out, nil
	}
	if err := connection.QueryRow(
		`SELECT COUNT(*), COUNT(DISTINCT occurred_at) FROM rm_docs WHERE occurred_at > 0`,
	).Scan(&out.Docs, &out.DistinctInstants); err != nil {
		return out, err
	}
	if out.Docs == 0 {
		return out, nil
	}
	if err := connection.QueryRow(
		`SELECT COALESCE(MAX(group_size), 0) FROM (SELECT COUNT(*) AS group_size FROM rm_docs WHERE occurred_at > 0 GROUP BY occurred_at)`,
	).Scan(&out.LargestGroup); err != nil {
		return out, err
	}
	if err := connection.QueryRow(
		`SELECT COALESCE(SUM(group_size * (group_size - 1) / 2), 0) FROM (SELECT COUNT(*) AS group_size FROM rm_docs WHERE occurred_at > 0 GROUP BY occurred_at)`,
	).Scan(&out.SameInstantPairs); err != nil {
		return out, err
	}
	out.PairsPerDoc = float64(out.SameInstantPairs) / float64(out.Docs)
	return out, nil
}
