package harness

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/qoke/toughdecisions/internal/gateway"
	"github.com/qoke/toughdecisions/internal/grading"
	"github.com/qoke/toughdecisions/internal/pack"
	"github.com/qoke/toughdecisions/internal/store"
)

func seedCompareSelection(t *testing.T, fx *hFixture) {
	t.Helper()
	fam := seedHFamily(t, fx.db, "SEL", "selection", []string{"hard", "hardest", "priority"}, []string{"possibility"})
	seedHCase(t, fx.db, fam, "SEL-a")
	seedHCase(t, fx.db, fam, "SEL-b")
}

func seedCompareGraders(t *testing.T, fx *hFixture) {
	t.Helper()
	insertHGrader(t, fx.db, "sa", "gs1", "openai", "selection", true)
	insertHGrader(t, fx.db, "sb", "gs2", "anthropic", "selection", true)
}

func compareScripts(n int) map[string][]gateway.Step {
	grade := gateway.Step{Content: hGradeJSON(hAll3(), "")}
	pair := gateway.Step{Content: `{"verdict":"A","margin":"clear","consequential_difference":"d"}`}
	scripts := map[string][]gateway.Step{
		"v-poss": {{Content: hViewJSON}, {Content: hViewJSON}, {Content: hViewJSON}, {Content: hViewJSON}, {Content: hViewJSON}, {Content: hViewJSON}},
		"cand-m": {{Content: hViewJSON}, {Content: hViewJSON}, {Content: hViewJSON}, {Content: hViewJSON}, {Content: hViewJSON}, {Content: hViewJSON}},
		"gs1":    {},
		"gs2":    {},
	}
	for i := 0; i < n; i++ {
		scripts["gs1"] = append(scripts["gs1"], grade, pair)
		scripts["gs2"] = append(scripts["gs2"], grade, pair)
	}
	return scripts
}

func seedCalItem(t *testing.T, fx *hFixture) {
	t.Helper()
	fam, err := fx.db.GetFamilyByKey("SEL")
	if err != nil {
		t.Fatalf("GetFamilyByKey: %v", err)
	}
	c, err := fx.db.GetCaseByKey("SEL-a")
	if err != nil {
		t.Fatalf("GetCaseByKey: %v", err)
	}
	_ = fam
	seedHCalibration(t, fx.db, c, "cal-1", hAll3(), "[]")
}

func TestComparePersistsResultAndVerdict(t *testing.T) {
	fx := setupHarness(t, compareScripts(8))
	seedCompareGraders(t, fx)
	seedCompareSelection(t, fx)
	seedCalItem(t, fx)

	// Act.
	res, err := fx.runner.Compare(context.Background(), CompareOptions{
		Spec: CandidateSpec{Key: "cand-a", Seat: "possibility", Model: "cand-m", Family: "openai", Finalist: "auto"},
	})
	// Assert: persisted compare_result_json the promotion step can read.
	if err != nil {
		t.Fatalf("Compare: %v", err)
	}
	if len(res.Cases) != 2 {
		t.Fatalf("cases = %d; want 2 selection cases", len(res.Cases))
	}
	if len(res.Cases) != 2 || res.Cases[0].Verdict == "" || res.Cases[1].Verdict == "" {
		t.Fatalf("cases = %+v; want 2 recorded verdicts", res.Cases)
	}
	if res.HardWins+res.HardLosses+res.HardOther != 2 {
		t.Fatalf("hard tally = %d+%d+%d; want 2 total", res.HardWins, res.HardLosses, res.HardOther)
	}
	row, err := fx.db.GetCandidateByKey(res.RunID, "cand-a")
	if err != nil || row.CompareResultJSON == nil {
		t.Fatalf("candidate = %+v, %v; want stored compare_result_json", row, err)
	}
	var stored CompareResult
	if err := json.Unmarshal([]byte(*row.CompareResultJSON), &stored); err != nil {
		t.Fatalf("decode compare_result_json: %v", err)
	}
	if len(stored.Cases) != 2 || stored.HardWins+stored.HardLosses+stored.HardOther != 2 {
		t.Fatalf("stored = %+v; want 2 cases with a full hard tally", stored)
	}
}

