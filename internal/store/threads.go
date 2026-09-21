package store

import (
	"fmt"

	"github.com/qoke/toughdecisions/internal/hash"
	"github.com/qoke/toughdecisions/internal/ids"
)

// Thread is a row in the threads table.
type Thread struct {
	ID        string
	CreatedAt string
	Name      string
	Archived  bool
}

// Card is a row in the cards table.
type Card struct {
	ID        string
	CreatedAt string
	ThreadID  string
	Version   int
	CardJSON  string
	CardHash  string
}

// CreateThread inserts a thread row.
func (db *DB) CreateThread(name string) (*Thread, error) {
	t := &Thread{ID: ids.NewID(), CreatedAt: nowUTC(), Name: name}
	_, err := db.db.Exec(
		`INSERT INTO threads (id, created_at, name, archived) VALUES (?, ?, ?, 0)`,
		t.ID, t.CreatedAt, t.Name,
	)
	if err != nil {
		return nil, fmt.Errorf("store: create thread: %w", err)
	}
	return t, nil
}

// ListThreads returns all threads ordered by creation time.
func (db *DB) ListThreads() ([]*Thread, error) {
	rows, err := db.db.Query(`SELECT id, created_at, name, archived FROM threads ORDER BY created_at`)
	if err != nil {
		return nil, fmt.Errorf("store: list threads: %w", err)
	}
	defer rows.Close()
	var out []*Thread
	for rows.Next() {
		var t Thread
		var archived int
		if err := rows.Scan(&t.ID, &t.CreatedAt, &t.Name, &archived); err != nil {
			return nil, fmt.Errorf("store: scan thread: %w", err)
		}
		t.Archived = archived != 0
		out = append(out, &t)
	}
	return out, rows.Err()
}

// InsertCard inserts a cards row, assigning the next version for the thread.
func (db *DB) InsertCard(threadID, cardJSON string) (*Card, error) {
	var maxV *int
	err := db.db.QueryRow(`SELECT MAX(version) FROM cards WHERE thread_id = ?`, threadID).Scan(&maxV)
	if err != nil {
		return nil, fmt.Errorf("store: max card version: %w", err)
	}
	version := 1
	if maxV != nil {
		version = *maxV + 1
	}
	c := &Card{
		ID:        ids.NewID(),
		CreatedAt: nowUTC(),
		ThreadID:  threadID,
		Version:   version,
		CardJSON:  cardJSON,
		CardHash:  hash.SHA256Hex([]byte(cardJSON)),
	}
	_, err = db.db.Exec(
		`INSERT INTO cards (id, created_at, thread_id, version, card_json, card_hash)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		c.ID, c.CreatedAt, c.ThreadID, c.Version, c.CardJSON, c.CardHash,
	)
	if err != nil {
		return nil, fmt.Errorf("store: insert card: %w", err)
	}
	return c, nil
}

// LatestCard returns the highest-version card for a thread.
func (db *DB) LatestCard(threadID string) (*Card, error) {
	row := db.db.QueryRow(`SELECT id, created_at, thread_id, version, card_json, card_hash
		FROM cards WHERE thread_id = ? ORDER BY version DESC LIMIT 1`, threadID)
	var c Card
	if err := row.Scan(&c.ID, &c.CreatedAt, &c.ThreadID, &c.Version, &c.CardJSON, &c.CardHash); err != nil {
		return nil, fmt.Errorf("store: latest card: %w", err)
	}
	return &c, nil
}
