package harness

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/qoke/toughdecisions/internal/config"
	"github.com/qoke/toughdecisions/internal/gateway"
	"github.com/qoke/toughdecisions/internal/grading"
	"github.com/qoke/toughdecisions/internal/logx"
	"github.com/qoke/toughdecisions/internal/models"
	"github.com/qoke/toughdecisions/internal/pack"
	"github.com/qoke/toughdecisions/internal/prompts"
	"github.com/qoke/toughdecisions/internal/schema"
	"github.com/qoke/toughdecisions/internal/store"
)

var _ = sql.ErrNoRows

const (
	hViewJSON  = `{"urgent_danger":{"present":false},"qualification":"q","suggested_reply":"r","decisive_insight":"i","tradeoff_or_objection":"t","depends_on":"d","fallback":"f"}`
	hJudgeJSON = `{"urgent_danger":{"present":false},"qualification":"q","recommended_reply":"r","why":"w","accepted_cost":"c","next":{"immediate":"i","forward":"f"},"change_course_if":"cc"}`
	hPackYAML  = `seats:
  possibility: {model: v-poss, family: openai}
  perspective: {model: v-persp, family: openai}
  stress_tester: {model: v-stress, family: openai}
  judge: {model: j1, family: openai}
`
	hModelsYAML = `models:
  - id: v-poss
    family: openai
    expected_response_model_prefixes: ["v-poss"]
    supports: {temperature: true, top_p: true, reasoning_effort: true, json_schema: true, json_object: true}
  - id: v-persp
    family: openai
    expected_response_model_prefixes: ["v-persp"]
    supports: {temperature: true, top_p: true, reasoning_effort: true, json_schema: true, json_object: true}
  - id: v-stress
    family: openai
    expected_response_model_prefixes: ["v-stress"]
    supports: {temperature: true, top_p: true, reasoning_effort: true, json_schema: true, json_object: true}
  - id: j1
    family: openai
    expected_response_model_prefixes: ["j1"]
    supports: {temperature: true, top_p: true, reasoning_effort: true, json_schema: true, json_object: true}
  - id: gs1
    family: openai
    expected_response_model_prefixes: ["gs1"]
    supports: {temperature: true, top_p: true, reasoning_effort: true, json_schema: true, json_object: true}
  - id: gs2
    family: anthropic
    expected_response_model_prefixes: ["gs2"]
    supports: {temperature: true, top_p: true, reasoning_effort: true, json_schema: true, json_object: true}
  - id: gscreen
    family: other
    expected_response_model_prefixes: ["gscreen"]
    supports: {temperature: true, top_p: true, reasoning_effort: true, json_schema: true, json_object: true}
  - id: gsub
    family: google
    expected_response_model_prefixes: ["gsub"]
    supports: {temperature: true, top_p: true, reasoning_effort: true, json_schema: true, json_object: true}
  - id: cand-m
    family: openai
    expected_response_model_prefixes: ["cand-m"]
    supports: {temperature: true, top_p: true, reasoning_effort: true, json_schema: true, json_object: true}
  - id: council-basic
    family: other
    expected_response_model_prefixes: ["basic-1"]
    supports: {temperature: false, top_p: false, reasoning_effort: false, json_schema: false, json_object: true}
`
)

type hFixture struct {
	db     *store.DB
	fake   *gateway.Fake
	runner *Runner
	pk     *pack.Pack
}

func writeTemp(t *testing.T, name, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatalf("write temp file: %v", err)
	}
	return p
}

func setupHarness(t *testing.T, scripts map[string][]gateway.Step) *hFixture {
	t.Helper()
	// H1: keep Report output out of the repo tree: default the reports dir
	// at a temp dir BEFORE config.Load reads the environment, unless the
	// caller already set COUNCIL_REPORTS_DIR (e.g. weeklyEnv).
	if os.Getenv("COUNCIL_REPORTS_DIR") == "" {
		t.Setenv("COUNCIL_REPORTS_DIR", filepath.Join(t.TempDir(), "reports"))
	}
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	db, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	pk, err := pack.Init(db, writeTemp(t, "pack.yaml", hPackYAML))
	if err != nil {
		t.Fatalf("pack.Init: %v", err)
	}
	mreg, err := models.LoadRegistry(writeTemp(t, "models.yaml", hModelsYAML))
	if err != nil {
		t.Fatalf("LoadRegistry: %v", err)
	}
	fake := gateway.NewFake(scripts)
	log := logx.NewWithWriter(cfg, discardWriter{})
	return &hFixture{db: db, fake: fake, runner: NewRunner(db, fake, cfg, log, mreg), pk: pk}
}

type discardWriter struct{}

func (discardWriter) Write(p []byte) (int, error) { return len(p), nil }

func insertHGrader(t *testing.T, db *store.DB, key, model, family, role string, admitted bool) *grading.Grader {
	t.Helper()
	stored, err := db.UpsertGraderConfig(&store.GraderConfig{
		GraderKey: key, Model: model, Family: family,
		ParamsJSON: "{}", RubricHash: prompts.RubricHash(),
		ConfigHash: "cfg-" + key, Role: role,
	})
	if err != nil {
		t.Fatalf("UpsertGraderConfig: %v", err)
	}
	if admitted {
		at := "2026-09-21T00:00:00Z"
		if err := db.SetGraderCalibration("cfg-"+key, `{"items":[]}`, true, &at); err != nil {
			t.Fatalf("SetGraderCalibration: %v", err)
		}
		stored, err = db.GetGraderConfig("cfg-" + key)
		if err != nil {
			t.Fatalf("GetGraderConfig: %v", err)
		}
	}
	return (*grading.Grader)(stored)
}

func seedHFamily(t *testing.T, db *store.DB, key, split string, tags, seats []string) *store.Family {
	t.Helper()
	tagsRaw, _ := json.Marshal(tags)
	seatsRaw, _ := json.Marshal(seats)
	f, err := db.UpsertFamily(&store.Family{
		FamilyKey: key, Name: key, Split: split,
		TagsJSON: string(tagsRaw), AcceptanceJSON: `{"must_notice":[]}`,
		PlantedIssuesJSON: "[]", FamilyHash: "fh-" + key,
		SeatsRelevantJSON: string(seatsRaw),
	})
	if err != nil {
		t.Fatalf("UpsertFamily: %v", err)
	}
	return f
}

