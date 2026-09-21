package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/qoke/toughdecisions/internal/config"
	"github.com/qoke/toughdecisions/internal/gateway"
	"github.com/qoke/toughdecisions/internal/logx"
)

// TestShippedExampleBootstrapOffline drives the SHIPPED example config
// (config/*.yaml + casepack/) end to end through the real CLI: validate,
// load, calibrate (admit), baselines, sentinel, weekly --notify. It proves
// the documented README bootstrap is coherent under the R-12 self-grading
// rule: every response family the example pack emits must have a legal
// (different-family, admitted) grader or substitute.
func TestShippedExampleBootstrapOffline(t *testing.T) {
	repoRoot := shippedRepoRoot(t)
	dir := t.TempDir()
	t.Setenv("COUNCIL_DB_PATH", filepath.Join(dir, "c.db"))
	t.Setenv("COUNCIL_PACK_FILE", filepath.Join(repoRoot, "config", "pack.yaml"))
	t.Setenv("COUNCIL_MODELS_FILE", filepath.Join(repoRoot, "config", "models.yaml"))
	t.Setenv("COUNCIL_GRADERS_FILE", filepath.Join(repoRoot, "config", "graders.yaml"))
	t.Setenv("COUNCIL_CANDIDATES_FILE", filepath.Join(repoRoot, "config", "candidates.yaml"))
	t.Setenv("COUNCIL_CASEPACK_DIR", filepath.Join(repoRoot, "casepack"))
	reports := filepath.Join(dir, "reports")
	t.Setenv("COUNCIL_REPORTS_DIR", reports)
	t.Setenv("COUNCIL_LOG_LEVEL", "error")
	// Keep the weekly run small and deterministic: 2 sentinel cases.
	t.Setenv("COUNCIL_HARNESS_SENTINEL_COUNT", "2")
	t.Setenv("COUNCIL_HARNESS_SCREEN_CASES", "2")

	viewJSON := `{"urgent_danger":{"present":false},"qualification":"q","suggested_reply":"r","decisive_insight":"i","tradeoff_or_objection":"t","depends_on":"d","fallback":"f"}`
	judgeJSON := `{"urgent_danger":{"present":false},"qualification":"q","recommended_reply":"r","why":"w","accepted_cost":"c","next":{"immediate":"i","forward":"f"},"change_course_if":"cc"}`
	gradeAll3 := `{"scores":{"grounding_calibration":3,"context_values_fidelity":3,"decision_insight":3,"practical_robustness":3,"role_execution":3},"supporting_passages":{"decision_insight":"p"},"notes_check":{"noticed":[],"missed":[],"beyond_notes":[]}}`
	gradeAll1Flag := `{"scores":{"grounding_calibration":1,"context_values_fidelity":1,"decision_insight":1,"practical_robustness":1,"role_execution":1},"supporting_passages":{"decision_insight":"p"},"flags":[{"type":"values_substitution","passage":"p","violated":"v"}],"notes_check":{"noticed":[],"missed":[],"beyond_notes":[]}}`
	grade33233 := `{"scores":{"grounding_calibration":3,"context_values_fidelity":3,"decision_insight":3,"practical_robustness":2,"role_execution":3},"supporting_passages":{"decision_insight":"p"},"notes_check":{"noticed":[],"missed":[],"beyond_notes":[]}}`
	grade01101Flag := `{"scores":{"grounding_calibration":0,"context_values_fidelity":1,"decision_insight":1,"practical_robustness":0,"role_execution":1},"supporting_passages":{"decision_insight":"p"},"flags":[{"type":"fabrication","passage":"p","violated":"v"}],"notes_check":{"noticed":[],"missed":[],"beyond_notes":[]}}`
	// A/clear verdicts need no reversal and parse first try (one call
	// per grader per compare job).
	pairJSON := `{"verdict":"A","margin":"clear","consequential_difference":"d"}`
	views := []gateway.Step{}
	for i := 0; i < 400; i++ {
		views = append(views, gateway.Step{Content: viewJSON})
	}
	views = append(views, gateway.Step{Content: judgeJSON}, gateway.Step{Content: judgeJSON})
	// council-judge-a serves BOTH the judge seat and the selection-a
	// grader; the Fake keys scripts by model id only, so route on the
	// seat tag: seat calls carry Tags["seat"], grader calls (absolute
	// with Tags["grader"], pairwise with neither) never do.
	judgeViews := append([]gateway.Step{}, views...)
	possViews := append([]gateway.Step{}, views...)
	scripts := map[string][]gateway.Step{}
	dual := gateway.NewFake(scripts)
	router := newTagRouter(dual, judgeViews, gradeAll3, gradeAll1Flag, grade33233, grade01101Flag, pairJSON)
	router.queues["council-view-a#seat"] = append([]gateway.Step{}, possViews...)
	router.queues["council-view-b#seat"] = append([]gateway.Step{}, possViews...)
	router.queues["council-view-c#seat"] = append([]gateway.Step{}, possViews...)
	old := gatewayFactory
	gatewayFactory = func(_ *config.Config, _ logx.Logger) gateway.Client { return router }
	defer func() { gatewayFactory = old }()

	for _, args := range [][]string{
		{"db", "migrate"},
		{"pack", "init"},
		{"cases", "validate"},
		{"cases", "load"},
		{"graders", "calibrate", "--all"},
		{"pack", "publish", "--baselines-only"},
		{"harness", "sentinel"},
	} {
		if got := run(args); got != exitOK {
			t.Fatalf("%v = %d, want 0", args, got)
		}
	}

	var notified atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		notified.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)
	t.Setenv("COUNCIL_NOTIFY_WEBHOOK_URL", srv.URL+"?token=secret")
	if got := run([]string{"harness", "weekly", "--notify"}); got != exitOK {
		t.Fatalf("weekly --notify = %d, want 0", got)
	}
	if notified.Load() != 1 {
		t.Fatalf("webhook posts = %d; want 1", notified.Load())
	}

	// Assert: a report file with every §12.8 section was written.
	// Resolve the weekly run id by scanning the reports dir for the
	// single *.md the weekly run just wrote.
	matches, gerr := filepath.Glob(filepath.Join(reports, "*.md"))
	if gerr != nil || len(matches) != 1 {
		t.Fatalf("reports dir = %v, %v; want exactly one report", matches, gerr)
	}
	weekID := matches[0]
	raw, err := os.ReadFile(weekID)
	if err != nil {
		t.Fatalf("read report: %v", err)
	}
	for _, section := range []string{
		"## Run summary", "## Incumbent drift", "## Grader recheck",
		"## Screening table", "## Compare per family", "## Downstream per family",
		"## Issue-coverage tables", "## Promotion checklist per candidate",
		"## Open flags", "## Production week", "## Failures/timeouts",
	} {
		if !strings.Contains(string(raw), section) {
			t.Errorf("report missing %q", section)
		}
	}
	t.Logf("shipped bootstrap report: %s", weekID)

}

