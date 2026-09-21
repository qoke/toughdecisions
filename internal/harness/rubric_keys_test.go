package harness

import (
	"testing"

	"github.com/qoke/toughdecisions/internal/casepack"
	"github.com/qoke/toughdecisions/internal/grading"
)

// TestRubricKeysMatchCasepack pins Finding 1: the casepack human_scores key
// set and the grading rubric-criteria set must stay identical. The check
// lives here (not in either leaf package) because casepack is a
// data-loading package that must not import the model-calling grading
// package, and grading must not import casepack; harness already imports
// both, so this linking test adds no new dependency edge or import cycle.
func TestRubricKeysMatchCasepack(t *testing.T) {
	want := casepack.ScoreKeys()
	got := grading.RubricCriteria()
	if len(want) != len(got) {
		t.Fatalf("score keys = %v; rubric criteria = %v; want identical sets", want, got)
	}
	for i := range want {
		if want[i] != got[i] {
			t.Fatalf("score keys = %v; rubric criteria = %v; want identical sets in order", want, got)
		}
	}
}
