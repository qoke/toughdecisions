package council

import (
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/qoke/toughdecisions/internal/gateway"
	"github.com/qoke/toughdecisions/internal/pack"
	"github.com/qoke/toughdecisions/internal/store"
)

// ghostViewPackYAML binds the possibility seat to a model absent from the
// models registry so ValidateSettings fails before any gateway call.
const ghostViewPackYAML = `seats:
  possibility: {model: ghost-view-model, family: openai}
  perspective: {model: v-persp, family: openai}
  stress_tester: {model: v-stress, family: openai}
  judge: {model: j1, family: openai}
`

// ghostJudgePackYAML binds the judge seat to an unknown model.
const ghostJudgePackYAML = `seats:
  possibility: {model: v-poss, family: openai}
  perspective: {model: v-persp, family: openai}
  stress_tester: {model: v-stress, family: openai}
  judge: {model: ghost-judge-model, family: openai}
`

// dangerJudgeJSON is testJudgeJSON with urgent danger raised.
const dangerJudgeJSON = `{"urgent_danger":{"present":true,"caution":"contact emergency services now"},"qualification":"q","recommended_reply":"r","why":"w","accepted_cost":"c","next":{"immediate":"i","forward":"f"},"change_course_if":"cc"}`

// seedCoverageRequest inserts a standalone request with a usable judge draft
// so Rewrite/resolveDraft paths run without a full gateway Run.
func seedCoverageRequest(t *testing.T, fx *testFixture, snapshot string) string {
	t.Helper()
	card, err := fx.db.InsertCard(fx.thread, testCardJSON)
	if err != nil {
		t.Fatalf("InsertCard: %v", err)
	}
	pk, err := pack.Active(fx.db)
	if err != nil {
		t.Fatalf("pack.Active: %v", err)
	}
	req, err := fx.db.InsertRequest(&store.Request{
		ThreadID: fx.thread, CardID: card.ID, PackID: pk.ID,
		SnapshotJSON: snapshot, InputHash: "coverage-hash",
		State:           "created",
		ViewsDeadlineAt: time.Now().UTC().Add(time.Hour).Format(time.RFC3339),
	})
	if err != nil {
		t.Fatalf("InsertRequest: %v", err)
	}
	if _, err := fx.db.InsertRequestJudge(req.ID); err != nil {
		t.Fatalf("InsertRequestJudge: %v", err)
	}
	resp, err := fx.db.InsertResponse(&store.Response{
		CacheKey: "ck-seed-" + req.ID, Seat: "judge", ConfigHash: "h",
		PromptPackHash: "p", InputHash: "coverage-hash", Origin: "production",
		ModelRequested: "j1", RawText: "judge draft text",
	})
	if err != nil {
		t.Fatalf("InsertResponse: %v", err)
	}
	if err := fx.db.UpdateJudgeResult(req.ID, store.JudgeResult{
		State: "complete", ResponseID: &resp.ID,
	}); err != nil {
		t.Fatalf("UpdateJudgeResult: %v", err)
	}
	return req.ID
}

// waitDangerEvent drains ch until the danger event arrives and returns its payload.
func waitDangerEvent(t *testing.T, ch <-chan Event) map[string]any {
	t.Helper()
	timeout := time.After(5 * time.Second)
	for {
		select {
		case ev, ok := <-ch:
			if !ok {
				t.Fatal("event channel closed before danger event")
			}
			if ev.Type == EventDanger {
				payload, _ := ev.Payload.(map[string]any)
				return payload
			}
		case <-timeout:
			t.Fatal("timed out waiting for danger event")
			return nil
		}
	}
}

