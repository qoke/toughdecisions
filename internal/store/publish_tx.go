package store

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/qoke/toughdecisions/internal/ids"
)

// execer covers *sql.DB and *sql.Tx for the tx-aware helpers below.
type execer interface {
	Exec(query string, args ...any) (sql.Result, error)
	QueryRow(query string, args ...any) *sql.Row
}

// PackTxSeed carries the inserts the atomic publish transaction performs.
type PackTxSeed struct {
	// New pack to insert; empty NewStatus skips pack activation
	// (baselines-only). NewID pins the new pack's id so callers can
	// reference it in Baselines/Bundles built before the transaction.
	NewID     string
	NewStatus string
	NewSeats  string
	NewHash   string
	NewNotes  string
	NewRunID  *string
	// Baselines keyed by an opaque caller key so judge bundle baselines can
	// share a (pack, seat, case_id) triple without colliding in the map.
	Baselines []BaselineSeed
	// Bundles upserted as natural snapshots with source_pack_id set later.
	Bundles []BundleSeed
}

// BaselineSeed is one baseline row for the atomic transaction.
type BaselineSeed struct {
	Key        string
	PackID     string
	Seat       string
	CaseID     string
	BundleID   *string
	ResponseID string
}

// BundleSeed is one natural-bundle upsert for the atomic transaction.
// ID pins the row id so callers can reference the bundle in baseline
// seeds built before the transaction runs.
type BundleSeed struct {
	ID                string
	Key               string
	BundleKey         string
	CaseID            string
	ViewsJSON         string
	ManipulationsJSON string
	BundleHash        string
	SourcePackID      *string
}

// PublishAtomicResult reports the pack the transaction activated (or the
// untouched active pack for baselines-only) plus the new pack id.
type PublishAtomicResult struct {
	ActiveID  string
	CreatedID string
}

