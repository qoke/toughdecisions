// Package models loads config/models.yaml and validates seat settings
// against model capabilities. Unsupported settings are rejected, never dropped.
package models

import (
	"fmt"
	"os"

	"github.com/qoke/toughdecisions/internal/gateway"
	"github.com/qoke/toughdecisions/internal/pack"
	"gopkg.in/yaml.v3"
)

// Supports holds the per-model capability flags.
type Supports struct {
	Temperature     bool `yaml:"temperature"`
	TopP            bool `yaml:"top_p"`
	ReasoningEffort bool `yaml:"reasoning_effort"`
	JSONSchema      bool `yaml:"json_schema"`
	JSONObject      bool `yaml:"json_object"`
}

// Capability is one model entry in models.yaml.
type Capability struct {
	ID               string   `yaml:"id"`
	Family           string   `yaml:"family"`
	ExpectedPrefixes []string `yaml:"expected_response_model_prefixes"`
	Supports         Supports `yaml:"supports"`
}

// Registry is the loaded model capability set keyed by model id.
type Registry struct {
	byID map[string]Capability
}

type fileShape struct {
	Models []Capability `yaml:"models"`
}

// LoadRegistry parses path, validates unique ids, and returns the registry.
// Malformed YAML or duplicate ids are errors.
func LoadRegistry(path string) (*Registry, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("models: read %s: %w", path, err)
	}
	var f fileShape
	if err := yaml.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("models: parse %s: %w", path, err)
	}
	r := &Registry{byID: make(map[string]Capability, len(f.Models))}
	for _, c := range f.Models {
		if c.ID == "" {
			return nil, fmt.Errorf("models: entry with empty id in %s", path)
		}
		if _, dup := r.byID[c.ID]; dup {
			return nil, fmt.Errorf("models: duplicate model id %q in %s", c.ID, path)
		}
		r.byID[c.ID] = c
	}
	return r, nil
}

// ValidateSettings rejects unknown model ids and SET parameters the model
// does not support. A nil pointer / empty string counts as unset.
// MaxOutputTokens is always allowed. Errors wrap gateway.ErrUnsupportedSetting.
func (r *Registry) ValidateSettings(sc pack.SeatConfig) error {
	cap, ok := r.byID[sc.Model]
	if !ok {
		return fmt.Errorf("models: unknown model %q: %w", sc.Model, gateway.ErrUnsupportedSetting)
	}
	if sc.Temperature != nil && !cap.Supports.Temperature {
		return fmt.Errorf("models: model %q does not support %q: %w", sc.Model, "temperature", gateway.ErrUnsupportedSetting)
	}
	if sc.TopP != nil && !cap.Supports.TopP {
		return fmt.Errorf("models: model %q does not support %q: %w", sc.Model, "top_p", gateway.ErrUnsupportedSetting)
	}
	if sc.ReasoningEffort != "" && !cap.Supports.ReasoningEffort {
		return fmt.Errorf("models: model %q does not support %q: %w", sc.Model, "reasoning_effort", gateway.ErrUnsupportedSetting)
	}
	return nil
}

// Known reports whether modelID exists in the registry.
func (r *Registry) Known(modelID string) bool {
	_, ok := r.byID[modelID]
	return ok
}

// ExpectedPrefixes returns the expected response-model prefixes for a model id,
// or nil when the model is unknown.
func (r *Registry) ExpectedPrefixes(modelID string) []string {
	cap, ok := r.byID[modelID]
	if !ok {
		return nil
	}
	return cap.ExpectedPrefixes
}

// Supports returns the capability flags for a model id.
// Unknown models report all-false.
func (r *Registry) Supports(modelID string) (temperature, topP, reasoningEffort, jsonSchema, jsonObject bool) {
	cap, ok := r.byID[modelID]
	if !ok {
		return false, false, false, false, false
	}
	s := cap.Supports
	return s.Temperature, s.TopP, s.ReasoningEffort, s.JSONSchema, s.JSONObject
}
