package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/qoke/toughdecisions/internal/schema"
)

// TestFakeEmitsHexDeploymentID pins (a): every fake response carries a
// realistic 64-hex deployment-hash LiteLLMModelID, like real LiteLLM's
// x-litellm-model-id header, which is never a model name.
func TestFakeEmitsHexDeploymentID(t *testing.T) {
	f := NewFake(map[string][]Step{"m": {{Content: "one"}, {Content: "two"}}})
	ctx := context.Background()
	for _, want := range []string{"one", "two"} {
		resp, err := f.Chat(ctx, ChatRequest{Model: "m"})
		if err != nil {
			t.Fatalf("Chat: %v", err)
		}
		if resp.Content != want {
			t.Fatalf("content = %q, want %q", resp.Content, want)
		}
		if !IsHexDeploymentID(resp.LiteLLMModelID) {
			t.Fatalf("LiteLLMModelID = %q, want 64-hex deployment hash", resp.LiteLLMModelID)
		}
		if matchesAnyPrefix(resp.LiteLLMModelID, []string{"m"}) {
			t.Fatalf("LiteLLMModelID = %q matches model prefix; must never look like a model name", resp.LiteLLMModelID)
		}
	}
	// Exhausted script path emits it too.
	resp, err := f.Chat(ctx, ChatRequest{Model: "m"})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if !IsHexDeploymentID(resp.LiteLLMModelID) {
		t.Fatalf("exhausted LiteLLMModelID = %q, want 64-hex", resp.LiteLLMModelID)
	}
	// Opt-out for tests deliberately ignoring deployment metadata.
	plain := NewFake(map[string][]Step{"m": {{Content: "x"}}})
	plain.SkipDeploymentID = true
	resp, err = plain.Chat(ctx, ChatRequest{Model: "m"})
	if err != nil || resp.LiteLLMModelID != "" {
		t.Fatalf("opt-out = %+v, %v; want empty LiteLLMModelID", resp, err)
	}
}

// TestFakeEnforcesSubstitutionOnBodyModel pins (a): the fake enforces the
// substitution guard on the body model, ignoring the deployment hash — so a
// green suite proves callers check the right field. A matching body model
// passes despite the hash never matching any prefix.
func TestFakeEnforcesSubstitutionOnBodyModel(t *testing.T) {
	ctx := context.Background()
	f := NewFake(map[string][]Step{
		"m": {{Content: "ok", ModelReturned: "gpt-x-1"}, {Content: "bad", ModelReturned: "other-model"}},
	})
	resp, err := f.Chat(ctx, ChatRequest{Model: "m", ExpectedModelPrefixes: []string{"gpt-x-"}})
	if err != nil {
		t.Fatalf("matching body model rejected: %v", err)
	}
	if resp.Content != "ok" || resp.ModelReturned != "gpt-x-1" {
		t.Fatalf("resp = %+v, want the matching body model", resp)
	}
	resp, err = f.Chat(ctx, ChatRequest{Model: "m", ExpectedModelPrefixes: []string{"gpt-x-"}})
	if !errors.Is(err, ErrSubstituted) {
		t.Fatalf("err = %v, want ErrSubstituted", err)
	}
	var sub *SubstitutionError
	if !errors.As(err, &sub) {
		t.Fatal("want *SubstitutionError")
	}
	if sub.Returned != "other-model" || sub.Response.Content != "bad" {
		t.Fatalf("sub = %+v, want body model + usable response attached", sub)
	}
	// Empty prefixes: no guard, wrong model passes through.
	f2 := NewFake(map[string][]Step{"m": {{Content: "x", ModelReturned: "other-model"}}})
	if _, err := f2.Chat(ctx, ChatRequest{Model: "m"}); err != nil {
		t.Fatalf("empty prefixes rejected: %v", err)
	}
	// Opt-out for tests deliberately exercising substitution downstream.
	f3 := NewFake(map[string][]Step{"m": {{Content: "x", ModelReturned: "other-model"}}})
	f3.SkipSubstitutionCheck = true
	if _, err := f3.Chat(ctx, ChatRequest{Model: "m", ExpectedModelPrefixes: []string{"gpt-x-"}}); err != nil {
		t.Fatalf("opt-out rejected: %v", err)
	}
}

