package harness

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/qoke/toughdecisions/internal/gateway"
	"github.com/qoke/toughdecisions/internal/pack"
	"github.com/qoke/toughdecisions/internal/store"
)

// seedPublishSelection seeds one selection family + case usable by publish
// tests.
func seedPublishSelection(t *testing.T, fx *hFixture) *store.Case {
	t.Helper()
	fam := seedHFamily(t, fx.db, "PSEL", "selection", []string{"hard"}, []string{"possibility"})
	return seedHCase(t, fx.db, fam, "PSEL-base")
}

// seedPromotedCandidate stores passing compare/downstream results for key in
// runID and runs Promotion so the checklist recommends promotion.
func seedPromotedCandidate(t *testing.T, fx *hFixture, runID, key string) {
	t.Helper()
	if _, err := fx.db.UpsertCandidate(runID, key, "possibility", `{"seat":"possibility","model":"cand-m","family":"openai","max_output_tokens":2000}`, "cfg"); err != nil {
		t.Fatalf("UpsertCandidate: %v", err)
	}
	row, err := fx.db.GetCandidateByKey(runID, key)
	if err != nil {
		t.Fatalf("GetCandidateByKey: %v", err)
	}
	cmp := CompareResult{
		RunID: runID, CandidateKey: key, Seat: "possibility",
		HardWins: 1, RoleMeans: []float64{3.5, 3.5}, RoleMins: []int{3, 3},
		P50Cand: 10, P50Inc: 100, P95Cand: 20, MeanCostCand: 0.01, MeanCostInc: 0.02,
	}
	ds := DownstreamResult{RunID: runID, CandidateKey: key, Seat: "possibility", AgreedNet: 1}
	cmpRaw, _ := json.Marshal(cmp)
	dsRaw, _ := json.Marshal(ds)
	c, d := string(cmpRaw), string(dsRaw)
	if err := fx.db.UpdateCandidateResults(row.ID, &store.CandidateResults{
		CompareResultJSON: &c, DownstreamResultJSON: &d,
	}); err != nil {
		t.Fatalf("UpdateCandidateResults: %v", err)
	}
	cl, err := fx.runner.Promotion(runID, key)
	if err != nil {
		t.Fatalf("Promotion: %v", err)
	}
	if !cl.PromoteRecommended {
		t.Fatalf("checklist = %+v; want promote recommended", cl)
	}
}

func seedFailingCandidate(t *testing.T, fx *hFixture, runID, key string) {
	t.Helper()
	if _, err := fx.db.UpsertCandidate(runID, key, "possibility", `{"seat":"possibility","model":"cand-m","family":"openai","max_output_tokens":2000}`, "cfg"); err != nil {
		t.Fatalf("UpsertCandidate: %v", err)
	}
	row, _ := fx.db.GetCandidateByKey(runID, key)
	cmp := CompareResult{RunID: runID, CandidateKey: key, Seat: "possibility", OpenFlags: 1}
	ds := DownstreamResult{RunID: runID, CandidateKey: key, Seat: "possibility"}
	cmpRaw, _ := json.Marshal(cmp)
	dsRaw, _ := json.Marshal(ds)
	c, d := string(cmpRaw), string(dsRaw)
	if err := fx.db.UpdateCandidateResults(row.ID, &store.CandidateResults{
		CompareResultJSON: &c, DownstreamResultJSON: &d,
	}); err != nil {
		t.Fatalf("UpdateCandidateResults: %v", err)
	}
	if _, err := fx.runner.Promotion(runID, key); err != nil {
		t.Fatalf("Promotion: %v", err)
	}
}