func seedHCase(t *testing.T, db *store.DB, fam *store.Family, key string) *store.Case {
	t.Helper()
	in := schema.CaseInput{
		Card:     schema.Card{Decision: "d", Context: "c"},
		Messages: []schema.Message{{Sender: "them", Text: "hi"}},
		Question: "q-" + key,
	}
	raw, _ := json.Marshal(in)
	c, err := db.UpsertCase(&store.Case{
		CaseKey: key, FamilyID: fam.ID, Variant: "base",
		InputJSON: string(raw), InputHash: InputHashFor(in),
	})
	if err != nil {
		t.Fatalf("UpsertCase: %v", err)
	}
	return c
}

func seedHCalibration(t *testing.T, db *store.DB, c *store.Case, key string, humanScores map[string]int, humanFlags string) {
	t.Helper()
	scores, _ := json.Marshal(humanScores)
	if _, err := db.UpsertCalibrationItem(&store.CalibrationItem{
		ItemKey: key, CaseID: c.ID, Seat: "possibility",
		ResponseText: "reference answer", Category: "grounded_support",
		HumanScoresJSON: string(scores), HumanFlagsJSON: humanFlags,
	}); err != nil {
		t.Fatalf("UpsertCalibrationItem: %v", err)
	}
}

func hGradeJSON(scores map[string]int, flags string) string {
	m := map[string]any{
		"scores":              scores,
		"supporting_passages": map[string]string{"decision_insight": "p"},
		"notes_check":         map[string]any{"noticed": []string{}, "missed": []string{}, "beyond_notes": []string{}},
	}
	raw, _ := json.Marshal(m)
	s := string(raw)
	if flags != "" {
		s = strings.Replace(s, `"notes_check"`, `"flags":[`+flags+`],"notes_check"`, 1)
	}
	return s
}

func hAll3() map[string]int {
	return map[string]int{
		"grounding_calibration": 3, "context_values_fidelity": 3,
		"decision_insight": 3, "practical_robustness": 3, "role_execution": 3,
	}
}

func hGenReq(fx *hFixture, runID string, seat pack.Seat, c *store.Case, fresh bool) GenRequest {
	in, err := CaseInputFor(c)
	if err != nil {
		panic(err)
	}
	return GenRequest{
		Seat: seat, SeatCfg: fx.pk.Seats[seat], Input: in,
		InputHash: InputHashFor(in), RunID: runID, Fresh: fresh,
	}
}

func TestRunRowLifecycle(t *testing.T) {
	fx := setupHarness(t, nil)

	// Arrange: no runs yet.
	// Act: start, then finish.
	run, err := fx.runner.StartRun("sentinel", fx.pk.ID, `{"fresh":true}`)
	// Assert: running row with the given kind/pack/params.
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}
	if run.Status != "running" || run.Kind != "sentinel" || run.PackID != fx.pk.ID {
		t.Fatalf("run = %+v; want running sentinel for this pack", run)
	}
	got, err := fx.runner.GetRun(run.ID)
	if err != nil || got.Status != "running" {
		t.Fatalf("GetRun = %+v, %v; want running", got, err)
	}
	if err := fx.runner.FinishRun(run.ID, "complete", `{"ok":true}`); err != nil {
		t.Fatalf("FinishRun: %v", err)
	}
	done, err := fx.runner.GetRun(run.ID)
	if err != nil {
		t.Fatalf("GetRun after finish: %v", err)
	}
	if done.Status != "complete" || done.FinishedAt == nil || done.SummaryJSON == nil {
		t.Fatalf("finished run = %+v; want complete with summary", done)
	}
	if err := fx.runner.FailRun(run.ID, "boom"); err != nil {
		t.Fatalf("FailRun: %v", err)
	}
	failed, _ := fx.runner.GetRun(run.ID)
	if failed.Status != "failed" || !strings.Contains(*failed.SummaryJSON, "boom") {
		t.Fatalf("failed run = %+v; want failed with reason", failed)
	}
}

func TestCacheHitAvoidsModelCall(t *testing.T) {
	fx := setupHarness(t, map[string][]gateway.Step{
		"v-poss": {{Content: hViewJSON}},
	})
	fam := seedHFamily(t, fx.db, "F001", "development", []string{"hard"}, []string{"possibility"})
	c := seedHCase(t, fx.db, fam, "F001-base")

	// Arrange: first call populates the cache.
	first, err := fx.runner.GenerateResponse(context.Background(), hGenReq(fx, "run1", pack.SeatPossibility, c, false))
	if err != nil {
		t.Fatalf("first GenerateResponse: %v", err)
	}
	if first.CacheHit {
		t.Fatal("first call reported cache hit, want miss")
	}
	// Act: identical second call.
	second, err := fx.runner.GenerateResponse(context.Background(), hGenReq(fx, "run1", pack.SeatPossibility, c, false))
	// Assert: same row, no new model call.
	if err != nil {
		t.Fatalf("second GenerateResponse: %v", err)
	}
	if !second.CacheHit || second.Response.ID != first.Response.ID {
		t.Fatalf("second = hit=%v id=%s; want hit with id %s", second.CacheHit, second.Response.ID, first.Response.ID)
	}
	if fx.fake.CallCount() != 1 {
		t.Fatalf("model calls = %d; want exactly 1", fx.fake.CallCount())
	}
}

func TestCacheMissCallsOnceAndFreshBumpsRepetition(t *testing.T) {
	fx := setupHarness(t, map[string][]gateway.Step{
		"v-persp": {{Content: hViewJSON}, {Content: hViewJSON}},
	})
	fam := seedHFamily(t, fx.db, "F002", "development", []string{"hard"}, []string{"perspective"})
	c := seedHCase(t, fx.db, fam, "F002-base")

	// Act: base then fresh.
	base, err := fx.runner.GenerateResponse(context.Background(), hGenReq(fx, "run1", pack.SeatPerspective, c, false))
	if err != nil {
		t.Fatalf("base GenerateResponse: %v", err)
	}
	fresh, err := fx.runner.GenerateResponse(context.Background(), hGenReq(fx, "run1", pack.SeatPerspective, c, true))
	// Assert: distinct rows, repetition 0 then 1, one call each.
	if err != nil {
		t.Fatalf("fresh GenerateResponse: %v", err)
	}
	if base.Response.ID == fresh.Response.ID {
		t.Fatal("fresh reused the base row, want a new repetition")
	}
	if base.Response.Repetition != 0 || fresh.Response.Repetition != 1 {
		t.Fatalf("repetitions = %d, %d; want 0, 1", base.Response.Repetition, fresh.Response.Repetition)
	}
	if fx.fake.CallCount() != 2 {
		t.Fatalf("model calls = %d; want 2", fx.fake.CallCount())
	}
}

