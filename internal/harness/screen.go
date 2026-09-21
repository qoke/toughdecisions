package harness

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/qoke/toughdecisions/internal/grading"
	"github.com/qoke/toughdecisions/internal/pack"
	"github.com/qoke/toughdecisions/internal/schema"
	"github.com/qoke/toughdecisions/internal/store"
	"gopkg.in/yaml.v3"
)

// MaxCandidates is the plan §12.2 cap: more than 3 is an error.
const MaxCandidates = 3

// ScreenMeanTolerance is the plan §12.2 mean rule (⚙): the candidate mean
// must be within 0.25 of the incumbent mean.
const ScreenMeanTolerance = 0.25

// CandidateSpec is one entry in candidates.yaml.
type CandidateSpec struct {
	Key              string   `yaml:"key"`
	Seat             string   `yaml:"seat"`
	Model            string   `yaml:"model"`
	Family           string   `yaml:"family"`
	Temperature      *float64 `yaml:"temperature"`
	TopP             *float64 `yaml:"top_p"`
	ReasoningEffort  string   `yaml:"reasoning_effort"`
	MaxOutputTokens  int      `yaml:"max_output_tokens"`
	RolePromptFile   string   `yaml:"role_prompt_file"`
	RolePromptInline string   `yaml:"role_prompt_override"`
	Finalist         string   `yaml:"finalist"`
	Notes            string   `yaml:"notes"`
}

type candidatesFile struct {
	Candidates []CandidateSpec `yaml:"candidates"`
}

// LoadCandidates parses path and validates every entry: at most
// MaxCandidates, a known seat, a finalist mode of auto|force|never, and
// model settings through models.ValidateSettings (rejects unsupported
// settings, never drops them).
func (r *Runner) LoadCandidates(path string) ([]CandidateSpec, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("harness: read candidates %s: %w", path, err)
	}
	var f candidatesFile
	if err := yaml.Unmarshal(raw, &f); err != nil {
		return nil, fmt.Errorf("harness: parse candidates %s: %w", path, err)
	}
	if len(f.Candidates) > MaxCandidates {
		return nil, fmt.Errorf("harness: %d candidates in %s exceeds the max of %d",
			len(f.Candidates), path, MaxCandidates)
	}
	for _, c := range f.Candidates {
		if strings.TrimSpace(c.Key) == "" {
			return nil, fmt.Errorf("harness: candidate with empty key in %s", path)
		}
		seat := pack.Seat(c.Seat)
		if !seat.Valid() {
			return nil, fmt.Errorf("harness: candidate %q has unknown seat %q", c.Key, c.Seat)
		}
		switch c.Finalist {
		case "", "auto", "force", "never":
		default:
			return nil, fmt.Errorf("harness: candidate %q has invalid finalist %q (want auto|force|never)", c.Key, c.Finalist)
		}
		if c.RolePromptFile != "" && c.RolePromptInline != "" {
			return nil, fmt.Errorf("harness: candidate %q sets both role_prompt_file and role_prompt_override", c.Key)
		}
		override := c.RolePromptInline
		if c.RolePromptFile != "" {
			oraw, err := os.ReadFile(c.RolePromptFile)
			if err != nil {
				return nil, fmt.Errorf("harness: candidate %q role_prompt_file: %w", c.Key, err)
			}
			override = strings.TrimSpace(string(oraw))
			if override == "" {
				return nil, fmt.Errorf("harness: candidate %q role_prompt_file is empty", c.Key)
			}
		}
		sc := seatConfigFor(c, seat, override)
		if err := r.models.ValidateSettings(sc); err != nil {
			return nil, fmt.Errorf("harness: candidate %q: %w", c.Key, err)
		}
	}
	return f.Candidates, nil
}

// seatConfigFor maps a candidate spec onto a pack SeatConfig for settings
// validation and cache-key hashing.
func seatConfigFor(c CandidateSpec, seat pack.Seat, override string) pack.SeatConfig {
	maxTokens := c.MaxOutputTokens
	if maxTokens == 0 {
		maxTokens = pack.DefaultMaxOutputTokens(seat)
	}
	return pack.SeatConfig{
		Seat: seat, Model: c.Model, Family: c.Family,
		Temperature: c.Temperature, TopP: c.TopP,
		ReasoningEffort: c.ReasoningEffort, MaxOutputTokens: maxTokens,
		RolePromptOverride: override,
	}
}

// ScreenCase is one development case selected for screening.
type ScreenCase struct {
	Case    *store.Case
	Family  *store.Family
	Input   schema.CaseInput
	Accept  string
	Incumb  *store.Response
	Grade   *store.Grade
	CandTot float64
	IncTot  float64
}

