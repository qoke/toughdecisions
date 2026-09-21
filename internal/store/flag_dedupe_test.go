package store

import (
	"testing"
)

func TestFlagInsertDeduplicatesOnIdentity(t *testing.T) {
	db := openTestDB(t)

	// Arrange: one flag with a fixed identity.
	first, err := db.InsertFlag(&Flag{
		GradeID: "grade-d1", ResponseID: "resp-d1", Type: "fabrication",
		Passage: "p1", Violated: "v1",
	})
	if err != nil {
		t.Fatalf("InsertFlag: %v", err)
	}

	// Act: a concurrent loser re-inserts the same identity.
	second, err := db.InsertFlag(&Flag{
		GradeID: "grade-d1", ResponseID: "resp-d1", Type: "fabrication",
		Passage: "p1", Violated: "v1",
	})
	// Assert: no duplicate row; the canonical row is returned.
	if err != nil {
		t.Fatalf("duplicate InsertFlag: %v", err)
	}
	if second.ID != first.ID {
		t.Fatalf("dedupe returned %s, want %s", second.ID, first.ID)
	}
	flags, err := db.ListFlagsByGradeID("grade-d1")
	if err != nil {
		t.Fatalf("ListFlagsByGradeID: %v", err)
	}
	if len(flags) != 1 {
		t.Fatalf("flags = %d; want exactly 1", len(flags))
	}
}
