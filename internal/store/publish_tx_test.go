package store

import (
	"testing"

	"github.com/qoke/toughdecisions/internal/ids"
)

func TestPublishAtomic_HappyPathAndPins(t *testing.T) {
	// Arrange: set up database with pre-existing previous and active packs
	db := openTestDB(t)
	fam := seedFamily(t, db, "F100")
	c1 := seedCase(t, db, fam, "F100-c1")

	prevPack, err := db.InsertPack("previous", `{"seat":"prev"}`, "hash-prev", "prev notes")
	if err != nil {
		t.Fatalf("InsertPack prev: %v", err)
	}
	activePack, err := db.InsertPack("active", `{"seat":"old"}`, "hash-old", "old notes")
	if err != nil {
		t.Fatalf("InsertPack active: %v", err)
	}

	pinnedPackID := ids.NewID()
	pinnedBundleID := ids.NewID()
	runID := "run-publish-001"

	seed := PackTxSeed{
		NewID:     pinnedPackID,
		NewStatus: "active",
		NewSeats:  `{"seat":"new"}`,
		NewHash:   "hash-new",
		NewNotes:  "new notes",
		NewRunID:  &runID,
		Bundles: []BundleSeed{
			{
				ID:                pinnedBundleID,
				BundleKey:         "bundle-key-1",
				CaseID:            c1.ID,
				ViewsJSON:         `{"v":1}`,
				ManipulationsJSON: `["m1"]`,
				BundleHash:        "bh-1",
				SourcePackID:      &pinnedPackID,
			},
		},
		Baselines: []BaselineSeed{
			{
				Key:        "k1",
				PackID:     pinnedPackID,
				Seat:       "judge",
				CaseID:     c1.ID,
				BundleID:   &pinnedBundleID,
				ResponseID: "resp-1",
			},
		},
	}

	// Act: execute atomic publish
	res, err := db.PublishAtomic(seed)
	if err != nil {
		t.Fatalf("PublishAtomic: %v", err)
	}

	// Assert: returned result contains created and active IDs
	if res.CreatedID != pinnedPackID {
		t.Errorf("res.CreatedID = %q, want %q", res.CreatedID, pinnedPackID)
	}
	if res.ActiveID != pinnedPackID {
		t.Errorf("res.ActiveID = %q, want %q", res.ActiveID, pinnedPackID)
	}

	// Assert: old previous pack is retired
	p0, err := db.GetPack(prevPack.ID)
	if err != nil {
		t.Fatalf("GetPack p0: %v", err)
	}
	if p0.Status != "retired" {
		t.Errorf("p0.Status = %q, want retired", p0.Status)
	}

	// Assert: old active pack is demoted to previous
	pOld, err := db.GetPack(activePack.ID)
	if err != nil {
		t.Fatalf("GetPack pOld: %v", err)
	}
	if pOld.Status != "previous" {
		t.Errorf("pOld.Status = %q, want previous", pOld.Status)
	}

	// Assert: new pack is active with published_from_run_id and activated_at populated
	pNew, err := db.GetPack(pinnedPackID)
	if err != nil {
		t.Fatalf("GetPack pNew: %v", err)
	}
	if pNew.Status != "active" {
		t.Errorf("pNew.Status = %q, want active", pNew.Status)
	}
	if pNew.PublishedFromRunID == nil || *pNew.PublishedFromRunID != runID {
		t.Errorf("pNew.PublishedFromRunID = %v, want %q", pNew.PublishedFromRunID, runID)
	}
	if pNew.ActivatedAt == nil || *pNew.ActivatedAt == "" {
		t.Errorf("pNew.ActivatedAt is empty, want non-empty timestamp")
	}

	// Assert: bundle created with pinned ID and correct fields
	bundle, err := db.GetBundle(pinnedBundleID)
	if err != nil {
		t.Fatalf("GetBundle: %v", err)
	}
	if bundle.BundleKey != "bundle-key-1" || bundle.CaseID != c1.ID || bundle.BundleHash != "bh-1" {
		t.Errorf("bundle mismatch: %+v", bundle)
	}
	if bundle.SourcePackID == nil || *bundle.SourcePackID != pinnedPackID {
		t.Errorf("bundle.SourcePackID = %v, want %q", bundle.SourcePackID, pinnedPackID)
	}

	// Assert: baseline created with response and pinned bundle ID
	bl, err := db.GetBaseline(pinnedPackID, "judge", c1.ID)
	if err != nil {
		t.Fatalf("GetBaseline: %v", err)
	}
	if bl.ResponseID != "resp-1" || bl.BundleID == nil || *bl.BundleID != pinnedBundleID {
		t.Errorf("baseline mismatch: %+v", bl)
	}
}

