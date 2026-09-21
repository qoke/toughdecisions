package store

import (
	"database/sql"
	"errors"
	"testing"
)

func TestHarnessMigrationApplies(t *testing.T) {
	db := openTestDB(t)
	applied, err := db.AppliedMigrations()
	if err != nil {
		t.Fatalf("AppliedMigrations: %v", err)
	}
	if len(applied) != 3 || applied[0] != "0001_init" || applied[1] != "0002_harness" || applied[2] != "0003_flag_dedupe" {
		t.Fatalf("applied = %v, want [0001_init 0002_harness 0003_flag_dedupe]", applied)
	}
	for _, table := range []string{
		"families", "cases", "bundles", "grader_configs", "calibration_items",
		"grades", "flags", "pairwise", "issue_coverage", "harness_runs",
		"sentinel_results", "baselines", "candidates",
	} {
		var n int
		if err := db.db.QueryRow(
			`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?`, table,
		).Scan(&n); err != nil {
			t.Fatalf("table check %s: %v", table, err)
		}
		if n != 1 {
			t.Fatalf("table %s missing", table)
		}
	}
}

func seedFamily(t *testing.T, db *DB, key string) *Family {
	t.Helper()
	f, err := db.UpsertFamily(&Family{
		FamilyKey: key, Name: "n-" + key, Split: "development",
		TagsJSON: `[]`, AcceptanceJSON: `{}`, PlantedIssuesJSON: `[]`,
		FamilyHash: "fh-" + key, SeatsRelevantJSON: `["judge"]`,
	})
	if err != nil {
		t.Fatalf("UpsertFamily: %v", err)
	}
	return f
}

func seedCase(t *testing.T, db *DB, fam *Family, key string) *Case {
	t.Helper()
	c, err := db.UpsertCase(&Case{
		CaseKey: key, FamilyID: fam.ID, Variant: "base",
		InputJSON: `{}`, InputHash: "ih-" + key,
		ExpectedChange: strptr("change"),
	})
	if err != nil {
		t.Fatalf("UpsertCase: %v", err)
	}
	return c
}

func TestFamilyUpsertGet(t *testing.T) {
	db := openTestDB(t)
	f := seedFamily(t, db, "F001")

	byID, err := db.GetFamily(f.ID)
	if err != nil {
		t.Fatalf("GetFamily: %v", err)
	}
	byKey, err := db.GetFamilyByKey("F001")
	if err != nil {
		t.Fatalf("GetFamilyByKey: %v", err)
	}
	for _, got := range []*Family{byID, byKey} {
		if got.FamilyKey != "F001" || got.Name != "n-F001" || got.Split != "development" ||
			got.FamilyHash != "fh-F001" || got.SeatsRelevantJSON != `["judge"]` {
			t.Fatalf("round-trip mismatch: %+v", got)
		}
	}

	// Upsert with the same key updates the row instead of duplicating it.
	if _, err := db.UpsertFamily(&Family{FamilyKey: "F001", Name: "renamed", Split: "selection"}); err != nil {
		t.Fatalf("UpsertFamily again: %v", err)
	}
	updated, err := db.GetFamilyByKey("F001")
	if err != nil {
		t.Fatalf("GetFamilyByKey after upsert: %v", err)
	}
	if updated.Name != "renamed" || updated.Split != "selection" {
		t.Fatalf("upsert did not update: %+v", updated)
	}
	list, err := db.ListFamilies()
	if err != nil || len(list) != 1 {
		t.Fatalf("ListFamilies = %v, %v", list, err)
	}

	if _, err := db.GetFamily("missing"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("GetFamily missing err = %v, want ErrNoRows", err)
	}
	if _, err := db.GetFamilyByKey("missing"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("GetFamilyByKey missing err = %v, want ErrNoRows", err)
	}
	if _, err := db.UpsertFamily(&Family{}); err == nil {
		t.Fatal("UpsertFamily empty key: want error")
	}
}

func TestCaseUpsertGet(t *testing.T) {
	db := openTestDB(t)
	fam := seedFamily(t, db, "F010")
	c := seedCase(t, db, fam, "F010-base")

	byID, err := db.GetCase(c.ID)
	if err != nil {
		t.Fatalf("GetCase: %v", err)
	}
	byKey, err := db.GetCaseByKey("F010-base")
	if err != nil {
		t.Fatalf("GetCaseByKey: %v", err)
	}
	for _, got := range []*Case{byID, byKey} {
		if got.CaseKey != "F010-base" || got.FamilyID != fam.ID || got.Variant != "base" ||
			got.InputHash != "ih-F010-base" || got.ExpectedChange == nil || *got.ExpectedChange != "change" {
			t.Fatalf("round-trip mismatch: %+v", got)
		}
	}

	if _, err := db.UpsertCase(&Case{CaseKey: "F010-base", FamilyID: fam.ID, InputHash: "ih2"}); err != nil {
		t.Fatalf("UpsertCase again: %v", err)
	}
	again, _ := db.GetCaseByKey("F010-base")
	if again.InputHash != "ih2" {
		t.Fatalf("upsert did not update: %+v", again)
	}
	list, err := db.ListCasesByFamily(fam.ID)
	if err != nil || len(list) != 1 {
		t.Fatalf("ListCasesByFamily = %v, %v", list, err)
	}
	if empty, err := db.ListCasesByFamily("missing"); err != nil || len(empty) != 0 {
		t.Fatalf("ListCasesByFamily missing = %v, %v", empty, err)
	}

	if _, err := db.GetCase("missing"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("GetCase missing err = %v, want ErrNoRows", err)
	}
	if _, err := db.GetCaseByKey("missing"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("GetCaseByKey missing err = %v, want ErrNoRows", err)
	}
	if _, err := db.UpsertCase(&Case{}); err == nil {
		t.Fatal("UpsertCase empty key: want error")
	}
}
