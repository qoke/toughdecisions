// Package schema holds the production output structs, their JSON Schemas,
// the production/assembler input types, and a lenient JSON parser.
package schema

import "encoding/json"

// Danger is a credible-urgent-danger flag present in views and judges.
type Danger struct {
	Present bool   `json:"present"`
	Caution string `json:"caution,omitempty"`
}

// View is one independent seat view.
type View struct {
	UrgentDanger        Danger `json:"urgent_danger"`
	Qualification       string `json:"qualification"`
	SuggestedReply      string `json:"suggested_reply"`
	RecommendedMove     string `json:"recommended_move,omitempty"`
	DecisiveInsight     string `json:"decisive_insight"`
	TradeoffOrObjection string `json:"tradeoff_or_objection"`
	DependsOn           string `json:"depends_on"`
	Fallback            string `json:"fallback"`
}

// Judge is the synthesis output.
type Judge struct {
	UrgentDanger       Danger   `json:"urgent_danger"`
	NoIndependentViews bool     `json:"no_independent_views"`
	MissingRoles       []string `json:"missing_roles,omitempty"`
	Qualification      string   `json:"qualification"`
	RecommendedReply   string   `json:"recommended_reply"`
	RecommendedAction  string   `json:"recommended_action,omitempty"`
	Why                string   `json:"why"`
	AcceptedCost       string   `json:"accepted_cost"`
	Next               struct {
		Immediate string `json:"immediate"`
		Forward   string `json:"forward"`
	} `json:"next"`
	ChangeCourseIf         string `json:"change_course_if"`
	UnresolvedDisagreement string `json:"unresolved_disagreement,omitempty"`
}

// AbsoluteGrade is a 0–4 rubric grade for one response.
type AbsoluteGrade struct {
	Scores                    map[string]int    `json:"scores"`
	SupportingPassages        map[string]string `json:"supporting_passages"`
	MostConsequentialWeakness *struct {
		Passage     string `json:"passage"`
		Explanation string `json:"explanation"`
	} `json:"most_consequential_weakness,omitempty"`
	Flags []struct {
		Type     string `json:"type"`
		Passage  string `json:"passage"`
		Violated string `json:"violated"`
	} `json:"flags,omitempty"`
	NotesCheck struct {
		Noticed     []string `json:"noticed"`
		Missed      []string `json:"missed"`
		BeyondNotes []string `json:"beyond_notes"`
	} `json:"notes_check"`
}

// Pairwise is an A/B comparison verdict.
type Pairwise struct {
	Verdict                 string `json:"verdict"`
	Margin                  string `json:"margin"`
	ConsequentialDifference string `json:"consequential_difference"`
	Notes                   string `json:"notes,omitempty"`
}

// Coverage is an issue-coverage table.
type Coverage struct {
	Rows []struct {
		IssueID       string   `json:"issue_id"`
		IssueText     string   `json:"issue_text"`
		IsPlanted     bool     `json:"is_planted"`
		NoticedBy     []string `json:"noticed_by"`
		UnsupportedBy []string `json:"unsupported_by"`
		JudgeOutcome  string   `json:"judge_outcome"`
	} `json:"rows"`
}

// Card is the 7-field case card. It mirrors the card_json layout in the store.
type Card struct {
	Decision   string `json:"decision"`
	Context    string `json:"context"`
	Priorities string `json:"priorities"`
	Unusual    string `json:"unusual"`
	History    string `json:"history"`
	Deadline   string `json:"deadline"`
	Style      string `json:"style"`
}

// Message is one case message (verbatim sender + text + timestamp).
type Message struct {
	Sender string `json:"sender"`
	Text   string `json:"text"`
	TS     string `json:"ts,omitempty"`
}

// CaseInput is the immutable input snapshot for one request.
//
// Role is a plain string (not pack.Seat) to avoid an import cycle;
// pack.Seat is string-based too, so conversion is trivial.
type CaseInput struct {
	Card     Card      `json:"card"`
	Messages []Message `json:"messages"`
	Question string    `json:"question"`
	Style    string    `json:"style,omitempty"`
}

