package gateway

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"
)

// Step is one scripted model response.
type Step struct {
	Delay         time.Duration
	Content       string
	ModelReturned string
	Err           error
}

// Fake is a deterministic scripted Client for tests.
//
// By default it mirrors three real-LiteLLM behaviours the old fake omitted:
//   - every response carries a realistic 64-hex deployment-hash
//     LiteLLMModelID (the x-litellm-model-id header value), which is never a
//     model name and never participates in substitution checks;
//   - substitution is enforced on the body model (ModelReturned): when
//     ExpectedModelPrefixes is non-empty and the body model matches none, Chat
//     returns a *SubstitutionError wrapping ErrSubstituted, like the real
//     client;
//   - strict structured-output conformance is enforced: a json_schema request
//     with Strict=true whose schema has any object node (explicit
//     "type":"object", or implicit via "properties"/"required") lacking
//     "additionalProperties":false, or omitting any "properties" key from
//     "required", is rejected with a 400-style error wrapping ErrGateway,
//     mirroring OpenAI-strict 400s.
//
// Each dimension is an opt-out switch for tests deliberately exercising
// something else: SkipSubstitutionCheck, SkipStrictSchemaCheck, or
// SkipDeploymentID. The zero value (NewFake) is fully strict.
type Fake struct {
	mu      sync.Mutex
	Scripts map[string][]Step
	Calls   []ChatRequest
	taken   map[string]int

	// SkipSubstitutionCheck disables the body-model substitution guard.
	SkipSubstitutionCheck bool
	// SkipStrictSchemaCheck disables strict json_schema conformance rejection.
	SkipStrictSchemaCheck bool
	// SkipDeploymentID disables emitting the deployment-hash LiteLLMModelID.
	SkipDeploymentID bool
}

// NewFake builds a Fake with the given per-model scripts.
func NewFake(scripts map[string][]Step) *Fake {
	if scripts == nil {
		scripts = map[string][]Step{}
	}
	return &Fake{Scripts: scripts, taken: map[string]int{}}
}

// SetScript replaces the script for a model.
func (f *Fake) SetScript(model string, steps []Step) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Scripts[model] = steps
}

// Chat replays the next scripted step for req.Model and records the call.
// Delay respects ctx cancellation. Schema conformance is a transport-level
// rejection like the real gateway: it runs before the call is recorded or a
// scripted step is consumed, so a 400 consumes nothing.
func (f *Fake) Chat(ctx context.Context, req ChatRequest) (ChatResponse, error) {
	if !f.SkipStrictSchemaCheck {
		if err := checkStrictSchema(req.ResponseFormat); err != nil {
			return ChatResponse{}, err
		}
	}
	f.mu.Lock()
	f.Calls = append(f.Calls, req)
	steps := f.Scripts[req.Model]
	i := f.taken[req.Model]
	f.taken[req.Model] = i + 1
	skipSubst := f.SkipSubstitutionCheck
	skipDeploy := f.SkipDeploymentID
	f.mu.Unlock()

	if i >= len(steps) {
		model := req.Model
		resp := ChatResponse{ModelReturned: model}
		if !skipDeploy {
			resp.LiteLLMModelID = deploymentIDFor(req.Model, i)
		}
		if !skipSubst {
			if err := checkPrefixes(model, req.ExpectedModelPrefixes, resp); err != nil {
				return resp, err
			}
		}
		return resp, nil
	}
	step := steps[i]
	if step.Delay > 0 {
		timer := time.NewTimer(step.Delay)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return ChatResponse{}, ctx.Err()
		case <-timer.C:
		}
	}
	if step.Err != nil {
		model := step.ModelReturned
		if model == "" {
			model = req.Model
		}
		return ChatResponse{ModelReturned: model}, step.Err
	}
	model := step.ModelReturned
	if model == "" {
		model = req.Model
	}
	resp := ChatResponse{ModelReturned: model, Content: step.Content}
	if !skipDeploy {
		resp.LiteLLMModelID = deploymentIDFor(req.Model, i)
	}
	if !skipSubst {
		if err := checkPrefixes(model, req.ExpectedModelPrefixes, resp); err != nil {
			return resp, err
		}
	}
	return resp, nil
}

// CallCount returns the number of recorded calls.
func (f *Fake) CallCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.Calls)
}

