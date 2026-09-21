package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
)

func openTestDB(t *testing.T) *DB {
	t.Helper()
	db, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func strptr(s string) *string { return &s }

func TestOpenCreatesFileAndAppliesMigrationOnce(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "c.db")
	db, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	applied, err := db.AppliedMigrations()
	if err != nil {
		t.Fatalf("AppliedMigrations: %v", err)
	}
	if len(applied) != 2 || applied[0] != "0001_init" || applied[1] != "0002_harness" {
		t.Fatalf("applied = %v, want [0001_init 0002_harness]", applied)
	}
	var mode string
	if err := db.db.QueryRow(`PRAGMA journal_mode`).Scan(&mode); err != nil {
		t.Fatalf("journal_mode: %v", err)
	}
	if !strings.EqualFold(mode, "wal") {
		t.Fatalf("journal_mode = %q, want wal", mode)
	}
	_ = db.Close()

	db2, err := Open(path)
	if err != nil {
		t.Fatalf("re-Open: %v", err)
	}
	defer db2.Close()
	applied2, err := db2.AppliedMigrations()
	if err != nil {
		t.Fatalf("AppliedMigrations: %v", err)
	}
	if len(applied2) != 2 {
		t.Fatalf("second open applied = %v, want exactly 2", applied2)
	}
}

