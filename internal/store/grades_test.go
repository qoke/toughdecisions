package store

import (
	"database/sql"
	"errors"
	"testing"
)

func TestGradeInsertGet(t *testing.T) {
	db := openTestDB(t)
	g, err := db.InsertGrade(&Grade{
		CacheKey: "grade-ck1", ResponseID: "resp1", GraderConfigHash: "ch1",
		RubricHash: "rh1", ScoresJSON: `{"role_execution":3}`,
		PassagesJSON: `{}`, WeaknessJSON: strptr(`{"passage":"p"}`),
		FlagsJSON: `[]`, NotesCheckJSON: `{}`, RawJSON: `{}`,
		RunID: strptr("run1"),
	})
	if err != nil {
		t.Fatalf("InsertGrade: %v", err)
	}
	byID, err := db.GetGrade(g.ID)
	if err != nil {
		t.Fatalf("GetGrade: %v", err)
	}
	byKey, err := db.GetGradeByCacheKey("grade-ck1")
	if err != nil {
		t.Fatalf("GetGradeByCacheKey: %v", err)
	}
	for _, got := range []*Grade{byID, byKey} {
		if got.CacheKey != "grade-ck1" || got.ResponseID != "resp1" ||
			got.GraderConfigHash != "ch1" || got.ScoresJSON != `{"role_execution":3}` ||
			got.WeaknessJSON == nil || got.RunID == nil || *got.RunID != "run1" {
			t.Fatalf("round-trip mismatch: %+v", got)
		}
	}
	if _, err := db.InsertGrade(&Grade{CacheKey: "grade-ck1", ResponseID: "resp2"}); err == nil {
		t.Fatal("duplicate cache_key: want error")
	}
	if _, err := db.GetGrade("missing"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("GetGrade missing err = %v", err)
	}
	if _, err := db.GetGradeByCacheKey("missing"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("GetGradeByCacheKey missing err = %v", err)
	}
	if _, err := db.InsertGrade(&Grade{}); err == nil {
		t.Fatal("InsertGrade empty: want error")
	}
}

func TestFlagInsertGetList(t *testing.T) {
	db := openTestDB(t)
	f, err := db.InsertFlag(&Flag{
		GradeID: "grade1", ResponseID: "resp1", Type: "fabrication",
		Passage: "p1", Violated: "v1", Status: "open", RunID: strptr("run1"),
	})
	if err != nil {
		t.Fatalf("InsertFlag: %v", err)
	}
	byID, err := db.GetFlag(f.ID)
	if err != nil {
		t.Fatalf("GetFlag: %v", err)
	}
	if byID.Type != "fabrication" || byID.Passage != "p1" || byID.Status != "open" {
		t.Fatalf("round-trip mismatch: %+v", byID)
	}
	open, err := db.ListFlagsByStatus("open")
	if err != nil || len(open) != 1 {
		t.Fatalf("ListFlagsByStatus open = %v, %v", open, err)
	}
	none, err := db.ListFlagsByStatus("confirmed")
	if err != nil || len(none) != 0 {
		t.Fatalf("ListFlagsByStatus confirmed = %v, %v", none, err)
	}
	note := "looks fine"
	if err := db.UpdateFlagStatus(f.ID, "dismissed", &note); err != nil {
		t.Fatalf("UpdateFlagStatus: %v", err)
	}
	after, _ := db.GetFlag(f.ID)
	if after.Status != "dismissed" || after.ResolutionNote == nil || *after.ResolutionNote != note {
		t.Fatalf("status not updated: %+v", after)
	}
	if err := db.UpdateFlagStatus("missing", "confirmed", nil); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("UpdateFlagStatus missing err = %v, want ErrNoRows", err)
	}
	if _, err := db.GetFlag("missing"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("GetFlag missing err = %v", err)
	}
	if _, err := db.InsertFlag(&Flag{}); err == nil {
		t.Fatal("InsertFlag empty: want error")
	}
}

func TestPairwiseInsertGet(t *testing.T) {
	db := openTestDB(t)
	p, err := db.InsertPairwise(&PairwiseRow{
		RunID: "run1", Purpose: "compare", CaseID: "c1", Seat: "judge",
		LeftResponseID: "l1", RightResponseID: "r1", GraderConfigHash: "ch1",
		LeftShownAs: "A", Verdict: "A", Margin: "clear",
		VerdictLeftRight: "left", ConsequentialDifference: "diff",
	})
	if err != nil {
		t.Fatalf("InsertPairwise: %v", err)
	}
	if p.Final {
		t.Fatal("new pairwise final, want false")
	}
	byID, err := db.GetPairwise(p.ID)
	if err != nil {
		t.Fatalf("GetPairwise: %v", err)
	}
	if byID.Verdict != "A" || byID.VerdictLeftRight != "left" || byID.Margin != "clear" {
		t.Fatalf("round-trip mismatch: %+v", byID)
	}
	rev, err := db.InsertPairwise(&PairwiseRow{
		RunID: "run1", Purpose: "compare", CaseID: "c1", Seat: "judge",
		LeftResponseID: "r1", RightResponseID: "l1", GraderConfigHash: "ch1",
		LeftShownAs: "B", Verdict: "B", Margin: "clear",
		VerdictLeftRight: "right", ConsequentialDifference: "diff",
		ReversalOfID: &p.ID, Final: true,
	})
	if err != nil {
		t.Fatalf("InsertPairwise reversal: %v", err)
	}
	got, _ := db.GetPairwise(rev.ID)
	if got.ReversalOfID == nil || *got.ReversalOfID != p.ID || !got.Final {
		t.Fatalf("reversal not stored: %+v", got)
	}
	rows, err := db.ListPairwiseByRun("run1", "c1")
	if err != nil || len(rows) != 2 {
		t.Fatalf("ListPairwiseByRun = %v, %v", rows, err)
	}
	if _, err := db.GetPairwise("missing"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("GetPairwise missing err = %v", err)
	}
	if _, err := db.InsertPairwise(&PairwiseRow{}); err == nil {
		t.Fatal("InsertPairwise empty: want error")
	}
}

func TestCoverageInsertList(t *testing.T) {
	db := openTestDB(t)
	row, err := db.InsertCoverage(&CoverageRow{
		RunID: "run1", CaseID: "c1", CouncilLabel: "candidate:x", IssueID: "F001-I1",
		IssueText: "text", IsPlanted: true, NoticedByJSON: `["judge"]`,
		UnsupportedByJSON: `[]`, JudgeOutcome: "preserved",
	})
	if err != nil {
		t.Fatalf("InsertCoverage: %v", err)
	}
	byID, err := db.GetCoverage(row.ID)
	if err != nil {
		t.Fatalf("GetCoverage: %v", err)
	}
	if byID.IssueID != "F001-I1" || !byID.IsPlanted || byID.JudgeOutcome != "preserved" {
		t.Fatalf("round-trip mismatch: %+v", byID)
	}
	rows, err := db.ListCoverageByRun("run1")
	if err != nil || len(rows) != 1 {
		t.Fatalf("ListCoverageByRun = %v, %v", rows, err)
	}
	empty, err := db.ListCoverageByRun("missing")
	if err != nil || len(empty) != 0 {
		t.Fatalf("ListCoverageByRun missing = %v, %v", empty, err)
	}
	if _, err := db.GetCoverage("missing"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("GetCoverage missing err = %v", err)
	}
	if _, err := db.InsertCoverage(&CoverageRow{}); err == nil {
		t.Fatal("InsertCoverage empty: want error")
	}
}
