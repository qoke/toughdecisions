package httpapi

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/qoke/toughdecisions/internal/config"
	"github.com/qoke/toughdecisions/internal/council"
	"github.com/qoke/toughdecisions/internal/gateway"
	"github.com/qoke/toughdecisions/internal/logx"
	"github.com/qoke/toughdecisions/internal/models"
	"github.com/qoke/toughdecisions/internal/pack"
	"github.com/qoke/toughdecisions/internal/schema"
	"github.com/qoke/toughdecisions/internal/store"
)

const (
	testViewJSON  = `{"urgent_danger":{"present":false},"qualification":"q","suggested_reply":"r","decisive_insight":"i","tradeoff_or_objection":"t","depends_on":"d","fallback":"f"}`
	testJudgeJSON = `{"urgent_danger":{"present":false},"qualification":"q","recommended_reply":"r","why":"w","accepted_cost":"c","next":{"immediate":"i","forward":"f"},"change_course_if":"cc"}`
	testPackYAML  = `seats:
  possibility: {model: v-poss, family: openai}
  perspective: {model: v-persp, family: openai}
  stress_tester: {model: v-stress, family: openai}
  judge: {model: j1, family: openai}
`
	testModelsYAML = `models:
  - id: v-poss
    family: openai
    expected_response_model_prefixes: ["v-poss"]
    supports: {temperature: true, top_p: true, reasoning_effort: true, json_schema: true, json_object: true}
  - id: v-persp
    family: openai
    expected_response_model_prefixes: ["v-persp"]
    supports: {temperature: true, top_p: true, reasoning_effort: true, json_schema: true, json_object: true}
  - id: v-stress
    family: openai
    expected_response_model_prefixes: ["v-stress"]
    supports: {temperature: true, top_p: true, reasoning_effort: true, json_schema: true, json_object: true}
  - id: j1
    family: openai
    expected_response_model_prefixes: ["j1"]
    supports: {temperature: true, top_p: true, reasoning_effort: true, json_schema: true, json_object: true}
`
	testCardJSON = `{"decision":"d","context":"c","priorities":"p","unusual":"u","history":"h","deadline":"dl","style":"s"}`
)

type testServer struct {
	srv    *Server
	db     *store.DB
	dbPath string
	runner *council.Runner
	reg    *council.Registry
	thread string
}

func writeTemp(t *testing.T, name, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatalf("write temp: %v", err)
	}
	return p
}

func setupTestServer(t *testing.T, scripts map[string][]gateway.Step) *testServer {
	t.Helper()
	t.Setenv("COUNCIL_VIEWS_DEADLINE", "2s")
	t.Setenv("COUNCIL_JUDGE_DEADLINE", "2s")
	t.Setenv("COUNCIL_REWRITE_DEADLINE", "2s")
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	db, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := pack.Init(db, writeTemp(t, "pack.yaml", testPackYAML)); err != nil {
		t.Fatalf("pack.Init: %v", err)
	}
	mreg, err := models.LoadRegistry(writeTemp(t, "models.yaml", testModelsYAML))
	if err != nil {
		t.Fatalf("LoadRegistry: %v", err)
	}
	th, err := db.CreateThread("t")
	if err != nil {
		t.Fatalf("CreateThread: %v", err)
	}
	if _, err := db.InsertCard(th.ID, testCardJSON); err != nil {
		t.Fatalf("InsertCard: %v", err)
	}
	fake := gateway.NewFake(scripts)
	reg := council.NewRegistry(5 * time.Minute)
	t.Cleanup(reg.Close)
	log := logx.NewWithWriter(cfg, io.Discard)
	runner := council.NewRunner(db, fake, reg, cfg, log, mreg)
	return &testServer{srv: New(db, runner, reg, cfg, log), db: db, dbPath: dbPath, runner: runner, reg: reg, thread: th.ID}
}

func fastScripts() map[string][]gateway.Step {
	return map[string][]gateway.Step{
		"v-poss":   {{Content: testViewJSON, ModelReturned: "v-poss"}},
		"v-persp":  {{Content: testViewJSON, ModelReturned: "v-persp"}},
		"v-stress": {{Content: testViewJSON, ModelReturned: "v-stress"}},
		"j1":       {{Content: testJudgeJSON, ModelReturned: "j1"}},
	}
}

