package store

import (
	"database/sql"
	"errors"
	"fmt"

	"github.com/qoke/toughdecisions/internal/ids"
)

// PairwiseRow is a row in the pairwise table.
type PairwiseRow struct {
	ID                      string
	CreatedAt               string
	RunID                   string
	Purpose                 string
	CaseID                  string
	Seat                    string
	LeftResponseID          string
	RightResponseID         string
	GraderConfigHash        string
	LeftShownAs             string
	Verdict                 string
	Margin                  string
	VerdictLeftRight        string
	ConsequentialDifference string
	ReversalOfID            *string
	Final                   bool
}

const pairwiseCols = `id, created_at, run_id, purpose, case_id, seat,
	left_response_id, right_response_id, grader_config_hash, left_shown_as,
	verdict, margin, verdict_left_right, consequential_difference,
	reversal_of_id, final`

// InsertPairwise inserts a pairwise comparison row, assigning id/created_at
// when empty. The reversal attempt stores reversal_of_id pointing at the
// first attempt; the agreed row is marked final.
func (db *DB) InsertPairwise(p *PairwiseRow) (*PairwiseRow, error) {
	if p.RunID == "" {
		return nil, errors.New("store: insert pairwise: empty run_id")
	}
	if p.CaseID == "" {
		return nil, errors.New("store: insert pairwise: empty case_id")
	}
	row := &PairwiseRow{
		ID:                      ids.NewID(),
		CreatedAt:               nowUTC(),
		RunID:                   p.RunID,
		Purpose:                 p.Purpose,
		CaseID:                  p.CaseID,
		Seat:                    p.Seat,
		LeftResponseID:          p.LeftResponseID,
		RightResponseID:         p.RightResponseID,
		GraderConfigHash:        p.GraderConfigHash,
		LeftShownAs:             orDefault(p.LeftShownAs, "A"),
		Verdict:                 orDefault(p.Verdict, "unable"),
		Margin:                  orDefault(p.Margin, "close"),
		VerdictLeftRight:        orDefault(p.VerdictLeftRight, "unable"),
		ConsequentialDifference: p.ConsequentialDifference,
		ReversalOfID:            p.ReversalOfID,
		Final:                   p.Final,
	}
	if _, err := db.db.Exec(
		`INSERT INTO pairwise (`+pairwiseCols+`) VALUES
		 (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		row.ID, row.CreatedAt, row.RunID, row.Purpose, row.CaseID, row.Seat,
		row.LeftResponseID, row.RightResponseID, row.GraderConfigHash,
		row.LeftShownAs, row.Verdict, row.Margin, row.VerdictLeftRight,
		row.ConsequentialDifference, row.ReversalOfID, boolInt(row.Final),
	); err != nil {
		return nil, fmt.Errorf("store: insert pairwise: %w", err)
	}
	return row, nil
}

// GetPairwise selects a pairwise row by id.
func (db *DB) GetPairwise(id string) (*PairwiseRow, error) {
	row := db.db.QueryRow(`SELECT `+pairwiseCols+` FROM pairwise WHERE id = ?`, id)
	return scanPairwise(row)
}

// ListPairwiseByRun lists pairwise rows for one run and case, oldest first.
func (db *DB) ListPairwiseByRun(runID, caseID string) ([]*PairwiseRow, error) {
	rows, err := db.db.Query(
		`SELECT `+pairwiseCols+` FROM pairwise WHERE run_id = ? AND case_id = ?
		 ORDER BY created_at, id`, runID, caseID)
	if err != nil {
		return nil, fmt.Errorf("store: list pairwise: %w", err)
	}
	defer rows.Close()
	var out []*PairwiseRow
	for rows.Next() {
		var p PairwiseRow
		var final int
		if err := rows.Scan(&p.ID, &p.CreatedAt, &p.RunID, &p.Purpose, &p.CaseID,
			&p.Seat, &p.LeftResponseID, &p.RightResponseID, &p.GraderConfigHash,
			&p.LeftShownAs, &p.Verdict, &p.Margin, &p.VerdictLeftRight,
			&p.ConsequentialDifference, &p.ReversalOfID, &final); err != nil {
			return nil, fmt.Errorf("store: scan pairwise: %w", err)
		}
		p.Final = final != 0
		out = append(out, &p)
	}
	return out, rows.Err()
}

func scanPairwise(row packRow) (*PairwiseRow, error) {
	var p PairwiseRow
	var final int
	if err := row.Scan(&p.ID, &p.CreatedAt, &p.RunID, &p.Purpose, &p.CaseID,
		&p.Seat, &p.LeftResponseID, &p.RightResponseID, &p.GraderConfigHash,
		&p.LeftShownAs, &p.Verdict, &p.Margin, &p.VerdictLeftRight,
		&p.ConsequentialDifference, &p.ReversalOfID, &final); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
		return nil, fmt.Errorf("store: scan pairwise: %w", err)
	}
	p.Final = final != 0
	return &p, nil
}
