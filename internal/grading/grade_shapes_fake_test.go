package grading

import (
	"context"
	"strings"
	"testing"

	"github.com/qoke/toughdecisions/internal/gateway"
	"github.com/qoke/toughdecisions/internal/schema"
)

// gradeArrayJSON is a grader payload in the form real models emit:
// supporting_passages as an array of {passage, assessment} objects (see
// evidenceArrayPayload in absolute_shapes_test.go). The fake returns it
// verbatim, so this test exercises the parser against reality.
func gradeArrayJSON() string {
	return `{"scores": {"grounding_and_calibration": 3, "context_and_values_fidelity": 3, "decision_insight": 3, "practical_robustness": 3, "role_execution": 3},` +
		` "supporting_passages": [{"passage": "the school waived fees", "assessment": "unsupported by the record"}, {"passage": "pickup timing no longer matters", "assessment": "overreaches from the evidence"}],` +
		` "notes_check": {"noticed": [], "missed": [], "beyond_notes": []}}`
}

// TestGradeAcceptsArrayAndMapPassagesViaFake pins (c): the fake serves both
// the realistic array form and the legacy map form of supporting_passages,
// and Grade persists both. Reverting the array-tolerant parser (decodePassages
// map-only) makes the array leg fail — verified by mutation check.
func TestGradeAcceptsArrayAndMapPassagesViaFake(t *testing.T) {
	for _, tc := range []struct {
		name    string
		content string
		want    string
	}{
		{"array passages (real models)", gradeArrayJSON(), "the school waived fees"},
		{"legacy map passages", gradeJSON(map[string]int{
			"grounding_and_calibration": 3, "context_and_values_fidelity": 3,
			"decision_insight": 3, "practical_robustness": 3, "role_execution": 3,
		}, ""), "decision_insight"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := openTestDB(t)
			cfg := testConfig(t)
			g := insertGrader(t, db, mkGrader("gshape", "openai", "selection", true))
			resp := mkResponse(t, db, "possibility", "some thoughtful advice text here")
			fake := gateway.NewFake(map[string][]gateway.Step{
				g.Model: {{Content: tc.content}},
			})
			svc := NewService(db, fake, cfg)

			res, err := svc.Grade(context.Background(), schema.CaseInput{}, "", resp, g, nil)
			if err != nil || res == nil {
				t.Fatalf("Grade = %+v, %v; want success", res, err)
			}
			if !strings.Contains(res.Grade.PassagesJSON, tc.want) {
				t.Fatalf("PassagesJSON = %s; want it to contain %q", res.Grade.PassagesJSON, tc.want)
			}
		})
	}
}