// TestResolveDraftSourceRefs covers every source_ref branch of resolveDraft.
func TestResolveDraftSourceRefs(t *testing.T) {
	// Arrange
	fx := setupFixture(t, fastScripts(), "1s", "1s")
	viewResp, err := fx.db.InsertResponse(&store.Response{
		CacheKey: "ck-cov-view", Seat: "possibility", ConfigHash: "h",
		PromptPackHash: "p", InputHash: "ih", Origin: "production",
		ModelRequested: "v-poss", RawText: "VIEW_RAW_TEXT",
	})
	if err != nil {
		t.Fatalf("InsertResponse(view): %v", err)
	}
	judgeResp, err := fx.db.InsertResponse(&store.Response{
		CacheKey: "ck-cov-judge", Seat: "judge", ConfigHash: "h",
		PromptPackHash: "p", InputHash: "ih", Origin: "production",
		ModelRequested: "j1", RawText: "JUDGE_RAW_TEXT",
	})
	if err != nil {
		t.Fatalf("InsertResponse(judge): %v", err)
	}

	cases := []struct {
		name      string
		agg       *store.RequestAggregate
		sourceRef string
		wantText  string
		wantErr   string
		wantIs    error
	}{
		{
			name: "should return view raw text when source ref matches a stored view response",
			agg: &store.RequestAggregate{
				Views: []*store.RequestView{{Seat: "possibility", ResponseID: &viewResp.ID}},
			},
			sourceRef: "view:possibility",
			wantText:  "VIEW_RAW_TEXT",
		},
		{
			name: "should error when view row has no response id",
			agg: &store.RequestAggregate{
				Views: []*store.RequestView{{Seat: "possibility"}},
			},
			sourceRef: "view:possibility",
			wantErr:   `no view response for "view:possibility"`,
		},
		{
			name: "should surface store error when view response row is missing",
			agg: &store.RequestAggregate{
				Views: []*store.RequestView{{Seat: "possibility", ResponseID: strPtr("missing-resp")}},
			},
			sourceRef: "view:possibility",
			wantIs:    sql.ErrNoRows,
		},
		{
			name:      "should error when request has no judge row",
			agg:       &store.RequestAggregate{},
			sourceRef: "judge",
			wantErr:   "no judge response",
		},
		{
			name: "should surface store error when judge response row is missing",
			agg: &store.RequestAggregate{
				Judge: &store.RequestJudge{ResponseID: strPtr("missing-resp")},
			},
			sourceRef: "judge",
			wantIs:    sql.ErrNoRows,
		},
		{
			name: "should return judge raw text when judge response exists",
			agg: &store.RequestAggregate{
				Judge: &store.RequestJudge{ResponseID: &judgeResp.ID},
			},
			sourceRef: "judge",
			wantText:  "JUDGE_RAW_TEXT",
		},
		{
			name: "should return rewrite output when rewrite id matches",
			agg: &store.RequestAggregate{
				Rewrites: []*store.Rewrite{{ID: "rw-1", OutputText: "REWRITE_TEXT"}},
			},
			sourceRef: "rewrite:rw-1",
			wantText:  "REWRITE_TEXT",
		},
		{
			name:      "should error when rewrite id is unknown",
			agg:       &store.RequestAggregate{},
			sourceRef: "rewrite:rw-404",
			wantErr:   `no rewrite "rw-404"`,
		},
		{
			name:      "should error when source ref prefix is unknown",
			agg:       &store.RequestAggregate{},
			sourceRef: "sent:1",
			wantErr:   `unknown source_ref "sent:1"`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Act
			got, err := fx.runner.resolveDraft(tc.agg, tc.sourceRef)

			// Assert
			if tc.wantIs != nil || tc.wantErr != "" {
				if err == nil {
					t.Fatalf("resolveDraft(%q) err = nil, want error", tc.sourceRef)
				}
				if tc.wantIs != nil && !errors.Is(err, tc.wantIs) {
					t.Errorf("resolveDraft(%q) err = %v, want Is(%v)", tc.sourceRef, err, tc.wantIs)
				}
				if tc.wantErr != "" && !strings.Contains(err.Error(), tc.wantErr) {
					t.Errorf("resolveDraft(%q) err = %q, want contains %q", tc.sourceRef, err, tc.wantErr)
				}
				if got != "" {
					t.Errorf("resolveDraft(%q) text = %q, want empty on error", tc.sourceRef, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("resolveDraft(%q): %v", tc.sourceRef, err)
			}
			if got != tc.wantText {
				t.Errorf("resolveDraft(%q) = %q, want %q", tc.sourceRef, got, tc.wantText)
			}
		})
	}
}

// TestRewriteInputGuards covers Rewrite's validation and resolve/decode guards.
func TestRewriteInputGuards(t *testing.T) {
	// Arrange
	fx := setupFixture(t, fastScripts(), "1s", "1s")
	seeded := seedCoverageRequest(t, fx, `{"question":"q"}`)
	badSnapshot := seedCoverageRequest(t, fx, "not-json")

	cases := []struct {
		name        string
		requestID   string
		sourceRef   string
		instruction string
		wantErr     string
		wantIs      error
	}{
		{
			name:        "should reject when request id is empty",
			requestID:   "",
			sourceRef:   "judge",
			instruction: "do it",
			wantErr:     "request_id is required",
		},
		{
			name:        "should reject when instruction is empty",
			requestID:   seeded,
			sourceRef:   "judge",
			instruction: "   ",
			wantErr:     "instruction is required",
		},
		{
			name:        "should surface store error when request does not exist",
			requestID:   "no-such-request",
			sourceRef:   "judge",
			instruction: "do it",
			wantIs:      sql.ErrNoRows,
		},
		{
			name:        "should reject unknown source ref",
			requestID:   seeded,
			sourceRef:   "sent:42",
			instruction: "do it",
			wantErr:     `unknown source_ref "sent:42"`,
		},
		{
			name:        "should reject when snapshot is not valid JSON",
			requestID:   badSnapshot,
			sourceRef:   "judge",
			instruction: "do it",
			wantErr:     "decode snapshot",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Act
			text, rwID, err := fx.runner.Rewrite(t.Context(), tc.requestID, tc.sourceRef, tc.instruction)

			// Assert
			if err == nil {
				t.Fatal("Rewrite err = nil, want error")
			}
			if tc.wantIs != nil && !errors.Is(err, tc.wantIs) {
				t.Errorf("Rewrite err = %v, want Is(%v)", err, tc.wantIs)
			}
			if tc.wantErr != "" && !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("Rewrite err = %q, want contains %q", err, tc.wantErr)
			}
			if text != "" || rwID != "" {
				t.Errorf("Rewrite = (%q, %q), want empty on error", text, rwID)
			}
		})
	}
}

