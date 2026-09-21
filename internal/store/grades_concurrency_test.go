package store

import (
	"sync"
	"testing"
)

// H2: concurrent same-key grade inserts dedupe to the pre-existing row
// (mirroring the response insertOrReuse contract), never a UNIQUE error
// and never divergent rows.
func TestGradeConcurrentDuplicateKeyReusesRow(t *testing.T) {
	db := openTestDB(t)
	seed, err := db.InsertGrade(&Grade{
		CacheKey: "grade-race", ResponseID: "resp1", GraderConfigHash: "ch",
		RubricHash: "rh",
	})
	if err != nil {
		t.Fatalf("seed InsertGrade: %v", err)
	}
	var wg sync.WaitGroup
	out := make([]*Grade, 2)
	errs := make([]error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			out[i], errs[i] = db.InsertGrade(&Grade{
				CacheKey: "grade-race", ResponseID: "resp-racer",
				GraderConfigHash: "ch", RubricHash: "rh",
			})
		}(i)
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("racer %d: %v", i, err)
		}
	}
	if out[0].ID != seed.ID || out[1].ID != seed.ID {
		t.Fatalf("duplicate key gave %s and %s; want %s", out[0].ID, out[1].ID, seed.ID)
	}
	if out[0].ResponseID != "resp1" || out[1].ResponseID != "resp1" {
		t.Fatalf("reuse diverged: %+v %+v", out[0], out[1])
	}
}
