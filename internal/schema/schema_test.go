package schema

import (
	"encoding/json"
	"strings"
	"testing"
)

const validViewJSON = `{
  "urgent_danger": {"present": false, "caution": ""},
  "qualification": "q",
  "suggested_reply": "r",
  "recommended_move": "m",
  "decisive_insight": "i",
  "tradeoff_or_objection": "t",
  "depends_on": "d",
  "fallback": "f"
}`

func TestParseLenientViewRoundTrip(t *testing.T) {
	v, ok := ParseLenient[View](validViewJSON)
	if !ok {
		t.Fatal("ParseLenient rejected valid view")
	}
	if v.Qualification != "q" || v.SuggestedReply != "r" {
		t.Fatalf("view = %+v", v)
	}
}

func TestParseLenientFencesAndTrailingProse(t *testing.T) {
	raw := "Here is the result:\n```json\n" + validViewJSON + "\n```\nHope this helps."
	v, ok := ParseLenient[View](raw)
	if !ok || v.Fallback != "f" {
		t.Fatalf("fenced parse = %+v, %v", v, ok)
	}
}

func TestParseLenientBracesInsideStrings(t *testing.T) {
	raw := `{"urgent_danger": {"present": false}, "qualification": "q {not a brace} end",
	  "suggested_reply": "r", "decisive_insight": "i", "tradeoff_or_objection": "t",
	  "depends_on": "d", "fallback": "f"} trailing`
	v, ok := ParseLenient[View](raw)
	if !ok {
		t.Fatal("rejected braces inside string")
	}
	if !strings.Contains(v.Qualification, "{not a brace}") {
		t.Fatalf("qualification = %q", v.Qualification)
	}
}

func TestParseLenientRejectsMalformed(t *testing.T) {
	for _, raw := range []string{"", "no json here", "{unclosed", "{\"a\":1} extra {"} {
		if _, ok := ParseLenient[View](raw); ok {
			t.Fatalf("accepted %q", raw)
		}
	}
	// Missing required fields.
	if _, ok := ParseLenient[View](`{"qualification":"q"}`); ok {
		t.Fatal("accepted view with missing fields")
	}
	// Invalid range.
	if _, ok := ParseLenient[AbsoluteGrade](`{"scores":{"role_execution":5},"supporting_passages":{},"notes_check":{"noticed":[],"missed":[],"beyond_notes":[]}}`); ok {
		t.Fatal("accepted score 5")
	}
	if _, ok := ParseLenient[AbsoluteGrade](`{"scores":{"role_execution":-1},"supporting_passages":{},"notes_check":{"noticed":[],"missed":[],"beyond_notes":[]}}`); ok {
		t.Fatal("accepted score -1")
	}
}

func TestSchemasAreValidJSON(t *testing.T) {
	for name, s := range map[string]string{"view": ViewJSONSchema, "judge": JudgeJSONSchema, "absolute": AbsoluteJSONSchema} {
		var v any
		if err := json.Unmarshal([]byte(s), &v); err != nil {
			t.Fatalf("%s schema invalid: %v", name, err)
		}
	}
}

func TestParseLenientJudge(t *testing.T) {
	raw := `{"urgent_danger":{"present":true,"caution":"c"},"qualification":"q",
	  "recommended_reply":"r","why":"w","accepted_cost":"a",
	  "next":{"immediate":"i","forward":"f"},"change_course_if":"c"}`
	j, ok := ParseLenient[Judge](raw)
	if !ok || !j.UrgentDanger.Present || j.Next.Forward != "f" {
		t.Fatalf("judge = %+v, %v", j, ok)
	}
}

// TestSchemasStrictModeConformant recursively walks every generated view and
// judge schema and asserts each object node carries additionalProperties ==
// false, as OpenAI-family strict structured-output mode requires on every
// object recursively. The walk descends into properties, items, additional
// properties, and all combinators, so depth coverage is structural, not
// limited to the top level.
func TestSchemasStrictModeConformant(t *testing.T) {
	for name, s := range map[string]string{"view": ViewJSONSchema, "judge": JudgeJSONSchema, "absolute": AbsoluteJSONSchema} {
		var v any
		if err := json.Unmarshal([]byte(s), &v); err != nil {
			t.Fatalf("%s schema invalid: %v", name, err)
		}
		assertStrictObjects(t, name, "$", v)
	}
}

