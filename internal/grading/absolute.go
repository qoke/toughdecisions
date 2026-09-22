package grading

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/qoke/toughdecisions/internal/gateway"
	"github.com/qoke/toughdecisions/internal/prompts"
	"github.com/qoke/toughdecisions/internal/render"
	"github.com/qoke/toughdecisions/internal/schema"
	"github.com/qoke/toughdecisions/internal/store"
)

// GraderDefaultMaxOutputTokens is the floor for grader call budgets: the
// per-grader max_output_tokens seat setting (graders.yaml) is used when it
// exceeds this floor (task D6: a reasoning model length-truncated a full
// absolute grade at the old 4000-token cap — completion_tokens=4000,
// finish_reason=length on both attempts). The largest complete grade needs
// ~1250 tokens of JSON for five scored criteria with supporting passages,
// a weakness, and flags; the remaining budget is headroom so the grader
// can emit complete JSON, never a truncated prefix.
const GraderDefaultMaxOutputTokens = 8000

// rubricCriteria are the five absolute-grading criteria required in every
// scores map.
var rubricCriteria = []string{
	"grounding_and_calibration",
	"context_and_values_fidelity",
	"decision_insight",
	"practical_robustness",
	"role_execution",
}

// RubricCriteria returns the five canonical absolute-grading criterion
// keys. It is the exported view of the enforced scores key set.
func RubricCriteria() []string {
	out := make([]string, len(rubricCriteria))
	copy(out, rubricCriteria)
	return out
}

// graderInputShapes lists the input shapes real grader models emit (plus the
// originally-assumed one, kept first for compatibility). Models routinely wrap
// each 0-4 score one level deeper, as {"criterion": {"score": n, ...}}, instead
// of the assumed flat {"criterion": n}; pre-schema grader calls also
// improvised container names (see the evidence2 provider log). Every entry
// here is attested by a real payload: root, assessment, evaluation,
// rubric_scores, rubric_evaluation, and why_scores containers all appear in
// live responses.
var graderInputShapes = []struct{ scores, passages, notes string }{
	{"scores", "supporting_passages", "notes_check"},
	{"scores", "supporting_passages", "acceptance_notes"},
	{"criteria", "supporting_passages", "notes_check"},
	{"criteria", "supporting_passages", "acceptance_notes"},
	{"assessment", "supporting_passages", "acceptance_notes"},
	{"scores", "supporting_passages", "notes"},
	{"rubric_scores", "supporting_passages", "acceptance_notes"},
	{"rubric_scores", "supporting_passages", "notes_check"},
	{"rubric_evaluation", "supporting_passages", "acceptance_notes"},
	{"evaluation", "supporting_passages", "acceptance_notes"},
	{"why_scores", "supporting_passages", "acceptance_notes"},
	{"", "supporting_passages", "acceptance_notes"},
	{"", "supporting_passages", "notes_check"},
}

// parseAbsoluteGrade parses a raw grader response into the strict
// schema.AbsoluteGrade, tolerating the container-shape variation real models
// emit: the five criterion scores may arrive flat or wrapped one level deeper,
// the supporting passages may arrive as an array (as models actually emit) or
// as the assumed map, and the notes-check block may arrive under its own key as
// a string or a list.
//
// Only the containers noted above are shape-tolerant. Every required field must
// still be present with the right type — a scores map with the five criteria,
// a notes-check object carrying all three lists, and, whenever a passage is
// present, a non-empty passage string paired with a non-empty assessment. A
// payload missing those, or carrying an unexpected shape, returns ("", false)
// and is never silently accepted.
func parseAbsoluteGrade(raw string) (schema.AbsoluteGrade, bool) {
	var zero schema.AbsoluteGrade
	obj, ok := firstBalancedObject(stripCodeFences(raw))
	if !ok {
		return zero, false
	}
	var root map[string]json.RawMessage
	if err := json.Unmarshal([]byte(obj), &root); err != nil {
		return zero, false
	}
	// Prefer shapes whose notes key is actually present: otherwise a payload
	// carrying acceptance_notes would match the notes_check shape first and
	// silently drop its notes.
	shapes := graderInputShapes
	var present []struct{ scores, passages, notes string }
	for _, shape := range graderInputShapes {
		if _, ok := root[shape.notes]; ok {
			present = append(present, shape)
		}
	}
	if len(present) > 0 {
		shapes = present
	}
	for _, shape := range shapes {
		if grade, ok := decodeAbsoluteGrade(root, shape.scores, shape.passages, shape.notes); ok {
			return grade, true
		}
	}
	return zero, false
}

