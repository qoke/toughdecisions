package grading

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"

	"github.com/qoke/toughdecisions/internal/gateway"
	"github.com/qoke/toughdecisions/internal/schema"
	"github.com/qoke/toughdecisions/internal/store"
)

func badScoresItem(caseID string) *store.CalibrationItem {
	return &store.CalibrationItem{
		ItemKey: "bad-scores", CaseID: caseID, Seat: "possibility",
		ResponseText: "reference answer", HumanScoresJSON: "{bad",
		HumanFlagsJSON: "[]",
	}
}

func badFlagsItem(caseID string) *store.CalibrationItem {
	return &store.CalibrationItem{
		ItemKey: "bad-flags", CaseID: caseID, Seat: "possibility",
		ResponseText: "reference answer", HumanScoresJSON: `{"a":3}`,
		HumanFlagsJSON: "[bad",
	}
}

// M3: invalidateAdmission propagates a missing grader-config row.
func TestCalibratePropagatesMissingGraderRow(t *testing.T) {
	db := openTestDB(t)
	cfg := testConfig(t)
	svc := NewService(db, gateway.NewFake(nil), cfg)
	ghost := mkGrader("ghost", "openai", "selection", false)

	// Act: config_hash was never upserted.
	_, err := svc.Calibrate(context.Background(), schema.CaseInput{}, "", ghost)

	// Assert: DB failure surfaces, no vacuous admission.
	if err == nil || !strings.Contains(err.Error(), "load grader config") {
		t.Fatalf("Calibrate ghost = %v; want load-grader-config error", err)
	}
}

// M3: persistCalibration propagates a store failure (deleted row between
// invalidate and persist).
func TestCalibratePropagatesStoreCalibrationFailure(t *testing.T) {
	db := openTestDB(t)
	cfg := testConfig(t)
	g := insertGrader(t, db, mkGrader("pc", "openai", "selection", false))
	putCalibItem(t, db, "pc-item", evenScores(3), nil)
	fake := gateway.NewFake(map[string][]gateway.Step{
		g.Model: {{Content: gradeJSON(evenScores(3), "")}},
	})
	svc := NewService(db, fake, cfg)

	// Arrange: remove the row after invalidateAdmission ran is impossible
	// externally, so exercise persistCalibration directly on a ghost hash.
	ghost := *g
	ghost.ConfigHash = "cfg-missing"
	if err := svc.persistCalibration(&ghost, nil, 0); err == nil {
		t.Fatal("persistCalibration ghost succeeded; want error")
	}
}

// M3: gradeItems rejects malformed human-scores JSON.
func TestGradeItemsRejectsMalformedHumanScores(t *testing.T) {
	db := openTestDB(t)
	cfg := testConfig(t)
	g := insertGrader(t, db, mkGrader("ms", "openai", "selection", false))
	caseID := ensureCalibCase(t, db)
	if _, err := db.UpsertCalibrationItem(badScoresItem(caseID)); err != nil {
		t.Fatalf("UpsertCalibrationItem: %v", err)
	}
	fake := gateway.NewFake(map[string][]gateway.Step{
		g.Model: {{Content: gradeJSON(evenScores(3), "")}},
	})
	svc := NewService(db, fake, cfg)

	// Act.
	_, err := svc.Calibrate(context.Background(), schema.CaseInput{}, "", g)

	// Assert: malformed human scores propagate as a decode error.
	if err == nil || !strings.Contains(err.Error(), "decode human scores") {
		t.Fatalf("Calibrate malformed = %v; want decode-human-scores error", err)
	}
}

