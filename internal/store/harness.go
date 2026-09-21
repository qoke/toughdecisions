package store

import (
	"database/sql"
	"errors"
	"fmt"

	"github.com/qoke/toughdecisions/internal/ids"
)

// HarnessRun is a row in the harness_runs table.
type HarnessRun struct {
	ID          string
	CreatedAt   string
	Kind        string
	PackID      string
	ParamsJSON  string
	Status      string
	StartedAt   string
	FinishedAt  *string
	ReportPath  *string
	SummaryJSON *string
}

const harnessRunCols = `id, created_at, kind, pack_id, params_json, status,
	started_at, finished_at, report_path, summary_json`

// CreateHarnessRun inserts a run in status created and returns it.
func (db *DB) CreateHarnessRun(kind, packID, paramsJSON string) (*HarnessRun, error) {
	if kind == "" {
		return nil, errors.New("store: create harness run: empty kind")
	}
	row := &HarnessRun{
		ID:         ids.NewID(),
		CreatedAt:  nowUTC(),
		Kind:       kind,
		PackID:     packID,
		ParamsJSON: orDefault(paramsJSON, "{}"),
		Status:     "created",
		StartedAt:  nowUTC(),
	}
	if _, err := db.db.Exec(
		`INSERT INTO harness_runs (`+harnessRunCols+`) VALUES
		 (?, ?, ?, ?, ?, ?, ?, NULL, NULL, NULL)`,
		row.ID, row.CreatedAt, row.Kind, row.PackID, row.ParamsJSON,
		row.Status, row.StartedAt,
	); err != nil {
		return nil, fmt.Errorf("store: create harness run: %w", err)
	}
	return row, nil
}

// GetHarnessRun selects a run by id.
func (db *DB) GetHarnessRun(id string) (*HarnessRun, error) {
	row := db.db.QueryRow(`SELECT `+harnessRunCols+` FROM harness_runs WHERE id = ?`, id)
	return scanHarnessRun(row)
}

// UpdateHarnessRunStatus sets a run's status.
func (db *DB) UpdateHarnessRunStatus(id, status string) error {
	res, err := db.db.Exec(`UPDATE harness_runs SET status=? WHERE id=?`, status, id)
	if err != nil {
		return fmt.Errorf("store: update harness run status: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("store: update harness run status rows: %w", err)
	}
	if n == 0 {
		return fmt.Errorf("store: harness run %s not found: %w", id, sql.ErrNoRows)
	}
	return nil
}

// FinishHarnessRun marks a run finished with status, report path, summary,
// and finish time.
func (db *DB) FinishHarnessRun(id, status string, reportPath, summaryJSON, finishedAt *string) error {
	if finishedAt == nil {
		t := nowUTC()
		finishedAt = &t
	}
	res, err := db.db.Exec(
		`UPDATE harness_runs SET status=?, report_path=?, summary_json=?, finished_at=? WHERE id=?`,
		status, reportPath, summaryJSON, finishedAt, id,
	)
	if err != nil {
		return fmt.Errorf("store: finish harness run: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("store: finish harness run rows: %w", err)
	}
	if n == 0 {
		return fmt.Errorf("store: harness run %s not found: %w", id, sql.ErrNoRows)
	}
	return nil
}

func scanHarnessRun(row packRow) (*HarnessRun, error) {
	var r HarnessRun
	if err := row.Scan(&r.ID, &r.CreatedAt, &r.Kind, &r.PackID, &r.ParamsJSON,
		&r.Status, &r.StartedAt, &r.FinishedAt, &r.ReportPath, &r.SummaryJSON); err != nil {
		return nil, fmt.Errorf("store: scan harness run: %w", err)
	}
	return &r, nil
}

// SentinelResult is a row in the sentinel_results table.
type SentinelResult struct {
	ID                 string
	CreatedAt          string
	RunID              string
	Seat               string
	CaseID             string
	FreshResponseID    string
	BaselineResponseID string
	VerdictsJSON       string
	LatencyMs          int64
	Regression         bool
}

const sentinelCols = `id, created_at, run_id, seat, case_id, fresh_response_id,
	baseline_response_id, verdicts_json, latency_ms, regression`

// InsertSentinelResult inserts one sentinel comparison row.
func (db *DB) InsertSentinelResult(s *SentinelResult) (*SentinelResult, error) {
	if s.RunID == "" {
		return nil, errors.New("store: insert sentinel result: empty run_id")
	}
	if s.CaseID == "" {
		return nil, errors.New("store: insert sentinel result: empty case_id")
	}
	row := &SentinelResult{
		ID:                 ids.NewID(),
		CreatedAt:          nowUTC(),
		RunID:              s.RunID,
		Seat:               s.Seat,
		CaseID:             s.CaseID,
		FreshResponseID:    s.FreshResponseID,
		BaselineResponseID: s.BaselineResponseID,
		VerdictsJSON:       orDefault(s.VerdictsJSON, "{}"),
		LatencyMs:          s.LatencyMs,
		Regression:         s.Regression,
	}
	if _, err := db.db.Exec(
		`INSERT INTO sentinel_results (`+sentinelCols+`) VALUES
		 (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		row.ID, row.CreatedAt, row.RunID, row.Seat, row.CaseID,
		row.FreshResponseID, row.BaselineResponseID, row.VerdictsJSON,
		row.LatencyMs, boolInt(row.Regression),
	); err != nil {
		return nil, fmt.Errorf("store: insert sentinel result: %w", err)
	}
	return row, nil
}

// GetSentinelResult selects a sentinel result by id.
func (db *DB) GetSentinelResult(id string) (*SentinelResult, error) {
	row := db.db.QueryRow(`SELECT `+sentinelCols+` FROM sentinel_results WHERE id = ?`, id)
	return scanSentinel(row)
}

// ListSentinelResultsByRun lists all sentinel results for one run.
func (db *DB) ListSentinelResultsByRun(runID string) ([]*SentinelResult, error) {
	rows, err := db.db.Query(
		`SELECT `+sentinelCols+` FROM sentinel_results WHERE run_id = ?
		 ORDER BY seat, case_id`, runID)
	if err != nil {
		return nil, fmt.Errorf("store: list sentinel results: %w", err)
	}
	defer rows.Close()
	var out []*SentinelResult
	for rows.Next() {
		var s SentinelResult
		var regression int
		if err := rows.Scan(&s.ID, &s.CreatedAt, &s.RunID, &s.Seat, &s.CaseID,
			&s.FreshResponseID, &s.BaselineResponseID, &s.VerdictsJSON,
			&s.LatencyMs, &regression); err != nil {
			return nil, fmt.Errorf("store: scan sentinel result: %w", err)
		}
		s.Regression = regression != 0
		out = append(out, &s)
	}
	return out, rows.Err()
}

func scanSentinel(row packRow) (*SentinelResult, error) {
	var s SentinelResult
	var regression int
	if err := row.Scan(&s.ID, &s.CreatedAt, &s.RunID, &s.Seat, &s.CaseID,
		&s.FreshResponseID, &s.BaselineResponseID, &s.VerdictsJSON,
		&s.LatencyMs, &regression); err != nil {
		return nil, fmt.Errorf("store: scan sentinel result: %w", err)
	}
	s.Regression = regression != 0
	return &s, nil
}