// decodeAbsoluteGrade attempts one container-shape mapping of the grader
// payload and reports whether the result is a valid absolute grade.
// An empty scoresKey means the five criteria live at the payload root.
func decodeAbsoluteGrade(root map[string]json.RawMessage, scoresKey, passagesKey, notesKey string) (schema.AbsoluteGrade, bool) {
	var zero schema.AbsoluteGrade
	var scores map[string]int
	if scoresKey == "" {
		var ok bool
		scores, ok = decodeScoresAtRoot(root)
		if !ok {
			return zero, false
		}
	} else {
		raw, ok := root[scoresKey]
		if !ok {
			return zero, false
		}
		var ok2 bool
		scores, ok2 = decodeScores(raw)
		if !ok2 {
			return zero, false
		}
	}
	passages, ok := decodePassages(root[passagesKey])
	if !ok {
		return zero, false
	}
	if scoresKey == "" {
		harvestRootPassages(root, passages)
	}
	notes, ok := decodeNotes(root[notesKey])
	if !ok {
		return zero, false
	}
	grade := schema.AbsoluteGrade{Scores: scores, SupportingPassages: passages, NotesCheck: notes}
	if rawMCW, ok := root["most_consequential_weakness"]; ok {
		var mcw struct {
			Passage     string `json:"passage"`
			Explanation string `json:"explanation"`
		}
		if err := json.Unmarshal(rawMCW, &mcw); err == nil && mcw.Passage != "" && mcw.Explanation != "" {
			grade.MostConsequentialWeakness = &mcw
		}
	}
	if grade.MostConsequentialWeakness == nil {
		grade.MostConsequentialWeakness = harvestWeakness(root)
	}
	if err := decodeFlags(root["flags"], &grade); err != nil {
		return zero, false
	}
	return grade, true
}

// harvestWeakness collects a most-consequential weakness from the variant
// keys real models emit ("weaknesses" list, "weakness_summary" string,
// "overall" summary, or score_justifications), so the persisted grade keeps
// the signal even when the key name varies. Absent variants yield nil.
func harvestWeakness(root map[string]json.RawMessage) *struct {
	Passage     string `json:"passage"`
	Explanation string `json:"explanation"`
} {
	if raw, ok := root["weaknesses"]; ok {
		var list []string
		if err := json.Unmarshal(raw, &list); err == nil {
			for _, w := range list {
				if strings.TrimSpace(w) != "" {
					return &struct {
						Passage     string `json:"passage"`
						Explanation string `json:"explanation"`
					}{Passage: w, Explanation: w}
				}
			}
		}
	}
	for _, key := range []string{"weakness_summary", "overall_assessment", "overall_notes", "overall_note"} {
		if raw, ok := root[key]; ok {
			var s string
			if err := json.Unmarshal(raw, &s); err == nil && strings.TrimSpace(s) != "" {
				return &struct {
					Passage     string `json:"passage"`
					Explanation string `json:"explanation"`
				}{Passage: s, Explanation: s}
			}
		}
	}
	if raw, ok := root["overall"]; ok {
		var overall struct {
			Summary string `json:"summary"`
		}
		if err := json.Unmarshal(raw, &overall); err == nil && strings.TrimSpace(overall.Summary) != "" {
			return &struct {
				Passage     string `json:"passage"`
				Explanation string `json:"explanation"`
			}{Passage: overall.Summary, Explanation: overall.Summary}
		}
	}
	return nil
}

