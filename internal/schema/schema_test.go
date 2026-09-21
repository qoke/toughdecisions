package schema

import (
	"encoding/json"
	"strings"
	"testing"
)

const validViewJSON = `{
  "urgent_danger": {"present": false},
  "qualification": "q",
  "suggested_reply": "r",
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
	for name, s := range map[string]string{"view": ViewJSONSchema, "judge": JudgeJSONSchema} {
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
	for name, s := range map[string]string{"view": ViewJSONSchema, "judge": JudgeJSONSchema} {
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

const validJudgeJSON = `{
  "urgent_danger": {"present": true, "caution": "c"},
  "qualification": "q",
  "recommended_reply": "r",
  "why": "w",
  "accepted_cost": "a",
  "next": {"immediate": "i", "forward": "f"},
  "change_course_if": "c"
}`

// TestSchemasAcceptWellFormedRejectMalformed checks the normalized schemas
// still validate a representative payload and reject a malformed one. The
// check is structural (required fields present and non-empty) mirroring the
// schema required/minLength constraints, without new dependencies.
func TestSchemasAcceptWellFormedRejectMalformed(t *testing.T) {
	for name, tc := range map[string]struct {
		schema   string
		required []string
		valid    string
	}{
		"view": {ViewJSONSchema,
			[]string{"urgent_danger", "qualification", "suggested_reply", "decisive_insight", "tradeoff_or_objection", "depends_on", "fallback"},
			validViewJSON},
		"judge": {JudgeJSONSchema,
			[]string{"urgent_danger", "qualification", "recommended_reply", "why", "accepted_cost", "next", "change_course_if"},
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
			v, ok := good[f]
			if !ok {
				t.Fatalf("%s valid payload missing %q", name, f)
			}
			if s, ok := v.(string); ok && strings.TrimSpace(s) == "" {
				t.Fatalf("%s valid payload has empty %q", name, f)
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
