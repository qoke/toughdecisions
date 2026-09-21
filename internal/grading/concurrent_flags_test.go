package grading

import (
	"context"
	"sync"
	"testing"

	"github.com/qoke/toughdecisions/internal/gateway"
	"github.com/qoke/toughdecisions/internal/schema"
)

func TestGradeConcurrentSameResponseInsertsOneFlagSet(t *testing.T) {
	db := openTestDB(t)
	cfg := testConfig(t)
	g := insertGrader(t, db, mkGrader("conc", "openai", "selection", true))
	resp := mkResponse(t, db, "possibility", "concurrent grade body text here")
	scores := map[string]int{
		"grounding_calibration": 3, "context_values_fidelity": 3,
		"decision_insight": 3, "practical_robustness": 3, "role_execution": 3,
	}
	fake := gateway.NewFake(map[string][]gateway.Step{})
	svc := NewService(db, fake, cfg)

	// Arrange: one fresh scripted grade (two identical flags) per call —
	// concurrent losers must converge on a single grade + single flag set.
	const n = 8
	steps := make([]gateway.Step, 0, n)
	for i := 0; i < n; i++ {
		steps = append(steps, gateway.Step{Content: gradeJSON(scores, `{"type":"coercive","passage":"p","violated":"v"}`)})
	}
	fake.SetScript(g.Model, steps)

	// Act: N graders race the same response+grader.
	var wg sync.WaitGroup
	results := make([]*GradeResult, n)
	errs := make([]error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			res, err := svc.Grade(context.Background(), schema.CaseInput{}, "", resp, g, nil)
			results[i], errs[i] = res, err
		}(i)
	}
	wg.Wait()

	// Assert: every call succeeds against the same canonical grade, and
	// exactly one flag row exists (no TOCTOU duplicates).
	for i := 0; i < n; i++ {
		if errs[i] != nil {
			t.Fatalf("Grade %d: %v", i, errs[i])
		}
		if results[i].Grade.ID != results[0].Grade.ID {
			t.Fatalf("Grade %d id = %s, want %s", i, results[i].Grade.ID, results[0].Grade.ID)
		}
	}
	flags, err := db.ListFlagsByGradeID(results[0].Grade.ID)
	if err != nil {
		t.Fatalf("ListFlagsByGradeID: %v", err)
	}
	if len(flags) != 1 {
		t.Fatalf("flags = %d; want exactly 1", len(flags))
	}

	// Assert: the reuse path still returns the existing grade with its flags.
	reused, err := svc.Grade(context.Background(), schema.CaseInput{}, "", resp, g, nil)
	if err != nil {
		t.Fatalf("reuse Grade: %v", err)
	}
	if reused.Grade.ID != results[0].Grade.ID || len(reused.Flags) != 1 {
		t.Fatalf("reuse = %+v; want existing grade with 1 flag", reused.Grade)
	}
}
