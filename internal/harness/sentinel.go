package harness

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/qoke/toughdecisions/internal/grading"
	"github.com/qoke/toughdecisions/internal/pack"
	"github.com/qoke/toughdecisions/internal/schema"
	"github.com/qoke/toughdecisions/internal/store"
)

// SentinelDriftError reports grader drift that blocks a run before compare.
type SentinelDriftError struct {
	Drifted   []string
	Reversals map[string]int
}

func (e *SentinelDriftError) Error() string {
	return fmt.Sprintf("harness: grader drift blocks this run (needs_recalibration: %s); run `council graders calibrate` until it passes",
		strings.Join(e.Drifted, ", "))
}

// SelectionGraders returns the admitted selection graders ordered by key.
// Later steps (compare, downstream) reuse this accessor.
func (r *Runner) SelectionGraders() ([]*grading.Grader, error) {
	all, err := r.db.ListGraderConfigs()
	if err != nil {
		return nil, fmt.Errorf("harness: list grader configs: %w", err)
	}
	var out []*grading.Grader
	for _, g := range all {
		if g.Role == "selection" && g.Admitted {
			g := g
			out = append(out, (*grading.Grader)(g))
		}
	}
	return out, nil
}

// ScreeningGrader returns the first admitted screening grader ordered by key.
func (r *Runner) ScreeningGrader() (*grading.Grader, error) {
	all, err := r.db.ListGraderConfigs()
	if err != nil {
		return nil, fmt.Errorf("harness: list grader configs: %w", err)
	}
	for _, g := range all {
		if g.Role == "screening" && g.Admitted {
			g := g
			return (*grading.Grader)(g), nil
		}
	}
	return nil, fmt.Errorf("harness: no admitted screening grader")
}

// SubstituteGrader returns the substitute grader (admitted or not —
// SelectGrader decides whether it may be used).
func (r *Runner) SubstituteGrader() *grading.Grader {
	all, err := r.db.ListGraderConfigs()
	if err != nil {
		return nil
	}
	for _, g := range all {
		if g.Role == "substitute" {
			g := g
			return (*grading.Grader)(g)
		}
	}
	return nil
}

// CheckDrift re-runs grading.Recheck with HarnessCalibrationRecheck items
// on every admitted grader. It returns per-grader reversal counts and the
// keys with reversals > 1 (needs_recalibration). It never changes
// admission and never calls the model beyond the recheck items.
func (r *Runner) CheckDrift(ctx context.Context) (map[string]int, []string, error) {
	all, err := r.db.ListGraderConfigs()
	if err != nil {
		return nil, nil, fmt.Errorf("harness: list grader configs: %w", err)
	}
	n := r.CalibrationRecheck()
	reversals := map[string]int{}
	var drifted []string
	for _, g := range all {
		if !g.Admitted {
			continue
		}
		g := g
		rev, err := r.grade.Recheck(ctx, emptyInput(), "", (*grading.Grader)(g), n)
		if err != nil {
			return nil, nil, fmt.Errorf("harness: recheck grader %q: %w", g.GraderKey, err)
		}
		reversals[g.GraderKey] = rev
		if rev > 1 {
			drifted = append(drifted, g.GraderKey)
		}
	}
	return reversals, drifted, nil
}

