package store

import (
	"testing"
	"time"
)

func backdate(t *testing.T, db *DB, table, id string, at string) {
	t.Helper()
	if _, err := db.db.Exec(`UPDATE `+table+` SET created_at=? WHERE id=?`, at, id); err != nil {
		t.Fatalf("backdate %s %s: %v", table, id, err)
	}
}

func insertProductionRequest(t *testing.T, db *DB, cacheSuffix string) *Request {
	t.Helper()
	th, err := db.CreateThread("t")
	if err != nil {
		t.Fatalf("InsertThread: %v", err)
	}
	card, err := db.InsertCard(th.ID, `{"decision":"d"}`)
	if err != nil {
		t.Fatalf("InsertCard: %v", err)
	}
	pk, err := db.InsertPack("active", `{"a":1}`, "pp", "")
	if err != nil {
		t.Fatalf("InsertPack: %v", err)
	}
	req, err := db.InsertRequest(&Request{
		ThreadID: th.ID, CardID: card.ID, PackID: pk.ID,
		SnapshotJSON: `{}`, InputHash: "ih", State: "complete",
		ViewsDeadlineAt: nowUTC(),
	})
	if err != nil {
		t.Fatalf("InsertRequest: %v", err)
	}
	if _, err := db.InsertRequestView(req.ID, "possibility", "complete"); err != nil {
		t.Fatalf("InsertRequestView: %v", err)
	}
	if _, err := db.db.Exec(`INSERT INTO request_judge (id, created_at, request_id, state) VALUES (?, ?, ?, ?)`,
		"j-"+req.ID, nowUTC(), req.ID, "complete"); err != nil {
		t.Fatalf("insert judge: %v", err)
	}
	if _, err := db.InsertResponse(&Response{
		CacheKey: "prune-" + cacheSuffix, Seat: "possibility",
		ConfigHash: "ch", PromptPackHash: "pp", InputHash: "ih",
		Origin: "production", ModelRequested: "m", RawText: "r",
	}); err != nil {
		t.Fatalf("InsertResponse: %v", err)
	}
	if _, err := db.db.Exec(`INSERT INTO rewrites (id, created_at, request_id, source_ref, instruction, response_id, output_text) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		"rw-"+req.ID, nowUTC(), req.ID, "judge", "shorter", "resp", "out"); err != nil {
		t.Fatalf("insert rewrite: %v", err)
	}
	if _, err := db.db.Exec(`INSERT INTO sent_messages (id, created_at, request_id, thread_id, text, sent_at) VALUES (?, ?, ?, ?, ?, ?)`,
		"sm-"+req.ID, nowUTC(), req.ID, th.ID, "sent", nowUTC()); err != nil {
		t.Fatalf("insert sent: %v", err)
	}
	return req
}

func TestPruneProductionDeletesOldKeepsFeedback(t *testing.T) {
	db := openTestDB(t)
	old := insertProductionRequest(t, db, "old")
	newer := insertProductionRequest(t, db, "new")
	if _, err := db.InsertFeedback(newer.ID, "too_slow", "n"); err != nil {
		t.Fatalf("InsertFeedback: %v", err)
	}
	ancient := time.Now().AddDate(0, 0, -100).UTC().Format(time.RFC3339)
	backdate(t, db, "requests", old.ID, ancient)
	backdate(t, db, "requests", newer.ID, ancient)
	// also backdate children of old so only the feedback guard matters
	for _, tbl := range []string{"request_views", "request_judge", "rewrites", "sent_messages", "responses", "feedback"} {
		if _, err := db.db.Exec(`UPDATE `+tbl+` SET created_at=? WHERE created_at < '2999-01-01'`, ancient); err != nil {
			t.Fatalf("backdate %s: %v", tbl, err)
		}
	}

	res, err := db.PruneProduction(time.Now().AddDate(0, 0, -90))
	if err != nil {
		t.Fatalf("PruneProduction: %v", err)
	}
	if res.Requests != 1 {
		t.Fatalf("requests pruned = %d, want 1", res.Requests)
	}
	// feedback-linked request survives with its tag count intact.
	counts, err := db.FeedbackTagCounts("2000-01-01T00:00:00Z")
	if err != nil {
		t.Fatalf("FeedbackTagCounts: %v", err)
	}
	if counts["too_slow"] != 1 {
		t.Fatalf("tag counts = %v, want too_slow=1", counts)
	}
	if _, err := db.GetRequestRow(newer.ID); err != nil {
		t.Fatalf("feedback request pruned: %v", err)
	}
	// harness rows are never pruned.
	if _, err := db.InsertResponse(&Response{
		CacheKey: "prune-harness", Seat: "judge",
		ConfigHash: "ch", PromptPackHash: "pp", InputHash: "ih",
		Origin: "harness", ModelRequested: "m", RawText: "r",
	}); err != nil {
		t.Fatalf("InsertResponse harness: %v", err)
	}
	if _, err := db.db.Exec(`UPDATE responses SET created_at=? WHERE cache_key='prune-harness'`, ancient); err != nil {
		t.Fatalf("backdate harness: %v", err)
	}
	res2, err := db.PruneProduction(time.Now().AddDate(0, 0, -90))
	if err != nil {
		t.Fatalf("PruneProduction 2: %v", err)
	}
	if res2.Responses != 0 {
		t.Fatalf("harness responses pruned = %d, want 0", res2.Responses)
	}
}
