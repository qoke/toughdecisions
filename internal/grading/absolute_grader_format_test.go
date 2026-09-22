package grading

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/qoke/toughdecisions/internal/gateway"
	"github.com/qoke/toughdecisions/internal/models"
	"github.com/qoke/toughdecisions/internal/schema"
)

const graderModelsYAML = `models:
  - id: g-schema
    family: openai
    expected_response_model_prefixes: ["g-schema"]
    supports: {temperature: true, top_p: true, reasoning_effort: true, json_schema: true, json_object: true}
  - id: g-object
    family: other
    expected_response_model_prefixes: ["g-object"]
    supports: {temperature: false, top_p: false, reasoning_effort: false, json_schema: false, json_object: true}
  - id: g-plain
    family: other
    expected_response_model_prefixes: ["g-plain"]
    supports: {temperature: false, top_p: false, reasoning_effort: false, json_schema: false, json_object: false}
`

func loadGraderTestRegistry(t *testing.T) *models.Registry {
	t.Helper()
	path := filepath.Join(t.TempDir(), "models.yaml")
	if err := os.WriteFile(path, []byte(graderModelsYAML), 0o644); err != nil {
		t.Fatalf("write models.yaml: %v", err)
	}
	r, err := models.LoadRegistry(path)
	if err != nil {
		t.Fatalf("LoadRegistry: %v", err)
	}
	return r
}

func schemaScores() map[string]int {
	return map[string]int{
		"grounding_and_calibration": 2, "context_and_values_fidelity": 2,
		"decision_insight": 2, "practical_robustness": 2, "role_execution": 2,
	}
}

// TestGraderResponseFormat honors per-model capability with no silent
// fallback: strict json_schema (the absolute schema) when supported, else
// nil. A json_object-only model gets no schema (ValidateGrader rejects it
// before any call), a plain model gets nil, an unknown model gets nil, and
// a nil registry sends nothing.
func TestGraderResponseFormat(t *testing.T) {
	db := openTestDB(t)
	cfg := testConfig(t)
	svc := NewService(db, gateway.NewFake(nil), cfg)
	svc.SetModels(loadGraderTestRegistry(t))

	if rf := svc.graderResponseFormat("g-schema"); rf == nil || rf.Type != "json_schema" || !rf.Strict ||
		rf.SchemaName != "absolute" || string(rf.Schema) != schema.AbsoluteJSONSchema {
		t.Fatalf("g-schema format = %+v; want strict json_schema absolute", rf)
	}
	if rf := svc.graderResponseFormat("g-object"); rf != nil {
		t.Fatalf("g-object format = %+v; want nil (no json_object downgrade for graders)", rf)
	}
	if rf := svc.graderResponseFormat("g-plain"); rf != nil {
		t.Fatalf("g-plain format = %+v; want nil", rf)
	}
	if rf := svc.graderResponseFormat("unknown-model"); rf != nil {
		t.Fatalf("unknown format = %+v; want nil", rf)
	}

	bare := NewService(db, gateway.NewFake(nil), cfg)
	if rf := bare.graderResponseFormat("g-schema"); rf != nil {
		t.Fatalf("nil-registry format = %+v; want nil", rf)
	}
}

// TestGradeSendsGraderResponseFormat proves the wiring end to end: a Grade
// through a models-attached service carries the strict absolute schema on
// the fake call, and the fake's own strict-schema gate accepts it (no 400).
func TestGradeSendsGraderResponseFormat(t *testing.T) {
	db := openTestDB(t)
	cfg := testConfig(t)
	g := mkGrader("gfmt", "openai", "selection", true)
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
	if len(fake.Calls) != 1 {
		t.Fatalf("calls = %d; want 1", len(fake.Calls))
	}
	rf := fake.Calls[0].ResponseFormat
	if rf == nil || rf.Type != "json_schema" || !rf.Strict || string(rf.Schema) != schema.AbsoluteJSONSchema {
		t.Fatalf("call format = %+v; want strict absolute json_schema", rf)
	}
}

// TestGradeSendsJSONObjectForObjectOnlyModel proves the fail-closed rule:
// a json_object-only grader model is rejected with the capability error
// and makes no gateway call.
func TestGradeSendsJSONObjectForObjectOnlyModel(t *testing.T) {
	db := openTestDB(t)
	cfg := testConfig(t)
	g := mkGrader("gobj", "other", "selection", true)
	g.Model = "g-object"
	g = insertGrader(t, db, g)
	resp := mkResponse(t, db, "possibility", "some thoughtful advice text here")
	fake := gateway.NewFake(map[string][]gateway.Step{
		g.Model: {{Content: gradeJSON(schemaScores(), "")}},
	})
	svc := NewService(db, fake, cfg)
	svc.SetModels(loadGraderTestRegistry(t))

	if _, err := svc.Grade(context.Background(), schema.CaseInput{}, "", resp, g, nil); err == nil ||
		!strings.Contains(err.Error(), "does not support strict structured outputs") {
		t.Fatalf("Grade err = %v; want strict-structured-output rejection", err)
	}
	if fake.CallCount() != 0 {
		t.Fatalf("calls = %d; want 0 (rejected before any gateway call)", fake.CallCount())
	}
}