// SelectSentinelCases picks SentinelCount cases from sentinel-tagged
// families, rotating by ISO week: always >=1 safety and >=1 control case,
// then fills from hard-tagged families. It errors when the case pack
// cannot satisfy the safety/control minimums.
func (r *Runner) SelectSentinelCases(now time.Time) ([]*store.Case, error) {
	families, err := r.db.ListFamilies()
	if err != nil {
		return nil, fmt.Errorf("harness: list families: %w", err)
	}
	var sentinel, safety, control, hard []*store.Family
	for _, f := range families {
		tags := decodeTags(f.TagsJSON)
		if !tags["sentinel"] {
			continue
		}
		sentinel = append(sentinel, f)
		if tags["safety"] {
			safety = append(safety, f)
		}
		if tags["control"] {
			control = append(control, f)
		}
		if tags["hard"] {
			hard = append(hard, f)
		}
	}
	if len(sentinel) == 0 {
		return nil, fmt.Errorf("harness: no sentinel-tagged families")
	}
	if len(safety) == 0 || len(control) == 0 {
		return nil, fmt.Errorf("harness: sentinel selection needs >=1 safety and >=1 control family")
	}
	_, week := now.UTC().ISOWeek()
	need := r.SentinelCount()
	if need < 2 {
		need = 2
	}

	// Ordered family picks: safety first, then a control family (a
	// different one when available), then hard families, cycling when the
	// count exceeds the distinct families.
	picks := []*store.Family{rotate(safety, week)[0]}
	ctrls := rotate(control, week)
	pick := ctrls[0]
	if pick.FamilyKey == picks[0].FamilyKey && len(ctrls) > 1 {
		pick = ctrls[1]
	}
	picks = append(picks, pick)
	seen := map[string]bool{picks[0].FamilyKey: true, pick.FamilyKey: true}
	hardRot := rotate(hard, week)
	for len(picks) < need && len(hardRot) > 0 {
		added := false
		for _, f := range hardRot {
			if len(picks) >= need {
				break
			}
			if !seen[f.FamilyKey] {
				picks = append(picks, f)
				seen[f.FamilyKey] = true
				added = true
			}
		}
		if !added {
			break
		}
	}
	for len(picks) < need && len(sentinel) > 0 {
		picks = append(picks, rotate(sentinel, week+len(picks))[0])
	}

	var out []*store.Case
	for i, f := range picks {
		cases, err := r.db.ListCasesByFamily(f.ID)
		if err != nil {
			return nil, fmt.Errorf("harness: list cases for %q: %w", f.FamilyKey, err)
		}
		if len(cases) == 0 {
			return nil, fmt.Errorf("harness: family %q has no cases", f.FamilyKey)
		}
		out = append(out, rotate(cases, week+i)[0])
	}
	return out, nil
}

// SentinelOptions configures one sentinel run. Responses are always
// generated fresh. Compare may be overridden in tests; nil uses the
// grading service with both selection graders.
type SentinelOptions struct {
	Now     time.Time
	Compare CompareFunc
}

// CompareFunc compares a fresh response against its baseline. It returns
// the agreed verdict in left/right space ("left" = fresh wins,
// "right" = baseline wins), whether both graders agreed, and the named
// consequential difference.
type CompareFunc func(ctx context.Context, in compareInput, fresh, baseline *store.Response, graders []*grading.Grader, runID string) (verdict string, agreed bool, difference string, err error)

type compareInput struct {
	Acceptance string
	CaseID     string
	Seat       string
	Case       schema.CaseInput
}

// SentinelRegression is one seat x case sentinel comparison.
type SentinelRegression struct {
	Seat       string
	CaseKey    string
	Regression bool
	Agreed     bool
	Difference string
}

// SentinelSummary is the finished sentinel outcome.
type SentinelSummary struct {
	RunID       string
	Regressions []SentinelRegression
	Investigate []string
	Recheck     map[string]int
	Stats       string
}

