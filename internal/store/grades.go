package store

import (
	"database/sql"
	"errors"
	"fmt"

	"github.com/qoke/toughdecisions/internal/ids"
)

// Grade is a row in the grades table.
type Grade struct {
	ID               string
	CreatedAt        string
	CacheKey         string
	ResponseID       string
	GraderConfigHash string
	RubricHash       string
	ScoresJSON       string
	PassagesJSON     string
	WeaknessJSON     *string
	FlagsJSON        string
	NotesCheckJSON   string
	RawJSON          string
	RunID            *string
}

const gradeCols = `id, created_at, cache_key, response_id, grader_config_hash,
	rubric_hash, scores_json, passages_json, weakness_json, flags_json,
	notes_check_json, raw_json, run_id`

// InsertGrade inserts a grade row. cache_key is UNIQUE: concurrent same-key
// inserts must reuse the existing row (GetGradeByCacheKey) per R-14.
func (db *DB) InsertGrade(g *Grade) (*Grade, error) {
	if g.CacheKey == "" {
		return nil, errors.New("store: insert grade: empty cache_key")
	}
	if g.ResponseID == "" {
		return nil, errors.New("store: insert grade: empty response_id")
	}
	row := &Grade{
		ID:               ids.NewID(),
		CreatedAt:        nowUTC(),
		CacheKey:         g.CacheKey,
		ResponseID:       g.ResponseID,
		GraderConfigHash: g.GraderConfigHash,
		RubricHash:       g.RubricHash,
		ScoresJSON:       orDefault(g.ScoresJSON, "{}"),
		PassagesJSON:     orDefault(g.PassagesJSON, "{}"),
		WeaknessJSON:     g.WeaknessJSON,
		FlagsJSON:        orDefault(g.FlagsJSON, "[]"),
		NotesCheckJSON:   orDefault(g.NotesCheckJSON, "{}"),
		RawJSON:          orDefault(g.RawJSON, "{}"),
		RunID:            g.RunID,
	}
	if _, err := db.db.Exec(
		`INSERT INTO grades (`+gradeCols+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		row.ID, row.CreatedAt, row.CacheKey, row.ResponseID, row.GraderConfigHash,
		row.RubricHash, row.ScoresJSON, row.PassagesJSON, row.WeaknessJSON,
		row.FlagsJSON, row.NotesCheckJSON, row.RawJSON, row.RunID,
	); err != nil {
		return nil, fmt.Errorf("store: insert grade: %w", err)
	}
	return row, nil
}

// GetGrade selects a grade by id.
func (db *DB) GetGrade(id string) (*Grade, error) {
	row := db.db.QueryRow(`SELECT `+gradeCols+` FROM grades WHERE id = ?`, id)
	return scanGrade(row)
}

// GetGradeByCacheKey selects a grade by cache_key.
func (db *DB) GetGradeByCacheKey(cacheKey string) (*Grade, error) {
	row := db.db.QueryRow(`SELECT `+gradeCols+` FROM grades WHERE cache_key = ?`, cacheKey)
	return scanGrade(row)
}

func scanGrade(row packRow) (*Grade, error) {
	var g Grade
	if err := row.Scan(&g.ID, &g.CreatedAt, &g.CacheKey, &g.ResponseID,
		&g.GraderConfigHash, &g.RubricHash, &g.ScoresJSON, &g.PassagesJSON,
		&g.WeaknessJSON, &g.FlagsJSON, &g.NotesCheckJSON, &g.RawJSON, &g.RunID); err != nil {
		return nil, fmt.Errorf("store: scan grade: %w", err)
	}
	return &g, nil
}

// Flag is a row in the flags table.
type Flag struct {
	ID             string
	CreatedAt      string
	GradeID        string
	ResponseID     string
	Type           string
	Passage        string
	Violated       string
	Status         string
	ResolutionNote *string
	RunID          *string
}

const flagCols = `id, created_at, grade_id, response_id, type, passage,
	violated, status, resolution_note, run_id`

// InsertFlag inserts a flag row with status open by default.
func (db *DB) InsertFlag(f *Flag) (*Flag, error) {
	if f.GradeID == "" {
		return nil, errors.New("store: insert flag: empty grade_id")
	}
	if f.ResponseID == "" {
		return nil, errors.New("store: insert flag: empty response_id")
	}
	row := &Flag{
		ID:             ids.NewID(),
		CreatedAt:      nowUTC(),
		GradeID:        f.GradeID,
		ResponseID:     f.ResponseID,
		Type:           f.Type,
		Passage:        f.Passage,
		Violated:       f.Violated,
		Status:         orDefault(f.Status, "open"),
		ResolutionNote: f.ResolutionNote,
		RunID:          f.RunID,
	}
	if _, err := db.db.Exec(
		`INSERT INTO flags (`+flagCols+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		row.ID, row.CreatedAt, row.GradeID, row.ResponseID, row.Type, row.Passage,
		row.Violated, row.Status, row.ResolutionNote, row.RunID,
	); err != nil {
		return nil, fmt.Errorf("store: insert flag: %w", err)
	}
	return row, nil
}

// GetFlag selects a flag by id.
func (db *DB) GetFlag(id string) (*Flag, error) {
	row := db.db.QueryRow(`SELECT `+flagCols+` FROM flags WHERE id = ?`, id)
	return scanFlag(row)
}

// ListFlagsByStatus lists flags with the given status, newest first.
func (db *DB) ListFlagsByStatus(status string) ([]*Flag, error) {
	rows, err := db.db.Query(`SELECT `+flagCols+` FROM flags WHERE status = ? ORDER BY created_at DESC, id DESC`, status)
	if err != nil {
		return nil, fmt.Errorf("store: list flags: %w", err)
	}
	defer rows.Close()
	var out []*Flag
	for rows.Next() {
		var f Flag
		if err := rows.Scan(&f.ID, &f.CreatedAt, &f.GradeID, &f.ResponseID, &f.Type,
			&f.Passage, &f.Violated, &f.Status, &f.ResolutionNote, &f.RunID); err != nil {
			return nil, fmt.Errorf("store: scan flag: %w", err)
		}
		out = append(out, &f)
	}
	return out, rows.Err()
}

// UpdateFlagStatus transitions a flag's status with an optional note.
func (db *DB) UpdateFlagStatus(id, status string, note *string) error {
	res, err := db.db.Exec(`UPDATE flags SET status=?, resolution_note=? WHERE id=?`, status, note, id)
	if err != nil {
		return fmt.Errorf("store: update flag status: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("store: update flag status rows: %w", err)
	}
	if n == 0 {
		return fmt.Errorf("store: flag %s not found: %w", id, sql.ErrNoRows)
	}
	return nil
}

func scanFlag(row packRow) (*Flag, error) {
	var f Flag
	if err := row.Scan(&f.ID, &f.CreatedAt, &f.GradeID, &f.ResponseID, &f.Type,
		&f.Passage, &f.Violated, &f.Status, &f.ResolutionNote, &f.RunID); err != nil {
		return nil, fmt.Errorf("store: scan flag: %w", err)
	}
	return &f, nil
}
