package harness

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/qoke/toughdecisions/internal/gateway"
	"github.com/qoke/toughdecisions/internal/hash"
	"github.com/qoke/toughdecisions/internal/ids"
	"github.com/qoke/toughdecisions/internal/models"
	"github.com/qoke/toughdecisions/internal/pack"
	"github.com/qoke/toughdecisions/internal/prompts"
	"github.com/qoke/toughdecisions/internal/schema"
	"github.com/qoke/toughdecisions/internal/store"
)

// GenRequest is one response-generation call: the seat config, the input
// snapshot, and (for the judge seat) the baseline bundle. Repetition is
// resolved by the helper: 0 unless Fresh, which uses MaxRepetition()+1.
type GenRequest struct {
	Seat      pack.Seat
	SeatCfg   pack.SeatConfig
	Input     schema.CaseInput
	InputHash string
	Bundle    *store.Bundle
	RunID     string
	Fresh     bool
}

// GenResult is the stored response plus whether it came from the cache.
type GenResult struct {
	Response *store.Response
	CacheHit bool
}

// GenerateResponse builds the seat prompt, calls gateway.Client, and stores
// the response keyed by store.ResponseCacheKey. On a cache hit it reuses
// the stored row and makes no model call. A timeout is stored as a FAILED
// (timed_out) response, counted in stats, and never retried. A concurrent
// duplicate-key insert reuses the existing row (A2) rather than erroring.
// Unsupported sampling/reasoning settings are rejected, never dropped.
func (r *Runner) GenerateResponse(ctx context.Context, g GenRequest) (*GenResult, error) {
	if !g.Seat.Valid() {
		return nil, fmt.Errorf("harness: unknown seat %q", string(g.Seat))
	}
	if err := r.models.ValidateSettings(g.SeatCfg); err != nil {
		return nil, err
	}
	var bundleHash *string
	if g.Bundle != nil {
		bundleHash = &g.Bundle.BundleHash
	}
	cfgHash := g.SeatCfg.Hash()
	packHash := prompts.PromptPackHash()

	repetition := 0
	if g.Fresh {
		max, err := r.db.MaxRepetition(string(g.Seat), cfgHash, packHash, g.InputHash, bundleHash)
		if err != nil {
			return nil, err
		}
		repetition = max + 1
	}
	key := store.ResponseCacheKey(string(g.Seat), cfgHash, packHash, g.InputHash, bundleHash, repetition)
	if existing, err := r.db.GetResponseByCacheKey(key); err == nil {
		r.stats.CacheHits.Add(1)
		return &GenResult{Response: existing, CacheHit: true}, nil
	} else if !errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("harness: cache lookup: %w", err)
	}

	msgs, err := buildSeatPrompt(g.Seat, g.SeatCfg, g.Input, g.Bundle)
	if err != nil {
		return nil, err
	}
	gwMsgs := make([]gateway.Message, 0, len(msgs))
	for _, m := range msgs {
		gwMsgs = append(gwMsgs, gateway.Message{Role: m.Role, Content: m.Content})
	}
	callCtx, cancel := context.WithDeadline(ctx, time.Now().Add(r.DeadlineFor(g.Seat)))
	defer cancel()

	r.stats.Calls.Add(1)
	t0 := time.Now()
	chatResp, chatErr := r.gw.Chat(callCtx, gateway.ChatRequest{
		Model:                 g.SeatCfg.Model,
		Messages:              gwMsgs,
		Temperature:           g.SeatCfg.Temperature,
		TopP:                  g.SeatCfg.TopP,
		MaxOutputTokens:       nonZeroMaxTokens(g.SeatCfg, g.Seat),
		ReasoningEffort:       g.SeatCfg.ReasoningEffort,
		ResponseFormat:        responseFormatFor(r.models, g.SeatCfg.Model, g.Seat),
		ExpectedModelPrefixes: r.models.ExpectedPrefixes(g.SeatCfg.Model),
		Tags:                  map[string]string{"run_id": g.RunID, "seat": string(g.Seat)},
	})
	latency := time.Since(t0).Milliseconds()

	if chatErr != nil {
		return r.storeFailed(ctx, g, key, repetition, bundleHash, cfgHash, packHash, chatErr, latency)
	}
	return r.storeSuccess(ctx, g, key, repetition, bundleHash, cfgHash, packHash, chatResp, latency)
}