// shippedRepoRoot resolves the repo checkout root from this test file so
// the smoke test consumes the SHIPPED config/ and casepack/ directories.
func shippedRepoRoot(t *testing.T) string {
	t.Helper()
	here, err := filepath.Abs(".")
	if err != nil {
		t.Fatal(err)
	}
	for d := here; ; {
		if _, err := os.Stat(filepath.Join(d, "config", "pack.yaml")); err == nil {
			if _, err := os.Stat(filepath.Join(d, "casepack")); err == nil {
				return d
			}
		}
		up := filepath.Dir(d)
		if up == d {
			t.Fatalf("repo root with config/pack.yaml + casepack/ not found from %s", here)
		}
		d = up
	}
}

// tagRouter splits the dual-use council-judge-a model id by call role:
// calls with Tags["grader"] set consume the grades queue, seat calls
// consume the judge-views queue. All other models delegate to the Fake.
type tagRouter struct {
	seat     *gateway.Fake
	queues   map[string][]gateway.Step
	calByKey map[string]gateway.Step
	pairStep gateway.Step
	absStep  gateway.Step
}

func newTagRouter(seat *gateway.Fake, judgeViews []gateway.Step, all3, all1, c33233, c01101, pair string) *tagRouter {
	return &tagRouter{seat: seat, queues: map[string][]gateway.Step{
		"council-judge-a#seat": append([]gateway.Step{}, judgeViews...),
	}, calByKey: map[string]gateway.Step{
		"CAL-001": {Content: all3}, "CAL-002": {Content: all1},
		"CAL-003": {Content: c33233}, "CAL-004": {Content: c01101},
	}, pairStep: gateway.Step{Content: pair}, absStep: gateway.Step{Content: all3}}
}

