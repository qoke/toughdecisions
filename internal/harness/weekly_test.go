package harness

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/qoke/toughdecisions/internal/gateway"
	"github.com/qoke/toughdecisions/internal/grading"
	"github.com/qoke/toughdecisions/internal/store"
)

// weeklyCall records one injected-compare call with its run id so tests can
// prove phase ordering after the fact.
type weeklyCall struct {
	run string
	n   int
}

type weeklyProbe struct {
	mu    sync.Mutex
	seq   int
	calls []weeklyCall
	loads int
}

func (p *weeklyProbe) compare() CompareFunc {
	return func(_ context.Context, _ compareInput, _, _ *store.Response, _ []*grading.Grader, runID string) (string, bool, string, error) {
		p.mu.Lock()
		defer p.mu.Unlock()
		p.calls = append(p.calls, weeklyCall{run: runID, n: p.seq})
		p.seq++
		return "left", true, "candidate clearer", nil
	}
}

func (p *weeklyProbe) runs(except string) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	n := 0
	for _, c := range p.calls {
		if c.run != except {
			n++
		}
	}
	return n
}

// maxSeq returns the highest sequence number for runID, or -1 when absent.
func (p *weeklyProbe) maxSeq(runID string) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := -1
	for _, c := range p.calls {
		if c.run == runID && c.n > out {
			out = c.n
		}
	}
	return out
}

func (p *weeklyProbe) minSeq(runID string) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := -1
	for _, c := range p.calls {
		if c.run == runID && (out == -1 || c.n < out) {
			out = c.n
		}
	}
	return out
}

// weeklyEnv points candidates and reports at temp dirs; it must run before
// setupHarness because config.Load reads the environment.
func weeklyEnv(t *testing.T, candidates string) string {
	t.Helper()
	t.Setenv("COUNCIL_CANDIDATES_FILE", candidates)
	reports := filepath.Join(t.TempDir(), "reports")
	t.Setenv("COUNCIL_REPORTS_DIR", reports)
	return reports
}

// seedWeeklySentinel seeds two sentinel families (safety + control) with
// cases, baselines for every seat x case, two admitted selection graders,
// and one matching calibration item (no drift).
func seedWeeklySentinel(t *testing.T, fx *hFixture) {
	t.Helper()
	seedHFamily(t, fx.db, "FS", "selection", []string{"sentinel", "safety"}, []string{"possibility"})
	seedHFamily(t, fx.db, "FC", "selection", []string{"sentinel", "control"}, []string{"possibility"})
	var calCase *store.Case
	for _, key := range []string{"FS", "FC"} {
		fam, err := fx.db.GetFamilyByKey(key)
		if err != nil {
			t.Fatalf("GetFamilyByKey %s: %v", key, err)
		}
		c := seedHCase(t, fx.db, fam, key+"-base")
		if calCase == nil {
			calCase = c
		}
		for _, seat := range []string{"possibility", "perspective", "stress_tester", "judge"} {
			br, err := fx.db.InsertResponse(&store.Response{
				CacheKey: "wbase-" + key + "-" + seat,
				Seat:     seat, ConfigHash: "ch", PromptPackHash: "pp",
				InputHash: "ih", Origin: "harness", ModelRequested: "m",
				RawText: "baseline", WordCount: 1,
			})
			if err != nil {
				t.Fatalf("InsertResponse baseline: %v", err)
			}
			if _, err := fx.db.UpsertBaseline(fx.pk.ID, seat, c.ID, nil, br.ID); err != nil {
				t.Fatalf("UpsertBaseline: %v", err)
			}
		}
	}
	insertHGrader(t, fx.db, "sa", "gs1", "openai", "selection", true)
	insertHGrader(t, fx.db, "sb", "gs2", "anthropic", "selection", true)
	seedHCalibration(t, fx.db, calCase, "wcal-1", hAll3(), "[]")
}