// buildSeatPrompt assembles view or judge messages. The judge seat is shown
// the baseline bundle's natural views; view seats use the case input.
func buildSeatPrompt(seat pack.Seat, cfg pack.SeatConfig, in schema.CaseInput, bundle *store.Bundle) ([]gateway.Message, error) {
	if seat == pack.SeatJudge {
		views := map[string]schema.RenderedView{}
		if bundle != nil && strings.TrimSpace(bundle.ViewsJSON) != "" && bundle.ViewsJSON != "{}" {
			var raw map[string]schema.RenderedView
			if err := json.Unmarshal([]byte(bundle.ViewsJSON), &raw); err != nil {
				return nil, fmt.Errorf("harness: decode bundle views: %w", err)
			}
			views = raw
		}
		return prompts.BuildJudgeWithOverride(in, views, nil, len(views) == 0, cfg.RolePromptOverride)
	}
	return prompts.BuildViewWithOverride(string(seat), cfg.RolePromptOverride, in)
}

// storeFailed stores a FAILED response for a gateway error. Timeouts —
// including context deadlines, which carry no retry — are marked timed_out
// and counted. Substitution is a failure, never silent. Nothing is retried.
func (r *Runner) storeFailed(_ context.Context, g GenRequest, key string, repetition int, bundleHash *string, cfgHash, packHash string, chatErr error, latency int64) (*GenResult, error) {
	timedOut := errors.Is(chatErr, gateway.ErrTimeout) || errors.Is(chatErr, context.DeadlineExceeded)
	substituted := errors.Is(chatErr, gateway.ErrSubstituted)
	if timedOut {
		r.stats.Timeouts.Add(1)
	} else {
		r.stats.Failures.Add(1)
	}
	var returned string
	content := ""
	var subErr *gateway.SubstitutionError
	if substituted && errors.As(chatErr, &subErr) {
		returned = subErr.Returned
		content = subErr.Response.Content
		if returned == "" {
			returned = subErr.Response.ModelReturned
		}
	}
	row := &store.Response{
		ID: ids.NewID(), CacheKey: key, Seat: string(g.Seat),
		ConfigHash: cfgHash, PromptPackHash: packHash, InputHash: g.InputHash,
		BundleHash: bundleHash, Repetition: repetition, Origin: "harness",
		RunID: strPtr(g.RunID), ModelRequested: g.SeatCfg.Model,
		ModelReturned: returned, Substituted: substituted,
		RawText: content, ParseOK: false, LatencyMs: latency,
		TimedOut: timedOut, Error: strPtr(chatErr.Error()),
	}
	stored, err := r.insertOrReuse(row)
	if err != nil {
		return nil, err
	}
	return &GenResult{Response: stored}, nil
}