// Chat implements gateway.Client. Seat calls (Tags["seat"]) consume the
// per-model view queues; grader calls are answered content-aware:
// pairwise prompts get A/clear verdicts, calibration prompts get their
// per-item grades, all other absolute grades get the generic all-3 grade.
func (t *tagRouter) Chat(ctx context.Context, req gateway.ChatRequest) (gateway.ChatResponse, error) {
	if req.Model == "council-judge-a" || req.Model == "council-view-a" || req.Model == "council-view-b" || req.Model == "council-view-c" {
		// Seat calls always carry Tags["seat"] and consume the seat
		// view queues positionally.
		if _, ok := req.Tags["seat"]; ok {
			key := req.Model + "#seat"
			q := t.queues[key]
			if len(q) == 0 {
				return gateway.ChatResponse{ModelReturned: req.Model}, nil
			}
			step := q[0]
			t.queues[key] = q[1:]
			t.seat.Calls = append(t.seat.Calls, req)
			return gateway.ChatResponse{ModelReturned: orModel(step.ModelReturned, req.Model), Content: step.Content}, nil
		}
		// Grader calls are content-aware (positional queues underflow
		// unevenly under R-12 substitution routing):
		// - pairwise prompts contain "Response A (role:" → pair verdict.
		// - calibration prompts (calibrate + drift recheck) contain the
		//   calibration response text; match CAL-00x by the quoted text.
		// - all other absolute grades → generic all-3 grade.
		step := t.gradeStep(req)
		t.seat.Calls = append(t.seat.Calls, req)
		return gateway.ChatResponse{ModelReturned: orModel(step.ModelReturned, req.Model), Content: step.Content}, nil
	}
	return t.seat.Chat(ctx, req)
}

func orModel(m, fallback string) string {
	if m == "" {
		return fallback
	}
	return m
}

// gradeStep returns the scripted grade for a grader call: pairwise
// verdicts for A/B prompts, per-item calibration grades when the prompt
// embeds a CAL-00x response text, else the generic all-3 absolute grade.
func (t *tagRouter) gradeStep(req gateway.ChatRequest) gateway.Step {
	last := lastMsg(req)
	if strings.Contains(last, "Response A (role:") {
		return t.pairStep
	}
	// Match calibration items by their distinctive response text
	// fragments embedded in the rendered prompt.
	for _, m := range []struct {
		frag string
		step gateway.Step
	}{
		{"arrange evening cover", t.calByKey["CAL-001"]},
		{"no matter what", t.calByKey["CAL-002"]},
		{"leaves Thursday pickup uncovered", t.calByKey["CAL-003"]},
		{"waived all late fees", t.calByKey["CAL-004"]},
	} {
		if strings.Contains(last, m.frag) {
			return m.step
		}
	}
	return t.absStep
}

func lastMsg(c gateway.ChatRequest) string {
	if len(c.Messages) == 0 {
		return ""
	}
	return c.Messages[len(c.Messages)-1].Content
}
