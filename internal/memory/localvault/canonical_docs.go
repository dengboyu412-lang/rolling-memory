// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package localvault

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"

	core "github.com/rocareer/rolling-memory/internal/core"
)

// canonicalDocChunk bounds one IN (...) list. SQLite builds in the wild cap
// host parameters at 999, and three tables are read per chunk.
const canonicalDocChunk = 300

// loadChangedDocsTx hydrates the kernel unit representation for the requested
// unit IDs from the canonical store. It is an adapter, not a second index: the
// terms, counts, positions and standard metadata come verbatim from rm_docs,
// rm_terms and rm_citations, which the kernel's Store already writes.
func (v *Vault) loadChangedDocsTx(ctx context.Context, tx *sql.Tx, ids []string) ([]core.Doc, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	if tx == nil {
		return nil, errors.New("localvault: doc hydration requires a transaction")
	}
	ids = append([]string(nil), ids...)
	sort.Strings(ids)
	byID := make(map[string]*core.Doc, len(ids))
	for start := 0; start < len(ids); start += canonicalDocChunk {
		end := start + canonicalDocChunk
		if end > len(ids) {
			end = len(ids)
		}
		chunk := ids[start:end]
		ph := sqlPlaceholders(len(chunk))
		args := make([]any, len(chunk))
		for i, id := range chunk {
			args[i] = id
		}
		if err := loadCanonicalDocsChunk(ctx, tx, ph, args, byID); err != nil {
			return nil, err
		}
		if err := loadCanonicalTermsChunk(ctx, tx, ph, args, byID); err != nil {
			return nil, err
		}
		if err := loadCanonicalCitationsChunk(ctx, tx, ph, args, byID); err != nil {
			return nil, err
		}
	}
	out := make([]core.Doc, 0, len(ids))
	for _, id := range ids {
		if doc := byID[id]; doc != nil {
			out = append(out, *doc)
		}
	}
	return out, nil
}

// loadChangedDocs is the transaction-owning variant used by tests and one-off
// callers. The builder uses loadChangedDocsTx so the change log and the unit
// rows come from one read snapshot.
func (v *Vault) loadChangedDocs(ctx context.Context, ids []string) ([]core.Doc, error) {
	if v == nil || v.db == nil {
		return nil, errors.New("localvault: nil vault")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	tx, err := v.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	return v.loadChangedDocsTx(ctx, tx, ids)
}

func loadCanonicalDocsChunk(ctx context.Context, tx *sql.Tx, ph string, args []any, byID map[string]*core.Doc) error {
	rows, err := tx.QueryContext(ctx, `
		SELECT unit_id, occurred_at, narrated_at, ingested_at, author_hash, doc_hash,
		       block_hash, line_start, line_end, layer, raw_text
		FROM rm_docs WHERE unit_id IN (`+ph+`)`, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var (
			doc                        core.Doc
			author, docHash, blockHash int64
			lineStart, lineEnd         int64
		)
		if err := rows.Scan(&doc.UnitID, &doc.OccurredAt, &doc.NarratedAt, &doc.IngestedAt,
			&author, &docHash, &blockHash, &lineStart, &lineEnd, &doc.Layer, &doc.RawText); err != nil {
			return err
		}
		doc.AuthorHash = uint64(author)
		doc.DocHash = uint64(docHash)
		doc.BlockHash = uint64(blockHash)
		doc.LineStart = int(lineStart)
		doc.LineEnd = int(lineEnd)
		doc.Terms = make(map[uint64]core.Term)
		doc.Counts = make(map[uint64]int)
		byID[doc.UnitID] = &doc
	}
	return rows.Err()
}

func loadCanonicalTermsChunk(ctx context.Context, tx *sql.Tx, ph string, args []any, byID map[string]*core.Doc) error {
	rows, err := tx.QueryContext(ctx, `
		SELECT unit_id, term_hash, bits_x10, class_flags, group_hash, occurrences, pos_blob
		FROM rm_terms WHERE unit_id IN (`+ph+`) ORDER BY unit_id, term_hash`, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var (
			unitID                       string
			termHash, bitsX10, groupHash int64
			classFlags, occurrences      int64
			posBlob                      []byte
		)
		if err := rows.Scan(&unitID, &termHash, &bitsX10, &classFlags, &groupHash, &occurrences, &posBlob); err != nil {
			return err
		}
		doc := byID[unitID]
		if doc == nil {
			continue
		}
		hash := uint64(termHash)
		role, lowExt := core.UnpackTermRoleExt(uint8(classFlags >> 8))
		extFlags := core.TermPersistExtFlags(classFlags)
		if extFlags == 0 {
			extFlags = lowExt
		}
		term := core.Term{
			Hash:     hash,
			Bits:     uint16(bitsX10),
			Flags:    uint8(classFlags),
			Role:     role,
			ExtFlags: extFlags,
			Group:    uint64(groupHash),
		}
		if occurrences < 1 {
			occurrences = 1
		}
		doc.Counts[hash] = int(occurrences)
		if len(posBlob) > 0 {
			positions, _, err := core.DecodePositionsIntoArena(posBlob, nil)
			if err != nil {
				return fmt.Errorf("localvault: decode positions for %s: %w", unitID, err)
			}
			if len(positions) > 0 {
				term.Sent = clampPosU16(positions[0].Sentence + 1)
				term.Slot = clampPosU16(positions[0].Slot + 1)
				if doc.Positions == nil {
					doc.Positions = make(map[uint64][]core.TermPosition, len(doc.Terms))
				}
				doc.Positions[hash] = positions
			}
		}
		doc.Terms[hash] = term
	}
	return rows.Err()
}

func loadCanonicalCitationsChunk(ctx context.Context, tx *sql.Tx, ph string, args []any, byID map[string]*core.Doc) error {
	rows, err := tx.QueryContext(ctx, `
		SELECT unit_id, cited_id FROM rm_citations
		WHERE unit_id IN (`+ph+`) ORDER BY unit_id, ord`, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var unitID, citedID string
		if err := rows.Scan(&unitID, &citedID); err != nil {
			return err
		}
		if doc := byID[unitID]; doc != nil {
			doc.CitedIDs = append(doc.CitedIDs, citedID)
		}
	}
	return rows.Err()
}

func sqlPlaceholders(n int) string {
	if n <= 0 {
		return ""
	}
	return strings.TrimSuffix(strings.Repeat("?,", n), ",")
}
