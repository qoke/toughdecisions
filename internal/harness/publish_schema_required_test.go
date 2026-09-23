package harness

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/qoke/toughdecisions/internal/gateway"
	"github.com/qoke/toughdecisions/internal/pack"
	"github.com/qoke/toughdecisions/internal/store"
)

// TestGenerateResponseFailsClosedWithoutStrictSchema proves GenerateResponse
// rejects a json_object-only seat model before any gateway call.
func TestGenerateResponseFailsClosedWithoutStrictSchema(t *testing.T) {
	fx := setupHarness(t, map[string][]gateway.Step{
		"v-poss": {{Content: hViewJSON}},
	})
	fam := seedHFamily(t, fx.db, "FSC1", "development", []string{"hard"}, []string{"possibility"})
	c := seedHCase(t, fx.db, fam, "FSC1-base")
	in, _ := CaseInputFor(c)
	req := GenRequest{
		Seat:      pack.SeatPossibility,
		SeatCfg:   pack.SeatConfig{Seat: pack.SeatPossibility, Model: "council-basic", Family: "other"},
		Input:     in,
		InputHash: InputHashFor(in),
		RunID:     "run1",
	}
	_, err := fx.runner.GenerateResponse(context.Background(), req)
	if !errors.Is(err, gateway.ErrUnsupportedSetting) {
		t.Fatalf("err = %v; want ErrUnsupportedSetting", err)
	}
	if fx.fake.CallCount() != 0 {
		t.Fatalf("model calls = %d; want 0", fx.fake.CallCount())
	}
}

// TestPublishRefusesSchemaLessSeat proves seatsForPublish refuses a
// candidate pack whose seat model lacks strict json_schema before
// publishSeeds/PublishAtomic (no baselines, no pack row).
func TestPublishRefusesSchemaLessSeat(t *testing.T) {
	fx := setupHarness(t, nil)
	seedHFamily(t, fx.db, "FPUB", "selection", []string{"sentinel", "safety"}, []string{"possibility"})
	cur, err := pack.Show(fx.db, fx.pk.ID)
	if err != nil {
		t.Fatalf("Show: %v", err)
	}
	cfg := pack.SeatConfig{Seat: pack.SeatPossibility, Model: "council-basic", Family: "other"}
	raw, _ := json.Marshal(cfg)
	row := &store.Candidate{
		CandidateKey: "cand-basic", Seat: "possibility", ConfigJSON: string(raw),
	}
	if _, err := fx.runner.seatsForPublish(cur, row); !errors.Is(err, gateway.ErrUnsupportedSetting) {
		t.Fatalf("seatsForPublish err = %v; want ErrUnsupportedSetting", err)
	}
}

// TestPublishRefusesSchemaLessActiveSeat proves the whole-seat check: even
// when the candidate itself is fine, an active-pack seat without strict
// schema refuses publish.
func TestPublishRefusesSchemaLessActiveSeat(t *testing.T) {
	fx := setupHarness(t, nil)
	cfg := pack.SeatConfig{Seat: pack.SeatPossibility, Model: "cand-m", Family: "openai"}
	raw, _ := json.Marshal(cfg)
	row := &store.Candidate{
		CandidateKey: "cand-ok", Seat: "possibility", ConfigJSON: string(raw),
	}
	cur := &pack.Pack{
		ID: "cur",
		Seats: map[pack.Seat]pack.SeatConfig{
			pack.SeatPossibility:  cfg,
			pack.SeatPerspective:  {Seat: pack.SeatPerspective, Model: "council-basic", Family: "other"},
			pack.SeatStressTester: {Seat: pack.SeatStressTester, Model: "v-stress", Family: "openai"},
			pack.SeatJudge:        {Seat: pack.SeatJudge, Model: "j1", Family: "openai"},
		},
	}
	if _, err := fx.runner.seatsForPublish(cur, row); !errors.Is(err, gateway.ErrUnsupportedSetting) {
		t.Fatalf("seatsForPublish err = %v; want ErrUnsupportedSetting", err)
	}
}