func TestCompareFragilityFlips(t *testing.T) {
	fx := setupHarness(t, compareScripts(8))
	seedCompareGraders(t, fx)
	seedCompareSelection(t, fx)
	seedCalItem(t, fx)
	calls := 0

	// Arrange: first pass says candidate wins, the --fresh fragility
	// re-run says the incumbent wins.
	cmp := func(ctx context.Context, in compareInput, fresh, baseline *store.Response, graders []*grading.Grader, runID string) (string, bool, string, error) {
		calls++
		if calls <= 2 {
			return "left", true, "candidate clearer", nil
		}
		return "right", true, "incumbent steadier", nil
	}
	// Act.
	res, err := fx.runner.Compare(context.Background(), CompareOptions{
		Spec:    CandidateSpec{Key: "cand-a", Seat: "possibility", Model: "cand-m", Family: "openai"},
		Compare: cmp,
	})
	// Assert: any flipped final marks fragile.
	if err != nil {
		t.Fatalf("Compare: %v", err)
	}
	if !res.Fragile {
		t.Fatalf("fragile=false; want true when a fresh final flips")
	}
	if res.Cases[0].FreshVerdict != "right" || !res.Cases[0].ReRanFresh {
		t.Fatalf("case0 = %+v; want fresh re-run recorded", res.Cases[0])
	}
}

func TestCompareFragilityFallbackNoHardest(t *testing.T) {
	// Arrange: no hardest tags — picks the two lowest incumbent means.
	hardest := []bool{false, false, false}
	means := []float64{15.0, 9.0, 12.0}
	// Act.
	picks := FragilityPicks(hardest, means)
	// Assert: lowest means first.
	if len(picks) != 2 || picks[0] != 1 || picks[1] != 2 {
		t.Fatalf("picks = %v; want [1 2]", picks)
	}
	// Arrange: hardest tags win over means.
	picks = FragilityPicks([]bool{false, true, true}, means)
	if len(picks) != 2 || picks[0] != 1 || picks[1] != 2 {
		t.Fatalf("hardest picks = %v; want [1 2]", picks)
	}
}

func TestLatencyP50P95OddEven(t *testing.T) {
	// Arrange/Act/Assert: odd and even sample counts.
	if got := P50([]int64{30, 10, 20}); got != 20 {
		t.Fatalf("P50 odd = %d; want 20", got)
	}
	if got := P95([]int64{10, 20, 30, 40, 50, 60, 70, 80, 90, 100,
		110, 120, 130, 140, 150, 160, 170, 180, 190, 200}); got != 190 {
		t.Fatalf("P95 even-20 = %d; want 190 (nearest-rank ceil(0.95*20)=19th)", got)
	}
	if got := P95([]int64{5, 1, 3}); got != 5 {
		t.Fatalf("P95 odd-3 = %d; want 5 (ceil(2.85)=3rd)", got)
	}
	if got := P95(nil); got != 0 {
		t.Fatalf("P95(nil) = %d; want 0", got)
	}
}

func TestDownstreamViewCandidateCouncil(t *testing.T) {
	scripts := compareScripts(12)
	scripts["j1"] = []gateway.Step{
		{Content: hJudgeJSON}, {Content: hJudgeJSON}, {Content: hJudgeJSON}, {Content: hJudgeJSON},
		{Content: hJudgeJSON}, {Content: hJudgeJSON}, {Content: hJudgeJSON}, {Content: hJudgeJSON},
	}
	fx := setupHarness(t, scripts)
	seedCompareGraders(t, fx)
	seedCompareSelection(t, fx)
	seedCalItem(t, fx)

	// Act: view candidate dropped into the incumbent council.
	res, err := fx.runner.Downstream(context.Background(), DownstreamOptions{
		Spec: CandidateSpec{Key: "cand-a", Seat: "possibility", Model: "cand-m", Family: "openai"},
	})
	// Assert: candidate-council vs incumbent-council verdicts persisted.
	if err != nil {
		t.Fatalf("Downstream: %v", err)
	}
	if len(res.Cases) != 2 {
		t.Fatalf("cases = %d; want 2", len(res.Cases))
	}
	row, err := fx.db.GetCandidateByKey(res.RunID, "cand-a")
	if err != nil || row.DownstreamResultJSON == nil {
		t.Fatalf("candidate = %+v, %v; want downstream_result_json", row, err)
	}
	var stored DownstreamResult
	if err := json.Unmarshal([]byte(*row.DownstreamResultJSON), &stored); err != nil {
		t.Fatalf("decode downstream_result_json: %v", err)
	}
	if len(stored.Cases) != 2 {
		t.Fatalf("stored cases = %d; want 2", len(stored.Cases))
	}
	if stored.AgreedNet < -2 || stored.AgreedNet > 2 {
		t.Fatalf("agreed net = %d; want within [-2,2]", stored.AgreedNet)
	}
}

