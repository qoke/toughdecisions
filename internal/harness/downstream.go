package harness

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/qoke/toughdecisions/internal/grading"
	"github.com/qoke/toughdecisions/internal/pack"
	"github.com/qoke/toughdecisions/internal/render"
	"github.com/qoke/toughdecisions/internal/schema"
	"github.com/qoke/toughdecisions/internal/store"
)

// DownstreamOptions configures one downstream run for a single candidate.
// Compare and Cover override the pairwise and coverage steps in tests; nil
// uses both selection graders (purpose "downstream") and grader A.
type DownstreamOptions struct {
	Spec    CandidateSpec
	RunID   string
	Fresh   bool
	Compare CompareFunc
	Cover   CoverFunc
}

// CoverFunc records issue coverage for one council on one case.
type CoverFunc func(ctx context.Context, in coverInput, views map[string]string, judge string, grader *grading.Grader, runID string) error

type coverInput struct {
	Acceptance string
	CaseID     string
	Label      string
	Planted    []grading.Issue
	Case       schema.CaseInput
}

// DownstreamCaseVerdict is the per-case downstream outcome. Verdict is in
// left/right space: "left" means the candidate council wins.
type DownstreamCaseVerdict struct {
	CaseKey     string
	FamilyKey   string
	BundleKey   string
	Agreed      bool
	Verdict     string
	Difference  string
	FreshRepeat bool
}

// DownstreamResult is the persisted downstream outcome. Reporting and
// promotion read this type; every number is per-run.
type DownstreamResult struct {
	RunID        string
	CandidateKey string
	Seat         string
	Cases        []DownstreamCaseVerdict
	AgreedNet    int
	FreshRepeats int
}

// Downstream runs plan §12.5 for one candidate. A view candidate is dropped
// into a council with cached incumbent views and judged by the incumbent
// judge; a judge candidate judges the identical saved bundles, including
// authored manipulation bundles. Close finals get exactly one --fresh
// judge repetition. It stores downstream_result_json on the candidate row.
func (r *Runner) Downstream(ctx context.Context, opts DownstreamOptions) (*DownstreamResult, error) {
	seat := pack.Seat(opts.Spec.Seat)
	if !seat.Valid() {
		return nil, fmt.Errorf("harness: unknown seat %q", opts.Spec.Seat)
	}
	pk, err := packActive(r)
	if err != nil {
		return nil, err
	}
	runID := opts.RunID
	if runID == "" {
		params, _ := json.Marshal(map[string]any{"fresh": opts.Fresh})
		run, err := r.StartRun("downstream", pk.ID, string(params))
		if err != nil {
			return nil, err
		}
		runID = run.ID
	}
	fail := func(reason error) (*DownstreamResult, error) {
		_ = r.FailRun(runID, reason.Error())
		return nil, reason
	}
	if _, _, err := r.compareDriftGate(ctx, runID); err != nil {
		return fail(err)
	}
	graders, err := r.selectionPair()
	if err != nil {
		return fail(err)
	}
	cmp := opts.Compare
	if cmp == nil {
		cmp = r.comparePurpose("downstream")
	}
	cover := opts.Cover
	if cover == nil {
		cover = r.coverDefault
	}
	cases, err := r.SelectCompareCases(seat)
	if err != nil {
		return fail(err)
	}
	candCfg := seatConfigFor(opts.Spec, seat, "")
	res := &DownstreamResult{RunID: runID, CandidateKey: opts.Spec.Key, Seat: string(seat)}
	covGrader := graders[0]
	for _, cc := range cases {
		v, err := r.downstreamOne(ctx, runID, pk, candCfg, seat, cc, opts.Fresh, graders, covGrader, cmp, cover)
		if err != nil {
			return fail(err)
		}
		res.Cases = append(res.Cases, *v)
		if v.FreshRepeat {
			res.FreshRepeats++
		}
		if v.Agreed {
			switch v.Verdict {
			case "left":
				res.AgreedNet++
			case "right":
				res.AgreedNet--
			}
		}
	}
	if err := r.storeDownstreamResult(runID, opts.Spec, candCfg, res); err != nil {
		return fail(err)
	}
	raw, _ := json.Marshal(res)
	if err := r.FinishRun(runID, "complete", string(raw)); err != nil {
		return nil, err
	}
	return res, nil
}