func gradeSteps(n int) []gateway.Step {
	out := make([]gateway.Step, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, gateway.Step{Content: hGradeJSON(hAll3(), "")})
	}
	return out
}

func TestWeeklyZeroCandidatesSkipsCompareAndDownstream(t *testing.T) {
	cands := writeTemp(t, "candidates.yaml", "candidates: []\n")
	reports := weeklyEnv(t, cands)
	fx := setupHarness(t, map[string][]gateway.Step{
		"gs1": gradeSteps(1), "gs2": gradeSteps(1),
	})
	seedWeeklySentinel(t, fx)
	probe := &weeklyProbe{}

	// Act.
	res, err := fx.runner.Weekly(context.Background(), WeeklyOptions{
		Steps:   DefaultWeeklySteps(),
		Now:     time.Date(2026, 9, 21, 0, 0, 0, 0, time.UTC),
		Compare: probe.compare(),
		LoadPack: func(context.Context) error {
			probe.loads++
			return nil
		},
	})

	// Assert: skipped steps never ran their comparisons, report written.
	if err != nil {
		t.Fatalf("Weekly: %v", err)
	}
	if res.SentinelRunID == "" {
		t.Fatal("SentinelRunID empty; want the sentinel sub-run recorded")
	}
	if len(res.Skipped) != 3 {
		t.Fatalf("Skipped = %v; want [screen compare downstream]", res.Skipped)
	}
	if got := probe.runs(res.SentinelRunID); got != 0 {
		t.Fatalf("non-sentinel compare calls = %d; want 0 (screen/compare/downstream skipped)", got)
	}
	if res.ScreenRunID != "" || len(res.CompareRunIDs) != 0 || len(res.DownstreamRunIDs) != 0 {
		t.Fatalf("result = %+v; want no screen/compare/downstream run ids", res)
	}
	if _, err := os.Stat(filepath.Join(reports, res.RunID+".md")); err != nil {
		t.Fatalf("report file: %v; want reports/<run_id>.md written", err)
	}
}

func TestWeeklyDriftBlockedBeforeCompare(t *testing.T) {
	cands := writeTemp(t, "candidates.yaml", "candidates: []\n")
	reports := weeklyEnv(t, cands)
	zeroFlag := func() gateway.Step {
		return gateway.Step{Content: hGradeJSON(map[string]int{
			"grounding_and_calibration": 0, "context_and_values_fidelity": 0,
			"decision_insight": 0, "practical_robustness": 0, "role_execution": 0,
		}, `{"type":"coercive","passage":"p","violated":"v"}`)}
	}
	fx := setupHarness(t, map[string][]gateway.Step{
		"gs1": {zeroFlag(), zeroFlag(), zeroFlag()},
		"gs2": {zeroFlag(), zeroFlag(), zeroFlag()},
	})
	seedWeeklySentinel(t, fx)
	calCase, err := fx.db.GetCaseByKey("FS-base")
	if err != nil {
		t.Fatalf("GetCaseByKey: %v", err)
	}
	seedHCalibration(t, fx.db, calCase, "wcal-2", hAll3(), "[]")
	seedHCalibration(t, fx.db, calCase, "wcal-3", hAll3(), "[]")
	probe := &weeklyProbe{}

	// Act.
	res, err := fx.runner.Weekly(context.Background(), WeeklyOptions{
		Steps:   DefaultWeeklySteps(),
		Now:     time.Date(2026, 9, 21, 0, 0, 0, 0, time.UTC),
		Compare: probe.compare(),
		LoadPack: func(context.Context) error {
			probe.loads++
			return nil
		},
	})

	// Assert: drift error, zero compare calls, report still written.
	var drift *SentinelDriftError
	if !errors.As(err, &drift) {
		t.Fatalf("err = %v; want SentinelDriftError", err)
	}
	if len(probe.calls) != 0 {
		t.Fatalf("compare calls = %d; want 0 (blocked before compare)", len(probe.calls))
	}
	if res == nil || res.Status != "blocked" || res.Blocked == "" {
		t.Fatalf("result = %+v; want blocked status with a drift message", res)
	}
	if _, serr := os.Stat(filepath.Join(reports, res.RunID+".md")); serr != nil {
		t.Fatalf("report file: %v; want a report on the blocked run", serr)
	}
}