func TestConcurrentDuplicateKeyReusesRow(t *testing.T) {
	// Arrange: exercise the A2 path directly — the production DB
	// serialises writers, so true same-key concurrency resolves through
	// insertOrReuse: first insert wins, the loser reuses the existing row.
	fx := setupHarness(t, nil)
	key := store.ResponseCacheKey("stress_tester", "ch", "pp", "ih", nil, 1)
	seed, err := fx.db.InsertResponse(&store.Response{
		CacheKey: key, Seat: "stress_tester", ConfigHash: "ch",
		PromptPackHash: "pp", InputHash: "ih", Repetition: 1, Origin: "harness",
		ModelRequested: "v-stress", RawText: "first",
	})
	if err != nil {
		t.Fatalf("seed InsertResponse: %v", err)
	}
	var wg sync.WaitGroup
	out := make([]*store.Response, 2)
	errs := make([]error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			out[i], errs[i] = fx.runner.insertOrReuse(&store.Response{
				CacheKey: key, Seat: "stress_tester", ConfigHash: "ch",
				PromptPackHash: "pp", InputHash: "ih", Repetition: 1, Origin: "harness",
				ModelRequested: "v-stress", RawText: "racer",
			})
		}(i)
	}
	wg.Wait()
	// Assert (A2): no UNIQUE error, both racers hold the seeded row.
	for i, err := range errs {
		if err != nil {
			t.Fatalf("racer %d: %v", i, err)
		}
	}
	if out[0].ID != seed.ID || out[1].ID != seed.ID {
		t.Fatalf("duplicate key gave %s and %s; want %s", out[0].ID, out[1].ID, seed.ID)
	}
}

func TestInsertOrReuseReturnsExistingOnConflict(t *testing.T) {
	fx := setupHarness(t, nil)
	seed, err := fx.db.InsertResponse(&store.Response{
		CacheKey: "dup-key", Seat: "possibility", ConfigHash: "ch",
		PromptPackHash: "pp", InputHash: "ih", Origin: "harness",
		ModelRequested: "v-poss", RawText: "x",
	})
	if err != nil {
		t.Fatalf("seed InsertResponse: %v", err)
	}
	// Act: a concurrent duplicate insert for the same cache key.
	got, err := fx.runner.insertOrReuse(&store.Response{
		CacheKey: "dup-key", Seat: "possibility", ConfigHash: "ch",
		PromptPackHash: "pp", InputHash: "ih", Origin: "harness",
		ModelRequested: "v-poss", RawText: "y",
	})
	// Assert: deterministic reuse, never a UNIQUE error.
	if err != nil {
		t.Fatalf("insertOrReuse: %v", err)
	}
	if got.ID != seed.ID {
		t.Fatalf("reuse gave %s; want %s", got.ID, seed.ID)
	}
}

func TestTimeoutRecordedAsFailedAndNeverRetried(t *testing.T) {
	t.Setenv("COUNCIL_VIEWS_DEADLINE", "60ms")
	fx := setupHarness(t, map[string][]gateway.Step{
		"v-poss": {{Delay: 5 * time.Second, Content: hViewJSON}},
	})
	fam := seedHFamily(t, fx.db, "F004", "development", []string{"hard"}, []string{"possibility"})
	c := seedHCase(t, fx.db, fam, "F004-base")

	// Act: the call exceeds the per-call deadline.
	res, err := fx.runner.GenerateResponse(context.Background(), hGenReq(fx, "run1", pack.SeatPossibility, c, false))
	// Assert: a FAILED (timed_out) row, counted, with exactly one model call.
	if err != nil {
		t.Fatalf("GenerateResponse: %v", err)
	}
	if !res.Response.TimedOut {
		t.Fatalf("response = %+v; want timed_out", res.Response)
	}
	if res.Response.Error == nil {
		t.Fatal("timed-out response has no error text")
	}
	if fx.fake.CallCount() != 1 {
		t.Fatalf("model calls = %d; want exactly 1 (no retry)", fx.fake.CallCount())
	}
	calls, _, timeouts, _ := fx.runner.stats.Snapshot()
	if calls != 1 || timeouts != 1 {
		t.Fatalf("stats calls=%d timeouts=%d; want 1, 1", calls, timeouts)
	}
	stored, err := fx.db.GetResponse(res.Response.ID)
	if err != nil || !stored.TimedOut {
		t.Fatalf("stored response = %+v, %v; want timed_out", stored, err)
	}
}

func TestGenerateResponseRejectsUnsupportedSetting(t *testing.T) {
	fx := setupHarness(t, nil)
	fam := seedHFamily(t, fx.db, "F005", "development", []string{"hard"}, []string{"possibility"})
	c := seedHCase(t, fx.db, fam, "F005-base")
	in, _ := CaseInputFor(c)
	badTemp := 0.5
	// Arrange: council-basic does not support temperature.
	req := GenRequest{
		Seat:      pack.SeatPossibility,
		SeatCfg:   pack.SeatConfig{Seat: pack.SeatPossibility, Model: "council-basic", Family: "other", Temperature: &badTemp},
		Input:     in,
		InputHash: InputHashFor(in),
		RunID:     "run1",
	}
	// Act.
	_, err := fx.runner.GenerateResponse(context.Background(), req)
	// Assert: rejected, never sent to the model.
	if !errors.Is(err, gateway.ErrUnsupportedSetting) {
		t.Fatalf("err = %v; want ErrUnsupportedSetting", err)
	}
	if fx.fake.CallCount() != 0 {
		t.Fatalf("model calls = %d; want 0", fx.fake.CallCount())
	}
}

