package store

import (
	"database/sql"
	"errors"
	"fmt"

	"github.com/qoke/toughdecisions/internal/ids"
)

// Candidate is a row in the candidates table.
type Candidate struct {
	ID                   string
	CreatedAt            string
	RunID                string
	CandidateKey         string
	Seat                 string
	ConfigJSON           string
	ConfigHash           string
	ScreenResultJSON     *string
	Finalist             bool
	CompareResultJSON    *string
	DownstreamResultJSON *string
	PromotionJSON        *string
}

// CandidateResults carries the nullable result columns updated as the
// harness steps complete.
type CandidateResults struct {
	ScreenResultJSON     *string
	Finalist             *bool
	CompareResultJSON    *string
	DownstreamResultJSON *string
	PromotionJSON        *string
}

const candidateCols = `id, created_at, run_id, candidate_key, seat, config_json,
	config_hash, screen_result_json, finalist, compare_result_json,
	downstream_result_json, promotion_json`

// UpsertCandidate inserts a candidate or refreshes the config of the
// existing row for the same (run_id, candidate_key). It returns the row.
func (db *DB) UpsertCandidate(runID, candidateKey, seat, configJSON, configHash string) (*Candidate, error) {
	if runID == "" {
		return nil, errors.New("store: upsert candidate: empty run_id")
	}
	if candidateKey == "" {
		return nil, errors.New("store: upsert candidate: empty candidate_key")
	}
	if existing, err := db.GetCandidateByKey(runID, candidateKey); err == nil {
		if _, err := db.db.Exec(
			`UPDATE candidates SET seat=?, config_json=?, config_hash=? WHERE id=?`,
			orKeep(existing.Seat, seat), orKeep(existing.ConfigJSON, configJSON),
			orKeep(existing.ConfigHash, configHash), existing.ID,
		); err != nil {
			return nil, fmt.Errorf("store: update candidate: %w", err)
		}
		return db.GetCandidate(existing.ID)
	}
	row := &Candidate{
		ID:           ids.NewID(),
		CreatedAt:    nowUTC(),
		RunID:        runID,
		CandidateKey: candidateKey,
		Seat:         seat,
		ConfigJSON:   orDefault(configJSON, "{}"),
		ConfigHash:   configHash,
	}
	if _, err := db.db.Exec(
		`INSERT INTO candidates (`+candidateCols+`) VALUES
		 (?, ?, ?, ?, ?, ?, ?, NULL, 0, NULL, NULL, NULL)`,
		row.ID, row.CreatedAt, row.RunID, row.CandidateKey, row.Seat,
		row.ConfigJSON, row.ConfigHash,
	); err != nil {
		return nil, fmt.Errorf("store: insert candidate: %w", err)
	}
	return row, nil
}

// GetCandidate selects a candidate by id.
func (db *DB) GetCandidate(id string) (*Candidate, error) {
	row := db.db.QueryRow(`SELECT `+candidateCols+` FROM candidates WHERE id = ?`, id)
	return scanCandidate(row)
}

// GetCandidateByKey selects a candidate by (run_id, candidate_key).
func (db *DB) GetCandidateByKey(runID, candidateKey string) (*Candidate, error) {
	row := db.db.QueryRow(
		`SELECT `+candidateCols+` FROM candidates WHERE run_id = ? AND candidate_key = ?`,
		runID, candidateKey)
	return scanCandidate(row)
}

// ListCandidatesByRun lists all candidates for one run ordered by key.
func (db *DB) ListCandidatesByRun(runID string) ([]*Candidate, error) {
	rows, err := db.db.Query(
		`SELECT `+candidateCols+` FROM candidates WHERE run_id = ? ORDER BY candidate_key`, runID)
	if err != nil {
		return nil, fmt.Errorf("store: list candidates: %w", err)
	}
	defer rows.Close()
	var out []*Candidate
	for rows.Next() {
		var c Candidate
		var finalist int
		if err := rows.Scan(&c.ID, &c.CreatedAt, &c.RunID, &c.CandidateKey,
			&c.Seat, &c.ConfigJSON, &c.ConfigHash, &c.ScreenResultJSON,
			&finalist, &c.CompareResultJSON, &c.DownstreamResultJSON,
			&c.PromotionJSON); err != nil {
			return nil, fmt.Errorf("store: scan candidate: %w", err)
		}
		c.Finalist = finalist != 0
		out = append(out, &c)
	}
	return out, rows.Err()
}

// UpdateCandidateResults stores step results; nil fields are left unchanged
// except Finalist (nil = unchanged).
func (db *DB) UpdateCandidateResults(id string, r *CandidateResults) error {
	existing, err := db.GetCandidate(id)
	if err != nil {
		return err
	}
	finalist := existing.Finalist
	if r.Finalist != nil {
		finalist = *r.Finalist
	}
	res, err := db.db.Exec(
		`UPDATE candidates SET screen_result_json=?, finalist=?,
		 compare_result_json=?, downstream_result_json=?, promotion_json=? WHERE id=?`,
		orKeepPtr(existing.ScreenResultJSON, r.ScreenResultJSON), boolInt(finalist),
		orKeepPtr(existing.CompareResultJSON, r.CompareResultJSON),
		orKeepPtr(existing.DownstreamResultJSON, r.DownstreamResultJSON),
		orKeepPtr(existing.PromotionJSON, r.PromotionJSON), id,
	)
	if err != nil {
		return fmt.Errorf("store: update candidate results: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("store: update candidate results rows: %w", err)
	}
	if n == 0 {
		return fmt.Errorf("store: candidate %s not found: %w", id, sql.ErrNoRows)
	}
	return nil
}

func scanCandidate(row packRow) (*Candidate, error) {
	var c Candidate
	var finalist int
	if err := row.Scan(&c.ID, &c.CreatedAt, &c.RunID, &c.CandidateKey,
		&c.Seat, &c.ConfigJSON, &c.ConfigHash, &c.ScreenResultJSON,
		&finalist, &c.CompareResultJSON, &c.DownstreamResultJSON,
		&c.PromotionJSON); err != nil {
		return nil, fmt.Errorf("store: scan candidate: %w", err)
	}
	c.Finalist = finalist != 0
	return &c, nil
}

// LatestCandidateRunID returns the most recently created candidates row's
// run id that already holds a compare result. CLI tests use it to resolve
// the compare run id for downstream (screen rows have no compare result).
func (db *DB) LatestCandidateRunID() (string, error) {
	var id string
	err := db.db.QueryRow(`SELECT run_id FROM candidates WHERE compare_result_json IS NOT NULL ORDER BY created_at DESC LIMIT 1`).Scan(&id)
	if err != nil {
		return "", fmt.Errorf("store: latest candidate run: %w", err)
	}
	return id, nil
}
