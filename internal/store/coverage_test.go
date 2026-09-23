package store

import (
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"
)

// TestLatestCandidateRunID covers resolution of the compare run id: an empty
// table reports sql.ErrNoRows, the newest candidate that actually holds a
// compare result wins, and candidates without a compare result are ignored.
func TestLatestCandidateRunID(t *testing.T) {
	db := openTestDB(t)

	// Arrange: no candidates yet.
	// Act.
	_, err := db.LatestCandidateRunID()
	// Assert: the wrapped no-rows error is observable and identifiable.
	if !errors.Is(err, sql.ErrNoRows) || !strings.Contains(err.Error(), "store: latest candidate run") {
		t.Fatalf("LatestCandidateRunID on empty db err = %v, want wrapped sql.ErrNoRows", err)
	}

	// Arrange: runA (old, compared), runB (new, compared), runC (newest, no
	// compare result yet).
	cmp := `{"wins":2}`
	a, err := db.UpsertCandidate("runA", "ca", "judge", `{}`, "chA")
	if err != nil {
		t.Fatalf("UpsertCandidate runA: %v", err)
	}
	b, err := db.UpsertCandidate("runB", "cb", "judge", `{}`, "chB")
	if err != nil {
		t.Fatalf("UpsertCandidate runB: %v", err)
	}
	if _, err := db.UpsertCandidate("runC", "cc", "judge", `{}`, "chC"); err != nil {
		t.Fatalf("UpsertCandidate runC: %v", err)
	}
	if err := db.UpdateCandidateResults(a.ID, &CandidateResults{CompareResultJSON: &cmp}); err != nil {
		t.Fatalf("UpdateCandidateResults runA: %v", err)
	}
	if err := db.UpdateCandidateResults(b.ID, &CandidateResults{CompareResultJSON: &cmp}); err != nil {
		t.Fatalf("UpdateCandidateResults runB: %v", err)
	}
	backdate(t, db, "candidates", a.ID, "2020-01-01T00:00:00Z")
	// c keeps the newest created_at but has no compare result.

	// Act.
	got, err := db.LatestCandidateRunID()
	// Assert: runB is the newest compared run; runC must not win.
	if err != nil || got != "runB" {
		t.Fatalf("LatestCandidateRunID = %q, %v; want runB", got, err)
	}

	// Act: dropping runB's compare result leaves runA as the newest compared
	// run (UpdateCandidateResults keeps existing values on nil, so clear via
	// direct SQL the way backdate does).
	if _, err := db.db.Exec(`UPDATE candidates SET compare_result_json=NULL WHERE id=?`, b.ID); err != nil {
		t.Fatalf("clear runB compare: %v", err)
	}
	got, err = db.LatestCandidateRunID()
	// Assert.
	if err != nil || got != "runA" {
		t.Fatalf("LatestCandidateRunID after clearing runB = %q, %v; want runA", got, err)
	}
}

// TestSetResponseWordCountIfMissingKeepsExisting verifies the one mutation
// graders may make: it stamps a missing word_count, refuses to overwrite a
// recorded one, and treats an unknown id as a no-op rather than an error.
func TestSetResponseWordCountIfMissingKeepsExisting(t *testing.T) {
	db := openTestDB(t)

	// Arrange.
	r, err := db.InsertResponse(&Response{
		CacheKey: "wc-1", Seat: "judge", ConfigHash: "ch", PromptPackHash: "pp",
		InputHash: "ih", Origin: "production", ModelRequested: "m", RawText: "r",
	})
	if err != nil {
		t.Fatalf("InsertResponse: %v", err)
	}
	got, err := db.GetResponse(r.ID)
	if err != nil {
		t.Fatalf("GetResponse: %v", err)
	}
	if got.WordCount != 0 {
		t.Fatalf("fresh response word_count = %d, want 0", got.WordCount)
	}

	// Act: stamp the missing count.
	if err := db.SetResponseWordCountIfMissing(r.ID, 42); err != nil {
		t.Fatalf("SetResponseWordCountIfMissing: %v", err)
	}
	// Assert.
	got, err = db.GetResponse(r.ID)
	if err != nil {
		t.Fatalf("GetResponse after set: %v", err)
	}
	if got.WordCount != 42 {
		t.Fatalf("word_count = %d, want 42", got.WordCount)
	}

	// Act: a second call must not overwrite the recorded count.
	if err := db.SetResponseWordCountIfMissing(r.ID, 99); err != nil {
		t.Fatalf("SetResponseWordCountIfMissing again: %v", err)
	}
	// Assert.
	got, _ = db.GetResponse(r.ID)
	if got.WordCount != 42 {
		t.Fatalf("word_count overwritten to %d, want 42", got.WordCount)
	}

	// Act: unknown id updates zero rows and is not an error.
	if err := db.SetResponseWordCountIfMissing("missing", 7); err != nil {
		t.Fatalf("SetResponseWordCountIfMissing missing id: %v", err)
	}
}

// TestUpsertCandidateRejectsEmptyKey verifies the second validation guard.
func TestUpsertCandidateRejectsEmptyKey(t *testing.T) {
	db := openTestDB(t)

	// Act.
	_, err := db.UpsertCandidate("run1", "", "judge", `{}`, "ch")
	// Assert.
	if err == nil || !strings.Contains(err.Error(), "empty candidate_key") {
		t.Fatalf("UpsertCandidate empty key err = %v, want empty candidate_key", err)
	}
}