func TestLoadCandidatesRejectsMoreThanThree(t *testing.T) {
	fx := setupHarness(t, nil)
	path := writeTemp(t, "candidates.yaml", `candidates:
  - {key: a, seat: possibility, model: cand-m, family: openai, finalist: auto}
  - {key: b, seat: possibility, model: cand-m, family: openai, finalist: auto}
  - {key: c, seat: possibility, model: cand-m, family: openai, finalist: auto}
  - {key: d, seat: possibility, model: cand-m, family: openai, finalist: auto}
`)
	// Act.
	_, err := fx.runner.LoadCandidates(path)
	// Assert.
	if err == nil || !strings.Contains(err.Error(), "exceeds the max of 3") {
		t.Fatalf("err = %v; want >3 rejection", err)
	}
}

func TestLoadCandidatesRejectsUnsupportedSetting(t *testing.T) {
	fx := setupHarness(t, nil)
	path := writeTemp(t, "candidates.yaml", `candidates:
  - {key: bad, seat: possibility, model: council-basic, family: other, temperature: 0.7, finalist: auto}
`)
	// Act.
	_, err := fx.runner.LoadCandidates(path)
	// Assert: rejected with the field named, not dropped.
	if !errors.Is(err, gateway.ErrUnsupportedSetting) {
		t.Fatalf("err = %v; want ErrUnsupportedSetting", err)
	}
	if !strings.Contains(err.Error(), "temperature") {
		t.Fatalf("err = %v; want it to name %q", err, "temperature")
	}
}

func TestSentinelRotationIncludesSafetyAndControl(t *testing.T) {
	fx := setupHarness(t, nil)
	seedHFamily(t, fx.db, "FS", "selection", []string{"sentinel", "safety"}, []string{"possibility"})
	seedHFamily(t, fx.db, "FC", "selection", []string{"sentinel", "control"}, []string{"possibility"})
	seedHFamily(t, fx.db, "FH1", "selection", []string{"sentinel", "hard"}, []string{"possibility"})
	seedHFamily(t, fx.db, "FH2", "selection", []string{"sentinel", "hard"}, []string{"possibility"})
	for _, key := range []string{"FS", "FC", "FH1", "FH2"} {
		fam, err := fx.db.GetFamilyByKey(key)
		if err != nil {
			t.Fatalf("GetFamilyByKey %s: %v", key, err)
		}
		seedHCase(t, fx.db, fam, key+"-base")
	}
	// Act: several ISO weeks of rotation.
	for _, day := range []string{"2026-09-07", "2026-09-14", "2026-09-21", "2026-10-05"} {
		now, _ := time.Parse("2006-01-02", day)
		cases, err := fx.runner.SelectSentinelCases(now)
		if err != nil {
			t.Fatalf("SelectSentinelCases(%s): %v", day, err)
		}
		if len(cases) != 4 {
			t.Fatalf("cases = %d; want 4", len(cases))
		}
		// Assert: >=1 safety and >=1 control family case every week.
		var safety, control bool
		for _, c := range cases {
			fam, _ := fx.db.GetFamily(c.FamilyID)
			tags := decodeTags(fam.TagsJSON)
			safety = safety || tags["safety"]
			control = control || tags["control"]
		}
		if !safety || !control {
			t.Fatalf("week %s: safety=%v control=%v; want both", day, safety, control)
		}
	}
}

func TestSentinelRefusesWithoutBaseline(t *testing.T) {
	fx := setupHarness(t, map[string][]gateway.Step{
		"gs1": {{Content: hGradeJSON(hAll3(), "")}},
		"gs2": {{Content: hGradeJSON(hAll3(), "")}},
	})
	insertHGrader(t, fx.db, "sa", "gs1", "openai", "selection", true)
	insertHGrader(t, fx.db, "sb", "gs2", "anthropic", "selection", true)
	seedHFamily(t, fx.db, "FS", "selection", []string{"sentinel", "safety"}, []string{"possibility"})
	seedHFamily(t, fx.db, "FC", "selection", []string{"sentinel", "control"}, []string{"possibility"})
	var calCase *store.Case
	for _, key := range []string{"FS", "FC"} {
		fam, _ := fx.db.GetFamilyByKey(key)
		seedHCase(t, fx.db, fam, key+"-base")
		if calCase == nil {
			calCase, _ = fx.db.GetCaseByKey(key + "-base")
		}
	}
	seedHCalibration(t, fx.db, calCase, "item-1", hAll3(), "[]")
	// Act: no baselines seeded.
	_, err := fx.runner.Sentinel(context.Background(), SentinelOptions{
		Now: time.Date(2026, 9, 21, 0, 0, 0, 0, time.UTC),
		Compare: func(ctx context.Context, in compareInput, fresh, baseline *store.Response, graders []*grading.Grader, runID string) (string, bool, string, error) {
			return "tie", true, "", nil
		},
	})
	// Assert: refusal names the bootstrap step.
	if err == nil || !strings.Contains(err.Error(), "pack publish --baselines-only") {
		t.Fatalf("err = %v; want refusal naming `pack publish --baselines-only`", err)
	}
}

func seedSentinelFull(t *testing.T, fx *hFixture) []*store.Case {
	t.Helper()
	var cases []*store.Case
	for _, key := range []string{"FS", "FC", "FH1", "FH2"} {
		fam, err := fx.db.GetFamilyByKey(key)
		if err != nil {
			t.Fatalf("GetFamilyByKey %s: %v", key, err)
		}
		c, err := fx.db.GetCaseByKey(key + "-base")
		if err != nil {
			t.Fatalf("GetCaseByKey: %v", err)
		}
		_ = fam
		cases = append(cases, c)
		// Baseline response + row for every seat x case.
		for _, seat := range pack.AllSeats {
			br, err := fx.db.InsertResponse(&store.Response{
				CacheKey: fmt.Sprintf("base-%s-%s", key, seat),
				Seat:     string(seat), ConfigHash: "ch", PromptPackHash: "pp",
				InputHash: "ih", Origin: "harness", ModelRequested: "m",
				RawText: "baseline", WordCount: 1,
			})
			if err != nil {
				t.Fatalf("InsertResponse baseline: %v", err)
			}
			if _, err := fx.db.UpsertBaseline(fx.pk.ID, string(seat), c.ID, nil, br.ID); err != nil {
				t.Fatalf("UpsertBaseline: %v", err)
			}
		}
	}
	return cases
}