// TestFakeRejectsNonStrictSchema pins (b): a strict json_schema request whose
// schema has any object node lacking additionalProperties:false is rejected
// with a 400-style ErrGateway, mirroring OpenAI-strict deployments.
func TestFakeRejectsNonStrictSchema(t *testing.T) {
	ctx := context.Background()
	lax := json.RawMessage(`{"type":"object","additionalProperties":false,"required":["a"],"properties":{"a":{"type":"object","additionalProperties":false,"required":["b"],"properties":{"b":{"type":"string"}}}}}`)
	missingAP := json.RawMessage(`{"type":"object","properties":{"a":{"type":"object","properties":{"b":{"type":"string"}}}}}`)
	newReq := func(s json.RawMessage, strictMode bool) ChatRequest {
		return ChatRequest{Model: "m",
			ResponseFormat: &ResponseFormat{Type: "json_schema", SchemaName: "v", Schema: s, Strict: strictMode}}
	}
	f := NewFake(map[string][]Step{"m": {{Content: "x"}, {Content: "x"}, {Content: "x"}, {Content: "x"}, {Content: "x"}}})

	// Nested object missing additionalProperties:false is rejected.
	_, err := f.Chat(ctx, newReq(missingAP, true))
	if !errors.Is(err, ErrGateway) {
		t.Fatalf("err = %v, want ErrGateway", err)
	}
	if !strings.Contains(err.Error(), "400") {
		t.Fatalf("err = %v, want 400-style message", err)
	}
	// Strict-conformant schema passes.
	if _, err := f.Chat(ctx, newReq(lax, true)); err != nil {
		t.Fatalf("conformant schema rejected: %v", err)
	}
	// Strict=false passes lax schemas through (mirrors real behaviour).
	if _, err := f.Chat(ctx, newReq(missingAP, false)); err != nil {
		t.Fatalf("non-strict request rejected: %v", err)
	}
	// Non-schema formats are untouched.
	f2 := NewFake(map[string][]Step{"m": {{Content: "x"}}})
	if _, err := f2.Chat(ctx, ChatRequest{Model: "m", ResponseFormat: &ResponseFormat{Type: "json_object"}}); err != nil {
		t.Fatalf("json_object rejected: %v", err)
	}
	// Opt-out for tests deliberately sending lax schemas.
	f3 := NewFake(map[string][]Step{"m": {{Content: "x"}}})
	f3.SkipStrictSchemaCheck = true
	if _, err := f3.Chat(ctx, newReq(lax, true)); err != nil {
		t.Fatalf("opt-out rejected: %v", err)
	}
}

// TestFakeRejectsMissingRequired pins the strict required-superset rule in
// the transport gate: a strict json_schema request whose object node omits a
// "properties" key from "required" (the live "Missing 'caution'" 400) is
// rejected with a 400-style ErrGateway. Reverting the fake's required check
// lets the D1 regression through offline.
func TestFakeRejectsMissingRequired(t *testing.T) {
	ctx := context.Background()
	missingCaution := json.RawMessage(`{"type":"object","additionalProperties":false,"required":["present"],"properties":{"present":{"type":"boolean"},"caution":{"type":"string"}}}`)
	full := json.RawMessage(`{"type":"object","additionalProperties":false,"required":["present","caution"],"properties":{"present":{"type":"boolean"},"caution":{"type":"string"}}}`)
	newReq := func(s json.RawMessage, strictMode bool) ChatRequest {
		return ChatRequest{Model: "m",
			ResponseFormat: &ResponseFormat{Type: "json_schema", SchemaName: "v", Schema: s, Strict: strictMode}}
	}
	f := NewFake(map[string][]Step{"m": {{Content: "x"}, {Content: "x"}}})
	if _, err := f.Chat(ctx, newReq(missingCaution, true)); !errors.Is(err, ErrGateway) {
		t.Fatalf("err = %v, want ErrGateway for missing required", err)
	} else if !strings.Contains(err.Error(), "400") {
		t.Fatalf("err = %v, want 400-style message", err)
	}
	if _, err := f.Chat(ctx, newReq(full, true)); err != nil {
		t.Fatalf("full required set rejected: %v", err)
	}
}

// TestFakeAcceptsProductionSchemas pins (b) end-to-end: the real view/judge
// schemas pass the fake's strict gate. Reverting the schema normalization
// fix makes this fail offline instead of 400ing every real call.
func TestFakeAcceptsProductionSchemas(t *testing.T) {
	ctx := context.Background()
	for name, s := range map[string]string{"view": schema.ViewJSONSchema, "judge": schema.JudgeJSONSchema} {
		f := NewFake(map[string][]Step{"m": {{Content: "x"}}})
		_, err := f.Chat(ctx, ChatRequest{Model: "m",
			ResponseFormat: &ResponseFormat{Type: "json_schema", SchemaName: name,
				Schema: json.RawMessage(s), Strict: true}})
		if err != nil {
			t.Fatalf("%s production schema rejected: %v", name, err)
		}
	}
}