// TestParseAbsoluteGradeRootWrappedAndWeaknessVariants covers the root-level
// per-criterion shape (smoke-d selection-a first response) plus the weakness
// harvest variants: root passages feed SupportingPassages, RubricCriteria
// exposes the canonical five keys, and weakness keys map to MCW.
func TestParseAbsoluteGradeRootWrappedAndWeaknessVariants(t *testing.T) {
	if got := RubricCriteria(); len(got) != 5 || got[0] != "grounding_and_calibration" {
		t.Fatalf("RubricCriteria = %v; want the five canonical keys", got)
	}
	if got := append([]string(nil), RubricCriteria()...); len(got) != 5 {
		t.Fatalf("RubricCriteria copy = %v", got)
	}
	raw := `{
	  "grounding_and_calibration": {"score": 1, "supporting_passage": "ask nearby family first", "assessment": "no such fact in the record"},
	  "context_and_values_fidelity": {"score": 2, "supporting_passage": "p2", "assessment": "a2"},
	  "decision_insight": {"score": 2, "supporting_passage": "p3", "assessment": "a3"},
	  "practical_robustness": {"score": 2, "supporting_passage": "p4", "assessment": "a4"},
	  "role_execution": {"score": 3, "supporting_passage": "p5", "assessment": "a5"},
	  "supporting_passages": [],
	  "notes_check": {"noticed": [], "missed": [], "beyond_notes": []},
	  "weakness_summary": "invents a material fact"
	}`
	grade, ok := parseAbsoluteGrade(raw)
	if !ok {
		t.Fatal("parseAbsoluteGrade rejected the root-wrapped shape")
	}
	if grade.Scores["grounding_and_calibration"] != 1 || grade.Scores["role_execution"] != 3 {
		t.Fatalf("scores = %v; want unwrapped root scores", grade.Scores)
	}
	if got := grade.SupportingPassages["ask nearby family first"]; !strings.Contains(got, "no such fact") {
		t.Fatalf("harvested passage = %q; want the root assessment", got)
	}
	if grade.MostConsequentialWeakness == nil || !strings.Contains(grade.MostConsequentialWeakness.Passage, "material fact") {
		t.Fatalf("weakness = %+v; want weakness_summary harvested", grade.MostConsequentialWeakness)
	}
}

// TestParseAbsoluteGradeRejectsGenuinelyUnparseable pins the replay ruling:
// the 4 remaining evidence failures are rejected, never zero-filled.
//   - scoreless flags-only (smoke-d block 5): flags but no scores key.
//   - severity typo (smoke-c block 8 shape): "severity","description" is not
//     valid JSON (missing colon), so the whole payload is malformed.
//   - stray-comma flags list (smoke-a block 16 shape): `}], {` is not valid
//     JSON, so the payload is malformed.
//   - truncated prose (smoke-a block 13 shape): length-truncated mid-JSON.
func TestParseAbsoluteGradeRejectsGenuinelyUnparseable(t *testing.T) {
	scoreless := `{"flags":[{"flag_type":"unsupported_claim","severity":"major","description":"empty case card, confident verdict","supporting_passage":"Take the job no matter what"}],"overall_note":"no grounding for any specific advice","other_strengths":null}`
	severityTypo := `{"flags":[{"type":"insufficient_grounding","severity","description":"placeholder"}],"scores":{"grounding_and_calibration":0,"context_and_values_fidelity":0,"decision_insight":0,"practical_robustness":0,"role_execution":0},"supporting_passages":[],"notes_check":{"noticed":[],"missed":[],"beyond_notes":[]}}`
	strayComma := `{"flags": [{"flag_type": "unsupported_claim", "quoted_text": "q", "why_flagged": "w", "supporting_passage": ""}], {"flag_type": "x"}, "scores": {"grounding_and_calibration": 0, "context_and_values_fidelity": 0, "decision_insight": 0, "practical_robustness": 0, "role_execution": 0}}`
	truncated := `{"case_summary": "empty", "scores": {"grounding_and_calibration": 0,`
	for _, tc := range []struct {
		name string
		raw  string
	}{
		{"scoreless flags-only", scoreless},
		{"severity typo malformed", severityTypo},
		{"stray-comma flags malformed", strayComma},
		{"truncated mid-JSON", truncated},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if grade, ok := parseAbsoluteGrade(tc.raw); ok {
				t.Fatalf("parseAbsoluteGrade accepted %s: %+v", tc.name, grade)
			}
		})
	}
}