// downstreamOne runs one downstream case: build both councils, judge both,
// pairwise them, cover both, and add exactly one --fresh judge repetition
// when the final is close (tie or grader disagreement).
func (r *Runner) downstreamOne(ctx context.Context, runID string, pk *pack.Pack, candCfg pack.SeatConfig, seat pack.Seat, cc *CompareCase, fresh bool, graders []*grading.Grader, covGrader *grading.Grader, cmp CompareFunc, cover CoverFunc) (*DownstreamCaseVerdict, error) {
	incBundle, candBundle, err := r.downstreamBundles(ctx, runID, pk, candCfg, seat, cc, fresh)
	if err != nil {
		return nil, err
	}
	candJudge, incJudge, err := r.downstreamJudges(ctx, runID, pk, candCfg, seat, cc, fresh, incBundle, candBundle)
	if err != nil {
		return nil, err
	}
	verdict, agreed, diff, err := cmp(ctx, compareInput{
		Acceptance: cc.Family.AcceptanceJSON, CaseID: cc.Case.ID,
		Seat: string(seat), Case: cc.Input,
	}, candJudge, incJudge, graders, runID)
	if err != nil {
		return nil, fmt.Errorf("harness: downstream compare case %q: %w", cc.Case.CaseKey, err)
	}
	v := &DownstreamCaseVerdict{
		CaseKey: cc.Case.CaseKey, FamilyKey: cc.Family.FamilyKey,
		Agreed: agreed, Verdict: verdict, Difference: diff,
	}
	if cc.Bundle != nil {
		v.BundleKey = cc.Bundle.BundleKey
	}
	if plant := plantedFor(cc.Family); len(plant) > 0 {
		candViews := renderedViewsOf(candBundle)
		incViews := renderedViewsOf(incBundle)
		if err := cover(ctx, coverInput{
			Acceptance: cc.Family.AcceptanceJSON, CaseID: cc.Case.ID,
			Label: "candidate:" + candCfg.Model, Planted: plant, Case: cc.Input,
		}, candViews, blindOf(candJudge), covGrader, runID); err != nil {
			return nil, fmt.Errorf("harness: cover candidate case %q: %w", cc.Case.CaseKey, err)
		}
		if err := cover(ctx, coverInput{
			Acceptance: cc.Family.AcceptanceJSON, CaseID: cc.Case.ID,
			Label: "incumbent", Planted: plant, Case: cc.Input,
		}, incViews, blindOf(incJudge), covGrader, runID); err != nil {
			return nil, fmt.Errorf("harness: cover incumbent case %q: %w", cc.Case.CaseKey, err)
		}
	}
	if verdict == "tie" || !agreed {
		refreshed, err := r.freshJudgeRepeat(ctx, runID, pk, candCfg, seat, cc, incBundle, candBundle)
		if err != nil {
			return nil, err
		}
		verdict2, agreed2, diff2, err := cmp(ctx, compareInput{
			Acceptance: cc.Family.AcceptanceJSON, CaseID: cc.Case.ID,
			Seat: string(seat), Case: cc.Input,
		}, refreshed.cand, refreshed.inc, graders, runID)
		if err != nil {
			return nil, fmt.Errorf("harness: downstream fresh compare case %q: %w", cc.Case.CaseKey, err)
		}
		v.Verdict, v.Agreed, v.Difference, v.FreshRepeat = verdict2, agreed2, diff2, true
	}
	return v, nil
}

