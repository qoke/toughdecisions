package harness

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"github.com/qoke/toughdecisions/internal/grading"
	"github.com/qoke/toughdecisions/internal/pack"
	"github.com/qoke/toughdecisions/internal/schema"
	"github.com/qoke/toughdecisions/internal/store"
)

// CompareOptions configures one compare run for a single candidate.
// Compare overrides the pairwise step in tests; nil uses both selection
// graders with purpose "compare".
type CompareOptions struct {
	Spec    CandidateSpec
	RunID   string
	Fresh   bool
	Compare CompareFunc
}

// CompareCase is one selection case (view seats) or one selection bundle
// (judge seat) under comparison.
type CompareCase struct {
	Case     *store.Case
	Family   *store.Family
	Input    schema.CaseInput
	Bundle   *store.Bundle // judge seat only
	Hard     bool
	Hardest  bool
	Priority bool
}

// CaseVerdict is the per-case compare outcome. Verdict is in left/right
// space: "left" means the candidate wins, "right" the incumbent.
type CaseVerdict struct {
	CaseKey       string
	FamilyKey     string
	Hard          bool
	Priority      bool
	Agreed        bool
	Verdict       string
	Difference    string
	CandMean      float64
	IncMean       float64
	CandLatencyMs int64
	IncLatencyMs  int64
	CandTimeout   bool
	FreshVerdict  string
	FreshAgreed   bool
	ReRanFresh    bool
}

// CompareResult is the persisted compare outcome. Reporting and promotion
// read this type; every number is per-run, never a cross-run average.
type CompareResult struct {
	RunID          string
	CandidateKey   string
	Seat           string
	Cases          []CaseVerdict
	HardWins       int
	HardLosses     int
	HardOther      int
	PriorityLosses int
	P50Cand        int64
	P95Cand        int64
	P50Inc         int64
	P95Inc         int64
	Timeouts       int
	MeanCostCand   float64
	MeanCostInc    float64
	Fragile        bool
	RoleMeans      []float64
	RoleMins       []int
	OpenFlags      int
	ConfirmedFlags int
}

