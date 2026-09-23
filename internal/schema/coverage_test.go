package schema

import (
	"encoding/json"
	"testing"
)

// fullScores returns a scores map containing every rubric criterion with an
// in-range value.
func fullScores(value int) map[string]int {
	out := make(map[string]int, len(rubricCriterionKeys))
	for _, key := range rubricCriterionKeys {
		out[key] = value
	}
	return out
}

// TestValidAbsoluteScores covers validAbsoluteScores: exactly the five rubric
// criteria, each 0-4, and nothing else. Wrong size, unknown keys and
// out-of-range values are all rejected so absent scores cannot be coerced
// into zeros.
func TestValidAbsoluteScores(t *testing.T) {
	replaced := fullScores(4)
	delete(replaced, rubricCriterionKeys[len(rubricCriterionKeys)-1])
	replaced["not_a_criterion"] = 4

	partial := fullScores(3)
	delete(partial, rubricCriterionKeys[0])

	tooHigh := fullScores(2)
	tooHigh[rubricCriterionKeys[0]] = 5

	negative := fullScores(2)
	negative[rubricCriterionKeys[0]] = -1

	cases := []struct {
		name   string
		scores map[string]int
		want   bool
	}{
		{name: "all five criteria at zero", scores: fullScores(0), want: true},
		{name: "all five criteria at four", scores: fullScores(4), want: true},
		{name: "scores map is nil", scores: nil, want: false},
		{name: "scores map is empty", scores: map[string]int{}, want: false},
		{name: "one criterion missing", scores: partial, want: false},
		{name: "unknown key replaces a criterion", scores: replaced, want: false},
		{name: "score above four", scores: tooHigh, want: false},
		{name: "score below zero", scores: negative, want: false},
	}
	for _, tc := range cases {
		t.Run("should return "+boolString(tc.want)+" when scores have "+tc.name, func(t *testing.T) {
			// Act
			got := validAbsoluteScores(tc.scores)

			// Assert
			if got != tc.want {
				t.Fatalf("validAbsoluteScores(%v) = %v, want %v", tc.scores, got, tc.want)
			}
		})
	}
}

// TestParseLenientAbsoluteGradeRejectsBadScores drives the same rules through
// the public parser: a structurally valid grade with a bad scores object must
// not parse.
func TestParseLenientAbsoluteGradeRejectsBadScores(t *testing.T) {
	cases := []struct {
		name  string
		build func() map[string]int
		want  bool
	}{
		{
			name: "complete rubric",
			build: func() map[string]int {
				return map[string]int{
					"grounding_and_calibration": 4, "context_and_values_fidelity": 3,
					"decision_insight": 2, "practical_robustness": 1, "role_execution": 0,
				}
			},
			want: true,
		},
		{
			name:  "scores omitted entirely",
			build: func() map[string]int { return nil },
			want:  false,
		},
		{
			name: "one rubric criterion out of range",
			build: func() map[string]int {
				return map[string]int{
					"grounding_and_calibration": 9, "context_and_values_fidelity": 3,
					"decision_insight": 2, "practical_robustness": 1, "role_execution": 0,
				}
			},
			want: false,
		},
	}
	for _, tc := range cases {
		t.Run("should parse = "+boolString(tc.want)+" when scores have "+tc.name, func(t *testing.T) {
			// Arrange
			scores, err := json.Marshal(tc.build())
			if err != nil {
				t.Fatalf("marshal scores: %v", err)
			}
			raw := `{"scores":` + string(scores) + `,"supporting_passages":{}}`

			// Act
			_, ok := ParseLenient[AbsoluteGrade](raw)

			// Assert
			if ok != tc.want {
				t.Fatalf("ParseLenient(%s) = %v, want %v", raw, ok, tc.want)
			}
		})
	}
}

func boolString(b bool) string {
	if b {
		return "true"
	}
	return "false"
}