// M3: gradeItems rejects malformed human-flags JSON.
func TestGradeItemsRejectsMalformedHumanFlags(t *testing.T) {
	db := openTestDB(t)
	cfg := testConfig(t)
	g := insertGrader(t, db, mkGrader("mf", "openai", "selection", false))
	caseID := ensureCalibCase(t, db)
	if _, err := db.UpsertCalibrationItem(badFlagsItem(caseID)); err != nil {
		t.Fatalf("UpsertCalibrationItem: %v", err)
	}
	fake := gateway.NewFake(map[string][]gateway.Step{
		g.Model: {{Content: gradeJSON(evenScores(3), "")}},
	})
	svc := NewService(db, fake, cfg)

	// Act.
	_, err := svc.Calibrate(context.Background(), schema.CaseInput{}, "", g)

	// Assert.
	if err == nil || !strings.Contains(err.Error(), "decode human flags") {
		t.Fatalf("Calibrate malformed flags = %v; want decode-human-flags error", err)
	}
}

// M3: Cover defaults an empty judge outcome to "na".
func TestCoverDefaultsEmptyJudgeOutcome(t *testing.T) {
	db := openTestDB(t)
	cfg := testConfig(t)
	g := insertGrader(t, db, mkGrader("cv", "openai", "selection", true))
	fake := gateway.NewFake(map[string][]gateway.Step{
		g.Model: {{Content: `{"rows":[{"issue_id":"I1","issue_text":"t","is_planted":true,"noticed_by":["possibility"],"unsupported_by":[]}]}`}},
	})
	svc := NewService(db, fake, cfg)

	// Act: grader omits judge_outcome.
	err := svc.Cover(context.Background(), schema.CaseInput{}, "run-cov", "case-cov",
		"candidate:x", []Issue{{ID: "I1", Text: "t", IsPlanted: true}},
		map[string]string{"possibility": "view"}, "judge text", g)

	// Assert: row stored with outcome "na".
	if err != nil {
		t.Fatalf("Cover: %v", err)
	}
	rows, err := db.ListCoverageByRun("run-cov")
	if err != nil || len(rows) != 1 || rows[0].JudgeOutcome != "na" {
		t.Fatalf("coverage rows = %+v, %v; want one na row", rows, err)
	}
}

// M2/H2: a grade cache hit hydrates flags; concurrent same-key inserts
// dedupe to one row with no duplicated flag rows.
func TestGradeCacheHitHydratesFlags(t *testing.T) {
	db := openTestDB(t)
	cfg := testConfig(t)
	g := insertGrader(t, db, mkGrader("hf", "openai", "selection", true))
	resp := mkResponse(t, db, "possibility", "hydration response body here")
	scores := map[string]int{
		"grounding_and_calibration": 3, "context_and_values_fidelity": 3,
		"decision_insight": 3, "practical_robustness": 3, "role_execution": 3,
	}
	flag := `{"type":"coercive","passage":"p","violated":"v"}`
	fake := gateway.NewFake(map[string][]gateway.Step{
		g.Model: {{Content: gradeJSON(scores, flag)}},
	})
	svc := NewService(db, fake, cfg)

	// Arrange: populate the cache (one flag row).
	first, err := svc.Grade(context.Background(), schema.CaseInput{}, "", resp, g, nil)
	if err != nil {
		t.Fatalf("first Grade: %v", err)
	}

	// Act: cache hit (no scripted steps left — any call would fail).
	second, err := svc.Grade(context.Background(), schema.CaseInput{}, "", resp, g, nil)

	// Assert: same grade, hydrated flags, no new model call.
	if err != nil {
		t.Fatalf("second Grade: %v", err)
	}
	if second.Grade.ID != first.Grade.ID {
		t.Fatalf("hit returned %s; want %s", second.Grade.ID, first.Grade.ID)
	}
	if len(second.Flags) != 1 || second.Flags[0].GradeID != first.Grade.ID {
		t.Fatalf("hit flags = %+v; want the stored flag row", second.Flags)
	}
	if fake.CallCount() != 1 {
		t.Fatalf("calls = %d; want 1", fake.CallCount())
	}
	if _, err := db.GetGradeByCacheKey("missing"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("missing grade err = %v; want ErrNoRows", err)
	}
}