// decodeScoresAtRoot decodes scores when the five criteria live at the
// payload root (the smoke-d selection-a first-response shape): each root
// key naming a criterion holds {"score": n, ...} (or a flat n). Extra root
// keys (flags, overall_assessment) are ignored; the five must validate.
func decodeScoresAtRoot(root map[string]json.RawMessage) (map[string]int, bool) {
	sub := make(map[string]json.RawMessage, len(rubricCriteria))
	for _, name := range rubricCriteria {
		raw, ok := root[name]
		if !ok {
			return nil, false
		}
		sub[name] = raw
	}
	scores := make(map[string]int, len(rubricCriteria))
	for name, raw := range sub {
		var flat int
		if err := json.Unmarshal(raw, &flat); err == nil {
			scores[name] = flat
			continue
		}
		var wrapped struct {
			Score          *int   `json:"score"`
			SupportingPass string `json:"supporting_passage"`
			Assessment     string `json:"assessment"`
			Weakness       string `json:"weakness"`
		}
		if err := json.Unmarshal(raw, &wrapped); err != nil || wrapped.Score == nil {
			return nil, false
		}
		scores[name] = *wrapped.Score
	}
	if !validScores(scores) {
		return nil, false
	}
	return scores, true
}

// harvestRootPassages collects supporting_passage/assessment pairs carried
// inside root-level per-criterion objects (the smoke-d root-wrapped shape),
// so that shape still yields evidence passages.
func harvestRootPassages(root map[string]json.RawMessage, passages map[string]string) {
	for _, name := range rubricCriteria {
		raw, ok := root[name]
		if !ok {
			continue
		}
		var wrapped struct {
			SupportingPass string `json:"supporting_passage"`
			Assessment     string `json:"assessment"`
			Weakness       string `json:"weakness"`
		}
		if err := json.Unmarshal(raw, &wrapped); err != nil {
			continue
		}
		passage := strings.TrimSpace(wrapped.SupportingPass)
		assessment := firstNonEmpty(wrapped.Assessment, wrapped.Weakness)
		if passage == "" || strings.TrimSpace(assessment) == "" {
			continue
		}
		passages[passage] = assessment
	}
}

// decodeScores accepts flat ({"criterion": 3}), wrapped ({"criterion":
// {"score": 3, ...}}), or array ([{"name": "criterion", "score": 3}]) score
// containers, and requires one valid 0-4 score for each of the five rubric
// criteria. Criterion keys are normalized (case, spaces, and the
// unambiguous truncations real models emit, e.g. "grounding_calibration"),
// so a misspelled-but-unambiguous key still grades instead of failing.
func decodeScores(raw json.RawMessage) (map[string]int, bool) {
	if len(raw) == 0 {
		return nil, false
	}
	var flat map[string]int
	if err := json.Unmarshal(raw, &flat); err == nil {
		if norm, ok := normalizeScoreKeys(flat); ok && validScores(norm) {
			return norm, true
		}
	}
	var wrapped map[string]struct {
		Score *int `json:"score"`
	}
	if err := json.Unmarshal(raw, &wrapped); err == nil {
		scores := make(map[string]int, len(wrapped))
		for name, entry := range wrapped {
			if entry.Score == nil {
				return nil, false
			}
			norm, ok := normalizeCriterionKey(name)
			if !ok {
				return nil, false
			}
			scores[norm] = *entry.Score
		}
		if !validScores(scores) {
			return nil, false
		}
		return scores, true
	}
	var listed []struct {
		Name  string `json:"name"`
		Score *int   `json:"score"`
	}
	if err := json.Unmarshal(raw, &listed); err != nil {
		return nil, false
	}
	scores := make(map[string]int, len(listed))
	for _, entry := range listed {
		if entry.Score == nil {
			return nil, false
		}
		norm, ok := normalizeCriterionKey(entry.Name)
		if !ok {
			return nil, false
		}
		scores[norm] = *entry.Score
	}
	if !validScores(scores) {
		return nil, false
	}
	return scores, true
}

