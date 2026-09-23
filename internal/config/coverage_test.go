package config_test

import (
	"strings"
	"testing"

	"github.com/qoke/toughdecisions/internal/config"
)

// TestLoadFailsFastOnInvalidLogLevel covers the validation error branch of
// Load: an env value outside cfggo.OneOf(debug|info|warn|error) must return
// (nil, error) instead of a partially-built Config.
func TestLoadFailsFastOnInvalidLogLevel(t *testing.T) {
	cases := []struct {
		level   string
		wantErr string
	}{
		{level: "verbose", wantErr: "log_level"},
		{level: "TRACE", wantErr: "log_level"},
	}
	for _, tc := range cases {
		t.Run("should return an error when log_level is "+tc.level, func(t *testing.T) {
			// Arrange
			t.Setenv("COUNCIL_LOG_LEVEL", tc.level)

			// Act
			cfg, err := config.Load()

			// Assert
			if err == nil {
				t.Fatalf("Load() error = nil, want validation error for log_level %q", tc.level)
			}
			if cfg != nil {
				t.Fatalf("Load() cfg = %+v, want nil when validation fails", cfg)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("Load() error = %q, want it to name %q", err.Error(), tc.wantErr)
			}
		})
	}
}
