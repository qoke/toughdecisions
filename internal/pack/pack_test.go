package pack

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/qoke/toughdecisions/internal/store"
)

const testSeatsYAML = `seats:
  possibility: {model: m1, family: openai, temperature: 0.7, max_output_tokens: 2000}
  perspective: {model: m1, family: openai}
  stress_tester: {model: m1, family: openai}
  judge: {model: m1, family: openai, max_output_tokens: 3000}
`

func openTestDB(t *testing.T) *store.DB {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func writeSeats(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "pack.yaml")
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatalf("write seats: %v", err)
	}
	return p
}

// TestInitLoadsSeats is the RED test for Init.
func TestInitLoadsSeats(t *testing.T) {
	db := openTestDB(t)
	p, err := Init(db, writeSeats(t, testSeatsYAML))
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	if len(p.Seats) != 4 {
		t.Fatalf("seats = %d, want 4", len(p.Seats))
	}
	if p.Status != "active" {
		t.Fatalf("status = %q, want active", p.Status)
	}
	if p.PromptPackHash == "" {
		t.Fatal("PromptPackHash is empty")
	}
	// Defaults: perspective/stress_tester get 2000 when max_output_tokens is 0.
	if p.Seats[SeatPerspective].MaxOutputTokens != DefaultViewsMaxOutputTokens {
		t.Errorf("perspective max_output_tokens = %d, want %d",
			p.Seats[SeatPerspective].MaxOutputTokens, DefaultViewsMaxOutputTokens)
	}
	active, err := Active(db)
	if err != nil {
		t.Fatalf("Active: %v", err)
	}
	if active.ID != p.ID {
		t.Fatalf("Active id = %s, want %s", active.ID, p.ID)
	}
}

func TestInitRejectsMissingSeat(t *testing.T) {
	db := openTestDB(t)
	yaml := `seats:
  possibility: {model: m1, family: openai}
  perspective: {model: m1, family: openai}
  judge: {model: m1, family: openai}
`
	if _, err := Init(db, writeSeats(t, yaml)); err == nil {
		t.Fatal("want error for missing seat, got nil")
	}
}

func TestInitRejectsUnknownSeat(t *testing.T) {
	db := openTestDB(t)
	yaml := `seats:
  possibility: {model: m1, family: openai}
  perspective: {model: m1, family: openai}
  stress_tester: {model: m1, family: openai}
  judge: {model: m1, family: openai}
  heckler: {model: m1, family: openai}
`
	if _, err := Init(db, writeSeats(t, yaml)); err == nil {
		t.Fatal("want error for unknown seat, got nil")
	}
}

func seatsWithModel(model string) map[Seat]SeatConfig {
	out := make(map[Seat]SeatConfig, 4)
	for _, s := range AllSeats {
		out[s] = SeatConfig{Seat: s, Model: model, Family: "openai", MaxOutputTokens: DefaultMaxOutputTokens(s)}
	}
	return out
}

func TestPublishDemotesAndRetires(t *testing.T) {
	db := openTestDB(t)
	p1, err := Init(db, writeSeats(t, testSeatsYAML))
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	p2, err := Publish(db, seatsWithModel("m2"), "")
	if err != nil {
		t.Fatalf("Publish: %v", err)
	}
	if p2.Status != "active" {
		t.Fatalf("p2 status = %q, want active", p2.Status)
	}
	got1, err := Show(db, p1.ID)
	if err != nil {
		t.Fatalf("Show p1: %v", err)
	}
	if got1.Status != "previous" {
		t.Fatalf("p1 status = %q, want previous", got1.Status)
	}
	p3, err := Publish(db, seatsWithModel("m3"), "")
	if err != nil {
		t.Fatalf("Publish p3: %v", err)
	}
	_ = p3
	got1, _ = Show(db, p1.ID)
	if got1.Status != "retired" {
		t.Fatalf("p1 status after 3rd publish = %q, want retired", got1.Status)
	}
	got2, _ := Show(db, p2.ID)
	if got2.Status != "previous" {
		t.Fatalf("p2 status = %q, want previous", got2.Status)
	}
}

func TestRollbackSwapsActivePrevious(t *testing.T) {
	db := openTestDB(t)
	p1, err := Init(db, writeSeats(t, testSeatsYAML))
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	p2, err := Publish(db, seatsWithModel("m2"), "")
	if err != nil {
		t.Fatalf("Publish: %v", err)
	}
	back, err := Rollback(db)
	if err != nil {
		t.Fatalf("Rollback: %v", err)
	}
	if back.ID != p1.ID {
		t.Fatalf("Rollback id = %s, want %s", back.ID, p1.ID)
	}
	got2, _ := Show(db, p2.ID)
	if got2.Status != "previous" {
		t.Fatalf("p2 status = %q, want previous", got2.Status)
	}
}

func TestShowEmptyIDReturnsActive(t *testing.T) {
	db := openTestDB(t)
	p1, err := Init(db, writeSeats(t, testSeatsYAML))
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	got, err := Show(db, "")
	if err != nil {
		t.Fatalf("Show: %v", err)
	}
	if got.ID != p1.ID {
		t.Fatalf("Show(\"\") id = %s, want %s", got.ID, p1.ID)
	}
}

func TestRollbackNoPreviousClearsError(t *testing.T) {
	db := openTestDB(t)
	if _, err := Init(db, writeSeats(t, testSeatsYAML)); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if _, err := Rollback(db); err == nil {
		t.Fatal("Rollback without previous: want error")
	}
}

func TestPackNewIDAndToPackError(t *testing.T) {
	if NewID() == "" {
		t.Fatal("NewID empty")
	}
	db := openTestDB(t)
	row, err := db.InsertPack("active", `not-json{`, "pp", "")
	if err != nil {
		t.Fatalf("InsertPack: %v", err)
	}
	if _, err := toPack(row); err == nil {
		t.Fatal("toPack bad seats: want error")
	}
	if _, err := Active(db); err == nil {
		t.Fatal("Active bad seats: want error")
	}
	if _, err := Show(db, row.ID); err == nil {
		t.Fatal("Show bad seats: want error")
	}
}

func TestPublishValidation(t *testing.T) {
	db := openTestDB(t)
	if _, err := Init(db, writeSeats(t, testSeatsYAML)); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if _, err := Publish(db, map[Seat]SeatConfig{}, ""); err == nil {
		t.Fatal("Publish empty: want error")
	}
	partial := seatsWithModel("m2")
	delete(partial, SeatJudge)
	if _, err := Publish(db, partial, ""); err == nil {
		t.Fatal("Publish partial: want error")
	}
	if _, err := Publish(db, seatsWithModel("m2"), "run1"); err != nil {
		t.Fatalf("Publish with run id: %v", err)
	}
	if _, err := loadSeats("/nonexistent.yaml"); err == nil {
		t.Fatal("loadSeats missing: want error")
	}
	if _, err := loadSeats(writeSeats(t, "seats: {}")); err == nil {
		t.Fatal("loadSeats empty: want error")
	}
}
