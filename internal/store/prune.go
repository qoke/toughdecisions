package store

import (
	"fmt"
	"time"
)

// PruneResult reports how many production rows PruneProduction deleted.
type PruneResult struct {
	Requests     int64
	RequestViews int64
	RequestJudge int64
	Rewrites     int64
	SentMessages int64
	Responses    int64
}

// PruneProduction deletes production rows older than cutoff, preserving
// harness/grading/baselines rows and all feedback rows (so feedback tag
// counts survive). A request is eligible only when it has no feedback row:
// feedback references requests(id), so eligible requests have no surviving
// child rows after their own production children are deleted. Threads and
// cards are preserved: cards back surviving requests and threads own cards.
func (db *DB) PruneProduction(cutoff time.Time) (*PruneResult, error) {
	cut := cutoff.UTC().Format(time.RFC3339)
	res := &PruneResult{}
	stmts := []struct {
		name  string
		dst   *int64
		query string
	}{
		{"sent_messages", &res.SentMessages, `DELETE FROM sent_messages WHERE request_id IN (SELECT id FROM requests WHERE created_at < ? AND id NOT IN (SELECT request_id FROM feedback))`},
		{"rewrites", &res.Rewrites, `DELETE FROM rewrites WHERE request_id IN (SELECT id FROM requests WHERE created_at < ? AND id NOT IN (SELECT request_id FROM feedback))`},
		{"request_judge", &res.RequestJudge, `DELETE FROM request_judge WHERE request_id IN (SELECT id FROM requests WHERE created_at < ? AND id NOT IN (SELECT request_id FROM feedback))`},
		{"request_views", &res.RequestViews, `DELETE FROM request_views WHERE request_id IN (SELECT id FROM requests WHERE created_at < ? AND id NOT IN (SELECT request_id FROM feedback))`},
		{"responses", &res.Responses, `DELETE FROM responses WHERE origin = 'production' AND created_at < ? AND (request_id IS NULL OR request_id NOT IN (SELECT request_id FROM feedback))`},
		{"requests", &res.Requests, `DELETE FROM requests WHERE created_at < ? AND id NOT IN (SELECT request_id FROM feedback)`},
	}
	for _, s := range stmts {
		out, err := db.db.Exec(s.query, cut)
		if err != nil {
			return nil, fmt.Errorf("store: prune %s: %w", s.name, err)
		}
		n, err := out.RowsAffected()
		if err != nil {
			return nil, fmt.Errorf("store: prune %s rows: %w", s.name, err)
		}
		*s.dst = n
	}
	return res, nil
}
