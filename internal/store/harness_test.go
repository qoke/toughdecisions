package store

import (
	"database/sql"
	"errors"
	"testing"
)

func TestHarnessRunLifecycle(t *testing.T) {
	db := openTestDB(t)
	r, err := db.CreateHarnessRun("weekly", "pack1", `{"fresh":true}`)
	if err != nil {
		t.Fatalf("CreateHarnessRun: %v", err)
	}
	if r.Status != "created" || r.FinishedAt != nil || r.ReportPath != nil || r.SummaryJSON != nil {
		t.Fatalf("unexpected initial run: %+v", r)
	}
	byID, err := db.GetHarnessRun(r.ID)
	if err != nil {
		t.Fatalf("GetHarnessRun: %v", err)
	}
	if byID.Kind != "weekly" || byID.PackID != "pack1" || byID.ParamsJSON != `{"fresh":true}` {
		t.Fatalf("round-trip mismatch: %+v", byID)
	}
	if err := db.UpdateHarnessRunStatus(r.ID, "running"); err != nil {
		t.Fatalf("UpdateHarnessRunStatus: %v", err)
	}
	path := "reports/x.md"
	summary := `{"ok":true}`
	finished := "2026-01-02T00:00:00Z"
	if err := db.FinishHarnessRun(r.ID, "complete", &path, &summary, &finished); err != nil {
		t.Fatalf("FinishHarnessRun: %v", err)
	}
	done, _ := db.GetHarnessRun(r.ID)
	if done.Status != "complete" || done.ReportPath == nil || *done.ReportPath != path ||
		done.SummaryJSON == nil || done.FinishedAt == nil || *done.FinishedAt != finished {
		t.Fatalf("finish not stored: %+v", done)
	}
	if err := db.UpdateHarnessRunStatus("missing", "running"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("UpdateHarnessRunStatus missing err = %v, want ErrNoRows", err)
	}
	if _, err := db.GetHarnessRun("missing"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("GetHarnessRun missing err = %v", err)
	}
	if _, err := db.CreateHarnessRun("", "pack1", `{}`); err == nil {
		t.Fatal("CreateHarnessRun empty kind: want error")
	}
}

func TestSentinelBaselinesCandidates(t *testing.T) {
	db := openTestDB(t)
	s, err := db.InsertSentinelResult(&SentinelResult{
		RunID: "run1", Seat: "judge", CaseID: "c1",
		FreshResponseID: "fresh1", BaselineResponseID: "base1",
		VerdictsJSON: `{"gA":"left"}`, LatencyMs: 120, Regression: true,
	})
	if err != nil {
		t.Fatalf("InsertSentinelResult: %v", err)
	}
	byID, err := db.GetSentinelResult(s.ID)
	if err != nil {
		t.Fatalf("GetSentinelResult: %v", err)
	}
	if byID.FreshResponseID != "fresh1" || !byID.Regression || byID.LatencyMs != 120 {
		t.Fatalf("round-trip mismatch: %+v", byID)
	}
	rows, err := db.ListSentinelResultsByRun("run1")
	if err != nil || len(rows) != 1 {
		t.Fatalf("ListSentinelResultsByRun = %v, %v", rows, err)
	}
	if _, err := db.GetSentinelResult("missing"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("GetSentinelResult missing err = %v", err)
	}
	if _, err := db.InsertSentinelResult(&SentinelResult{}); err == nil {
		t.Fatal("InsertSentinelResult empty: want error")
	}

	fam := seedFamily(t, db, "F030")
	c := seedCase(t, db, fam, "F030-base")
	bun, err := db.UpsertBundle(&Bundle{BundleKey: "F030-B1", CaseID: c.ID, BundleHash: "bh"})
	if err != nil {
		t.Fatalf("UpsertBundle: %v", err)
	}
	bl, err := db.UpsertBaseline("pack1", "judge", c.ID, &bun.ID, "resp-base")
	if err != nil {
		t.Fatalf("UpsertBaseline: %v", err)
	}
	gotBl, err := db.GetBaseline("pack1", "judge", c.ID)
	if err != nil {
		t.Fatalf("GetBaseline: %v", err)
	}
	if gotBl.ID != bl.ID || gotBl.ResponseID != "resp-base" || gotBl.BundleID == nil {
		t.Fatalf("round-trip mismatch: %+v", gotBl)
	}
	// Upsert moves the baseline to the new response instead of duplicating it.
	if _, err := db.UpsertBaseline("pack1", "judge", c.ID, nil, "resp-base2"); err != nil {
		t.Fatalf("UpsertBaseline again: %v", err)
	}
	moved, _ := db.GetBaseline("pack1", "judge", c.ID)
	if moved.ResponseID != "resp-base2" || moved.ID != bl.ID {
		t.Fatalf("upsert did not move: %+v", moved)
	}
	if _, err := db.GetBaseline("pack1", "judge", "missing"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("GetBaseline missing err = %v", err)
	}
	if _, err := db.UpsertBaseline("", "judge", c.ID, nil, "r"); err == nil {
		t.Fatal("UpsertBaseline empty pack: want error")
	}

	cand, err := db.UpsertCandidate("run1", "cand-x", "judge", `{"model":"m"}`, "ch1")
	if err != nil {
		t.Fatalf("UpsertCandidate: %v", err)
	}
	if cand.Finalist {
		t.Fatal("new candidate finalist, want false")
	}
	gotCand, err := db.GetCandidate(cand.ID)
	if err != nil {
		t.Fatalf("GetCandidate: %v", err)
	}
	if gotCand.CandidateKey != "cand-x" || gotCand.ConfigHash != "ch1" {
		t.Fatalf("round-trip mismatch: %+v", gotCand)
	}
	byKey, err := db.GetCandidateByKey("run1", "cand-x")
	if err != nil {
		t.Fatalf("GetCandidateByKey: %v", err)
	}
	if byKey.ID != cand.ID {
		t.Fatalf("by key mismatch: %+v vs %+v", byKey, cand)
	}
	screen := `{"passed":true}`
	compare := `{"wins":2}`
	downstream := `{"net":1}`
	promo := `{"recommend":true}`
	if err := db.UpdateCandidateResults(cand.ID, &CandidateResults{
		ScreenResultJSON: &screen, Finalist: boolptr(true),
		CompareResultJSON: &compare, DownstreamResultJSON: &downstream,
		PromotionJSON: &promo,
	}); err != nil {
		t.Fatalf("UpdateCandidateResults: %v", err)
	}
	final, _ := db.GetCandidate(cand.ID)
	if !final.Finalist || final.ScreenResultJSON == nil || *final.ScreenResultJSON != screen ||
		final.CompareResultJSON == nil || final.DownstreamResultJSON == nil || final.PromotionJSON == nil {
		t.Fatalf("results not stored: %+v", final)
	}
	list, err := db.ListCandidatesByRun("run1")
	if err != nil || len(list) != 1 {
		t.Fatalf("ListCandidatesByRun = %v, %v", list, err)
	}
	if err := db.UpdateCandidateResults("missing", &CandidateResults{}); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("UpdateCandidateResults missing err = %v, want ErrNoRows", err)
	}
	if _, err := db.GetCandidate("missing"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("GetCandidate missing err = %v", err)
	}
	if _, err := db.UpsertCandidate("", "k", "s", `{}`, "ch"); err == nil {
		t.Fatal("UpsertCandidate empty run: want error")
	}
}

func boolptr(b bool) *bool { return &b }
