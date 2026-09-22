package grading

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/qoke/toughdecisions/internal/gateway"
	"github.com/qoke/toughdecisions/internal/store"
)

// seedCalibCase stores one case with a distinctive marker plus its family,
// returning the case ID. The marker lets the test prove the grader saw the
// resolved case rather than a blank card.
func seedCalibCase(t *testing.T, db *store.DB, familyKey, caseKey, marker string) string {
	t.Helper()
	fam, err := db.UpsertFamily(&store.Family{
		FamilyKey: familyKey, Name: familyKey, Split: "development",
		AcceptanceJSON: `{"must_notice":["` + marker + `"]}`,
	})
	if err != nil {
		t.Fatalf("UpsertFamily: %v", err)
	}
	c, err := db.UpsertCase(&store.Case{
		CaseKey: caseKey, FamilyID: fam.ID, Variant: "base",
		InputJSON: `{"card":{"decision":"` + marker + `","context":"ctx","priorities":"p","unusual":"u","history":"h","deadline":"soon","style":"s"},"messages":[{"sender":"me","text":"` + marker + `"}],"question":"q?"}`,
	})
	if err != nil {
		t.Fatalf("UpsertCase: %v", err)
	}
	return c.ID
}

func putCalibItemForCase(t *testing.T, db *store.DB, key, caseID string, humanScores map[string]int, humanFlags []string) {
	t.Helper()
	hs, _ := json.Marshal(humanScores)
	hf, _ := json.Marshal(humanFlags)
	if _, err := db.UpsertCalibrationItem(&store.CalibrationItem{
		ItemKey: key, CaseID: caseID, Seat: "possibility",
		ResponseText:    "reference answer for " + key,
		HumanScoresJSON: string(hs), HumanFlagsJSON: string(hf),
	}); err != nil {
		t.Fatalf("UpsertCalibrationItem: %v", err)
	}
}

// TestCalibrateGradesAgainstResolvedCaseContext pins D5: calibration must
// grade each item against its own stored case (D5 passed an empty
// CaseInput, so graders saw a blank card and reversed). The capturing fake
// asserts the prompt carries the item's case marker; it fails if the old
// empty-CaseInput call returns.
func TestCalibrateGradesAgainstResolvedCaseContext(t *testing.T) {
	db := openTestDB(t)
	cfg := testConfig(t)
	g := insertGrader(t, db, mkGrader("d5ctx", "openai", "selection", false))
	caseID := seedCalibCase(t, db, "D5F", "D5CASE", "marker-d5-unique")
	putCalibItemForCase(t, db, "d5-item", caseID, evenScores(3), nil)
	var seen []string
	fake := gateway.NewFake(nil)
	fake.SkipStrictSchemaCheck = true
	fake.Scripts = map[string][]gateway.Step{g.Model: {{Content: gradeJSON(evenScores(3), "")}}}
	svc := NewService(db, &capturingClient{fake: fake, seen: &seen}, cfg)

	// Act.
	reversals, err := svc.Calibrate(context.Background(), g)
	// Assert.
	if err != nil || reversals != 0 {
		t.Fatalf("Calibrate = %d, %v; want 0, nil", reversals, err)
	}
	if len(seen) == 0 {
		t.Fatal("no grader prompts captured")
	}
	for _, body := range seen {
		if !strings.Contains(body, "marker-d5-unique") {
			t.Fatalf("calibration prompt lacks resolved case context:\n%s", body)
		}
	}
}

// TestCalibrateFailsClosedOnUnresolvableCase pins the D5 fail-closed
// invariant: an item whose CaseID resolves to no stored case, or which
// carries no case linkage at all, is an error — never a silent 0-score
// grade. (The store FK pins linkage at insert time, so the missing-row
// path is exercised at the resolver seam Calibrate grades through.)
func TestCalibrateFailsClosedOnUnresolvableCase(t *testing.T) {
	db := openTestDB(t)
	cfg := testConfig(t)
	svc := NewService(db, gateway.NewFake(nil), cfg)

	// Act: case row does not exist.
	_, _, err := svc.caseContextForItem(&store.CalibrationItem{ItemKey: "d5-ghost", CaseID: "case-id-missing"})
	// Assert: explicit error — a missing case never grades a blank card.
	if err == nil || !strings.Contains(err.Error(), "load case") {
		t.Fatalf("ghost case = %v; want load-case error", err)
	}

	// Act: no case linkage at all.
	_, _, err = svc.caseContextForItem(&store.CalibrationItem{ItemKey: "d5-nolink"})
	// Assert.
	if err == nil || !strings.Contains(err.Error(), "no case linkage") {
		t.Fatalf("unlinked item = %v; want no-case-linkage error", err)
	}
}

// TestCalibrateFailsClosedOnEmptyCaseInput pins the second half of the
// fail-closed invariant: a case row whose InputJSON decodes to an empty
// CaseInput (blank card, no messages, no question) errors rather than
// grading a blank card into a phantom 0.
func TestCalibrateFailsClosedOnEmptyCaseInput(t *testing.T) {
	db := openTestDB(t)
	cfg := testConfig(t)
	g := insertGrader(t, db, mkGrader("d5empty", "openai", "selection", false))
	fam, err := db.UpsertFamily(&store.Family{FamilyKey: "D5E", Name: "n", Split: "development"})
	if err != nil {
		t.Fatalf("UpsertFamily: %v", err)
	}
	c, err := db.UpsertCase(&store.Case{CaseKey: "d5-empty", FamilyID: fam.ID, Variant: "base", InputJSON: "{}"})
	if err != nil {
		t.Fatalf("UpsertCase: %v", err)
	}
	putCalibItemForCase(t, db, "d5-empty-item", c.ID, evenScores(3), nil)
	svc := NewService(db, gateway.NewFake(nil), cfg)

	// Act.
	_, err = svc.Calibrate(context.Background(), g)
	// Assert.
	if err == nil || !strings.Contains(err.Error(), "empty input") {
		t.Fatalf("Calibrate empty input = %v; want empty-input error", err)
	}
	stored, serr := db.GetGraderConfig(g.ConfigHash)
	if serr != nil {
		t.Fatalf("GetGraderConfig: %v", serr)
	}
	if stored.Admitted {
		t.Fatal("grader admitted on empty calibration case")
	}
}

// capturingClient wraps the scripted fake and records every user-message
// body so the test can assert on the case context the grader actually saw.
type capturingClient struct {
	fake *gateway.Fake
	seen *[]string
}

func (c *capturingClient) Chat(ctx context.Context, req gateway.ChatRequest) (gateway.ChatResponse, error) {
	for _, m := range req.Messages {
		if m.Role == "user" {
			*c.seen = append(*c.seen, m.Content)
		}
	}
	return c.fake.Chat(ctx, req)
}
