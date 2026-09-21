package store

import (
	"database/sql"
	"fmt"

	"github.com/qoke/toughdecisions/internal/ids"
)

// Pack is a row in the packs table.
type Pack struct {
	ID                 string
	CreatedAt          string
	Status             string
	SeatsJSON          string
	PromptPackHash     string
	Notes              string
	PublishedFromRunID *string
	ActivatedAt        *string
}

// InsertPack inserts a pack row with a fresh id and timestamps.
func (db *DB) InsertPack(status, seatsJSON, promptPackHash, notes string) (*Pack, error) {
	p := &Pack{
		ID:             ids.NewID(),
		CreatedAt:      nowUTC(),
		Status:         status,
		SeatsJSON:      seatsJSON,
		PromptPackHash: promptPackHash,
		Notes:          notes,
	}
	_, err := db.db.Exec(
		`INSERT INTO packs (id, created_at, status, seats_json, prompt_pack_hash, notes)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		p.ID, p.CreatedAt, p.Status, p.SeatsJSON, p.PromptPackHash, p.Notes,
	)
	if err != nil {
		return nil, fmt.Errorf("store: insert pack: %w", err)
	}
	return p, nil
}

// GetPack selects a pack by id.
func (db *DB) GetPack(id string) (*Pack, error) {
	row := db.db.QueryRow(`SELECT id, created_at, status, seats_json, prompt_pack_hash,
		notes, published_from_run_id, activated_at FROM packs WHERE id = ?`, id)
	return scanPack(row)
}

// Active returns the pack with status 'active', or sql.ErrNoRows.
func (db *DB) Active() (*Pack, error) {
	row := db.db.QueryRow(`SELECT id, created_at, status, seats_json, prompt_pack_hash,
		notes, published_from_run_id, activated_at FROM packs WHERE status = 'active' LIMIT 1`)
	return scanPack(row)
}

// PacksByStatus lists packs with the given status, newest first.
func (db *DB) PacksByStatus(status string) ([]*Pack, error) {
	rows, err := db.db.Query(`SELECT id, created_at, status, seats_json, prompt_pack_hash,
		notes, published_from_run_id, activated_at FROM packs WHERE status = ?
		ORDER BY created_at DESC, id DESC`, status)
	if err != nil {
		return nil, fmt.Errorf("store: packs by status: %w", err)
	}
	defer rows.Close()
	var out []*Pack
	for rows.Next() {
		var p Pack
		if err := rows.Scan(&p.ID, &p.CreatedAt, &p.Status, &p.SeatsJSON,
			&p.PromptPackHash, &p.Notes, &p.PublishedFromRunID, &p.ActivatedAt); err != nil {
			return nil, fmt.Errorf("store: scan pack: %w", err)
		}
		out = append(out, &p)
	}
	return out, rows.Err()
}

// UpdatePackMeta sets published_from_run_id and/or activated_at on a pack.
// Keys present in meta are updated; absent keys are left unchanged.
func (db *DB) UpdatePackMeta(id string, meta map[string]any) error {
	if v, ok := meta["published_from_run_id"]; ok {
		if _, err := db.db.Exec(`UPDATE packs SET published_from_run_id = ? WHERE id = ?`, v, id); err != nil {
			return fmt.Errorf("store: update pack run id: %w", err)
		}
	}
	if v, ok := meta["activated_at"]; ok {
		if _, err := db.db.Exec(`UPDATE packs SET activated_at = ? WHERE id = ?`, v, id); err != nil {
			return fmt.Errorf("store: update pack activated_at: %w", err)
		}
	}
	return nil
}

// SetStatus transitions a pack's status.
func (db *DB) SetStatus(id, status string) error {
	res, err := db.db.Exec(`UPDATE packs SET status = ? WHERE id = ?`, status, id)
	if err != nil {
		return fmt.Errorf("store: set pack status: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("store: set pack status rows: %w", err)
	}
	if n == 0 {
		return fmt.Errorf("store: pack %s not found: %w", id, sql.ErrNoRows)
	}
	return nil
}

type packRow interface{ Scan(dest ...any) error }

func scanPack(row packRow) (*Pack, error) {
	var p Pack
	if err := row.Scan(&p.ID, &p.CreatedAt, &p.Status, &p.SeatsJSON,
		&p.PromptPackHash, &p.Notes, &p.PublishedFromRunID, &p.ActivatedAt); err != nil {
		return nil, fmt.Errorf("store: scan pack: %w", err)
	}
	return &p, nil
}