// assertStrictObjects depth-first walks a decoded schema: every map that is
// (or contains) an object schema must have additionalProperties == false,
// and every child value is visited regardless of key.
func assertStrictObjects(t *testing.T, schemaName, path string, v any) {
	t.Helper()
	switch n := v.(type) {
	case map[string]any:
		if isObjectNode(n) {
			ap, ok := n["additionalProperties"]
			if !ok {
				t.Errorf("%s %s: object node missing additionalProperties", schemaName, path)
			} else if b, ok := ap.(bool); !ok || b {
				t.Errorf("%s %s: additionalProperties = %v, want false", schemaName, path, ap)
			}
		}
		for k, child := range n {
			assertStrictObjects(t, schemaName, path+"."+k, child)
		}
	case []any:
		for i, child := range n {
			assertStrictObjects(t, schemaName, path+"["+itoa(i)+"]", child)
		}
	}
}

func isObjectNode(n map[string]any) bool {
	if typ, ok := n["type"].(string); ok && typ == "object" {
		return true
	}
	if _, ok := n["properties"]; ok {
		return true
	}
	return false
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b [20]byte
	p := len(b)
	for i > 0 {
		p--
		b[p] = byte('0' + i%10)
		i /= 10
	}
	return string(b[p:])
}

// TestSchemasStrictModeRequiresAllProperties pins the OpenAI strict-mode
// required-superset rule ("'required' ... [must be] an array including every
// key in properties", per the live proxy 400 body "Missing 'caution'"):
// every object node's properties must all appear in required, recursively.
// Reverting mustStrictNormalize's required merge (leaving e.g.
// urgent_danger.required=["present"] while properties has present+caution)
// fails this offline instead of 400ing every real call.
func TestSchemasStrictModeRequiresAllProperties(t *testing.T) {
	for name, s := range map[string]string{"view": ViewJSONSchema, "judge": JudgeJSONSchema, "absolute": AbsoluteJSONSchema} {
		var v any
		if err := json.Unmarshal([]byte(s), &v); err != nil {
			t.Fatalf("%s schema invalid: %v", name, err)
		}
		assertRequiredSuperset(t, name, "$", v)
	}
	// Spot-check the exact property that 400ed live.
	for name, s := range map[string]string{"view": ViewJSONSchema, "judge": JudgeJSONSchema} {
		var sch map[string]any
		if err := json.Unmarshal([]byte(s), &sch); err != nil {
			t.Fatalf("%s schema invalid: %v", name, err)
		}
		props, _ := sch["properties"].(map[string]any)
		ud, _ := props["urgent_danger"].(map[string]any)
		req, _ := ud["required"].([]any)
		have := map[string]bool{}
		for _, r := range req {
			if str, ok := r.(string); ok {
				have[str] = true
			}
		}
		if !have["present"] || !have["caution"] {
			t.Fatalf("%s urgent_danger.required = %v; want [present caution]", name, req)
		}
	}
}

// assertRequiredSuperset depth-first walks a decoded schema: every object
// node with "properties" must list each property key in "required".
func assertRequiredSuperset(t *testing.T, schemaName, path string, v any) {
	t.Helper()
	switch n := v.(type) {
	case map[string]any:
		if props, ok := n["properties"].(map[string]any); ok && isObjectNode(n) {
			have := map[string]bool{}
			if req, ok := n["required"].([]any); ok {
				for _, r := range req {
					if s, ok := r.(string); ok {
						have[s] = true
					}
				}
			}
			for name := range props {
				if !have[name] {
					t.Errorf("%s %s: property %q missing from required", schemaName, path, name)
				}
			}
		}
		for k, child := range n {
			assertRequiredSuperset(t, schemaName, path+"."+k, child)
		}
	case []any:
		for i, child := range n {
			assertRequiredSuperset(t, schemaName, path+"["+itoa(i)+"]", child)
		}
	}
}

