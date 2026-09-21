package grading

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/qoke/toughdecisions/internal/gateway"
	"github.com/qoke/toughdecisions/internal/schema"
)

func pairwiseJSON(verdict, margin, diff string) string {
	raw, _ := json.Marshal(map[string]string{
		"verdict": verdict, "margin": margin, "consequential_difference": diff,
	})
	return string(raw)
}

func TestCompareAgrees(t *testing.T) {
	db := openTestDB(t)
	cfg := testConfig(t)
	ga := insertGrader(t, db, mkGrader("pa", "openai", "selection", true))
	gb := insertGrader(t, db, mkGrader("pb", "anthropic", "selection", true))
	left := mkResponse(t, db, "possibility", "left response body here now")
	right := mkResponse(t, db, "possibility", "right response body here now")
	origFlip := flip
	flip = func() (bool, error) { return true, nil }
	t.Cleanup(func() { flip = origFlip })
	fake := gateway.NewFake(map[string][]gateway.Step{
		ga.Model: {{Content: pairwiseJSON("A", "clear", "left names the cost")}},
		gb.Model: {{Content: pairwiseJSON("A", "clear", "left names the cost")}},
	})
	svc := NewService(db, fake, cfg)

	// Act.
	res, agreed, err := svc.Compare(context.Background(), schema.CaseInput{}, "", "compare", "case1", "possibility", left, right, []*Grader{ga, gb}, "run1")
	// Assert: both finals identical -> left with agreement.
	if err != nil || !agreed || res.Verdict != "left" {
		t.Fatalf("Compare = %+v, %v, %v; want left+agreed", res, agreed, err)
	}
	rows, err := db.ListPairwiseByRun("run1", "case1")
	if err != nil {
		t.Fatalf("ListPairwiseByRun: %v", err)
	}
	finals := 0
	for _, r := range rows {
		if r.Final {
			finals++
		}
	}
	if finals != 2 {
		t.Fatalf("final rows = %d; want 2 (one per grader)", finals)
	}
}

func TestCompareDisagreementIsTie(t *testing.T) {
	db := openTestDB(t)
	cfg := testConfig(t)
	ga := insertGrader(t, db, mkGrader("pc", "openai", "selection", true))
	gb := insertGrader(t, db, mkGrader("pd", "anthropic", "selection", true))
	left := mkResponse(t, db, "perspective", "left response body here now")
	right := mkResponse(t, db, "perspective", "right response body here now")
	origFlip := flip
	flip = func() (bool, error) { return true, nil }
	t.Cleanup(func() { flip = origFlip })
	fake := gateway.NewFake(map[string][]gateway.Step{
		ga.Model: {{Content: pairwiseJSON("A", "clear", "left better")}},
		gb.Model: {{Content: pairwiseJSON("B", "clear", "right better")}},
	})
	svc := NewService(db, fake, cfg)

	// Act.
	res, agreed, err := svc.Compare(context.Background(), schema.CaseInput{}, "", "compare", "case2", "perspective", left, right, []*Grader{ga, gb}, "run2")
	// Assert: grader disagreement resolves to tie.
	if err != nil || agreed || res.Verdict != "tie" {
		t.Fatalf("Compare = %+v, %v, %v; want tie+disagreement", res, agreed, err)
	}
}

func TestCompareCloseTriggersReversal(t *testing.T) {
	db := openTestDB(t)
	cfg := testConfig(t)
	ga := insertGrader(t, db, mkGrader("pe", "openai", "selection", true))
	gb := insertGrader(t, db, mkGrader("pf", "anthropic", "selection", true))
	left := mkResponse(t, db, "possibility", "left response body here now")
	right := mkResponse(t, db, "possibility", "right response body here now")
	origFlip := flip
	flip = func() (bool, error) { return true, nil }
	t.Cleanup(func() { flip = origFlip })
	fake := gateway.NewFake(map[string][]gateway.Step{
		ga.Model: {
			{Content: pairwiseJSON("A", "close", "left slightly better")},
			{Content: pairwiseJSON("B", "clear", "left better reversed")},
		},
		gb.Model: {{Content: pairwiseJSON("A", "clear", "left better")}},
	})
	svc := NewService(db, fake, cfg)

	// Act.
	res, _, err := svc.Compare(context.Background(), schema.CaseInput{}, "", "compare", "case3", "possibility", left, right, []*Grader{ga, gb}, "run3")
	// Assert: close triggered exactly one reversal; agreeing left/right wins.
	if err != nil || res.Verdict != "left" {
		t.Fatalf("Compare = %+v, %v; want left", res, err)
	}
	rows, err := db.ListPairwiseByRun("run3", "case3")
	if err != nil {
		t.Fatalf("ListPairwiseByRun: %v", err)
	}
	reversals := 0
	for _, r := range rows {
		if r.ReversalOfID != nil {
			reversals++
		}
	}
	if reversals != 1 {
		t.Fatalf("reversal rows = %d; want exactly 1", reversals)
	}
	if fake.CallCount() != 3 {
		t.Fatalf("calls = %d; want 3 (2 for ga incl. reversal, 1 for gb)", fake.CallCount())
	}
}

func TestCompareNormalisesShownOrder(t *testing.T) {
	db := openTestDB(t)
	cfg := testConfig(t)
	ga := insertGrader(t, db, mkGrader("pg", "openai", "selection", true))
	gb := insertGrader(t, db, mkGrader("ph", "anthropic", "selection", true))
	left := mkResponse(t, db, "possibility", "left response body here now")
	right := mkResponse(t, db, "possibility", "right response body here now")
	// Arrange: left shown as B, so an A verdict means right.
	origFlip := flip
	flip = func() (bool, error) { return false, nil }
	t.Cleanup(func() { flip = origFlip })
	fake := gateway.NewFake(map[string][]gateway.Step{
		ga.Model: {{Content: pairwiseJSON("A", "clear", "right better")}},
		gb.Model: {{Content: pairwiseJSON("A", "clear", "right better")}},
	})
	svc := NewService(db, fake, cfg)

	// Act.
	res, agreed, err := svc.Compare(context.Background(), schema.CaseInput{}, "", "compare", "case4", "possibility", left, right, []*Grader{ga, gb}, "run4")
	// Assert.
	if err != nil || !agreed || res.Verdict != "right" {
		t.Fatalf("Compare = %+v, %v, %v; want right+agreed", res, agreed, err)
	}
}