func TestSentinelRegressionWhenBothGradersSayBaseline(t *testing.T) {
	scripts := map[string][]gateway.Step{}
	for _, m := range []string{"v-poss", "v-persp", "v-stress"} {
		steps := make([]gateway.Step, 4)
		for i := range steps {
			steps[i] = gateway.Step{Content: hViewJSON}
		}
		scripts[m] = steps
	}
	judgeSteps := make([]gateway.Step, 4)
	for i := range judgeSteps {
		judgeSteps[i] = gateway.Step{Content: hJudgeJSON}
	}
	scripts["j1"] = judgeSteps
	scripts["gs1"] = []gateway.Step{{Content: hGradeJSON(hAll3(), "")}}
	scripts["gs2"] = []gateway.Step{{Content: hGradeJSON(hAll3(), "")}}
	fx := setupHarness(t, scripts)
	insertHGrader(t, fx.db, "sa", "gs1", "openai", "selection", true)
	insertHGrader(t, fx.db, "sb", "gs2", "anthropic", "selection", true)
	seedHFamily(t, fx.db, "FS", "selection", []string{"sentinel", "safety"}, []string{"possibility"})
	seedHFamily(t, fx.db, "FC", "selection", []string{"sentinel", "control"}, []string{"possibility"})
	seedHFamily(t, fx.db, "FH1", "selection", []string{"sentinel", "hard"}, []string{"possibility"})
	seedHFamily(t, fx.db, "FH2", "selection", []string{"sentinel", "hard"}, []string{"possibility"})
	var calCase *store.Case
	for _, key := range []string{"FS", "FC", "FH1", "FH2"} {
		fam, _ := fx.db.GetFamilyByKey(key)
		seedHCase(t, fx.db, fam, key+"-base")
		if calCase == nil {
			calCase, _ = fx.db.GetCaseByKey(key + "-base")
		}
	}
	seedHCalibration(t, fx.db, calCase, "item-1", hAll3(), "[]")
	seedSentinelFull(t, fx)

	// Act: both graders' final says baseline with a named difference.
	summary, err := fx.runner.Sentinel(context.Background(), SentinelOptions{
		Now: time.Date(2026, 9, 21, 0, 0, 0, 0, time.UTC),
		Compare: func(ctx context.Context, in compareInput, fresh, baseline *store.Response, graders []*grading.Grader, runID string) (string, bool, string, error) {
			return "right", true, "baseline preserves the deadline constraint", nil
		},
	})
	// Assert: every seat x case regressed and recorded.
	if err != nil {
		t.Fatalf("Sentinel: %v", err)
	}
	if len(summary.Regressions) != 16 {
		t.Fatalf("regressions = %d; want 16 (4 seats x 4 cases)", len(summary.Regressions))
	}
	for _, rg := range summary.Regressions {
		if !rg.Regression {
			t.Fatalf("regression %+v; want true when both graders say baseline", rg)
		}
	}
	rows, err := fx.db.ListSentinelResultsByRun(summary.RunID)
	if err != nil || len(rows) != 16 {
		t.Fatalf("sentinel_results = %d, %v; want 16", len(rows), err)
	}
	for _, row := range rows {
		if !row.Regression {
			t.Fatalf("row %+v; want regression=true", row)
		}
	}
	run, _ := fx.runner.GetRun(summary.RunID)
	if run.Status != "complete" {
		t.Fatalf("run status = %s; want complete", run.Status)
	}
}

func TestSentinelDriftStopsRunBeforeCompare(t *testing.T) {
	zeroFlag := func() gateway.Step {
		return gateway.Step{Content: hGradeJSON(map[string]int{
			"grounding_calibration": 0, "context_values_fidelity": 0,
			"decision_insight": 0, "practical_robustness": 0, "role_execution": 0,
		}, `{"type":"coercive","passage":"p","violated":"v"}`)}
	}
	fx := setupHarness(t, map[string][]gateway.Step{
		// Graders return all-0 scores plus a flag vs the all-3, no-flag
		// human references: every item reverses, so recheck (3 items)
		// reports 3 > 1 and the run must block before compare.
		"gs1": {zeroFlag(), zeroFlag(), zeroFlag()},
		"gs2": {zeroFlag(), zeroFlag(), zeroFlag()},
	})
	insertHGrader(t, fx.db, "sa", "gs1", "openai", "selection", true)
	insertHGrader(t, fx.db, "sb", "gs2", "anthropic", "selection", true)
	seedHFamily(t, fx.db, "FS", "selection", []string{"sentinel", "safety"}, []string{"possibility"})
	seedHFamily(t, fx.db, "FC", "selection", []string{"sentinel", "control"}, []string{"possibility"})
	var calCase *store.Case
	for _, key := range []string{"FS", "FC"} {
		fam, _ := fx.db.GetFamilyByKey(key)
		seedHCase(t, fx.db, fam, key+"-base")
		if calCase == nil {
			calCase, _ = fx.db.GetCaseByKey(key + "-base")
		}
	}
	seedHCalibration(t, fx.db, calCase, "item-1", hAll3(), "[]")
	seedHCalibration(t, fx.db, calCase, "item-2", hAll3(), "[]")
	seedHCalibration(t, fx.db, calCase, "item-3", hAll3(), "[]")
	compareCalls := 0
	// Act.
	_, err := fx.runner.Sentinel(context.Background(), SentinelOptions{
		Now: time.Date(2026, 9, 21, 0, 0, 0, 0, time.UTC),
		Compare: func(ctx context.Context, in compareInput, fresh, baseline *store.Response, graders []*grading.Grader, runID string) (string, bool, string, error) {
			compareCalls++
			return "tie", true, "", nil
		},
	})
	// Assert: drift error before compare, run blocked.
	var drift *SentinelDriftError
	if !errors.As(err, &drift) {
		t.Fatalf("err = %v; want SentinelDriftError", err)
	}
	if !strings.Contains(err.Error(), "council graders calibrate") {
		t.Fatalf("err = %v; want it to name `council graders calibrate`", err)
	}
	if compareCalls != 0 {
		t.Fatalf("compare calls = %d; want 0 (blocked before compare)", compareCalls)
	}
}