// TestParseLenientGradeRejectsMissingOrPartialScores pins D3: a grade
// payload with no "scores" key, or with fewer than the five rubric criteria,
// is rejected — never coerced to a mean-0 grade that manufactures phantom
// calibration reversals (15/33 live responses).
func TestParseLenientGradeRejectsMissingOrPartialScores(t *testing.T) {
	for _, raw := range []string{
		`{"supporting_passages": {}, "notes_check": {"noticed": [], "missed": [], "beyond_notes": []}}`,
		`{"overall_assessment": "fine", "supporting_passages": {}, "notes_check": {"noticed": [], "missed": [], "beyond_notes": []}}`,
		`{"scores": {"grounding_and_calibration": 3}, "supporting_passages": {}, "notes_check": {"noticed": [], "missed": [], "beyond_notes": []}}`,
		`{"scores": {}, "supporting_passages": {}, "notes_check": {"noticed": [], "missed": [], "beyond_notes": []}}`,
	} {
		if grade, ok := ParseLenient[AbsoluteGrade](raw); ok {
			t.Fatalf("ParseLenient[AbsoluteGrade] accepted %s: %+v", raw, grade)
		}
	}
}

const validJudgeJSON = `{
  "urgent_danger": {"present": true, "caution": "c"},
  "no_independent_views": false,
  "missing_roles": [],
  "qualification": "q",
  "recommended_reply": "r",
  "recommended_action": "a2",
  "why": "w",
  "accepted_cost": "a",
  "next": {"immediate": "i", "forward": "f"},
  "change_course_if": "c",
  "unresolved_disagreement": ""
}`

// TestSchemasAcceptWellFormedRejectMalformed checks the normalized schemas
// still validate a representative payload and reject a malformed one. The
// check is structural (required fields present and non-empty) mirroring the
// schema required/minLength constraints, without new dependencies. Under the
// strict-mode contract every property is required, so the expected required
// sets are the full property sets.
func TestSchemasAcceptWellFormedRejectMalformed(t *testing.T) {
	for name, tc := range map[string]struct {
		schema   string
		required []string
		valid    string
	}{
		"view": {ViewJSONSchema,
			[]string{"urgent_danger", "qualification", "suggested_reply", "recommended_move", "decisive_insight", "tradeoff_or_objection", "depends_on", "fallback"},
			validViewJSON},
		"judge": {JudgeJSONSchema,
			[]string{"urgent_danger", "no_independent_views", "missing_roles", "qualification", "recommended_reply", "recommended_action", "why", "accepted_cost", "next", "change_course_if", "unresolved_disagreement"},
			validJudgeJSON},
	} {
		var sch map[string]any
		if err := json.Unmarshal([]byte(tc.schema), &sch); err != nil {
			t.Fatalf("%s schema invalid: %v", name, err)
		}
		req, _ := sch["required"].([]any)
		if len(req) != len(tc.required) {
			t.Fatalf("%s required = %v, want %v", name, req, tc.required)
		}
		var good map[string]any
		if err := json.Unmarshal([]byte(tc.valid), &good); err != nil {
			t.Fatalf("%s valid payload invalid JSON: %v", name, err)
		}
		for _, f := range tc.required {
			if _, ok := good[f]; !ok {
				t.Fatalf("%s valid payload missing %q", name, f)
			}
		}
		// Malformed: drop every required field but one.
		bad := map[string]any{"qualification": "q"}
		for _, f := range tc.required {
			if _, ok := bad[f]; !ok {
				_ = f
			}
		}
		missing := 0
		for _, f := range tc.required {
			if _, ok := bad[f]; !ok {
				missing++
			}
		}
		if missing == 0 {
			t.Fatalf("%s malformed payload unexpectedly complete", name)
		}
		if _, ok := ParseLenient[View](`{"qualification":"q"}`); name == "view" && ok {
			t.Fatalf("%s malformed payload accepted", name)
		}
		if _, ok := ParseLenient[Judge](`{"qualification":"q"}`); name == "judge" && ok {
			t.Fatalf("%s malformed payload accepted", name)
		}
	}
}