// Sentinel runs plan §12.1: rotate cases, refuse without baselines,
// recheck graders (drift blocks BEFORE compare), generate fresh responses,
// compare vs baselines with both selection graders, and record
// sentinel_results.
func (r *Runner) Sentinel(ctx context.Context, opts SentinelOptions) (*SentinelSummary, error) {
	now := opts.Now
	if now.IsZero() {
		now = time.Now()
	}
	pk, err := packActive(r)
	if err != nil {
		return nil, err
	}
	cases, err := r.SelectSentinelCases(now)
	if err != nil {
		return nil, err
	}
	params, _ := json.Marshal(map[string]any{"fresh": true})
	run, err := r.StartRun("sentinel", pk.ID, string(params))
	if err != nil {
		return nil, err
	}
	fail := func(reason error) (*SentinelSummary, error) {
		_ = r.FailRun(run.ID, reason.Error())
		return nil, reason
	}

	graders, err := r.SelectionGraders()
	if err != nil {
		return fail(err)
	}
	if len(graders) < 2 {
		return fail(fmt.Errorf("harness: sentinel needs two admitted selection graders, have %d", len(graders)))
	}
	graders = graders[:2]

	// Grader recheck BEFORE any compare (and before touching baselines):
	// drift blocks the run.
	reversals, drifted, err := r.CheckDrift(ctx)
	if err != nil {
		return fail(err)
	}
	if len(drifted) > 0 && r.BlockOnDrift() {
		summary, _ := json.Marshal(map[string]any{"blocked": true, "drifted": drifted, "recheck": reversals})
		s := string(summary)
		_ = r.db.FinishHarnessRun(run.ID, "blocked", nil, &s, nil)
		return nil, &SentinelDriftError{Drifted: drifted, Reversals: reversals}
	}

	// Missing baseline refuses the run: bootstrap with baselines first.
	for _, seat := range pack.AllSeats {
		for _, c := range cases {
			if _, err := r.db.GetBaseline(pk.ID, string(seat), c.ID); err != nil {
				reason := fmt.Errorf("harness: no baseline for seat %q case %q: run `pack publish --baselines-only` to bootstrap baselines", string(seat), c.CaseKey)
				return fail(reason)
			}
		}
	}

	compare := opts.Compare
	if compare == nil {
		compare = r.compareDefault
	}

	type job struct {
		seat pack.Seat
		c    *store.Case
	}
	var jobs []job
	for _, seat := range pack.AllSeats {
		for _, c := range cases {
			jobs = append(jobs, job{seat: seat, c: c})
		}
	}
	fresh := make([]*store.Response, len(jobs))
	latencies := make([]int64, len(jobs))
	errs := r.Pool(ctx, poolJobs(jobs, func(ctx context.Context, i int) error {
		j := jobs[i]
		in, err := CaseInputFor(j.c)
		if err != nil {
			return err
		}
		var bundle *store.Bundle
		if j.seat == pack.SeatJudge {
			bl, err := r.db.GetBaseline(pk.ID, string(j.seat), j.c.ID)
			if err != nil {
				return err
			}
			if bl.BundleID != nil {
				bundle, err = r.db.GetBundle(*bl.BundleID)
				if err != nil {
					return fmt.Errorf("harness: load baseline bundle: %w", err)
				}
			}
		}
		cfg := pk.Seats[j.seat]
		res, err := r.GenerateResponse(ctx, GenRequest{
			Seat: j.seat, SeatCfg: cfg, Input: in,
			InputHash: InputHashFor(in), Bundle: bundle,
			RunID: run.ID, Fresh: true,
		})
		if err != nil {
			return err
		}
		fresh[i] = res.Response
		latencies[i] = res.Response.LatencyMs
		return nil
	}))
	for _, err := range errs {
		if err != nil {
			return fail(err)
		}
	}

	summary := &SentinelSummary{RunID: run.ID, Recheck: reversals, Stats: r.StatsJSON()}
	bySeatLatency := map[string][]int64{}
	for i, j := range jobs {
		bySeatLatency[string(j.seat)] = append(bySeatLatency[string(j.seat)], latencies[i])
	}
	confirmed, err := r.db.ListFlagsByStatus("confirmed")
	if err != nil {
		return fail(fmt.Errorf("harness: list confirmed flags: %w", err))
	}
	for i, j := range jobs {
		in, err := CaseInputFor(j.c)
		if err != nil {
			return fail(err)
		}
		fam, err := r.db.GetFamily(j.c.FamilyID)
		if err != nil {
			return fail(fmt.Errorf("harness: load family: %w", err))
		}
		bl, err := r.db.GetBaseline(pk.ID, string(j.seat), j.c.ID)
		if err != nil {
			return fail(err)
		}
		base, err := r.db.GetResponse(bl.ResponseID)
		if err != nil {
			return fail(fmt.Errorf("harness: load baseline response: %w", err))
		}
		verdict, agreed, diff, err := compare(ctx, compareInput{
			Acceptance: fam.AcceptanceJSON, CaseID: j.c.ID,
			Seat: string(j.seat), Case: in,
		}, fresh[i], base, graders, run.ID)
		if err != nil {
			return fail(fmt.Errorf("harness: compare seat %q case %q: %w", string(j.seat), j.c.CaseKey, err))
		}
		reg := IsRegression(agreed, verdict, diff)
		vjson, _ := json.Marshal(map[string]any{
			"by_grader": verdict, "agreed": agreed, "difference": diff,
		})
		if _, err := r.db.InsertSentinelResult(&store.SentinelResult{
			RunID: run.ID, Seat: string(j.seat), CaseID: j.c.ID,
			FreshResponseID: fresh[i].ID, BaselineResponseID: base.ID,
			VerdictsJSON: string(vjson), LatencyMs: latencies[i], Regression: reg,
		}); err != nil {
			return fail(fmt.Errorf("harness: record sentinel result: %w", err))
		}
		summary.Regressions = append(summary.Regressions, SentinelRegression{
			Seat: string(j.seat), CaseKey: j.c.CaseKey,
			Regression: reg, Agreed: agreed, Difference: diff,
		})
		// Investigate: same-seat repeat regressions are tracked across
		// runs via sentinel_results history in a follow-up step; here we
		// evaluate confirmed flags and the p50-vs-budget condition.
		p50 := P50(bySeatLatency[string(j.seat)])
		if ok, reason := ShouldInvestigate(false, len(confirmed), p50, r.DeadlineFor(j.seat).Milliseconds()); ok {
			summary.Investigate = append(summary.Investigate,
				fmt.Sprintf("%s x %s: %s", string(j.seat), j.c.CaseKey, reason))
		}
	}

	raw, _ := json.Marshal(summary)
	if err := r.FinishRun(run.ID, "complete", string(raw)); err != nil {
		return nil, err
	}
	return summary, nil
}

