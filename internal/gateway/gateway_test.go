package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/qoke/toughdecisions/internal/config"
	"github.com/qoke/toughdecisions/internal/logx"
)

func testLogger(t *testing.T) logx.Logger {
	t.Helper()
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	return logx.NewWithWriter(cfg, io.Discard)
}

func TestFakeDeterministicAndRecords(t *testing.T) {
	f := NewFake(map[string][]Step{
		"m1": {{Content: "one"}, {Content: "two"}},
	})
	ctx := context.Background()
	r1, err := f.Chat(ctx, ChatRequest{Model: "m1"})
	if err != nil || r1.Content != "one" {
		t.Fatalf("step1 = %+v, %v", r1, err)
	}
	r2, err := f.Chat(ctx, ChatRequest{Model: "m1"})
	if err != nil || r2.Content != "two" {
		t.Fatalf("step2 = %+v, %v", r2, err)
	}
	if f.CallCount() != 2 || len(f.Calls) != 2 || f.Calls[0].Model != "m1" {
		t.Fatalf("calls = %+v", f.Calls)
	}
	f.SetScript("m1", []Step{{Content: "reset"}})
	r3, _ := f.Chat(ctx, ChatRequest{Model: "m1"})
	_ = r3
}

func TestFakeDelayRespectsCancel(t *testing.T) {
	f := NewFake(map[string][]Step{"m": {{Delay: 5 * time.Second, Content: "x"}}})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := f.Chat(ctx, ChatRequest{Model: "m"}); err == nil {
		t.Fatal("expected cancellation error")
	}
}

func okHandler(body string, headers map[string]string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		for k, v := range headers {
			w.Header().Set(k, v)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, body)
	}
}

func TestLiteLLMBodyOmitsEmptyAndNeverFallbacks(t *testing.T) {
	var bodies []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var m map[string]any
		_ = json.Unmarshal(raw, &m)
		bodies = append(bodies, m)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"model":"gpt-x","choices":[{"message":{"content":"hi"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":2}}`)
	}))
	defer srv.Close()
	client := New(&http.Client{}, srv.URL, "k", 4, testLogger(t))

	if _, err := client.Chat(context.Background(), ChatRequest{Model: "m", Messages: []Message{{Role: "user", Content: "hi"}}, MaxOutputTokens: 50}); err != nil {
		t.Fatalf("Chat: %v", err)
	}
	b := bodies[0]
	for _, k := range []string{"temperature", "top_p", "reasoning_effort", "fallbacks", "num_retries", "drop_params"} {
		if _, ok := b[k]; ok {
			t.Fatalf("body contains %q: %v", k, b)
		}
	}
	if _, ok := b["max_completion_tokens"]; !ok {
		t.Fatal("missing max_completion_tokens")
	}

	temp := 0.5
	top := 0.9
	_, err := client.Chat(context.Background(), ChatRequest{Model: "m", Messages: []Message{{Role: "user", Content: "hi"}},
		Temperature: &temp, TopP: &top, ReasoningEffort: "low", MaxOutputTokens: 50,
		ResponseFormat: &ResponseFormat{Type: "json_object"}})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	b2 := bodies[1]
	if b2["temperature"] != 0.5 || b2["top_p"] != 0.9 || b2["reasoning_effort"] != "low" {
		t.Fatalf("optional fields missing: %v", b2)
	}
	rf, ok := b2["response_format"].(map[string]any)
	if !ok || rf["type"] != "json_object" {
		t.Fatalf("response_format = %v", b2["response_format"])
	}
}