// normalizeScoreKeys normalizes every key of a flat scores map. A collision
// (two keys normalizing to one criterion) collapses the map below five
// entries and fails validScores downstream — never silently merged.
func normalizeScoreKeys(flat map[string]int) (map[string]int, bool) {
	out := make(map[string]int, len(flat))
	for name, score := range flat {
		norm, ok := normalizeCriterionKey(name)
		if !ok {
			return nil, false
		}
		out[norm] = score
	}
	return out, true
}

// normalizeCriterionKey maps a criterion key to its canonical form by its
// distinctive word, tolerating case, spacing, and unambiguous truncation
// ("Grounding and calibration", "grounding_calibration"). An unrecognized
// key reports false and the payload is rejected, never coerced.
func normalizeCriterionKey(name string) (string, bool) {
	lower := strings.ToLower(strings.ReplaceAll(strings.TrimSpace(name), " ", "_"))
	for _, canonical := range rubricCriteria {
		if lower == canonical {
			return canonical, true
		}
	}
	switch {
	case strings.Contains(lower, "grounding"):
		return "grounding_and_calibration", true
	case strings.Contains(lower, "context") || strings.Contains(lower, "fidelity") || strings.Contains(lower, "values"):
		return "context_and_values_fidelity", true
	case strings.Contains(lower, "insight") || strings.Contains(lower, "decision"):
		return "decision_insight", true
	case strings.Contains(lower, "robustness") || strings.Contains(lower, "practical"):
		return "practical_robustness", true
	case strings.Contains(lower, "role") || strings.Contains(lower, "execution"):
		return "role_execution", true
	default:
		return "", false
	}
}

// validScores requires exactly the five rubric criteria, each 0-4.
func validScores(scores map[string]int) bool {
	if len(scores) != len(rubricCriteria) {
		return false
	}
	for _, name := range rubricCriteria {
		score, ok := scores[name]
		if !ok || score < 0 || score > 4 {
			return false
		}
	}
	return true
}

// decodePassages accepts the array form real models emit ([{passage,
// assessment}, ...]), the bare string array real retries emit (each string
// is both the quoted passage and its own evidence note), and the
// originally-assumed map form ({"criterion": "passage"}). An absent field
// yields no passages; a present but malformed entry is a hard failure, so a
// broken payload is still caught.
// In every branch each passage and assessment must be non-empty after
// trimming: an empty string is malformed, not evidence.
func decodePassages(raw json.RawMessage) (map[string]string, bool) {
	if len(raw) == 0 {
		return map[string]string{}, true
	}
	var asMap map[string]string
	if json.Unmarshal(raw, &asMap) == nil {
		if asMap == nil {
			return map[string]string{}, true
		}
		for k, v := range asMap {
			if strings.TrimSpace(k) == "" || strings.TrimSpace(v) == "" {
				return nil, false
			}
		}
		return asMap, true
	}
	var stringsOnly []string
	if err := json.Unmarshal(raw, &stringsOnly); err == nil {
		passages := make(map[string]string, len(stringsOnly))
		for _, s := range stringsOnly {
			if strings.TrimSpace(s) == "" {
				return nil, false
			}
			passages[s] = s
		}
		return passages, true
	}
	var entries []struct {
		Passage    string `json:"passage"`
		Assessment string `json:"assessment"`
		WhyHelps   string `json:"why_it_helps"`
		Response   string `json:"response"`
		Issue      string `json:"issue"`
		Quote      string `json:"quote"`
	}
	if err := json.Unmarshal(raw, &entries); err != nil {
		return nil, false
	}
	passages := make(map[string]string, len(entries))
	for _, entry := range entries {
		passage := firstNonEmpty(entry.Passage, entry.Response, entry.Quote)
		assessment := firstNonEmpty(entry.Assessment, entry.WhyHelps, entry.Issue)
		if strings.TrimSpace(passage) == "" || strings.TrimSpace(assessment) == "" {
			return nil, false
		}
		passages[passage] = assessment
	}
	return passages, true
}