// downstreamBundles builds the incumbent and candidate bundle views. A view
// candidate replaces its seat while cached incumbent views fill the other
// seats; a judge candidate reuses the identical saved bundle for both.
func (r *Runner) downstreamBundles(ctx context.Context, runID string, pk *pack.Pack, candCfg pack.SeatConfig, seat pack.Seat, cc *CompareCase, fresh bool) (inc, cand map[string]*store.Response, err error) {
	if seat == pack.SeatJudge {
		bundle := cc.Bundle
		if bundle == nil {
			return nil, nil, fmt.Errorf("harness: judge candidate needs a saved bundle for case %q", cc.Case.CaseKey)
		}
		inc, err = r.cachedIncumbentViews(ctx, runID, pk, cc, bundle, fresh)
		if err != nil {
			return nil, nil, err
		}
		return inc, inc, nil
	}
	inc, err = r.cachedIncumbentViews(ctx, runID, pk, cc, nil, fresh)
	if err != nil {
		return nil, nil, err
	}
	cand = map[string]*store.Response{}
	for s, resp := range inc {
		cand[s] = resp
	}
	cr, err := r.GenerateResponse(ctx, GenRequest{
		Seat: seat, SeatCfg: candCfg, Input: cc.Input,
		InputHash: InputHashFor(cc.Input), RunID: runID, Fresh: fresh,
	})
	if err != nil {
		return nil, nil, fmt.Errorf("harness: downstream candidate view case %q: %w", cc.Case.CaseKey, err)
	}
	cand[string(seat)] = cr.Response
	return inc, cand, nil
}

// cachedIncumbentViews ensures one incumbent response per view seat: from
// the saved bundle's views when the judge seat tests bundles, otherwise
// generated (and cached) through the Step 8 helper.
func (r *Runner) cachedIncumbentViews(ctx context.Context, runID string, pk *pack.Pack, cc *CompareCase, bundle *store.Bundle, fresh bool) (map[string]*store.Response, error) {
	out := map[string]*store.Response{}
	if bundle != nil {
		views, err := bundleResponsesFor(r, bundle)
		if err != nil {
			return nil, err
		}
		for s, resp := range views {
			out[s] = resp
		}
		return out, nil
	}
	for _, s := range []pack.Seat{pack.SeatPossibility, pack.SeatPerspective, pack.SeatStressTester} {
		cfg, ok := pk.Seats[s]
		if !ok {
			return nil, fmt.Errorf("harness: seat %q not in active pack", string(s))
		}
		gr, err := r.GenerateResponse(ctx, GenRequest{
			Seat: s, SeatCfg: cfg, Input: cc.Input,
			InputHash: InputHashFor(cc.Input), RunID: runID, Fresh: fresh,
		})
		if err != nil {
			return nil, fmt.Errorf("harness: downstream incumbent view %q case %q: %w", string(s), cc.Case.CaseKey, err)
		}
		out[string(s)] = gr.Response
	}
	return out, nil
}

// downstreamJudges runs the incumbent judge over the candidate council and
// the incumbent council (view candidate), or the candidate and incumbent
// judges over the identical saved bundle (judge candidate).
func (r *Runner) downstreamJudges(ctx context.Context, runID string, pk *pack.Pack, candCfg pack.SeatConfig, seat pack.Seat, cc *CompareCase, fresh bool, incBundle, candBundle map[string]*store.Response) (cand, inc *store.Response, err error) {
	judgeCfg, ok := pk.Seats[pack.SeatJudge]
	if !ok {
		return nil, nil, fmt.Errorf("harness: seat %q not in active pack", "judge")
	}
	if seat == pack.SeatJudge {
		bundle := cc.Bundle
		cr, err := r.GenerateResponse(ctx, GenRequest{
			Seat: pack.SeatJudge, SeatCfg: candCfg, Input: cc.Input,
			InputHash: InputHashFor(cc.Input), Bundle: bundle,
			RunID: runID, Fresh: fresh,
		})
		if err != nil {
			return nil, nil, fmt.Errorf("harness: downstream candidate judge case %q: %w", cc.Case.CaseKey, err)
		}
		ir, err := r.GenerateResponse(ctx, GenRequest{
			Seat: pack.SeatJudge, SeatCfg: judgeCfg, Input: cc.Input,
			InputHash: InputHashFor(cc.Input), Bundle: bundle,
			RunID: runID, Fresh: fresh,
		})
		if err != nil {
			return nil, nil, fmt.Errorf("harness: downstream incumbent judge case %q: %w", cc.Case.CaseKey, err)
		}
		return cr.Response, ir.Response, nil
	}
	candB := bundleForHash(incBundleHash(candBundle))
	incB := bundleForHash(incBundleHash(incBundle))
	cr, err := r.GenerateResponse(ctx, GenRequest{
		Seat: pack.SeatJudge, SeatCfg: judgeCfg, Input: cc.Input,
		InputHash: InputHashFor(cc.Input), Bundle: candB,
		RunID: runID, Fresh: fresh,
	})
	if err != nil {
		return nil, nil, fmt.Errorf("harness: downstream candidate council judge case %q: %w", cc.Case.CaseKey, err)
	}
	ir, err := r.GenerateResponse(ctx, GenRequest{
		Seat: pack.SeatJudge, SeatCfg: judgeCfg, Input: cc.Input,
		InputHash: InputHashFor(cc.Input), Bundle: incB,
		RunID: runID, Fresh: fresh,
	})
	if err != nil {
		return nil, nil, fmt.Errorf("harness: downstream incumbent council judge case %q: %w", cc.Case.CaseKey, err)
	}
	return cr.Response, ir.Response, nil
}