func TestDownstreamCloseFinalSingleFreshRepeat(t *testing.T) {
	scripts := compareScripts(12)
	scripts["j1"] = []gateway.Step{
		{Content: hJudgeJSON}, {Content: hJudgeJSON}, {Content: hJudgeJSON}, {Content: hJudgeJSON},
		{Content: hJudgeJSON}, {Content: hJudgeJSON}, {Content: hJudgeJSON}, {Content: hJudgeJSON},
	}
	fx := setupHarness(t, scripts)
	seedCompareGraders(t, fx)
	seedCompareSelection(t, fx)
	seedCalItem(t, fx)
	cmpCalls := 0
	judgeCallsBefore := 0

	// Arrange: every downstream final is close (tie), forcing the single
	// --fresh judge repetition.
	cmp := func(ctx context.Context, in compareInput, fresh, baseline *store.Response, graders []*grading.Grader, runID string) (string, bool, string, error) {
		cmpCalls++
		return "tie", true, "too close to call", nil
	}
	// Act.
	judgeCallsBefore = countModelCalls(fx, "j1")
	res, err := fx.runner.Downstream(context.Background(), DownstreamOptions{
		Spec:    CandidateSpec{Key: "cand-a", Seat: "possibility", Model: "cand-m", Family: "openai"},
		Compare: cmp,
	})
	// Assert: exactly one extra judge repetition per case, one re-compare.
	if err != nil {
		t.Fatalf("Downstream: %v", err)
	}
	if res.FreshRepeats != 2 {
		t.Fatalf("fresh repeats = %d; want 2 (one per case)", res.FreshRepeats)
	}
	if cmpCalls != 4 {
		t.Fatalf("compare calls = %d; want 4 (2 initial + 2 fresh)", cmpCalls)
	}
	judgeCalls := countModelCalls(fx, "j1") - judgeCallsBefore
	if judgeCalls <= 4 {
		t.Fatalf("judge calls = %d; want more than the 4 initial (fresh repeats added)", judgeCalls)
	}
	_ = judgeCallsBefore
}

func countModelCalls(fx *hFixture, model string) int {
	n := 0
	for _, c := range fx.fake.Calls {
		if strings.Contains(c.Model, model) {
			n++
		}
	}
	return n
}

func TestPromotionSixRulesPassAndFail(t *testing.T) {
	// Arrange: an input that passes all six automated rules.
	pass := ChecklistInput{
		OpenFlags: 0, ConfirmedFlags: 0,
		RoleMeans: []float64{3.5, 3.2}, RoleMins: []int{3, 2},
		RoleMinMean: 3.0, RoleMinFloor: 2,
		HardWins: 2, HardLosses: 1,
		P50Cand: 100, P50Inc: 200, SpeedRatio: 0.8,
		PriorityLosses: 0, DownstreamNet: 1,
		P95Cand: 500, SeatBudgetMs: 30000, Timeouts: 0,
		MeanCostCand: 0.01, MeanCostInc: 0.02,
		Fragile: false, UniqueNoticed: []string{"F001-I1"}, Preserved: 1,
	}
	// Act.
	cl := EvaluateChecklist("cand-a", pass)
	// Assert: promote_recommended iff all six pass.
	if !cl.PromoteRecommended {
		t.Fatalf("checklist = %+v; want promote_recommended", cl)
	}
	if len(cl.Rows) != 9 {
		t.Fatalf("rows = %d; want 6 automated + 3 informational", len(cl.Rows))
	}
	for _, want := range []string{
		"no unresolved material flag", "executes role reliably",
		"improves hard cases or same quality with better speed",
		"no unacceptable regression on priority cases",
		"preserves or improves final recommendation",
		"reliably fits the deadline",
	} {
		found := false
		for _, row := range cl.Rows {
			if row.Name == want {
				found = true
				if row.Status != CheckPass {
					t.Fatalf("row %q = %s; want pass", want, row.Status)
				}
				if strings.TrimSpace(row.Evidence) == "" {
					t.Fatalf("row %q has no evidence", want)
				}
			}
		}
		if !found {
			t.Fatalf("missing automated row %q", want)
		}
	}

	// Arrange: each rule failing alone blocks promotion.
	failures := []struct {
		name string
		mut  func(*ChecklistInput)
	}{
		{"flags", func(in *ChecklistInput) { in.OpenFlags = 1 }},
		{"role mean", func(in *ChecklistInput) { in.RoleMeans = []float64{2.0, 3.5} }},
		{"role floor", func(in *ChecklistInput) { in.RoleMins = []int{3, 1} }},
		{"hard net", func(in *ChecklistInput) { in.HardWins, in.HardLosses = 1, 2 }},
		{"priority", func(in *ChecklistInput) { in.PriorityLosses = 1 }},
		{"downstream", func(in *ChecklistInput) { in.DownstreamNet = -1 }},
		{"deadline", func(in *ChecklistInput) { in.P95Cand = 99999 }},
		{"timeouts", func(in *ChecklistInput) { in.Timeouts = 1 }},
	}
	for _, f := range failures {
		bad := pass
		f.mut(&bad)
		got := EvaluateChecklist("cand-a", bad)
		if got.PromoteRecommended {
			t.Fatalf("rule %s: promote_recommended=true; want false", f.name)
		}
	}
}