// TestFinishHarnessRunDefaultsFinishedAt verifies the server stamps a finish
// time when the caller leaves finished_at nil.
func TestFinishHarnessRunDefaultsFinishedAt(t *testing.T) {
	db := openTestDB(t)

	// Arrange.
	r, err := db.CreateHarnessRun("weekly", "pack1", `{}`)
	if err != nil {
		t.Fatalf("CreateHarnessRun: %v", err)
	}

	// Act: finish with nil finished_at (and nil optional fields).
	if err := db.FinishHarnessRun(r.ID, "complete", nil, nil, nil); err != nil {
		t.Fatalf("FinishHarnessRun: %v", err)
	}

	// Assert: a finish time was stamped and the row is finished.
	done, err := db.GetHarnessRun(r.ID)
	if err != nil {
		t.Fatalf("GetHarnessRun: %v", err)
	}
	if done.Status != "complete" {
		t.Fatalf("status = %q, want complete", done.Status)
	}
	if done.FinishedAt == nil || *done.FinishedAt == "" {
		t.Fatalf("FinishedAt = %v, want a stamped time", done.FinishedAt)
	}
}

// TestInsertCoverageRejectsEmptyCaseID verifies the case_id validation guard.
func TestInsertCoverageRejectsEmptyCaseID(t *testing.T) {
	db := openTestDB(t)

	// Act.
	_, err := db.InsertCoverage(&CoverageRow{RunID: "run1"})
	// Assert.
	if err == nil || !strings.Contains(err.Error(), "empty case_id") {
		t.Fatalf("InsertCoverage empty case err = %v, want empty case_id", err)
	}
}

// TestClosedDatabaseSurfacesErrors verifies each affected store method
// reports the closed database to its caller with its own error prefix,
// instead of succeeding or panicking.
func TestClosedDatabaseSurfacesErrors(t *testing.T) {
	cases := []struct {
		name string
		want string
		call func(db *DB) error
	}{
		{
			name: "should surface insert response error when the database is closed",
			want: "store: insert response",
			call: func(db *DB) error {
				_, err := db.InsertResponse(&Response{CacheKey: "ck", Seat: "judge", Origin: "production"})
				return err
			},
		},
		{
			name: "should surface set response word count error when the database is closed",
			want: "store: set response word_count",
			call: func(db *DB) error {
				return db.SetResponseWordCountIfMissing("id", 1)
			},
		},
		{
			name: "should surface insert request error when the database is closed",
			want: "store: insert request",
			call: func(db *DB) error {
				_, err := db.InsertRequest(&Request{ThreadID: "th", CardID: "ca", PackID: "pk", State: "created"})
				return err
			},
		},
		{
			name: "should surface insert request view error when the database is closed",
			want: "store: insert request view",
			call: func(db *DB) error {
				_, err := db.InsertRequestView("req", "possibility", "pending")
				return err
			},
		},
		{
			name: "should surface update request state error when the database is closed",
			want: "store: update request state",
			call: func(db *DB) error {
				return db.UpdateRequestState("req", "complete")
			},
		},
		{
			name: "should surface insert feedback error when the database is closed",
			want: "store: insert feedback",
			call: func(db *DB) error {
				_, err := db.InsertFeedback("req", "too_slow", "n")
				return err
			},
		},
		{
			name: "should surface insert coverage error when the database is closed",
			want: "store: insert coverage",
			call: func(db *DB) error {
				_, err := db.InsertCoverage(&CoverageRow{RunID: "r", CaseID: "c"})
				return err
			},
		},
		{
			name: "should surface list candidates error when the database is closed",
			want: "store: list candidates",
			call: func(db *DB) error {
				_, err := db.ListCandidatesByRun("run")
				return err
			},
		},
		{
			name: "should surface list coverage error when the database is closed",
			want: "store: list coverage",
			call: func(db *DB) error {
				_, err := db.ListCoverageByRun("run")
				return err
			},
		},
		{
			name: "should surface list requests error when the database is closed",
			want: "store: list requests",
			call: func(db *DB) error {
				_, err := db.ListRequestsByThread("th")
				return err
			},
		},
		{
			name: "should surface finish harness run error when the database is closed",
			want: "store: finish harness run",
			call: func(db *DB) error {
				return db.FinishHarnessRun("id", "complete", nil, nil, nil)
			},
		},
		{
			name: "should surface update harness run status error when the database is closed",
			want: "store: update harness run status",
			call: func(db *DB) error {
				return db.UpdateHarnessRunStatus("id", "running")
			},
		},
		{
			name: "should surface timeout counts error when the database is closed",
			want: "store: timeout counts",
			call: func(db *DB) error {
				_, err := db.TimeoutCounts("2020-01-01T00:00:00Z")
				return err
			},
		},
		{
			name: "should surface request count error when the database is closed",
			want: "store: request count",
			call: func(db *DB) error {
				_, _, _, err := db.RequestTimings("2020-01-01T00:00:00Z")
				return err
			},
		},
		{
			name: "should surface view state counts error when the database is closed",
			want: "store: view state counts",
			call: func(db *DB) error {
				_, err := db.ViewStateCounts("2020-01-01T00:00:00Z")
				return err
			},
		},
		{
			name: "should surface feedback tag counts error when the database is closed",
			want: "store: feedback tag counts",
			call: func(db *DB) error {
				_, err := db.FeedbackTagCounts("2020-01-01T00:00:00Z")
				return err
			},
		},
		{
			name: "should surface prune error when the database is closed",
			want: "store: prune",
			call: func(db *DB) error {
				_, err := db.PruneProduction(time.Now())
				return err
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Arrange: a fresh database that is then closed.
			db := openTestDB(t)
			if err := db.Close(); err != nil {
				t.Fatalf("Close: %v", err)
			}
			// Act.
			err := tc.call(db)
			// Assert: error identity is the method's own wrapped message.
			if err == nil {
				t.Fatal("want error on closed database, got nil")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %q, want substring %q", err.Error(), tc.want)
			}
		})
	}
}
