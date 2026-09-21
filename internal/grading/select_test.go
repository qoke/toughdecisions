package grading

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/qoke/toughdecisions/internal/config"
	"github.com/qoke/toughdecisions/internal/gateway"
	"github.com/qoke/toughdecisions/internal/prompts"
	"github.com/qoke/toughdecisions/internal/schema"
	"github.com/qoke/toughdecisions/internal/store"
)

func openTestDB(t *testing.T) *store.DB {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func testConfig(t *testing.T) *config.Config {
	t.Helper()
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	return cfg
}

func mkGrader(key, family, role string, admitted bool) *Grader {
	g := &Grader{
		GraderKey: key, Model: "model-" + key, Family: family,
		ParamsJSON: "{}", RubricHash: prompts.RubricHash(),
		ConfigHash: "cfg-" + key, Role: role, Admitted: admitted,
	}
	return g
}

func insertGrader(t *testing.T, db *store.DB, g *Grader) *Grader {
	t.Helper()
	stored, err := db.UpsertGraderConfig((*store.GraderConfig)(g))
	if err != nil {
		t.Fatalf("UpsertGraderConfig: %v", err)
	}
	if g.Admitted {
		at := "2026-09-21T00:00:00Z"
		if err := db.SetGraderCalibration(g.ConfigHash, `{"items":[]}`, true, &at); err != nil {
			t.Fatalf("SetGraderCalibration: %v", err)
		}
		stored, err = db.GetGraderConfig(g.ConfigHash)
		if err != nil {
			t.Fatalf("GetGraderConfig: %v", err)
		}
	}
	return (*Grader)(stored)
}

func mkResponse(t *testing.T, db *store.DB, seat, raw string) *store.Response {
	t.Helper()
	r, err := db.InsertResponse(&store.Response{
		CacheKey: "ck-" + seat + "-" + raw[:min(8, len(raw))],
		Seat:     seat, ConfigHash: "ch", PromptPackHash: "pp",
		InputHash: "ih", Origin: "harness", ModelRequested: "m", RawText: raw,
	})
	if err != nil {
		t.Fatalf("InsertResponse: %v", err)
	}
	return r
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func gradeJSON(scores map[string]int, flags string) string {
	m := map[string]any{
		"scores":              scores,
		"supporting_passages": map[string]string{"decision_insight": "p"},
		"notes_check":         map[string]any{"noticed": []string{}, "missed": []string{}, "beyond_notes": []string{}},
	}
	raw, _ := json.Marshal(m)
	s := string(raw)
	if flags != "" {
		s = strings.Replace(s, `"notes_check"`, `"flags":[`+flags+`],"notes_check"`, 1)
	}
	return s
}

func TestSelectGrader(t *testing.T) {
	a := mkGrader("a", "openai", "selection", true)
	b := mkGrader("b", "anthropic", "selection", true)
	sub := mkGrader("sub", "gemini", "substitute", true)

	// Arrange: first candidate differs in family.
	got, err := SelectGrader([]*Grader{a, b}, "anthropic", sub)
	// Assert: picks the first non-conflicting grader, never a sibling.
	if err != nil || got.GraderKey != "a" {
		t.Fatalf("SelectGrader = %+v, %v; want a", got, err)
	}

	// Arrange: all candidates share the response family.
	got, err = SelectGrader([]*Grader{a}, "openai", sub)
	if err != nil || got.GraderKey != "sub" {
		t.Fatalf("same-family routing = %+v, %v; want substitute", got, err)
	}

	// Arrange: substitute unadmitted.
	bad := mkGrader("bad", "gemini", "substitute", false)
	if _, err := SelectGrader([]*Grader{a}, "openai", bad); !errors.Is(err, ErrSubstituteUnadmitted) {
		t.Fatalf("unadmitted substitute err = %v; want ErrSubstituteUnadmitted", err)
	}

	// Arrange: no substitute configured.
	if _, err := SelectGrader([]*Grader{a}, "openai", nil); !errors.Is(err, ErrSameFamily) {
		t.Fatalf("absent substitute err = %v; want ErrSameFamily", err)
	}
}

func TestGradeCacheKey(t *testing.T) {
	a := GradeCacheKey("r1", "cfg", "rub")
	if a == "" || len(a) != 64 {
		t.Fatalf("key = %q; want 64 hex chars", a)
	}
	if got := GradeCacheKey("r1", "cfg", "rub"); got != a {
		t.Fatalf("same input gave %q vs %q", a, got)
	}
	for name, key := range map[string]string{
		"response": GradeCacheKey("r2", "cfg", "rub"),
		"config":   GradeCacheKey("r1", "other", "rub"),
		"rubric":   GradeCacheKey("r1", "cfg", "other"),
	} {
		if key == a {
			t.Fatalf("changing %s did not change the key", name)
		}
	}
}

func TestGradeInsertsGradeAndFlags(t *testing.T) {
	db := openTestDB(t)
	cfg := testConfig(t)
	g := insertGrader(t, db, mkGrader("ga", "openai", "selection", true))
	resp := mkResponse(t, db, "possibility", "some thoughtful advice text here")
	scores := map[string]int{
		"grounding_calibration": 3, "context_values_fidelity": 3,
		"decision_insight": 2, "practical_robustness": 3, "role_execution": 3,
	}
	flag := `{"type":"coercive","passage":"p","violated":"v"}`
	fake := gateway.NewFake(map[string][]gateway.Step{
		g.Model: {{Content: gradeJSON(scores, flag)}},
	})
	svc := NewService(db, fake, cfg)

	// Act.
	res, err := svc.Grade(context.Background(), schema.CaseInput{}, "", resp, g, nil)
	// Assert.
	if err != nil {
		t.Fatalf("Grade: %v", err)
	}
	if res.Grade.CacheKey != GradeCacheKey(resp.ID, g.ConfigHash, g.RubricHash) {
		t.Fatalf("cache key = %q", res.Grade.CacheKey)
	}
	if len(res.Flags) != 1 || res.Flags[0].Status != "open" {
		t.Fatalf("flags = %+v; want one open flag", res.Flags)
	}
	got, err := db.GetResponse(resp.ID)
	if err != nil {
		t.Fatalf("GetResponse: %v", err)
	}
	if got.WordCount != 5 {
		t.Fatalf("word_count = %d; want 5", got.WordCount)
	}
	if len(fake.Calls) != 1 {
		t.Fatalf("calls = %d; want 1", len(fake.Calls))
	}
}

func TestGradeCacheHitSkipsCall(t *testing.T) {
	db := openTestDB(t)
	cfg := testConfig(t)
	g := insertGrader(t, db, mkGrader("gb", "openai", "selection", true))
	resp := mkResponse(t, db, "perspective", "another response body here")
	scores := map[string]int{
		"grounding_calibration": 2, "context_values_fidelity": 2,
		"decision_insight": 2, "practical_robustness": 2, "role_execution": 2,
	}
	fake := gateway.NewFake(map[string][]gateway.Step{
		g.Model: {{Content: gradeJSON(scores, "")}},
	})
	svc := NewService(db, fake, cfg)

	// Arrange: first grade populates the cache.
	first, err := svc.Grade(context.Background(), schema.CaseInput{}, "", resp, g, nil)
	if err != nil {
		t.Fatalf("first Grade: %v", err)
	}
	// Act: second grade with the same grader + rubric.
	second, err := svc.Grade(context.Background(), schema.CaseInput{}, "", resp, g, nil)
	// Assert: cache hit returns the stored row without a new call.
	if err != nil {
		t.Fatalf("second Grade: %v", err)
	}
	if second.Grade.ID != first.Grade.ID {
		t.Fatalf("cache hit returned %s, want %s", second.Grade.ID, first.Grade.ID)
	}
	if fake.CallCount() != 1 {
		t.Fatalf("calls = %d; want 1", fake.CallCount())
	}
}

func TestGradeRubricChangeMissesCache(t *testing.T) {
	db := openTestDB(t)
	cfg := testConfig(t)
	g := insertGrader(t, db, mkGrader("gc", "openai", "selection", true))
	resp := mkResponse(t, db, "judge", "judge response body here")
	scores := map[string]int{
		"grounding_calibration": 3, "context_values_fidelity": 3,
		"decision_insight": 3, "practical_robustness": 3, "role_execution": 3,
	}
	fake := gateway.NewFake(map[string][]gateway.Step{
		g.Model: {{Content: gradeJSON(scores, "")}, {Content: gradeJSON(scores, "")}},
	})
	svc := NewService(db, fake, cfg)

	// Arrange.
	if _, err := svc.Grade(context.Background(), schema.CaseInput{}, "", resp, g, nil); err != nil {
		t.Fatalf("first Grade: %v", err)
	}
	// Act: rubric change alters the cache key.
	g2 := *g
	g2.RubricHash = "different-rubric"
	if _, err := svc.Grade(context.Background(), schema.CaseInput{}, "", resp, &g2, nil); err != nil {
		t.Fatalf("changed-rubric Grade: %v", err)
	}
	// Assert: a second call was made.
	if fake.CallCount() != 2 {
		t.Fatalf("calls = %d; want 2", fake.CallCount())
	}
}

func TestGradeRetriesOnceOnParseFailure(t *testing.T) {
	db := openTestDB(t)
	cfg := testConfig(t)
	g := insertGrader(t, db, mkGrader("gd", "openai", "selection", true))
	resp := mkResponse(t, db, "possibility", "retry response body here")
	scores := map[string]int{
		"grounding_calibration": 3, "context_values_fidelity": 3,
		"decision_insight": 3, "practical_robustness": 3, "role_execution": 3,
	}
	fake := gateway.NewFake(map[string][]gateway.Step{
		g.Model: {{Content: "not json at all"}, {Content: gradeJSON(scores, "")}},
	})
	svc := NewService(db, fake, cfg)

	// Act.
	res, err := svc.Grade(context.Background(), schema.CaseInput{}, "", resp, g, nil)
	// Assert: exactly one retry then success.
	if err != nil || res == nil {
		t.Fatalf("Grade = %+v, %v; want success after one retry", res, err)
	}
	if fake.CallCount() != 2 {
		t.Fatalf("calls = %d; want exactly 2", fake.CallCount())
	}
}

func TestGradeDoubleParseFailureErrors(t *testing.T) {
	db := openTestDB(t)
	cfg := testConfig(t)
	g := insertGrader(t, db, mkGrader("ge", "openai", "selection", true))
	resp := mkResponse(t, db, "possibility", "broken response body here")
	fake := gateway.NewFake(map[string][]gateway.Step{
		g.Model: {{Content: "garbage one"}, {Content: "garbage two"}},
	})
	svc := NewService(db, fake, cfg)

	// Act.
	res, err := svc.Grade(context.Background(), schema.CaseInput{}, "", resp, g, nil)
	// Assert: error after exactly one retry, no grade row cached.
	if err == nil || res != nil {
		t.Fatalf("Grade = %+v, %v; want error", res, err)
	}
	if fake.CallCount() != 2 {
		t.Fatalf("calls = %d; want exactly 2", fake.CallCount())
	}
	key := GradeCacheKey(resp.ID, g.ConfigHash, g.RubricHash)
	if _, err := db.GetGradeByCacheKey(key); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("grade cached despite double failure: %v", err)
	}
}