func TestPromotionInformationalNeverBlocks(t *testing.T) {
	// Arrange: all six pass but the informational rows look bad.
	in := ChecklistInput{
		OpenFlags: 0, ConfirmedFlags: 0,
		RoleMeans: []float64{3.5, 3.2}, RoleMins: []int{3, 2},
		RoleMinMean: 3.0, RoleMinFloor: 2,
		HardWins: 2, HardLosses: 1,
		P50Cand: 100, P50Inc: 200, SpeedRatio: 0.8,
		PriorityLosses: 0, DownstreamNet: 1,
		P95Cand: 500, SeatBudgetMs: 30000, Timeouts: 0,
		MeanCostCand: 0.99, MeanCostInc: 0.01,
		Fragile: true, UniqueNoticed: nil, Preserved: 0,
	}
	// Act.
	cl := EvaluateChecklist("cand-a", in)
	// Assert: cost/fragility/coverage are info-only.
	if !cl.PromoteRecommended {
		t.Fatalf("checklist = %+v; want promote despite bad informational rows", cl)
	}
	for _, row := range cl.Rows {
		switch row.Name {
		case "cost tie-break", "fragility", "issue coverage":
			if row.Status != CheckInfo {
				t.Fatalf("informational row %q = %s; want info", row.Name, row.Status)
			}
		}
	}
}

func TestPromotionSpeedPathPasses(t *testing.T) {
	// Arrange: tied hard quality with materially better speed.
	in := ChecklistInput{
		RoleMeans: []float64{3.0, 3.0}, RoleMins: []int{2, 2},
		RoleMinMean: 3.0, RoleMinFloor: 2,
		HardWins: 1, HardLosses: 1,
		P50Cand: 100, P50Inc: 200, SpeedRatio: 0.8,
		PriorityLosses: 0, DownstreamNet: 0,
		P95Cand: 500, SeatBudgetMs: 30000, Timeouts: 0,
	}
	// Act/Assert: net 0 + p50 <= 0.8x incumbent passes rule 3.
	cl := EvaluateChecklist("cand-a", in)
	if !cl.PromoteRecommended {
		t.Fatalf("checklist = %+v; want promote on the speed path", cl)
	}
	// Arrange: same tie but too slow.
	in.P50Cand = 190
	if got := EvaluateChecklist("cand-a", in); got.PromoteRecommended {
		t.Fatalf("slow tie promoted; want blocked")
	}
}