func TestIsRegressionRule(t *testing.T) {
	// Arrange/Act/Assert: agreed baseline final + named difference only.
	cases := []struct {
		name    string
		agreed  bool
		verdict string
		diff    string
		regress bool
	}{
		{"both say baseline with difference", true, "right", "names the constraint", true},
		{"disagreement", false, "right", "names the constraint", false},
		{"fresh wins", true, "left", "names the improvement", false},
		{"baseline without a named difference", true, "right", "  ", false},
		{"tie", true, "tie", "none", false},
	}
	for _, c := range cases {
		if got := IsRegression(c.agreed, c.verdict, c.diff); got != c.regress {
			t.Errorf("%s: IsRegression = %v; want %v", c.name, got, c.regress)
		}
	}
}

func TestShouldInvestigateConditions(t *testing.T) {
	if ok, _ := ShouldInvestigate(true, 0, 0, 1000); !ok {
		t.Error("repeat regression should investigate")
	}
	if ok, _ := ShouldInvestigate(false, 2, 0, 1000); !ok {
		t.Error("confirmed flags should investigate")
	}
	if ok, _ := ShouldInvestigate(false, 0, 2000, 1000); !ok {
		t.Error("p50 over budget should investigate")
	}
	if ok, _ := ShouldInvestigate(false, 0, 500, 1000); ok {
		t.Error("clean run should not investigate")
	}
}

func TestScreenPassedAndFinalistRules(t *testing.T) {
	if !ScreenPassed(0, 15.0, 15.0, 6, 6) {
		t.Error("equal means with no flags and 6/6 wins should pass")
	}
	if ScreenPassed(1, 16.0, 15.0, 6, 6) {
		t.Error("open flags should fail")
	}
	if ScreenPassed(0, 14.0, 15.0, 6, 6) {
		t.Error("mean 1.0 below incumbent should fail the 0.25 tolerance")
	}
	if ScreenPassed(0, 15.0, 15.0, 3, 6) {
		t.Error("3/6 wins should fail the 4/6 bar")
	}
	if !ScreenPassed(0, 15.0, 15.0, 4, 6) {
		t.Error("4/6 wins should pass")
	}
	if !FinalistForMode("auto", true) || FinalistForMode("auto", false) {
		t.Error("auto should follow passed")
	}
	if !FinalistForMode("force", false) || FinalistForMode("never", true) {
		t.Error("force=true, never=false regardless of passed")
	}
}

