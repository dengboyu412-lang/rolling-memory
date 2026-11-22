// Copyright 2026 Rocareer & Rolling & PsychologyLake.org.cn
// Author: Albert <albert@rocareer.com>

package episodic

import (
	"database/sql"
	"encoding/json"
	"math"
	"strings"

	"github.com/rocareer/rolling-memory/internal/speech"
)

// OpenCode stores one conversation stream per session in `session_message`
// (session_v2 schema): `user` rows carry `data.text`, `assistant` rows carry a
// `data.content` array of reasoning / tool / text elements, and `idle` rows
// carry the session-level outcome (`succeeded`, `failed`, `interrupted`).
//
// The reader keeps only speech: user text and assistant text elements. Reasoning
// and tool elements are protocol steps, and an assistant step that invoked a tool
// is working narration rather than the turn's answer, so neither becomes a unit.
// A turn is completed when its last assistant step finished (`time.completed`)
// without an error and without a tool element, and, when an idle event exists,
// that event reported `succeeded`. Unknown types and incomplete turns are left
// out of the import, matching every other host reader.
const (
	openCodeTypeUser      = "user"
	openCodeTypeAssistant = "assistant"
	openCodeTypeIdle      = "idle"

	openCodeContentText = "text"
	openCodeContentTool = "tool"

	openCodeOutcomeSucceeded = "succeeded"
)

// OpenCodeTurn is one user message plus the speech that followed it before the
// next user message, with the completion verdict the reader derived.
type OpenCodeTurn struct {
	Turn
	Completed bool
}

type openCodeMessageData struct {
	Text    string          `json:"text"`
	Error   json.RawMessage `json:"error"`
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
	Time struct {
		Created   int64 `json:"created"`
		Completed int64 `json:"completed"`
	} `json:"time"`
}

// openCodeErrorPresent reports whether the assistant step carries an explicit
// failure object. A missing field and an explicit `null` are both "no error".
func openCodeErrorPresent(raw json.RawMessage) bool {
	trimmed := strings.TrimSpace(string(raw))
	return trimmed != "" && trimmed != "null"
}

// openCodeSeconds normalises a host timestamp. Current stores write
// milliseconds; the normalisation keeps second-based stores readable too.
func openCodeSeconds(value int64) int64 {
	if value > 1e11 {
		value /= 1000
	}
	if value < 0 {
		return 0
	}
	return value
}

// openCodeTurnBuilder is the shared state machine of the session scanner and the
// single-turn resolver. Both must produce byte-identical segments so `#<k>`
// names the same speech in the ingest and in the live pointer reader.
type openCodeTurnBuilder struct {
	turns []OpenCodeTurn
	cur   *OpenCodeTurn
	// last assistant step state; reset when a turn is closed
	seenAssistant   bool
	assistantDone   bool
	assistantTool   bool
	assistantFailed bool
}

func (b *openCodeTurnBuilder) close(completed bool) {
	if b.cur == nil {
		return
	}
	if len(b.cur.Segments) > 0 {
		b.cur.Completed = completed
		b.turns = append(b.turns, *b.cur)
	}
	b.cur = nil
	b.seenAssistant, b.assistantDone, b.assistantTool, b.assistantFailed = false, false, false, false
}

// verdict is the completion rule shared by idle-closed and stream-ended turns.
func (b *openCodeTurnBuilder) verdict(idleOutcome string) bool {
	if !b.seenAssistant || !b.assistantDone || b.assistantTool || b.assistantFailed {
		return false
	}
	outcome := strings.ToLower(strings.TrimSpace(idleOutcome))
	return outcome == "" || outcome == openCodeOutcomeSucceeded
}

func (b *openCodeTurnBuilder) appendSegment(role, text string, occurredAt int64) {
	if b.cur == nil {
		return
	}
	cleaned := speech.StripConversationNoise(text)
	if cleaned == "" {
		return
	}
	if occurredAt > b.cur.OccurredAt {
		b.cur.OccurredAt = occurredAt
	}
	b.cur.Segments = append(b.cur.Segments, Segment{
		Role: role, Text: cleaned, OccurredAt: occurredAt, Index: len(b.cur.Segments),
	})
}

func (b *openCodeTurnBuilder) user(id, text string, occurredAt int64) {
	// A user message that arrives before an idle event still ends the previous
	// turn: the host wrote the next prompt. The verdict only accepts a final
	// answer step, so an interrupted tool loop stays unimportable.
	b.close(b.verdict(""))
	if strings.TrimSpace(speech.StripConversationNoise(text)) == "" {
		return
	}
	b.cur = &OpenCodeTurn{Turn: Turn{UserMsgID: id}}
	b.appendSegment(RoleUser, text, occurredAt)
}

