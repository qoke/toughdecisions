package models

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/qoke/toughdecisions/internal/gateway"
	"github.com/qoke/toughdecisions/internal/pack"
)

// TestLoadRegistryRejectsBadInput covers the unreadable-file and empty-id
// error branches of LoadRegistry.
func TestLoadRegistryRejectsBadInput(t *testing.T) {
	cases := []struct {
		name    string
		path    func(t *testing.T) string
		wantErr string
	}{
		{
			name:    "file does not exist",
			path:    func(t *testing.T) string { return filepath.Join(t.TempDir(), "absent.yaml") },
			wantErr: "read",
		},
		{
			name: "entry with empty id",
			path: func(t *testing.T) string {
				return writeTestModels(t, "models:\n  - family: openai\n    supports: {temperature: true}\n")
			},
			wantErr: "empty id",
		},
	}
	for _, tc := range cases {
		t.Run("should return an error when "+tc.name, func(t *testing.T) {
			// Arrange
			path := tc.path(t)

			// Act
			reg, err := LoadRegistry(path)

			// Assert
			if err == nil {
				t.Fatalf("LoadRegistry(%s) error = nil, want error", path)
			}
			if reg != nil {
				t.Fatalf("LoadRegistry(%s) registry = %+v, want nil on error", path, reg)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("LoadRegistry error = %q, want it to mention %q", err.Error(), tc.wantErr)
			}
		})
	}
}

// TestValidateSettingsRejectsUnsupportedSettings covers the top_p and
// reasoning_effort rejection branches of ValidateSettings.
func TestValidateSettingsRejectsUnsupportedSettings(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(sc *pack.SeatConfig)
		want   string
	}{
		{
			name:   "top_p on a model without top_p support",
			mutate: func(sc *pack.SeatConfig) { sc.TopP = f64ptr(0.2) },
			want:   "top_p",
		},
		{
			name:   "reasoning_effort on a model without reasoning_effort support",
			mutate: func(sc *pack.SeatConfig) { sc.ReasoningEffort = "low" },
			want:   "reasoning_effort",
		},
	}
	for _, tc := range cases {
		t.Run("should reject "+tc.name, func(t *testing.T) {
			// Arrange
			r := loadTestRegistry(t)
			sc := pack.SeatConfig{Seat: pack.SeatPossibility, Model: "council-basic", Family: "other"}
			tc.mutate(&sc)

			// Act
			err := r.ValidateSettings(sc)

			// Assert
			if err == nil {
				t.Fatalf("ValidateSettings(%+v) = nil, want error", sc)
			}
			if !errors.Is(err, gateway.ErrUnsupportedSetting) {
				t.Fatalf("err = %v, want errors.Is ErrUnsupportedSetting", err)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want it to name %q", err, tc.want)
			}
		})
	}
}

// TestExpectedPrefixesIsNilWhenModelUnknown covers the unknown-model branch
// of ExpectedPrefixes.
func TestExpectedPrefixesIsNilWhenModelUnknown(t *testing.T) {
	// Arrange
	r := loadTestRegistry(t)

	// Act
	got := r.ExpectedPrefixes("no-such-model")

	// Assert
	if got != nil {
		t.Fatalf("ExpectedPrefixes(unknown) = %v, want nil", got)
	}
}

// TestSupportsAllFalseWhenModelUnknown covers the unknown-model branch of
// Supports.
func TestSupportsAllFalseWhenModelUnknown(t *testing.T) {
	// Arrange
	r := loadTestRegistry(t)

	// Act
	temp, topP, effort, jsonSchema, jsonObject := r.Supports("no-such-model")

	// Assert
	if temp || topP || effort || jsonSchema || jsonObject {
		t.Fatalf("Supports(unknown) = %v %v %v %v %v, want all false", temp, topP, effort, jsonSchema, jsonObject)
	}
}
