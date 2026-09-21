package store

import (
	"fmt"
)

// RequestProgressPatch carries optional request-row progress updates.
// Nil pointers leave the column unchanged.
type RequestProgressPatch struct {
	State                    *string
	JudgeStartedAt           *string
	JudgeDeadlineAt          *string
	FinishedAt               *string
	TFirstUsableViewMs       *int64
	TFinalMs                 *int64
	ViewsCompletedInDeadline *int
	JudgeCompletedInDeadline *int
	DangerFlagged            *bool
}

// UpdateRequestProgress applies the non-nil fields of p to a request row.
func (db *DB) UpdateRequestProgress(id string, p RequestProgressPatch) error {
	set := ""
	args := []any{}
	add := func(col string, v any) {
		if set != "" {
			set += ", "
		}
		set += col + " = ?"
		args = append(args, v)
	}
	if p.State != nil {
		add("state", *p.State)
	}
	if p.JudgeStartedAt != nil {
		add("judge_started_at", *p.JudgeStartedAt)
	}
	if p.JudgeDeadlineAt != nil {
		add("judge_deadline_at", *p.JudgeDeadlineAt)
	}
	if p.FinishedAt != nil {
		add("finished_at", *p.FinishedAt)
	}
	if p.TFirstUsableViewMs != nil {
		add("t_first_usable_view_ms", *p.TFirstUsableViewMs)
	}
	if p.TFinalMs != nil {
		add("t_final_ms", *p.TFinalMs)
	}
	if p.ViewsCompletedInDeadline != nil {
		add("views_completed_in_deadline", *p.ViewsCompletedInDeadline)
	}
	if p.JudgeCompletedInDeadline != nil {
		add("judge_completed_in_deadline", *p.JudgeCompletedInDeadline)
	}
	if p.DangerFlagged != nil {
		add("danger_flagged", boolInt(*p.DangerFlagged))
	}
	if set == "" {
		return nil
	}
	args = append(args, id)
	res, err := db.db.Exec(`UPDATE requests SET `+set+` WHERE id = ?`, args...)
	if err != nil {
		return fmt.Errorf("store: update request progress: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("store: update request progress rows: %w", err)
	}
	if n == 0 {
		return fmt.Errorf("store: request %s not found", id)
	}
	return nil
}

// UpdateRequestStateIfNotSuperseded sets state unless the row is superseded.
// It returns (updated=false, nil) when the row is already superseded.
func (db *DB) UpdateRequestStateIfNotSuperseded(id, state string) (bool, error) {
	res, err := db.db.Exec(`UPDATE requests SET state = ? WHERE id = ? AND state != 'superseded'`, state, id)
	if err != nil {
		return false, fmt.Errorf("store: update request state: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("store: update request state rows: %w", err)
	}
	return n > 0, nil
}

// SetFirstUsableViewMsIfUnset records t_first_usable_view_ms only when unset.
func (db *DB) SetFirstUsableViewMsIfUnset(id string, ms int64) error {
	_, err := db.db.Exec(`UPDATE requests SET t_first_usable_view_ms = ?
		WHERE id = ? AND t_first_usable_view_ms IS NULL`, ms, id)
	if err != nil {
		return fmt.Errorf("store: set first usable view: %w", err)
	}
	return nil
}

// SetDangerFlagged sets danger_flagged on a request row.
func (db *DB) SetDangerFlagged(id string) error {
	_, err := db.db.Exec(`UPDATE requests SET danger_flagged = 1 WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("store: set danger flagged: %w", err)
	}
	return nil
}

// GetRequestRow returns the requests row without the aggregate joins.
func (db *DB) GetRequestRow(id string) (*Request, error) {
	row := db.db.QueryRow(`SELECT `+requestCols+` FROM requests WHERE id = ?`, id)
	return scanRequest(row)
}

// ListRequestViews returns the request_views rows for a request ordered by seat.
func (db *DB) ListRequestViews(requestID string) ([]*RequestView, error) {
	rows, err := db.db.Query(`SELECT id, created_at, request_id, seat, response_id, state,
		included_in_judge, completed_at FROM request_views WHERE request_id = ? ORDER BY seat`, requestID)
	if err != nil {
		return nil, fmt.Errorf("store: list views: %w", err)
	}
	defer rows.Close()
	var out []*RequestView
	for rows.Next() {
		var v RequestView
		var included int
		if err := rows.Scan(&v.ID, &v.CreatedAt, &v.RequestID, &v.Seat,
			&v.ResponseID, &v.State, &included, &v.CompletedAt); err != nil {
			return nil, fmt.Errorf("store: scan view: %w", err)
		}
		v.IncludedInJudge = included != 0
		out = append(out, &v)
	}
	return out, rows.Err()
}

// JudgeResult carries the persisted judge outcome for one request.
type JudgeResult struct {
	State              string
	ResponseID         *string
	IncludedSeats      []string
	MissingSeats       []string
	NoIndependentViews bool
}

// UpdateJudgeResult writes the final request_judge row for a request.
func (db *DB) UpdateJudgeResult(requestID string, res JudgeResult) error {
	included := "[]"
	if res.IncludedSeats != nil {
		included = jsonStringArray(res.IncludedSeats)
	}
	missing := "[]"
	if res.MissingSeats != nil {
		missing = jsonStringArray(res.MissingSeats)
	}
	_, err := db.db.Exec(`UPDATE request_judge SET state = ?, response_id = ?,
		included_seats_json = ?, missing_seats_json = ?, no_independent_views = ?
		WHERE request_id = ?`,
		res.State, res.ResponseID, included, missing, boolInt(res.NoIndependentViews), requestID)
	if err != nil {
		return fmt.Errorf("store: update judge result: %w", err)
	}
	return nil
}

// jsonStringArray encodes a string slice as compact JSON.
func jsonStringArray(ss []string) string {
	if ss == nil {
		return "[]"
	}
	out := "["
	for i, s := range ss {
		if i > 0 {
			out += ","
		}
		out += quoteJSONString(s)
	}
	return out + "]"
}

// quoteJSONString quotes one string as JSON.
func quoteJSONString(s string) string {
	q := make([]byte, 0, len(s)+2)
	q = append(q, '"')
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch c {
		case '"', '\\':
			q = append(q, '\\', c)
		case '\n':
			q = append(q, '\\', 'n')
		case '\r':
			q = append(q, '\\', 'r')
		case '\t':
			q = append(q, '\\', 't')
		default:
			q = append(q, c)
		}
	}
	return string(append(q, '"'))
}
