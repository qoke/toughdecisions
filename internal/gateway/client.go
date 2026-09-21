// Package gateway provides the LiteLLM chat client and a scripted fake.
package gateway

import (
	"context"
	"encoding/json"
	"errors"
)

// Message is a single chat message ("system" | "user").
type Message struct {
	Role    string
	Content string
}

// ResponseFormat selects the response format; pass through verbatim.
type ResponseFormat struct {
	Type       string
	SchemaName string
	Schema     json.RawMessage
	Strict     bool
}

// ChatRequest is a single non-streaming chat completion request.
type ChatRequest struct {
	Model                 string
	Messages              []Message
	Temperature           *float64
	TopP                  *float64
	MaxOutputTokens       int
	ReasoningEffort       string
	ResponseFormat        *ResponseFormat
	ExpectedModelPrefixes []string
	Tags                  map[string]string
}

// ChatResponse is the parsed result of a chat completion.
type ChatResponse struct {
	ModelReturned    string
	Content          string
	FinishReason     string
	PromptTokens     int
	CompletionTokens int
	CostUSD          *float64
	LatencyMs        int64
	// LiteLLMModelID carries the x-litellm-model-id header value: on real
	// LiteLLM a 64-hex deployment hash, never a model name. Diagnostic only;
	// substitution checks must use ModelReturned (the body model).
	LiteLLMModelID string
}

// Sentinel errors; wrap with %w and branch with errors.Is.
var (
	ErrTimeout            = errors.New("gateway: timeout")
	ErrSubstituted        = errors.New("gateway: substituted model")
	ErrUnsupportedSetting = errors.New("gateway: unsupported setting")
	ErrBadJSON            = errors.New("gateway: bad JSON")
	ErrGateway            = errors.New("gateway: error")
)

// SubstitutionError carries the usable response when the model was substituted.
type SubstitutionError struct {
	Response ChatResponse
	Returned string
}

func (e *SubstitutionError) Error() string {
	return "gateway: substituted model: " + e.Returned
}

// Unwrap lets errors.Is(err, ErrSubstituted) succeed.
func (e *SubstitutionError) Unwrap() error { return ErrSubstituted }

// Client performs a chat completion.
type Client interface {
	Chat(ctx context.Context, req ChatRequest) (ChatResponse, error)
}
