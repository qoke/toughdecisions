package grading

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/qoke/toughdecisions/internal/gateway"
	"github.com/qoke/toughdecisions/internal/schema"
)

func coverageJSON(rows string) string {
	return `{"rows":[` + rows + `]}`
}

func TestCoverWritesRows(t *testing.T) {
	db := openTestDB(t)
	cfg := testConfig(t)
	g := insertGrader(t, db, mkGrader("ca", "openai", "selection", true))
	row := `{"issue_id":"I1","issue_text":"t","is_planted":true,"noticed_by":["possibility"],"unsupported_by":[],"judge_outcome":"preserved"}`
	fake := gateway.NewFake(map[string][]gateway.Step{
		g.Model: {{Content: coverageJSON(row)}},
	})
	svc := NewService(db, fake, cfg)
	planted := []Issue{{ID: "I1", Text: "t", IsPlanted: true}}

	// Act.
	err := svc.Cover(context.Background(), schema.CaseInput{}, "run1", "case1", "incumbent", planted,
		map[string]string{"possibility": "view text"}, "judge text", g)
	// Assert.
	if err != nil {
		t.Fatalf("Cover: %v", err)
	}
	rows, err := db.ListCoverageByRun("run1")
	if err != nil {
		t.Fatalf("ListCoverageByRun: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("rows = %d; want 1", len(rows))
	}
	var noticed []string
	if err := json.Unmarshal([]byte(rows[0].NoticedByJSON), &noticed); err != nil {
		t.Fatalf("noticed_by: %v", err)
	}
	if !rows[0].IsPlanted || rows[0].JudgeOutcome != "preserved" || len(noticed) != 1 || noticed[0] != "possibility" {
		t.Fatalf("row = %+v", rows[0])
	}
}

func TestCoverDoubleParseFailureErrors(t *testing.T) {
	db := openTestDB(t)
	cfg := testConfig(t)
	g := insertGrader(t, db, mkGrader("cb", "openai", "selection", true))
	fake := gateway.NewFake(map[string][]gateway.Step{
		g.Model: {{Content: "junk one"}, {Content: "junk two"}},
	})
	svc := NewService(db, fake, cfg)

	// Act.
	err := svc.Cover(context.Background(), schema.CaseInput{}, "run1", "case1", "incumbent",
		[]Issue{{ID: "I1", Text: "t"}}, nil, "", g)
	// Assert: exactly one retry then error.
	if err == nil {
		t.Fatal("Cover succeeded; want error")
	}
	if fake.CallCount() != 2 {
		t.Fatalf("calls = %d; want exactly 2", fake.CallCount())
	}
}

func TestNonGraderCallHasNoRetry(t *testing.T) {
	db := openTestDB(t)
	cfg := testConfig(t)
	g := insertGrader(t, db, mkGrader("cc", "openai", "selection", true))
	fake := gateway.NewFake(map[string][]gateway.Step{
		g.Model: {{Err: errBoom}},
	})
	svc := NewService(db, fake, cfg)

	// Act: a direct single chat call (the non-grader path) with a failing gateway.
	_, err := svc.chat(context.Background(), g, nil)
	// Assert: exactly one call, error surfaces unchanged.
	if err == nil {
		t.Fatal("chat succeeded; want gateway error")
	}
	if fake.CallCount() != 1 {
		t.Fatalf("calls = %d; want exactly 1 (no retry for non-grader path)", fake.CallCount())
	}
}

var errBoom = errBoomSentinel()

type boomErr struct{}

func (boomErr) Error() string { return "boom" }

func errBoomSentinel() error { return boomErr{} }