// firstNonEmpty returns the first non-blank string, or "".
func firstNonEmpty(ss ...string) string {
	for _, s := range ss {
		if strings.TrimSpace(s) != "" {
			return s
		}
	}
	return ""
}

// decodeNotes accepts the three-list object and the string/array forms models
// use in its place ("acceptance_notes": "..." or ["..."]).
func decodeNotes(raw json.RawMessage) (struct {
	Noticed     []string `json:"noticed"`
	Missed      []string `json:"missed"`
	BeyondNotes []string `json:"beyond_notes"`
}, bool) {
	var notes struct {
		Noticed     []string `json:"noticed"`
		Missed      []string `json:"missed"`
		BeyondNotes []string `json:"beyond_notes"`
	}
	if len(raw) == 0 {
		return notes, true
	}
	if err := json.Unmarshal(raw, &notes); err == nil {
		return notes, true
	}
	var one string
	if err := json.Unmarshal(raw, &one); err == nil {
		notes.Noticed = []string{one}
		return notes, true
	}
	var many []string
	if err := json.Unmarshal(raw, &many); err == nil {
		notes.Noticed = many
		return notes, true
	}
	return notes, false
}

// decodeFlags accepts the array of flag objects as well as the bare string
// list some models return, and rejects a present-but-malformed flag list.
// Object flags may carry the type under "type" (schema-conformant) or the
// "flag_type" alias real retries emit; quote/detail/why aliases are
// preserved into the passage slot.
func decodeFlags(raw json.RawMessage, grade *schema.AbsoluteGrade) error {
	if len(raw) == 0 {
		return nil
	}
	var objs []struct {
		Type       string `json:"type"`
		FlagType   string `json:"flag_type"`
		Passage    string `json:"passage"`
		Quote      string `json:"quote"`
		QuotedText string `json:"quoted_text"`
		Detail     string `json:"detail"`
		Violated   string `json:"violated"`
		Why        string `json:"why"`
		WhyFlagged string `json:"why_flagged"`
		Severity   string `json:"severity"`
	}
	if err := json.Unmarshal(raw, &objs); err == nil {
		for i := range objs {
			if objs[i].Type == "" {
				objs[i].Type = objs[i].FlagType
			}
			if objs[i].Type == "" {
				return errMissingFlagType
			}
			if objs[i].Passage == "" {
				objs[i].Passage = firstNonEmpty(objs[i].Quote, objs[i].QuotedText, objs[i].Detail)
			}
			if objs[i].Violated == "" {
				objs[i].Violated = firstNonEmpty(objs[i].Why, objs[i].WhyFlagged, objs[i].Severity)
			}
		}
		for _, o := range objs {
			grade.Flags = append(grade.Flags, struct {
				Type     string `json:"type"`
				Passage  string `json:"passage"`
				Violated string `json:"violated"`
			}{Type: o.Type, Passage: o.Passage, Violated: o.Violated})
		}
		return nil
	}
	var names []string
	if err := json.Unmarshal(raw, &names); err != nil {
		return err
	}
	for _, name := range names {
		grade.Flags = append(grade.Flags, struct {
			Type     string `json:"type"`
			Passage  string `json:"passage"`
			Violated string `json:"violated"`
		}{Type: name})
	}
	return nil
}

var errMissingFlagType = errf("flag type is required")

// stripCodeFences removes markdown ```json ... ``` wrappers wherever they
// appear, mirroring schema.ParseLenient's fenced-response handling.
func stripCodeFences(s string) string {
	s = strings.TrimSpace(s)
	for {
		start := strings.Index(s, "```")
		if start < 0 {
			return s
		}
		rest := s[start+3:]
		end := strings.Index(rest, "```")
		if end < 0 {
			return s
		}
		inner := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(rest[:end]), "json"))
		s = strings.TrimSpace(s[:start] + "\n" + inner + "\n" + rest[end+3:])
	}
}

