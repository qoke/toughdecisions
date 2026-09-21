package harness

import (
	"context"
	"testing"

	"github.com/qoke/toughdecisions/internal/gateway"
	"github.com/qoke/toughdecisions/internal/pack"
)

// TestPublishBaselinesOnlySkipsFailedSeats pins Finding 2's partial-case
// policy: only gradeable responses become baselines, the skipped count is
// reported (never silent), and the gradeable seats still publish.
func TestPublishBaselinesOnlySkipsFailedSeats(t *testing.T) {
	// Arrange: three gradeable views, a failing judge seat.
	scripts := map[string][]gateway.Step{}
	for _, m := range []string{"v-poss", "v-persp", "v-stress"} {
		scripts[m] = []gateway.Step{{Content: hViewJSON}}
	}
	scripts["j1"] = []gateway.Step{{Err: gateway.ErrGateway}}
	fx := setupHarness(t, scripts)
	c := seedPublishSelection(t, fx)

	// Act.
	got, err := fx.runner.Publish(context.Background(), PublishOptions{BaselinesOnly: true})

	// Assert: publish succeeds with 3 gradeable baselines and 1 reported skip.
	if err != nil {
		t.Fatalf("baselines-only with one failed seat: %v", err)
	}
	if got.SkippedBaselines != 1 {
		t.Fatalf("SkippedBaselines = %d; want 1 (judge seat failed, never silent)", got.SkippedBaselines)
	}
	if got.Baselines != len(pack.AllSeats)-1 {
		t.Fatalf("Baselines = %d; want %d (gradeable seats only)", got.Baselines, len(pack.AllSeats)-1)
	}
	for _, seat := range []pack.Seat{pack.SeatPossibility, pack.SeatPerspective, pack.SeatStressTester} {
		bl, gerr := fx.db.GetBaseline(fx.pk.ID, string(seat), c.ID)
		if gerr != nil {
			t.Fatalf("baseline %s missing: %v", seat, gerr)
		}
		resp, gerr := fx.db.GetResponse(bl.ResponseID)
		if gerr != nil || !gradeableResponse(resp) {
			t.Fatalf("baseline %s = %+v, %v; want a gradeable response", seat, resp, gerr)
		}
	}
	if _, gerr := fx.db.GetBaseline(fx.pk.ID, string(pack.SeatJudge), c.ID); gerr == nil {
		t.Fatal("judge baseline written from failed evidence; want it absent, not substituted")
	}
}