// PublishAtomic swaps the active pack and inserts every baseline and
// natural-bundle row in ONE transaction. Baselines-only mode passes an
// empty NewStatus and skips pack activation; rollback-after-publish keeps
// working because the previous active pack becomes previous in the same tx.
func (db *DB) PublishAtomic(seed PackTxSeed) (*PublishAtomicResult, error) {
	res := &PublishAtomicResult{}
	err := db.WithTx(context.Background(), func(tx *sql.Tx) error {
		activeID := ""
		if row := tx.QueryRow(`SELECT id FROM packs WHERE status='active' LIMIT 1`); row != nil {
			_ = row.Scan(&activeID)
		}
		res.ActiveID = activeID
		if seed.NewStatus != "" {
			if _, err := tx.Exec(`UPDATE packs SET status='retired' WHERE status='previous'`); err != nil {
				return fmt.Errorf("store: publish retired: %w", err)
			}
			if activeID != "" {
				if _, err := tx.Exec(`UPDATE packs SET status='previous' WHERE id=?`, activeID); err != nil {
					return fmt.Errorf("store: publish demote: %w", err)
				}
			}
			id := seed.NewID
			if id == "" {
				id = ids.NewID()
			}
			now := nowUTC()
			if _, err := tx.Exec(
				`INSERT INTO packs (id, created_at, status, seats_json, prompt_pack_hash, notes)
				 VALUES (?, ?, ?, ?, ?, ?)`,
				id, now, seed.NewStatus, seed.NewSeats, seed.NewHash, seed.NewNotes,
			); err != nil {
				return fmt.Errorf("store: publish insert: %w", err)
			}
			if seed.NewRunID != nil {
				if _, err := tx.Exec(`UPDATE packs SET published_from_run_id=? WHERE id=?`, *seed.NewRunID, id); err != nil {
					return fmt.Errorf("store: publish run id: %w", err)
				}
			}
			if _, err := tx.Exec(`UPDATE packs SET activated_at=? WHERE id=?`, now, id); err != nil {
				return fmt.Errorf("store: publish activated_at: %w", err)
			}
			res.ActiveID = id
			res.CreatedID = id
		}
		for _, b := range seed.Bundles {
			if err := upsertBundleTx(tx, b); err != nil {
				return err
			}
		}
		for _, bl := range seed.Baselines {
			if err := upsertBaselineTx(tx, bl); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return res, nil
}

func upsertBundleTx(tx *sql.Tx, b BundleSeed) error {
	if b.BundleKey == "" {
		return fmt.Errorf("store: upsert bundle: empty bundle_key")
	}
	var id, caseID, kind, views, manips, bhash string
	var source *string
	err := tx.QueryRow(`SELECT id, case_id, kind, views_json, manipulations_json,
		bundle_hash, source_pack_id FROM bundles WHERE bundle_key=?`, b.BundleKey,
	).Scan(&id, &caseID, &kind, &views, &manips, &bhash, &source)
	if err == nil {
		if b.CaseID != "" {
			caseID = b.CaseID
		}
		if b.ViewsJSON != "" {
			views = b.ViewsJSON
		}
		if b.ManipulationsJSON != "" {
			manips = b.ManipulationsJSON
		}
		if b.BundleHash != "" {
			bhash = b.BundleHash
		}
		if b.SourcePackID != nil {
			source = b.SourcePackID
		}
		if _, err := tx.Exec(`UPDATE bundles SET case_id=?, kind='natural',
			views_json=?, manipulations_json=?, bundle_hash=?, source_pack_id=?
			WHERE id=?`, caseID, views, manips, bhash, source, id); err != nil {
			return fmt.Errorf("store: update bundle: %w", err)
		}
		return nil
	}
	if b.CaseID == "" {
		return fmt.Errorf("store: upsert bundle: empty case_id")
	}
	viewsJSON := b.ViewsJSON
	if viewsJSON == "" {
		viewsJSON = "{}"
	}
	manipsJSON := b.ManipulationsJSON
	if manipsJSON == "" {
		manipsJSON = "[]"
	}
	bid := b.ID
	if bid == "" {
		bid = ids.NewID()
	}
	if _, err := tx.Exec(`INSERT INTO bundles
		(id, created_at, bundle_key, case_id, kind, views_json,
		 manipulations_json, bundle_hash, source_pack_id)
		VALUES (?, ?, ?, ?, 'natural', ?, ?, ?, ?)`,
		bid, nowUTC(), b.BundleKey, b.CaseID, viewsJSON,
		manipsJSON, b.BundleHash, b.SourcePackID); err != nil {
		return fmt.Errorf("store: insert bundle: %w", err)
	}
	return nil
}

func upsertBaselineTx(tx *sql.Tx, b BaselineSeed) error {
	if b.PackID == "" {
		return fmt.Errorf("store: upsert baseline: empty pack_id")
	}
	if b.CaseID == "" {
		return fmt.Errorf("store: upsert baseline: empty case_id")
	}
	if b.ResponseID == "" {
		return fmt.Errorf("store: upsert baseline: empty response_id")
	}
	var id string
	err := tx.QueryRow(`SELECT id FROM baselines WHERE pack_id=? AND seat=? AND case_id=?`,
		b.PackID, b.Seat, b.CaseID).Scan(&id)
	if err == nil {
		if _, err := tx.Exec(`UPDATE baselines SET bundle_id=?, response_id=? WHERE id=?`,
			b.BundleID, b.ResponseID, id); err != nil {
			return fmt.Errorf("store: update baseline: %w", err)
		}
		return nil
	}
	if _, err := tx.Exec(`INSERT INTO baselines
		(id, created_at, pack_id, seat, case_id, bundle_id, response_id)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		ids.NewID(), nowUTC(), b.PackID, b.Seat, b.CaseID, b.BundleID, b.ResponseID); err != nil {
		return fmt.Errorf("store: insert baseline: %w", err)
	}
	return nil
}
