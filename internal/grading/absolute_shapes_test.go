package grading

import (
	"context"
	"strings"
	"testing"

	"github.com/qoke/toughdecisions/internal/gateway"
	"github.com/qoke/toughdecisions/internal/schema"
)

// evidenceArrayPayload is the shape real models emit (grader block 4 of the
// council-smoke-a run): flat 0-4 scores, supporting_passages as an array of
// {passage, assessment} objects, flags as objects, and string-list notes under
// "weaknesses"/"acceptance" keys.
const evidenceArrayPayload = `{
  "scores": {
    "grounding_and_calibration": 0,
    "context_and_values_fidelity": 0,
    "decision_insight": 0,
    "practical_robustness": 0,
    "role_execution": 0
  },
  "flags": [
    {
      "type": "unsupported_claim",
      "severity": "critical",
      "description": "The response asserts that the school waived all late fees this term, but the supplied case card and messages contain no such information."
    }
  ],
  "supporting_passages": [
    {
      "passage": "The school has waived all late fees this term",
      "assessment": "This is an externally verifiable factual claim presented as certain despite having no support in the supplied material."
    },
    {
      "passage": "so pickup timing no longer matters",
      "assessment": "This overreaches from fees to all consequences of pickup timing and ignores possible non-financial constraints."
    }
  ],
  "weaknesses": [
    "Invents a material fact.",
    "Treats one possible financial consequence as the only relevant consideration."
  ],
  "acceptance": {
    "acceptable": false,
    "reason": "The response is unsupported by the record."
  }
}`

// evidenceNestedScoresPayload is the shape grader block 7 of the same run
// emits: each criterion score wrapped one level deeper, with per-criterion
// supporting_passages, plus flag objects and "acceptance_notes".
const evidenceNestedScoresPayload = `{
  "scores": {
    "grounding_and_calibration": {"score": 1, "supporting_passages": ["Thursday pickup"], "reason": "States facts not in evidence."},
    "context_and_values_fidelity": {"score": 1, "reason": "No relationship context supplied."},
    "decision_insight": {"score": 2, "reason": "Identifies a planning risk in the abstract."},
    "practical_robustness": {"score": 2, "reason": "Sensible safeguard if the facts were real."},
    "role_execution": {"score": 3, "reason": "Focuses on a concrete vulnerability."}
  },
  "flags": [
    {"type": "unsupported_specifics", "passage": "Thursday pickup", "violated": "grounding"}
  ],
  "supporting_passages": [
    {"passage": "Thursday pickup", "assessment": "No Thursday pickup is described in the supplied case card."}
  ],
  "acceptance_notes": ["Frame concerns conditionally rather than asserting invented facts."]
}`

func TestParseAbsoluteGradeEvidenceArrayPassages(t *testing.T) {
	// Arrange: a real evidence-derived payload with array passages.
	// Act.
	grade, ok := parseAbsoluteGrade(evidenceArrayPayload)
	// Assert: it parses, and the passages are extracted with content, not
	// merely "no error".
	if !ok {
		t.Fatal("parseAbsoluteGrade rejected the evidence array payload")
	}
	for _, want := range []struct {
		passage, assessment string
	}{
		{"The school has waived all late fees this term", "externally verifiable"},
		{"so pickup timing no longer matters", "overreaches from fees"},
	} {
		got, ok := grade.SupportingPassages[want.passage]
		if !ok {
			t.Fatalf("missing passage %q; got %v", want.passage, grade.SupportingPassages)
		}
		if !strings.Contains(got, want.assessment) {
			t.Fatalf("passage %q assessment = %q; want it to contain %q", want.passage, got, want.assessment)
		}
	}
	if len(grade.Scores) != 5 {
		t.Fatalf("scores = %v; want all five criteria", grade.Scores)
	}
	if len(grade.Flags) != 1 || grade.Flags[0].Type != "unsupported_claim" {
		t.Fatalf("flags = %+v; want the one unsupported_claim flag", grade.Flags)
	}
}

func TestParseAbsoluteGradeNestedScores(t *testing.T) {
	// Arrange: wrapped per-criterion score objects, array passages.
	// Act.
	grade, ok := parseAbsoluteGrade(evidenceNestedScoresPayload)
	// Assert: scores unwrap to the flat rubric values.
	if !ok {
		t.Fatal("parseAbsoluteGrade rejected the nested-scores payload")
	}
	want := map[string]int{
		"grounding_and_calibration": 1, "context_and_values_fidelity": 1,
		"decision_insight": 2, "practical_robustness": 2, "role_execution": 3,
	}
	for k, v := range want {
		if grade.Scores[k] != v {
			t.Fatalf("scores[%q] = %d; want %d (all: %v)", k, grade.Scores[k], v, grade.Scores)
		}
	}
	if got := grade.SupportingPassages["Thursday pickup"]; !strings.Contains(got, "No Thursday pickup") {
		t.Fatalf("passage assessment = %q; want the mapped assessment", got)
	}
}

