package council

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/qoke/toughdecisions/internal/config"
	"github.com/qoke/toughdecisions/internal/gateway"
	"github.com/qoke/toughdecisions/internal/logx"
	"github.com/qoke/toughdecisions/internal/models"
	"github.com/qoke/toughdecisions/internal/pack"
	"github.com/qoke/toughdecisions/internal/schema"
	"github.com/qoke/toughdecisions/internal/store"
)

const (
	testViewJSON  = `{"urgent_danger":{"present":false},"qualification":"q","suggested_reply":"r","decisive_insight":"i","tradeoff_or_objection":"t","depends_on":"d","fallback":"f"}`
	testJudgeJSON = `{"urgent_danger":{"present":false},"qualification":"q","recommended_reply":"r","why":"w","accepted_cost":"c","next":{"immediate":"i","forward":"f"},"change_course_if":"cc"}`
	testPackYAML  = `seats:
  possibility: {model: v-poss, family: openai}
  perspective: {model: v-persp, family: openai}
  stress_tester: {model: v-stress, family: openai}
  judge: {model: j1, family: openai}
`
	testModelsYAML = `models:
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
`
	testCardJSON = `{"decision":"d","context":"c","priorities":"p","unusual":"u","history":"h","deadline":"dl","style":"s"}`
)

type testFixture struct {
	db     *store.DB
	fake   *gateway.Fake
	runner *Runner
	reg    *Registry
	thread string
}

func writeTemp(t *testing.T, name, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatalf("write temp file: %v", err)
	}
	return p
}

func setupFixture(t *testing.T, scripts map[string][]gateway.Step, viewsDL, judgeDL string) *testFixture {
	t.Helper()
	t.Setenv("COUNCIL_VIEWS_DEADLINE", viewsDL)
	t.Setenv("COUNCIL_JUDGE_DEADLINE", judgeDL)
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	db, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := pack.Init(db, writeTemp(t, "pack.yaml", testPackYAML)); err != nil {
		t.Fatalf("pack.Init: %v", err)
	}
	mreg, err := models.LoadRegistry(writeTemp(t, "models.yaml", testModelsYAML))
	if err != nil {
		t.Fatalf("LoadRegistry: %v", err)
	}
	th, err := db.CreateThread("t")
	if err != nil {
		t.Fatalf("CreateThread: %v", err)
	}
	if _, err := db.InsertCard(th.ID, testCardJSON); err != nil {
		t.Fatalf("InsertCard: %v", err)
	}
	fake := gateway.NewFake(scripts)
	reg := NewRegistry(5 * time.Minute)
	t.Cleanup(reg.Close)
	log := logx.NewWithWriter(cfg, testWriter{t})
	return &testFixture{db: db, fake: fake, runner: NewRunner(db, fake, reg, cfg, log, mreg), reg: reg, thread: th.ID}
}

type testWriter struct{ t *testing.T }

func (w testWriter) Write(p []byte) (int, error) { return len(p), nil }

func testCreateRequest(thread string) CreateRequest {
	return CreateRequest{
		ThreadID: thread,
		Messages: []schema.Message{{Sender: "them", Text: "hi", TS: "2026-09-01T10:00:00Z"}},
		Question: "what should I do?",
	}
}

