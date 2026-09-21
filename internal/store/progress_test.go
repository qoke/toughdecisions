package store

import (
	"testing"
)

func seedProgressRequest(t *testing.T, db *DB) string {
	t.Helper()
	th, err := db.CreateThread("t")
	if err != nil {
		t.Fatalf("CreateThread: %v", err)
	}
	card, err := db.InsertCard(th.ID, `{"decision":"d"}`)
	if err != nil {
		t.Fatalf("InsertCard: %v", err)
	}
	pk, err := db.InsertPack("active", `{}`, "pp", "")
	if err != nil {
		t.Fatalf("InsertPack: %v", err)
	}
	req, err := db.InsertRequest(&Request{
		ThreadID: th.ID, CardID: card.ID, PackID: pk.ID,
		SnapshotJSON: `{}`, InputHash: "ih", State: "created",
		ViewsDeadlineAt: "2026-01-01T00:00:00Z",
	})
	if err != nil {
		t.Fatalf("InsertRequest: %v", err)
	}
	if _, err := db.InsertRequestJudge(req.ID); err != nil {
		t.Fatalf("InsertRequestJudge: %v", err)
	}
	return req.ID
}

func TestProgressRoundTrip(t *testing.T) {
	db := openTestDB(t)
	id := seedProgressRequest(t, db)

	state := "running_views"
	started := "2026-01-01T00:00:01Z"
	deadline := "2026-01-01T00:00:31Z"
	finished := "2026-01-01T00:00:40Z"
	tFirst := int64(120)
	tFinal := int64(900)
	viewsIn := 2
	judgeIn := 1
	danger := true
	if err := db.UpdateRequestProgress(id, RequestProgressPatch{
		State: &state, JudgeStartedAt: &started, JudgeDeadlineAt: &deadline,
		FinishedAt: &finished, TFirstUsableViewMs: &tFirst, TFinalMs: &tFinal,
		ViewsCompletedInDeadline: &viewsIn, JudgeCompletedInDeadline: &judgeIn,
		DangerFlagged: &danger,
	}); err != nil {
		t.Fatalf("UpdateRequestProgress: %v", err)
	}
	row, err := db.GetRequestRow(id)
	if err != nil {
		t.Fatalf("GetRequestRow: %v", err)
	}
	if row.State != state || row.JudgeStartedAt == nil || *row.JudgeStartedAt != started ||
		row.JudgeDeadlineAt == nil || row.FinishedAt == nil ||
		row.TFirstUsableViewMs == nil || *row.TFirstUsableViewMs != tFirst ||
		row.TFinalMs == nil || *row.TFinalMs != tFinal ||
		row.ViewsCompletedInDeadline != viewsIn || row.JudgeCompletedInDeadline == nil || *row.JudgeCompletedInDeadline != judgeIn ||
		!row.DangerFlagged {
		t.Fatalf("progress round-trip mismatch: %+v", row)
	}
	// Empty patch is a no-op.
	if err := db.UpdateRequestProgress(id, RequestProgressPatch{}); err != nil {
		t.Fatalf("empty patch: %v", err)
	}
	if err := db.UpdateRequestProgress("missing", RequestProgressPatch{State: &state}); err == nil {
		t.Fatal("want error for missing request, got nil")
	}
}

func TestFirstUsableAndDanger(t *testing.T) {
	db := openTestDB(t)
	id := seedProgressRequest(t, db)
	if err := db.SetFirstUsableViewMsIfUnset(id, 50); err != nil {
		t.Fatalf("SetFirstUsable: %v", err)
	}
	if err := db.SetFirstUsableViewMsIfUnset(id, 99); err != nil {
		t.Fatalf("SetFirstUsable again: %v", err)
	}
	row, _ := db.GetRequestRow(id)
	if row.TFirstUsableViewMs == nil || *row.TFirstUsableViewMs != 50 {
		t.Fatalf("first usable overwritten: %+v", row.TFirstUsableViewMs)
	}
	if err := db.SetDangerFlagged(id); err != nil {
		t.Fatalf("SetDangerFlagged: %v", err)
	}
	row, _ = db.GetRequestRow(id)
	if !row.DangerFlagged {
		t.Fatal("danger not set")
	}
}

func TestStateIfNotSuperseded(t *testing.T) {
	db := openTestDB(t)
	id := seedProgressRequest(t, db)
	updated, err := db.UpdateRequestStateIfNotSuperseded(id, "running_views")
	if err != nil || !updated {
		t.Fatalf("update = %v, %v", updated, err)
	}
	if err := db.SetSuperseded(id, "other"); err != nil {
		t.Fatalf("SetSuperseded: %v", err)
	}
	updated, err = db.UpdateRequestStateIfNotSuperseded(id, "complete")
	if err != nil {
		t.Fatalf("superseded update err: %v", err)
	}
	if updated {
		t.Fatal("superseded row was updated, want false")
	}
}

