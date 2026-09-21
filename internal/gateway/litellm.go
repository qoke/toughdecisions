package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/qoke/toughdecisions/internal/logx"
)

// LiteLLM is an OpenAI-compatible chat client over POST {base}/v1/chat/completions.
type LiteLLM struct {
	client *http.Client
	base   string
	key    string
	sem    chan struct{}
	log    logx.Logger
}

// New builds a LiteLLM client. maxConcurrent sizes the global semaphore
// (values < 1 are clamped to 1). The http.Client must have no Timeout;
// deadlines come only from ctx.
func New(client *http.Client, baseURL, apiKey string, maxConcurrent int, log logx.Logger) *LiteLLM {
	if maxConcurrent < 1 {
		maxConcurrent = 1
	}
	return &LiteLLM{
		client: client,
		base:   strings.TrimRight(baseURL, "/"),
		key:    apiKey,
		sem:    make(chan struct{}, maxConcurrent),
		log:    log,
	}
}

// Chat performs one non-streaming completion.
func (l *LiteLLM) Chat(ctx context.Context, req ChatRequest) (resp ChatResponse, err error) {
	start := time.Now()
	errClass := "ok"
	defer func() {
		latency := time.Since(start).Milliseconds()
		resp.LatencyMs = latency
		var cost float64
		hasCost := resp.CostUSD != nil
		if hasCost {
			cost = *resp.CostUSD
		}
		l.log.Info("gateway chat",
			"model", req.Model,
			"latency_ms", latency,
			"prompt_tokens", resp.PromptTokens,
			"completion_tokens", resp.CompletionTokens,
			"cost_usd", cost,
			"has_cost", hasCost,
			"err_class", errClass,
		)
	}()

	select {
	case l.sem <- struct{}{}:
		defer func() { <-l.sem }()
	case <-ctx.Done():
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			errClass = "timeout"
			return ChatResponse{}, fmt.Errorf("gateway: acquire semaphore: %w", ErrTimeout)
		}
		errClass = "canceled"
		return ChatResponse{}, fmt.Errorf("gateway: acquire semaphore: %w", ctx.Err())
	}

	body, err := buildBody(req)
	if err != nil {
		errClass = "request"
		return ChatResponse{}, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, l.base+"/v1/chat/completions", bytes.NewReader(body))
	if err != nil {
		errClass = "request"
		return ChatResponse{}, fmt.Errorf("gateway: build request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if l.key != "" {
		httpReq.Header.Set("Authorization", "Bearer "+l.key)
	}

	httpResp, err := l.client.Do(httpReq)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
			errClass = "timeout"
			return ChatResponse{}, fmt.Errorf("gateway: do: %w", ErrTimeout)
		}
		errClass = "transport"
		return ChatResponse{}, fmt.Errorf("gateway: do: %w: %w", ErrGateway, err)
	}
	defer httpResp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(httpResp.Body, 8<<20))
	if err != nil {
		errClass = "transport"
		return ChatResponse{}, fmt.Errorf("gateway: read body: %w: %w", ErrGateway, err)
	}

	if httpResp.StatusCode < 200 || httpResp.StatusCode > 299 {
		msg := parseErrorMessage(raw)
		errClass = "gateway"
		return ChatResponse{}, fmt.Errorf("gateway: status %d: %s: %w", httpResp.StatusCode, msg, ErrGateway)
	}

	var parsed chatCompletionResponse
	if err := json.Unmarshal(raw, &parsed); err != nil {
		errClass = "bad_json"
		return ChatResponse{}, fmt.Errorf("gateway: decode response: %w", ErrBadJSON)
	}

	resp = ChatResponse{
		ModelReturned:    parsed.Model,
		PromptTokens:     parsed.Usage.PromptTokens,
		CompletionTokens: parsed.Usage.CompletionTokens,
	}
	if len(parsed.Choices) > 0 {
		resp.Content = parsed.Choices[0].Message.Content
		resp.FinishReason = parsed.Choices[0].FinishReason
	}
	if v := httpResp.Header.Get("x-litellm-response-cost"); v != "" {
		if f, err := strconv.ParseFloat(strings.TrimSpace(v), 64); err == nil {
			resp.CostUSD = &f
		}
	}

	// Substitution check: response model and (advisory A6) the
	// x-litellm-model-id header must match one of the expected prefixes.
	if len(req.ExpectedModelPrefixes) > 0 {
		if !matchesAnyPrefix(parsed.Model, req.ExpectedModelPrefixes) {
			errClass = "substituted"
			return resp, &SubstitutionError{Response: resp, Returned: parsed.Model}
		}
		if headerID := httpResp.Header.Get("x-litellm-model-id"); headerID != "" &&
			!matchesAnyPrefix(headerID, req.ExpectedModelPrefixes) {
			errClass = "substituted"
			return resp, &SubstitutionError{Response: resp, Returned: headerID}
		}
	}
	return resp, nil
}

func matchesAnyPrefix(value string, prefixes []string) bool {
	for _, p := range prefixes {
		if len(p) > 0 && strings.HasPrefix(strings.ToLower(value), strings.ToLower(p)) {
			return true
		}
	}
	return false
}

func buildBody(req ChatRequest) ([]byte, error) {
	msgs := make([]map[string]string, 0, len(req.Messages))
	for _, m := range req.Messages {
		msgs = append(msgs, map[string]string{"role": m.Role, "content": m.Content})
	}
	body := map[string]any{
		"model":                 req.Model,
		"messages":              msgs,
		"max_completion_tokens": req.MaxOutputTokens,
		"stream":                false,
	}
	if req.Temperature != nil {
		body["temperature"] = *req.Temperature
	}
	if req.TopP != nil {
		body["top_p"] = *req.TopP
	}
	if req.ReasoningEffort != "" {
		body["reasoning_effort"] = req.ReasoningEffort
	}
	if req.ResponseFormat != nil {
		switch req.ResponseFormat.Type {
		case "json_schema":
			schema := req.ResponseFormat.Schema
			if len(schema) == 0 {
				schema = json.RawMessage(`{}`)
			}
			body["response_format"] = map[string]any{
				"type": "json_schema",
				"json_schema": map[string]any{
					"name":   req.ResponseFormat.SchemaName,
					"schema": json.RawMessage(schema),
					"strict": req.ResponseFormat.Strict,
				},
			}
		case "json_object":
			body["response_format"] = map[string]any{"type": "json_object"}
		}
	}
	if len(req.Tags) > 0 {
		body["metadata"] = req.Tags
	}
	out, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("gateway: marshal body: %w", ErrGateway)
	}
	return out, nil
}

type chatCompletionResponse struct {
	Model   string `json:"model"`
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
	} `json:"usage"`
}

func parseErrorMessage(raw []byte) string {
	var envelope struct {
		Error struct {
			Message string `json:"message"`
			Type    string `json:"type"`
			Param   string `json:"param"`
			Code    string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return "unparseable error body"
	}
	if envelope.Error.Message == "" {
		return "unknown error"
	}
	return envelope.Error.Message
}
