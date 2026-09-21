package store

import (
	"fmt"

	"github.com/qoke/toughdecisions/internal/ids"
)

// Response is a row in the responses table.
type Response struct {
	ID               string
	CreatedAt        string
	CacheKey         string
	Seat             string
	ConfigHash       string
	PromptPackHash   string
	InputHash        string
	BundleHash       *string
	Repetition       int
	Origin           string
	RequestID        *string
	RunID            *string
	ModelRequested   string
	ModelReturned    string
	Substituted      bool
	RawText          string
	ParsedJSON       *string
	ParseOK          bool
	PromptTokens     int
	CompletionTokens int
	CostUSD          *float64
	LatencyMs        int64
	TimedOut         bool
	Error            *string
	WordCount        int
}

const responseCols = `id, created_at, cache_key, seat, config_hash, prompt_pack_hash,
	input_hash, bundle_hash, repetition, origin, request_id, run_id, model_requested,
	model_returned, substituted, raw_text, parsed_json, parse_ok, prompt_tokens,
	completion_tokens, cost_usd, latency_ms, timed_out, error, word_count`

// InsertResponse inserts a response row, assigning id/created_at when empty.
func (db *DB) InsertResponse(r *Response) (*Response, error) {
	if r.ID == "" {
		r.ID = ids.NewID()
	}
	if r.CreatedAt == "" {
		r.CreatedAt = nowUTC()
	}
	_, err := db.db.Exec(
		`INSERT INTO responses (`+responseCols+`) VALUES
		 (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		r.ID, r.CreatedAt, r.CacheKey, r.Seat, r.ConfigHash, r.PromptPackHash,
		r.InputHash, r.BundleHash, r.Repetition, r.Origin, r.RequestID, r.RunID,
		r.ModelRequested, r.ModelReturned, boolInt(r.Substituted), r.RawText,
		r.ParsedJSON, boolInt(r.ParseOK), r.PromptTokens, r.CompletionTokens,
		r.CostUSD, r.LatencyMs, boolInt(r.TimedOut), r.Error, r.WordCount,
	)
	if err != nil {
		return nil, fmt.Errorf("store: insert response: %w", err)
	}
	return r, nil
}

// GetResponse selects a response by id.
func (db *DB) GetResponse(id string) (*Response, error) {
	row := db.db.QueryRow(`SELECT `+responseCols+` FROM responses WHERE id = ?`, id)
	return scanResponse(row)
}

// GetResponseByCacheKey selects a response by cache_key.
func (db *DB) GetResponseByCacheKey(cacheKey string) (*Response, error) {
	row := db.db.QueryRow(`SELECT `+responseCols+` FROM responses WHERE cache_key = ?`, cacheKey)
	return scanResponse(row)
}

func scanResponse(row packRow) (*Response, error) {
	var r Response
	var substituted, parseOK, timedOut int
	if err := row.Scan(&r.ID, &r.CreatedAt, &r.CacheKey, &r.Seat, &r.ConfigHash,
		&r.PromptPackHash, &r.InputHash, &r.BundleHash, &r.Repetition, &r.Origin,
		&r.RequestID, &r.RunID, &r.ModelRequested, &r.ModelReturned, &substituted,
		&r.RawText, &r.ParsedJSON, &parseOK, &r.PromptTokens, &r.CompletionTokens,
		&r.CostUSD, &r.LatencyMs, &timedOut, &r.Error, &r.WordCount); err != nil {
		return nil, fmt.Errorf("store: scan response: %w", err)
	}
	r.Substituted = substituted != 0
	r.ParseOK = parseOK != 0
	r.TimedOut = timedOut != 0
	return &r, nil
}
