package grading

import (
	"context"
	"strings"
	"testing"

	"github.com/qoke/toughdecisions/internal/gateway"
	"github.com/qoke/toughdecisions/internal/schema"
)

// TestCalibrateAcceptsRealShapedArrayPassages pins D2: the calibration path
// parses the array supporting_passages shape real models emit (the live
// passages-array-unsupported failures), using the same tolerant parser as the
// Grade path. Routing calibration back through strict ParseLenient fails this.
func TestCalibrateAcceptsRealShapedArrayPassages(t *testing.T) {
	db := openTestDB(t)
	cfg := testConfig(t)
	g := insertGrader(t, db, mkGrader("kreal", "openai", "selection", false))
	putCalibItem(t, db, "item1", evenScores(0), []string{"hallucination"})
	// Real-shaped: flat five scores, array passages with non-empty
	// passage+assessment, string flag list, acceptance_notes variant.
	raw := `{"scores": {"grounding_and_calibration": 0, "context_and_values_fidelity": 0, "decision_insight": 0, "practical_robustness": 0, "role_execution": 0}, "supporting_passages": [{"passage": "Thursday pickup", "assessment": "no Thursday pickup in the case card"}], "flags": ["hallucination"], "acceptance_notes": ["frame conditionally"]}`
	fake := gateway.NewFake(map[string][]gateway.Step{g.Model: {{Content: raw}}})
	svc := NewService(db, fake, cfg)

	reversals, err := svc.Calibrate(context.Background(), schema.CaseInput{}, "", g)
	if err != nil {
		t.Fatalf("Calibrate real-shaped = %v; want success", err)
	}
	if reversals != 0 {
		t.Fatalf("reversals = %d; want 0 (identical means, both flagged)", reversals)
	}
	stored, err := db.GetGraderConfig(g.ConfigHash)
	if err != nil {
		t.Fatalf("GetGraderConfig: %v", err)
	}
	if !stored.Admitted || stored.CalibrationJSON == nil || !strings.Contains(*stored.CalibrationJSON, "grader_mean") {
		t.Fatalf("calibration not persisted with scores: %+v", stored)
	}
}

// TestCalibrateRejectsMissingScores pins D3 on the calibration path: a
// payload with no scores key is unparseable, so Calibrate surfaces the
// one-retry error instead of persisting a mean-0 grade with a phantom
// reversal.
func TestCalibrateRejectsMissingScores(t *testing.T) {
	db := openTestDB(t)
	cfg := testConfig(t)
	g := insertGrader(t, db, mkGrader("knoscore", "openai", "selection", false))
	putCalibItem(t, db, "item1", evenScores(3), nil)
	raw := `{"overall_assessment": "fine", "supporting_passages": [], "notes_check": {"noticed": [], "missed": [], "beyond_notes": []}}`
	fake := gateway.NewFake(map[string][]gateway.Step{g.Model: {{Content: raw}, {Content: raw}}})
	svc := NewService(db, fake, cfg)

	res, err := svc.Calibrate(context.Background(), schema.CaseInput{}, "", g)
	if err == nil {
		t.Fatalf("Calibrate missing-scores = %d, nil; want unparseable error", res)
	}
	if !strings.Contains(err.Error(), "unparseable") {
		t.Fatalf("err = %v; want unparseable calibration grade", err)
	}
	if fake.CallCount() != 2 {
		t.Fatalf("calls = %d; want exactly 2 (one retry)", fake.CallCount())
	}
	stored, err := db.GetGraderConfig(g.ConfigHash)
	if err != nil {
		t.Fatalf("GetGraderConfig: %v", err)
	}
	if stored.Admitted {
		t.Fatal("grader admitted on unparseable calibration grade")
	}
}