func TestPromotionEndToEndNoLifetimeAverages(t *testing.T) {
	scripts := compareScripts(16)
	scripts["j1"] = []gateway.Step{
		{Content: hJudgeJSON}, {Content: hJudgeJSON}, {Content: hJudgeJSON}, {Content: hJudgeJSON},
		{Content: hJudgeJSON}, {Content: hJudgeJSON}, {Content: hJudgeJSON}, {Content: hJudgeJSON},
		{Content: hJudgeJSON}, {Content: hJudgeJSON}, {Content: hJudgeJSON}, {Content: hJudgeJSON},
	}
	fx := setupHarness(t, scripts)
	seedCompareGraders(t, fx)
	seedCompareSelection(t, fx)
	seedCalItem(t, fx)
	ctx := context.Background()

	// Arrange: one compare run then a second run — promotion must read
	// only the run it is asked about.
	res1, err := fx.runner.Compare(ctx, CompareOptions{
		Spec: CandidateSpec{Key: "cand-a", Seat: "possibility", Model: "cand-m", Family: "openai"},
	})
	if err != nil {
		t.Fatalf("first Compare: %v", err)
	}
	ds1, err := fx.runner.Downstream(ctx, DownstreamOptions{
		Spec:  CandidateSpec{Key: "cand-a", Seat: "possibility", Model: "cand-m", Family: "openai"},
		RunID: res1.RunID,
	})
	if err != nil {
		t.Fatalf("Downstream: %v", err)
	}
	_ = ds1
	// Act: promote within the same run; then run compare again.
	cl, err := fx.runner.Promotion(res1.RunID, "cand-a")
	if err != nil {
		t.Fatalf("Promotion: %v", err)
	}
	if len(cl.Rows) != 9 {
		t.Fatalf("rows = %d; want 9", len(cl.Rows))
	}
	row, _ := fx.db.GetCandidateByKey(res1.RunID, "cand-a")
	if row.PromotionJSON == nil {
		t.Fatal("promotion_json not stored")
	}
	res2, err := fx.runner.Compare(ctx, CompareOptions{
		Spec: CandidateSpec{Key: "cand-a", Seat: "possibility", Model: "cand-m", Family: "openai"},
	})
	if err != nil {
		t.Fatalf("second Compare: %v", err)
	}
	// Assert: run 2 never consulted run 1 — distinct rows, per-run numbers.
	if res2.RunID == res1.RunID {
		t.Fatal("second compare reused the run id; want a new per-run scope")
	}
	row2, _ := fx.db.GetCandidateByKey(res2.RunID, "cand-a")
	if row2.CompareResultJSON == nil || *row2.CompareResultJSON == *row.CompareResultJSON {
		t.Fatal("run 2 compare_result_json identical to run 1; want per-run evidence")
	}
	_ = time.Now
}

func seedJudgeSelection(t *testing.T, fx *hFixture) {
	t.Helper()
	fam := seedHFamily(t, fx.db, "JSEL", "selection", []string{"hard"}, []string{"judge"})
	c := seedHCase(t, fx.db, fam, "JSEL-a")
	if _, err := fx.db.UpsertBundle(&store.Bundle{
		BundleKey: "JSEL-B1", CaseID: c.ID, Kind: "authored",
		ViewsJSON:         `{"possibility":{"Role":"possibility","Qualification":"q"},"perspective":{"Role":"perspective","Qualification":"q"},"stress_tester":{"Role":"stress_tester","Qualification":"q"}}`,
		ManipulationsJSON: `["omitted_constraint"]`,
		BundleHash:        "bh-JSEL-B1",
	}); err != nil {
		t.Fatalf("UpsertBundle: %v", err)
	}
}

func coverageScript() gateway.Step {
	return gateway.Step{Content: `{"rows":[{"issue_id":"SEL-I1","issue_text":"deadline","is_planted":true,"noticed_by":["possibility"],"unsupported_by":[],"judge_outcome":"preserved"}]}`}
}

func TestSelectCompareCasesJudgeSeatUsesBundles(t *testing.T) {
	fx := setupHarness(t, nil)
	seedHFamily(t, fx.db, "SEL", "selection", []string{"hard"}, []string{"judge"})
	fam, _ := fx.db.GetFamilyByKey("SEL")
	c := seedHCase(t, fx.db, fam, "SEL-a")
	for _, key := range []string{"SEL-B1", "SEL-B2"} {
		if _, err := fx.db.UpsertBundle(&store.Bundle{
			BundleKey: key, CaseID: c.ID, Kind: "authored",
			ViewsJSON: `{"possibility":{"Role":"possibility"}}`, BundleHash: "bh-" + key,
		}); err != nil {
			t.Fatalf("UpsertBundle: %v", err)
		}
	}
	// Act.
	cases, err := fx.runner.SelectCompareCases(pack.Seat("judge"))
	// Assert: one entry per selection bundle.
	if err != nil {
		t.Fatalf("SelectCompareCases: %v", err)
	}
	if len(cases) != 2 || cases[0].Bundle == nil || cases[1].Bundle == nil {
		t.Fatalf("cases = %+v; want 2 bundle entries", cases)
	}
}