// RenderedView is one rendered independent view for the judge prompt.
type RenderedView struct {
	Role                string
	Qualification       string
	SuggestedReply      string
	DecisiveInsight     string
	TradeoffOrObjection string
	DependsOn           string
	Fallback            string
	UrgentDanger        Danger
}

// viewJSONSchemaTmpl is the raw template for the View output JSON Schema.
// ViewJSONSchema is its strict-mode-normalized form; use that at call sites.
const viewJSONSchemaTmpl = `{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "title": "view",
  "type": "object",
  "required": ["urgent_danger", "qualification", "suggested_reply", "decisive_insight", "tradeoff_or_objection", "depends_on", "fallback"],
  "properties": {
    "urgent_danger": {"type": "object",
      "required": ["present"],
      "properties": {"present": {"type": "boolean"}, "caution": {"type": "string"}}},
    "qualification": {"type": "string", "minLength": 1},
    "suggested_reply": {"type": "string", "minLength": 1},
    "recommended_move": {"type": "string"},
    "decisive_insight": {"type": "string", "minLength": 1},
    "tradeoff_or_objection": {"type": "string", "minLength": 1},
    "depends_on": {"type": "string", "minLength": 1},
    "fallback": {"type": "string", "minLength": 1}
  },
  "additionalProperties": false
}`

// judgeJSONSchemaTmpl is the raw template for the Judge output JSON Schema.
// JudgeJSONSchema is its strict-mode-normalized form; use that at call sites.
const judgeJSONSchemaTmpl = `{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "title": "judge",
  "type": "object",
  "required": ["urgent_danger", "qualification", "recommended_reply", "why", "accepted_cost", "next", "change_course_if"],
  "properties": {
    "urgent_danger": {"type": "object",
      "required": ["present"],
      "properties": {"present": {"type": "boolean"}, "caution": {"type": "string"}}},
    "no_independent_views": {"type": "boolean"},
    "missing_roles": {"type": "array", "items": {"type": "string"}},
    "qualification": {"type": "string", "minLength": 1},
    "recommended_reply": {"type": "string", "minLength": 1},
    "recommended_action": {"type": "string"},
    "why": {"type": "string", "minLength": 1},
    "accepted_cost": {"type": "string", "minLength": 1},
    "next": {"type": "object",
      "required": ["immediate", "forward"],
      "properties": {"immediate": {"type": "string"}, "forward": {"type": "string"}}},
    "change_course_if": {"type": "string", "minLength": 1},
    "unresolved_disagreement": {"type": "string"}
  },
  "additionalProperties": false
}`

// absoluteJSONSchemaTmpl is the raw template for the absolute-grader output
// JSON Schema. AbsoluteJSONSchema is its strict-mode-normalized form; the
// grading service sends it as the response_format on grader calls whose
// model supports json_schema, mirroring the view/judge mechanism.
const absoluteJSONSchemaTmpl = `{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "title": "absolute",
  "type": "object",
  "required": ["scores", "supporting_passages", "notes_check"],
  "properties": {
    "scores": {"type": "object",
      "required": ["grounding_and_calibration", "context_and_values_fidelity", "decision_insight", "practical_robustness", "role_execution"],
      "properties": {
        "grounding_and_calibration": {"type": "integer"},
        "context_and_values_fidelity": {"type": "integer"},
        "decision_insight": {"type": "integer"},
        "practical_robustness": {"type": "integer"},
        "role_execution": {"type": "integer"}
      }},
    "supporting_passages": {"type": "array", "items": {"type": "object",
      "required": ["passage", "assessment"],
      "properties": {"passage": {"type": "string"}, "assessment": {"type": "string"}}}},
    "notes_check": {"type": "object",
      "required": ["noticed", "missed", "beyond_notes"],
      "properties": {
        "noticed": {"type": "array", "items": {"type": "string"}},
        "missed": {"type": "array", "items": {"type": "string"}},
        "beyond_notes": {"type": "array", "items": {"type": "string"}}}},
    "flags": {"type": "array", "items": {"type": "object",
      "required": ["type"],
      "properties": {"type": {"type": "string"}, "passage": {"type": "string"}, "violated": {"type": "string"}}}}
  },
  "additionalProperties": false
}`