// TestRewriteSurfacesGatewayError asserts a failed rewrite chat call is
// returned to the caller instead of being swallowed.
func TestRewriteSurfacesGatewayError(t *testing.T) {
	// Arrange
	scripts := fastScripts()
	scripts["j1"] = []gateway.Step{
		{Content: testJudgeJSON, ModelReturned: "j1"},
		{Err: errors.New("rewrite gateway down"), ModelReturned: "j1"},
	}
	fx := setupFixture(t, scripts, "100ms", "2s")
	id, err := fx.runner.Run(t.Context(), testCreateRequest(fx.thread))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	waitRequestState(t, fx.db, id, "complete", 10*time.Second)

	// Act
	text, rwID, err := fx.runner.Rewrite(t.Context(), id, "judge", "make it shorter")

	// Assert
	if err == nil {
		t.Fatal("Rewrite err = nil, want gateway error")
	}
	if !strings.Contains(err.Error(), "rewrite gateway down") {
		t.Errorf("Rewrite err = %q, want contains %q", err, "rewrite gateway down")
	}
	if text != "" || rwID != "" {
		t.Errorf("Rewrite = (%q, %q), want empty on error", text, rwID)
	}
}

// TestRewriteFallsBackToJudgeSeatWhenConfiguredSeatInvalid asserts an
// unparseable rewrite_seat config falls back to the judge seat.
func TestRewriteFallsBackToJudgeSeatWhenConfiguredSeatInvalid(t *testing.T) {
	// Arrange
	t.Setenv("COUNCIL_REWRITE_SEAT", "chair")
	scripts := fastScripts()
	scripts["j1"] = []gateway.Step{
		{Content: testJudgeJSON, ModelReturned: "j1"},
		{Content: "rewritten via judge seat", ModelReturned: "j1"},
	}
	fx := setupFixture(t, scripts, "100ms", "2s")
	id, err := fx.runner.Run(t.Context(), testCreateRequest(fx.thread))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	waitRequestState(t, fx.db, id, "complete", 10*time.Second)

	if got := fx.runner.cfg.RewriteSeat(); got != "chair" {
		t.Fatalf("RewriteSeat() = %q, want configured %q", got, "chair")
	}

	// Act
	text, rwID, err := fx.runner.Rewrite(t.Context(), id, "judge", "make it shorter")

	// Assert
	if err != nil {
		t.Fatalf("Rewrite: %v", err)
	}
	if text != "rewritten via judge seat" {
		t.Errorf("Rewrite text = %q, want %q", text, "rewritten via judge seat")
	}
	if rwID == "" {
		t.Error("Rewrite id is empty")
	}
	calls := fx.fake.Calls
	if len(calls) == 0 {
		t.Fatal("no gateway calls recorded")
	}
	if got := calls[len(calls)-1].Model; got != "j1" {
		t.Errorf("last gateway model = %q, want judge model j1", got)
	}
}

