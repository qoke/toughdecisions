package store

import (
	"fmt"

	"github.com/qoke/toughdecisions/internal/ids"
)

// Request is a row in the requests table.
type Request struct {
	ID                       string
	CreatedAt                string
	ThreadID                 string
	CardID                   string
	PackID                   string
	SnapshotJSON             string
	InputHash                string
	State                    string
	SupersedesRequestID      *string
	SupersededByRequestID    *string
	ViewsDeadlineAt          string
	JudgeStartedAt           *string
	JudgeDeadlineAt          *string
	FinishedAt               *string
	TFirstUsableViewMs       *int64
	TFinalMs                 *int64
	ViewsCompletedInDeadline int
	JudgeCompletedInDeadline *int
	DangerFlagged            bool
}

// RequestView is a row in the request_views table.
type RequestView struct {
	ID              string
	CreatedAt       string
	RequestID       string
	Seat            string
	ResponseID      *string
	State           string
	IncludedInJudge bool
	CompletedAt     *string
}

// RequestJudge is a row in the request_judge table.
type RequestJudge struct {
	ID                 string
	CreatedAt          string
	RequestID          string
	ResponseID         *string
	State              string
	IncludedSeatsJSON  string
	MissingSeatsJSON   string
	NoIndependentViews bool
}

// Rewrite is a row in the rewrites table.
type Rewrite struct {
	ID          string
	CreatedAt   string
	RequestID   string
	SourceRef   string
	Instruction string
	ResponseID  string
	OutputText  string
}

// SentMessage is a row in the sent_messages table.
type SentMessage struct {
	ID        string
	CreatedAt string
	RequestID string
	ThreadID  string
	Text      string
	SourceRef *string
	SentAt    string
}

// RequestAggregate is the full state of a request for the API.
type RequestAggregate struct {
	Request  *Request
	Views    []*RequestView
	Judge    *RequestJudge
	Rewrites []*Rewrite
	Sent     []*SentMessage
}

const requestCols = `id, created_at, thread_id, card_id, pack_id, snapshot_json,
	input_hash, state, supersedes_request_id, superseded_by_request_id, views_deadline_at,
	judge_started_at, judge_deadline_at, finished_at, t_first_usable_view_ms, t_final_ms,
	views_completed_in_deadline, judge_completed_in_deadline, danger_flagged`

