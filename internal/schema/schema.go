// Package schema holds the production output structs, their JSON Schemas,
// the production/assembler input types, and a lenient JSON parser.
package schema

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

// ViewJSONSchema is the JSON Schema for View outputs.
const ViewJSONSchema = `{
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

// JudgeJSONSchema is the JSON Schema for Judge outputs.
const JudgeJSONSchema = `{
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