func TestPublishRefusesFailingChecklist(t *testing.T) {
	scripts := map[string][]gateway.Step{}
	for _, m := range []string{"v-poss", "v-persp", "v-stress"} {
		scripts[m] = []gateway.Step{{Content: hViewJSON}, {Content: hViewJSON}}
	}
	scripts["j1"] = []gateway.Step{{Content: hJudgeJSON}, {Content: hJudgeJSON}}
	fx := setupHarness(t, scripts)
	seedPublishSelection(t, fx)
	run, err := fx.runner.StartRun("weekly", fx.pk.ID, "{}")
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}
	seedFailingCandidate(t, fx, run.ID, "cand-a")
	before, err := pack.Active(fx.db)
	if err != nil {
		t.Fatalf("Active: %v", err)
	}

	// Act: publish without force.
	_, err = fx.runner.Publish(context.Background(), PublishOptions{RunID: run.ID, CandidateKey: "cand-a"})

	// Assert: refusal naming the candidate, active pack unchanged.
	var blocked *ChecklistBlockedError
	if !isChecklistBlocked(err, &blocked) {
		t.Fatalf("err = %v; want ChecklistBlockedError", err)
	}
	if !strings.Contains(err.Error(), "cand-a") {
		t.Fatalf("err = %v; want it to name cand-a", err)
	}
	after, _ := pack.Active(fx.db)
	if after.ID != before.ID {
		t.Fatalf("active changed to %s; want %s", after.ID, before.ID)
	}
}

func TestPublishForcedRecordsMarker(t *testing.T) {
	scripts := map[string][]gateway.Step{}
	for _, m := range []string{"v-poss", "v-persp", "v-stress"} {
		scripts[m] = []gateway.Step{{Content: hViewJSON}, {Content: hViewJSON}}
	}
	scripts["j1"] = []gateway.Step{{Content: hJudgeJSON}, {Content: hJudgeJSON}}
	fx := setupHarness(t, scripts)
	seedPublishSelection(t, fx)
	run, _ := fx.runner.StartRun("weekly", fx.pk.ID, "{}")
	seedFailingCandidate(t, fx, run.ID, "cand-a")

	// Act: forced publish.
	got, err := fx.runner.Publish(context.Background(), PublishOptions{RunID: run.ID, CandidateKey: "cand-a", Force: true})

	// Assert: new active pack, forced marker on the candidate row.
	if err != nil {
		t.Fatalf("Publish forced: %v", err)
	}
	if got.Forced != true {
		t.Fatalf("Forced = false; want true")
	}
	row, _ := fx.db.GetCandidateByKey(run.ID, "cand-a")
	if row.PromotionJSON == nil {
		t.Fatal("promotion_json missing")
	}
	var cl Checklist
	if err := json.Unmarshal([]byte(*row.PromotionJSON), &cl); err != nil {
		t.Fatalf("decode promotion_json: %v", err)
	}
	if !cl.Forced {
		t.Fatalf("checklist = %+v; want Forced=true recorded", cl)
	}
	cur, _ := pack.Active(fx.db)
	if cur.ID != got.Pack.ID {
		t.Fatalf("active = %s; want %s", cur.ID, got.Pack.ID)
	}
}

func TestPublishBaselinesAtomicAndRollbackWorks(t *testing.T) {
	scripts := map[string][]gateway.Step{}
	for _, m := range []string{"v-poss", "v-persp", "v-stress"} {
		steps := make([]gateway.Step, 4)
		for i := range steps {
			steps[i] = gateway.Step{Content: hViewJSON}
		}
		scripts[m] = steps
	}
	scripts["j1"] = []gateway.Step{{Content: hJudgeJSON}, {Content: hJudgeJSON}, {Content: hJudgeJSON}}
	scripts["cand-m"] = []gateway.Step{{Content: hViewJSON}, {Content: hViewJSON}}
	fx := setupHarness(t, scripts)
	c := seedPublishSelection(t, fx)
	run, _ := fx.runner.StartRun("weekly", fx.pk.ID, "{}")
	seedPromotedCandidate(t, fx, run.ID, "cand-a")

	// Act.
	got, err := fx.runner.Publish(context.Background(), PublishOptions{RunID: run.ID, CandidateKey: "cand-a"})

	// Assert: every seat x selection case has a baseline; rollback swaps back.
	if err != nil {
		t.Fatalf("Publish: %v", err)
	}
	for _, seat := range pack.AllSeats {
		if _, err := fx.db.GetBaseline(got.Pack.ID, string(seat), c.ID); err != nil {
			t.Fatalf("baseline %s missing: %v", seat, err)
		}
	}
	rolled, err := pack.Rollback(fx.db)
	if err != nil {
		t.Fatalf("Rollback after publish: %v", err)
	}
	if rolled.ID != fx.pk.ID {
		t.Fatalf("rollback active = %s; want previous %s", rolled.ID, fx.pk.ID)
	}
	cur, _ := pack.Active(fx.db)
	if cur.ID != fx.pk.ID {
		t.Fatalf("active = %s; want %s", cur.ID, fx.pk.ID)
	}
}

