package grading

import (
	"context"
	"errors"
	"testing"

	"github.com/qoke/toughdecisions/internal/gateway"
	"github.com/qoke/toughdecisions/internal/schema"
	"github.com/qoke/toughdecisions/internal/store"
)

func TestEdgeCases(t *testing.T) {
	db := openTestDB(t)
	cfg := testConfig(t)
	g := insertGrader(t, db, mkGrader("ea", "openai", "selection", true))
	gb := insertGrader(t, db, mkGrader("eb", "anthropic", "selection", true))
	left := mkResponse(t, db, "possibility", "left edge response body here")
	right := mkResponse(t, db, "possibility", "right edge response body now")
	svc := NewService(db, gateway.NewFake(nil), cfg)

	// Arrange/Act/Assert: Compare requires exactly two graders.
	if _, _, err := svc.Compare(context.Background(), schema.CaseInput{}, "", "compare", "c", "s", left, right, []*Grader{g}, "r"); err == nil {
		t.Fatal("Compare with 1 grader succeeded; want error")
	}
	// Nil responses.
	if _, _, err := svc.Compare(context.Background(), schema.CaseInput{}, "", "compare", "c", "s", nil, right, []*Grader{g, gb}, "r"); err == nil {
		t.Fatal("Compare with nil response succeeded; want error")
	}
	// Empty run/case.
	if _, _, err := svc.Compare(context.Background(), schema.CaseInput{}, "", "compare", "", "s", left, right, []*Grader{g, gb}, ""); err == nil {
		t.Fatal("Compare with empty ids succeeded; want error")
	}
	// Grade validation.
	if _, err := svc.Grade(context.Background(), schema.CaseInput{}, "", nil, g, nil); err == nil {
		t.Fatal("Grade with nil response succeeded; want error")
	}
	if _, err := svc.Grade(context.Background(), schema.CaseInput{}, "", left, nil, nil); err == nil {
		t.Fatal("Grade with nil grader succeeded; want error")
	}
	badResp := &store.Response{ID: "", RawText: "x"}
	if _, err := svc.Grade(context.Background(), schema.CaseInput{}, "", badResp, g, nil); err == nil {
		t.Fatal("Grade with empty response id succeeded; want error")
	}
	badGrader := *g
	badGrader.ConfigHash = ""
	if _, err := svc.Grade(context.Background(), schema.CaseInput{}, "", left, &badGrader, nil); err == nil {
		t.Fatal("Grade with empty config hash succeeded; want error")
	}
	// Cover validation.
	if err := svc.Cover(context.Background(), schema.CaseInput{}, "", "c", "l", []Issue{{ID: "i"}}, nil, "", g); err == nil {
		t.Fatal("Cover with empty run succeeded; want error")
	}
	if err := svc.Cover(context.Background(), schema.CaseInput{}, "r", "c", "l", nil, nil, "", g); err == nil {
		t.Fatal("Cover with no issues succeeded; want error")
	}
	if err := svc.Cover(context.Background(), schema.CaseInput{}, "r", "c", "l", []Issue{{ID: "i"}}, nil, "", nil); err == nil {
		t.Fatal("Cover with nil grader succeeded; want error")
	}
	// Calibrate/Recheck validation.
	if _, err := svc.Calibrate(context.Background(), nil); err == nil {
		t.Fatal("Calibrate with nil grader succeeded; want error")
	}
	if _, err := svc.Recheck(context.Background(), nil, 1); err == nil {
		t.Fatal("Recheck with nil grader succeeded; want error")
	}
	if _, err := svc.Recheck(context.Background(), g, 0); err == nil {
		t.Fatal("Recheck with n=0 succeeded; want error")
	}
	if _, err := svc.Recheck(context.Background(), g, 2); err == nil {
		t.Fatal("Recheck on empty set succeeded; want error")
	}
	// SelectGrader skips nil candidates.
	sub := mkGrader("esub", "gemini", "substitute", true)
	if got, err := SelectGrader([]*Grader{nil, gb}, "openai", sub); err != nil || got.GraderKey != "eb" {
		t.Fatalf("SelectGrader with nil = %+v, %v", got, err)
	}
	// Substitute sharing the family is rejected.
	sameSub := mkGrader("esame", "openai", "substitute", true)
	if _, err := SelectGrader([]*Grader{g}, "openai", sameSub); !errors.Is(err, ErrSameFamily) {
		t.Fatalf("same-family substitute err = %v", err)
	}
	// Blind nil + helpers.
	if b := Blind(nil); b.Role != "" || b.Rendered != "" {
		t.Fatalf("Blind(nil) = %+v", b)
	}
	if got := mean(map[string]int{}); got != 0 {
		t.Fatalf("mean(empty) = %v", got)
	}
	if err := validatePairwise(schema.Pairwise{Verdict: "A", Margin: "wide"}); err == nil {
		t.Fatal("validatePairwise bad margin succeeded; want error")
	}
	if err := validatePairwise(schema.Pairwise{Verdict: "C", Margin: "clear"}); err == nil {
		t.Fatal("validatePairwise bad verdict succeeded; want error")
	}
	// Coin flip error surfaces.
	origFlip := flip
	flip = func() (bool, error) { return false, errf("flip boom") }
	defer func() { flip = origFlip }()
	if _, _, err := svc.Compare(context.Background(), schema.CaseInput{}, "", "compare", "c", "s", left, right, []*Grader{g, gb}, "r"); err == nil {
		t.Fatal("Compare with flip error succeeded; want error")
	}
}