// SelectScreenCases picks up to ScreenCases development cases whose
// seats_relevant includes the seat, ordered priority first then by key.
func (r *Runner) SelectScreenCases(seat pack.Seat) ([]*ScreenCase, error) {
	families, err := r.db.ListFamilies()
	if err != nil {
		return nil, fmt.Errorf("harness: list families: %w", err)
	}
	type famPrio struct {
		f    *store.Family
		prio bool
	}
	var dev []famPrio
	for _, f := range families {
		if f.Split != "development" {
			continue
		}
		if !seatRelevant(f.SeatsRelevantJSON, string(seat)) {
			continue
		}
		tags := decodeTags(f.TagsJSON)
		dev = append(dev, famPrio{f: f, prio: tags["priority"]})
	}
	sort.Slice(dev, func(i, j int) bool {
		if dev[i].prio != dev[j].prio {
			return dev[i].prio
		}
		return dev[i].f.FamilyKey < dev[j].f.FamilyKey
	})
	var out []*ScreenCase
	for _, d := range dev {
		cases, err := r.db.ListCasesByFamily(d.f.ID)
		if err != nil {
			return nil, fmt.Errorf("harness: list cases for %q: %w", d.f.FamilyKey, err)
		}
		for _, c := range cases {
			in, err := CaseInputFor(c)
			if err != nil {
				return nil, err
			}
			out = append(out, &ScreenCase{Case: c, Family: d.f, Input: in, Accept: d.f.AcceptanceJSON})
			if len(out) >= r.ScreenCases() {
				return out, nil
			}
		}
	}
	return out, nil
}

func seatRelevant(raw, seat string) bool {
	var seats []string
	if err := json.Unmarshal([]byte(raw), &seats); err != nil {
		return false
	}
	for _, s := range seats {
		if s == seat {
			return true
		}
	}
	return false
}

// ScreenResult is the per-candidate screening outcome.
type ScreenResult struct {
	RunID        string
	CandidateKey string
	Means        map[string]float64
	MeanTotal    float64
	IncMeanTotal float64
	Wins         int
	Cases        int
	OpenFlags    int
	Passed       bool
	Finalist     bool
	Mode         string
	Weakest      string
}

// Screen runs plan §12.2 for every candidate in path: candidate and
// incumbent responses go through GenerateResponse (repetition 0 unless
// fresh), both are graded with the SAME screening grader, and the
// passed/finalist rules decide the outcome. Results are stored on the
// candidates rows of one harness run.
func (r *Runner) Screen(ctx context.Context, path string, fresh bool) ([]ScreenResult, error) {
	specs, err := r.LoadCandidates(path)
	if err != nil {
		return nil, err
	}
	pk, err := packActive(r)
	if err != nil {
		return nil, err
	}
	screener, err := r.ScreeningGrader()
	if err != nil {
		return nil, err
	}
	params, _ := json.Marshal(map[string]any{"fresh": fresh})
	run, err := r.StartRun("screen", pk.ID, string(params))
	if err != nil {
		return nil, err
	}
	fail := func(reason error) ([]ScreenResult, error) {
		_ = r.FailRun(run.ID, reason.Error())
		return nil, reason
	}

	var results []ScreenResult
	for _, spec := range specs {
		res, err := r.screenOne(ctx, run.ID, pk, screener, spec, fresh)
		if err != nil {
			return fail(err)
		}
		results = append(results, *res)
	}
	raw, _ := json.Marshal(results)
	if err := r.FinishRun(run.ID, "complete", string(raw)); err != nil {
		return nil, err
	}
	return results, nil
}

