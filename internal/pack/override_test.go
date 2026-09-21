package pack

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestLoadSeatsRolePromptOverrideInline is RED: inline override text parsed.
func TestLoadSeatsRolePromptOverrideInline(t *testing.T) {
	// Arrange
	yaml := `seats:
  possibility: {model: m1, family: openai, role_prompt_override: "CUSTOM POSSIBILITY"}
  perspective: {model: m1, family: openai}
  stress_tester: {model: m1, family: openai}
  judge: {model: m1, family: openai}
`

	// Act
	db := openTestDB(t)
	p, err := Init(db, writeSeats(t, yaml))

	// Assert
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	if got := p.Seats[SeatPossibility].RolePromptOverride; got != "CUSTOM POSSIBILITY" {
		t.Fatalf("override = %q, want %q", got, "CUSTOM POSSIBILITY")
	}
	if got := p.Seats[SeatPerspective].RolePromptOverride; got != "" {
		t.Fatalf("perspective override = %q, want empty", got)
	}
	// Persisted through the DB round trip.
	active, err := Active(db)
	if err != nil {
		t.Fatalf("Active: %v", err)
	}
	if got := active.Seats[SeatPossibility].RolePromptOverride; got != "CUSTOM POSSIBILITY" {
		t.Fatalf("round-trip override = %q, want %q", got, "CUSTOM POSSIBILITY")
	}
}

// TestLoadSeatsRolePromptFile is RED: override loaded from a sibling file.
func TestLoadSeatsRolePromptFile(t *testing.T) {
	// Arrange
	dir := t.TempDir()
	roleFile := filepath.Join(dir, "custom_role.md")
	if err := os.WriteFile(roleFile, []byte("FILE ROLE TEXT"), 0o644); err != nil {
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

	// Act
	db := openTestDB(t)
	p, err := Init(db, seatsPath)

	// Assert
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	if got := p.Seats[SeatPossibility].RolePromptOverride; got != "FILE ROLE TEXT" {
		t.Fatalf("override = %q, want %q", got, "FILE ROLE TEXT")
	}
}

// TestLoadSeatsRolePromptFileMissing is RED: a missing file is a hard error.
func TestLoadSeatsRolePromptFileMissing(t *testing.T) {
	// Arrange
	yaml := `seats:
  possibility: {model: m1, family: openai, role_prompt_file: "does-not-exist.md"}
  perspective: {model: m1, family: openai}
  stress_tester: {model: m1, family: openai}
  judge: {model: m1, family: openai}
`

	// Act
	db := openTestDB(t)
	_, err := Init(db, writeSeats(t, yaml))

	// Assert
	if err == nil {
		t.Fatal("want hard error for missing role_prompt_file, got nil")
	}
	if !strings.Contains(err.Error(), "role_prompt_file") {
		t.Fatalf("error %q does not mention role_prompt_file", err)
	}
}

// TestLoadSeatsRolePromptBothSet is RED: inline + file together is an error.
func TestLoadSeatsRolePromptBothSet(t *testing.T) {
	// Arrange
	dir := t.TempDir()
	roleFile := filepath.Join(dir, "custom_role.md")
	if err := os.WriteFile(roleFile, []byte("FILE ROLE TEXT"), 0o644); err != nil {
		t.Fatalf("write role file: %v", err)
	}
	yaml := `seats:
  possibility: {model: m1, family: openai, role_prompt_override: "INLINE", role_prompt_file: "custom_role.md"}
  perspective: {model: m1, family: openai}
  stress_tester: {model: m1, family: openai}
  judge: {model: m1, family: openai}
`
	seatsPath := filepath.Join(dir, "pack.yaml")
	if err := os.WriteFile(seatsPath, []byte(yaml), 0o644); err != nil {
		t.Fatalf("write seats: %v", err)
	}

	// Act
	db := openTestDB(t)
	_, err := Init(db, seatsPath)

	// Assert
	if err == nil {
		t.Fatal("want error when both override and file are set, got nil")
	}
}

// TestSeatConfigHashCoversOverride is RED: Hash changes with the override.
func TestSeatConfigHashCoversOverride(t *testing.T) {
	// Arrange
	a := baseSeatConfig()

	// Act
	b := baseSeatConfig()
	b.RolePromptOverride = "custom role text"

	// Assert
	if a.Hash() == b.Hash() {
		t.Fatal("Hash() did not change when RolePromptOverride changed")
	}
	if got := b.Hash(); len(got) != 64 {
		t.Fatalf("hash %q is not 64-char sha256 hex", got)
	}
}
