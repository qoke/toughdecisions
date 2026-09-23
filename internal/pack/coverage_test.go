package pack

import (
	"strings"
	"testing"
)

// TestLoadSeatsParsesWithoutPersisting covers LoadSeats: the seats file must
// be parsed and defaulted without writing any pack row.
func TestLoadSeatsParsesWithoutPersisting(t *testing.T) {
	// Arrange
	db := openTestDB(t)

	// Act
	seats, err := LoadSeats(writeSeats(t, testSeatsYAML))

	// Assert
	if err != nil {
		t.Fatalf("LoadSeats: %v", err)
	}
	if len(seats) != 4 {
		t.Fatalf("seats = %d, want 4", len(seats))
	}
	poss := seats[SeatPossibility]
	if poss.Model != "m1" || poss.Family != "openai" {
		t.Fatalf("possibility = %+v, want model m1 family openai", poss)
	}
	if poss.Temperature == nil || *poss.Temperature != 0.7 {
		t.Fatalf("possibility temperature = %v, want 0.7", poss.Temperature)
	}
	if got := seats[SeatJudge].MaxOutputTokens; got != DefaultJudgeMaxOutputTokens {
		t.Fatalf("judge max_output_tokens = %d, want %d", got, DefaultJudgeMaxOutputTokens)
	}
	if got := seats[SeatPerspective].MaxOutputTokens; got != DefaultViewsMaxOutputTokens {
		t.Fatalf("perspective max_output_tokens = %d, want %d (defaulted)", got, DefaultViewsMaxOutputTokens)
	}
	if _, err := Active(db); err == nil {
		t.Fatal("Active() error = nil after LoadSeats, want no active pack persisted")
	}
}

// TestInsertActivePersistsValidatedSeats covers InsertActive's happy path:
// a complete seats map becomes the active pack.
func TestInsertActivePersistsValidatedSeats(t *testing.T) {
	// Arrange
	db := openTestDB(t)
	seats, err := LoadSeats(writeSeats(t, testSeatsYAML))
	if err != nil {
		t.Fatalf("LoadSeats: %v", err)
	}

	// Act
	p, err := InsertActive(db, seats)

	// Assert
	if err != nil {
		t.Fatalf("InsertActive: %v", err)
	}
	if p == nil {
		t.Fatal("InsertActive pack = nil, want a pack")
	}
	if p.Status != StatusActive {
		t.Fatalf("status = %q, want %q", p.Status, StatusActive)
	}
	if len(p.Seats) != 4 {
		t.Fatalf("seats = %d, want 4", len(p.Seats))
	}
	if p.PromptPackHash == "" {
		t.Fatal("PromptPackHash is empty")
	}
	active, err := Active(db)
	if err != nil {
		t.Fatalf("Active: %v", err)
	}
	if active.ID != p.ID {
		t.Fatalf("Active id = %s, want %s", active.ID, p.ID)
	}
}

// TestInsertActiveRejectsMissingSeat covers InsertActive's validation branch:
// a seats map missing a council seat is rejected before anything is written.
func TestInsertActiveRejectsMissingSeat(t *testing.T) {
	// Arrange
	db := openTestDB(t)
	seats, err := LoadSeats(writeSeats(t, testSeatsYAML))
	if err != nil {
		t.Fatalf("LoadSeats: %v", err)
	}
	delete(seats, SeatJudge)

	// Act
	p, err := InsertActive(db, seats)

	// Assert
	if err == nil {
		t.Fatal("InsertActive error = nil, want missing-seat error")
	}
	if p != nil {
		t.Fatalf("InsertActive pack = %+v, want nil on error", p)
	}
	if !strings.Contains(err.Error(), `missing seat "judge"`) {
		t.Fatalf("err = %v, want it to name the missing seat", err)
	}
	if _, aerr := Active(db); aerr == nil {
		t.Fatal("Active() error = nil after rejected InsertActive, want no pack persisted")
	}
}
