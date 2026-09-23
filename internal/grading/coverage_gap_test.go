package grading

import (
	"encoding/json"
	"maps"
	"testing"
)

// TestNormalizeCriterionKeyVariants covers the fuzzy-match switch in
// normalizeCriterionKey: real models emit criterion keys with different
// spacing, casing, and unambiguous truncations, and a truly unknown key must
// be rejected rather than coerced.
func TestNormalizeCriterionKeyVariants(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
		ok   bool
	}{
		{name: "should keep the key when it already matches a canonical criterion", in: "grounding_and_calibration", want: "grounding_and_calibration", ok: true},
		{name: "should fold case and surrounding spaces when the key matches a criterion", in: "  Decision Insight ", want: "decision_insight", ok: true},
		{name: "should map to grounding when the key contains grounding", in: "Grounding Calibration", want: "grounding_and_calibration", ok: true},
		{name: "should map to context when the key contains fidelity", in: "Values Fidelity", want: "context_and_values_fidelity", ok: true},
		{name: "should map to insight when the key contains decision", in: "Decision Quality", want: "decision_insight", ok: true},
		{name: "should map to robustness when the key contains robustness", in: "Robustness Score", want: "practical_robustness", ok: true},
		{name: "should map to execution when the key contains execution", in: "Execution Style", want: "role_execution", ok: true},
		{name: "should reject the key when no distinctive word matches", in: "overall_quality", want: "", ok: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Act.
			got, ok := normalizeCriterionKey(tc.in)
			// Assert: both the mapped key and the acceptance flag are checked,
			// so a coercion-to-wrong-criterion regression fails.
			if ok != tc.ok || got != tc.want {
				t.Fatalf("normalizeCriterionKey(%q) = %q, %v; want %q, %v", tc.in, got, ok, tc.want, tc.ok)
			}
		})
	}
}

// TestDecodeScoresShapeVariants covers the flat, wrapped, and listed score
// container shapes decodeScores accepts, plus every rejection branch: empty
// payload, missing score, unknown criterion, out-of-range value, and a
// criterion count below five.
func TestDecodeScoresShapeVariants(t *testing.T) {
	fullScores := map[string]int{
		"grounding_and_calibration":   3,
		"context_and_values_fidelity": 2,
		"decision_insight":            1,
		"practical_robustness":        4,
		"role_execution":              0,
	}
	cases := []struct {
		name   string
		raw    string
		wantOK bool
		want   map[string]int
	}{
		{
			name:   "should reject when the scores payload is empty",
			raw:    ``,
			wantOK: false,
		},
		{
			name:   "should accept flat human-readable keys when they map unambiguously",
			raw:    `{"Grounding Calibration":3,"Values Fidelity":2,"Decision Quality":1,"Robustness Score":4,"Execution Style":0}`,
			wantOK: true,
			want:   fullScores,
		},
		{
			name:   "should reject a flat key when no criterion matches it",
			raw:    `{"overall_quality":3}`,
			wantOK: false,
		},
		{
			name: "should reject a wrapped entry when its score is missing",
			raw: `{"grounding_and_calibration":{"reason":"no score"},` +
				`"context_and_values_fidelity":{"score":2},"decision_insight":{"score":1},` +
				`"practical_robustness":{"score":4},"role_execution":{"score":0}}`,
			wantOK: false,
		},
		{
			name: "should reject a wrapped entry when its criterion key is unknown",
			raw: `{"overall_quality":{"score":3},` +
				`"context_and_values_fidelity":{"score":2},"decision_insight":{"score":1},` +
				`"practical_robustness":{"score":4},"role_execution":{"score":0}}`,
			wantOK: false,
		},
		{
			name: "should reject wrapped scores when a value is out of range",
			raw: `{"grounding_and_calibration":{"score":9},` +
				`"context_and_values_fidelity":{"score":2},"decision_insight":{"score":1},` +
				`"practical_robustness":{"score":4},"role_execution":{"score":0}}`,
			wantOK: false,
		},
		{
			name: "should accept listed scores when names and scores are valid",
			raw: `[{"name":"grounding_and_calibration","score":3},` +
				`{"name":"context_and_values_fidelity","score":2},` +
				`{"name":"decision_insight","score":1},` +
				`{"name":"practical_robustness","score":4},` +
				`{"name":"role_execution","score":0}]`,
			wantOK: true,
			want:   fullScores,
		},
		{
			name:   "should reject a listed entry when its score is missing",
			raw:    `[{"name":"grounding_and_calibration"}]`,
			wantOK: false,
		},
		{
			name: "should reject a listed entry when its criterion name is unknown",
			raw: `[{"name":"grounding_and_calibration","score":3},` +
				`{"name":"context_and_values_fidelity","score":2},` +
				`{"name":"decision_insight","score":1},` +
				`{"name":"practical_robustness","score":4},` +
				`{"name":"overall_quality","score":0}]`,
			wantOK: false,
		},
		{
			name: "should reject listed scores when fewer than five criteria are present",
			raw: `[{"name":"grounding_and_calibration","score":3},` +
				`{"name":"context_and_values_fidelity","score":2},` +
				`{"name":"decision_insight","score":1},` +
				`{"name":"practical_robustness","score":4}]`,
			wantOK: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Act.
			got, ok := decodeScores(json.RawMessage(tc.raw))
			// Assert: acceptance flag plus the exact criterion->score mapping,
			// so a shape that parses into wrong keys still fails.
			if ok != tc.wantOK {
				t.Fatalf("decodeScores(%s) ok = %v, want %v (got %v)", tc.raw, ok, tc.wantOK, got)
			}
			if tc.wantOK {
				if !maps.Equal(tc.want, got) {
					t.Fatalf("decodeScores(%s) = %v, want %v", tc.raw, got, tc.want)
				}
				return
			}
			if len(got) != 0 {
				t.Fatalf("decodeScores(%s) = %v on rejection, want nil", tc.raw, got)
			}
		})
	}
}
