package harness

import (
	"context"
	"testing"

	"github.com/qoke/toughdecisions/internal/gateway"
	"github.com/qoke/toughdecisions/internal/pack"
)

// Defect 1 (refuse): all baseline generations fail/substituted/timed-out,
// so --baselines-only must refuse with NoGradeableEvidenceError and write
// no baselines.
func TestPublishBaselinesOnlyRefusesFailedEvidence(t *testing.T) {
	fx := setupHarness(t, map[string][]gateway.Step{
		"v-poss":   {{Err: gateway.ErrGateway}},
		"v-persp":  {{Err: gateway.ErrGateway}},
		"v-stress": {{Err: gateway.ErrGateway}},
		"j1":       {{Err: gateway.ErrGateway}},
	})
	c := seedPublishSelection(t, fx)

	_, err := fx.runner.Publish(context.Background(), PublishOptions{BaselinesOnly: true})
	if err == nil {
		t.Fatal("baselines-only with all-failed generations: want refusal, got nil")
	}
	var blocked *NoGradeableEvidenceError
	if !isNoGradeable(err, &blocked) {
		t.Fatalf("err = %v; want NoGradeableEvidenceError", err)
	}
	for _, seat := range pack.AllSeats {
		if _, gerr := fx.db.GetBaseline(fx.pk.ID, string(seat), c.ID); gerr == nil {
			t.Fatalf("baseline %s written from failed evidence; want none", seat)
		}
	}
}

// Defect 1 (still publishes): a genuine successful run fills baselines and
// returns counts; guards against a blanket refusal.
func TestPublishBaselinesOnlyStillPublishesOnSuccess(t *testing.T) {
	scripts := map[string][]gateway.Step{}
	for _, m := range []string{"v-poss", "v-persp", "v-stress"} {
		scripts[m] = []gateway.Step{{Content: hViewJSON}}
	}
	scripts["j1"] = []gateway.Step{{Content: hJudgeJSON}}
	fx := setupHarness(t, scripts)
	c := seedPublishSelection(t, fx)

	got, err := fx.runner.Publish(context.Background(), PublishOptions{BaselinesOnly: true})
	if err != nil {
		t.Fatalf("baselines-only on success: %v", err)
	}
	if got.Baselines != len(pack.AllSeats) {
		t.Fatalf("baselines = %d; want %d", got.Baselines, len(pack.AllSeats))
	}
	for _, seat := range pack.AllSeats {
		bl, gerr := fx.db.GetBaseline(fx.pk.ID, string(seat), c.ID)
		if gerr != nil {
			t.Fatalf("baseline %s missing: %v", seat, gerr)
		}
		resp, gerr := fx.db.GetResponse(bl.ResponseID)
		if gerr != nil {
			t.Fatalf("baseline %s response: %v", seat, gerr)
		}
		if !gradeableResponse(resp) {
			t.Fatalf("baseline %s points at non-gradeable response %+v", seat, resp)
		}
	}
}

func isNoGradeable(err error, target **NoGradeableEvidenceError) bool {
	for err != nil {
		if e, ok := err.(*NoGradeableEvidenceError); ok {
			*target = e
			return true
		}
		u, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
	return false
}
