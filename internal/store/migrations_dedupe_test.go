package store

import (
	"path/filepath"
	"testing"
)

// TestFlagDedupeMigrationHealsDuplicates builds a database at migration 2
// state (duplicates present, no unique index, 0003 unrecorded), then drives
// the real migration runner by re-opening the file and asserts migration 3
// succeeds, keeps exactly one survivor per identity, and re-applies cleanly.
func TestFlagDedupeMigrationHealsDuplicates(t *testing.T) {
	path := filepath.Join(t.TempDir(), "dedupe.db")

	// Arrange: a fully-migrated DB, then rolled back to migration-2 state
	// with duplicate flag rows seeded via raw SQL.
	db, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	seed := []struct{ id, createdAt, gradeID, typ, passage, violated, status string }{
		{"a1", "2026-01-01T00:00:00Z", "grade-dedupe", "fabrication", "p1", "v1", "open"},
		{"a2", "2026-01-02T00:00:00Z", "grade-dedupe", "fabrication", "p1", "v1", "open"},
		{"a3", "2026-01-01T00:00:00Z", "grade-dedupe", "fabrication", "p1", "v1", "dismissed"},
		{"b1", "2026-01-01T00:00:00Z", "grade-dedupe", "other", "p2", "v2", "open"},
		{"b2", "2026-01-02T00:00:00Z", "grade-dedupe", "other", "p2", "v2", "open"},
	}
	if _, err := db.db.Exec(`DROP INDEX IF EXISTS idx_flags_grade_identity`); err != nil {
		t.Fatalf("drop index: %v", err)
	}
	for _, s := range seed {
		if _, err := db.db.Exec(
			`INSERT INTO flags (id, created_at, grade_id, response_id, type, passage, violated, status) VALUES (?, ?, ?, 'resp-seed', ?, ?, ?, ?)`,
			s.id, s.createdAt, s.gradeID, s.typ, s.passage, s.violated, s.status,
		); err != nil {
			t.Fatalf("seed flag %s: %v", s.id, err)
		}
	}
	if _, err := db.db.Exec(`DELETE FROM schema_migrations WHERE version = '0003_flag_dedupe'`); err != nil {
		t.Fatalf("unrecord 0003: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Act: re-opening drives the real migrate() runner over pending 0003.
	db2, err := Open(path)
	if err != nil {
		t.Fatalf("(a) migration 3 on duplicate rows: %v", err)
	}
	defer db2.Close()

	// Assert (b): exactly one row per identity survives.
	var total int
	if err := db2.db.QueryRow(`SELECT COUNT(*) FROM flags`).Scan(&total); err != nil {
		t.Fatalf("count flags: %v", err)
	}
	if total != 2 {
		t.Fatalf("(b) flags = %d; want exactly 2 (one per identity)", total)
	}
	rows, err := db2.db.Query(`SELECT id, status FROM flags ORDER BY id`)
	if err != nil {
		t.Fatalf("list survivors: %v", err)
	}
	defer rows.Close()
	got := map[string]string{}
	for rows.Next() {
		var id, status string
		if err := rows.Scan(&id, &status); err != nil {
			t.Fatalf("scan survivor: %v", err)
		}
		got[id] = status
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}
	// Survivor rule: triaged (non-open) row wins over newer open rows, and
	// the newer of two open rows wins.
	want := map[string]string{"a3": "dismissed", "b2": "open"}
	if len(got) != len(want) {
		t.Fatalf("(b) survivors = %v; want %v", got, want)
	}
	for id, status := range want {
		if got[id] != status {
			t.Fatalf("(b) survivors = %v; want %v", got, want)
		}
	}
	var idx int
	if err := db2.db.QueryRow(
		`SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND name='idx_flags_grade_identity'`,
	).Scan(&idx); err != nil {
		t.Fatalf("index check: %v", err)
	}
	if idx != 1 {
		t.Fatal("(b) idx_flags_grade_identity missing after migration 3")
	}
	applied, err := db2.AppliedMigrations()
	if err != nil {
		t.Fatalf("AppliedMigrations: %v", err)
	}
	if len(applied) != 3 || applied[2] != "0003_flag_dedupe" {
		t.Fatalf("(b) applied = %v, want [0001_init 0002_harness 0003_flag_dedupe]", applied)
	}
	if err := db2.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Assert (c): re-opening is a no-op.
	db3, err := Open(path)
	if err != nil {
		t.Fatalf("(c) re-open: %v", err)
	}
	defer db3.Close()
	applied3, err := db3.AppliedMigrations()
	if err != nil {
		t.Fatalf("AppliedMigrations: %v", err)
	}
	if len(applied3) != 3 {
		t.Fatalf("(c) applied = %v, want exactly 3", applied3)
	}
	var total3 int
	if err := db3.db.QueryRow(`SELECT COUNT(*) FROM flags`).Scan(&total3); err != nil {
		t.Fatalf("count flags: %v", err)
	}
	if total3 != 2 {
		t.Fatalf("(c) flags = %d after re-open; want 2", total3)
	}
}
