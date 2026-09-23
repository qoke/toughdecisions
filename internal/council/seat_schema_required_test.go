package council

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/qoke/toughdecisions/internal/gateway"
	"github.com/qoke/toughdecisions/internal/pack"
)

// TestViewFailsClosedWithoutStrictSchema is the R-03 proof: a seat model
// without strict json_schema support fails with reason unsupported before
// any gateway call.
func TestViewFailsClosedWithoutStrictSchema(t *testing.T) {
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
    supports: {temperature: true, top_p: true, reasoning_effort: true, json_schema: true, json_object: true}
  - id: j1
    family: openai
    expected_response_model_prefixes: ["j1"]
    supports: {temperature: true, top_p: true, reasoning_effort: true, json_schema: true, json_object: true}
`
	fx := setupFixture(t, fastScripts(), "2s", "2s")
	fx.runner.models = loadRegistryForTest(t, modelsYAML)
	before := fx.fake.CallCount()
	id, err := fx.runner.Run(context.Background(), testCreateRequest(fx.thread))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	waitRequestState(t, fx.db, id, "complete_partial", 10*time.Second)
	v := viewBySeat(t, fx.db, id, "perspective")
	if v.State != "failed" {
		t.Fatalf("perspective state = %q, want failed", v.State)
	}
	if v.ResponseID != nil {
		t.Fatal("fail-closed view must not store a response row")
	}
	for _, c := range fx.fake.Calls {
		if c.Model == "v-persp" {
			t.Fatalf("v-persp called the gateway %d times; want 0", fx.fake.CallCount()-before)
		}
	}
}

// TestJudgeFailsClosedWithoutStrictSchema proves the judge seat checks the
// same gate before its gateway call.
func TestJudgeFailsClosedWithoutStrictSchema(t *testing.T) {
	modelsYAML := `models:
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
    supports: {temperature: true, top_p: true, reasoning_effort: true, json_schema: false, json_object: true}
`
	fx := setupFixture(t, fastScripts(), "2s", "2s")
	fx.runner.models = loadRegistryForTest(t, modelsYAML)
	id, err := fx.runner.Run(context.Background(), testCreateRequest(fx.thread))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	waitRequestState(t, fx.db, id, "complete_partial", 10*time.Second)
	agg, err := fx.db.GetRequest(id)
	if err != nil {
		t.Fatalf("GetRequest: %v", err)
	}
	if agg.Judge.State != "failed" {
		t.Fatalf("judge state = %q, want failed", agg.Judge.State)
	}
	if agg.Judge.ResponseID != nil {
		t.Fatal("fail-closed judge must not store a response row")
	}
	for _, c := range fx.fake.Calls {
		if c.Model == "j1" {
			t.Fatal("j1 called the gateway; want 0 calls")
		}
	}
}

// TestResponseFormatHasNoJSONObjectFallback pins the removed branch: a
// json_object-only model gets nil, never json_object.
func TestResponseFormatHasNoJSONObjectFallback(t *testing.T) {
	modelsYAML := `models:
  - id: only-object
    family: openai
    expected_response_model_prefixes: ["only-object"]
    supports: {temperature: true, top_p: true, reasoning_effort: true, json_schema: false, json_object: true}
`
	p := filepath.Join(t.TempDir(), "models-cap.yaml")
	if err := os.WriteFile(p, []byte(modelsYAML), 0o644); err != nil {
		t.Fatalf("write models: %v", err)
	}
	fx := setupFixture(t, fastScripts(), "2s", "2s")
	_ = fx
	mreg := loadRegistryForTest(t, modelsYAML)
	if requiresStrictSchema(mreg, "only-object") != true {
		t.Fatal("requiresStrictSchema(only-object) = false, want true")
	}
	if requiresStrictSchema(nil, "only-object") != true {
		t.Fatal("requiresStrictSchema(nil) = false, want fail closed")
	}
	r := &Runner{models: mreg}
	if got := r.responseFormatFor("only-object", "view", "{}"); got != nil {
		t.Fatalf("responseFormatFor = %+v, want nil", got)
	}
	if got := r.responseFormatFor("unknown-model", "view", "{}"); got != nil {
		t.Fatalf("responseFormatFor unknown = %+v, want nil", got)
	}
	rNil := &Runner{}
	if got := rNil.responseFormatFor("only-object", "view", "{}"); got != nil {
		t.Fatalf("nil-registry responseFormatFor = %+v, want nil", got)
	}
}

// TestRewriteIgnoresStrictSchemaGate pins R-04: the rewrite path keeps its
// ValidateSettings check and makes exactly one gateway call.
func TestRewriteIgnoresStrictSchemaGate(t *testing.T) {
	modelsYAML := `models:
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
    supports: {temperature: true, top_p: true, reasoning_effort: true, json_schema: false, json_object: true}
`
	scripts := fastScripts()
	scripts["j1"] = []gateway.Step{
		{Content: testJudgeJSON, ModelReturned: "j1"},
		{Content: "rewritten text", ModelReturned: "j1"},
	}
	_ = pack.SeatJudge
	fx := setupFixture(t, scripts, "2s", "2s")
	fx.runner.models = loadRegistryForTest(t, modelsYAML)
	id, err := fx.runner.Run(context.Background(), testCreateRequest(fx.thread))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	waitRequestState(t, fx.db, id, "complete_partial", 10*time.Second)
	before := fx.fake.CallCount()
	text, rwID, err := fx.runner.Rewrite(context.Background(), id, "view:possibility", "make it shorter")
	if err != nil {
		t.Fatalf("Rewrite: %v", err)
	}
	if rwID == "" {
		t.Fatalf("Rewrite = %q with empty id", text)
	}
	if text != testJudgeJSON {
		t.Fatalf("Rewrite = %q, want the scripted judge text (rewrite unaffected by the schema gate)", text)
	}
	if got := fx.fake.CallCount() - before; got != 1 {
		t.Fatalf("rewrite gateway calls = %d, want exactly 1", got)
	}
}