func TestParseAbsoluteGradeLegacyMapPassages(t *testing.T) {
	// Arrange: the previously-assumed map form must keep working.
	raw := gradeJSON(map[string]int{
		"grounding_and_calibration": 3, "context_and_values_fidelity": 3,
		"decision_insight": 2, "practical_robustness": 3, "role_execution": 3,
	}, "")
	// Act.
	grade, ok := parseAbsoluteGrade(raw)
	// Assert.
	if !ok {
		t.Fatal("parseAbsoluteGrade rejected the legacy map payload")
	}
	if grade.SupportingPassages["decision_insight"] != "p" {
		t.Fatalf("passages = %v; want the legacy map preserved", grade.SupportingPassages)
	}
}

func TestParseAbsoluteGradeRejectsMalformed(t *testing.T) {
	cases := []struct {
		name string
		raw  string
	}{
		{"not json", "this is not json at all"},
		{"truncated json", `{"scores": {"grounding_and_calibration": 0,`},
		{"missing scores", `{"supporting_passages": [], "notes_check": {"noticed": [], "missed": [], "beyond_notes": []}}`},
		{"score out of range", `{"scores": {"grounding_and_calibration": 5, "context_and_values_fidelity": 0, "decision_insight": 0, "practical_robustness": 0, "role_execution": 0}, "supporting_passages": [], "notes_check": {"noticed": [], "missed": [], "beyond_notes": []}}`},
		{"missing criterion", `{"scores": {"grounding_and_calibration": 0}, "supporting_passages": [], "notes_check": {"noticed": [], "missed": [], "beyond_notes": []}}`},
		{"wrong score type", `{"scores": {"grounding_and_calibration": "high", "context_and_values_fidelity": 0, "decision_insight": 0, "practical_robustness": 0, "role_execution": 0}, "supporting_passages": [], "notes_check": {"noticed": [], "missed": [], "beyond_notes": []}}`},
		{"array passage missing assessment", `{"scores": {"grounding_and_calibration": 0, "context_and_values_fidelity": 0, "decision_insight": 0, "practical_robustness": 0, "role_execution": 0}, "supporting_passages": [{"passage": "only a quote"}], "notes_check": {"noticed": [], "missed": [], "beyond_notes": []}}`},
		{"array passage empty strings", `{"scores": {"grounding_and_calibration": 0, "context_and_values_fidelity": 0, "decision_insight": 0, "practical_robustness": 0, "role_execution": 0}, "supporting_passages": [{"passage": "", "assessment": ""}], "notes_check": {"noticed": [], "missed": [], "beyond_notes": []}}`},
		{"passages wrong type", `{"scores": {"grounding_and_calibration": 0, "context_and_values_fidelity": 0, "decision_insight": 0, "practical_robustness": 0, "role_execution": 0}, "supporting_passages": 42, "notes_check": {"noticed": [], "missed": [], "beyond_notes": []}}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if grade, ok := parseAbsoluteGrade(tc.raw); ok {
				t.Fatalf("parseAbsoluteGrade accepted malformed input: %+v", grade)
			}
		})
	}
}

func TestGradePersistsEvidenceArrayPassages(t *testing.T) {
	db := openTestDB(t)
	cfg := testConfig(t)
	g := insertGrader(t, db, mkGrader("garr", "openai", "selection", true))
	resp := mkResponse(t, db, "possibility", "some thoughtful advice text here")
	fake := gateway.NewFake(map[string][]gateway.Step{
		g.Model: {{Content: evidenceArrayPayload}},
	})
	svc := NewService(db, fake, cfg)

	// Act.
	res, err := svc.Grade(context.Background(), schema.CaseInput{}, "", resp, g, nil)
	// Assert: the evidence-shaped grade persists with its passages intact.
	if err != nil || res == nil {
		t.Fatalf("Grade = %+v, %v; want success on evidence-shaped output", res, err)
	}
	if !strings.Contains(res.Grade.PassagesJSON, "waived all late fees") {
		t.Fatalf("PassagesJSON = %s; want the evidence passage persisted", res.Grade.PassagesJSON)
	}
	if got := fake.Calls[0].MaxOutputTokens; got != graderMaxOutputTokens {
		t.Fatalf("MaxOutputTokens = %d; want %d", got, graderMaxOutputTokens)
	}
}

func TestGradeSendsRaisedTokenBudget(t *testing.T) {
	db := openTestDB(t)
	cfg := testConfig(t)
	g := insertGrader(t, db, mkGrader("gbud", "openai", "selection", true))
	resp := mkResponse(t, db, "possibility", "another thoughtful advice body here")
	scores := map[string]int{
		"grounding_and_calibration": 2, "context_and_values_fidelity": 2,
		"decision_insight": 2, "practical_robustness": 2, "role_execution": 2,
	}
	fake := gateway.NewFake(map[string][]gateway.Step{
		g.Model: {{Content: gradeJSON(scores, "")}},
	})
	svc := NewService(db, fake, cfg)

	// Act.
	if _, err := svc.Grade(context.Background(), schema.CaseInput{}, "", resp, g, nil); err != nil {
		t.Fatalf("Grade: %v", err)
	}
	// Assert: the grader call carries the raised budget, not the old 2000 cap.
	if len(fake.Calls) != 1 {
		t.Fatalf("calls = %d; want 1", len(fake.Calls))
	}
	if got := fake.Calls[0].MaxOutputTokens; got != 4000 {
		t.Fatalf("MaxOutputTokens = %d; want 4000", got)
	}
}

func TestParseAbsoluteGradeNotesAndFlagVariants(t *testing.T) {
	base := `"scores": {"grounding_and_calibration": 2, "context_and_values_fidelity": 2, "decision_insight": 2, "practical_robustness": 2, "role_execution": 2}, "supporting_passages": {"decision_insight": "p"}`
	cases := []struct {
		name      string
		raw       string
		wantNotes []string
		wantFlag  string
	}{
		{"string acceptance notes", `{` + base + `, "acceptance_notes": "good overall"}`,
			[]string{"good overall"}, ""},
		{"array acceptance notes", `{` + base + `, "acceptance_notes": ["note one", "note two"]}`,
			[]string{"note one", "note two"}, ""},
		{"string flag list", `{` + base + `, "notes_check": {"noticed": [], "missed": [], "beyond_notes": []}, "flags": ["hallucination", "unsupported_claim"]}`,
			nil, "hallucination"},
		{"fenced payload", "```json\n{" + base + `, "notes_check": {"noticed": ["n"], "missed": [], "beyond_notes": []}}` + "\n```",
			[]string{"n"}, ""},
		{"prose-wrapped payload", "Here is the grade: {" + base + `, "notes_check": {"noticed": [], "missed": [], "beyond_notes": []}}` + " hope this helps.",
			nil, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			grade, ok := parseAbsoluteGrade(tc.raw)
			if !ok {
				t.Fatalf("parseAbsoluteGrade rejected %s", tc.name)
			}
			if tc.wantNotes != nil {
				if len(grade.NotesCheck.Noticed) != len(tc.wantNotes) || grade.NotesCheck.Noticed[0] != tc.wantNotes[0] {
					t.Fatalf("notes = %+v; want %v", grade.NotesCheck, tc.wantNotes)
				}
			}
			if tc.wantFlag != "" {
				if len(grade.Flags) != 2 || grade.Flags[0].Type != tc.wantFlag {
					t.Fatalf("flags = %+v; want string list converted", grade.Flags)
				}
			}
		})
	}
}

func TestParseAbsoluteGradeWeaknessAndTruncation(t *testing.T) {
	// Arrange: weakness object plus a mid-JSON truncation.
	raw := `{"scores": {"grounding_and_calibration": 1, "context_and_values_fidelity": 1, "decision_insight": 1, "practical_robustness": 1, "role_execution": 1}, "supporting_passages": [], "notes_check": {"noticed": [], "missed": [], "beyond_notes": []}, "most_consequential_weakness": {"passage": "q", "explanation": "e"}}`
	// Act/Assert: weakness survives the round trip; truncation is rejected.
	grade, ok := parseAbsoluteGrade(raw)
	if !ok || grade.MostConsequentialWeakness == nil || grade.MostConsequentialWeakness.Passage != "q" {
		t.Fatalf("weakness = %+v, %v; want it preserved", grade.MostConsequentialWeakness, ok)
	}
	if _, ok := parseAbsoluteGrade(raw[:len(raw)-30]); ok {
		t.Fatal("parseAbsoluteGrade accepted a truncated payload")
	}
}

func TestParseAbsoluteGradeRejectsEmptyMapPassages(t *testing.T) {
	base := `"scores": {"grounding_and_calibration": 3, "context_and_values_fidelity": 3, "decision_insight": 3, "practical_robustness": 3, "role_execution": 3}`
	notes := `"notes_check": {"noticed": [], "missed": [], "beyond_notes": []}`
	cases := []struct {
		name string
		raw  string
	}{
		{"empty map value", `{` + base + `, "supporting_passages": {"decision_insight": ""}, ` + notes + `}`},
		{"whitespace map value", `{` + base + `, "supporting_passages": {"decision_insight": "  "}, ` + notes + `}`},
		{"empty map key", `{` + base + `, "supporting_passages": {"": "has assessment"}, ` + notes + `}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if grade, ok := parseAbsoluteGrade(tc.raw); ok {
				t.Fatalf("parseAbsoluteGrade accepted %s: %+v", tc.name, grade)
			}
		})
	}
}
