package store

import (
	"database/sql"
	"errors"
	"testing"
)

func TestBundleUpsertGet(t *testing.T) {
	db := openTestDB(t)
	fam := seedFamily(t, db, "F020")
	c := seedCase(t, db, fam, "F020-base")

	b, err := db.UpsertBundle(&Bundle{
		BundleKey: "F020-B1", CaseID: c.ID, Kind: "authored",
		ViewsJSON: `{"judge":"v"}`, ManipulationsJSON: `["m1"]`,
		BundleHash: "bh1",
	})
	if err != nil {
		t.Fatalf("UpsertBundle: %v", err)
	}
	gotByID, err := db.GetBundle(b.ID)
	if err != nil {
		t.Fatalf("GetBundle: %v", err)
	}
	gotByKey, err := db.GetBundleByKey("F020-B1")
	if err != nil {
		t.Fatalf("GetBundleByKey: %v", err)
	}
	for _, tc := range []struct {
		name string
		got  *Bundle
	}{
		{"by id", gotByID},
		{"by key", gotByKey},
	} {
		if tc.got.BundleKey != "F020-B1" || tc.got.CaseID != c.ID || tc.got.Kind != "authored" ||
			tc.got.ViewsJSON != `{"judge":"v"}` || tc.got.BundleHash != "bh1" {
			t.Fatalf("%s mismatch: %+v", tc.name, tc.got)
		}
	}
	if _, err := db.UpsertBundle(&Bundle{BundleKey: "F020-B1", CaseID: c.ID, BundleHash: "bh2"}); err != nil {
		t.Fatalf("UpsertBundle again: %v", err)
	}
	again, _ := db.GetBundleByKey("F020-B1")
	if again.BundleHash != "bh2" {
		t.Fatalf("upsert did not update: %+v", again)
	}
	if list, err := db.ListBundlesByCase(c.ID); err != nil || len(list) != 1 {
		t.Fatalf("ListBundlesByCase = %v, %v", list, err)
	}
	if _, err := db.GetBundle("missing"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("GetBundle missing err = %v", err)
	}
	if _, err := db.UpsertBundle(&Bundle{}); err == nil {
		t.Fatal("UpsertBundle empty key: want error")
	}
}

func TestGraderUpsertGetCalibrate(t *testing.T) {
	db := openTestDB(t)
	g, err := db.UpsertGraderConfig(&GraderConfig{
		GraderKey: "gA", Model: "m1", Family: "f1", ParamsJSON: `{}`,
		RubricHash: "rh", ConfigHash: "ch-gA", Role: "selection",
	})
	if err != nil {
		t.Fatalf("UpsertGraderConfig: %v", err)
	}
	if g.Admitted {
		t.Fatal("new grader admitted, want false")
	}
	byHash, err := db.GetGraderConfig("ch-gA")
	if err != nil {
		t.Fatalf("GetGraderConfig: %v", err)
	}
	if byHash.GraderKey != "gA" || byHash.Model != "m1" || byHash.Role != "selection" {
		t.Fatalf("round-trip mismatch: %+v", byHash)
	}
	if _, err := db.UpsertGraderConfig(&GraderConfig{GraderKey: "gA", ConfigHash: "ch-gA", Model: "m2"}); err != nil {
		t.Fatalf("UpsertGraderConfig again: %v", err)
	}
	again, _ := db.GetGraderConfig("ch-gA")
	if again.Model != "m2" {
		t.Fatalf("upsert did not update: %+v", again)
	}
	list, err := db.ListGraderConfigs()
	if err != nil || len(list) != 1 {
		t.Fatalf("ListGraderConfigs = %v, %v", list, err)
	}
	at := "2026-01-01T00:00:00Z"
	if err := db.SetGraderCalibration("ch-gA", `{"items":[]}`, true, &at); err != nil {
		t.Fatalf("SetGraderCalibration: %v", err)
	}
	cal, _ := db.GetGraderConfig("ch-gA")
	if !cal.Admitted || cal.CalibrationJSON == nil || cal.AdmittedAt == nil || *cal.AdmittedAt != at {
		t.Fatalf("calibration not stored: %+v", cal)
	}
	if err := db.SetGraderCalibration("missing", `{}`, true, &at); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("SetGraderCalibration missing err = %v, want ErrNoRows", err)
	}
	if _, err := db.GetGraderConfig("missing"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("GetGraderConfig missing err = %v", err)
	}
	if _, err := db.UpsertGraderConfig(&GraderConfig{}); err == nil {
		t.Fatal("UpsertGraderConfig empty hash: want error")
	}
}

func TestCalibrationItemUpsertGet(t *testing.T) {
	db := openTestDB(t)
	fam := seedFamily(t, db, "F021")
	c := seedCase(t, db, fam, "F021-base")
	it, err := db.UpsertCalibrationItem(&CalibrationItem{
		ItemKey: "cal-1", CaseID: c.ID, Seat: "judge", ResponseText: "resp",
		Category: "grounded_support", HumanScoresJSON: `{"a":1}`,
		HumanFlagsJSON: `[]`, Notes: "n",
	})
	if err != nil {
		t.Fatalf("UpsertCalibrationItem: %v", err)
	}
	byID, err := db.GetCalibrationItem(it.ID)
	if err != nil {
		t.Fatalf("GetCalibrationItem: %v", err)
	}
	byKey, err := db.GetCalibrationItemByKey("cal-1")
	if err != nil {
		t.Fatalf("GetCalibrationItemByKey: %v", err)
	}
	for _, got := range []*CalibrationItem{byID, byKey} {
		if got.ItemKey != "cal-1" || got.CaseID != c.ID || got.Seat != "judge" ||
			got.ResponseText != "resp" || got.Category != "grounded_support" {
			t.Fatalf("round-trip mismatch: %+v", got)
		}
	}
	if _, err := db.UpsertCalibrationItem(&CalibrationItem{ItemKey: "cal-1", CaseID: c.ID, Notes: "n2"}); err != nil {
		t.Fatalf("UpsertCalibrationItem again: %v", err)
	}
	again, _ := db.GetCalibrationItemByKey("cal-1")
	if again.Notes != "n2" {
		t.Fatalf("upsert did not update: %+v", again)
	}
	if list, err := db.ListCalibrationItems(); err != nil || len(list) != 1 {
		t.Fatalf("ListCalibrationItems = %v, %v", list, err)
	}
	if _, err := db.GetCalibrationItem("missing"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("GetCalibrationItem missing err = %v", err)
	}
	if _, err := db.UpsertCalibrationItem(&CalibrationItem{}); err == nil {
		t.Fatal("UpsertCalibrationItem empty key: want error")
	}
}
