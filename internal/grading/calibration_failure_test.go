package grading

import (
	"context"
	"strings"
	"testing"

	"github.com/qoke/toughdecisions/internal/gateway"
)

// TestRecordCalibrationFailureIsRecordedForThatGrader pins D6: one
// grader's hard failure (double unparseable response) is recorded for
// that grader (failed/absent, with the reason) and never substitutes a
// grade — the sibling grader still calibrates and reports independently.
func TestRecordCalibrationFailureIsRecordedForThatGrader(t *testing.T) {
	db := openTestDB(t)
	cfg := testConfig(t)
	bad := insertGrader(t, db, mkGrader("d6bad", "openai", "selection", false))
	putCalibItem(t, db, "d6-item1", evenScores(3), nil)
	putCalibItem(t, db, "d6-item2", evenScores(3), nil)
	fake := gateway.NewFake(map[string][]gateway.Step{
		// Shared fake model queue: the bad grader consumes garbage twice
		// (initial + one retry), then the good grader's grades follow.
		"model-d6bad": {
			{Content: "garbage one"},
			{Content: "garbage two"},
		},
	})
	// Point the bad grader at its own queued model; the good grader gets a
	// valid grade.
	good := insertGrader(t, db, mkGrader("d6good", "anthropic", "selection", false))
	fake.Scripts[good.Model] = []gateway.Step{
		{Content: gradeJSON(evenScores(3), "")},
		{Content: gradeJSON(evenScores(3), "")},
	}
	svc := NewService(db, fake, cfg)

	// Act: the bad grader hard-fails; record it for that grader.
	_, err := svc.Calibrate(context.Background(), bad)
	if err == nil || !strings.Contains(err.Error(), "unparseable calibration grade") {
		t.Fatalf("bad Calibrate = %v; want unparseable-grade error", err)
	}
	if ferr := svc.RecordCalibrationFailure(bad, err); ferr != nil {
		t.Fatalf("RecordCalibrationFailure: %v", ferr)
	}

	// Assert: the bad grader is recorded failed/absent with the reason.
	stored, serr := db.GetGraderConfig(bad.ConfigHash)
	if serr != nil {
		t.Fatalf("GetGraderConfig: %v", serr)
	}
	if stored.Admitted {
		t.Fatal("failed grader admitted; want failed/absent")
	}
	if stored.CalibrationJSON == nil || !strings.Contains(*stored.CalibrationJSON, "unparseable calibration grade") {
		t.Fatalf("calibration_json = %v; want the failure reason recorded", stored.CalibrationJSON)
	}

	// Assert: the sibling still runs and reports (not masked).
	reversals, err := svc.Calibrate(context.Background(), good)
	if err != nil || reversals != 0 {
		t.Fatalf("good Calibrate = %d, %v; want 0, nil", reversals, err)
	}
	gstored, serr := db.GetGraderConfig(good.ConfigHash)
	if serr != nil {
		t.Fatalf("GetGraderConfig good: %v", serr)
	}
	if !gstored.Admitted {
		t.Fatal("good grader not admitted; the bad grader masked it")
	}
}