func (b *openCodeTurnBuilder) assistant(data openCodeMessageData, occurredAt int64) {
	if b.cur == nil {
		return
	}
	hasTool := false
	for _, content := range data.Content {
		if strings.EqualFold(strings.TrimSpace(content.Type), openCodeContentTool) {
			hasTool = true
			break
		}
	}
	b.seenAssistant = true
	b.assistantDone = data.Time.Completed > 0
	b.assistantTool = hasTool
	b.assistantFailed = openCodeErrorPresent(data.Error)
	if hasTool {
		// The step's text is process narration around its tool calls.
		return
	}
	for _, content := range data.Content {
		if !strings.EqualFold(strings.TrimSpace(content.Type), openCodeContentText) {
			continue
		}
		b.appendSegment(RoleAssistant, content.Text, occurredAt)
	}
}

func (b *openCodeTurnBuilder) idle(outcome string) {
	if b.cur == nil {
		return
	}
	b.close(b.verdict(outcome))
}

// finishStream closes the tail turn. A session's newest turn may still be
// growing; the rule above already refuses a step that is unfinished, failed, or
// waiting on a tool call, so only a completed answer step closes the turn.
func (b *openCodeTurnBuilder) finishStream() {
	if b.cur == nil {
		return
	}
	b.close(b.verdict(""))
}

type openCodeRowScanner interface {
	Next() bool
	Scan(dest ...any) error
	Err() error
}

func (b *openCodeTurnBuilder) consume(rows openCodeRowScanner) error {
	for rows.Next() {
		var (
			id       string
			typ      string
			dataJSON string
			created  int64
		)
		if err := rows.Scan(&id, &typ, &created, &dataJSON); err != nil {
			return err
		}
		occurredAt := openCodeSeconds(created)
		switch strings.ToLower(strings.TrimSpace(typ)) {
		case openCodeTypeUser:
			var data openCodeMessageData
			_ = json.Unmarshal([]byte(dataJSON), &data)
			b.user(id, data.Text, occurredAt)
		case openCodeTypeAssistant:
			var data openCodeMessageData
			if err := json.Unmarshal([]byte(dataJSON), &data); err != nil {
				continue
			}
			b.assistant(data, occurredAt)
		case openCodeTypeIdle:
			var data struct {
				Outcome string `json:"outcome"`
			}
			_ = json.Unmarshal([]byte(dataJSON), &data)
			b.idle(data.Outcome)
		}
	}
	return rows.Err()
}

const openCodeRowColumns = `id, type, time_created, data`

// OpenCodeSessionTurns enumerates every closed or closed-tail turn of one
// OpenCode session in sequence order.
func OpenCodeSessionTurns(db *sql.DB, sessionID string) ([]OpenCodeTurn, error) {
	if db == nil || strings.TrimSpace(sessionID) == "" {
		return nil, nil
	}
	rows, err := db.Query(`SELECT `+openCodeRowColumns+`
		FROM session_message
		WHERE session_id = ? AND type IN ('user', 'assistant', 'idle')
		ORDER BY seq ASC`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	builder := &openCodeTurnBuilder{}
	if err := builder.consume(rows); err != nil {
		return nil, err
	}
	builder.finishStream()
	return builder.turns, nil
}

// OpenCodeTurnSegmentsUpTo resolves one turn for the zero-copy pointer reader.
// The scan is bounded to the message range between the opening user message and
// the next user message, so a pointer lookup does not read the whole session.
// `minSegments` is accepted for symmetry with the ZCode reader; the bounded
// range always yields every segment of the turn.
func OpenCodeTurnSegmentsUpTo(db *sql.DB, sessionID, userMsgID string, minSegments int) ([]Segment, bool, error) {
	if db == nil || strings.TrimSpace(sessionID) == "" || strings.TrimSpace(userMsgID) == "" {
		return nil, false, sql.ErrNoRows
	}
	var startSeq sql.NullInt64
	if err := db.QueryRow(`SELECT seq FROM session_message WHERE session_id = ? AND id = ? AND type = 'user'`, sessionID, userMsgID).Scan(&startSeq); err != nil {
		return nil, false, err
	}
	if !startSeq.Valid {
		return nil, false, sql.ErrNoRows
	}
	var nextSeq sql.NullInt64
	if err := db.QueryRow(`SELECT MIN(seq) FROM session_message WHERE session_id = ? AND type = 'user' AND seq > ?`, sessionID, startSeq.Int64).Scan(&nextSeq); err != nil {
		return nil, false, err
	}
	upper := int64(math.MaxInt64)
	if nextSeq.Valid {
		upper = nextSeq.Int64 + 1
	}
	rows, err := db.Query(`SELECT `+openCodeRowColumns+`
		FROM session_message
		WHERE session_id = ? AND seq >= ? AND seq < ?
		  AND type IN ('user', 'assistant', 'idle')
		ORDER BY seq ASC`,
		sessionID, startSeq.Int64, upper)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	builder := &openCodeTurnBuilder{}
	if err := builder.consume(rows); err != nil {
		return nil, false, err
	}
	builder.finishStream()
	if len(builder.turns) == 0 {
		return nil, false, sql.ErrNoRows
	}
	turn := builder.turns[0]
	return turn.Segments, turn.Completed, nil
}