func TestCompareHelpersAndErrors(t *testing.T) {
	fx := setupHarness(t, nil)
	// Fragility caps at two hardest picks.
	if picks := FragilityPicks([]bool{true, true, true}, []float64{1, 2, 3}); len(picks) != 2 {
		t.Fatalf("picks = %v; want 2", picks)
	}
	// Unknown seat rejected before any model call.
	if _, err := fx.runner.Compare(context.Background(), CompareOptions{Spec: CandidateSpec{Key: "x", Seat: "nope"}}); err == nil {
		t.Fatal("unknown seat: want error")
	}
	// No selection families is an error.
	if _, err := fx.runner.SelectCompareCases(pack.Seat("possibility")); err == nil {
		t.Fatal("no selection families: want error")
	}
	// costOf/meanTotal edge cases.
	if costOf(nil) != 0 || costOf(&store.Response{}) != 0 {
		t.Fatal("costOf nil/missing should be 0")
	}
	if meanTotal(nil) != 0 {
		t.Fatal("meanTotal(nil) should be 0")
	}
	// countRunFlags filters by run and response.
	fx2 := setupHarness(t, nil)
	resp, err := fx2.db.InsertResponse(&store.Response{CacheKey: "k1", Seat: "possibility", ConfigHash: "c", PromptPackHash: "p", InputHash: "i", Origin: "harness", ModelRequested: "m"})
	if err != nil {
		t.Fatalf("InsertResponse: %v", err)
	}
	grade := &store.Grade{CacheKey: "g1", ResponseID: resp.ID, GraderConfigHash: "gh", RubricHash: "rh", ScoresJSON: "{}", RawJSON: "{}"}
	stored, err := fx2.db.InsertGrade(grade)
	if err != nil {
		t.Fatalf("InsertGrade: %v", err)
	}
	runID := "run-other"
	if _, err := fx2.db.InsertFlag(&store.Flag{GradeID: stored.ID, ResponseID: resp.ID, Type: "coercive", Status: "open", RunID: &runID}); err != nil {
		t.Fatalf("InsertFlag: %v", err)
	}
	if n, err := fx2.runner.countRunFlags("open", "run1", map[string]bool{resp.ID: true}); err != nil || n != 0 {
		t.Fatalf("countRunFlags other-run = %d, %v; want 0", n, err)
	}
	if _, err := fx2.runner.countRunFlags("bogus-status", "run1", map[string]bool{}); err != nil {
		t.Fatalf("countRunFlags empty: %v", err)
	}
}

func TestDownstreamJudgeCandidateLikeForLike(t *testing.T) {
	scripts := compareScripts(16)
	scripts["j1"] = []gateway.Step{
		{Content: hJudgeJSON}, {Content: hJudgeJSON}, {Content: hJudgeJSON}, {Content: hJudgeJSON},
	}
	scripts["cand-j"] = []gateway.Step{
		{Content: hJudgeJSON}, {Content: hJudgeJSON},
	}
	fx := setupHarness(t, scripts)
	// Candidate judge registered in models via cand-m alias family; reuse it.
	seedCompareGraders(t, fx)
	seedJudgeSelection(t, fx)
	seedHFamily(t, fx.db, "SEL", "selection", []string{"hard"}, []string{"judge"})
	fam, _ := fx.db.GetFamilyByKey("JSEL")
	c, _ := fx.db.GetCaseByKey("JSEL-a")
	_ = fam
	_ = c
	seedCalItemFor(t, fx, "JSEL-a")

	// Act: judge candidate judges the identical saved bundles.
	res, err := fx.runner.Downstream(context.Background(), DownstreamOptions{
		Spec: CandidateSpec{Key: "cand-j", Seat: "judge", Model: "cand-m", Family: "openai"},
	})
	if err != nil {
		t.Fatalf("Downstream judge: %v", err)
	}
	if len(res.Cases) == 0 || res.Cases[0].BundleKey == "" {
		t.Fatalf("cases = %+v; want bundle-scoped verdicts", res.Cases)
	}
}

func seedCalItemFor(t *testing.T, fx *hFixture, caseKey string) {
	t.Helper()
	c, err := fx.db.GetCaseByKey(caseKey)
	if err != nil {
		t.Fatalf("GetCaseByKey: %v", err)
	}
	seedHCalibration(t, fx.db, c, "cal-"+caseKey, hAll3(), "[]")
}