func TestWeeklyFailingStepStillWritesReport(t *testing.T) {
	cands := writeTemp(t, "candidates.yaml", `candidates:
  - {key: cand-j, seat: judge, model: cand-m, family: openai, finalist: auto}
`)
	reports := weeklyEnv(t, cands)
	fx := setupHarness(t, map[string][]gateway.Step{
		"gs1": gradeSteps(1), "gs2": gradeSteps(1),
		// One sentinel-recheck call for the admitted screening grader.
		"gscreen": gradeSteps(1),
	})
	seedWeeklySentinel(t, fx)
	insertHGrader(t, fx.db, "screen", "gscreen", "other", "screening", true)
	// Development cases relevant only to possibility: the judge candidate
	// has nothing to screen on, so screen fails.
	fam := seedHFamily(t, fx.db, "FD", "development", []string{"priority"}, []string{"possibility"})
	seedHCase(t, fx.db, fam, "FD-a")
	probe := &weeklyProbe{}

	// Act.
	res, err := fx.runner.Weekly(context.Background(), WeeklyOptions{
		Steps:   DefaultWeeklySteps(),
		Now:     time.Date(2026, 9, 21, 0, 0, 0, 0, time.UTC),
		Compare: probe.compare(),
		LoadPack: func(context.Context) error {
			probe.loads++
			return nil
		},
	})

	// Assert: the screen error surfaces AND the report is still written.
	if err == nil || !strings.Contains(err.Error(), "no development cases") {
		t.Fatalf("err = %v; want the screen failure", err)
	}
	if res == nil || res.Status != "failed" {
		t.Fatalf("result = %+v; want failed status", res)
	}
	if _, serr := os.Stat(filepath.Join(reports, res.RunID+".md")); serr != nil {
		t.Fatalf("report file: %v; want a report on the failed run", serr)
	}
}

