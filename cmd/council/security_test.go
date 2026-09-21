package main

import (
	"os"
	"path/filepath"
	"testing"
)

// TestGradersFileRejectsUnknownField is SEC-L1: strict decoding rejects
// unknown/misspelled fields in graders.yaml.
func TestGradersFileRejectsUnknownField(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("COUNCIL_DB_PATH", filepath.Join(dir, "c.db"))
	t.Setenv("COUNCIL_MODELS_FILE", writeModelsFile(t))
	t.Setenv("COUNCIL_PACK_FILE", writePackFile(t))
	content := "graders:\n" +
		"  - {key: grader-a, model: m1, family: openai, role: selection, max_output_tokens: 2000, modle: m1}\n"
	if err := os.WriteFile(filepath.Join(dir, "graders.yaml"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("COUNCIL_GRADERS_FILE", filepath.Join(dir, "graders.yaml"))
	if got := run([]string{"db", "migrate"}); got != exitOK {
		t.Fatalf("migrate = %d, want 0", got)
	}
	if got := run([]string{"graders", "status"}); got != exitError {
		t.Fatalf("status with unknown field = %d, want %d", got, exitError)
	}
}

// TestScreenCandidatesRejectsUnknownField is SEC-L1: strict decoding rejects
// unknown/misspelled fields in candidates.yaml (harness screen site).
func TestScreenCandidatesRejectsUnknownField(t *testing.T) {
	dir := testEnv(t)
	writeCasepackDir(t, dir)
	writeGradersFile(t, dir)
	content := "candidates:\n" +
		"  - {key: c1, seat: possibility, model: m1, family: openai, finalist: never, modle: m1}\n"
	if err := os.WriteFile(filepath.Join(dir, "candidates.yaml"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := run([]string{"db", "migrate"}); got != exitOK {
		t.Fatalf("migrate = %d, want 0", got)
	}
	if got := run([]string{"pack", "init"}); got != exitOK {
		t.Fatalf("pack init = %d, want 0", got)
	}
	if got := run([]string{"cases", "load"}); got != exitOK {
		t.Fatalf("cases load = %d, want 0", got)
	}
	// Unknown field fails at candidate parsing (exit 1), before any model call.
	if got := run([]string{"harness", "screen"}); got != exitError {
		t.Fatalf("screen with unknown field = %d, want %d", got, exitError)
	}
}