// firstBalancedObject returns the first balanced {...} substring, ignoring
// braces inside JSON string literals (with escape handling).
func firstBalancedObject(s string) (string, bool) {
	start := -1
	depth := 0
	inStr := false
	esc := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		if inStr {
			if esc {
				esc = false
				continue
			}
			if c == '\\' {
				esc = true
				continue
			}
			if c == '"' {
				inStr = false
			}
			continue
		}
		switch c {
		case '"':
			inStr = true
		case '{':
			if start < 0 {
				start = i
			}
			depth++
		case '}':
			if start < 0 {
				continue
			}
			depth--
			if depth == 0 {
				return s[start : i+1], true
			}
		}
	}
	return "", false
}

// BlindResponse is the only response material a grader may see: the
// pre-rendered blind Markdown plus the role name. It carries no model
// names and no prior grades or rankings, which enforces R-07 blindness
// structurally: Grade cannot accept identity or prior-result data.
type BlindResponse struct {
	// Role is the role name labelling the response under review.
	Role string
	// Rendered is render.Markdown of the parsed (or raw) response.
	Rendered string
}

// Blind renders a stored response to blind grader material. Parsed output
// uses the uniform view/judge template; raw text uses the raw template.
// The role name comes from the response seat.
func Blind(resp *store.Response) BlindResponse {
	if resp == nil {
		return BlindResponse{}
	}
	rendered := render.Raw(resp.RawText)
	if resp.ParseOK && resp.ParsedJSON != nil {
		if v, ok := schema.ParseLenient[schema.View](*resp.ParsedJSON); ok {
			rendered = render.ViewMarkdown(v, resp.Seat)
		} else if j, ok := schema.ParseLenient[schema.Judge](*resp.ParsedJSON); ok {
			rendered = render.JudgeMarkdown(j)
		}
	}
	return BlindResponse{Role: resp.Seat, Rendered: rendered}
}

// GradeResult is the persisted absolute grade plus the inserted flags.
type GradeResult struct {
	Grade *store.Grade
	Flags []*store.Flag
}

// Grade performs absolute 0-4 rubric grading per R-08. It reuses the stored
// grade on cache hit (same response_id + grader config_hash + rubric_hash)
// and otherwise calls the grader once with exactly one parse retry (R-13),
// inserts the grade row plus one open flags row per returned flag, and
// stores word_count when missing. A double parse failure is recorded as an
// error, never retried again.
func (s *Service) Grade(ctx context.Context, in schema.CaseInput, acceptance string, resp *store.Response, grader *Grader, runID *string) (*GradeResult, error) {
	if resp == nil {
		return nil, errf("response is required")
	}
	if grader == nil {
		return nil, errf("grader is required")
	}
	if strings.TrimSpace(resp.ID) == "" {
		return nil, errf("response id is required")
	}
	if strings.TrimSpace(grader.ConfigHash) == "" {
		return nil, errf("grader config_hash is required")
	}
	// Early fail-closed validation: reject a grader whose model cannot
	// enforce the strict schema before any cache lookup or gateway call.
	if err := s.ValidateGrader(grader); err != nil {
		return nil, err
	}
	// Self-grading avoidance is enforced by SelectGrader before Grade:
	// stored responses carry no family, so Grade cannot re-derive it.
	rubricHash := grader.RubricHash
	if strings.TrimSpace(rubricHash) == "" {
		rubricHash = prompts.RubricHash()
	}
	key := GradeCacheKey(resp.ID, grader.ConfigHash, rubricHash)
	if existing, err := s.db.GetGradeByCacheKey(key); err == nil {
		flags, ferr := s.db.ListFlagsByGradeID(existing.ID)
		if ferr != nil {
			return nil, errf("load grade flags: %v", ferr)
		}
		return &GradeResult{Grade: existing, Flags: flags}, nil
	}
	blind := Blind(resp)
	msgs, err := prompts.BuildAbsoluteGrade(in, acceptance, blind.Role, blind.Rendered)
	if err != nil {
		return nil, err
	}
	content, err := s.graderChat(ctx, grader, msgs)
	if err != nil {
		return nil, err
	}
	grade, ok := parseAbsoluteGrade(content)
	if !ok {
		return nil, errf("grader %q returned unparseable absolute grade after one retry", grader.GraderKey)
	}
	if err := s.storeWordCountIfMissing(resp); err != nil {
		return nil, err
	}
	stored, flags, err := s.insertGrade(resp, grader, rubricHash, key, grade, runID)
	if err != nil {
		return nil, err
	}
	return &GradeResult{Grade: stored, Flags: flags}, nil
}