// ViewJSONSchema is the strict-mode-normalized View output JSON Schema:
// every object at every nesting depth carries "additionalProperties": false
// and lists every property in "required", as required by OpenAI-family
// structured outputs when strict is true ("'required' ... [must be] an array
// including every key in properties" per the live proxy 400 body).
// Normalization runs centrally here so no call site can regress.
// Previously-optional fields (caution, recommended_move, ...) keep their
// plain "type": "string" (no minLength): requiring them does not force null,
// models emit "" when absent, which parses to the Go zero value. No
// ["string","null"] union is needed.
var ViewJSONSchema = mustStrictNormalize(viewJSONSchemaTmpl)

// JudgeJSONSchema is the strict-mode-normalized Judge output JSON Schema.
// See ViewJSONSchema for the invariant.
var JudgeJSONSchema = mustStrictNormalize(judgeJSONSchemaTmpl)

// AbsoluteJSONSchema is the strict-mode-normalized absolute-grader output
// JSON Schema. The grading service sends it as the response_format on
// grader calls whose model supports json_schema, mirroring the view/judge
// mechanism. Scores stay flat 0-4 integers (no wrapped {"score": n} form),
// supporting_passages is always an array of {passage, assessment} objects,
// notes_check is always the three-list object: the schema-conformant shape.
// Previously-optional fields (flags members, notes lists) are plain types
// with no minLength: models emit "" / [] when absent.
var AbsoluteJSONSchema = mustStrictNormalize(absoluteJSONSchemaTmpl)

// mustStrictNormalize parses a raw schema template and enforces the
// OpenAI strict-mode contract on every object node recursively (root,
// nested properties, array items, and combinators): it sets
// "additionalProperties": false and merges every key of "properties" into
// "required" (creating the array when absent, never duplicating). It panics
// on invalid input because the templates are compile-time constants.
func mustStrictNormalize(tmpl string) string {
	var v any
	if err := json.Unmarshal([]byte(tmpl), &v); err != nil {
		panic("schema: invalid template: " + err.Error())
	}
	strictWalk(v)
	out, err := json.Marshal(v)
	if err != nil {
		panic("schema: marshal normalized: " + err.Error())
	}
	return string(out)
}

// strictWalk enforces the strict-mode contract on every map that declares
// type object (explicitly or implicitly via properties/required), then
// recurses into every child value so no nesting depth can regress.
func strictWalk(v any) {
	switch n := v.(type) {
	case map[string]any:
		if typ, ok := n["type"].(string); ok && typ == "object" {
			n["additionalProperties"] = false
			requireAllProperties(n)
		} else if props, ok := n["properties"]; ok {
			n["additionalProperties"] = false
			if _, ok := props.(map[string]any); ok {
				requireAllProperties(n)
			}
		} else if _, ok := n["required"]; ok {
			n["additionalProperties"] = false
		}
		for _, child := range n {
			strictWalk(child)
		}
	case []any:
		for _, child := range n {
			strictWalk(child)
		}
	}
}

// requireAllProperties merges every key of "properties" into "required"
// (creating the array when absent, preserving existing entries, no
// duplicates), satisfying the strict-mode required-superset rule.
func requireAllProperties(n map[string]any) {
	props, ok := n["properties"].(map[string]any)
	if !ok {
		return
	}
	have := map[string]bool{}
	var req []any
	if existing, ok := n["required"].([]any); ok {
		for _, r := range existing {
			if s, ok := r.(string); ok {
				if !have[s] {
					have[s] = true
					req = append(req, s)
				}
			}
		}
	}
	for name := range props {
		if !have[name] {
			have[name] = true
			req = append(req, name)
		}
	}
	n["required"] = req
}