func TestPairwiseTieAndUnableReversal(t *testing.T) {
	db := openTestDB(t)
	cfg := testConfig(t)
	ga := insertGrader(t, db, mkGrader("ta", "openai", "selection", true))
	gb := insertGrader(t, db, mkGrader("tb", "anthropic", "selection", true))
	left := mkResponse(t, db, "possibility", "left tie response body here")
	right := mkResponse(t, db, "possibility", "right tie response now here")
	origFlip := flip
	flip = func() (bool, error) { return true, nil }
	defer func() { flip = origFlip }()
	// Arrange: tie then unable -> both runs non-agreeing -> final tie.
	fake := gateway.NewFake(map[string][]gateway.Step{
		ga.Model: {
			{Content: pairwiseJSON("tie", "clear", "no difference")},
			{Content: pairwiseJSON("unable", "close", "still unclear")},
		},
		gb.Model: {
			{Content: pairwiseJSON("tie", "clear", "no difference")},
			{Content: pairwiseJSON("tie", "clear", "still tied")},
		},
	})
	svc := NewService(db, fake, cfg)

	// Act.
	res, _, err := svc.Compare(context.Background(), schema.CaseInput{}, "", "tie", "caseT", "possibility", left, right, []*Grader{ga, gb}, "runT")
	// Assert.
	if err != nil || res.Verdict != "tie" {
		t.Fatalf("Compare = %+v, %v; want tie", res, err)
	}
}

func TestPairwiseCallFailures(t *testing.T) {
	db := openTestDB(t)
	cfg := testConfig(t)
	ga := insertGrader(t, db, mkGrader("fa", "openai", "selection", true))
	gb := insertGrader(t, db, mkGrader("fb", "anthropic", "selection", true))
	left := mkResponse(t, db, "possibility", "left fail response body here")
	right := mkResponse(t, db, "possibility", "right fail response now here")
	origFlip := flip
	flip = func() (bool, error) { return true, nil }
	defer func() { flip = origFlip }()
	svc := NewService(db, gateway.NewFake(map[string][]gateway.Step{
		ga.Model: {{Content: "junk one"}, {Content: "junk two"}},
		gb.Model: {{Content: pairwiseJSON("A", "clear", "ok")}},
	}), cfg)

	// Act: double parse failure on grader A.
	_, _, err := svc.Compare(context.Background(), schema.CaseInput{}, "", "compare", "caseF", "possibility", left, right, []*Grader{ga, gb}, "runF")
	// Assert.
	if err == nil {
		t.Fatal("Compare with double parse failure succeeded; want error")
	}

	// Arrange: invalid verdict then valid on retry.
	svc2 := NewService(db, gateway.NewFake(map[string][]gateway.Step{
		ga.Model: {
			{Content: pairwiseJSON("C", "clear", "bad")},
			{Content: pairwiseJSON("A", "clear", "good")},
		},
		gb.Model: {{Content: pairwiseJSON("A", "clear", "good")}},
	}), cfg)
	// Act.
	res, agreed, err := svc2.Compare(context.Background(), schema.CaseInput{}, "", "compare", "caseF2", "possibility", left, right, []*Grader{ga, gb}, "runF2")
	// Assert: retry recovers.
	if err != nil || !agreed || res.Verdict != "left" {
		t.Fatalf("Compare = %+v, %v, %v; want left+agreed", res, agreed, err)
	}
}
