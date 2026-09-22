package models

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/qoke/toughdecisions/internal/gateway"
	"github.com/qoke/toughdecisions/internal/pack"
)

const testModelsYAML = `models:
  - id: council-gpt-x
    family: openai
    expected_response_model_prefixes: ["gpt-x-2026"]
    supports: {temperature: true, top_p: true, reasoning_effort: true, json_schema: true, json_object: true}
  - id: council-basic
    family: other
    expected_response_model_prefixes: ["basic-1"]
    supports: {temperature: false, top_p: false, reasoning_effort: false, json_schema: false, json_object: true}
`

func writeTestModels(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "models.yaml")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write temp models.yaml: %v", err)
	}
	return path
}

func f64ptr(v float64) *float64 { return &v }

func loadTestRegistry(t *testing.T) *Registry {
	t.Helper()
	r, err := LoadRegistry(writeTestModels(t, testModelsYAML))
	if err != nil {
		t.Fatalf("LoadRegistry: %v", err)
	}
	return r
}

func TestLoadRegistryParsesTempYAML(t *testing.T) {
	r := loadTestRegistry(t)
	prefixes := r.ExpectedPrefixes("council-gpt-x")
	if len(prefixes) != 1 || prefixes[0] != "gpt-x-2026" {
		t.Fatalf("ExpectedPrefixes = %v", prefixes)
	}
	temp, topP, effort, js, jo := r.Supports("council-gpt-x")
	if !temp || !topP || !effort || !js || !jo {
		t.Fatalf("Supports = %v %v %v %v %v", temp, topP, effort, js, jo)
	}
	if !r.Known("council-gpt-x") || r.Known("nope") {
		t.Fatal("Known misreports registry membership")
	}
}

func TestLoadRegistryDuplicateIDs(t *testing.T) {
	dup := testModelsYAML + `  - id: council-gpt-x
    family: openai
    expected_response_model_prefixes: ["other"]
    supports: {temperature: true, top_p: true, reasoning_effort: true, json_schema: true, json_object: true}
`
	if _, err := LoadRegistry(writeTestModels(t, dup)); err == nil {
		t.Fatal("expected error on duplicate ids, got nil")
	}
}

func TestLoadRegistryMalformed(t *testing.T) {
	if _, err := LoadRegistry(writeTestModels(t, "models: [unclosed")); err == nil {
		t.Fatal("expected error on malformed YAML, got nil")
	}
}

func TestValidateSettingsRejectsUnsupportedNamingField(t *testing.T) {
	r := loadTestRegistry(t)
	sc := pack.SeatConfig{
		Seat: pack.SeatPossibility, Model: "council-basic",
		Family: "other", Temperature: f64ptr(0.5),
	}
	err := r.ValidateSettings(sc)
	if err == nil {
		t.Fatal("expected ErrUnsupportedSetting, got nil")
	}
	if !errors.Is(err, gateway.ErrUnsupportedSetting) {
		t.Fatalf("err = %v, want errors.Is ErrUnsupportedSetting", err)
	}
	if !strings.Contains(err.Error(), "temperature") {
		t.Fatalf("err = %v, want it to name %q", err, "temperature")
	}
}

func TestValidateSettingsRejectsUnknownModel(t *testing.T) {
	r := loadTestRegistry(t)
	sc := pack.SeatConfig{Seat: pack.SeatPossibility, Model: "nope", Family: "x"}
	err := r.ValidateSettings(sc)
	if err == nil {
		t.Fatal("expected error for unknown model, got nil")
	}
	if !errors.Is(err, gateway.ErrUnsupportedSetting) {
		t.Fatalf("err = %v, want errors.Is ErrUnsupportedSetting", err)
	}
	if !strings.Contains(err.Error(), "nope") {
		t.Fatalf("err = %v, want it to name the model", err)
	}
}

func TestValidateSettingsAcceptsNilPointerForUnsupported(t *testing.T) {
	r := loadTestRegistry(t)
	sc := pack.SeatConfig{
		Seat: pack.SeatPossibility, Model: "council-basic",
		Family: "other", MaxOutputTokens: 5000,
		// Temperature/TopP nil, ReasoningEffort empty -> unset, must pass.
	}
	if err := r.ValidateSettings(sc); err != nil {
		t.Fatalf("ValidateSettings with unset fields = %v, want nil", err)
	}
}