// storeWordCountIfMissing persists word_count computed from the raw text
// when the response row has none.
func (s *Service) storeWordCountIfMissing(resp *store.Response) error {
	if resp.WordCount != 0 {
		return nil
	}
	if err := s.db.SetResponseWordCountIfMissing(resp.ID, len(strings.Fields(resp.RawText))); err != nil {
		return errf("store word_count: %v", err)
	}
	return nil
}

// insertGrade persists the grade row and one open flag row per returned
// flag, returning both.
func (s *Service) insertGrade(resp *store.Response, grader *Grader, rubricHash, key string, grade schema.AbsoluteGrade, runID *string) (*store.Grade, []*store.Flag, error) {
	scoresJSON, err := json.Marshal(grade.Scores)
	if err != nil {
		return nil, nil, errf("marshal scores: %v", err)
	}
	passagesJSON, err := json.Marshal(grade.SupportingPassages)
	if err != nil {
		return nil, nil, errf("marshal passages: %v", err)
	}
	var weaknessJSON *string
	if grade.MostConsequentialWeakness != nil {
		raw, err := json.Marshal(grade.MostConsequentialWeakness)
		if err != nil {
			return nil, nil, errf("marshal weakness: %v", err)
		}
		weaknessJSON = strPtr(string(raw))
	}
	flagsJSON, err := json.Marshal(grade.Flags)
	if err != nil {
		return nil, nil, errf("marshal flags: %v", err)
	}
	notesJSON, err := json.Marshal(grade.NotesCheck)
	if err != nil {
		return nil, nil, errf("marshal notes_check: %v", err)
	}
	rawJSON, err := json.Marshal(grade)
	if err != nil {
		return nil, nil, errf("marshal raw grade: %v", err)
	}
	stored, err := s.db.InsertGrade(&store.Grade{
		CacheKey:         key,
		ResponseID:       resp.ID,
		GraderConfigHash: grader.ConfigHash,
		RubricHash:       rubricHash,
		ScoresJSON:       string(scoresJSON),
		PassagesJSON:     string(passagesJSON),
		WeaknessJSON:     weaknessJSON,
		FlagsJSON:        string(flagsJSON),
		NotesCheckJSON:   string(notesJSON),
		RawJSON:          string(rawJSON),
		RunID:            runID,
	})
	if err != nil {
		return nil, nil, errf("insert grade: %v", err)
	}
	if existing, err := s.db.ListFlagsByGradeID(stored.ID); err == nil && len(existing) > 0 {
		return stored, existing, nil
	}
	var flags []*store.Flag
	for _, f := range grade.Flags {
		ins, err := s.db.InsertFlag(&store.Flag{
			GradeID:    stored.ID,
			ResponseID: resp.ID,
			Type:       f.Type,
			Passage:    f.Passage,
			Violated:   f.Violated,
			Status:     "open",
			RunID:      runID,
		})
		if err != nil {
			return nil, nil, errf("insert flag: %v", err)
		}
		flags = append(flags, ins)
	}
	return stored, flags, nil
}