// InsertRequest inserts a request row.
func (db *DB) InsertRequest(r *Request) (*Request, error) {
	if r.ID == "" {
		r.ID = ids.NewID()
	}
	if r.CreatedAt == "" {
		r.CreatedAt = nowUTC()
	}
	_, err := db.db.Exec(
		`INSERT INTO requests (`+requestCols+`) VALUES
		 (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		r.ID, r.CreatedAt, r.ThreadID, r.CardID, r.PackID, r.SnapshotJSON,
		r.InputHash, r.State, r.SupersedesRequestID, r.SupersededByRequestID,
		r.ViewsDeadlineAt, r.JudgeStartedAt, r.JudgeDeadlineAt, r.FinishedAt,
		r.TFirstUsableViewMs, r.TFinalMs, r.ViewsCompletedInDeadline,
		r.JudgeCompletedInDeadline, boolInt(r.DangerFlagged),
	)
	if err != nil {
		return nil, fmt.Errorf("store: insert request: %w", err)
	}
	return r, nil
}

// InsertRequestView inserts a request_views row.
func (db *DB) InsertRequestView(requestID, seat, state string) (*RequestView, error) {
	v := &RequestView{ID: ids.NewID(), CreatedAt: nowUTC(), RequestID: requestID, Seat: seat, State: state}
	_, err := db.db.Exec(
		`INSERT INTO request_views (id, created_at, request_id, seat, state, included_in_judge)
		 VALUES (?, ?, ?, ?, ?, 0)`,
		v.ID, v.CreatedAt, v.RequestID, v.Seat, v.State,
	)
	if err != nil {
		return nil, fmt.Errorf("store: insert request view: %w", err)
	}
	return v, nil
}

// InsertRequestJudge inserts the request_judge row for a request.
func (db *DB) InsertRequestJudge(requestID string) (*RequestJudge, error) {
	j := &RequestJudge{ID: ids.NewID(), CreatedAt: nowUTC(), RequestID: requestID,
		State: "pending", IncludedSeatsJSON: "[]", MissingSeatsJSON: "[]"}
	_, err := db.db.Exec(
		`INSERT INTO request_judge (id, created_at, request_id, state, included_seats_json, missing_seats_json, no_independent_views)
		 VALUES (?, ?, ?, ?, ?, ?, 0)`,
		j.ID, j.CreatedAt, j.RequestID, j.State, j.IncludedSeatsJSON, j.MissingSeatsJSON,
	)
	if err != nil {
		return nil, fmt.Errorf("store: insert request judge: %w", err)
	}
	return j, nil
}

// UpdateRequestViewState updates a view's state, response, inclusion, and completion time.
func (db *DB) UpdateRequestViewState(id, state string, responseID *string, included bool, completedAt *string) error {
	_, err := db.db.Exec(
		`UPDATE request_views SET state = ?, response_id = ?, included_in_judge = ?, completed_at = ? WHERE id = ?`,
		state, responseID, boolInt(included), completedAt, id,
	)
	if err != nil {
		return fmt.Errorf("store: update request view: %w", err)
	}
	return nil
}

// UpdateRequestState updates a request's state.
func (db *DB) UpdateRequestState(id, state string) error {
	_, err := db.db.Exec(`UPDATE requests SET state = ? WHERE id = ?`, state, id)
	if err != nil {
		return fmt.Errorf("store: update request state: %w", err)
	}
	return nil
}

// SetSuperseded marks a request superseded by another.
func (db *DB) SetSuperseded(id, byID string) error {
	_, err := db.db.Exec(
		`UPDATE requests SET state = 'superseded', superseded_by_request_id = ? WHERE id = ?`, byID, id)
	if err != nil {
		return fmt.Errorf("store: set superseded: %w", err)
	}
	return nil
}

// InsertRewrite inserts a rewrites row.
func (db *DB) InsertRewrite(requestID, sourceRef, instruction, responseID, outputText string) (*Rewrite, error) {
	r := &Rewrite{ID: ids.NewID(), CreatedAt: nowUTC(), RequestID: requestID,
		SourceRef: sourceRef, Instruction: instruction, ResponseID: responseID, OutputText: outputText}
	_, err := db.db.Exec(
		`INSERT INTO rewrites (id, created_at, request_id, source_ref, instruction, response_id, output_text)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		r.ID, r.CreatedAt, r.RequestID, r.SourceRef, r.Instruction, r.ResponseID, r.OutputText,
	)
	if err != nil {
		return nil, fmt.Errorf("store: insert rewrite: %w", err)
	}
	return r, nil
}