func TestPublishBaselinesOnlyKeepsActive(t *testing.T) {
	scripts := map[string][]gateway.Step{}
	for _, m := range []string{"v-poss", "v-persp", "v-stress"} {
		scripts[m] = []gateway.Step{{Content: hViewJSON}}
	}
	scripts["j1"] = []gateway.Step{{Content: hJudgeJSON}}
	fx := setupHarness(t, scripts)
	c := seedPublishSelection(t, fx)
	run, _ := fx.runner.StartRun("weekly", fx.pk.ID, "{}")

	// Act.
	got, err := fx.runner.Publish(context.Background(), PublishOptions{RunID: run.ID, BaselinesOnly: true})

	// Assert: no new pack, active unchanged, baselines filled on active.
	if err != nil {
		t.Fatalf("Publish baselines-only: %v", err)
	}
	if got.Pack.ID != fx.pk.ID {
		t.Fatalf("pack = %s; want active %s", got.Pack.ID, fx.pk.ID)
	}
	for _, seat := range pack.AllSeats {
		if _, err := fx.db.GetBaseline(fx.pk.ID, string(seat), c.ID); err != nil {
			t.Fatalf("baseline %s missing: %v", seat, err)
		}
	}
}

// isChecklistBlocked unwraps to *ChecklistBlockedError via errors.As.
func isChecklistBlocked(err error, target **ChecklistBlockedError) bool {
	return errors.As(err, target)
}

func TestReportSectionsAndWebhookFailure(t *testing.T) {
	fx := setupHarness(t, nil)
	run, _ := fx.runner.StartRun("weekly", fx.pk.ID, "{}")
	seedPromotedCandidate(t, fx, run.ID, "cand-a")

	// Arrange: failing webhook server.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)
	t.Setenv("COUNCIL_NOTIFY_WEBHOOK_URL", srv.URL)

	// Act.
	got, err := fx.runner.Report(context.Background(), ReportOptions{
		RunID: run.ID, CompareRunIDs: []string{run.ID},
		DownstreamRunIDs: []string{run.ID}, Notify: true,
		Summary: "weekly summary",
	})

	// Assert: report written, every §12.8 section present, webhook non-fatal.
	if err != nil {
		t.Fatalf("Report: %v", err)
	}
	for _, section := range []string{
		"## Run summary", "## Incumbent drift", "## Grader recheck",
		"## Screening table", "## Compare per family", "## Downstream per family",
		"## Issue-coverage tables", "## Promotion checklist per candidate",
		"## Open flags", "## Production week", "## Failures/timeouts",
	} {
		if !strings.Contains(got.Markdown, section) {
			t.Errorf("report missing %q", section)
		}
	}
	if got.Notified {
		t.Error("Notified=true; want false on webhook failure")
	}
	raw, err := os.ReadFile(got.Path)
	if err != nil || string(raw) != got.Markdown {
		t.Fatalf("report file = %v, %v; want written markdown", len(raw), err)
	}
}