func TestDownstreamHelpers(t *testing.T) {
	// bundleResponsesFor rejects malformed bundle views.
	fx := setupHarness(t, nil)
	if _, err := bundleResponsesFor(fx.runner, &store.Bundle{ViewsJSON: "{bad"}); err == nil {
		t.Fatal("bad bundle views: want error")
	}
	// plantedFor skips blank ids and rejects malformed JSON.
	if got := plantedFor(&store.Family{PlantedIssuesJSON: "bad"}); len(got) != 0 {
		t.Fatalf("planted bad JSON = %v; want empty", got)
	}
	if got := plantedFor(&store.Family{PlantedIssuesJSON: `[{"id":"","text":"x"}]`}); len(got) != 0 {
		t.Fatalf("planted blank id = %v; want empty", got)
	}
	// blindOf/renderedViewsOf/bundleViewText edge cases.
	if blindOf(nil) != "" {
		t.Fatal("blindOf(nil) should be empty")
	}
	if got := blindOf(&store.Response{RawText: "raw words"}); !strings.Contains(got, "raw words") {
		t.Fatalf("blindOf raw = %q; want it to contain the raw text", got)
	}
	if got := renderedViewsOf(map[string]*store.Response{"possibility": {RawText: "v"}}); got["possibility"] == "" {
		t.Fatal("renderedViewsOf should render")
	}
	// coverDefault requires a grader.
	if err := fx.runner.coverDefault(context.Background(), coverInput{}, nil, "", nil, "run1"); err == nil {
		t.Fatal("cover without grader: want error")
	}
	// Unknown seat rejected.
	if _, err := fx.runner.Downstream(context.Background(), DownstreamOptions{Spec: CandidateSpec{Key: "x", Seat: "nope"}}); err == nil {
		t.Fatal("unknown seat: want error")
	}
	// Judge candidate without a saved bundle is an error.
	fx2 := setupHarness(t, nil)
	seedHFamily(t, fx2.db, "SEL", "selection", []string{"hard"}, []string{"judge"})
	fam, _ := fx2.db.GetFamilyByKey("SEL")
	seedHCase(t, fx2.db, fam, "SEL-a")
	if _, _, err := fx2.runner.downstreamBundles(context.Background(), "run1", fx2.pk, pack.SeatConfig{}, pack.Seat("judge"), &CompareCase{Case: &store.Case{CaseKey: "k"}}, false); err == nil {
		t.Fatal("judge without bundle: want error")
	}
	// decodeStrings/decode helpers.
	if decodeStrings("bad") != nil {
		t.Fatal("decodeStrings bad JSON should be nil")
	}
	if got := decodeStrings(`["a"]`); len(got) != 1 {
		t.Fatalf("decodeStrings = %v", got)
	}
	_ = coverageScript
}

func TestPromotionErrorsAndCoverageInfo(t *testing.T) {
	fx := setupHarness(t, nil)
	// Missing candidate.
	if _, err := fx.runner.Promotion("run1", "ghost"); err == nil {
		t.Fatal("missing candidate: want error")
	}
	// Candidate without compare/downstream results.
	if _, err := fx.db.UpsertCandidate("run1", "cand-a", "possibility", "{}", "ch"); err != nil {
		t.Fatalf("UpsertCandidate: %v", err)
	}
	if _, err := fx.runner.Promotion("run1", "cand-a"); err == nil {
		t.Fatal("no compare result: want error")
	}
	s := `{"run_id":"x"}`
	row, _ := fx.db.GetCandidateByKey("run1", "cand-a")
	if err := fx.db.UpdateCandidateResults(row.ID, &store.CandidateResults{CompareResultJSON: &s}); err != nil {
		t.Fatalf("UpdateCandidateResults: %v", err)
	}
	if _, err := fx.runner.Promotion("run1", "cand-a"); err == nil {
		t.Fatal("no downstream result: want error")
	}
	// Malformed compare JSON.
	bad := "{bad"
	if err := fx.db.UpdateCandidateResults(row.ID, &store.CandidateResults{CompareResultJSON: &bad}); err != nil {
		t.Fatalf("UpdateCandidateResults: %v", err)
	}
	if _, err := fx.runner.Promotion("run1", "cand-a"); err == nil {
		t.Fatal("malformed compare: want error")
	}
	// coverageInfo on an empty run returns no uniques.
	if unique, preserved := fx.runner.coverageInfo("run1", "possibility"); len(unique) != 0 || preserved != 0 {
		t.Fatalf("coverageInfo = %v, %d; want empty", unique, preserved)
	}
}