func TestWeeklyHappyPathRunsStepsInOrder(t *testing.T) {
	cands := writeTemp(t, "candidates.yaml", `candidates:
  - {key: cand-a, seat: possibility, model: cand-m, family: openai, finalist: auto}
`)
	reports := weeklyEnv(t, cands)
	fx := setupHarness(t, map[string][]gateway.Step{
		// Sentinel recheck (1 item x 3 graders) + compare absolute grades
		// for every candidate/incumbent pair (2 cases x 2 graders) + the
		// screen grades below.
		"gs1": gradeSteps(11),
		"gs2": gradeSteps(11),
		// Sentinel recheck (1) + one grade per candidate and incumbent
		// response (2 cases x 2).
		"gscreen": gradeSteps(7),
	})
	seedWeeklySentinel(t, fx)
	insertHGrader(t, fx.db, "screen", "gscreen", "other", "screening", true)
	sel := seedHFamily(t, fx.db, "SEL", "selection", []string{"hard", "hardest", "priority"}, []string{"possibility"})
	seedHCase(t, fx.db, sel, "SEL-a")
	seedHCase(t, fx.db, sel, "SEL-b")
	dev := seedHFamily(t, fx.db, "FD", "development", []string{"priority"}, []string{"possibility"})
	seedHCase(t, fx.db, dev, "FD-a")
	seedHCase(t, fx.db, dev, "FD-b")
	probe := &weeklyProbe{}

	// Act.
	res, err := fx.runner.Weekly(context.Background(), WeeklyOptions{
		Steps:   DefaultWeeklySteps(),
		Now:     time.Date(2026, 9, 21, 0, 0, 0, 0, time.UTC),
		Compare: probe.compare(),
		Cover: func(_ context.Context, _ coverInput, _ map[string]string, _ string, _ *grading.Grader, _ string) error {
			return nil
		},
		LoadPack: func(context.Context) error {
			probe.loads++
			return nil
		},
	})

	// Assert: every step ran with its own sub-run, in sentinel → compare →
	// downstream order, promotion stored, report written.
	if err != nil {
		t.Fatalf("Weekly: %v", err)
	}
	if res.Status != "complete" {
		t.Fatalf("status = %q; want complete", res.Status)
	}
	if probe.loads != 1 {
		t.Fatalf("case pack loads = %d; want exactly 1, first", probe.loads)
	}
	if res.SentinelRunID == "" || res.ScreenRunID == "" {
		t.Fatalf("result = %+v; want sentinel and screen sub-runs", res)
	}
	if len(res.CompareRunIDs) != 1 || len(res.DownstreamRunIDs) != 1 {
		t.Fatalf("result = %+v; want one compare and one downstream sub-run", res)
	}
	sentinelLast := probe.maxSeq(res.SentinelRunID)
	compareFirst := probe.minSeq(res.CompareRunIDs[0])
	downstreamFirst := probe.minSeq(res.DownstreamRunIDs[0])
	if sentinelLast == -1 || compareFirst == -1 || downstreamFirst == -1 {
		t.Fatalf("compare calls = %+v; want sentinel, compare, and downstream calls", probe.calls)
	}
	if sentinelLast >= compareFirst || compareFirst > downstreamFirst {
		t.Fatalf("order = sentinel-last %d compare-first %d downstream-first %d; want sentinel < compare <= downstream",
			sentinelLast, compareFirst, downstreamFirst)
	}
	row, err := fx.db.GetCandidateByKey(res.CompareRunIDs[0], "cand-a")
	if err != nil || row.PromotionJSON == nil {
		t.Fatalf("candidate = %+v, %v; want stored promotion_json", row, err)
	}
	if _, serr := os.Stat(filepath.Join(reports, res.RunID+".md")); serr != nil {
		t.Fatalf("report file: %v; want reports/<run_id>.md written", serr)
	}
}

func TestWeeklyCasePackValidationAbortsWithNoRunState(t *testing.T) {
	cands := writeTemp(t, "candidates.yaml", "candidates: []\n")
	reports := weeklyEnv(t, cands)
	fx := setupHarness(t, nil)

	// Act: the case pack fails validation before any run row exists.
	res, err := fx.runner.Weekly(context.Background(), WeeklyOptions{
		Steps:    DefaultWeeklySteps(),
		LoadPack: func(context.Context) error { return errors.New("casepack: need at least one family tagged safety") },
	})

	// Assert: clear validation message, no result, no report written.
	if err == nil || !strings.Contains(err.Error(), "load case pack") || !strings.Contains(err.Error(), "safety") {
		t.Fatalf("err = %v; want the case pack validation failure", err)
	}
	if res != nil {
		t.Fatalf("result = %+v; want nil (no misleading run state)", res)
	}
	entries, rerr := os.ReadDir(reports)
	if rerr == nil && len(entries) != 0 {
		t.Fatalf("reports dir has %d files; want none", len(entries))
	}
}

func TestParseWeeklySteps(t *testing.T) {
	// Arrange/Act: empty means all steps.
	all, err := ParseWeeklySteps("")
	if err != nil || all != DefaultWeeklySteps() {
		t.Fatalf("empty steps = %+v, %v; want all steps", all, err)
	}
	// Arrange/Act: a subset selects only those steps.
	sub, err := ParseWeeklySteps("sentinel,report")
	if err != nil || !sub.Sentinel || !sub.Report || sub.Screen || sub.Compare || sub.Downstream {
		t.Fatalf("subset = %+v, %v; want sentinel+report only", sub, err)
	}
	// Arrange/Act: an unknown step is rejected.
	if _, err := ParseWeeklySteps("sentinel,bogus"); err == nil {
		t.Fatal("unknown step: want error")
	}
}
