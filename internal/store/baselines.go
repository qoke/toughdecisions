package store

import (
	"errors"
	"fmt"

	"github.com/qoke/toughdecisions/internal/ids"
)

// Baseline is a row in the baselines table: the qualification output cached
// at publish for one pack seat and case.
type Baseline struct {
	ID         string
	CreatedAt  string
	PackID     string
	Seat       string
	CaseID     string
	BundleID   *string
	ResponseID string
}

const baselineCols = `id, created_at, pack_id, seat, case_id, bundle_id, response_id`

// UpsertBaseline inserts a baseline or moves the existing row for the same
// (pack_id, seat, case_id) to the new response. It returns the stored row.
func (db *DB) UpsertBaseline(packID, seat, caseID string, bundleID *string, responseID string) (*Baseline, error) {
	if packID == "" {
		return nil, errors.New("store: upsert baseline: empty pack_id")
	}
	if caseID == "" {
		return nil, errors.New("store: upsert baseline: empty case_id")
	}
	if responseID == "" {
		return nil, errors.New("store: upsert baseline: empty response_id")
	}
	if existing, err := db.GetBaseline(packID, seat, caseID); err == nil {
		if _, err := db.db.Exec(
			`UPDATE baselines SET bundle_id=?, response_id=? WHERE id=?`,
			bundleID, responseID, existing.ID,
		); err != nil {
			return nil, fmt.Errorf("store: update baseline: %w", err)
		}
		return db.GetBaseline(packID, seat, caseID)
	}
	row := &Baseline{
		ID:         ids.NewID(),
		CreatedAt:  nowUTC(),
		PackID:     packID,
		Seat:       seat,
		CaseID:     caseID,
		BundleID:   bundleID,
		ResponseID: responseID,
	}
	if _, err := db.db.Exec(
		`INSERT INTO baselines (`+baselineCols+`) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		row.ID, row.CreatedAt, row.PackID, row.Seat, row.CaseID, row.BundleID, row.ResponseID,
	); err != nil {
		return nil, fmt.Errorf("store: insert baseline: %w", err)
	}
	return row, nil
}

// GetBaseline selects a baseline by (pack_id, seat, case_id).
func (db *DB) GetBaseline(packID, seat, caseID string) (*Baseline, error) {
	row := db.db.QueryRow(
		`SELECT `+baselineCols+` FROM baselines WHERE pack_id = ? AND seat = ? AND case_id = ?`,
		packID, seat, caseID)
	return scanBaseline(row)
}

func scanBaseline(row packRow) (*Baseline, error) {
	var b Baseline
	if err := row.Scan(&b.ID, &b.CreatedAt, &b.PackID, &b.Seat, &b.CaseID,
		&b.BundleID, &b.ResponseID); err != nil {
		return nil, fmt.Errorf("store: scan baseline: %w", err)
	}
	return &b, nil
}
