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
		return &GradeResult{Grade: existing}, nil
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
	grade, ok := schema.ParseLenient[schema.AbsoluteGrade](content)
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
	if _, ok := schema.ParseLenient[schema.AbsoluteGrade](content); ok {
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
		MaxOutputTokens: 2000,
		Tags:            map[string]string{"grader": grader.GraderKey},
	})
	if err != nil {
		return "", errf("grader %q call: %v", grader.GraderKey, err)
	}
	return resp.Content, nil
}