// graderChat performs one grader call with exactly one parse retry (R-13):
// on a first response that fails lenient parsing as an absolute grade, it
// appends a single "Return only the JSON object" user message and retries
// once. Any error (including the retry) is returned without further
// retries; non-grader calls must not use this helper.
func (s *Service) graderChat(ctx context.Context, grader *Grader, msgs []gateway.Message) (string, error) {
	content, err := s.absoluteChat(ctx, grader, msgs)
	if err != nil {
		return "", err
	}
	if _, ok := parseAbsoluteGrade(content); ok {
		return content, nil
	}
	retry := append(append([]gateway.Message{}, msgs...),
		gateway.Message{Role: "user", Content: "Return only the JSON object."})
	return s.absoluteChat(ctx, grader, retry)
}

// absoluteChat performs a single absolute-grader gateway call. It sends the
// strict json_schema response_format; a grader model without strict
// json_schema support is rejected by ValidateGrader, never downgraded to
// json_object. Any error is returned as-is.
func (s *Service) absoluteChat(ctx context.Context, grader *Grader, msgs []gateway.Message) (string, error) {
	if err := s.ValidateGrader(grader); err != nil {
		return "", err
	}
	return s.chatWithFormat(ctx, grader, msgs, s.graderResponseFormat(grader.Model))
}

// graderResponseFormat selects the response format for an absolute-grader
// call: strict json_schema (schema.AbsoluteJSONSchema) when supported,
// else nil. There is intentionally no json_object fallback — an
// unenforced grader shape is a degraded grade, so ValidateGrader rejects
// such models before any call. A nil registry returns nil (the unit-test
// path); the view/judge responseFormatFor helpers in internal/council and
// internal/harness keep their own json_object behaviour and are untouched.
func (s *Service) graderResponseFormat(model string) *gateway.ResponseFormat {
	if s.models == nil {
		return nil
	}
	_, _, _, jsonSchema, _ := s.models.Supports(model)
	if !jsonSchema {
		return nil
	}
	return &gateway.ResponseFormat{
		Type: "json_schema", SchemaName: "absolute",
		Schema: json.RawMessage(schema.AbsoluteJSONSchema), Strict: true,
	}
}

// graderBudgetFor returns the output-token budget for a grader call: the
// per-grader max_output_tokens seat setting carried in ParamsJSON
// (graders.yaml, synced by cmd/council syncGradersFile) when it exceeds
// the GraderDefaultMaxOutputTokens floor, else the floor. The registry
// (internal/models) carries no per-model output ceiling — MaxOutputTokens
// is always allowed — so the seat setting is the capability-derived
// source. ParamsJSON is untrusted and never fails a call: unparseable or
// non-positive values fall back to the floor.
func graderBudgetFor(grader *Grader) int {
	if grader != nil && strings.TrimSpace(grader.ParamsJSON) != "" {
		var params struct {
			MaxOutputTokens int `json:"max_output_tokens"`
		}
		if err := json.Unmarshal([]byte(grader.ParamsJSON), &params); err == nil && params.MaxOutputTokens > GraderDefaultMaxOutputTokens {
			return params.MaxOutputTokens
		}
	}
	return GraderDefaultMaxOutputTokens
}

// chatWithFormat performs a single gateway call with the grader model and
// the given response format. No retries, no fallbacks, no substitution:
// any error is returned as-is.
func (s *Service) chatWithFormat(ctx context.Context, grader *Grader, msgs []gateway.Message, rf *gateway.ResponseFormat) (string, error) {
	resp, err := s.gw.Chat(ctx, gateway.ChatRequest{
		Model:           grader.Model,
		Messages:        msgs,
		MaxOutputTokens: graderBudgetFor(grader),
		ResponseFormat:  rf,
		Tags:            map[string]string{"grader": grader.GraderKey},
	})
	if err != nil {
		return "", errf("grader %q call: %v", grader.GraderKey, err)
	}
	return resp.Content, nil
}

// chat performs a single gateway call with the grader model. No retries,
// no fallbacks, no substitution: any error is returned as-is.
func (s *Service) chat(ctx context.Context, grader *Grader, msgs []gateway.Message) (string, error) {
	return s.chatWithFormat(ctx, grader, msgs, nil)
}