func TestLiteLLMResponseFormatJSONSchema(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var m map[string]any
		_ = json.Unmarshal(raw, &m)
		rf := m["response_format"].(map[string]any)
		js := rf["json_schema"].(map[string]any)
		if rf["type"] != "json_schema" || js["name"] != "view" || js["strict"] != true {
			t.Errorf("response_format = %v", m["response_format"])
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"model":"m","choices":[],"usage":{}}`)
	}))
	defer srv.Close()
	client := New(&http.Client{}, srv.URL, "k", 4, testLogger(t))
	_, err := client.Chat(context.Background(), ChatRequest{Model: "m",
		ResponseFormat: &ResponseFormat{Type: "json_schema", SchemaName: "view",
			Schema: json.RawMessage(`{"type":"object"}`), Strict: true}})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
}

func TestLiteLLMDeadlineMapsToTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(300 * time.Millisecond)
		_, _ = io.WriteString(w, `{}`)
	}))
	defer srv.Close()
	client := New(&http.Client{}, srv.URL, "k", 4, testLogger(t))
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	_, err := client.Chat(ctx, ChatRequest{Model: "m"})
	if !errors.Is(err, ErrTimeout) {
		t.Fatalf("err = %v, want ErrTimeout", err)
	}
}

func TestLiteLLMSubstitutionMismatch(t *testing.T) {
	srv := httptest.NewServer(okHandler(
		`{"model":"other-model-1","choices":[{"message":{"content":"x"},"finish_reason":"stop"}],"usage":{}}`, nil))
	defer srv.Close()
	client := New(&http.Client{}, srv.URL, "k", 4, testLogger(t))
	resp, err := client.Chat(context.Background(), ChatRequest{Model: "m", ExpectedModelPrefixes: []string{"gpt-x-"}})
	if !errors.Is(err, ErrSubstituted) {
		t.Fatalf("err = %v, want ErrSubstituted", err)
	}
	if resp.Content != "x" {
		t.Fatalf("response not attached: %+v", resp)
	}
	var sub *SubstitutionError
	if !errors.As(err, &sub) {
		t.Fatal("want *SubstitutionError")
	}
}

func TestLiteLLMHeaderSubstitution(t *testing.T) {
	// The x-litellm-model-id header is a deployment identifier, not a model
	// name, so it must never trigger substitution: a matching body model
	// succeeds even when the header does not match any expected prefix.
	srv := httptest.NewServer(okHandler(
		`{"model":"gpt-x-1","choices":[{"message":{"content":"x"},"finish_reason":"stop"}],"usage":{}}`,
		map[string]string{"x-litellm-model-id": "other-provider-model"}))
	defer srv.Close()
	client := New(&http.Client{}, srv.URL, "k", 4, testLogger(t))
	resp, err := client.Chat(context.Background(), ChatRequest{Model: "m", ExpectedModelPrefixes: []string{"gpt-x-"}})
	if err != nil {
		t.Fatalf("err = %v, want nil (header must be ignored)", err)
	}
	if resp.Content != "x" {
		t.Fatalf("content = %q, want %q", resp.Content, "x")
	}
}

func TestLiteLLMHashDeploymentIDAccepted(t *testing.T) {
	// Regression test for the live failure: real LiteLLM (1.98.0) returns a
	// 64-hex deployment hash in x-litellm-model-id. The guard must accept it
	// when the body model matches the expected prefix. Fails on the old code
	// (which prefix-matched the header and returned ErrSubstituted).
	const hashID = "527fa7e8c1d94b6a8e2f0a3c5d7b9e1f2a4c6d8e0b1f3a5c7d9e1b3a5c7d9001"
	srv := httptest.NewServer(okHandler(
		`{"model":"gpt-5.6-luna","choices":[{"message":{"content":"hi"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":2}}`,
		map[string]string{"x-litellm-model-id": hashID}))
	defer srv.Close()
	client := New(&http.Client{}, srv.URL, "k", 4, testLogger(t))
	resp, err := client.Chat(context.Background(), ChatRequest{Model: "m", ExpectedModelPrefixes: []string{"gpt-5.6-"}})
	if err != nil {
		t.Fatalf("err = %v, want nil (hash deployment id must be accepted)", err)
	}
	if resp.ModelReturned != "gpt-5.6-luna" {
		t.Fatalf("model = %q, want %q", resp.ModelReturned, "gpt-5.6-luna")
	}
}

func TestLiteLLMBodyMismatchStillRejectedWithHashHeader(t *testing.T) {
	// True positives survive: a body model that does not match the expected
	// prefix still hard-fails even when a hash deployment id is present.
	const hashID = "da890bcf1d94b6a8e2f0a3c5d7b9e1f2a4c6d8e0b1f3a5c7d9e1b3a5c7d9002"
	srv := httptest.NewServer(okHandler(
		`{"model":"other-model-1","choices":[{"message":{"content":"x"},"finish_reason":"stop"}],"usage":{}}`,
		map[string]string{"x-litellm-model-id": hashID}))
	defer srv.Close()
	client := New(&http.Client{}, srv.URL, "k", 4, testLogger(t))
	_, err := client.Chat(context.Background(), ChatRequest{Model: "m", ExpectedModelPrefixes: []string{"gpt-x-"}})
	if !errors.Is(err, ErrSubstituted) {
		t.Fatalf("err = %v, want ErrSubstituted", err)
	}
	var sub *SubstitutionError
	if !errors.As(err, &sub) {
		t.Fatal("want *SubstitutionError")
	}
	if sub.Returned != "other-model-1" {
		t.Fatalf("returned = %q, want body model %q", sub.Returned, "other-model-1")
	}
}

func TestLiteLLMMissingBodyModelFailsClosed(t *testing.T) {
	srv := httptest.NewServer(okHandler(
		`{"choices":[{"message":{"content":"x"},"finish_reason":"stop"}],"usage":{}}`, nil))
	defer srv.Close()
	client := New(&http.Client{}, srv.URL, "k", 4, testLogger(t))
	_, err := client.Chat(context.Background(), ChatRequest{Model: "m", ExpectedModelPrefixes: []string{"gpt-x-"}})
	if !errors.Is(err, ErrSubstituted) {
		t.Fatalf("err = %v, want ErrSubstituted", err)
	}
}

func TestLiteLLMBaseURLNormalization(t *testing.T) {
	const wantPath = "/v1/chat/completions"
	for _, tc := range []struct {
		name string
		base string
	}{
		{"no suffix", ""},
		{"v1 suffix", "/v1"},
		{"v1 suffix trailing slash", "/v1/"},
		{"trailing slash", "/"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var gotPath string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotPath = r.URL.Path
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, `{"model":"m","choices":[],"usage":{}}`)
			}))
			defer srv.Close()
			client := New(&http.Client{}, srv.URL+tc.base, "k", 4, testLogger(t))
			if _, err := client.Chat(context.Background(), ChatRequest{Model: "m"}); err != nil {
				t.Fatalf("Chat: %v", err)
			}
			if gotPath != wantPath {
				t.Fatalf("path = %q, want %q", gotPath, wantPath)
			}
		})
	}
}

