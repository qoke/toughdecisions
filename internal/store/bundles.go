package store

import (
	"errors"
	"fmt"

	"github.com/qoke/toughdecisions/internal/ids"
)

// Bundle is a row in the bundles table.
type Bundle struct {
	ID                string
	CreatedAt         string
	BundleKey         string
	CaseID            string
	Kind              string
	ViewsJSON         string
	ManipulationsJSON string
	BundleHash        string
	SourcePackID      *string
}

const bundleCols = `id, created_at, bundle_key, case_id, kind, views_json,
	manipulations_json, bundle_hash, source_pack_id`

// UpsertBundle inserts a bundle or updates the existing row with the same
// bundle_key. It returns the stored row.
func (db *DB) UpsertBundle(b *Bundle) (*Bundle, error) {
	if b.BundleKey == "" {
		return nil, errors.New("store: upsert bundle: empty bundle_key")
	}
	existing, err := db.GetBundleByKey(b.BundleKey)
	if err == nil {
		_, err = db.db.Exec(
			`UPDATE bundles SET case_id=?, kind=?, views_json=?, manipulations_json=?,
			 bundle_hash=?, source_pack_id=? WHERE id=?`,
			orKeep(existing.CaseID, b.CaseID), orKeep(existing.Kind, b.Kind),
			orKeep(existing.ViewsJSON, b.ViewsJSON),
			orKeep(existing.ManipulationsJSON, b.ManipulationsJSON),
			orKeep(existing.BundleHash, b.BundleHash),
			orKeepPtr(existing.SourcePackID, b.SourcePackID), existing.ID,
		)
		if err != nil {
			return nil, fmt.Errorf("store: update bundle: %w", err)
		}
		return db.GetBundle(existing.ID)
	}
	row := &Bundle{
		ID:                ids.NewID(),
		CreatedAt:         nowUTC(),
		BundleKey:         b.BundleKey,
		CaseID:            b.CaseID,
		Kind:              orDefault(b.Kind, "natural"),
		ViewsJSON:         orDefault(b.ViewsJSON, "{}"),
		ManipulationsJSON: orDefault(b.ManipulationsJSON, "[]"),
		BundleHash:        b.BundleHash,
		SourcePackID:      b.SourcePackID,
	}
	if _, err := db.db.Exec(
		`INSERT INTO bundles (`+bundleCols+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		row.ID, row.CreatedAt, row.BundleKey, row.CaseID, row.Kind, row.ViewsJSON,
		row.ManipulationsJSON, row.BundleHash, row.SourcePackID,
	); err != nil {
		return nil, fmt.Errorf("store: insert bundle: %w", err)
	}
	return row, nil
}

// GetBundle selects a bundle by id.
func (db *DB) GetBundle(id string) (*Bundle, error) {
	row := db.db.QueryRow(`SELECT `+bundleCols+` FROM bundles WHERE id = ?`, id)
	return scanBundle(row)
}

// GetBundleByKey selects a bundle by bundle_key.
func (db *DB) GetBundleByKey(key string) (*Bundle, error) {
	row := db.db.QueryRow(`SELECT `+bundleCols+` FROM bundles WHERE bundle_key = ?`, key)
	return scanBundle(row)
}

// ListBundlesByCase lists bundles of one case ordered by key.
func (db *DB) ListBundlesByCase(caseID string) ([]*Bundle, error) {
	rows, err := db.db.Query(`SELECT `+bundleCols+` FROM bundles WHERE case_id = ? ORDER BY bundle_key`, caseID)
	if err != nil {
		return nil, fmt.Errorf("store: list bundles: %w", err)
	}
	defer rows.Close()
	var out []*Bundle
	for rows.Next() {
		var b Bundle
		if err := rows.Scan(&b.ID, &b.CreatedAt, &b.BundleKey, &b.CaseID, &b.Kind,
			&b.ViewsJSON, &b.ManipulationsJSON, &b.BundleHash, &b.SourcePackID); err != nil {
			return nil, fmt.Errorf("store: scan bundle: %w", err)
		}
		out = append(out, &b)
	}
	return out, rows.Err()
}

func scanBundle(row packRow) (*Bundle, error) {
	var b Bundle
	if err := row.Scan(&b.ID, &b.CreatedAt, &b.BundleKey, &b.CaseID, &b.Kind,
		&b.ViewsJSON, &b.ManipulationsJSON, &b.BundleHash, &b.SourcePackID); err != nil {
		return nil, fmt.Errorf("store: scan bundle: %w", err)
	}
	return &b, nil
}
