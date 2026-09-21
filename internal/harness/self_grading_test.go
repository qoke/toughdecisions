package harness

import (
	"context"
	"strings"
	"testing"

	"github.com/qoke/toughdecisions/internal/gateway"
	"github.com/qoke/toughdecisions/internal/grading"
	"github.com/qoke/toughdecisions/internal/schema"
	"github.com/qoke/toughdecisions/internal/store"
)

func sgCaseInput() schema.CaseInput {
	return schema.CaseInput{
		Card:     schema.Card{Decision: "d", Context: "c"},
		Messages: []schema.Message{{Sender: "them", Text: "hi"}},
		Question: "q-sg",
	}
}

// C1: a grader whose model family matches the response family routes to
// the admitted substitute through the harness (pairwiseBothFamilies), not
// just unit-tested SelectGrader.
func TestPairwiseBothFamiliesUsesSubstitute(t *testing.T) {
	fx := setupHarness(t, map[string][]gateway.Step{
		"gsub": {
			{Content: `{"verdict":"A","margin":"clear","consequential_difference":"d"}`},
		},
		"gs2": {
			{Content: `{"verdict":"A","margin":"clear","consequential_difference":"d"}`},
		},
	})
	conflicted := insertHGrader(t, fx.db, "sa", "gs1", "openai", "selection", true)
	other := insertHGrader(t, fx.db, "sb", "gs2", "anthropic", "selection", true)
	insertHGrader(t, fx.db, "sub", "gsub", "google", "substitute", true)
	fresh, err := fx.db.InsertResponse(&store.Response{CacheKey: "sg-l", Seat: "possibility", ConfigHash: "ch", PromptPackHash: "pp", InputHash: "ih", Origin: "harness", ModelRequested: "m", RawText: "left body"})
	if err != nil {
		t.Fatalf("seed left: %v", err)
	}
	right, err := fx.db.InsertResponse(&store.Response{CacheKey: "sg-r", Seat: "possibility", ConfigHash: "ch", PromptPackHash: "pp", InputHash: "ih2", Origin: "harness", ModelRequested: "m", RawText: "right body"})
	if err != nil {
		t.Fatalf("seed right: %v", err)
	}

	// Act: both selection graders see a response family of "openai", so
	// the openai grader must route to the substitute while the anthropic
	// grader stays eligible.
	_, _, _, err = fx.runner.pairwiseBothFamilies(context.Background(), "compare",
		"case-sg", "possibility", sgCaseInput(), "{}", []string{"openai"},
		fresh, right, []*grading.Grader{conflicted, other}, "run-sg")

	// Assert: openai slot served by the substitute, anthropic slot by sb
	// (2 grader calls: one gsub, one gs2). Deterministic under the
	// crypto/rand coin flip: routing is asserted on the SET of grader
	// config hashes persisted per pairwise row (resolveGraders is
	// positional, but the set of serving graders is flip-independent).
	if err != nil {
		t.Fatalf("pairwiseBothFamilies: %v", err)
	}
	if n := countModelCalls(fx, "gsub"); n != 1 {
		t.Fatalf("substitute calls = %d; want 1 (conflicted slot only)", n)
	}
	if n := countModelCalls(fx, "gs2"); n != 1 {
		t.Fatalf("eligible grader calls = %d; want 1 (sb serves its slot)", n)
	}
	if n := countModelCalls(fx, "gs1"); n != 0 {
		t.Fatalf("conflicted grader calls = %d; want 0 (sa must be substituted)", n)
	}
	for _, c := range fx.fake.Calls {
		if !(strings.Contains(c.Model, "gsub") || strings.Contains(c.Model, "gs2")) {
			t.Fatalf("model call = %q; want gsub or gs2 only", c.Model)
		}
	}
	rows, lerr := fx.db.ListPairwiseByRun("run-sg", "case-sg")
	if lerr != nil {
		t.Fatalf("ListPairwiseByRun: %v", lerr)
	}
	if len(rows) != 2 {
		t.Fatalf("pairwise rows = %d; want 2 (one per serving grader)", len(rows))
	}
	got := map[string]bool{}
	for _, row := range rows {
		got[row.GraderConfigHash] = true
	}
	if !got["cfg-sub"] || !got["cfg-sb"] || len(got) != 2 {
		t.Fatalf("serving grader hashes = %v; want exactly {cfg-sub cfg-sb}", got)
	}
	// No verdict/agreement assertion here by design: each grader flips
	// its own left_shown_as coin, so agreement is 50/50 even when both
	// answer "A". Routing is proven by the call counts and hashes above.
}