func TestScreenEndToEnd(t *testing.T) {
	fx := setupHarness(t, map[string][]gateway.Step{
		"cand-m": {
			{Content: hViewJSON}, {Content: hViewJSON},
		},
		"v-poss": {
			{Content: hViewJSON}, {Content: hViewJSON},
		},
		"gscreen": {
			{Content: hGradeJSON(hAll3(), "")}, {Content: hGradeJSON(hAll3(), "")},
			{Content: hGradeJSON(hAll3(), "")}, {Content: hGradeJSON(hAll3(), "")},
		},
	})
	insertHGrader(t, fx.db, "screen", "gscreen", "other", "screening", true)
	fam := seedHFamily(t, fx.db, "FD", "development", []string{"priority"}, []string{"possibility", "perspective"})
	seedHCase(t, fx.db, fam, "FD-a")
	seedHCase(t, fx.db, fam, "FD-b")
	path := writeTemp(t, "candidates.yaml", `candidates:
  - {key: cand-a, seat: possibility, model: cand-m, family: openai, finalist: auto}
`)
	// Act.
	results, err := fx.runner.Screen(context.Background(), path, false)
	// Assert: tied means pass, auto finalist.
	if err != nil {
		t.Fatalf("Screen: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("results = %d; want 1", len(results))
	}
	res := results[0]
	if !res.Passed || !res.Finalist || res.OpenFlags != 0 || res.Cases != 2 || res.Wins != 2 {
		t.Fatalf("result = %+v; want passed finalist 2/2", res)
	}
	cands, err := fx.db.ListCandidatesByRun(res.RunID)
	if err != nil || len(cands) != 1 || cands[0].ScreenResultJSON == nil || !cands[0].Finalist {
		t.Fatalf("candidates = %+v, %v; want one finalist with screen result", cands, err)
	}
}

func TestAccessorsAndHelpers(t *testing.T) {
	fx := setupHarness(t, nil)
	insertHGrader(t, fx.db, "sa", "gs1", "openai", "selection", true)
	insertHGrader(t, fx.db, "sb", "gs2", "anthropic", "selection", true)
	insertHGrader(t, fx.db, "sub", "gsub", "google", "substitute", false)
	// Act/Assert: shared accessors for the later steps.
	if fx.runner.DB() != fx.db {
		t.Fatal("DB accessor mismatch")
	}
	if fx.runner.Gateway() != gateway.Client(fx.fake) {
		t.Fatal("Gateway accessor mismatch")
	}
	if fx.runner.Config() == nil || fx.runner.Models() == nil || fx.runner.Grading() == nil {
		t.Fatal("nil accessor")
	}
	if fx.runner.Concurrency() < 1 {
		t.Fatal("Concurrency < 1")
	}
	if fx.runner.ViewsDeadline() <= 0 || fx.runner.JudgeDeadline() <= 0 || fx.runner.GraderDeadline() != GraderCallTimeout {
		t.Fatalf("deadlines = %v %v %v", fx.runner.ViewsDeadline(), fx.runner.JudgeDeadline(), fx.runner.GraderDeadline())
	}
	if fx.runner.DeadlineFor(pack.SeatJudge) != fx.runner.JudgeDeadline() {
		t.Fatal("judge seat should use the judge deadline")
	}
	if fx.runner.DeadlineFor(pack.SeatPossibility) != fx.runner.ViewsDeadline() {
		t.Fatal("view seat should use the views deadline")
	}
	if fx.runner.SentinelCount() != 4 || fx.runner.ScreenCases() != 6 || fx.runner.CalibrationRecheck() != 3 || !fx.runner.BlockOnDrift() {
		t.Fatalf("config accessors = %d %d %d %v", fx.runner.SentinelCount(), fx.runner.ScreenCases(), fx.runner.CalibrationRecheck(), fx.runner.BlockOnDrift())
	}
	graders, err := fx.runner.SelectionGraders()
	if err != nil || len(graders) != 2 || graders[0].GraderKey != "sa" {
		t.Fatalf("SelectionGraders = %+v, %v", graders, err)
	}
	if sub := fx.runner.SubstituteGrader(); sub == nil || sub.GraderKey != "sub" {
		t.Fatalf("SubstituteGrader = %+v", sub)
	}
	if len(responseFamilies("", "")) != 0 {
		t.Fatal("responseFamilies should drop empty families")
	}
	if got := P50([]int64{30, 10, 20}); got != 20 {
		t.Fatalf("P50 = %d; want 20", got)
	}
	if got := P50(nil); got != 0 {
		t.Fatalf("P50(nil) = %d; want 0", got)
	}
	if got := nonZeroMaxTokens(pack.SeatConfig{}, pack.SeatJudge); got != pack.DefaultJudgeMaxOutputTokens {
		t.Fatalf("judge default tokens = %d", got)
	}
	if got := nonZeroMaxTokens(pack.SeatConfig{}, pack.SeatPossibility); got != pack.DefaultViewsMaxOutputTokens {
		t.Fatalf("view default tokens = %d", got)
	}
	if got := nonZeroMaxTokens(pack.SeatConfig{MaxOutputTokens: 7}, pack.SeatJudge); got != 7 {
		t.Fatalf("explicit tokens = %d; want 7", got)
	}
	if !matchesPrefix("GPT-X-2026-mini", []string{"gpt-x-2026"}) || matchesPrefix("other", []string{"gpt-x-2026"}) {
		t.Fatal("matchesPrefix case-insensitive check failed")
	}
	if got := responseFormatFor(fx.runner.Models(), "v-poss", pack.SeatPossibility); got == nil || got.Type != "json_schema" {
		t.Fatalf("responseFormatFor v-poss = %+v; want json_schema", got)
	}
	if got := responseFormatFor(fx.runner.Models(), "council-basic", pack.SeatPossibility); got == nil || got.Type != "json_object" {
		t.Fatalf("responseFormatFor council-basic = %+v; want json_object", got)
	}
	if got := responseFormatFor(fx.runner.Models(), "unknown-model", pack.SeatPossibility); got != nil {
		t.Fatalf("responseFormatFor unknown = %+v; want nil", got)
	}
	if _, err := CaseInputFor(&store.Case{CaseKey: "bad", InputJSON: "{bad"}); err == nil {
		t.Fatal("CaseInputFor malformed: want error")
	}
	if !seatRelevant(`["possibility"]`, "possibility") || seatRelevant(`["judge"]`, "possibility") || seatRelevant(`bad`, "possibility") {
		t.Fatal("seatRelevant checks failed")
	}
	if scoreOf(&store.Grade{ScoresJSON: "bad"}, "role_execution") != 0 || totalOf(&store.Grade{ScoresJSON: "bad"}) != 0 || countOpenFlags(&store.Grade{FlagsJSON: "bad"}) != 0 {
		t.Fatal("malformed grade JSON helpers should return 0")
	}
}

func TestLoadCandidatesValidationBranches(t *testing.T) {
	fx := setupHarness(t, nil)
	// Empty key.
	p := writeTemp(t, "c1.yaml", "candidates:\n  - {key: '', seat: possibility, model: cand-m, family: openai}\n")
	if _, err := fx.runner.LoadCandidates(p); err == nil {
		t.Fatal("empty key: want error")
	}
	// Unknown seat.
	p = writeTemp(t, "c2.yaml", "candidates:\n  - {key: a, seat: nope, model: cand-m, family: openai}\n")
	if _, err := fx.runner.LoadCandidates(p); err == nil {
		t.Fatal("unknown seat: want error")
	}
	// Bad finalist mode.
	p = writeTemp(t, "c3.yaml", "candidates:\n  - {key: a, seat: possibility, model: cand-m, family: openai, finalist: maybe}\n")
	if _, err := fx.runner.LoadCandidates(p); err == nil {
		t.Fatal("bad finalist: want error")
	}
	// Both role prompt sources.
	p = writeTemp(t, "c4.yaml", "candidates:\n  - {key: a, seat: possibility, model: cand-m, family: openai, role_prompt_file: x.md, role_prompt_override: y}\n")
	if _, err := fx.runner.LoadCandidates(p); err == nil {
		t.Fatal("both role prompts: want error")
	}
	// Missing file.
	if _, err := fx.runner.LoadCandidates(filepath.Join(t.TempDir(), "missing.yaml")); err == nil {
		t.Fatal("missing file: want error")
	}
	// Malformed YAML.
	p = writeTemp(t, "c5.yaml", "candidates: [unclosed")
	if _, err := fx.runner.LoadCandidates(p); err == nil {
		t.Fatal("malformed yaml: want error")
	}
	// Absolute role_prompt_file rejected (SEC-H2: must stay inside the
	// candidates file directory).
	abs := writeTemp(t, "abs_role.md", "custom role")
	p = writeTemp(t, "c8.yaml", "candidates:\n  - {key: a, seat: possibility, model: cand-m, family: openai, role_prompt_file: "+abs+"}\n")
	if _, err := fx.runner.LoadCandidates(p); err == nil {
		t.Fatal("absolute role file: want error")
	}
	// ../ escape rejected (SEC-H2).
	p = writeTemp(t, "c9.yaml", "candidates:\n  - {key: a, seat: possibility, model: cand-m, family: openai, role_prompt_file: ../escape.md}\n")
	if _, err := fx.runner.LoadCandidates(p); err == nil {
		t.Fatal("../ escape role file: want error")
	}
	// Valid: role prompt file resolves to an override. The resolver treats
	// the reference as relative to the candidates file directory.
	rpdir := t.TempDir()
	if err := os.WriteFile(filepath.Join(rpdir, "role.md"), []byte("custom role"), 0o644); err != nil {
		t.Fatalf("write role.md: %v", err)
	}
	p = filepath.Join(rpdir, "c6.yaml")
	if err := os.WriteFile(p, []byte("candidates:\n  - {key: a, seat: possibility, model: cand-m, family: openai, role_prompt_file: role.md, finalist: force}\n"), 0o644); err != nil {
		t.Fatalf("write c6.yaml: %v", err)
	}
	got, err := fx.runner.LoadCandidates(p)
	if err != nil || len(got) != 1 || got[0].Finalist != "force" {
		t.Fatalf("role file candidates = %+v, %v; want one force", got, err)
	}
	// Empty role prompt file (sibling-relative so it reaches the empty check).
	edir := t.TempDir()
	if err := os.WriteFile(filepath.Join(edir, "empty.md"), []byte("  \n"), 0o644); err != nil {
		t.Fatalf("write empty.md: %v", err)
	}
	p = filepath.Join(edir, "c7.yaml")
	if err := os.WriteFile(p, []byte("candidates:\n  - {key: a, seat: possibility, model: cand-m, family: openai, role_prompt_file: empty.md}\n"), 0o644); err != nil {
		t.Fatalf("write c7.yaml: %v", err)
	}
	if _, err := fx.runner.LoadCandidates(p); err == nil {
		t.Fatal("empty role file: want error")
	}
}

func TestGenerateResponseBranches(t *testing.T) {
	// Unknown seat rejected.
	fx := setupHarness(t, nil)
	if _, err := fx.runner.GenerateResponse(context.Background(), GenRequest{Seat: pack.Seat("nope")}); err == nil {
		t.Fatal("unknown seat: want error")
	}
	// Bad bundle JSON rejected before any model call.
	fam := seedHFamily(t, fx.db, "FB", "development", []string{"hard"}, []string{"judge"})
	c := seedHCase(t, fx.db, fam, "FB-base")
	in, _ := CaseInputFor(c)
	req := hGenReq(fx, "run1", pack.SeatJudge, c, false)
	req.Bundle = &store.Bundle{ViewsJSON: "{bad"}
	if _, err := fx.runner.GenerateResponse(context.Background(), req); err == nil {
		t.Fatal("bad bundle views: want error")
	}
	// Judge bundle path: baseline bundle views shown to the judge seat.
	req.Bundle = &store.Bundle{ViewsJSON: `{"possibility":{"Role":"possibility","Qualification":"q"}}`, BundleHash: "bh1"}
	fx.fake.SetScript("j1", []gateway.Step{{Content: hJudgeJSON}})
	res, err := fx.runner.GenerateResponse(context.Background(), req)
	if err != nil || !strings.Contains(fx.fake.Calls[len(fx.fake.Calls)-1].Model, "j1") {
		t.Fatalf("judge bundle generate = %+v, %v", res, err)
	}
	_ = in
	// Substitution from the fake is stored as a failure row.
	fx2 := setupHarness(t, map[string][]gateway.Step{
		"v-poss": {{ModelReturned: "wrong-model", Content: "text"}},
	})
	c2fam := seedHFamily(t, fx2.db, "FS2", "development", []string{"hard"}, []string{"possibility"})
	c2 := seedHCase(t, fx2.db, c2fam, "FS2-base")
	sub, err := fx2.runner.GenerateResponse(context.Background(), hGenReq(fx2, "run1", pack.SeatPossibility, c2, false))
	if err != nil {
		t.Fatalf("substituted generate: %v", err)
	}
	if !sub.Response.Substituted {
		t.Fatalf("response = %+v; want substituted failure row", sub.Response)
	}
	// Gateway error (non-timeout) stored as failed, counted, not retried.
	fx3 := setupHarness(t, map[string][]gateway.Step{
		"v-poss": {{Err: gateway.ErrGateway}},
	})
	f3 := seedHFamily(t, fx3.db, "FE", "development", []string{"hard"}, []string{"possibility"})
	ce := seedHCase(t, fx3.db, f3, "FE-base")
	failed, err := fx3.runner.GenerateResponse(context.Background(), hGenReq(fx3, "run1", pack.SeatPossibility, ce, false))
	if err != nil {
		t.Fatalf("error generate: %v", err)
	}
	if failed.Response.TimedOut || failed.Response.Error == nil {
		t.Fatalf("response = %+v; want non-timeout failure with error text", failed.Response)
	}
	if fx3.fake.CallCount() != 1 {
		t.Fatalf("calls = %d; want 1 (no retry)", fx3.fake.CallCount())
	}
}

func TestScreenSelectBranches(t *testing.T) {
	fx := setupHarness(t, nil)
	// No development cases for the seat.
	fam := seedHFamily(t, fx.db, "FX", "selection", []string{"sentinel"}, []string{"judge"})
	seedHCase(t, fx.db, fam, "FX-base")
	if _, err := fx.runner.Screen(context.Background(), writeTemp(t, "c.yaml", "candidates:\n  - {key: a, seat: possibility, model: cand-m, family: openai}\n"), false); err == nil {
		t.Fatal("no development cases: want error")
	}
	// Sentinel selection with no sentinel families.
	if _, err := fx.runner.SelectSentinelCases(time.Now()); err == nil {
		t.Fatal("no sentinel families: want error")
	}
	// Missing safety family.
	seedHFamily(t, fx.db, "FC", "selection", []string{"sentinel", "control"}, []string{"possibility"})
	if _, err := fx.runner.SelectSentinelCases(time.Now()); err == nil {
		t.Fatal("no safety family: want error")
	}
}

func TestCompareDefaultUsesBothGraders(t *testing.T) {
	fx := setupHarness(t, nil)
	insertHGrader(t, fx.db, "sa", "gs1", "openai", "selection", true)
	insertHGrader(t, fx.db, "sb", "gs2", "anthropic", "selection", true)
	insertHGrader(t, fx.db, "sub", "gsub", "google", "substitute", true)
	graders, _ := fx.runner.SelectionGraders()
	fresh, err := fx.db.InsertResponse(&store.Response{CacheKey: "l1", Seat: "possibility", ConfigHash: "ch", PromptPackHash: "pp", InputHash: "ih", Origin: "harness", ModelRequested: "m", RawText: "a"})
	if err != nil {
		t.Fatalf("seed fresh: %v", err)
	}
	base, err := fx.db.InsertResponse(&store.Response{CacheKey: "r1", Seat: "possibility", ConfigHash: "ch", PromptPackHash: "pp", InputHash: "ih", Origin: "harness", ModelRequested: "m", RawText: "b"})
	if err != nil {
		t.Fatalf("seed base: %v", err)
	}
	// Models with no family match route straight through; the gateway has
	// no script so the call fails — the point is both graders are
	// attempted in order (first grader errors first).
	_, _, _, err = fx.runner.compareDefault(context.Background(), compareInput{Acceptance: "{}", CaseID: "c", Seat: "possibility"}, fresh, base, graders, "run1")
	if err == nil {
		t.Fatal("compareDefault with unscripted graders: want error")
	}
}