// TestRewriteRejectsUnknownModelSeat asserts ValidateSettings rejects a
// rewrite seat whose model is absent from the models registry before any
// gateway call is made.
func TestRewriteRejectsUnknownModelSeat(t *testing.T) {
	// Arrange
	fx := setupFixtureWithPack(t, ghostJudgePackYAML, fastScripts())
	reqID := seedCoverageRequest(t, fx, `{"question":"q"}`)

	// Act
	text, rwID, err := fx.runner.Rewrite(t.Context(), reqID, "judge", "shorten it")

	// Assert
	if err == nil {
		t.Fatal("Rewrite err = nil, want unsupported-setting error")
	}
	if !errors.Is(err, gateway.ErrUnsupportedSetting) {
		t.Errorf("Rewrite err = %v, want Is(gateway.ErrUnsupportedSetting)", err)
	}
	if text != "" || rwID != "" {
		t.Errorf("Rewrite = (%q, %q), want empty on error", text, rwID)
	}
}

// TestRunMarksUnsupportedViewSeatFailed asserts a seat bound to an unknown
// model fails without ever reaching the gateway, leaving the run partial.
func TestRunMarksUnsupportedViewSeatFailed(t *testing.T) {
	// Arrange
	fx := setupFixtureWithPack(t, ghostViewPackYAML, fastScripts())

	// Act
	id, err := fx.runner.Run(t.Context(), testCreateRequest(fx.thread))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	waitRequestState(t, fx.db, id, "complete_partial", 10*time.Second)

	// Assert
	v := viewBySeat(t, fx.db, id, "possibility")
	if v.State != "failed" {
		t.Errorf("possibility state = %q, want failed", v.State)
	}
	if v.ResponseID != nil {
		t.Errorf("possibility response id = %q, want nil (no gateway call)", *v.ResponseID)
	}
	if p := viewBySeat(t, fx.db, id, "perspective"); p.State != "complete" {
		t.Errorf("perspective state = %q, want complete", p.State)
	}
	if s := viewBySeat(t, fx.db, id, "stress_tester"); s.State != "complete" {
		t.Errorf("stress_tester state = %q, want complete", s.State)
	}
	for _, c := range fx.fake.Calls {
		if c.Model == "ghost-view-model" {
			t.Errorf("gateway called with unsupported model %q", c.Model)
		}
	}
	if got := fx.fake.CallCount(); got != 3 {
		t.Errorf("gateway calls = %d, want 3 (two views + judge)", got)
	}
}

// TestRunFailsJudgeWhenModelUnknown asserts an unknown judge model fails the
// judge phase before any gateway call and leaves the request partial.
func TestRunFailsJudgeWhenModelUnknown(t *testing.T) {
	// Arrange
	fx := setupFixtureWithPack(t, ghostJudgePackYAML, fastScripts())

	// Act
	id, err := fx.runner.Run(t.Context(), testCreateRequest(fx.thread))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	waitRequestState(t, fx.db, id, "complete_partial", 10*time.Second)

	// Assert
	agg, err := fx.db.GetRequest(id)
	if err != nil {
		t.Fatalf("GetRequest: %v", err)
	}
	if agg.Judge == nil {
		t.Fatal("judge row is nil")
	}
	if agg.Judge.State != "failed" {
		t.Errorf("judge state = %q, want failed", agg.Judge.State)
	}
	if agg.Judge.ResponseID != nil {
		t.Errorf("judge response id = %q, want nil", *agg.Judge.ResponseID)
	}
	if got := fx.fake.CallCount(); got != 3 {
		t.Errorf("gateway calls = %d, want 3 (views only; judge rejected before call)", got)
	}
}

