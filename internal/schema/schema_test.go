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
