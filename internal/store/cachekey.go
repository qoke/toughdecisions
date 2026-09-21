package store

import (
	"fmt"

	"github.com/qoke/toughdecisions/internal/hash"
)

// ResponseCacheKey implements DEVELOPMENT_PLAN.md §12: the harness response
// cache key is sha256(seat, config_hash, prompt_pack_hash, input_hash,
// bundle_hash, repetition). The grade cache in §11 reuses this helper with
// (response_id, grader config_hash, rubric_hash); it is sensitive to all of
// those inputs by construction.
//
// Concurrent same-key inserts must resolve deterministically: callers that
// race on the same key must reuse the existing row (INSERT ... ON CONFLICT
// DO NOTHING + select, or Get-then-Insert) per R-14, never create duplicates.
func ResponseCacheKey(seat, configHash, promptPackHash, inputHash string, bundleHash *string, repetition int) string {
	bundle := ""
	if bundleHash != nil {
		bundle = *bundleHash
	}
	return hash.SHA256Hex(hash.CanonicalJSON(map[string]any{
		"seat":             seat,
		"config_hash":      configHash,
		"prompt_pack_hash": promptPackHash,
		"input_hash":       inputHash,
		"bundle_hash":      bundle,
		"repetition":       repetition,
	}))
}

// MaxRepetition returns the highest repetition stored for the response cache
// key prefix (seat, config, pack, input, bundle), or -1 when none exists.
// Callers implementing `--fresh` semantics insert with MaxRepetition()+1.
func (db *DB) MaxRepetition(seat, configHash, promptPackHash, inputHash string, bundleHash *string) (int, error) {
	var max *int
	var err error
	if bundleHash == nil {
		err = db.db.QueryRow(
			`SELECT MAX(repetition) FROM responses WHERE seat=? AND config_hash=?
			 AND prompt_pack_hash=? AND input_hash=? AND bundle_hash IS NULL`,
			seat, configHash, promptPackHash, inputHash,
		).Scan(&max)
	} else {
		err = db.db.QueryRow(
			`SELECT MAX(repetition) FROM responses WHERE seat=? AND config_hash=?
			 AND prompt_pack_hash=? AND input_hash=? AND bundle_hash=?`,
			seat, configHash, promptPackHash, inputHash, *bundleHash,
		).Scan(&max)
	}
	if err != nil {
		return 0, fmt.Errorf("store: max repetition: %w", err)
	}
	if max == nil {
		return -1, nil
	}
	return *max, nil
}