func (r *Runner) screenOne(ctx context.Context, runID string, pk *pack.Pack, screener *grading.Grader, spec CandidateSpec, fresh bool) (*ScreenResult, error) {
	seat := pack.Seat(spec.Seat)
	cases, err := r.SelectScreenCases(seat)
	if err != nil {
		return nil, err
	}
	if len(cases) == 0 {
		return nil, fmt.Errorf("harness: no development cases relevant to seat %q", string(seat))
	}
	candCfg := seatConfigFor(spec, seat, "")
	incCfg, ok := pk.Seats[seat]
	if !ok {
		return nil, fmt.Errorf("harness: seat %q not in active pack", string(seat))
	}
	type pair struct {
		cand *store.Response
		inc  *store.Response
	}
	pairs := make([]pair, len(cases))
	errs := r.Pool(ctx, poolJobs(cases, func(ctx context.Context, i int) error {
		sc := cases[i]
		cr, err := r.GenerateResponse(ctx, GenRequest{
			Seat: seat, SeatCfg: candCfg, Input: sc.Input,
			InputHash: InputHashFor(sc.Input), RunID: runID, Fresh: fresh,
		})
		if err != nil {
			return fmt.Errorf("harness: candidate %q case %q: %w", spec.Key, sc.Case.CaseKey, err)
		}
		ir, err := r.GenerateResponse(ctx, GenRequest{
			Seat: seat, SeatCfg: incCfg, Input: sc.Input,
			InputHash: InputHashFor(sc.Input), RunID: runID, Fresh: fresh,
		})
		if err != nil {
			return fmt.Errorf("harness: incumbent case %q: %w", sc.Case.CaseKey, err)
		}
		pairs[i] = pair{cand: cr.Response, inc: ir.Response}
		return nil
	}))
	for _, err := range errs {
		if err != nil {
			return nil, err
		}
	}

	criteria := []string{"grounding_calibration", "context_values_fidelity", "decision_insight", "practical_robustness", "role_execution"}
	sums := map[string]float64{}
	var candTotal, incTotal float64
	wins := 0
	openFlags := 0
	for i, sc := range cases {
		cg, err := r.gradeWith(ctx, sc.Input, sc.Accept, pairs[i].cand, screener, runID)
		if err != nil {
			return nil, fmt.Errorf("harness: grade candidate %q case %q: %w", spec.Key, sc.Case.CaseKey, err)
		}
		ig, err := r.gradeWith(ctx, sc.Input, sc.Accept, pairs[i].inc, screener, runID)
		if err != nil {
			return nil, fmt.Errorf("harness: grade incumbent case %q: %w", sc.Case.CaseKey, err)
		}
		ct := totalOf(cg)
		it := totalOf(ig)
		cases[i].CandTot = ct
		cases[i].IncTot = it
		cases[i].Grade = cg
		candTotal += ct
		incTotal += it
		if ct >= it {
			wins++
		}
		if n := countOpenFlags(cg); n > 0 {
			openFlags += n
		}
		for _, c := range criteria {
			sums[c] += float64(scoreOf(cg, c))
		}
	}
	n := float64(len(cases))
	means := map[string]float64{}
	weakest := criteria[0]
	for _, c := range criteria {
		means[c] = sums[c] / n
		if means[c] < means[weakest] {
			weakest = c
		}
	}
	meanTotal := candTotal / n
	incMean := incTotal / n
	mode := spec.Finalist
	if mode == "" {
		mode = "auto"
	}
	passed := ScreenPassed(openFlags, meanTotal, incMean, wins, len(cases))
	finalist := FinalistForMode(mode, passed)
	res := &ScreenResult{
		RunID: runID, CandidateKey: spec.Key, Means: means, MeanTotal: meanTotal,
		IncMeanTotal: incMean, Wins: wins, Cases: len(cases),
		OpenFlags: openFlags, Passed: passed, Finalist: finalist,
		Mode: mode, Weakest: weakest,
	}
	cfgRaw, _ := json.Marshal(candCfg)
	if _, err := r.db.UpsertCandidate(runID, spec.Key, string(seat), string(cfgRaw), candCfg.Hash()); err != nil {
		return nil, fmt.Errorf("harness: record candidate %q: %w", spec.Key, err)
	}
	row, err := r.db.GetCandidateByKey(runID, spec.Key)
	if err != nil {
		return nil, err
	}
	sjson, _ := json.Marshal(res)
	s := string(sjson)
	fin := finalist
	if err := r.db.UpdateCandidateResults(row.ID, &store.CandidateResults{
		ScreenResultJSON: &s, Finalist: &fin,
	}); err != nil {
		return nil, fmt.Errorf("harness: store screen result: %w", err)
	}
	return res, nil
}

// gradeWith grades one response under the grader-call deadline with the
// single allowed parse retry inside the grading service.
func (r *Runner) gradeWith(ctx context.Context, in schema.CaseInput, acceptance string, resp *store.Response, grader *grading.Grader, runID string) (*store.Grade, error) {
	callCtx, cancel := context.WithDeadline(ctx, time.Now().Add(r.GraderDeadline()))
	defer cancel()
	res, err := r.grade.Grade(callCtx, in, acceptance, resp, grader, &runID)
	if err != nil {
		return nil, err
	}
	return res.Grade, nil
}

func totalOf(g *store.Grade) float64 {
	var scores map[string]int
	if err := json.Unmarshal([]byte(g.ScoresJSON), &scores); err != nil {
		return 0
	}
	sum := 0
	for _, v := range scores {
		sum += v
	}
	return float64(sum)
}

func scoreOf(g *store.Grade, criterion string) int {
	var scores map[string]int
	if err := json.Unmarshal([]byte(g.ScoresJSON), &scores); err != nil {
		return 0
	}
	return scores[criterion]
}

func countOpenFlags(g *store.Grade) int {
	var flags []map[string]any
	if err := json.Unmarshal([]byte(g.FlagsJSON), &flags); err != nil {
		return 0
	}
	return len(flags)
}