func waitRequestState(t *testing.T, db *store.DB, id, want string, timeout time.Duration) *store.Request {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		agg, err := db.GetRequest(id)
		if err != nil {
			t.Fatalf("GetRequest: %v", err)
		}
		if agg.Request.State == want && agg.Request.FinishedAt != nil {
			complete := true
			for _, v := range agg.Views {
				if v.State == "pending" || (v.State == "complete" && v.ResponseID == nil) {
					complete = false
					break
				}
			}
			if complete {
				return agg.Request
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("request %s state = %q, want %q after %v", id, agg.Request.State, want, timeout)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func waitViewState(t *testing.T, db *store.DB, id, seat, want string, timeout time.Duration) {
	t.Helper()
	end := time.Now().Add(timeout)
	for {
		agg, err := db.GetRequest(id)
		if err != nil {
			t.Fatalf("GetRequest: %v", err)
		}
		for _, v := range agg.Views {
			if v.Seat == seat {
				if v.State == want {
					return
				}
				break
			}
		}
		if time.Now().After(end) {
			t.Fatalf("view %s never reached %q after %v", seat, want, timeout)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func viewBySeat(t *testing.T, db *store.DB, id, seat string) *store.RequestView {
	t.Helper()
	agg, err := db.GetRequest(id)
	if err != nil {
		t.Fatalf("GetRequest: %v", err)
	}
	for _, v := range agg.Views {
		if v.Seat == seat {
			return v
		}
	}
	t.Fatalf("no view for seat %q", seat)
	return nil
}

func fastScripts() map[string][]gateway.Step {
	return map[string][]gateway.Step{
		"v-poss":   {{Content: testViewJSON, ModelReturned: "v-poss"}},
		"v-persp":  {{Content: testViewJSON, ModelReturned: "v-persp"}},
		"v-stress": {{Content: testViewJSON, ModelReturned: "v-stress"}},
		"j1":       {{Content: testJudgeJSON, ModelReturned: "j1"}},
	}
}

func TestRunAllViewsCompleteOnTime(t *testing.T) {
	fx := setupFixture(t, fastScripts(), "100ms", "100ms")
	id, err := fx.runner.Run(context.Background(), testCreateRequest(fx.thread))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	waitRequestState(t, fx.db, id, "complete", 10*time.Second)
	if n := fx.fake.CallCount(); n != 4 {
		t.Fatalf("gateway calls = %d, want 4 (3 views + 1 judge)", n)
	}
	agg, err := fx.db.GetRequest(id)
	if err != nil {
		t.Fatalf("GetRequest: %v", err)
	}
	for _, v := range agg.Views {
		if v.State != "complete" || !v.IncludedInJudge || v.ResponseID == nil {
			t.Fatalf("view %s = %+v, want complete+included", v.Seat, v)
		}
	}
	if agg.Judge.State != "complete" || agg.Judge.NoIndependentViews {
		t.Fatalf("judge = %+v, want complete with views", agg.Judge)
	}
	if agg.Request.TFirstUsableViewMs == nil || agg.Request.TFinalMs == nil {
		t.Fatalf("timings missing: %+v", agg.Request)
	}
	if agg.Request.JudgeStartedAt == nil {
		t.Fatal("judge_started_at missing")
	}
}

func TestRunSlowViewLateAndPartial(t *testing.T) {
	scripts := fastScripts()
	scripts["v-stress"] = []gateway.Step{{Delay: 1500 * time.Millisecond, Content: testViewJSON, ModelReturned: "v-stress"}}
	fx := setupFixture(t, scripts, "100ms", "5s")
	id, err := fx.runner.Run(context.Background(), testCreateRequest(fx.thread))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	ch, unsub := fx.reg.Subscribe(id)
	defer unsub()
	seenLate := make(chan struct{}, 1)
	doneCh := make(chan struct{}, 1)
	go func() {
		for ev := range ch {
			if ev.Type == EventViewLate || (ev.Type == EventViewFailed) {
				select {
				case seenLate <- struct{}{}:
				default:
				}
			}
			if ev.Type == EventDone {
				select {
				case doneCh <- struct{}{}:
				default:
				}
				return
			}
		}
	}()
	waitRequestState(t, fx.db, id, "complete_partial", 15*time.Second)
	// The views deadline cancels the slow call at 100ms, so the view is
	// recorded as a deadline timeout rather than a late arrival.
	waitViewState(t, fx.db, id, "stress_tester", "timed_out", 15*time.Second)
	late := viewBySeat(t, fx.db, id, "stress_tester")
	if late.IncludedInJudge {
		t.Fatal("timed-out view included_in_judge = true, want false")
	}
	if n := fx.fake.CallCount(); n != 4 {
		t.Fatalf("gateway calls = %d, want 4 (exactly one judge call)", n)
	}
	agg, err := fx.db.GetRequest(id)
	if err != nil {
		t.Fatalf("GetRequest: %v", err)
	}
	if !strings.Contains(agg.Judge.MissingSeatsJSON, "stress_tester") {
		t.Fatalf("missing = %s, want stress_tester", agg.Judge.MissingSeatsJSON)
	}
	select {
	case <-seenLate:
	case <-doneCh:
		t.Log("done arrived before view outcome; timed-out view stored, event racing")
		select {
		case <-seenLate:
		case <-time.After(10 * time.Second):
			t.Fatal("done before view outcome event")
		}
	case <-time.After(15 * time.Second):
		t.Fatal("never observed view outcome event")
	}
}

func TestRunAllViewsFailNoIndependentViews(t *testing.T) {
	scripts := map[string][]gateway.Step{
		"v-poss":   {{Err: gateway.ErrGateway}},
		"v-persp":  {{Err: gateway.ErrGateway}},
		"v-stress": {{Err: gateway.ErrGateway}},
		"j1":       {{Content: testJudgeJSON, ModelReturned: "j1"}},
	}
	fx := setupFixture(t, scripts, "100ms", "2s")
	id, err := fx.runner.Run(context.Background(), testCreateRequest(fx.thread))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	waitRequestState(t, fx.db, id, "complete_partial", 10*time.Second)
	agg, err := fx.db.GetRequest(id)
	if err != nil {
		t.Fatalf("GetRequest: %v", err)
	}
	if !agg.Judge.NoIndependentViews {
		t.Fatalf("no_independent_views = false, want true: %+v", agg.Judge)
	}
	if agg.Judge.State != "complete" {
		t.Fatalf("judge state = %q, want complete", agg.Judge.State)
	}
}

func TestRunJudgeTimeoutNoRetry(t *testing.T) {
	scripts := fastScripts()
	scripts["j1"] = []gateway.Step{{Delay: 500 * time.Millisecond, Content: testJudgeJSON, ModelReturned: "j1"}}
	fx := setupFixture(t, scripts, "100ms", "100ms")
	id, err := fx.runner.Run(context.Background(), testCreateRequest(fx.thread))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	waitRequestState(t, fx.db, id, "complete_partial", 10*time.Second)
	if n := fx.fake.CallCount(); n != 4 {
		t.Fatalf("gateway calls = %d, want 4 (no judge retry)", n)
	}
	agg, err := fx.db.GetRequest(id)
	if err != nil {
		t.Fatalf("GetRequest: %v", err)
	}
	if agg.Judge.State != "timed_out" {
		t.Fatalf("judge state = %q, want timed_out", agg.Judge.State)
	}
	// Views remain available.
	for _, v := range agg.Views {
		if v.State != "complete" {
			t.Fatalf("view %s = %q, want complete", v.Seat, v.State)
		}
	}
}

func TestRunSupersedeCancelsAndPreserves(t *testing.T) {
	scripts := map[string][]gateway.Step{
		"v-poss":   {{Content: testViewJSON, ModelReturned: "v-poss"}, {Content: testViewJSON, ModelReturned: "v-poss"}},
		"v-persp":  {{Content: testViewJSON, ModelReturned: "v-persp"}, {Content: testViewJSON, ModelReturned: "v-persp"}},
		"v-stress": {{Content: testViewJSON, ModelReturned: "v-stress"}, {Content: testViewJSON, ModelReturned: "v-stress"}},
		"j1": {
			{Delay: 2 * time.Second, Content: testJudgeJSON, ModelReturned: "j1"},
			{Content: testJudgeJSON, ModelReturned: "j1"},
		},
	}
	// 10s views/judge deadlines (not the 100ms fixture default): this test
	// verifies supersede semantics and asserts the new run is "complete",
	// which requires all views included; under full-suite load the 100ms
	// default can fire the deadline-triggered judge with missing seats,
	// yielding the designed complete_partial outcome as a false failure.
	fx := setupFixture(t, scripts, "10s", "10s")
	oldID, err := fx.runner.Run(context.Background(), testCreateRequest(fx.thread))
	if err != nil {
		t.Fatalf("Run old: %v", err)
	}
	// Wait until the old judge has started (views done, judge in-flight).
	end := time.Now().Add(10 * time.Second)
	for {
		row, err := fx.db.GetRequestRow(oldID)
		if err != nil {
			t.Fatalf("GetRequestRow: %v", err)
		}
		if row.JudgeStartedAt != nil {
			break
		}
		if time.Now().After(end) {
			t.Fatal("old judge never started")
		}
		time.Sleep(10 * time.Millisecond)
	}
	ch, unsub := fx.reg.Subscribe(oldID)
	defer unsub()
	req := testCreateRequest(fx.thread)
	req.SupersedesRequestID = oldID
	newID, err := fx.runner.Run(context.Background(), req)
	if err != nil {
		t.Fatalf("Run new: %v", err)
	}
	waitRequestState(t, fx.db, newID, "complete", 15*time.Second)
	old, err := fx.db.GetRequest(oldID)
	if err != nil {
		t.Fatalf("GetRequest old: %v", err)
	}
	if old.Request.State != "superseded" {
		t.Fatalf("old state = %q, want superseded", old.Request.State)
	}
	for _, v := range old.Views {
		if v.State != "complete" || v.ResponseID == nil {
			t.Fatalf("old view %s = %+v, want completed output preserved", v.Seat, v)
		}
	}
	if old.Judge.State == "complete" {
		t.Fatal("old judge completed despite supersede cancel")
	}
	seen := false
	timeout := time.After(5 * time.Second)
	for !seen {
		select {
		case ev, ok := <-ch:
			if !ok {
				t.Fatal("subscription closed before superseded event")
			}
			if ev.Type == EventSuperseded {
				seen = true
			}
		case <-timeout:
			t.Fatal("never observed superseded event")
		}
	}
}

func TestRunSubstitutedModelFailsView(t *testing.T) {
	scripts := fastScripts()
	scripts["v-persp"] = []gateway.Step{{Content: testViewJSON, ModelReturned: "wrong-model"}}
	fx := setupFixture(t, scripts, "100ms", "2s")
	id, err := fx.runner.Run(context.Background(), testCreateRequest(fx.thread))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	waitRequestState(t, fx.db, id, "complete_partial", 10*time.Second)
	v := viewBySeat(t, fx.db, id, "perspective")
	if v.State != "failed" {
		t.Fatalf("perspective state = %q, want failed", v.State)
	}
	if v.ResponseID == nil {
		t.Fatal("substituted response not stored for audit")
	}
	resp, err := fx.db.GetResponse(*v.ResponseID)
	if err != nil {
		t.Fatalf("GetResponse: %v", err)
	}
	if !resp.Substituted {
		t.Fatalf("responses.substituted = false, want true: %+v", resp)
	}
}

func TestRewriteOneCall(t *testing.T) {
	scripts := fastScripts()
	scripts["j1"] = []gateway.Step{
		{Content: testJudgeJSON, ModelReturned: "j1"},
		{Content: "rewritten text", ModelReturned: "j1"},
	}
	fx := setupFixture(t, scripts, "100ms", "2s")
	id, err := fx.runner.Run(context.Background(), testCreateRequest(fx.thread))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	waitRequestState(t, fx.db, id, "complete", 10*time.Second)
	before := fx.fake.CallCount()
	text, rwID, err := fx.runner.Rewrite(context.Background(), id, "judge", "make it shorter")
	if err != nil {
		t.Fatalf("Rewrite: %v", err)
	}
	if text != "rewritten text" || rwID == "" {
		t.Fatalf("Rewrite = %q, %q", text, rwID)
	}
	if got := fx.fake.CallCount() - before; got != 1 {
		t.Fatalf("rewrite gateway calls = %d, want exactly 1", got)
	}
	agg, err := fx.db.GetRequest(id)
	if err != nil {
		t.Fatalf("GetRequest: %v", err)
	}
	if len(agg.Rewrites) != 1 || agg.Rewrites[0].OutputText != "rewritten text" {
		t.Fatalf("rewrites = %+v", agg.Rewrites)
	}
}

// TestRunDetachedContext is the B1 assertion: cancelling the caller's
// context right after Run returns must not cancel in-flight work.
func TestRunDetachedContext(t *testing.T) {
	fx := setupFixture(t, fastScripts(), "100ms", "2s")
	ctx, cancel := context.WithCancel(context.Background())
	id, err := fx.runner.Run(ctx, testCreateRequest(fx.thread))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	cancel()
	waitRequestState(t, fx.db, id, "complete", 10*time.Second)
}
