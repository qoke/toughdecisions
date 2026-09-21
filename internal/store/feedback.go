package store

import (
	"fmt"

	"github.com/qoke/toughdecisions/internal/ids"
)

// Feedback is a row in the feedback table.
type Feedback struct {
	ID        string
	CreatedAt string
	RequestID string
	Tag       string
	Note      string
}

// InsertFeedback inserts a feedback row.
func (db *DB) InsertFeedback(requestID, tag, note string) (*Feedback, error) {
	f := &Feedback{ID: ids.NewID(), CreatedAt: nowUTC(), RequestID: requestID, Tag: tag, Note: note}
	_, err := db.db.Exec(
		`INSERT INTO feedback (id, created_at, request_id, tag, note) VALUES (?, ?, ?, ?, ?)`,
		f.ID, f.CreatedAt, f.RequestID, f.Tag, f.Note,
	)
	if err != nil {
		return nil, fmt.Errorf("store: insert feedback: %w", err)
	}
	return f, nil
}

// FeedbackTagCounts returns per-tag counts for feedback created at or after since (RFC3339).
func (db *DB) FeedbackTagCounts(since string) (map[string]int, error) {
	rows, err := db.db.Query(`SELECT tag, COUNT(*) FROM feedback WHERE created_at >= ? GROUP BY tag`, since)
	if err != nil {
		return nil, fmt.Errorf("store: feedback tag counts: %w", err)
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var tag string
		var n int
		if err := rows.Scan(&tag, &n); err != nil {
			return nil, fmt.Errorf("store: scan tag count: %w", err)
		}
		out[tag] = n
	}
	return out, rows.Err()
}