// InsertSentMessage inserts a sent_messages row.
func (db *DB) InsertSentMessage(requestID, threadID, text string, sourceRef *string) (*SentMessage, error) {
	m := &SentMessage{ID: ids.NewID(), CreatedAt: nowUTC(), RequestID: requestID,
		ThreadID: threadID, Text: text, SourceRef: sourceRef, SentAt: nowUTC()}
	_, err := db.db.Exec(
		`INSERT INTO sent_messages (id, created_at, request_id, thread_id, text, source_ref, sent_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		m.ID, m.CreatedAt, m.RequestID, m.ThreadID, m.Text, m.SourceRef, m.SentAt,
	)
	if err != nil {
		return nil, fmt.Errorf("store: insert sent message: %w", err)
	}
	return m, nil
}

// ListRequestsByThread returns requests for a thread, newest first.
func (db *DB) ListRequestsByThread(threadID string) ([]*Request, error) {
	rows, err := db.db.Query(`SELECT `+requestCols+` FROM requests WHERE thread_id = ? ORDER BY created_at DESC`, threadID)
	if err != nil {
		return nil, fmt.Errorf("store: list requests: %w", err)
	}
	defer rows.Close()
	var out []*Request
	for rows.Next() {
		r, err := scanRequest(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// GetRequest returns the full aggregate (request + views + judge + rewrites + sent messages).
func (db *DB) GetRequest(id string) (*RequestAggregate, error) {
	row := db.db.QueryRow(`SELECT `+requestCols+` FROM requests WHERE id = ?`, id)
	req, err := scanRequest(row)
	if err != nil {
		return nil, err
	}
	agg := &RequestAggregate{Request: req}

	vrows, err := db.db.Query(`SELECT id, created_at, request_id, seat, response_id, state,
		included_in_judge, completed_at FROM request_views WHERE request_id = ? ORDER BY seat`, id)
	if err != nil {
		return nil, fmt.Errorf("store: list views: %w", err)
	}
	for vrows.Next() {
		var v RequestView
		var included int
		if err := vrows.Scan(&v.ID, &v.CreatedAt, &v.RequestID, &v.Seat,
			&v.ResponseID, &v.State, &included, &v.CompletedAt); err != nil {
			vrows.Close()
			return nil, fmt.Errorf("store: scan view: %w", err)
		}
		v.IncludedInJudge = included != 0
		agg.Views = append(agg.Views, &v)
	}
	vrows.Close()
	if err := vrows.Err(); err != nil {
		return nil, fmt.Errorf("store: views rows: %w", err)
	}

	jrow := db.db.QueryRow(`SELECT id, created_at, request_id, response_id, state,
		included_seats_json, missing_seats_json, no_independent_views
		FROM request_judge WHERE request_id = ?`, id)
	var j RequestJudge
	var noViews int
	if err := jrow.Scan(&j.ID, &j.CreatedAt, &j.RequestID, &j.ResponseID, &j.State,
		&j.IncludedSeatsJSON, &j.MissingSeatsJSON, &noViews); err != nil {
		return nil, fmt.Errorf("store: scan judge: %w", err)
	}
	j.NoIndependentViews = noViews != 0
	agg.Judge = &j

	rrows, err := db.db.Query(`SELECT id, created_at, request_id, source_ref, instruction,
		response_id, output_text FROM rewrites WHERE request_id = ? ORDER BY created_at`, id)
	if err != nil {
		return nil, fmt.Errorf("store: list rewrites: %w", err)
	}
	for rrows.Next() {
		var r Rewrite
		if err := rrows.Scan(&r.ID, &r.CreatedAt, &r.RequestID, &r.SourceRef,
			&r.Instruction, &r.ResponseID, &r.OutputText); err != nil {
			rrows.Close()
			return nil, fmt.Errorf("store: scan rewrite: %w", err)
		}
		agg.Rewrites = append(agg.Rewrites, &r)
	}
	rrows.Close()
	if err := rrows.Err(); err != nil {
		return nil, fmt.Errorf("store: rewrites rows: %w", err)
	}

	srows, err := db.db.Query(`SELECT id, created_at, request_id, thread_id, text,
		source_ref, sent_at FROM sent_messages WHERE request_id = ? ORDER BY created_at`, id)
	if err != nil {
		return nil, fmt.Errorf("store: list sent: %w", err)
	}
	for srows.Next() {
		var m SentMessage
		if err := srows.Scan(&m.ID, &m.CreatedAt, &m.RequestID, &m.ThreadID,
			&m.Text, &m.SourceRef, &m.SentAt); err != nil {
			srows.Close()
			return nil, fmt.Errorf("store: scan sent: %w", err)
		}
		agg.Sent = append(agg.Sent, &m)
	}
	srows.Close()
	if err := srows.Err(); err != nil {
		return nil, fmt.Errorf("store: sent rows: %w", err)
	}
	return agg, nil
}

func scanRequest(row packRow) (*Request, error) {
	var r Request
	var danger int
	if err := row.Scan(&r.ID, &r.CreatedAt, &r.ThreadID, &r.CardID, &r.PackID,
		&r.SnapshotJSON, &r.InputHash, &r.State, &r.SupersedesRequestID,
		&r.SupersededByRequestID, &r.ViewsDeadlineAt, &r.JudgeStartedAt,
		&r.JudgeDeadlineAt, &r.FinishedAt, &r.TFirstUsableViewMs, &r.TFinalMs,
		&r.ViewsCompletedInDeadline, &r.JudgeCompletedInDeadline, &danger); err != nil {
		return nil, fmt.Errorf("store: scan request: %w", err)
	}
	r.DangerFlagged = danger != 0
	return &r, nil
}