// SelectCompareCases returns the comparison scope for a seat: all cases in
// selection-split families for view seats, all selection bundles (one entry
// per bundle) for the judge seat.
func (r *Runner) SelectCompareCases(seat pack.Seat) ([]*CompareCase, error) {
	families, err := r.db.ListFamilies()
	if err != nil {
		return nil, fmt.Errorf("harness: list families: %w", err)
	}
	var sel []*store.Family
	for _, f := range families {
		if f.Split == "selection" {
			sel = append(sel, f)
		}
	}
	if len(sel) == 0 {
		return nil, fmt.Errorf("harness: no selection families")
	}
	var out []*CompareCase
	for _, f := range sel {
		tags := decodeTags(f.TagsJSON)
		cases, err := r.db.ListCasesByFamily(f.ID)
		if err != nil {
			return nil, fmt.Errorf("harness: list cases for %q: %w", f.FamilyKey, err)
		}
		for _, c := range cases {
			in, err := CaseInputFor(c)
			if err != nil {
				return nil, err
			}
			cc := &CompareCase{
				Case: c, Family: f, Input: in,
				Hard: tags["hard"], Hardest: tags["hardest"], Priority: tags["priority"],
			}
			if seat != pack.SeatJudge {
				out = append(out, cc)
				continue
			}
			bundles, err := r.db.ListBundlesByCase(c.ID)
			if err != nil {
				return nil, fmt.Errorf("harness: list bundles for %q: %w", c.CaseKey, err)
			}
			for _, b := range bundles {
				b := b
				bc := *cc
				bc.Bundle = b
				out = append(out, &bc)
			}
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("harness: no compare cases for seat %q", string(seat))
	}
	return out, nil
}

// FragilityPicks returns up to two case indexes for the --fresh fragility
// re-run: hardest-tagged cases first (plan §12.4), falling back to the two
// lowest incumbent means when no case is tagged hardest.
func FragilityPicks(hardest []bool, incMeans []float64) []int {
	n := len(hardest)
	if len(incMeans) < n {
		n = len(incMeans)
	}
	var picks []int
	for i := 0; i < n; i++ {
		if hardest[i] {
			picks = append(picks, i)
		}
	}
	if len(picks) == 0 {
		ord := make([]int, n)
		for i := range ord {
			ord[i] = i
		}
		sort.Slice(ord, func(a, b int) bool { return incMeans[ord[a]] < incMeans[ord[b]] })
		picks = append(picks, ord...)
	}
	if len(picks) > 2 {
		picks = picks[:2]
	}
	return picks
}

// Compare runs plan §12.4 for one candidate: absolute grades by both
// selection graders, pairwise candidate vs incumbent with both graders, the
// --fresh fragility re-run on the two hardest cases, and per-run latency
// percentiles. It stores compare_result_json on the candidate row.
func (r *Runner) Compare(ctx context.Context, opts CompareOptions) (*CompareResult, error) {
	seat := pack.Seat(opts.Spec.Seat)
	if !seat.Valid() {
		return nil, fmt.Errorf("harness: unknown seat %q", opts.Spec.Seat)
	}
	pk, err := packActive(r)
	if err != nil {
		return nil, err
	}
	cases, err := r.SelectCompareCases(seat)
	if err != nil {
		return nil, err
	}
	runID := opts.RunID
	if runID == "" {
		params, _ := json.Marshal(map[string]any{"fresh": opts.Fresh})
		run, err := r.StartRun("compare", pk.ID, string(params))
		if err != nil {
			return nil, err
		}
		runID = run.ID
	}
	fail := func(reason error) (*CompareResult, error) {
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
		cmp = r.comparePurpose("compare")
	}
	candCfg := seatConfigFor(opts.Spec, seat, "")
	incCfg, ok := pk.Seats[seat]
	if !ok {
		return fail(fmt.Errorf("harness: seat %q not in active pack", string(seat)))
	}
	pairs, err := r.genComparePairs(ctx, runID, seat, candCfg, incCfg, cases, opts.Fresh)
	if err != nil {
		return fail(err)
	}
	candGrades, err := r.gradeComparePairs(ctx, runID, cases, pairs, graders)
	if err != nil {
		return fail(err)
	}
	res := &CompareResult{RunID: runID, CandidateKey: opts.Spec.Key, Seat: string(seat)}
	if err := r.verdictCompareCases(ctx, runID, res, cases, pairs, cmp, graders, candCfg, incCfg); err != nil {
		return fail(err)
	}
	if err := r.rerunFragility(ctx, runID, res, seat, candCfg, incCfg, cases, pairs, cmp, graders); err != nil {
		return fail(err)
	}
	if err := r.summarizeCompare(runID, res, cases, pairs, candGrades); err != nil {
		return fail(err)
	}
	if err := r.storeCompareResult(runID, opts.Spec, candCfg, res); err != nil {
		return fail(err)
	}
	raw, _ := json.Marshal(res)
	if err := r.FinishRun(runID, "complete", string(raw)); err != nil {
		return nil, err
	}
	return res, nil
}

// compareDriftGate rechecks graders before compare; drift blocks the run.
func (r *Runner) compareDriftGate(ctx context.Context, runID string) (map[string]int, []string, error) {
	reversals, drifted, err := r.CheckDrift(ctx)
	if err != nil {
		return nil, nil, err
	}
	if len(drifted) > 0 && r.BlockOnDrift() {
		summary, _ := json.Marshal(map[string]any{"blocked": true, "drifted": drifted, "recheck": reversals})
		s := string(summary)
		_ = r.db.FinishHarnessRun(runID, "blocked", nil, &s, nil)
		return nil, nil, &SentinelDriftError{Drifted: drifted, Reversals: reversals}
	}
	return reversals, drifted, nil
}

// selectionPair returns the first two admitted selection graders.
func (r *Runner) selectionPair() ([]*grading.Grader, error) {
	graders, err := r.SelectionGraders()
	if err != nil {
		return nil, err
	}
	if len(graders) < 2 {
		return nil, fmt.Errorf("harness: compare needs two admitted selection graders, have %d", len(graders))
	}
	return graders[:2], nil
}

// comparePurpose adapts pairwiseBoth to the CompareFunc shape so compare
// and downstream share one default with different purposes.
func (r *Runner) comparePurpose(purpose string) CompareFunc {
	return func(ctx context.Context, in compareInput, left, right *store.Response, graders []*grading.Grader, runID string) (string, bool, string, error) {
		return r.pairwiseBothFamilies(ctx, purpose, in.CaseID, in.Seat, in.Case, in.Acceptance, in.ResponseFamilies, left, right, graders, runID)
	}
}

// pairwiseBothFamilies resolves R-12 self-grading routing and runs
// grading Compare with both selection graders under the grader-call
// deadline. families holds the model families behind the two responses
// under comparison (see compareInput.ResponseFamilies): each same-family
// grader yields the admitted substitute, never a sibling, and an
// unadmitted/absent substitute is a hard error.
func (r *Runner) pairwiseBothFamilies(ctx context.Context, purpose, caseID, seat string, in schema.CaseInput, acceptance string, families []string, left, right *store.Response, graders []*grading.Grader, runID string) (string, bool, string, error) {
	resolved, err := r.resolveGraders(graders, families)
	if err != nil {
		return "", false, "", err
	}
	callCtx, cancel := context.WithDeadline(ctx, time.Now().Add(r.GraderDeadline()))
	defer cancel()
	res, agreed, err := r.grade.Compare(callCtx, in, acceptance, purpose, caseID, seat, left, right, resolved, runID)
	if err != nil {
		return "", false, "", err
	}
	return res.Verdict, agreed, res.Difference, nil
}

type comparePair struct {
	cand *store.Response
	inc  *store.Response
}

// genComparePairs generates candidate and incumbent responses per case
// through the Step 8 cache helper (repetition 0 unless fresh).
func (r *Runner) genComparePairs(ctx context.Context, runID string, seat pack.Seat, candCfg, incCfg pack.SeatConfig, cases []*CompareCase, fresh bool) ([]comparePair, error) {
	pairs := make([]comparePair, len(cases))
	errs := r.Pool(ctx, poolJobs(cases, func(ctx context.Context, i int) error {
		cc := cases[i]
		cr, err := r.GenerateResponse(ctx, GenRequest{
			Seat: seat, SeatCfg: candCfg, Input: cc.Input,
			InputHash: InputHashFor(cc.Input), Bundle: cc.Bundle,
			RunID: runID, Fresh: fresh, ResponseFamily: candCfg.Family,
		})
		if err != nil {
			return fmt.Errorf("harness: candidate case %q: %w", cc.Case.CaseKey, err)
		}
		ir, err := r.GenerateResponse(ctx, GenRequest{
			Seat: seat, SeatCfg: incCfg, Input: cc.Input,
			InputHash: InputHashFor(cc.Input), Bundle: cc.Bundle,
			RunID: runID, Fresh: fresh, ResponseFamily: incCfg.Family,
		})
		if err != nil {
			return fmt.Errorf("harness: incumbent case %q: %w", cc.Case.CaseKey, err)
		}
		pairs[i] = comparePair{cand: cr.Response, inc: ir.Response}
		return nil
	}))
	for _, err := range errs {
		if err != nil {
			return nil, err
		}
	}
	return pairs, nil
}

// gradeComparePairs grades every candidate and incumbent response with both
// selection graders, sequentially so scripted tests stay deterministic.
func (r *Runner) gradeComparePairs(ctx context.Context, runID string, cases []*CompareCase, pairs []comparePair, graders []*grading.Grader) ([][]*store.Grade, error) {
	out := make([][]*store.Grade, len(cases))
	for i, cc := range cases {
		out[i] = make([]*store.Grade, len(graders))
		for gi, g := range graders {
			cg, err := r.gradeWith(ctx, cc.Input, cc.Family.AcceptanceJSON, pairs[i].cand, g, runID)
			if err != nil {
				return nil, fmt.Errorf("harness: grade candidate case %q: %w", cc.Case.CaseKey, err)
			}
			if _, err := r.gradeWith(ctx, cc.Input, cc.Family.AcceptanceJSON, pairs[i].inc, g, runID); err != nil {
				return nil, fmt.Errorf("harness: grade incumbent case %q: %w", cc.Case.CaseKey, err)
			}
			out[i][gi] = cg
		}
	}
	return out, nil
}

// verdictCompareCases runs the pairwise step per case and aggregates the
// agreed hard/priority tallies.
func (r *Runner) verdictCompareCases(ctx context.Context, runID string, res *CompareResult, cases []*CompareCase, pairs []comparePair, cmp CompareFunc, graders []*grading.Grader, candCfg, incCfg pack.SeatConfig) error {
	for i, cc := range cases {
		verdict, agreed, diff, err := cmp(ctx, compareInput{
			Acceptance: cc.Family.AcceptanceJSON, CaseID: cc.Case.ID,
			Seat: string(pack.Seat(res.Seat)), Case: cc.Input,
			ResponseFamilies: responseFamilies(candCfg.Family, incCfg.Family),
		}, pairs[i].cand, pairs[i].inc, graders, runID)
		if err != nil {
			return fmt.Errorf("harness: compare case %q: %w", cc.Case.CaseKey, err)
		}
		res.Cases = append(res.Cases, CaseVerdict{
			CaseKey: cc.Case.CaseKey, FamilyKey: cc.Family.FamilyKey,
			Hard: cc.Hard, Priority: cc.Priority,
			Agreed: agreed, Verdict: verdict, Difference: diff,
		})
	}
	r.tallyCompare(res, cases)
	return nil
}

// tallyCompare counts agreed hard wins/losses/others and agreed priority
// losses. "left" is a candidate win, "right" an incumbent win.
func (r *Runner) tallyCompare(res *CompareResult, cases []*CompareCase) {
	for i, cc := range cases {
		v := res.Cases[i]
		if !v.Agreed {
			if cc.Hard {
				res.HardOther++
			}
			continue
		}
		switch v.Verdict {
		case "left":
			if cc.Hard {
				res.HardWins++
			}
		case "right":
			if cc.Hard {
				res.HardLosses++
			}
			if cc.Priority {
				res.PriorityLosses++
			}
		default:
			if cc.Hard {
				res.HardOther++
			}
		}
	}
}

// rerunFragility regenerates the fragility picks with --fresh for both
// sides and compares again; any flipped final verdict marks fragile.
func (r *Runner) rerunFragility(ctx context.Context, runID string, res *CompareResult, seat pack.Seat, candCfg, incCfg pack.SeatConfig, cases []*CompareCase, pairs []comparePair, cmp CompareFunc, graders []*grading.Grader) error {
	hardest := make([]bool, len(cases))
	incMeans := make([]float64, len(cases))
	for i := range cases {
		hardest[i] = cases[i].Hardest
		incMeans[i] = res.Cases[i].IncMean
	}
	for _, idx := range FragilityPicks(hardest, incMeans) {
		cc := cases[idx]
		cr, err := r.GenerateResponse(ctx, GenRequest{
			Seat: seat, SeatCfg: candCfg, Input: cc.Input,
			InputHash: InputHashFor(cc.Input), Bundle: cc.Bundle,
			RunID: runID, Fresh: true, ResponseFamily: candCfg.Family,
		})
		if err != nil {
			return fmt.Errorf("harness: fragility candidate case %q: %w", cc.Case.CaseKey, err)
		}
		ir, err := r.GenerateResponse(ctx, GenRequest{
			Seat: seat, SeatCfg: incCfg, Input: cc.Input,
			InputHash: InputHashFor(cc.Input), Bundle: cc.Bundle,
			RunID: runID, Fresh: true, ResponseFamily: incCfg.Family,
		})
		if err != nil {
			return fmt.Errorf("harness: fragility incumbent case %q: %w", cc.Case.CaseKey, err)
		}
		verdict, agreed, _, err := cmp(ctx, compareInput{
			Acceptance: cc.Family.AcceptanceJSON, CaseID: cc.Case.ID,
			Seat: string(seat), Case: cc.Input,
			ResponseFamilies: responseFamilies(candCfg.Family, incCfg.Family),
		}, cr.Response, ir.Response, graders, runID)
		if err != nil {
			return fmt.Errorf("harness: fragility compare case %q: %w", cc.Case.CaseKey, err)
		}
		res.Cases[idx].FreshVerdict = verdict
		res.Cases[idx].FreshAgreed = agreed
		res.Cases[idx].ReRanFresh = true
		orig := res.Cases[idx]
		if verdict != orig.Verdict || agreed != orig.Agreed {
			res.Fragile = true
		}
		_ = pairs
	}
	return nil
}

// summarizeCompare fills per-run latency percentiles, costs, timeouts,
// role-execution profile, and this-run flag counts. Nothing here consults
// cross-run history.
func (r *Runner) summarizeCompare(runID string, res *CompareResult, cases []*CompareCase, pairs []comparePair, candGrades [][]*store.Grade) error {
	var candLat, incLat []int64
	var costCand, costInc float64
	roleSums := []float64{0, 0}
	roleMins := []int{4, 4}
	roleCounts := []int{0, 0}
	for i := range cases {
		candLat = append(candLat, pairs[i].cand.LatencyMs)
		incLat = append(incLat, pairs[i].inc.LatencyMs)
		costCand += costOf(pairs[i].cand)
		costInc += costOf(pairs[i].inc)
		if pairs[i].cand.TimedOut {
			res.Timeouts++
		}
		res.Cases[i].CandLatencyMs = pairs[i].cand.LatencyMs
		res.Cases[i].IncLatencyMs = pairs[i].inc.LatencyMs
		res.Cases[i].CandTimeout = pairs[i].cand.TimedOut
		res.Cases[i].CandMean = meanTotal(candGrades[i])
		for gi, g := range candGrades[i] {
			s := scoreOf(g, "role_execution")
			roleSums[gi] += float64(s)
			roleCounts[gi]++
			if s < roleMins[gi] {
				roleMins[gi] = s
			}
		}
	}
	n := float64(len(cases))
	if n > 0 {
		res.MeanCostCand = costCand / n
		res.MeanCostInc = costInc / n
	}
	res.P50Cand, res.P95Cand = P50(candLat), P95(candLat)
	res.P50Inc, res.P95Inc = P50(incLat), P95(incLat)
	for gi := range roleSums {
		if roleCounts[gi] > 0 {
			res.RoleMeans = append(res.RoleMeans, roleSums[gi]/float64(roleCounts[gi]))
			res.RoleMins = append(res.RoleMins, roleMins[gi])
		}
	}
	ids := map[string]bool{}
	for _, p := range pairs {
		ids[p.cand.ID] = true
	}
	open, err := r.countRunFlags("open", runID, ids)
	if err != nil {
		return err
	}
	confirmed, err := r.countRunFlags("confirmed", runID, ids)
	if err != nil {
		return err
	}
	res.OpenFlags, res.ConfirmedFlags = open, confirmed
	return nil
}

// storeCompareResult persists compare_result_json on the candidate row.
func (r *Runner) storeCompareResult(runID string, spec CandidateSpec, candCfg pack.SeatConfig, res *CompareResult) error {
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
	if err := r.db.UpdateCandidateResults(row.ID, &store.CandidateResults{CompareResultJSON: &s}); err != nil {
		return fmt.Errorf("harness: store compare result: %w", err)
	}
	return nil
}

// countRunFlags counts flags with the given status filed this run against
// the given candidate response ids.
func (r *Runner) countRunFlags(status, runID string, ids map[string]bool) (int, error) {
	flags, err := r.db.ListFlagsByStatus(status)
	if err != nil {
		return 0, fmt.Errorf("harness: list %s flags: %w", status, err)
	}
	n := 0
	for _, f := range flags {
		if f.RunID == nil || *f.RunID != runID {
			continue
		}
		if ids[f.ResponseID] {
			n++
		}
	}
	return n, nil
}

func costOf(resp *store.Response) float64 {
	if resp == nil || resp.CostUSD == nil {
		return 0
	}
	return *resp.CostUSD
}

func meanTotal(grades []*store.Grade) float64 {
	if len(grades) == 0 {
		return 0
	}
	sum := 0.0
	for _, g := range grades {
		sum += totalOf(g)
	}
	return sum / float64(len(grades))
}