type judgePair struct {
	cand *store.Response
	inc  *store.Response
}

// freshJudgeRepeat regenerates exactly one --fresh judge call per council
// after a close final and returns the new pair.
func (r *Runner) freshJudgeRepeat(ctx context.Context, runID string, pk *pack.Pack, candCfg pack.SeatConfig, seat pack.Seat, cc *CompareCase, incBundle, candBundle map[string]*store.Response) (judgePair, error) {
	judgeCfg, ok := pk.Seats[pack.SeatJudge]
	if !ok {
		return judgePair{}, fmt.Errorf("harness: seat %q not in active pack", "judge")
	}
	if seat == pack.SeatJudge {
		bundle := cc.Bundle
		cr, err := r.GenerateResponse(ctx, GenRequest{
			Seat: pack.SeatJudge, SeatCfg: candCfg, Input: cc.Input,
			InputHash: InputHashFor(cc.Input), Bundle: bundle,
			RunID: runID, Fresh: true,
		})
		if err != nil {
			return judgePair{}, fmt.Errorf("harness: downstream fresh candidate judge case %q: %w", cc.Case.CaseKey, err)
		}
		ir, err := r.GenerateResponse(ctx, GenRequest{
			Seat: pack.SeatJudge, SeatCfg: judgeCfg, Input: cc.Input,
			InputHash: InputHashFor(cc.Input), Bundle: bundle,
			RunID: runID, Fresh: true,
		})
		if err != nil {
			return judgePair{}, fmt.Errorf("harness: downstream fresh incumbent judge case %q: %w", cc.Case.CaseKey, err)
		}
		return judgePair{cand: cr.Response, inc: ir.Response}, nil
	}
	cr, err := r.GenerateResponse(ctx, GenRequest{
		Seat: pack.SeatJudge, SeatCfg: judgeCfg, Input: cc.Input,
		InputHash: InputHashFor(cc.Input), Bundle: bundleForHash(incBundleHash(candBundle)),
		RunID: runID, Fresh: true,
	})
	if err != nil {
		return judgePair{}, fmt.Errorf("harness: downstream fresh candidate council judge case %q: %w", cc.Case.CaseKey, err)
	}
	ir, err := r.GenerateResponse(ctx, GenRequest{
		Seat: pack.SeatJudge, SeatCfg: judgeCfg, Input: cc.Input,
		InputHash: InputHashFor(cc.Input), Bundle: bundleForHash(incBundleHash(incBundle)),
		RunID: runID, Fresh: true,
	})
	if err != nil {
		return judgePair{}, fmt.Errorf("harness: downstream fresh incumbent council judge case %q: %w", cc.Case.CaseKey, err)
	}
	return judgePair{cand: cr.Response, inc: ir.Response}, nil
}

// coverDefault records issue coverage with grader A under the grader-call
// deadline.
func (r *Runner) coverDefault(ctx context.Context, in coverInput, views map[string]string, judge string, grader *grading.Grader, runID string) error {
	if grader == nil {
		return fmt.Errorf("harness: cover requires a grader")
	}
	callCtx, cancel := context.WithDeadline(ctx, time.Now().Add(r.GraderDeadline()))
	defer cancel()
	return r.grade.Cover(callCtx, in.Case, runID, in.CaseID, in.Label, in.Planted, views, judge, grader)
}

