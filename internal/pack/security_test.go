package pack

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestResolveRolePromptFileRejectsAbsolute is SEC-H2: absolute refs rejected.
func TestResolveRolePromptFileRejectsAbsolute(t *testing.T) {
	if _, err := ResolveRolePromptFile(t.TempDir(), "/etc/passwd"); err == nil {
		t.Fatal("absolute role_prompt_file: want error, got nil")
	}
}

// TestResolveRolePromptFileRejectsEscape is SEC-H2: ../ escape rejected.
func TestResolveRolePromptFileRejectsEscape(t *testing.T) {
	dir := t.TempDir()
	for _, ref := range []string{"../escape.md", "sub/../../escape.md"} {
		if _, err := ResolveRolePromptFile(dir, ref); err == nil {
			t.Fatalf("ref %q: want escape error, got nil", ref)
		}
	}
}

// TestResolveRolePromptFileSiblingLoads is SEC-H2: sibling file still loads.
func TestResolveRolePromptFileSiblingLoads(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "custom_role.md"), []byte("SIBLING TEXT"), 0o644); err != nil {
		t.Fatalf("write role file: %v", err)
	}
	yaml := `seats:
  possibility: {model: m1, family: openai, role_prompt_file: "custom_role.md"}
  perspective: {model: m1, family: openai}
  stress_tester: {model: m1, family: openai}
  judge: {model: m1, family: openai}
`
	seatsPath := filepath.Join(dir, "pack.yaml")
	if err := os.WriteFile(seatsPath, []byte(yaml), 0o644); err != nil {
		t.Fatalf("write seats: %v", err)
	}
	db := openTestDB(t)
	p, err := Init(db, seatsPath)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	if got := p.Seats[SeatPossibility].RolePromptOverride; got != "SIBLING TEXT" {
		t.Fatalf("override = %q, want %q", got, "SIBLING TEXT")
	}
}

// TestLoadSeatsRolePromptFileAbsoluteRejected is SEC-H2 via loadSeats.
func TestLoadSeatsRolePromptFileAbsoluteRejected(t *testing.T) {
	yaml := `seats:
  possibility: {model: m1, family: openai, role_prompt_file: "/etc/passwd"}
  perspective: {model: m1, family: openai}
  stress_tester: {model: m1, family: openai}
  judge: {model: m1, family: openai}
`
	if _, err := Init(openTestDB(t), writeSeats(t, yaml)); err == nil {
		t.Fatal("absolute role_prompt_file: want error, got nil")
	}
}

// TestLoadSeatsRolePromptFileEscapeRejected is SEC-H2 via loadSeats.
func TestLoadSeatsRolePromptFileEscapeRejected(t *testing.T) {
	yaml := `seats:
  possibility: {model: m1, family: openai, role_prompt_file: "../escape.md"}
  perspective: {model: m1, family: openai}
  stress_tester: {model: m1, family: openai}
  judge: {model: m1, family: openai}
`
	if _, err := Init(openTestDB(t), writeSeats(t, yaml)); err == nil {
		t.Fatal("../ escape role_prompt_file: want error, got nil")
	}
}

// TestLoadSeatsRejectsUnknownField is SEC-L1: strict decoding rejects typos.
func TestLoadSeatsRejectsUnknownField(t *testing.T) {
	yaml := `seats:
  possibility: {model: m1, family: openai, modle: m1}
  perspective: {model: m1, family: openai}
  stress_tester: {model: m1, family: openai}
  judge: {model: m1, family: openai}
`
	_, err := Init(openTestDB(t), writeSeats(t, yaml))
	if err == nil {
		t.Fatal("unknown field: want error, got nil")
	}
	if !strings.Contains(err.Error(), "modle") {
		t.Fatalf("error %q does not mention the unknown field", err)
	}
}
