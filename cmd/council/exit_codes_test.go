package main

import (
	"encoding/json"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/qoke/toughdecisions/internal/store"
)

// TestExitCodesAreDisjoint proves R-11 on three real inputs: an unknown
// subcommand exits 2, a spending command with an empty key exits 1, and
// pack publish refusing on a failing checklist exits 3 (pack.go:126 via
// harness.ChecklistBlockedError from publish.go:110). Exit 3 needs only a
// migrated store, an active pack, and one candidate row whose
// promotion_json fails — no gateway calls, no graded checklist.
func TestExitCodesAreDisjoint(t *testing.T) {
	// exit 2: unknown subcommand.
	oldErr := os.Stderr
	r, w, _ := os.Pipe()
	os.Stderr = w
	code2 := run([]string{"bogus-subcommand-xyz"})
	_ = w.Close()
	os.Stderr = oldErr
	err2, _ := io.ReadAll(r)
	if code2 != exitValidation {
		t.Fatalf("unknown subcommand = %d, want %d", code2, exitValidation)
	}

	// exit 1: spending command with an empty key (no network, no work).
	dir := t.TempDir()
	t.Setenv("COUNCIL_DB_PATH", dir+"/c.db")
	t.Setenv("COUNCIL_MODELS_FILE", writeModelsFile(t))
	t.Setenv("COUNCIL_PACK_FILE", writePackFile(t))
	t.Setenv("COUNCIL_GATEWAY_API_KEY", "")
	oldErr = os.Stderr
	r, w, _ = os.Pipe()
	os.Stderr = w
	code1 := run([]string{"harness", "sentinel"})
	_ = w.Close()
	os.Stderr = oldErr
	err1, _ := io.ReadAll(r)
	if code1 != exitError {
		t.Fatalf("harness sentinel without key = %d, want %d", code1, exitError)
	}

	// exit 3: pack publish refusing a failing checklist without --force.
	t.Setenv("COUNCIL_GATEWAY_API_KEY", "sk-test-blocked-key-001")
	if got := run([]string{"db", "migrate"}); got != exitOK {
		t.Fatalf("db migrate = %d, want %d", got, exitOK)
	}
	if got := run([]string{"pack", "init"}); got != exitOK {
		t.Fatalf("pack init = %d, want %d", got, exitOK)
	}
	db, cfg, err := openStore()
	if err != nil {
		t.Fatalf("openStore: %v", err)
	}
	runRow, err := db.CreateHarnessRun("weekly", "pack", "{}")
	if err != nil {
		_ = db.Close()
		t.Fatalf("CreateHarnessRun: %v", err)
	}
	cand, err := db.UpsertCandidate(runRow.ID, "cand-a", "possibility",
		`{"seat":"possibility","model":"m1","family":"openai","max_output_tokens":2000}`, "cfg")
	if err != nil {
		_ = db.Close()
		t.Fatalf("UpsertCandidate: %v", err)
	}
	_ = cfg
	failing := map[string]any{"candidate_key": "cand-a", "promote_recommended": false, "rows": []any{}}
	raw, _ := json.Marshal(failing)
	s := string(raw)
	if err := db.UpdateCandidateResults(cand.ID, &store.CandidateResults{PromotionJSON: &s}); err != nil {
		_ = db.Close()
		t.Fatalf("UpdateCandidateResults: %v", err)
	}
	_ = db.Close()
	oldErr = os.Stderr
	r, w, _ = os.Pipe()
	os.Stderr = w
	code3 := run([]string{"pack", "publish", "--run", runRow.ID, "--candidate", "cand-a"})
	_ = w.Close()
	os.Stderr = oldErr
	err3, _ := io.ReadAll(r)
	if code3 != exitBlocked {
		t.Fatalf("pack publish failing checklist = %d, want %d (%s)", code3, exitBlocked, string(err3))
	}
	if !strings.Contains(string(err3), "cand-a") || !strings.Contains(string(err3), "--force") {
		t.Fatalf("stderr = %q, want it to name cand-a and --force", string(err3))
	}

	// Disjointness: three real inputs, three distinct codes.
	if code1 == code2 || code1 == code3 || code2 == code3 {
		t.Fatalf("codes not disjoint: exit1=%d exit2=%d exit3=%d", code1, code2, code3)
	}
	_ = err1
	_ = err2
}