func TestPublishAtomic_UpsertExistingAndDefaults(t *testing.T) {
	// Arrange: set up database with initial bundle and baseline
	db := openTestDB(t)
	fam := seedFamily(t, db, "F200")
	c1 := seedCase(t, db, fam, "F200-c1")
	c2 := seedCase(t, db, fam, "F200-c2")

	// Run first publish without pinned IDs to exercise default ID generation
	firstRes, err := db.PublishAtomic(PackTxSeed{
		NewStatus: "active",
		NewSeats:  `{"seat":"s1"}`,
		NewHash:   "h1",
		Bundles: []BundleSeed{
			{
				BundleKey:  "bundle-upsert-key",
				CaseID:     c1.ID,
				BundleHash: "hash-initial",
			},
		},
		Baselines: []BaselineSeed{
			{
				PackID:     "pack-upsert-1",
				Seat:       "editor",
				CaseID:     c1.ID,
				ResponseID: "resp-init",
			},
		},
	})
	if err != nil {
		t.Fatalf("initial PublishAtomic: %v", err)
	}
	if firstRes.CreatedID == "" {
		t.Fatal("expected generated pack ID")
	}

	initialBundle, err := db.GetBundleByKey("bundle-upsert-key")
	if err != nil {
		t.Fatalf("GetBundleByKey: %v", err)
	}
	if initialBundle.ViewsJSON != "{}" || initialBundle.ManipulationsJSON != "[]" {
		t.Errorf("bundle defaults mismatch: views=%q manips=%q", initialBundle.ViewsJSON, initialBundle.ManipulationsJSON)
	}

	// Act: upsert bundle and baseline in the same transaction with updated attributes
	newSourcePack := firstRes.CreatedID
	_, err = db.PublishAtomic(PackTxSeed{
		Bundles: []BundleSeed{
			{
				BundleKey:         "bundle-upsert-key",
				CaseID:            c2.ID,
				ViewsJSON:         `{"updated":true}`,
				ManipulationsJSON: `["m2"]`,
				BundleHash:        "hash-updated",
				SourcePackID:      &newSourcePack,
			},
		},
		Baselines: []BaselineSeed{
			{
				PackID:     "pack-upsert-1",
				Seat:       "editor",
				CaseID:     c1.ID,
				BundleID:   &initialBundle.ID,
				ResponseID: "resp-updated",
			},
		},
	})
	if err != nil {
		t.Fatalf("update PublishAtomic: %v", err)
	}

	// Assert: bundle row was updated in place
	updatedBundle, err := db.GetBundleByKey("bundle-upsert-key")
	if err != nil {
		t.Fatalf("GetBundleByKey updated: %v", err)
	}
	if updatedBundle.ID != initialBundle.ID {
		t.Errorf("bundle ID changed: got %q, want %q", updatedBundle.ID, initialBundle.ID)
	}
	if updatedBundle.CaseID != c2.ID || updatedBundle.BundleHash != "hash-updated" ||
		updatedBundle.ViewsJSON != `{"updated":true}` || updatedBundle.ManipulationsJSON != `["m2"]` {
		t.Errorf("updated bundle fields mismatch: %+v", updatedBundle)
	}
	if updatedBundle.SourcePackID == nil || *updatedBundle.SourcePackID != newSourcePack {
		t.Errorf("bundle SourcePackID = %v, want %q", updatedBundle.SourcePackID, newSourcePack)
	}

	// Assert: baseline row was updated with new response and bundle ID
	updatedBL, err := db.GetBaseline("pack-upsert-1", "editor", c1.ID)
	if err != nil {
		t.Fatalf("GetBaseline: %v", err)
	}
	if updatedBL.ResponseID != "resp-updated" || updatedBL.BundleID == nil || *updatedBL.BundleID != initialBundle.ID {
		t.Errorf("updated baseline mismatch: %+v", updatedBL)
	}
}

func TestPublishAtomic_BaselinesOnlyMode(t *testing.T) {
	// Arrange: existing active pack
	db := openTestDB(t)
	fam := seedFamily(t, db, "F300")
	c1 := seedCase(t, db, fam, "F300-c1")

	active, err := db.InsertPack("active", `{"seats":1}`, "hash-act", "active notes")
	if err != nil {
		t.Fatalf("InsertPack: %v", err)
	}

	// Act: PublishAtomic with empty NewStatus (baselines-only)
	res, err := db.PublishAtomic(PackTxSeed{
		NewStatus: "", // baselines-only mode
		Bundles: []BundleSeed{
			{
				BundleKey: "bundle-bo-1",
				CaseID:    c1.ID,
			},
		},
		Baselines: []BaselineSeed{
			{
				PackID:     active.ID,
				Seat:       "judge",
				CaseID:     c1.ID,
				ResponseID: "resp-bo-1",
			},
		},
	})
	if err != nil {
		t.Fatalf("PublishAtomic baselines-only: %v", err)
	}

	// Assert: active pack is untouched and returned as ActiveID, CreatedID is empty
	if res.ActiveID != active.ID {
		t.Errorf("res.ActiveID = %q, want %q", res.ActiveID, active.ID)
	}
	if res.CreatedID != "" {
		t.Errorf("res.CreatedID = %q, want empty", res.CreatedID)
	}

	curActive, err := db.Active()
	if err != nil {
		t.Fatalf("Active: %v", err)
	}
	if curActive.ID != active.ID || curActive.Status != "active" {
		t.Errorf("curActive mismatch: %+v", curActive)
	}
}