func TestJudgeResultAndListViews(t *testing.T) {
	db := openTestDB(t)
	id := seedProgressRequest(t, db)
	v, err := db.InsertRequestView(id, "possibility", "pending")
	if err != nil {
		t.Fatalf("InsertRequestView: %v", err)
	}
	respID := "resp1"
	now := "2026-01-01T00:00:02Z"
	if err := db.UpdateRequestViewState(v.ID, "complete", &respID, true, &now); err != nil {
		t.Fatalf("UpdateRequestViewState: %v", err)
	}
	views, err := db.ListRequestViews(id)
	if err != nil || len(views) != 1 || views[0].State != "complete" || !views[0].IncludedInJudge {
		t.Fatalf("ListRequestViews = %+v, %v", views, err)
	}
	included := []string{"possibility"}
	missing := []string{"perspective", "stress_tester"}
	if err := db.UpdateJudgeResult(id, JudgeResult{
		State: "complete", IncludedSeats: included, MissingSeats: missing,
	}); err != nil {
		t.Fatalf("UpdateJudgeResult: %v", err)
	}
	agg, err := db.GetRequest(id)
	if err != nil {
		t.Fatalf("GetRequest: %v", err)
	}
	if agg.Judge.State != "complete" {
		t.Fatalf("judge = %+v", agg.Judge)
	}
	if got := jsonStringArray(nil); got != "[]" {
		t.Fatalf("jsonStringArray(nil) = %q", got)
	}
	if got := jsonStringArray([]string{`a"b`, "c\nd"}); got != `["a\"b","c\nd"]` {
		t.Fatalf("jsonStringArray escape = %q", got)
	}
}

func TestMetricsQueries(t *testing.T) {
	db := openTestDB(t)
	id := seedProgressRequest(t, db)
	pk, err := db.PacksByStatus("active")
	if err != nil || len(pk) != 1 {
		t.Fatalf("PacksByStatus = %+v, %v", pk, err)
	}
	if _, err := db.InsertRequestView(id, "possibility", "complete"); err != nil {
		t.Fatalf("view: %v", err)
	}
	if err := db.UpdateJudgeResult(id, JudgeResult{State: "complete"}); err != nil {
		t.Fatalf("judge: %v", err)
	}
	since := "2020-01-01T00:00:00Z"
	if _, _, _, err := db.RequestTimings(since); err != nil {
		t.Fatalf("RequestTimings: %v", err)
	}
	if _, err := db.ViewStateCounts(since); err != nil {
		t.Fatalf("ViewStateCounts: %v", err)
	}
	if _, err := db.JudgeStateCounts(since); err != nil {
		t.Fatalf("JudgeStateCounts: %v", err)
	}
	if _, err := db.TimeoutCounts(since); err != nil {
		t.Fatalf("TimeoutCounts: %v", err)
	}
	if _, err := db.PacksByStatus("active"); err != nil {
		t.Fatalf("PacksByStatus: %v", err)
	}
	if err := db.UpdatePackMeta(pk[0].ID, map[string]any{"activated_at": pk[0].CreatedAt}); err != nil {
		t.Fatalf("UpdatePackMeta: %v", err)
	}
}

func TestTimeoutCountsAndTimingsWithData(t *testing.T) {
	db := openTestDB(t)
	id := seedProgressRequest(t, db)
	tFirst := int64(42)
	tFinal := int64(777)
	if err := db.UpdateRequestProgress(id, RequestProgressPatch{TFirstUsableViewMs: &tFirst, TFinalMs: &tFinal}); err != nil {
		t.Fatalf("progress: %v", err)
	}
	if _, err := db.InsertResponse(&Response{CacheKey: "ck-timeout", Seat: "possibility", ConfigHash: "c", PromptPackHash: "p", InputHash: "i", Origin: "production", RequestID: &id, ModelRequested: "m", TimedOut: true}); err != nil {
		t.Fatalf("InsertResponse: %v", err)
	}
	since := "2020-01-01T00:00:00Z"
	tc, err := db.TimeoutCounts(since)
	if err != nil || tc["possibility"] != 1 {
		t.Fatalf("TimeoutCounts = %v, %v", tc, err)
	}
	fu, fin, count, err := db.RequestTimings(since)
	if err != nil || count != 1 || len(fu) != 1 || fu[0] != 42 || len(fin) != 1 || fin[0] != 777 {
		t.Fatalf("RequestTimings = %v %v %d %v", fu, fin, count, err)
	}
	if _, err := db.GetRequestRow("missing"); err == nil {
		t.Fatal("GetRequestRow missing: want error")
	}
	if _, err := db.ListRequestViews("missing"); err != nil {
		t.Fatalf("ListRequestViews missing: %v", err)
	}
	if err := db.UpdatePackMeta("missing", map[string]any{"published_from_run_id": "r", "activated_at": "t"}); err != nil {
		t.Fatalf("UpdatePackMeta: %v", err)
	}
	if err := db.SetStatus("missing", "active"); err == nil {
		t.Fatal("SetStatus missing: want error")
	}
}
