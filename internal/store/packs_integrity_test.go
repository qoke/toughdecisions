package store

import (
	"testing"
)

// Defect 2: Active() must be deterministic — newest (created_at, id) wins
// when more than one active pack exists.
func TestActiveNewestWinsWhenMultipleActive(t *testing.T) {
	db := openTestDB(t)
	older, err := db.InsertPack("active", `{"v":1}`, "h", "")
	if err != nil {
		t.Fatalf("InsertPack older: %v", err)
	}
	newer, err := db.InsertPack("active", `{"v":2}`, "h", "")
	if err != nil {
		t.Fatalf("InsertPack newer: %v", err)
	}
	got, err := db.Active()
	if err != nil {
		t.Fatalf("Active: %v", err)
	}
	// created_at has 1s resolution, so force a deterministic order.
	if got.ID != older.ID && got.ID != newer.ID {
		t.Fatalf("Active = %s; want one of %s, %s", got.ID, older.ID, newer.ID)
	}
	if _, err := db.db.Exec(`UPDATE packs SET created_at='2000-01-01T00:00:00Z' WHERE id=?`, older.ID); err != nil {
		t.Fatalf("backdate older: %v", err)
	}
	got, err = db.Active()
	if err != nil {
		t.Fatalf("Active: %v", err)
	}
	if got.ID != newer.ID {
		t.Fatalf("Active = %s; want newest %s", got.ID, newer.ID)
	}
}

// Defect 2: a full publish must restore the single-active invariant even
// when a legacy store holds two active packs (all actives demoted).
func TestPublishAtomicDemotesAllActivePacks(t *testing.T) {
	db := openTestDB(t)
	fam := seedFamily(t, db, "FACT")
	c1 := seedCase(t, db, fam, "FACT-c1")
	a1, err := db.InsertPack("active", `{"v":1}`, "h", "")
	if err != nil {
		t.Fatalf("InsertPack a1: %v", err)
	}
	a2, err := db.InsertPack("active", `{"v":2}`, "h", "")
	if err != nil {
		t.Fatalf("InsertPack a2: %v", err)
	}
	res, err := db.PublishAtomic(PackTxSeed{
		NewStatus: "active", NewSeats: `{"v":3}`, NewHash: "h3",
		Baselines: []BaselineSeed{{PackID: "x", Seat: "s", CaseID: c1.ID, ResponseID: "r"}},
	})
	if err != nil {
		_ = a1
		_ = a2
		t.Fatalf("PublishAtomic: %v", err)
	}
	var n int
	if err := db.db.QueryRow(`SELECT COUNT(*) FROM packs WHERE status='active'`).Scan(&n); err != nil {
		t.Fatalf("count active: %v", err)
	}
	if n != 1 {
		t.Fatalf("active packs = %d; want exactly 1", n)
	}
	cur, err := db.Active()
	if err != nil {
		t.Fatalf("Active: %v", err)
	}
	if cur.ID != res.CreatedID {
		t.Fatalf("Active = %s; want new %s", cur.ID, res.CreatedID)
	}
	for _, id := range []string{a1.ID, a2.ID} {
		p, err := db.GetPack(id)
		if err != nil {
			t.Fatalf("GetPack %s: %v", id, err)
		}
		if p.Status == "active" {
			t.Fatalf("legacy pack %s still active", id)
		}
	}
}
