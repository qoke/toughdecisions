package gateway

import (
	"context"
	"testing"
)

// TestFakeSchemaRejectionConsumesNothing pins Finding 5: a strict-schema
// rejection happens before the call is recorded or a scripted step is
// consumed, like a real gateway 400. A follow-up valid call must replay the
// first scripted step, not the second.
func TestFakeSchemaRejectionConsumesNothing(t *testing.T) {
	ctx := context.Background()
	lax := `{"type":"object","properties":{"a":{"type":"string"}}}`
	f := NewFake(map[string][]Step{"m": {{Content: "first"}, {Content: "second"}}})
	bad := ChatRequest{Model: "m",
		ResponseFormat: &ResponseFormat{Type: "json_schema", SchemaName: "v", Schema: []byte(lax), Strict: true}}
	if _, err := f.Chat(ctx, bad); err == nil {
		t.Fatal("lax strict schema: want rejection")
	}
	if got := f.CallCount(); got != 0 {
		t.Fatalf("calls after rejection = %d; want 0 (rejected before recording)", got)
	}
	good := ChatRequest{Model: "m",
		ResponseFormat: &ResponseFormat{Type: "json_object"}}
	resp, err := f.Chat(ctx, good)
	if err != nil {
		t.Fatalf("valid call: %v", err)
	}
	if resp.Content != "first" {
		t.Fatalf("content = %q; want %q (rejection consumed no step)", resp.Content, "first")
	}
}
