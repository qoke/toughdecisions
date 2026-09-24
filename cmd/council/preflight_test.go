package main

import (
	"strings"
	"testing"

	"github.com/qoke/toughdecisions/internal/config"
)

// RED: preflight helpers are pure (no network, no store, no logging).
func TestNormalizeGatewayKey(t *testing.T) {
	cases := []struct{ in, want string }{
		{"key", "key"},
		{"  k  ", "k"},
		{"\tkey\n", "key"},
		{"", ""},
		{"   ", ""},
	}
	for _, tc := range cases {
		if got := normalizeGatewayKey(tc.in); got != tc.want {
			t.Fatalf("normalizeGatewayKey(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestRequireGatewayKeyRejectsBlank(t *testing.T) {
	t.Setenv("COUNCIL_GATEWAY_API_KEY", "")
	for _, tc := range []struct {
		name, key string
	}{
		{"empty", ""},
		{"spaces", "   "},
		{"tab", "\t"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("COUNCIL_GATEWAY_API_KEY", tc.key)
			cfg, err := config.Load()
			if err != nil {
				t.Fatalf("config.Load: %v", err)
			}
			err = requireGatewayKey(cfg)
			if err == nil {
				t.Fatal("requireGatewayKey: want error, got nil")
			}
			msg := err.Error()
			for _, want := range []string{"COUNCIL_GATEWAY_API_KEY", "council config diagnose", "export COUNCIL_GATEWAY_API_KEY="} {
				if !strings.Contains(msg, want) {
					t.Fatalf("preflight message = %q, want %q", msg, want)
				}
			}
		})
	}
}

func TestRequireGatewayKeyAcceptsKey(t *testing.T) {
	t.Setenv("COUNCIL_GATEWAY_API_KEY", "key")
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	if err := requireGatewayKey(cfg); err != nil {
		t.Fatalf("requireGatewayKey: %v, want nil", err)
	}
}