// C1: an unadmitted (or absent) substitute is a hard error, never a
// sibling grader.
func TestPairwiseBothFamiliesHardErrorsWithoutAdmittedSubstitute(t *testing.T) {
	for _, name := range []string{"absent", "unadmitted"} {
		fx := setupHarness(t, nil)
		conflicted := insertHGrader(t, fx.db, "sa", "gs1", "openai", "selection", true)
		other := insertHGrader(t, fx.db, "sb", "gs2", "openai", "selection", true)
		if name == "unadmitted" {
			insertHGrader(t, fx.db, "sub", "gsub", "google", "substitute", false)
		}
		fresh, _ := fx.db.InsertResponse(&store.Response{CacheKey: "sg-l-" + name, Seat: "possibility", ConfigHash: "ch", PromptPackHash: "pp", InputHash: "ih", Origin: "harness", ModelRequested: "m", RawText: "left body"})
		right, _ := fx.db.InsertResponse(&store.Response{CacheKey: "sg-r-" + name, Seat: "possibility", ConfigHash: "ch", PromptPackHash: "pp", InputHash: "ih2", Origin: "harness", ModelRequested: "m", RawText: "right body"})

		// Act: every selection grader conflicts and no admitted
		// substitute can serve.
		_, _, _, err := fx.runner.pairwiseBothFamilies(context.Background(), "compare",
			"case-sg", "possibility", sgCaseInput(), "{}", []string{"openai"},
			fresh, right, []*grading.Grader{conflicted, other}, "run-sg")

		// Assert: hard error, and no grader model was called.
		if err == nil {
			t.Fatalf("%s: want hard error, got nil", name)
		}
		if len(fx.fake.Calls) != 0 {
			t.Fatalf("%s: %d model calls; want 0 (refuse, never sibling)", name, len(fx.fake.Calls))
		}
	}
}

// C1: compareDefault threads compareInput.ResponseFamilies end to end.
func TestCompareDefaultRoutesFamiliesToSubstitute(t *testing.T) {
	fx := setupHarness(t, map[string][]gateway.Step{
		"gsub": {
			{Content: `{"verdict":"A","margin":"clear","consequential_difference":"d"}`},
			{Content: `{"verdict":"A","margin":"clear","consequential_difference":"d"}`},
		},
	})
	insertHGrader(t, fx.db, "sa", "gs1", "openai", "selection", true)
	insertHGrader(t, fx.db, "sb", "gs2", "openai", "selection", true)
	insertHGrader(t, fx.db, "sub", "gsub", "google", "substitute", true)
	graders, _ := fx.runner.SelectionGraders()
	fresh, _ := fx.db.InsertResponse(&store.Response{CacheKey: "cd-l", Seat: "possibility", ConfigHash: "ch", PromptPackHash: "pp", InputHash: "ih", Origin: "harness", ModelRequested: "m", RawText: "a"})
	base, _ := fx.db.InsertResponse(&store.Response{CacheKey: "cd-r", Seat: "possibility", ConfigHash: "ch", PromptPackHash: "pp", InputHash: "ih2", Origin: "harness", ModelRequested: "m", RawText: "b"})

	// Act.
	_, _, _, err := fx.runner.compareDefault(context.Background(), compareInput{
		Acceptance: "{}", CaseID: "c", Seat: "possibility",
		ResponseFamilies: []string{"openai"},
	}, fresh, base, graders, "run1")

	// Assert: both conflicting slots routed to the substitute, never
	// graded by a sibling. Flip-independent: two pairwise rows must both
	// carry the substitute config hash.
	if err != nil {
		t.Fatalf("compareDefault: %v", err)
	}
	for _, c := range fx.fake.Calls {
		if !strings.Contains(c.Model, "gsub") {
			t.Fatalf("model call = %q; want only the substitute", c.Model)
		}
	}
	rows, lerr := fx.db.ListPairwiseByRun("run1", "c")
	if lerr != nil {
		t.Fatalf("ListPairwiseByRun: %v", lerr)
	}
	if len(rows) != 2 {
		t.Fatalf("pairwise rows = %d; want 2 (one per substitute slot)", len(rows))
	}
	for _, row := range rows {
		if row.GraderConfigHash != "cfg-sub" {
			t.Fatalf("serving grader hash = %q; want cfg-sub on every row", row.GraderConfigHash)
		}
	}
}