func TestLiteLLMCostHeaderAndUsage(t *testing.T) {
	srv := httptest.NewServer(okHandler(
		`{"model":"gpt-x-1","choices":[{"message":{"content":"hi"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":7}}`,
		map[string]string{"x-litellm-response-cost": "0.004"}))
	defer srv.Close()
	client := New(&http.Client{}, srv.URL, "k", 4, testLogger(t))
	resp, err := client.Chat(context.Background(), ChatRequest{Model: "m", ExpectedModelPrefixes: []string{"GPT-X-"}})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if resp.PromptTokens != 3 || resp.CompletionTokens != 7 {
		t.Fatalf("usage = %+v", resp)
	}
	if resp.CostUSD == nil || *resp.CostUSD != 0.004 {
		t.Fatalf("cost = %+v", resp.CostUSD)
	}
}

func TestLiteLLMNon2xxMapsToGateway(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(400)
		_, _ = io.WriteString(w, `{"error":{"message":"bad model","type":"invalid","param":"model","code":"400"}}`)
	}))
	defer srv.Close()
	client := New(&http.Client{}, srv.URL, "k", 4, testLogger(t))
	_, err := client.Chat(context.Background(), ChatRequest{Model: "m"})
	if !errors.Is(err, ErrGateway) {
		t.Fatalf("err = %v, want ErrGateway", err)
	}
	if !strings.Contains(err.Error(), "bad model") {
		t.Fatalf("err missing message: %v", err)
	}
}

func TestLiteLLMMalformedJSONMapsToBadJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `not json`)
	}))
	defer srv.Close()
	client := New(&http.Client{}, srv.URL, "k", 4, testLogger(t))
	_, err := client.Chat(context.Background(), ChatRequest{Model: "m"})
	if !errors.Is(err, ErrBadJSON) {
		t.Fatalf("err = %v, want ErrBadJSON", err)
	}
}
