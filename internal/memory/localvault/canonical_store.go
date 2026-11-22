// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package localvault

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	core "github.com/rocareer/rolling-memory/internal/core"
)

// canonicalBusinessDDL holds the RM-owned business tables that live beside the
// canonical core schema (`rm_docs`/`rm_terms`/`rm_citations`). The core schema
// is the single source of truth for retrieval terms; these tables only carry
// product metadata (lineage pointers, background-file bookkeeping, L3 records,
// tombstones and revision counters) keyed by unit id.
//
// Revision triggers are attached to the core tables so freshness checks no
// longer depend on rowids: `INSERT OR REPLACE` changes a rowid on SQLite, while
// the revision counters move monotonically with every write.
const canonicalBusinessDDL = `
CREATE TABLE IF NOT EXISTS rm_unit_meta (
	unit_id TEXT PRIMARY KEY,
	layer TEXT NOT NULL DEFAULT '',
	ref_pointer TEXT NOT NULL DEFAULT '',
	sentences_json TEXT NOT NULL DEFAULT '[]',
	kept_indices_json TEXT NOT NULL DEFAULT '[]',
	address_uri TEXT NOT NULL DEFAULT '',
	coord64 INTEGER NOT NULL DEFAULT 0,
	waveform_bar TEXT NOT NULL DEFAULT '',
	doc_macro_wave TEXT NOT NULL DEFAULT '',
	assistant_id TEXT NOT NULL DEFAULT '',
	incarnation_id TEXT NOT NULL DEFAULT '',
	locus TEXT NOT NULL DEFAULT '',
	inserted_at INTEGER NOT NULL DEFAULT 0,
	meta_rev INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE IF NOT EXISTS rm_bg_dirs (
	dir_id TEXT PRIMARY KEY,
	root_path TEXT NOT NULL UNIQUE,
	added_at INTEGER NOT NULL DEFAULT 0,
	include_exts_json TEXT NOT NULL DEFAULT '[]'
);

CREATE TABLE IF NOT EXISTS rm_bg_files (
	abs_path TEXT PRIMARY KEY,
	dir_id TEXT NOT NULL,
	rel_path TEXT NOT NULL,
	mtime_ns INTEGER NOT NULL DEFAULT 0,
	file_size INTEGER NOT NULL DEFAULT 0,
	content_sha256 TEXT NOT NULL DEFAULT '',
	chunk_count INTEGER NOT NULL DEFAULT 0,
	unit_ids_json TEXT NOT NULL DEFAULT '[]',
	chunk_hashes_json TEXT NOT NULL DEFAULT '[]',
	chunker_generation TEXT NOT NULL DEFAULT ''
);

CREATE TABLE IF NOT EXISTS rm_bg_chunks (
	unit_id TEXT PRIMARY KEY,
	abs_path TEXT NOT NULL,
	rel_path TEXT NOT NULL,
	chunk_index INTEGER NOT NULL DEFAULT 0,
	anchor TEXT NOT NULL DEFAULT '',
	occurred_at INTEGER NOT NULL DEFAULT 0,
	inserted_at INTEGER NOT NULL DEFAULT 0,
	address_uri TEXT NOT NULL DEFAULT '',
	coord64 INTEGER NOT NULL DEFAULT 0,
	waveform_bar TEXT NOT NULL DEFAULT '',
	kept_indices_json TEXT NOT NULL DEFAULT '[]'
);
CREATE INDEX IF NOT EXISTS idx_rm_bg_chunks_path ON rm_bg_chunks(abs_path);
CREATE INDEX IF NOT EXISTS idx_rm_bg_chunks_occurred_at ON rm_bg_chunks(occurred_at DESC, unit_id ASC);

CREATE TABLE IF NOT EXISTS rm_episodic_index (
	doc_id TEXT NOT NULL,
	ref_pointer TEXT NOT NULL,
	codec_key BLOB NOT NULL,
	payload BLOB,
	content_digest BLOB,
	payload_mac BLOB,
	PRIMARY KEY (doc_id, codec_key)
);

CREATE TABLE IF NOT EXISTS rm_episodic_sources (
	turn_ref TEXT NOT NULL,
	doc_id TEXT NOT NULL,
	ref_pointer TEXT NOT NULL,
	content_digest BLOB,
	source_mac BLOB,
	PRIMARY KEY (turn_ref, doc_id, ref_pointer)
);
CREATE TABLE IF NOT EXISTS rm_episodic_lineage (
	turn_ref TEXT NOT NULL,
	doc_id TEXT NOT NULL,
	occurred_at INTEGER NOT NULL DEFAULT 0,
	PRIMARY KEY (turn_ref, doc_id)
);
CREATE INDEX IF NOT EXISTS idx_rm_episodic_lineage_doc ON rm_episodic_lineage(doc_id);
CREATE TRIGGER IF NOT EXISTS rm_episodic_sources_delete AFTER DELETE ON rm_docs BEGIN
	DELETE FROM rm_episodic_sources WHERE doc_id = OLD.unit_id;
	DELETE FROM rm_episodic_lineage WHERE doc_id = OLD.unit_id;
END;

CREATE TABLE IF NOT EXISTS rm_noetic (
	noetic_id TEXT PRIMARY KEY,
	occurred_at INTEGER NOT NULL DEFAULT 0,
	inserted_at INTEGER NOT NULL DEFAULT 0,
	status TEXT NOT NULL DEFAULT 'active',
	query_text TEXT NOT NULL,
	answer_text TEXT NOT NULL,
	cited_unit_ids_json TEXT NOT NULL DEFAULT '[]',
	cited_sentences_json TEXT NOT NULL DEFAULT '[]',
	address_uri TEXT NOT NULL DEFAULT '',
	coord64 INTEGER NOT NULL DEFAULT 0,
	waveform_bar TEXT NOT NULL DEFAULT '',
	assistant_id TEXT NOT NULL DEFAULT '',
	incarnation_id TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_rm_noetic_status_ts ON rm_noetic(status, occurred_at DESC);

CREATE TABLE IF NOT EXISTS rm_deleted_units (
	unit_id TEXT PRIMARY KEY,
	layer TEXT NOT NULL DEFAULT '',
	deleted_at INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE IF NOT EXISTS rm_revision (
	id INTEGER PRIMARY KEY CHECK (id = 1),
	docs_rev INTEGER NOT NULL DEFAULT 0,
	terms_rev INTEGER NOT NULL DEFAULT 0,
	meta_rev INTEGER NOT NULL DEFAULT 0,
	bg_rev INTEGER NOT NULL DEFAULT 0,
	episodic_rev INTEGER NOT NULL DEFAULT 0,
	docs_cnt INTEGER NOT NULL DEFAULT 0,
	terms_cnt INTEGER NOT NULL DEFAULT 0,
	change_seq INTEGER NOT NULL DEFAULT 0,
	purge_epoch INTEGER NOT NULL DEFAULT 0
);
INSERT OR IGNORE INTO rm_revision(id) VALUES (1);

CREATE TABLE IF NOT EXISTS rm_migration (
	key TEXT PRIMARY KEY,
	value TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS rm_change (
	seq INTEGER PRIMARY KEY AUTOINCREMENT,
	unit_id TEXT NOT NULL,
	op TEXT NOT NULL CHECK (op IN ('upsert','delete'))
);
CREATE INDEX IF NOT EXISTS idx_rm_change_unit ON rm_change(unit_id);

CREATE TABLE IF NOT EXISTS rm_ingest_queue (
	queue_id INTEGER PRIMARY KEY AUTOINCREMENT,
	request_key TEXT NOT NULL UNIQUE,
	status TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','running','applied','failed','canceled')),
	attempt INTEGER NOT NULL DEFAULT 0 CHECK (attempt >= 0),
	lease_token TEXT NOT NULL DEFAULT '',
	lease_expires_at_ns INTEGER NOT NULL DEFAULT 0,
	next_attempt_at_ns INTEGER NOT NULL DEFAULT 0 CHECK (next_attempt_at_ns >= 0),
	purge_epoch INTEGER NOT NULL DEFAULT 0 CHECK (purge_epoch >= 0),
	applied_change_seq INTEGER NOT NULL DEFAULT 0 CHECK (applied_change_seq >= 0),
	occurred_at INTEGER NOT NULL DEFAULT 0,
	assistant_id TEXT NOT NULL DEFAULT '',
	incarnation_id TEXT NOT NULL DEFAULT '',
	locus TEXT NOT NULL DEFAULT '',
	raw_payload BLOB NOT NULL,
	error_text TEXT NOT NULL DEFAULT '',
	CHECK (
		(status = 'running' AND attempt > 0 AND lease_token <> '' AND lease_expires_at_ns > 0)
		OR
		(status <> 'running' AND lease_token = '' AND lease_expires_at_ns = 0)
	),
	CHECK (status = 'applied' OR applied_change_seq = 0)
);

CREATE TRIGGER IF NOT EXISTS rm_rev_docs_ai AFTER INSERT ON rm_docs BEGIN
	UPDATE rm_revision SET docs_rev = docs_rev + 1 WHERE id = 1;
END;
CREATE TRIGGER IF NOT EXISTS rm_rev_docs_au AFTER UPDATE ON rm_docs BEGIN
	UPDATE rm_revision SET docs_rev = docs_rev + 1 WHERE id = 1;
END;
CREATE TRIGGER IF NOT EXISTS rm_rev_docs_ad AFTER DELETE ON rm_docs BEGIN
	UPDATE rm_revision SET docs_rev = docs_rev + 1 WHERE id = 1;
END;
CREATE TRIGGER IF NOT EXISTS rm_rev_terms_ai AFTER INSERT ON rm_terms BEGIN
	UPDATE rm_revision SET terms_rev = terms_rev + 1 WHERE id = 1;
END;
CREATE TRIGGER IF NOT EXISTS rm_rev_terms_au AFTER UPDATE ON rm_terms BEGIN
	UPDATE rm_revision SET terms_rev = terms_rev + 1 WHERE id = 1;
END;
CREATE TRIGGER IF NOT EXISTS rm_rev_terms_ad AFTER DELETE ON rm_terms BEGIN
	UPDATE rm_revision SET terms_rev = terms_rev + 1 WHERE id = 1;
END;
CREATE TRIGGER IF NOT EXISTS rm_rev_meta_ai AFTER INSERT ON rm_unit_meta BEGIN
	UPDATE rm_revision SET meta_rev = meta_rev + 1 WHERE id = 1;
END;
CREATE TRIGGER IF NOT EXISTS rm_rev_meta_au AFTER UPDATE ON rm_unit_meta BEGIN
	UPDATE rm_revision SET meta_rev = meta_rev + 1 WHERE id = 1;
END;
CREATE TRIGGER IF NOT EXISTS rm_rev_meta_ad AFTER DELETE ON rm_unit_meta BEGIN
	UPDATE rm_revision SET meta_rev = meta_rev + 1 WHERE id = 1;
END;
CREATE TRIGGER IF NOT EXISTS rm_rev_bg_ai AFTER INSERT ON rm_bg_chunks BEGIN
	UPDATE rm_revision SET bg_rev = bg_rev + 1 WHERE id = 1;
END;
CREATE TRIGGER IF NOT EXISTS rm_rev_bg_au AFTER UPDATE ON rm_bg_chunks BEGIN
	UPDATE rm_revision SET bg_rev = bg_rev + 1 WHERE id = 1;
END;
CREATE TRIGGER IF NOT EXISTS rm_rev_bg_ad AFTER DELETE ON rm_bg_chunks BEGIN
	UPDATE rm_revision SET bg_rev = bg_rev + 1 WHERE id = 1;
END;
CREATE TRIGGER IF NOT EXISTS rm_rev_epi_ai AFTER INSERT ON rm_episodic_index BEGIN
	UPDATE rm_revision SET episodic_rev = episodic_rev + 1 WHERE id = 1;
END;
CREATE TRIGGER IF NOT EXISTS rm_rev_epi_au AFTER UPDATE ON rm_episodic_index BEGIN
	UPDATE rm_revision SET episodic_rev = episodic_rev + 1 WHERE id = 1;
END;
CREATE TRIGGER IF NOT EXISTS rm_rev_epi_ad AFTER DELETE ON rm_episodic_index BEGIN
	UPDATE rm_revision SET episodic_rev = episodic_rev + 1 WHERE id = 1;
END;

CREATE TRIGGER IF NOT EXISTS rm_cnt_docs_ai AFTER INSERT ON rm_docs BEGIN
	UPDATE rm_revision SET docs_cnt = docs_cnt + 1 WHERE id = 1;
END;
CREATE TRIGGER IF NOT EXISTS rm_cnt_docs_ad AFTER DELETE ON rm_docs BEGIN
	UPDATE rm_revision SET docs_cnt = docs_cnt - 1 WHERE id = 1;
END;
CREATE TRIGGER IF NOT EXISTS rm_cnt_terms_ai AFTER INSERT ON rm_terms BEGIN
	UPDATE rm_revision SET terms_cnt = terms_cnt + 1 WHERE id = 1;
END;
CREATE TRIGGER IF NOT EXISTS rm_cnt_terms_ad AFTER DELETE ON rm_terms BEGIN
	UPDATE rm_revision SET terms_cnt = terms_cnt - 1 WHERE id = 1;
END;

CREATE TRIGGER IF NOT EXISTS rm_change_docs_ai AFTER INSERT ON rm_docs BEGIN
	INSERT INTO rm_change(unit_id, op) VALUES (NEW.unit_id, 'upsert');
	UPDATE rm_revision SET change_seq = (SELECT seq FROM rm_change WHERE rowid = last_insert_rowid()) WHERE id = 1;
END;
CREATE TRIGGER IF NOT EXISTS rm_change_docs_au AFTER UPDATE ON rm_docs BEGIN
	INSERT INTO rm_change(unit_id, op) VALUES (NEW.unit_id, 'upsert');
	UPDATE rm_revision SET change_seq = (SELECT seq FROM rm_change WHERE rowid = last_insert_rowid()) WHERE id = 1;
END;
CREATE TRIGGER IF NOT EXISTS rm_change_docs_ad AFTER DELETE ON rm_docs BEGIN
	INSERT INTO rm_change(unit_id, op) VALUES (OLD.unit_id, 'delete');
	UPDATE rm_revision SET change_seq = (SELECT seq FROM rm_change WHERE rowid = last_insert_rowid()) WHERE id = 1;
END;
CREATE TRIGGER IF NOT EXISTS rm_change_meta_ai AFTER INSERT ON rm_unit_meta BEGIN
	INSERT INTO rm_change(unit_id, op) VALUES (NEW.unit_id, 'upsert');
	UPDATE rm_revision SET change_seq = (SELECT seq FROM rm_change WHERE rowid = last_insert_rowid()) WHERE id = 1;
END;
CREATE TRIGGER IF NOT EXISTS rm_change_meta_au AFTER UPDATE ON rm_unit_meta BEGIN
	INSERT INTO rm_change(unit_id, op) VALUES (NEW.unit_id, 'upsert');
	UPDATE rm_revision SET change_seq = (SELECT seq FROM rm_change WHERE rowid = last_insert_rowid()) WHERE id = 1;
END;
CREATE TRIGGER IF NOT EXISTS rm_change_meta_ad AFTER DELETE ON rm_unit_meta BEGIN
	INSERT INTO rm_change(unit_id, op) VALUES (OLD.unit_id, 'upsert');
	UPDATE rm_revision SET change_seq = (SELECT seq FROM rm_change WHERE rowid = last_insert_rowid()) WHERE id = 1;
END;
`