func doReq(t *testing.T, h http.Handler, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	var r *http.Request
	if body == "" {
		r = httptest.NewRequest(method, path, nil)
	} else {
		r = httptest.NewRequest(method, path, strings.NewReader(body))
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func decodeJSON(t *testing.T, w *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode %q: %v", w.Body.String(), err)
	}
	return out
}

func waitState(t *testing.T, db *store.DB, id, want string) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for {
		agg, err := db.GetRequest(id)
		if err != nil {
			t.Fatalf("GetRequest: %v", err)
		}
		if agg.Request.State == want && agg.Request.FinishedAt != nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("request state = %q, want %q", agg.Request.State, want)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func runCompletedRequest(t *testing.T, ts *testServer) string {
	t.Helper()
	id, err := ts.runner.Run(context.Background(), council.CreateRequest{
		ThreadID: ts.thread,
		Messages: []schema.Message{{Sender: "them", Text: "hi"}},
		Question: "what should I do?",
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	waitState(t, ts.db, id, "complete")
	return id
}

func TestPostRequestsEmptyMessagesAndQuestionRejected(t *testing.T) {
	ts := setupTestServer(t, fastScripts())
	w := doReq(t, ts.srv.Handler(), "POST", "/api/requests",
		fmt.Sprintf(`{"thread_id":%q,"messages":[],"question":""}`, ts.thread))
	if w.Code < 400 || w.Code >= 500 {
		t.Fatalf("status = %d, want 4xx", w.Code)
	}
	out := decodeJSON(t, w)
	if _, ok := out["error"]; !ok {
		t.Fatalf("body missing error key: %v", out)
	}
	if _, ok := out["code"]; !ok {
		t.Fatalf("body missing code key: %v", out)
	}
}

func TestPostRequestsOversizeBodyRejected(t *testing.T) {
	ts := setupTestServer(t, fastScripts())
	big := strings.Repeat("x", 300*1024)
	body := fmt.Sprintf(`{"thread_id":%q,"messages":[{"sender":"them","text":%q}],"question":"q"}`, ts.thread, big)
	w := doReq(t, ts.srv.Handler(), "POST", "/api/requests", body)
	if w.Code != http.StatusRequestEntityTooLarge && (w.Code < 400 || w.Code >= 500) {
		t.Fatalf("status = %d, want 413 or 4xx", w.Code)
	}
}

func TestHealthz(t *testing.T) {
	ts := setupTestServer(t, fastScripts())
	w := doReq(t, ts.srv.Handler(), "GET", "/healthz", "")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", w.Code, w.Body.String())
	}
	out := decodeJSON(t, w)
	if out["ok"] != true {
		t.Fatalf("ok = %v: %v", out["ok"], out)
	}
	if _, ok := out["db"]; !ok {
		t.Fatalf("missing db key: %v", out)
	}
	packID, ok := out["pack_id"].(string)
	if !ok || packID == "" {
		t.Fatalf("missing pack_id: %v", out)
	}
}

func TestSSESnapshotFirstThenLive(t *testing.T) {
	ts := setupTestServer(t, fastScripts())
	id, err := ts.runner.Run(context.Background(), council.CreateRequest{
		ThreadID: ts.thread,
		Messages: []schema.Message{{Sender: "them", Text: "hi"}},
		Question: "q?",
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	httpSrv := httptest.NewServer(ts.srv.Handler())
	defer httpSrv.Close()

	type frame struct {
		event string
		data  string
	}
	frames := make(chan frame, 8)
	errCh := make(chan error, 1)
	go func() {
		resp, err := httpSrv.Client().Get(httpSrv.URL + "/api/requests/" + id + "/events")
		if err != nil {
			errCh <- err
			return
		}
		defer resp.Body.Close()
		if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
			errCh <- fmt.Errorf("content-type = %q", ct)
			return
		}
		sc := bufio.NewScanner(resp.Body)
		sc.Buffer(make([]byte, 1024*1024), 1024*1024)
		var ev, data string
		for sc.Scan() {
			line := sc.Text()
			switch {
			case strings.HasPrefix(line, "event:"):
				ev = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
			case strings.HasPrefix(line, "data:"):
				data = strings.TrimSpace(strings.TrimPrefix(line, "data:"))
			case line == "":
				if ev != "" {
					frames <- frame{ev, data}
					ev, data = "", ""
				}
			}
			if len(frames) == cap(frames) {
				return
			}
		}
	}()

	select {
	case f := <-frames:
		if f.event != "snapshot" {
			t.Fatalf("first SSE event = %q, want snapshot", f.event)
		}
		var payload map[string]any
		if err := json.Unmarshal([]byte(f.data), &payload); err != nil {
			t.Fatalf("snapshot data not JSON: %v", err)
		}
	case err := <-errCh:
		t.Fatalf("sse client: %v", err)
	case <-time.After(10 * time.Second):
		t.Fatal("no snapshot frame")
	}

	ts.reg.Publish(id, council.Event{Type: council.EventViewComplete, RequestID: id, At: time.Now(), Payload: map[string]any{"seat": "possibility"}})
	select {
	case f := <-frames:
		if f.event != "view_complete" {
			t.Fatalf("second SSE event = %q, want view_complete", f.event)
		}
	case err := <-errCh:
		t.Fatalf("sse client: %v", err)
	case <-time.After(10 * time.Second):
		t.Fatal("no live frame after publish")
	}
}

func TestGetRequestReconstructsAfterRestart(t *testing.T) {
	ts := setupTestServer(t, fastScripts())
	id := runCompletedRequest(t, ts)
	before := doReq(t, ts.srv.Handler(), "GET", "/api/requests/"+id, "")
	if before.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", before.Code, before.Body.String())
	}
	if err := ts.db.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	db2, err := store.Open(ts.dbPath)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer db2.Close()
	t.Setenv("COUNCIL_VIEWS_DEADLINE", "2s")
	t.Setenv("COUNCIL_JUDGE_DEADLINE", "2s")
	t.Setenv("COUNCIL_REWRITE_DEADLINE", "2s")
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	log := logx.NewWithWriter(cfg, io.Discard)
	reg2 := council.NewRegistry(0)
	defer reg2.Close()
	srv2 := New(db2, nil, reg2, cfg, log)
	after := doReq(t, srv2.Handler(), "GET", "/api/requests/"+id, "")
	if after.Code != http.StatusOK {
		t.Fatalf("status after reopen = %d: %s", after.Code, after.Body.String())
	}
	if before.Body.String() != after.Body.String() {
		t.Fatalf("state differs after restart:\nbefore: %s\nafter:  %s", before.Body.String(), after.Body.String())
	}
	var out map[string]any
	if err := json.Unmarshal(after.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	views, ok := out["views"].(map[string]any)
	if !ok || len(views) != 3 {
		t.Fatalf("views = %v, want 3 seats", out["views"])
	}
	if _, ok := out["judge"]; !ok {
		t.Fatalf("missing judge: %v", out)
	}
}

func TestRewriteSentFeedback(t *testing.T) {
	scripts := fastScripts()
	scripts["j1"] = []gateway.Step{
		{Content: testJudgeJSON, ModelReturned: "j1"},
		{Content: "rewritten text", ModelReturned: "j1"},
	}
	ts := setupTestServer(t, scripts)
	id := runCompletedRequest(t, ts)

	w := doReq(t, ts.srv.Handler(), "POST", "/api/requests/"+id+"/rewrite",
		`{"source_ref":"judge","instruction":"make it shorter"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("rewrite status = %d: %s", w.Code, w.Body.String())
	}
	out := decodeJSON(t, w)
	if out["text"] != "rewritten text" {
		t.Fatalf("rewrite text = %v", out)
	}
	if _, ok := out["rewrite_id"]; !ok {
		t.Fatalf("missing rewrite_id: %v", out)
	}

	w = doReq(t, ts.srv.Handler(), "POST", "/api/requests/"+id+"/sent",
		`{"text":"final answer","source_ref":"judge"}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("sent status = %d: %s", w.Code, w.Body.String())
	}

	w = doReq(t, ts.srv.Handler(), "POST", "/api/requests/"+id+"/feedback",
		`{"tag":"too_slow","note":"slow"}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("feedback status = %d: %s", w.Code, w.Body.String())
	}

	agg, err := ts.db.GetRequest(id)
	if err != nil {
		t.Fatalf("GetRequest: %v", err)
	}
	if len(agg.Rewrites) != 1 || agg.Rewrites[0].OutputText != "rewritten text" {
		t.Fatalf("rewrites not persisted: %+v", agg.Rewrites)
	}
	if len(agg.Sent) != 1 || agg.Sent[0].Text != "final answer" {
		t.Fatalf("sent not persisted: %+v", agg.Sent)
	}
	counts, err := ts.db.FeedbackTagCounts("2020-01-01T00:00:00Z")
	if err != nil {
		t.Fatalf("FeedbackTagCounts: %v", err)
	}
	if counts["too_slow"] != 1 {
		t.Fatalf("feedback not persisted: %v", counts)
	}
}

func TestIndexServesEmbeddedUI(t *testing.T) {
	ts := setupTestServer(t, fastScripts())
	w := doReq(t, ts.srv.Handler(), "GET", "/", "")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); !strings.Contains(ct, "text/html") {
		t.Fatalf("content-type = %q", ct)
	}
	if !strings.Contains(w.Body.String(), "Independent view — not yet synthesized") {
		t.Fatal("embedded HTML missing badge string")
	}
}

func TestThreadsCardRoundTrip(t *testing.T) {
	ts := setupTestServer(t, fastScripts())
	w := doReq(t, ts.srv.Handler(), "POST", "/api/threads", `{"name":"family"}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("create status = %d: %s", w.Code, w.Body.String())
	}
	created := decodeJSON(t, w)
	id, _ := created["id"].(string)
	if id == "" {
		t.Fatalf("missing id: %v", created)
	}
	w = doReq(t, ts.srv.Handler(), "PUT", "/api/threads/"+id+"/card", testCardJSON)
	if w.Code != http.StatusOK && w.Code != http.StatusCreated {
		t.Fatalf("card status = %d: %s", w.Code, w.Body.String())
	}
	w = doReq(t, ts.srv.Handler(), "GET", "/api/threads/"+id, "")
	if w.Code != http.StatusOK {
		t.Fatalf("get status = %d: %s", w.Code, w.Body.String())
	}
	got := decodeJSON(t, w)
	card, ok := got["card"].(map[string]any)
	if !ok || card["decision"] != "d" {
		t.Fatalf("card = %v", got["card"])
	}
}

func TestMetricsSummaryEmptyAndWithData(t *testing.T) {
	ts := setupTestServer(t, fastScripts())
	h := ts.srv.Handler()
	w := doReq(t, h, "GET", "/api/metrics/summary", "")
	if w.Code != 200 {
		t.Fatalf("empty metrics status = %d: %s", w.Code, w.Body.String())
	}
	var out map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out["request_count"] != float64(0) {
		t.Fatalf("request_count = %v, want 0", out["request_count"])
	}
	// With data: complete one request, record timings via the DB.
	id := runCompletedRequest(t, ts)
	agg, err := ts.db.GetRequest(id)
	if err != nil {
		t.Fatalf("GetRequest: %v", err)
	}
	_ = agg
	tFirst := int64(100)
	tFinal := int64(500)
	if err := ts.db.UpdateRequestProgress(id, store.RequestProgressPatch{TFirstUsableViewMs: &tFirst, TFinalMs: &tFinal}); err != nil {
		t.Fatalf("progress: %v", err)
	}
	if _, err := ts.db.InsertFeedback(id, "too_slow", "n"); err != nil {
		t.Fatalf("feedback: %v", err)
	}
	w = doReq(t, h, "GET", "/api/metrics/summary?days=30", "")
	if w.Code != 200 {
		t.Fatalf("metrics status = %d: %s", w.Code, w.Body.String())
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out["request_count"] != float64(1) {
		t.Fatalf("request_count = %v, want 1", out["request_count"])
	}
	if out["view_completion_rate"] != float64(1) {
		t.Fatalf("view rate = %v", out["view_completion_rate"])
	}
	if _, ok := out["feedback_tag_counts"]; !ok {
		t.Fatalf("missing feedback_tag_counts: %v", out)
	}
	w = doReq(t, h, "GET", "/api/metrics/summary?days=nope", "")
	if w.Code != 400 {
		t.Fatalf("bad days status = %d", w.Code)
	}
}

func TestPackActiveNeverLeaksKey(t *testing.T) {
	ts := setupTestServer(t, fastScripts())
	w := doReq(t, ts.srv.Handler(), "GET", "/api/pack/active", "")
	if w.Code != 200 {
		t.Fatalf("status = %d: %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	for _, leak := range []string{"api_key", "secret", "temperature", "top_p", "reasoning"} {
		if strings.Contains(strings.ToLower(body), leak) {
			t.Fatalf("pack/active leaks %q: %s", leak, body)
		}
	}
	var out map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if _, ok := out["seats"]; !ok {
		t.Fatalf("missing seats: %v", out)
	}
	if out["status"] != "active" {
		t.Fatalf("status = %v", out["status"])
	}
}

func TestListThreadsAndErrors(t *testing.T) {
	ts := setupTestServer(t, fastScripts())
	h := ts.srv.Handler()
	w := doReq(t, h, "GET", "/api/threads", "")
	if w.Code != 200 {
		t.Fatalf("list status = %d: %s", w.Code, w.Body.String())
	}
	var out map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if _, ok := out["threads"]; !ok {
		t.Fatalf("missing threads: %v", out)
	}
	// Create/get request error branches return generic messages.
	w = doReq(t, h, "POST", "/api/requests", `{"thread_id":"missing","messages":[{"sender":"them","text":"hi"}],"question":"q"}`)
	if w.Code != 404 {
		t.Fatalf("missing thread status = %d", w.Code)
	}
	w = doReq(t, h, "POST", "/api/requests", `{"thread_id":"`+ts.thread+`","messages":[],"question":""}`)
	if w.Code < 400 {
		t.Fatalf("empty status = %d", w.Code)
	}
	w = doReq(t, h, "GET", "/api/requests/does-not-exist", "")
	if w.Code != 404 {
		t.Fatalf("missing request status = %d: %s", w.Code, w.Body.String())
	}
	got := decodeJSON(t, w)
	if got["error"] == nil || got["code"] == nil {
		t.Fatalf("shape = %v", got)
	}
	// Create with a thread that has no card: generic client message, no verbatim internals.
	th, err := ts.db.CreateThread("nocard")
	if err != nil {
		t.Fatalf("CreateThread: %v", err)
	}
	w = doReq(t, h, "POST", "/api/requests", `{"thread_id":"`+th.ID+`","messages":[{"sender":"them","text":"hi"}],"question":"q"}`)
	if w.Code < 400 {
		t.Fatalf("no-card status = %d", w.Code)
	}
	got = decodeJSON(t, w)
	if msg, _ := got["error"].(string); strings.Contains(msg, "no rows") || strings.Contains(msg, "sql") {
		t.Fatalf("verbatim internal error: %v", got)
	}
	// Rewrite error branch is generic too.
	w = doReq(t, h, "POST", "/api/requests/does-not-exist/rewrite", `{"source_ref":"judge","instruction":"x"}`)
	if w.Code != 404 {
		t.Fatalf("rewrite missing status = %d", w.Code)
	}
	id := runCompletedRequest(t, ts)
	w = doReq(t, h, "POST", "/api/requests/"+id+"/rewrite", `{"source_ref":"bogus","instruction":"x"}`)
	if w.Code < 400 {
		t.Fatalf("bad rewrite status = %d", w.Code)
	}
	got = decodeJSON(t, w)
	if _, ok := got["error"]; !ok {
		t.Fatalf("rewrite shape = %v", got)
	}
}

func TestCreateThreadSentFeedbackValidation(t *testing.T) {
	ts := setupTestServer(t, fastScripts())
	h := ts.srv.Handler()
	w := doReq(t, h, "POST", "/api/threads", `{"name":""}`)
	if w.Code != 400 {
		t.Fatalf("empty name status = %d", w.Code)
	}
	w = doReq(t, h, "POST", "/api/threads", `not json`)
	if w.Code != 400 {
		t.Fatalf("bad json status = %d", w.Code)
	}
	id := runCompletedRequest(t, ts)
	w = doReq(t, h, "POST", "/api/requests/"+id+"/sent", `{"text":""}`)
	if w.Code != 400 {
		t.Fatalf("empty sent status = %d", w.Code)
	}
	w = doReq(t, h, "POST", "/api/requests/"+id+"/feedback", `{"tag":"nope"}`)
	if w.Code != 400 {
		t.Fatalf("bad tag status = %d", w.Code)
	}
	w = doReq(t, h, "GET", "/nope", "")
	if w.Code != 404 {
		t.Fatalf("index 404 = %d", w.Code)
	}
	w = doReq(t, h, "GET", "/api/threads/missing", "")
	if w.Code != 404 {
		t.Fatalf("thread 404 = %d", w.Code)
	}
}
