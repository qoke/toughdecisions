package store

import (
	"errors"
	"fmt"

	"github.com/qoke/toughdecisions/internal/ids"
)

// CoverageRow is a row in the issue_coverage table.
type CoverageRow struct {
	ID                string
	CreatedAt         string
	RunID             string
	CaseID            string
	CouncilLabel      string
	IssueID           string
	IssueText         string
	IsPlanted         bool
	NoticedByJSON     string
	UnsupportedByJSON string
	JudgeOutcome      string
}

const coverageCols = `id, created_at, run_id, case_id, council_label, issue_id,
	issue_text, is_planted, noticed_by_json, unsupported_by_json, judge_outcome`

// InsertCoverage inserts one issue-coverage row, assigning id/created_at
// when empty.
func (db *DB) InsertCoverage(c *CoverageRow) (*CoverageRow, error) {
	if c.RunID == "" {
		return nil, errors.New("store: insert coverage: empty run_id")
	}
	if c.CaseID == "" {
		return nil, errors.New("store: insert coverage: empty case_id")
	}
	row := &CoverageRow{
		ID:                ids.NewID(),
		CreatedAt:         nowUTC(),
		RunID:             c.RunID,
		CaseID:            c.CaseID,
		CouncilLabel:      c.CouncilLabel,
		IssueID:           c.IssueID,
		IssueText:         c.IssueText,
		IsPlanted:         c.IsPlanted,
		NoticedByJSON:     orDefault(c.NoticedByJSON, "[]"),
		UnsupportedByJSON: orDefault(c.UnsupportedByJSON, "[]"),
		JudgeOutcome:      orDefault(c.JudgeOutcome, "na"),
	}
	if _, err := db.db.Exec(
		`INSERT INTO issue_coverage (`+coverageCols+`) VALUES
		 (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		row.ID, row.CreatedAt, row.RunID, row.CaseID, row.CouncilLabel,
		row.IssueID, row.IssueText, boolInt(row.IsPlanted), row.NoticedByJSON,
		row.UnsupportedByJSON, row.JudgeOutcome,
	); err != nil {
		return nil, fmt.Errorf("store: insert coverage: %w", err)
	}
	return row, nil
}

// GetCoverage selects a coverage row by id.
func (db *DB) GetCoverage(id string) (*CoverageRow, error) {
	row := db.db.QueryRow(`SELECT `+coverageCols+` FROM issue_coverage WHERE id = ?`, id)
	return scanCoverage(row)
}

// ListCoverageByRun lists all coverage rows for one run ordered by case and
// issue.
func (db *DB) ListCoverageByRun(runID string) ([]*CoverageRow, error) {
	rows, err := db.db.Query(
		`SELECT `+coverageCols+` FROM issue_coverage WHERE run_id = ?
		 ORDER BY case_id, issue_id`, runID)
	if err != nil {
		return nil, fmt.Errorf("store: list coverage: %w", err)
	}
	defer rows.Close()
	var out []*CoverageRow
	for rows.Next() {
		var c CoverageRow
		var planted int
		if err := rows.Scan(&c.ID, &c.CreatedAt, &c.RunID, &c.CaseID,
			&c.CouncilLabel, &c.IssueID, &c.IssueText, &planted,
			&c.NoticedByJSON, &c.UnsupportedByJSON, &c.JudgeOutcome); err != nil {
			return nil, fmt.Errorf("store: scan coverage: %w", err)
		}
		c.IsPlanted = planted != 0
		out = append(out, &c)
	}
	return out, rows.Err()
}

func scanCoverage(row packRow) (*CoverageRow, error) {
	var c CoverageRow
	var planted int
	if err := row.Scan(&c.ID, &c.CreatedAt, &c.RunID, &c.CaseID,
		&c.CouncilLabel, &c.IssueID, &c.IssueText, &planted,
		&c.NoticedByJSON, &c.UnsupportedByJSON, &c.JudgeOutcome); err != nil {
		return nil, fmt.Errorf("store: scan coverage: %w", err)
	}
	c.IsPlanted = planted != 0
	return &c, nil
}