// Queue indexes are full (status, ...) definitions rather than partial ones.
// Claim reads only the pending prefix, while the bounded-enqueue byte budget,
// the depth statistics and the applied/canceled pruning all filter other
// statuses and would fall back to a full table scan without a status-leading
// index. The covered history stays bounded because applied rows are pruned
// past the built snapshot and canceled rows are released with their purge
// generation.
const (
	queuePendingIndexName = "idx_rm_ingest_queue_pending"
	queueRunningIndexName = "idx_rm_ingest_queue_running"
	queueLegacyIndexName  = "idx_rm_ingest_queue_claim"
	queuePendingIndexCols = "(status, queue_id)"
	queueRunningIndexCols = "(status, lease_expires_at_ns, queue_id)"
	queuePendingIndexSQL  = "CREATE INDEX IF NOT EXISTS " + queuePendingIndexName + " ON rm_ingest_queue" + queuePendingIndexCols
	queueRunningIndexSQL  = "CREATE INDEX IF NOT EXISTS " + queueRunningIndexName + " ON rm_ingest_queue" + queueRunningIndexCols
)

// ensureIngestQueueIndexes keeps the queue indexes in the one supported shape.
// The probe reads sqlite_master only, so a steady-state open takes no write
// lock; the DROP/CREATE migration runs once when a vault still carries another
// definition (the earlier partial indexes) or a legacy index name.
func ensureIngestQueueIndexes(db *sql.DB) error {
	current, err := ingestQueueIndexesCurrent(db)
	if err != nil {
		return err
	}
	if current {
		return nil
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	for _, stmt := range []string{
		`DROP INDEX IF EXISTS ` + queueLegacyIndexName,
		`DROP INDEX IF EXISTS ` + queuePendingIndexName,
		`DROP INDEX IF EXISTS ` + queueRunningIndexName,
		queuePendingIndexSQL,
		queueRunningIndexSQL,
	} {
		if _, err := tx.Exec(stmt); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// ingestQueueIndexesCurrent reports whether both queue indexes already exist in
// the expected full-column shape and no legacy index name is left behind.
func ingestQueueIndexesCurrent(db *sql.DB) (bool, error) {
	rows, err := db.Query(
		`SELECT name, COALESCE(sql, '') FROM sqlite_master WHERE type = 'index' AND name IN (?, ?, ?)`,
		queuePendingIndexName, queueRunningIndexName, queueLegacyIndexName)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	seen := make(map[string]string, 3)
	for rows.Next() {
		var name, statement string
		if err := rows.Scan(&name, &statement); err != nil {
			return false, err
		}
		seen[name] = statement
	}
	if err := rows.Err(); err != nil {
		return false, err
	}
	if _, legacy := seen[queueLegacyIndexName]; legacy {
		return false, nil
	}
	pending, ok := seen[queuePendingIndexName]
	if !ok || !indexMatchesColumns(pending, queuePendingIndexCols) {
		return false, nil
	}
	running, ok := seen[queueRunningIndexName]
	if !ok || !indexMatchesColumns(running, queueRunningIndexCols) {
		return false, nil
	}
	return true, nil
}

// indexMatchesColumns compares the stored CREATE INDEX text after collapsing
// whitespace. A WHERE clause means a partial index, whose definition no longer
// supports the history-side queries.
func indexMatchesColumns(statement, columns string) bool {
	flat := strings.ToLower(strings.Join(strings.Fields(statement), " "))
	if strings.Contains(flat, " where ") {
		return false
	}
	return strings.Contains(flat, strings.ToLower(columns))
}

// canonicalUnitMeta carries the RM-owned metadata that accompanies one
// canonical document. Retrieval terms always come from the text extractor;
// none of these fields influence term identity.
type canonicalUnitMeta struct {
	Layer           string
	RefPointer      string
	SentencesJSON   string
	KeptIndicesJSON string
	AddressURI      string
	Coord64         int64
	WaveformBar     string
	DocMacroWave    string
	AssistantID     string
	IncarnationID   string
	Locus           string
	InsertedAt      int64
	DocHash         uint64
	BlockHash       uint64
	LineStart       int
	LineEnd         int
	CitedIDs        []string
	// StoreRawText persists the unit text into rm_docs.raw_text. Inline units
	// keep their text; pointer units resolve it from the source snapshot and
	// leave the column empty to avoid duplicating conversation transcripts.
	StoreRawText bool
}

// ensureCanonicalBusinessSchema creates the canonical core schema and the
// RM-owned metadata tables on the shared connection pool.
func ensureCanonicalBusinessSchema(db *sql.DB) error {
	if db == nil {
		return errors.New("localvault: nil sql.DB")
	}
	if _, err := core.OpenStoreWithOptions(db, core.StoreOptions{
		Dialect:    core.DialectSQLite,
		OwnsDB:     false,
		SkipSchema: false,
	}); err != nil {
		return err
	}
	// Vaults created before the background revision counter existed keep a
	// three-column rm_revision; the bg triggers in the DDL below would fail on
	// every chunk write without this additive migration. A fresh database has no
	// rm_revision yet, so both "duplicate column" and "no such table" are benign.
	for _, col := range []string{"bg_rev", "episodic_rev", "docs_cnt", "terms_cnt", "change_seq", "purge_epoch"} {
		if _, err := db.Exec(`ALTER TABLE rm_revision ADD COLUMN ` + col + ` INTEGER NOT NULL DEFAULT 0`); err != nil {
			msg := strings.ToLower(err.Error())
			if !strings.Contains(msg, "duplicate column") && !strings.Contains(msg, "no such table") {
				return err
			}
		}
	}
	if _, err := db.Exec(canonicalBusinessDDL); err != nil {
		return err
	}
	// A trial BEFORE INSERT trigger compensated the REPLACE rewrite leak, but it
	// also fired for INSERT ... ON CONFLICT DO UPDATE, which resolves to an
	// UPDATE and never fires the INSERT counter, so it could decrement on a
	// rewrite that did not add a row. The adapter now adjusts docs_cnt for the
	// rewrite it actually performs; drop the trial trigger from vaults that
	// carry it.
	var trialCounter int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type = 'trigger' AND name = 'rm_cnt_docs_bi'`).Scan(&trialCounter); err != nil {
		return err
	}
	if trialCounter != 0 {
		if _, err := db.Exec(`DROP TRIGGER IF EXISTS rm_cnt_docs_bi`); err != nil {
			return err
		}
	}
	// Vaults created by an earlier build of the ingest queue lack the deferred
	// retry column; the additive migration keeps them writable instead of
	// failing every claim query. Fresh databases already carry the column.
	if _, err := db.Exec(`ALTER TABLE rm_ingest_queue ADD COLUMN next_attempt_at_ns INTEGER NOT NULL DEFAULT 0`); err != nil {
		msg := strings.ToLower(err.Error())
		if !strings.Contains(msg, "duplicate column") && !strings.Contains(msg, "no such table") {
			return err
		}
	}
	// The queue indexes are created here, not in the DDL above: the DDL runs on
	// every open, and an unconditional DROP/CREATE of the status index rewrites
	// it (a full table scan) on each NewVault.
	if err := ensureIngestQueueIndexes(db); err != nil {
		return err
	}
	// One-time backfills for vaults that predate the O(1) corpus counters. The
	// markers keep later opens from scanning the tables again. The v2 marker
	// repairs the drift that REPLACE rewrites left before rm_cnt_docs_bi existed.
	backfillCounts := func(marker string) error {
		var counted int
		if err := db.QueryRow(`SELECT COUNT(*) FROM rm_migration WHERE key = ?`, marker).Scan(&counted); err != nil {
			return err
		}
		if counted != 0 {
			return nil
		}
		if _, err := db.Exec(`UPDATE rm_revision SET docs_cnt = (SELECT COUNT(*) FROM rm_docs), terms_cnt = (SELECT COUNT(*) FROM rm_terms) WHERE id = 1`); err != nil {
			return err
		}
		_, err := db.Exec(`INSERT OR REPLACE INTO rm_migration(key, value) VALUES (?, '1')`, marker)
		return err
	}
	if err := backfillCounts("canonical_counts_v1"); err != nil {
		return err
	}
	if err := backfillCounts("canonical_counts_v2"); err != nil {
		return err
	}
	return nil
}

// canonicalStore returns the shared core store handle wrapped around the vault
// connection. The handle is opened once per snapshot ref (shared by clones).
func (v *Vault) canonicalStore() (*core.Store, error) {
	if v == nil || v.db == nil {
		return nil, errors.New("localvault: nil Vault db")
	}
	ref := v.snapRef
	if ref == nil {
		return core.OpenStoreWithOptions(v.db, core.StoreOptions{
			Dialect: core.DialectSQLite, OwnsDB: false, SkipSchema: true,
		})
	}
	ref.canonicalOnce.Do(func() {
		ref.canonicalStore, ref.canonicalErr = core.OpenStoreWithOptions(v.db, core.StoreOptions{
			Dialect: core.DialectSQLite, OwnsDB: false, SkipSchema: true,
		})
	})
	return ref.canonicalStore, ref.canonicalErr
}

// PreparedCanonicalIngest is one canonical document derived outside the write
// transaction. Preparation reads the current row for metadata preservation and
// runs the term extractor; applying it only performs SQL.
type PreparedCanonicalIngest struct {
	doc  core.Doc
	meta canonicalUnitMeta
}

// prepareCanonicalIngest derives the retrieval document from the unit text with
// the canonical extractor. Retrieval terms are never reconstructed from packed
// atoms.
func (v *Vault) prepareCanonicalIngest(unitID string, occurredAt int64, text string, meta canonicalUnitMeta) (PreparedCanonicalIngest, error) {
	if v == nil || v.db == nil {
		return PreparedCanonicalIngest{}, errors.New("localvault: nil Vault db")
	}
	unitID = strings.TrimSpace(unitID)
	if unitID == "" {
		return PreparedCanonicalIngest{}, errors.New("localvault: canonical ingest requires a unit id")
	}
	// L1 background chunks are static document slices, not temporal dialogue
	// events: their file mtime must not enter SpacetimeCluster exact-timestamp
	// buckets, where thousands of chunks collapse onto one timestamp and create
	// O(B^2) neighbour edges. Same-document slices couple via DocHash + lines.
	if meta.Layer == LayerBackground {
		occurredAt = 0
	}
	var existingOccurred int64
	var existingAssistant, existingIncarnation, existingLocus string
	if err := v.db.QueryRow(`
		SELECT d.occurred_at, COALESCE(m.assistant_id, ''), COALESCE(m.incarnation_id, ''), COALESCE(m.locus, '')
		  FROM rm_docs d LEFT JOIN rm_unit_meta m ON m.unit_id = d.unit_id
		 WHERE d.unit_id = ?`, unitID).Scan(&existingOccurred, &existingAssistant, &existingIncarnation, &existingLocus); err == nil {
		if occurredAt <= 0 && existingOccurred > 0 {
			occurredAt = existingOccurred
		}
		if existingAssistant != "" {
			meta.AssistantID = existingAssistant
		}
		if existingIncarnation != "" {
			meta.IncarnationID = existingIncarnation
		}
		if existingLocus != "" {
			meta.Locus = existingLocus
		}
	}
	te := v.newVaultTermExtractor()
	doc := te.ExtractDoc(unitID, occurredAt, text)
	doc.Layer = meta.Layer
	doc.DocHash = meta.DocHash
	doc.BlockHash = meta.BlockHash
	doc.LineStart = meta.LineStart
	doc.LineEnd = meta.LineEnd
	doc.CitedIDs = meta.CitedIDs
	if meta.StoreRawText {
		doc.RawText = text
	}
	return PreparedCanonicalIngest{doc: doc, meta: meta}, nil
}

const canonicalUnitMetaUpsertSQL = `
	INSERT INTO rm_unit_meta(
		unit_id, layer, ref_pointer, sentences_json, kept_indices_json,
		address_uri, coord64, waveform_bar, doc_macro_wave,
		assistant_id, incarnation_id, locus, inserted_at, meta_rev
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 1)
	ON CONFLICT(unit_id) DO UPDATE SET
		layer = excluded.layer,
		ref_pointer = excluded.ref_pointer,
		sentences_json = excluded.sentences_json,
		kept_indices_json = excluded.kept_indices_json,
		address_uri = excluded.address_uri,
		coord64 = excluded.coord64,
		waveform_bar = excluded.waveform_bar,
		doc_macro_wave = excluded.doc_macro_wave,
		assistant_id = CASE WHEN rm_unit_meta.assistant_id <> '' THEN rm_unit_meta.assistant_id ELSE excluded.assistant_id END,
		incarnation_id = CASE WHEN rm_unit_meta.incarnation_id <> '' THEN rm_unit_meta.incarnation_id ELSE excluded.incarnation_id END,
		locus = CASE WHEN rm_unit_meta.locus <> '' THEN rm_unit_meta.locus ELSE excluded.locus END,
		inserted_at = CASE WHEN rm_unit_meta.inserted_at > 0 THEN rm_unit_meta.inserted_at ELSE excluded.inserted_at END,
		meta_rev = rm_unit_meta.meta_rev + 1`

func canonicalUnitMetaArgs(unitID string, meta canonicalUnitMeta) []any {
	sentences := meta.SentencesJSON
	if strings.TrimSpace(sentences) == "" {
		sentences = "[]"
	}
	kept := meta.KeptIndicesJSON
	if strings.TrimSpace(kept) == "" {
		kept = "[]"
	}
	return []any{
		unitID, meta.Layer, meta.RefPointer, sentences, kept,
		meta.AddressURI, meta.Coord64, meta.WaveformBar, meta.DocMacroWave,
		meta.AssistantID, meta.IncarnationID, meta.Locus, meta.InsertedAt,
	}
}

// writePreparedCanonical writes one prepared document and its metadata on an
// open transaction, leaving commit to the caller. The episodic tombstone check
// runs inside the same transaction so a concurrent deletion cannot be overtaken
// by a write that already passed a pre-flight read.
func writePreparedCanonical(tx *sql.Tx, store *core.Store, unitID string, prepared PreparedCanonicalIngest) error {
	if prepared.meta.Layer == LayerEpisodic {
		var deleted int
		if err := tx.QueryRow(`SELECT EXISTS(SELECT 1 FROM rm_deleted_units WHERE unit_id = ? AND (layer = 'episodic' OR layer = ''))`, unitID).Scan(&deleted); err != nil {
			return err
		}
		if deleted != 0 {
			return fmt.Errorf("%w: %s", ErrEpisodicUnitDeleted, unitID)
		}
	}
	// INSERT OR REPLACE resolves a primary-key conflict by deleting the old row,
	// and SQLite does not fire DELETE triggers for that implicit delete unless
	// recursive triggers are enabled. Remember whether this unit already exists
	// so docs_cnt stays equal to the live row count after the replacement.
	var replacing int
	if err := tx.QueryRow(`SELECT EXISTS(SELECT 1 FROM rm_docs WHERE unit_id = ?)`, unitID).Scan(&replacing); err != nil {
		return err
	}
	if err := store.IngestTx(tx, []core.Doc{prepared.doc}); err != nil {
		return err
	}
	if replacing != 0 {
		if _, err := tx.Exec(`UPDATE rm_revision SET docs_cnt = docs_cnt - 1 WHERE id = 1`); err != nil {
			return err
		}
	}
	_, err := tx.Exec(canonicalUnitMetaUpsertSQL, canonicalUnitMetaArgs(unitID, prepared.meta)...)
	return err
}

// ingestCanonicalText writes one prepared document and its metadata through a
// single transaction.
func (v *Vault) ingestCanonicalText(unitID string, occurredAt int64, text string, meta canonicalUnitMeta) error {
	if v == nil || v.db == nil {
		return nil
	}
	unitID = strings.TrimSpace(unitID)
	if unitID == "" {
		return nil
	}
	prepared, err := v.prepareCanonicalIngest(unitID, occurredAt, text, meta)
	if err != nil {
		return err
	}
	store, err := v.canonicalStore()
	if err != nil {
		return err
	}
	tx, err := v.db.BeginTx(context.Background(), nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := writePreparedCanonical(tx, store, unitID, prepared); err != nil {
		return err
	}
	return tx.Commit()
}

// ApplyPreparedQueuedIngest commits one prepared canonical document together
// with its unit metadata and the queue terminal state in a single transaction.
// The queue row must still belong to the supplied attempt and lease token, and
// a destructive purge that advanced the generation after enqueue rejects the
// write instead of resurrecting retired units.
func (v *Vault) ApplyPreparedQueuedIngest(ctx context.Context, item IngestQueueItem, prepared PreparedCanonicalIngest) (IngestQueueItem, error) {
	if v == nil || v.db == nil {
		return IngestQueueItem{}, errors.New("localvault: nil Vault db")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return IngestQueueItem{}, err
	}
	if item.QueueID <= 0 || item.Attempt <= 0 || strings.TrimSpace(item.LeaseToken) == "" {
		return IngestQueueItem{}, errors.New("localvault: queued ingest requires queue id, attempt, and lease token")
	}
	unitID := strings.TrimSpace(prepared.doc.UnitID)
	if unitID == "" {
		return IngestQueueItem{}, errors.New("localvault: queued ingest requires a prepared unit")
	}
	store, err := v.canonicalStore()
	if err != nil {
		return IngestQueueItem{}, err
	}

	tx, err := v.db.BeginTx(ctx, nil)
	if err != nil {
		return IngestQueueItem{}, err
	}
	defer func() { _ = tx.Rollback() }()

	var status, leaseToken string
	var attempt, rowEpoch, liveEpoch int64
	err = tx.QueryRowContext(ctx, `
		SELECT q.status, q.attempt, q.lease_token, q.purge_epoch, r.purge_epoch
		FROM rm_ingest_queue q CROSS JOIN rm_revision r
		WHERE q.queue_id = ? AND r.id = 1`, item.QueueID).
		Scan(&status, &attempt, &leaseToken, &rowEpoch, &liveEpoch)
	if errors.Is(err, sql.ErrNoRows) {
		return IngestQueueItem{}, ErrIngestQueueLeaseLost
	}
	if err != nil {
		return IngestQueueItem{}, err
	}
	if status != IngestQueueRunning || attempt != item.Attempt || leaseToken != item.LeaseToken {
		return IngestQueueItem{}, ErrIngestQueueLeaseLost
	}
	if rowEpoch != liveEpoch {
		return IngestQueueItem{}, ErrIngestQueuePurgeEpochChanged
	}
	if err := writePreparedCanonical(tx, store, unitID, prepared); err != nil {
		return IngestQueueItem{}, err
	}
	result, err := tx.ExecContext(ctx, `
		UPDATE rm_ingest_queue
		SET status = 'applied',
		    lease_token = '',
		    lease_expires_at_ns = 0,
		    applied_change_seq = COALESCE((SELECT change_seq FROM rm_revision WHERE id = 1), 0),
		    error_text = ''
		WHERE queue_id = ?
		  AND status = 'running'
		  AND attempt = ?
		  AND lease_token = ?
		  AND purge_epoch = (SELECT purge_epoch FROM rm_revision WHERE id = 1)`,
		item.QueueID, item.Attempt, item.LeaseToken,
	)
	if err != nil {
		return IngestQueueItem{}, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return IngestQueueItem{}, err
	}
	if affected != 1 {
		return IngestQueueItem{}, ErrIngestQueueLeaseLost
	}
	applied, err := ingestQueueItemByID(ctx, tx, item.QueueID)
	if err != nil {
		return IngestQueueItem{}, err
	}
	if err := tx.Commit(); err != nil {
		return IngestQueueItem{}, err
	}
	applied.RawPayload = append([]byte(nil), applied.RawPayload...)
	if v.slotCacheMu != nil {
		v.slotCacheMu.Lock()
		delete(v.slotCache, unitID)
		if v.snapRef != nil {
			v.snapRef.snapWriteGen++
		}
		v.slotCacheMu.Unlock()
	}
	return applied, nil
}

// deleteCanonicalUnits removes the retrieval documents and their metadata rows.
func (v *Vault) deleteCanonicalUnits(unitIDs ...string) error {
	if v == nil || v.db == nil || len(unitIDs) == 0 {
		return nil
	}
	filtered := make([]string, 0, len(unitIDs))
	placeholders := make([]string, 0, len(unitIDs))
	for _, id := range unitIDs {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		filtered = append(filtered, id)
		placeholders = append(placeholders, "?")
	}
	if len(filtered) == 0 {
		return nil
	}
	store, err := v.canonicalStore()
	if err != nil {
		return err
	}
	if err := store.DeleteDocs(filtered...); err != nil {
		return err
	}
	_, err = v.db.Exec(`DELETE FROM rm_unit_meta WHERE unit_id IN (`+strings.Join(placeholders, ",")+`)`, toAnySlice(filtered)...)
	return err
}

func toAnySlice(values []string) []any {
	out := make([]any, len(values))
	for i, value := range values {
		out[i] = value
	}
	return out
}

// deleteCanonicalBackgroundFile removes every canonical unit bound to one
// background file, then the file's RM bookkeeping rows.
func (v *Vault) deleteCanonicalBackgroundFile(absPath string) error {
	if v == nil || v.db == nil || strings.TrimSpace(absPath) == "" {
		return nil
	}
	rows, err := v.db.Query(`SELECT unit_id FROM rm_bg_chunks WHERE abs_path = ?`, absPath)
	if err != nil {
		return err
	}
	var unitIDs []string
	for rows.Next() {
		var unitID string
		if err := rows.Scan(&unitID); err != nil {
			rows.Close()
			return err
		}
		unitIDs = append(unitIDs, unitID)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	if err := v.deleteCanonicalUnits(unitIDs...); err != nil {
		return err
	}
	if _, err := v.db.Exec(`DELETE FROM rm_bg_chunks WHERE abs_path = ?`, absPath); err != nil {
		return err
	}
	_, err = v.db.Exec(`DELETE FROM rm_bg_files WHERE abs_path = ?`, absPath)
	return err
}

// deleteCanonicalEpisodicIndex removes the episodic index/source rows mirrored
// from the thin tables for one document.
func (v *Vault) deleteCanonicalEpisodicIndex(docID string) error {
	if v == nil || v.db == nil || strings.TrimSpace(docID) == "" {
		return nil
	}
	if _, err := v.db.Exec(`DELETE FROM rm_episodic_index WHERE doc_id = ?`, docID); err != nil {
		return err
	}
	_, err := v.db.Exec(`DELETE FROM rm_episodic_sources WHERE doc_id = ?`, docID)
	return err
}