// storeDownstreamResult persists downstream_result_json on the candidate row.
func (r *Runner) storeDownstreamResult(runID string, spec CandidateSpec, candCfg pack.SeatConfig, res *DownstreamResult) error {
	cfgRaw, _ := json.Marshal(candCfg)
	if _, err := r.db.UpsertCandidate(runID, spec.Key, string(candCfg.Seat), string(cfgRaw), candCfg.Hash()); err != nil {
		return fmt.Errorf("harness: record candidate %q: %w", spec.Key, err)
	}
	row, err := r.db.GetCandidateByKey(runID, spec.Key)
	if err != nil {
		return err
	}
	sjson, _ := json.Marshal(res)
	s := string(sjson)
	if err := r.db.UpdateCandidateResults(row.ID, &store.CandidateResults{DownstreamResultJSON: &s}); err != nil {
		return fmt.Errorf("harness: store downstream result: %w", err)
	}
	return nil
}

// plantedFor parses the family's planted issues into grading issues.
func plantedFor(f *store.Family) []grading.Issue {
	var raw []struct {
		ID   string `json:"id"`
		Text string `json:"text"`
		Kind string `json:"kind"`
	}
	if err := json.Unmarshal([]byte(f.PlantedIssuesJSON), &raw); err != nil {
		return nil
	}
	var out []grading.Issue
	for _, p := range raw {
		if p.ID == "" {
			continue
		}
		out = append(out, grading.Issue{ID: p.ID, Text: p.Text, IsPlanted: true})
	}
	return out
}

// bundleResponsesFor resolves a saved bundle's views into response rows by
// matching stored responses on raw text. Bundles authored in YAML carry the
// view text; the same text generated through the cache helper produces the
// row the judge comparison grades.
func bundleResponsesFor(r *Runner, b *store.Bundle) (map[string]*store.Response, error) {
	var views map[string]schema.RenderedView
	if err := json.Unmarshal([]byte(b.ViewsJSON), &views); err != nil {
		return nil, fmt.Errorf("harness: decode bundle views: %w", err)
	}
	out := map[string]*store.Response{}
	for seat, v := range views {
		out[seat] = &store.Response{
			ID: "bundle-" + b.ID + "-" + seat, Seat: seat,
			RawText: bundleViewText(v),
		}
	}
	_ = r
	return out, nil
}

func bundleViewText(v schema.RenderedView) string {
	return strings.Join([]string{
		v.Role, v.Qualification, v.SuggestedReply, v.DecisiveInsight,
		v.TradeoffOrObjection, v.DependsOn, v.Fallback,
	}, "\n")
}

// renderedViewsOf renders council views to blind text for coverage.
func renderedViewsOf(council map[string]*store.Response) map[string]string {
	out := map[string]string{}
	for seat, resp := range council {
		out[seat] = blindOf(resp)
	}
	return out
}

func blindOf(resp *store.Response) string {
	if resp == nil {
		return ""
	}
	if resp.ParseOK && resp.ParsedJSON != nil {
		if v, ok := schema.ParseLenient[schema.View](*resp.ParsedJSON); ok {
			return render.ViewMarkdown(v, resp.Seat)
		}
		if j, ok := schema.ParseLenient[schema.Judge](*resp.ParsedJSON); ok {
			return render.JudgeMarkdown(j)
		}
	}
	return render.Raw(resp.RawText)
}

// bundleForHash wraps a council hash in a bundle row so the judge cache key
// distinguishes the candidate council from the incumbent council.
func bundleForHash(h string) *store.Bundle {
	return &store.Bundle{BundleHash: h, ViewsJSON: "{}"}
}

// incBundleHash hashes the council's underlying view text so identical
// councils share a judge cache key and differing seats miss.
func incBundleHash(council map[string]*store.Response) string {
	seats := []string{string(pack.SeatPossibility), string(pack.SeatPerspective), string(pack.SeatStressTester)}
	var parts []string
	for _, s := range seats {
		if resp, ok := council[s]; ok && resp != nil {
			parts = append(parts, s+"="+resp.RawText)
		}
	}
	raw, _ := json.Marshal(parts)
	return string(raw)
}
