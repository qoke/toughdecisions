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

// graderMaxOutputTokens is the grader call budget. The largest complete
// evidence grade is 4916 chars (~1250 tokens); the one length-truncated reply
// died at 2663 chars under the old 2000-token cap. Doubling to 4000 keeps the
// longest observed grade plus full chain-of-thought headroom while staying far
// below the 128k provider ceiling, and mirrors the judge seat's 3000-token
// budget (pack.DefaultJudgeMaxOutputTokens) with margin for grader verbosity.
const graderMaxOutputTokens = 4000

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
// of the assumed flat {"criterion": n}.
var graderInputShapes = []struct{ scores, passages, notes string }{
	{"scores", "supporting_passages", "notes_check"},
	{"scores", "supporting_passages", "acceptance_notes"},
	{"criteria", "supporting_passages", "notes_check"},
	{"criteria", "supporting_passages", "acceptance_notes"},
	{"assessment", "supporting_passages", "acceptance_notes"},
	{"scores", "supporting_passages", "notes"},
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
func decodeAbsoluteGrade(root map[string]json.RawMessage, scoresKey, passagesKey, notesKey string) (schema.AbsoluteGrade, bool) {
	var zero schema.AbsoluteGrade
	raw, ok := root[scoresKey]
	if !ok {
		return zero, false
	}
	scores, ok := decodeScores(raw)
	if !ok {
		return zero, false
	}
	passages, ok := decodePassages(root[passagesKey])
	if !ok {
		return zero, false
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
	if err := decodeFlags(root["flags"], &grade); err != nil {
		return zero, false
	}
	return grade, true
}

// decodeScores accepts either {"criterion": 3} or {"criterion": {"score": 3,
// ...}} and requires one valid 0-4 score for each of the five rubric criteria.
func decodeScores(raw json.RawMessage) (map[string]int, bool) {
	if len(raw) == 0 {
		return nil, false
	}
	var flat map[string]int
	if err := json.Unmarshal(raw, &flat); err == nil && validScores(flat) {
		return flat, true
	}
	var wrapped map[string]struct {
		Score *int `json:"score"`
	}
	if err := json.Unmarshal(raw, &wrapped); err != nil {
		return nil, false
	}
	scores := make(map[string]int, len(wrapped))
	for name, entry := range wrapped {
		if entry.Score == nil {
			return nil, false
		}
		scores[name] = *entry.Score
	}
	if !validScores(scores) {
		return nil, false
	}
	return scores, true
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
// assessment}, ...]) and the originally-assumed map form
// ({"criterion": "passage"}). An absent field yields no passages; a present
// but malformed entry is a hard failure, so a broken payload is still caught.
// In both branches every passage key and assessment value must be non-empty
// after trimming: an empty string is malformed, not evidence.
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
	var entries []struct {
		Passage    string `json:"passage"`
		Assessment string `json:"assessment"`
	}
	if err := json.Unmarshal(raw, &entries); err != nil {
		return nil, false
	}
	passages := make(map[string]string, len(entries))
	for _, entry := range entries {
		if strings.TrimSpace(entry.Passage) == "" || strings.TrimSpace(entry.Assessment) == "" {
			return nil, false
		}
		passages[entry.Passage] = entry.Assessment
	}
	return passages, true
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
func decodeFlags(raw json.RawMessage, grade *schema.AbsoluteGrade) error {
	if len(raw) == 0 {
		return nil
	}
	var objs []struct {
		Type     string `json:"type"`
		Passage  string `json:"passage"`
		Violated string `json:"violated"`
	}
	if err := json.Unmarshal(raw, &objs); err == nil {
		for _, o := range objs {
			if o.Type == "" {
				return errMissingFlagType
			}
		}
		grade.Flags = objs
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
	content, err := s.chat(ctx, grader, msgs)
	if err != nil {
		return "", err
	}
	if _, ok := parseAbsoluteGrade(content); ok {
		return content, nil
	}
	retry := append(append([]gateway.Message{}, msgs...),
		gateway.Message{Role: "user", Content: "Return only the JSON object."})
	return s.chat(ctx, grader, retry)
}

// chat performs a single gateway call with the grader model. No retries,
// no fallbacks, no substitution: any error is returned as-is.
func (s *Service) chat(ctx context.Context, grader *Grader, msgs []gateway.Message) (string, error) {
	resp, err := s.gw.Chat(ctx, gateway.ChatRequest{
		Model:           grader.Model,
		Messages:        msgs,
		MaxOutputTokens: graderMaxOutputTokens,
		Tags:            map[string]string{"grader": grader.GraderKey},
	})
	if err != nil {
		return "", errf("grader %q call: %v", grader.GraderKey, err)
	}
	return resp.Content, nil
}