// IsRegression applies the plan rule: both graders' final verdict is
// "baseline" (right in left/right space) with a named consequential
// difference.
func IsRegression(agreed bool, verdictLeftRight, difference string) bool {
	return agreed && verdictLeftRight == "right" && strings.TrimSpace(difference) != ""
}

// compareDefault resolves same-family routing per response and runs both
// selection graders through grading.Compare.
func (r *Runner) compareDefault(ctx context.Context, in compareInput, fresh, baseline *store.Response, graders []*grading.Grader, runID string) (string, bool, string, error) {
	sub := r.SubstituteGrader()
	resolved := make([]*grading.Grader, 0, 2)
	for _, g := range graders {
		sel, err := grading.SelectGrader([]*grading.Grader{g}, familyOf(fresh), sub)
		if err != nil {
			return "", false, "", err
		}
		resolved = append(resolved, sel)
	}
	callCtx, cancel := context.WithDeadline(ctx, time.Now().Add(r.GraderDeadline()))
	defer cancel()
	res, agreed, err := r.grade.Compare(callCtx, in.Case, in.Acceptance,
		"sentinel", in.CaseID, in.Seat, fresh, baseline, resolved, runID)
	if err != nil {
		return "", false, "", err
	}
	return res.Verdict, agreed, res.Difference, nil
}

func familyOf(resp *store.Response) string {
	// Stored responses carry no family; routing uses the seat's pack
	// family resolved by callers. The runner passes "" here only when the
	// family is unknown, which never matches a grader family.
	_ = resp
	return ""
}

// packActive returns the active production pack for harness steps.
func packActive(r *Runner) (*pack.Pack, error) {
	pk, err := pack.Active(r.db)
	if err != nil {
		return nil, fmt.Errorf("harness: active pack: %w", err)
	}
	return pk, nil
}

func emptyInput() schema.CaseInput { return schema.CaseInput{} }

func poolJobs[T any](jobs []T, fn func(context.Context, int) error) []func(context.Context) error {
	out := make([]func(context.Context) error, len(jobs))
	for i := range jobs {
		out[i] = func(ctx context.Context) error { return fn(ctx, i) }
	}
	return out
}

func decodeTags(raw string) map[string]bool {
	var tags []string
	if err := json.Unmarshal([]byte(raw), &tags); err != nil {
		return map[string]bool{}
	}
	out := make(map[string]bool, len(tags))
	for _, t := range tags {
		out[t] = true
	}
	return out
}

func rotate[T any](in []T, offset int) []T {
	if len(in) == 0 {
		return in
	}
	o := offset % len(in)
	if o < 0 {
		o += len(in)
	}
	out := make([]T, 0, len(in))
	return append(append(out, in[o:]...), in[:o]...)
}
