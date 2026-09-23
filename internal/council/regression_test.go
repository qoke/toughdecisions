package council

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/qoke/toughdecisions/internal/gateway"
	"github.com/qoke/toughdecisions/internal/models"
)

// hangingClient blocks on one model until ctx is done, then returns the ctx
// error like a real cancelled HTTP call. Other models answer immediately.
type hangingClient struct {
	inner *gateway.Fake
	hang  string
}

func (h *hangingClient) Chat(ctx context.Context, req gateway.ChatRequest) (gateway.ChatResponse, error) {
	if req.Model == h.hang {
		<-ctx.Done()
		return gateway.ChatResponse{}, ctx.Err()
	}
	return h.inner.Chat(ctx, req)
}

func loadRegistryForTest(t *testing.T, yaml string) *models.Registry {
	t.Helper()
	p := filepath.Join(t.TempDir(), "models-cap.yaml")
	if err := os.WriteFile(p, []byte(yaml), 0o644); err != nil {
		t.Fatalf("write models: %v", err)
	}
	mreg, err := models.LoadRegistry(p)
	if err != nil {
		t.Fatalf("LoadRegistry: %v", err)
	}
	return mreg
}

func waitDone(t *testing.T, reg *Registry, id string, timeout time.Duration) {
	ch, unsub := reg.Subscribe(id)
	defer unsub()
	deadline := time.After(timeout)
	for {
		select {
		case ev := <-ch:
			if ev.Type == EventDone {
				return
			}
		case <-deadline:
			t.Fatalf("no done event for %s after %v", id, timeout)
		}
	}
}

// TestRunHangingViewCancelledAtDeadline is the regression test for the views
// deadline never cancelling in-flight calls: a view that hangs must be cut
// off by the deadline, the judge must still run, and done must publish.
func TestRunHangingViewCancelledAtDeadline(t *testing.T) {
	fx := setupFixture(t, fastScripts(), "200ms", "5s")
	before := runtime.NumGoroutine()
	hanging := &hangingClient{inner: fx.fake, hang: "v-stress"}
	fx.runner.gw = hanging

	start := time.Now()
	id, err := fx.runner.Run(context.Background(), testCreateRequest(fx.thread))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	waitDone(t, fx.reg, id, 15*time.Second)
	elapsed := time.Since(start)
	if elapsed > 12*time.Second {
		t.Fatalf("run took %v, deadline did not cancel the hung view", elapsed)
	}
	waitRequestState(t, fx.db, id, "complete_partial", 10*time.Second)

	v := viewBySeat(t, fx.db, id, "stress_tester")
	if v.State != "timed_out" && v.State != "failed" {
		t.Fatalf("stress_tester state = %q, want timed_out terminal", v.State)
	}
	agg, err := fx.db.GetRequest(id)
	if err != nil {
		t.Fatalf("GetRequest: %v", err)
	}
	if agg.Request.JudgeStartedAt == nil {
		t.Fatal("judge never started")
	}
	// Goroutine-leak check: give stragglers a moment, then require the
	// registry entry to be marked done.
	deadline := time.Now().Add(10 * time.Second)
	for {
		if fx.reg.Has(id) {
			if time.Now().After(deadline) {
				break
			}
			time.Sleep(20 * time.Millisecond)
			continue
		}
		break
	}
	time.Sleep(500 * time.Millisecond)
	after := runtime.NumGoroutine()
	if after > before+15 {
		t.Fatalf("goroutines before=%d after=%d, in-flight view leaked", before, after)
	}
}

func TestResponseFormatStrictSchemaWhenSupported(t *testing.T) {
	fx := setupFixture(t, fastScripts(), "2s", "2s")
	id, err := fx.runner.Run(context.Background(), testCreateRequest(fx.thread))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	waitRequestState(t, fx.db, id, "complete", 10*time.Second)
	for _, c := range fx.fake.Calls {
		if c.ResponseFormat == nil {
			t.Fatalf("model %s: no response_format, want strict json_schema", c.Model)
		}
		if c.ResponseFormat.Type != "json_schema" || !c.ResponseFormat.Strict {
			t.Fatalf("model %s: response_format = %+v, want strict json_schema", c.Model, c.ResponseFormat)
		}
	}
}

func TestResponseFormatJSONObjectOnly(t *testing.T) {
	modelsYAML := `models:
   - id: v-poss
     family: openai
     expected_response_model_prefixes: ["v-poss"]
     supports: {temperature: true, top_p: true, reasoning_effort: true, json_schema: true, json_object: true}
   - id: v-persp
     family: openai
     expected_response_model_prefixes: ["v-persp"]
     supports: {temperature: true, top_p: true, reasoning_effort: true, json_schema: false, json_object: true}
   - id: v-stress
     family: openai
     expected_response_model_prefixes: ["v-stress"]
     supports: {temperature: true, top_p: true, reasoning_effort: true, json_schema: false, json_object: false}
   - id: j1
     family: openai
     expected_response_model_prefixes: ["j1"]
     supports: {temperature: true, top_p: true, reasoning_effort: true, json_schema: true, json_object: true}
 `
	fx := setupFixture(t, fastScripts(), "2s", "2s")
	mreg := loadRegistryForTest(t, modelsYAML)
	fx.runner.models = mreg
	id, err := fx.runner.Run(context.Background(), testCreateRequest(fx.thread))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	waitRequestState(t, fx.db, id, "complete_partial", 10*time.Second)
	byModel := map[string]*gateway.ResponseFormat{}
	for i, c := range fx.fake.Calls {
		rf := fx.fake.Calls[i].ResponseFormat
		byModel[c.Model] = rf
	}
	if byModel["v-poss"] == nil || byModel["v-poss"].Type != "json_schema" || !byModel["v-poss"].Strict {
		t.Fatalf("v-poss format = %+v, want strict json_schema", byModel["v-poss"])
	}
	if _, ok := byModel["v-persp"]; ok {
		t.Fatalf("v-persp format = %+v, want no call (fail closed without strict json_schema)", byModel["v-persp"])
	}
	if byModel["v-stress"] != nil {
		t.Fatalf("v-stress format = %+v, want absent", byModel["v-stress"])
	}
}