// storeSuccess parses and stores a successful model response.
func (r *Runner) storeSuccess(_ context.Context, g GenRequest, key string, repetition int, bundleHash *string, cfgHash, packHash string, chatResp gateway.ChatResponse, latency int64) (*GenResult, error) {
	content := chatResp.Content
	returned := chatResp.ModelReturned
	// The scripted fake does not enforce substitution; check expected
	// prefixes so a wrong model is a failure, never a silent substitution.
	if !matchesPrefix(returned, r.models.ExpectedPrefixes(g.SeatCfg.Model)) {
		subErr := &gateway.SubstitutionError{
			Response: gateway.ChatResponse{ModelReturned: returned, Content: content},
			Returned: returned,
		}
		return r.storeFailed(context.Background(), g, key, repetition, bundleHash, cfgHash, packHash, subErr, latency)
	}
	var parsedJSON *string
	parseOK := false
	if g.Seat == pack.SeatJudge {
		if judge, ok := schema.ParseLenient[schema.Judge](content); ok {
			raw, _ := json.Marshal(judge)
			s := string(raw)
			parsedJSON = &s
			parseOK = true
		}
	} else {
		if view, ok := schema.ParseLenient[schema.View](content); ok {
			raw, _ := json.Marshal(view)
			s := string(raw)
			parsedJSON = &s
			parseOK = true
		}
	}
	row := &store.Response{
		ID: ids.NewID(), CacheKey: key, Seat: string(g.Seat),
		ConfigHash: cfgHash, PromptPackHash: packHash, InputHash: g.InputHash,
		BundleHash: bundleHash, Repetition: repetition, Origin: "harness",
		RunID: strPtr(g.RunID), ModelRequested: g.SeatCfg.Model,
		ModelReturned: returned, RawText: content, ParsedJSON: parsedJSON,
		ParseOK:      parseOK,
		PromptTokens: chatResp.PromptTokens, CompletionTokens: chatResp.CompletionTokens,
		CostUSD: chatResp.CostUSD, LatencyMs: latency,
		WordCount: len(strings.Fields(content)),
	}
	stored, err := r.insertOrReuse(row)
	if err != nil {
		return nil, err
	}
	return &GenResult{Response: stored}, nil
}

// insertOrReuse inserts a response row, resolving a concurrent duplicate
// cache key deterministically (A2): when the insert hits the UNIQUE
// constraint, the existing row is returned and the error is swallowed.
func (r *Runner) insertOrReuse(row *store.Response) (*store.Response, error) {
	stored, err := r.db.InsertResponse(row)
	if err == nil {
		return stored, nil
	}
	if existing, gerr := r.db.GetResponseByCacheKey(row.CacheKey); gerr == nil {
		r.stats.CacheHits.Add(1)
		return existing, nil
	}
	return nil, fmt.Errorf("harness: store response: %w", err)
}

// nonZeroMaxTokens applies the seat default when MaxOutputTokens is 0.
func nonZeroMaxTokens(cfg pack.SeatConfig, seat pack.Seat) int {
	if cfg.MaxOutputTokens != 0 {
		return cfg.MaxOutputTokens
	}
	return pack.DefaultMaxOutputTokens(seat)
}

// responseFormatFor selects strict json_schema when supported, else
// json_object when supported, else nil.
func responseFormatFor(m *models.Registry, model string, seat pack.Seat) *gateway.ResponseFormat {
	_, _, _, jsonSchema, jsonObject := m.Supports(model)
	name := "view"
	schemaJSON := schema.ViewJSONSchema
	if seat == pack.SeatJudge {
		name = "judge"
		schemaJSON = schema.JudgeJSONSchema
	}
	switch {
	case jsonSchema:
		return &gateway.ResponseFormat{
			Type: "json_schema", SchemaName: name,
			Schema: json.RawMessage(schemaJSON), Strict: true,
		}
	case jsonObject:
		return &gateway.ResponseFormat{Type: "json_object"}
	default:
		return nil
	}
}

func matchesPrefix(value string, prefixes []string) bool {
	if len(prefixes) == 0 {
		return true
	}
	lower := strings.ToLower(value)
	for _, p := range prefixes {
		if p != "" && strings.HasPrefix(lower, strings.ToLower(p)) {
			return true
		}
	}
	return false
}

// InputHashFor returns the canonical input hash for a case input snapshot.
func InputHashFor(in schema.CaseInput) string {
	return hash.SHA256Hex(hash.CanonicalJSON(in))
}

// CaseInputFor decodes a stored case InputJSON into a CaseInput.
func CaseInputFor(c *store.Case) (schema.CaseInput, error) {
	var in schema.CaseInput
	if err := json.Unmarshal([]byte(c.InputJSON), &in); err != nil {
		return schema.CaseInput{}, fmt.Errorf("harness: decode case %q input: %w", c.CaseKey, err)
	}
	return in, nil
}

func strPtr(s string) *string { return &s }
