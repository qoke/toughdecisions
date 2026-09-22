package grading

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/qoke/toughdecisions/internal/gateway"
	"github.com/qoke/toughdecisions/internal/store"
)

func ensureCalibCase(t *testing.T, db *store.DB) string {
	t.Helper()
	fam, err := db.UpsertFamily(&store.Family{FamilyKey: "F001", Name: "n", Split: "development", AcceptanceJSON: `{"must_notice":["deadline"]}`})
	if err != nil {
		t.Fatalf("UpsertFamily: %v", err)
	}
	c, err := db.UpsertCase(&store.Case{
		CaseKey: "case1", FamilyID: fam.ID, Variant: "base",
		InputJSON: `{"card":{"decision":"whether to move","context":"care commitment","priorities":"keep care","unusual":"u","history":"h","deadline":"soon","style":"warm"},"messages":[{"sender":"me","text":"help"}],"question":"what now?"}`,
	})
	if err != nil {
		t.Fatalf("UpsertCase: %v", err)
	}
	return c.ID
}

func putCalibItem(t *testing.T, db *store.DB, key string, humanScores map[string]int, humanFlags []string) {
	t.Helper()
	caseID := ensureCalibCase(t, db)
	hs, _ := json.Marshal(humanScores)
	hf, _ := json.Marshal(humanFlags)
	if _, err := db.UpsertCalibrationItem(&store.CalibrationItem{
		ItemKey: key, CaseID: caseID, Seat: "possibility",
		ResponseText:    "reference answer for " + key,
		HumanScoresJSON: string(hs), HumanFlagsJSON: string(hf),
	}); err != nil {
		t.Fatalf("UpsertCalibrationItem: %v", err)
	}
}

func evenScores(v int) map[string]int {
	return map[string]int{
		"grounding_and_calibration": v, "context_and_values_fidelity": v,
		"decision_insight": v, "practical_robustness": v, "role_execution": v,
	}
}

func TestCalibrateAdmits(t *testing.T) {
	db := openTestDB(t)
	cfg := testConfig(t)
	g := insertGrader(t, db, mkGrader("ka", "openai", "selection", false))
	putCalibItem(t, db, "item1", evenScores(3), nil)
	putCalibItem(t, db, "item2", evenScores(3), nil)
	fake := gateway.NewFake(map[string][]gateway.Step{
		g.Model: {
			{Content: gradeJSON(evenScores(3), "")},
			{Content: gradeJSON(evenScores(3), "")},
		},
	})
	svc := NewService(db, fake, cfg)

	// Act.
	reversals, err := svc.Calibrate(context.Background(), g)
	// Assert: zero reversals -> admitted with persisted calibration_json.
	if err != nil || reversals != 0 {
		t.Fatalf("Calibrate = %d, %v; want 0, nil", reversals, err)
	}
	stored, err := db.GetGraderConfig(g.ConfigHash)
	if err != nil {
		t.Fatalf("GetGraderConfig: %v", err)
	}
	if !stored.Admitted || stored.CalibrationJSON == nil {
		t.Fatalf("grader = %+v; want admitted with calibration_json", stored)
	}
}

func TestCalibrateRejectsOnDrift(t *testing.T) {
	db := openTestDB(t)
	cfg := testConfig(t)
	g := insertGrader(t, db, mkGrader("kb", "openai", "selection", false))
	putCalibItem(t, db, "item1", evenScores(1), nil)
	putCalibItem(t, db, "item2", evenScores(1), nil)
	// Arrange: grader means differ by >= 1.5 on both items -> 2 reversals.
	fake := gateway.NewFake(map[string][]gateway.Step{
		g.Model: {
			{Content: gradeJSON(evenScores(4), "")},
			{Content: gradeJSON(evenScores(4), "")},
		},
	})
	svc := NewService(db, fake, cfg)

	// Act.
	reversals, err := svc.Calibrate(context.Background(), g)
	// Assert: reversals exceed AdmitMaxReversals(1) -> not admitted.
	if err != nil || reversals != 2 {
		t.Fatalf("Calibrate = %d, %v; want 2, nil", reversals, err)
	}
	stored, err := db.GetGraderConfig(g.ConfigHash)
	if err != nil {
		t.Fatalf("GetGraderConfig: %v", err)
	}
	if stored.Admitted {
		t.Fatal("grader admitted despite 2 reversals")
	}
}

func TestCalibrateFlagMismatchIsReversal(t *testing.T) {
	db := openTestDB(t)
	cfg := testConfig(t)
	g := insertGrader(t, db, mkGrader("kc", "openai", "selection", false))
	// Arrange: identical means but human flagged, grader did not.
	putCalibItem(t, db, "item1", evenScores(3), []string{"coercive"})
	fake := gateway.NewFake(map[string][]gateway.Step{
		g.Model: {{Content: gradeJSON(evenScores(3), "")}},
	})
	svc := NewService(db, fake, cfg)

	// Act.
	reversals, err := svc.Calibrate(context.Background(), g)
	// Assert: XOR on flags counts as a reversal.
	if err != nil || reversals != 1 {
		t.Fatalf("Calibrate = %d, %v; want 1, nil", reversals, err)
	}
}

func TestCalibrateEmptyIsError(t *testing.T) {
	db := openTestDB(t)
	cfg := testConfig(t)
	g := insertGrader(t, db, mkGrader("kd", "openai", "selection", false))
	svc := NewService(db, gateway.NewFake(nil), cfg)

	// Act: no calibration items loaded.
	_, err := svc.Calibrate(context.Background(), g)
	// Assert: explicit error, and admission was invalidated (never vacuous).
	if err == nil {
		t.Fatal("Calibrate succeeded on empty set; want error")
	}
	stored, serr := db.GetGraderConfig(g.ConfigHash)
	if serr != nil {
		t.Fatalf("GetGraderConfig: %v", serr)
	}
	if stored.Admitted {
		t.Fatal("grader admitted on empty calibration set")
	}
}

func TestRecheckRotatesAndKeepsAdmission(t *testing.T) {
	db := openTestDB(t)
	cfg := testConfig(t)
	g := insertGrader(t, db, mkGrader("ke", "openai", "selection", true))
	putCalibItem(t, db, "item1", evenScores(3), nil)
	putCalibItem(t, db, "item2", evenScores(3), nil)
	putCalibItem(t, db, "item3", evenScores(3), nil)
	putCalibItem(t, db, "item4", evenScores(3), nil)
	fake := gateway.NewFake(map[string][]gateway.Step{
		g.Model: {
			{Content: gradeJSON(evenScores(3), "")},
			{Content: gradeJSON(evenScores(3), "")},
		},
	})
	svc := NewService(db, fake, cfg)

	// Act: recheck 2 of 4 items.
	reversals, err := svc.Recheck(context.Background(), g, 2)
	// Assert: only n calls, admission untouched.
	if err != nil || reversals != 0 {
		t.Fatalf("Recheck = %d, %v; want 0, nil", reversals, err)
	}
	if fake.CallCount() != 2 {
		t.Fatalf("calls = %d; want 2", fake.CallCount())
	}
	stored, err := db.GetGraderConfig(g.ConfigHash)
	if err != nil {
		t.Fatalf("GetGraderConfig: %v", err)
	}
	if !stored.Admitted {
		t.Fatal("Recheck changed admission; want untouched")
	}
}