func TestWithTxRollbackOnError(t *testing.T) {
	db := openTestDB(t)
	err := db.WithTx(context.Background(), func(tx *sql.Tx) error {
		if _, err := tx.Exec(`INSERT INTO threads (id, created_at, name, archived) VALUES ('x','t','n',0)`); err != nil {
			return err
		}
		return sql.ErrConnDone
	})
	if err == nil {
		t.Fatal("WithTx returned nil, want error")
	}
	var n int
	if err := db.db.QueryRow(`SELECT COUNT(*) FROM threads WHERE id='x'`).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 0 {
		t.Fatal("row persisted despite rollback")
	}
	if err := db.WithTx(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec(`INSERT INTO threads (id, created_at, name, archived) VALUES ('y','t','n',0)`)
		return err
	}); err != nil {
		t.Fatalf("commit tx: %v", err)
	}
	if err := db.db.QueryRow(`SELECT COUNT(*) FROM threads WHERE id='y'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("committed row missing: n=%d err=%v", n, err)
	}
}

func TestPackInsertSelectAndStatus(t *testing.T) {
	db := openTestDB(t)
	p, err := db.InsertPack("draft", `{"a":1}`, "ph", "notes")
	if err != nil {
		t.Fatalf("InsertPack: %v", err)
	}
	got, err := db.GetPack(p.ID)
	if err != nil {
		t.Fatalf("GetPack: %v", err)
	}
	if got.SeatsJSON != `{"a":1}` || got.Status != "draft" || got.Notes != "notes" {
		t.Fatalf("round-trip mismatch: %+v", got)
	}
	if err := db.SetStatus(p.ID, "active"); err != nil {
		t.Fatalf("SetStatus: %v", err)
	}
	active, err := db.Active()
	if err != nil {
		t.Fatalf("Active: %v", err)
	}
	if active.ID != p.ID {
		t.Fatalf("Active id = %s, want %s", active.ID, p.ID)
	}
}

func TestResponseRoundTrip(t *testing.T) {
	db := openTestDB(t)
	cost := 0.0125
	in := &Response{
		CacheKey: "ck1", Seat: "judge", ConfigHash: "ch", PromptPackHash: "pp",
		InputHash: "ih", BundleHash: strptr("bh"), Repetition: 2, Origin: "production",
		RequestID: strptr("req1"), ModelRequested: "m-req", ModelReturned: "m-ret",
		Substituted: true, RawText: "raw", ParsedJSON: strptr(`{"a":1}`), ParseOK: true,
		PromptTokens: 10, CompletionTokens: 20, CostUSD: &cost, LatencyMs: 123,
		TimedOut: true, Error: strptr("e"), WordCount: 5,
	}
	got, err := db.InsertResponse(in)
	if err != nil {
		t.Fatalf("InsertResponse: %v", err)
	}
	byID, err := db.GetResponse(got.ID)
	if err != nil {
		t.Fatalf("GetResponse: %v", err)
	}
	byKey, err := db.GetResponseByCacheKey("ck1")
	if err != nil {
		t.Fatalf("GetResponseByCacheKey: %v", err)
	}
	for _, r := range []*Response{byID, byKey} {
		if r.CacheKey != "ck1" || *r.BundleHash != "bh" || r.Repetition != 2 ||
			*r.RequestID != "req1" || !r.Substituted || *r.ParsedJSON != `{"a":1}` ||
			!r.ParseOK || *r.CostUSD != cost || r.LatencyMs != 123 || !r.TimedOut ||
			*r.Error != "e" || r.WordCount != 5 {
			t.Fatalf("round-trip mismatch: %+v", r)
		}
	}
}

func TestThreadCard(t *testing.T) {
	db := openTestDB(t)
	th, err := db.CreateThread("t1")
	if err != nil {
		t.Fatalf("CreateThread: %v", err)
	}
	list, err := db.ListThreads()
	if err != nil || len(list) != 1 || list[0].ID != th.ID {
		t.Fatalf("ListThreads = %v, %v", list, err)
	}
	c1, err := db.InsertCard(th.ID, `{"decision":"d1"}`)
	if err != nil {
		t.Fatalf("InsertCard: %v", err)
	}
	c2, err := db.InsertCard(th.ID, `{"decision":"d2"}`)
	if err != nil {
		t.Fatalf("InsertCard: %v", err)
	}
	if c1.Version != 1 || c2.Version != 2 {
		t.Fatalf("versions = %d, %d", c1.Version, c2.Version)
	}
	latest, err := db.LatestCard(th.ID)
	if err != nil {
		t.Fatalf("LatestCard: %v", err)
	}
	if latest.CardJSON != `{"decision":"d2"}` || latest.CardHash == "" {
		t.Fatalf("LatestCard = %+v", latest)
	}
}

func TestRequestAggregateAndFeedback(t *testing.T) {
	db := openTestDB(t)
	th, _ := db.CreateThread("t")
	card, _ := db.InsertCard(th.ID, `{}`)
	pack, _ := db.InsertPack("active", `{}`, "pp", "")
	req, err := db.InsertRequest(&Request{
		ThreadID: th.ID, CardID: card.ID, PackID: pack.ID,
		SnapshotJSON: `{}`, InputHash: "ih", State: "created",
		ViewsDeadlineAt: "2026-01-01T00:00:00Z",
	})
	if err != nil {
		t.Fatalf("InsertRequest: %v", err)
	}
	v, err := db.InsertRequestView(req.ID, "judge", "pending")
	if err != nil {
		t.Fatalf("InsertRequestView: %v", err)
	}
	if _, err := db.InsertRequestJudge(req.ID); err != nil {
		t.Fatalf("InsertRequestJudge: %v", err)
	}
	now := "2026-01-01T00:00:01Z"
	if err := db.UpdateRequestViewState(v.ID, "complete", strptr("resp1"), true, &now); err != nil {
		t.Fatalf("UpdateRequestViewState: %v", err)
	}
	if _, err := db.InsertRewrite(req.ID, "judge", "shorter", "resp1", "out"); err != nil {
		t.Fatalf("InsertRewrite: %v", err)
	}
	if _, err := db.InsertSentMessage(req.ID, th.ID, "hello", strptr("judge")); err != nil {
		t.Fatalf("InsertSentMessage: %v", err)
	}
	if _, err := db.InsertFeedback(req.ID, "too_slow", "n1"); err != nil {
		t.Fatalf("InsertFeedback: %v", err)
	}
	if _, err := db.InsertFeedback(req.ID, "too_slow", "n2"); err != nil {
		t.Fatalf("InsertFeedback: %v", err)
	}
	agg, err := db.GetRequest(req.ID)
	if err != nil {
		t.Fatalf("GetRequest: %v", err)
	}
	if agg.Request.ID != req.ID || len(agg.Views) != 1 || agg.Judge == nil ||
		len(agg.Rewrites) != 1 || len(agg.Sent) != 1 {
		t.Fatalf("aggregate incomplete: %+v", agg)
	}
	if agg.Views[0].State != "complete" || !agg.Views[0].IncludedInJudge {
		t.Fatalf("view = %+v", agg.Views[0])
	}
	counts, err := db.FeedbackTagCounts("2020-01-01T00:00:00Z")
	if err != nil {
		t.Fatalf("FeedbackTagCounts: %v", err)
	}
	if counts["too_slow"] != 2 {
		t.Fatalf("counts = %v", counts)
	}
	byThread, err := db.ListRequestsByThread(th.ID)
	if err != nil || len(byThread) != 1 {
		t.Fatalf("ListRequestsByThread = %v, %v", byThread, err)
	}
	if err := db.UpdateRequestState(req.ID, "complete"); err != nil {
		t.Fatalf("UpdateRequestState: %v", err)
	}
	if err := db.SetSuperseded(req.ID, "other"); err != nil {
		t.Fatalf("SetSuperseded: %v", err)
	}
}