func TestPublishAtomic_RollbackOnFailure(t *testing.T) {
	// Arrange: set up initial active pack and baseline state
	db := openTestDB(t)
	fam := seedFamily(t, db, "F400")
	c1 := seedCase(t, db, fam, "F400-c1")

	activePack, err := db.InsertPack("active", `{"s":"initial"}`, "h-init", "initial active")
	if err != nil {
		t.Fatalf("InsertPack active: %v", err)
	}

	// Deliberately cause failure in the middle of the transaction:
	// Bundle with invalid foreign key reference (nonexistent case_id with PRAGMA foreign_keys=ON)
	candidatePackID := ids.NewID()
	runID := "run-fail"
	failingSeed := PackTxSeed{
		NewID:     candidatePackID,
		NewStatus: "active",
		NewSeats:  `{"s":"failed"}`,
		NewHash:   "h-failed",
		NewRunID:  &runID,
		Bundles: []BundleSeed{
			{
				BundleKey: "bundle-valid-in-seed",
				CaseID:    c1.ID,
			},
			{
				BundleKey: "bundle-invalid-fk",
				CaseID:    "nonexistent-case-id-fk-violation",
			},
		},
		Baselines: []BaselineSeed{
			{
				PackID:     candidatePackID,
				Seat:       "judge",
				CaseID:     c1.ID,
				ResponseID: "resp-should-not-exist",
			},
		},
	}

	// Act: execute transaction that must fail and rollback
	res, err := db.PublishAtomic(failingSeed)
	if err == nil {
		t.Fatalf("expected PublishAtomic to fail, got res: %+v", res)
	}

	// Assert: NOTHING was committed
	// 1. Previously active pack is STILL active
	curActive, err := db.Active()
	if err != nil {
		t.Fatalf("Active: %v", err)
	}
	if curActive.ID != activePack.ID {
		t.Errorf("active pack ID = %q, want %q", curActive.ID, activePack.ID)
	}

	// 2. Candidate pack does NOT exist
	var count int
	if err := db.db.QueryRow(`SELECT COUNT(*) FROM packs WHERE id=?`, candidatePackID).Scan(&count); err != nil {
		t.Fatalf("query packs count: %v", err)
	}
	if count != 0 {
		t.Errorf("candidate pack persisted: count = %d", count)
	}

	// 3. Valid bundle in failing seed was NOT persisted
	if err := db.db.QueryRow(`SELECT COUNT(*) FROM bundles WHERE bundle_key='bundle-valid-in-seed'`).Scan(&count); err != nil {
		t.Fatalf("query bundles count: %v", err)
	}
	if count != 0 {
		t.Errorf("bundle persisted despite rollback: count = %d", count)
	}

	// 4. Baseline was NOT persisted
	if err := db.db.QueryRow(`SELECT COUNT(*) FROM baselines WHERE pack_id=?`, candidatePackID).Scan(&count); err != nil {
		t.Fatalf("query baselines count: %v", err)
	}
	if count != 0 {
		t.Errorf("baseline persisted despite rollback: count = %d", count)
	}
}

func TestPublishAtomic_ValidationErrors(t *testing.T) {
	db := openTestDB(t)

	// Sub-test 1: empty bundle_key
	_, err := db.PublishAtomic(PackTxSeed{
		Bundles: []BundleSeed{
			{BundleKey: "", CaseID: "c1"},
		},
	})
	if err == nil {
		t.Error("expected error for empty bundle_key")
	}

	// Sub-test 2: empty case_id for new bundle
	_, err = db.PublishAtomic(PackTxSeed{
		Bundles: []BundleSeed{
			{BundleKey: "b-valid", CaseID: ""},
		},
	})
	if err == nil {
		t.Error("expected error for empty case_id on new bundle")
	}

	// Sub-test 3: empty pack_id on baseline
	_, err = db.PublishAtomic(PackTxSeed{
		Baselines: []BaselineSeed{
			{PackID: "", Seat: "judge", CaseID: "c1", ResponseID: "r1"},
		},
	})
	if err == nil {
		t.Error("expected error for empty pack_id on baseline")
	}

	// Sub-test 4: empty case_id on baseline
	_, err = db.PublishAtomic(PackTxSeed{
		Baselines: []BaselineSeed{
			{PackID: "p1", Seat: "judge", CaseID: "", ResponseID: "r1"},
		},
	})
	if err == nil {
		t.Error("expected error for empty case_id on baseline")
	}

	// Sub-test 5: empty response_id on baseline
	_, err = db.PublishAtomic(PackTxSeed{
		Baselines: []BaselineSeed{
			{PackID: "p1", Seat: "judge", CaseID: "c1", ResponseID: ""},
		},
	})
	if err == nil {
		t.Error("expected error for empty response_id on baseline")
	}
}
