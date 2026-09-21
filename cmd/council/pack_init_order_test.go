package main

import (
	"os"
	"testing"

	"github.com/qoke/toughdecisions/internal/config"
	"github.com/qoke/toughdecisions/internal/store"
)

// TestPackInitInvalidModelWritesNoRow is the R-05a CLI regression test: a
// failed `pack init` (unknown model) must exit non-zero while leaving the
// store unchanged — no pack row, no active pack.
func TestPackInitInvalidModelWritesNoRow(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("COUNCIL_DB_PATH", dir+"/c.db")
	t.Setenv("COUNCIL_MODELS_FILE", writeModelsFile(t))
	t.Setenv("COUNCIL_PACK_FILE", writePackFile(t))
	if got := run([]string{"db", "migrate"}); got != exitOK {
		t.Fatalf("migrate = %d", got)
	}
	other := t.TempDir() + "/other-pack.yaml"
	content := "seats:\n" +
		"  possibility: {model: unknown-model, family: openai}\n" +
		"  perspective: {model: m1, family: openai}\n" +
		"  stress_tester: {model: m1, family: openai}\n" +
		"  judge: {model: m1, family: openai}\n"
	if err := os.WriteFile(other, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := packInit([]string{"--file", other}); got != exitValidation {
		t.Fatalf("init unknown model = %d, want %d", got, exitValidation)
	}
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(cfg.DBPath())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	rows, err := db.PacksByStatus("active")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 0 {
		t.Fatalf("active packs = %d; want 0 (failed pack init must write no row)", len(rows))
	}
}

// TestPackInitValidStillSucceeds guards the other half: a valid init still
// exits 0 and leaves exactly one active pack.
func TestPackInitValidStillSucceeds(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("COUNCIL_DB_PATH", dir+"/c.db")
	t.Setenv("COUNCIL_MODELS_FILE", writeModelsFile(t))
	t.Setenv("COUNCIL_PACK_FILE", writePackFile(t))
	if got := run([]string{"db", "migrate"}); got != exitOK {
		t.Fatalf("migrate = %d", got)
	}
	if got := run([]string{"pack", "init"}); got != exitOK {
		t.Fatalf("pack init = %d, want 0", got)
	}
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(cfg.DBPath())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	rows, err := db.PacksByStatus("active")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("active packs = %d; want 1", len(rows))
	}
}
