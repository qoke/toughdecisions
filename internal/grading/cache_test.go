package grading

import (
	"database/sql"
	"errors"
	"testing"

	"github.com/qoke/toughdecisions/internal/store"
)

// respKey is the harness response cache key under test: changing the SHARED
// instructions changes prompt_pack_hash, which must change the key.
func respKey(seat, configHash, packHash, inputHash string, rep int) string {
	return store.ResponseCacheKey(seat, configHash, packHash, inputHash, nil, rep)
}

func insertResp(t *testing.T, db *store.DB, key string) *store.Response {
	t.Helper()
	r, err := db.InsertResponse(&store.Response{
		CacheKey: key, Seat: "possibility", ConfigHash: "ch",
		PromptPackHash: "pp", InputHash: "ih", Origin: "harness",
		ModelRequested: "m", RawText: "cached response body",
	})
	if err != nil {
		t.Fatalf("InsertResponse: %v", err)
	}
	return r
}

func mustGradeHit(t *testing.T, db *store.DB, key string) {
	t.Helper()
	if _, err := db.GetGradeByCacheKey(key); err != nil {
		t.Fatalf("GetGradeByCacheKey(%q): %v", key, err)
	}
}

func mustGradeMiss(t *testing.T, db *store.DB, key string) {
	t.Helper()
	if _, err := db.GetGradeByCacheKey(key); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("GetGradeByCacheKey(%q) = %v; want ErrNoRows", key, err)
	}
}

func insertGrade(t *testing.T, db *store.DB, resp *store.Response, cfgHash, rubric string) {
	t.Helper()
	if _, err := db.InsertGrade(&store.Grade{
		CacheKey: GradeCacheKey(resp.ID, cfgHash, rubric), ResponseID: resp.ID,
		GraderConfigHash: cfgHash, RubricHash: rubric,
	}); err != nil {
		t.Fatalf("InsertGrade: %v", err)
	}
}

func TestResponseCacheMissOnSharedInstructionChange(t *testing.T) {
	// Arrange: a cached response under prompt pack "pp".
	base := respKey("possibility", "ch", "pp", "ih", 0)
	// Act: shared instructions change → new prompt_pack_hash.
	changed := respKey("possibility", "ch", "pp-v2", "ih", 0)
	// Assert: different response key (cache MISS).
	if changed == base {
		t.Fatalf("shared-instruction change did not change the response key")
	}
	// Assert: an identical recomputation still hits the old key.
	if same := respKey("possibility", "ch", "pp", "ih", 0); same != base {
		t.Fatalf("identical inputs gave %q vs %q", same, base)
	}
}

func TestGraderChangeMissesGradeButHitsResponse(t *testing.T) {
	db := openTestDB(t)

	// Arrange: one cached response graded by grader config "cfg-old".
	resp := insertResp(t, db, respKey("possibility", "ch", "pp", "ih", 0))
	insertGrade(t, db, resp, "cfg-old", "rub")
	if _, err := db.GetResponseByCacheKey(resp.CacheKey); err != nil {
		t.Fatalf("GetResponseByCacheKey: %v", err)
	}

	// Act: the grader config changes (new config_hash) / the rubric changes.
	newCfgKey := GradeCacheKey(resp.ID, "cfg-new", "rub")
	newRubricKey := GradeCacheKey(resp.ID, "cfg-old", "rub-v2")

	// Assert: RESPONSE cache still hits (same response key resolves).
	if _, err := db.GetResponseByCacheKey(resp.CacheKey); err != nil {
		t.Fatalf("response cache missed after grader change: %v", err)
	}
	// Assert: GRADE cache misses for both the new config_hash and the new
	// rubric (fd-review B2 invariant) while the old key still hits.
	mustGradeMiss(t, db, newCfgKey)
	mustGradeMiss(t, db, newRubricKey)
	mustGradeHit(t, db, GradeCacheKey(resp.ID, "cfg-old", "rub"))
}

func TestFreshRepetitionProducesNewResponseKey(t *testing.T) {
	db := openTestDB(t)

	// Arrange: repetition 0 is cached.
	insertResp(t, db, respKey("possibility", "ch", "pp", "ih", 0))
	max, err := db.MaxRepetition("possibility", "ch", "pp", "ih", nil)
	if err != nil {
		t.Fatalf("MaxRepetition: %v", err)
	}
	// Act: --fresh semantics insert with max+1.
	fresh := respKey("possibility", "ch", "pp", "ih", max+1)
	// Assert: the fresh key differs (cache MISS by construction).
	if fresh == respKey("possibility", "ch", "pp", "ih", 0) {
		t.Fatalf("--fresh repetition did not change the response key")
	}
	if _, err := db.GetResponseByCacheKey(fresh); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("fresh key unexpectedly hits: %v", err)
	}
}

func TestGradeKeyStableForIdenticalInputs(t *testing.T) {
	// Arrange: fixed grade inputs.
	a := GradeCacheKey("resp-1", "cfg-1", "rub-1")
	// Act + Assert: identical inputs are stable, and each input is load-
	// bearing (a change to any one of them misses).
	if b := GradeCacheKey("resp-1", "cfg-1", "rub-1"); b != a {
		t.Fatalf("same inputs gave %q vs %q", a, b)
	}
	for name, key := range map[string]string{
		"response": GradeCacheKey("resp-2", "cfg-1", "rub-1"),
		"config":   GradeCacheKey("resp-1", "cfg-2", "rub-1"),
		"rubric":   GradeCacheKey("resp-1", "cfg-1", "rub-2"),
	} {
		if key == a {
			t.Fatalf("changing %s did not change the grade key", name)
		}
	}
}