// checkPrefixes enforces the body-model substitution guard: with a non-empty
// prefix list, an empty or non-matching body model hard-fails, mirroring the
// real client. The deployment hash never participates.
func checkPrefixes(model string, prefixes []string, resp ChatResponse) error {
	if len(prefixes) == 0 {
		return nil
	}
	if model == "" || !matchesAnyPrefix(model, prefixes) {
		return &SubstitutionError{Response: resp, Returned: model}
	}
	return nil
}

// checkStrictSchema rejects non-strict-conformant schemas the way
// OpenAI-strict deployments do: a json_schema request with Strict=true must
// have additionalProperties:false on every object node AND every key of
// "properties" listed in "required" (checked recursively). Non-schema
// requests, Strict=false, empty or unparseable schemas pass through
// untouched (the fake mirrors real behaviour, never invents rules).
func checkStrictSchema(rf *ResponseFormat) error {
	if rf == nil || rf.Type != "json_schema" || !rf.Strict {
		return nil
	}
	if len(strings.TrimSpace(string(rf.Schema))) == 0 {
		return nil
	}
	var v any
	if err := json.Unmarshal(rf.Schema, &v); err != nil {
		return nil
	}
	if path := firstNonStrictObject(v); path != "" {
		return fmt.Errorf("gateway: status 400: schema at %s: object schemas require additionalProperties=false in strict mode: %w", path, ErrGateway)
	}
	if path := firstMissingRequired(v); path != "" {
		return fmt.Errorf("gateway: status 400: schema at %s: 'required' is required to be supplied and to be an array including every key in properties in strict mode: %w", path, ErrGateway)
	}
	return nil
}

// firstNonStrictObject returns the path of the first object node (explicit
// type object, or implicit via properties/required) lacking
// additionalProperties:false, or "" when the schema is strict-conformant.
func firstNonStrictObject(v any) string {
	return walkStrict(v, "$")
}

// walkStrict depth-first walks a decoded schema.
func walkStrict(v any, path string) string {
	switch n := v.(type) {
	case map[string]any:
		if isStrictObjectNode(n) {
			ap, ok := n["additionalProperties"]
			if !ok {
				return path
			}
			if b, ok := ap.(bool); !ok || b {
				return path
			}
		}
		for k, child := range n {
			if p := walkStrict(child, path+"."+k); p != "" {
				return p
			}
		}
	case []any:
		for idx, child := range n {
			if p := walkStrict(child, fmt.Sprintf("%s[%d]", path, idx)); p != "" {
				return p
			}
		}
	}
	return ""
}

// isStrictObjectNode reports whether a decoded schema map is an object node:
// explicit "type":"object", or implicit via "properties" or "required".
func isStrictObjectNode(n map[string]any) bool {
	if typ, ok := n["type"].(string); ok && typ == "object" {
		return true
	}
	if _, ok := n["properties"]; ok {
		return true
	}
	if _, ok := n["required"]; ok {
		return true
	}
	return false
}

// firstMissingRequired returns the path of the first object node whose
// "properties" keys are not all listed in "required", or "" when every
// object node satisfies the strict required-superset rule.
func firstMissingRequired(v any) string {
	return walkRequired(v, "$")
}

// walkRequired depth-first walks a decoded schema.
func walkRequired(v any, path string) string {
	switch n := v.(type) {
	case map[string]any:
		if props, ok := n["properties"].(map[string]any); ok && isStrictObjectNode(n) {
			have := map[string]bool{}
			if existing, ok := n["required"].([]any); ok {
				for _, r := range existing {
					if s, ok := r.(string); ok {
						have[s] = true
					}
				}
			}
			for name := range props {
				if !have[name] {
					return path
				}
			}
		}
		for k, child := range n {
			if p := walkRequired(child, path+"."+k); p != "" {
				return p
			}
		}
	case []any:
		for idx, child := range n {
			if p := walkRequired(child, fmt.Sprintf("%s[%d]", path, idx)); p != "" {
				return p
			}
		}
	}
	return ""
}

// deploymentIDFor derives a deterministic 64-hex deployment hash from the
// model key and call index, mimicking the opaque LiteLLM deployment ids.
func deploymentIDFor(model string, index int) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s#%d", model, index)))
	return hex.EncodeToString(sum[:])
}

// IsHexDeploymentID reports whether s looks like a real x-litellm-model-id:
// exactly 64 lowercase hex chars.
func IsHexDeploymentID(s string) bool {
	if len(s) != 64 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= '0' && c <= '9' || c >= 'a' && c <= 'f' {
			continue
		}
		return false
	}
	return true
}
