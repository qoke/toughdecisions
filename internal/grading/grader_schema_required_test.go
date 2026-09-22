package grading

import (
	"context"
	"strings"
	"testing"

	"github.com/qoke/toughdecisions/internal/gateway"
	"github.com/qoke/toughdecisions/internal/schema"
)

// TestGradeRejectsGraderWithoutStrictSchema is the D7 fail-closed proof:
// a grader whose registry entry has json_schema=false is rejected with a
// clear capability error before any gateway call, instead of being
// silently downgraded to response_format json_object (which produced the
// abbreviated-criterion degraded grades in the council-smoke-i evidence).
func TestGradeRejectsGraderWithoutStrictSchema(t *testing.T) {
	db := openTestDB(t)
	cfg := testConfig(t)
	g := mkGrader("gnoschema", "google", "selection", true)
	g.Model = "g-object"
	g = insertGrader(t, db, g)
	resp := mkResponse(t, db, "possibility", "some thoughtful advice text here")
	fake := gateway.NewFake(map[string][]gateway.Step{
		g.Model: {{Content: gradeJSON(schemaScores(), "")}},
	})
	svc := NewService(db, fake, cfg)
	svc.SetModels(loadGraderTestRegistry(t))

	_, err := svc.Grade(context.Background(), schema.CaseInput{}, "", resp, g, nil)
	if err == nil {
		t.Fatal("Grade accepted a grader without strict json_schema; want rejection")
	}
	if !strings.Contains(err.Error(), "does not support strict structured outputs") ||
		!strings.Contains(err.Error(), "g-object") {
		t.Fatalf("Grade err = %v; want the model named with the missing strict-structured-outputs capability", err)
	}
	if fake.CallCount() != 0 {
		t.Fatalf("calls = %d; want 0 (config rejected before any gateway call)", fake.CallCount())
	}
}

// TestGradeSucceedsWithStrictSchemaModel is the no-regression companion:
// a grader whose model supports json_schema still grades normally with
// the strict absolute schema on the call.
func TestGradeSucceedsWithStrictSchemaModel(t *testing.T) {
	db := openTestDB(t)
	cfg := testConfig(t)
	g := mkGrader("gstrict", "openai", "selection", true)
	g.Model = "g-schema"
	g = insertGrader(t, db, g)
	resp := mkResponse(t, db, "possibility", "some thoughtful advice text here")
	fake := gateway.NewFake(map[string][]gateway.Step{
		g.Model: {{Content: gradeJSON(schemaScores(), "")}},
	})
	svc := NewService(db, fake, cfg)
	svc.SetModels(loadGraderTestRegistry(t))

	if _, err := svc.Grade(context.Background(), schema.CaseInput{}, "", resp, g, nil); err != nil {
		t.Fatalf("Grade: %v", err)
	}
	if fake.CallCount() != 1 {
		t.Fatalf("calls = %d; want 1", fake.CallCount())
	}
	rf := fake.Calls[0].ResponseFormat
	if rf == nil || rf.Type != "json_schema" || !rf.Strict {
		t.Fatalf("call format = %+v; want strict json_schema", rf)
	}
}

// TestCalibrateRejectsGraderWithoutStrictSchema proves the early check on
// the calibrate path: rejection happens before any gateway call.
func TestCalibrateRejectsGraderWithoutStrictSchema(t *testing.T) {
	db := openTestDB(t)
	cfg := testConfig(t)
	g := mkGrader("gcal", "google", "selection", true)
	g.Model = "g-object"
	g = insertGrader(t, db, g)
	fake := gateway.NewFake(map[string][]gateway.Step{
		g.Model: {{Content: gradeJSON(schemaScores(), "")}},
	})
	svc := NewService(db, fake, cfg)
	svc.SetModels(loadGraderTestRegistry(t))

	if _, err := svc.Calibrate(context.Background(), g); err == nil ||
		!strings.Contains(err.Error(), "does not support strict structured outputs") {
		t.Fatalf("Calibrate err = %v; want strict-structured-output rejection", err)
	}
	if fake.CallCount() != 0 {
		t.Fatalf("calls = %d; want 0 (config rejected before any gateway call)", fake.CallCount())
	}
}