// TestRunJudgeDangerFlagsRequest asserts a judge payload reporting urgent
// danger persists danger_flagged and publishes the danger event with the
// judge seat.
func TestRunJudgeDangerFlagsRequest(t *testing.T) {
	// Arrange: delay one view so Subscribe lands before judge/danger events.
	scripts := fastScripts()
	scripts["v-stress"] = []gateway.Step{
		{Content: testViewJSON, ModelReturned: "v-stress", Delay: 200 * time.Millisecond},
	}
	scripts["j1"] = []gateway.Step{{Content: dangerJudgeJSON, ModelReturned: "j1"}}
	fx := setupFixture(t, scripts, "1s", "1s")
	id, err := fx.runner.Run(t.Context(), testCreateRequest(fx.thread))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	ch, unsub := fx.reg.Subscribe(id)
	defer unsub()

	// Act
	row := waitRequestState(t, fx.db, id, "complete", 10*time.Second)
	payload := waitDangerEvent(t, ch)

	// Assert
	if !row.DangerFlagged {
		t.Error("danger_flagged = false, want true after judge reports urgent danger")
	}
	if got, _ := payload["seat"].(string); got != "judge" {
		t.Errorf("danger event seat = %q, want judge", got)
	}
	if got, _ := payload["caution"].(string); got != "contact emergency services now" {
		t.Errorf("danger event caution = %q, want judge caution text", got)
	}
}

// TestRunFlagsRequestWhenViewReportsDanger asserts a view payload reporting
// urgent danger persists danger_flagged and publishes the danger event with
// that view's seat.
func TestRunFlagsRequestWhenViewReportsDanger(t *testing.T) {
	// Arrange: delay the danger view so Subscribe lands before its event.
	dangerView := strings.Replace(testViewJSON,
		`"present":false`,
		`"present":true,"caution":"confirm the sender really asked"`, 1)
	scripts := fastScripts()
	scripts["v-poss"] = []gateway.Step{
		{Content: dangerView, ModelReturned: "v-poss", Delay: 200 * time.Millisecond},
	}
	fx := setupFixture(t, scripts, "1s", "1s")
	id, err := fx.runner.Run(t.Context(), testCreateRequest(fx.thread))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	ch, unsub := fx.reg.Subscribe(id)
	defer unsub()

	// Act
	row := waitRequestState(t, fx.db, id, "complete", 10*time.Second)
	payload := waitDangerEvent(t, ch)

	// Assert
	if !row.DangerFlagged {
		t.Error("danger_flagged = false, want true after view reports urgent danger")
	}
	if got, _ := payload["seat"].(string); got != "possibility" {
		t.Errorf("danger event seat = %q, want possibility", got)
	}
	if got, _ := payload["caution"].(string); got != "confirm the sender really asked" {
		t.Errorf("danger event caution = %q, want view caution text", got)
	}
}

// TestRegistryPublishAndMarkDoneForUnknownRequest asserts Publish never
// fabricates run entries while MarkDone registers one so the cleaner can
// reclaim it.
func TestRegistryPublishAndMarkDoneForUnknownRequest(t *testing.T) {
	t.Run("should not create a run entry when publishing to an unknown request", func(t *testing.T) {
		// Arrange
		reg := NewRegistry(time.Minute)
		t.Cleanup(reg.Close)

		// Act
		reg.Publish("ghost-publish-req", Event{
			Type: EventDone, RequestID: "ghost-publish-req", At: time.Now(),
		})

		// Assert
		if reg.Has("ghost-publish-req") {
			t.Error("Publish fabricated a run entry for an unknown request")
		}
	})

	t.Run("should register a run entry when marking an unknown request done", func(t *testing.T) {
		// Arrange
		reg := NewRegistry(time.Minute)
		t.Cleanup(reg.Close)

		// Act
		reg.MarkDone("ghost-done-req")

		// Assert
		if !reg.Has("ghost-done-req") {
			t.Error("MarkDone did not register the run entry for cleaner reclamation")
		}
	})
}

// TestRunValidatesCreateRequest asserts Run rejects incomplete create
// requests before touching the store or gateway.
func TestRunValidatesCreateRequest(t *testing.T) {
	// Arrange
	fx := setupFixture(t, fastScripts(), "1s", "1s")

	cases := []struct {
		name    string
		req     CreateRequest
		wantErr string
	}{
		{
			name:    "should reject when thread id is empty",
			req:     CreateRequest{Question: "what should I do?"},
			wantErr: "thread_id is required",
		},
		{
			name:    "should reject when messages and question are both empty",
			req:     CreateRequest{ThreadID: fx.thread},
			wantErr: "messages or question is required",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Act
			id, err := fx.runner.Run(t.Context(), tc.req)

			// Assert
			if err == nil {
				t.Fatal("Run err = nil, want validation error")
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("Run err = %q, want contains %q", err, tc.wantErr)
			}
			if id != "" {
				t.Errorf("Run id = %q, want empty on validation failure", id)
			}
		})
	}
}
