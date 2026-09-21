package pack

import (
	"database/sql"
	"errors"
	"testing"

	"github.com/qoke/toughdecisions/internal/gateway"
)

// TestInitValidatedFailedInitWritesNoRow is the R-05a regression test: a
// seats file whose model settings fail validation must leave the store
// unchanged — no pack row written, no active pack left behind.
func TestInitValidatedFailedInitWritesNoRow(t *testing.T) {
	db := openTestDB(t)
	bad := `seats:
  possibility: {model: unknown-model, family: openai}
  perspective: {model: m1, family: openai}
  stress_tester: {model: m1, family: openai}
  judge: {model: m1, family: openai}
`
	validate := func(sc SeatConfig) error {
		if sc.Model == "unknown-model" {
			return gateway.ErrUnsupportedSetting
		}
		return nil
	}
	if _, err := InitValidated(db, writeSeats(t, bad), validate); err == nil {
		t.Fatal("InitValidated bad model: want error, got nil")
	}
	rows, err := db.PacksByStatus(StatusActive)
	if err != nil {
		t.Fatalf("PacksByStatus: %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("active packs = %d; want 0 (failed init must write no row)", len(rows))
	}
	if _, err := db.Active(); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("Active err = %v; want no active pack (sql.ErrNoRows)", err)
	}
}

// TestInitValidatedSuccessStillPersists guards the other half: a valid
// seats file still persists exactly one active pack.
func TestInitValidatedSuccessStillPersists(t *testing.T) {
	db := openTestDB(t)
	p, err := InitValidated(db, writeSeats(t, testSeatsYAML), func(SeatConfig) error { return nil })
	if err != nil {
		t.Fatalf("InitValidated valid: %v", err)
	}
	if p.Status != StatusActive {
		t.Fatalf("status = %q; want active", p.Status)
	}
	active, err := Active(db)
	if err != nil {
		t.Fatalf("Active: %v", err)
	}
	if active.ID != p.ID {
		t.Fatalf("Active id = %s; want %s", active.ID, p.ID)
	}
}
