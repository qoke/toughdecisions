package config_test

import (
	"flag"
	"strings"
	"testing"
	"time"

	"github.com/qoke/toughdecisions/internal/config"
)

func countFlags() int {
	n := 0
	flag.CommandLine.VisitAll(func(*flag.Flag) { n++ })
	return n
}

func TestLoadReturnsDefaults(t *testing.T) {
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	cases := []struct {
		name string
		got  any
		want any
	}{
		{"server_listen", cfg.ServerListen(), "127.0.0.1:8080"},
		{"db_path", cfg.DBPath(), "./data/council.db"},
		{"views_deadline", cfg.ViewsDeadline(), 30 * time.Second},
		{"judge_deadline", cfg.JudgeDeadline(), 30 * time.Second},
		{"gateway_max_concurrent", cfg.GatewayMaxConcurrent(), 8},
		{"log_redact_content", cfg.LogRedactContent(), true},
	}
	for _, tc := range cases {
		if tc.got != tc.want {
			t.Errorf("%s = %v, want %v", tc.name, tc.got, tc.want)
		}
	}
}

func TestLoadEnvOverride(t *testing.T) {
	t.Setenv("COUNCIL_SERVER_LISTEN", ":9999")
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if got := cfg.ServerListen(); got != ":9999" {
		t.Fatalf("ServerListen() = %q, want %q", got, ":9999")
	}
}

func TestRedactedOutputMasksSecret(t *testing.T) {
	t.Setenv("COUNCIL_GATEWAY_API_KEY", "super-secret-value")
	t.Setenv("COUNCIL_SERVER_TOKEN", "server-secret-value")
	t.Setenv("COUNCIL_GATEWAY_BASE_URL", "https://proxy.example.com:4000")
	t.Setenv("COUNCIL_NOTIFY_WEBHOOK_URL", "https://hooks.example.com/secret-hook")
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	out := cfg.RedactedString()
	if strings.Contains(out, "super-secret-value") {
		t.Fatalf("redacted output leaks gateway_api_key")
	}
	if strings.Contains(out, "server-secret-value") {
		t.Fatalf("redacted output leaks server_token")
	}
	if strings.Contains(out, "proxy.example.com") {
		t.Fatalf("redacted output leaks gateway_base_url")
	}
	if strings.Contains(out, "hooks.example.com") {
		t.Fatalf("redacted output leaks notify_webhook_url")
	}
}

func TestLoadRegistersNoFlags(t *testing.T) {
	before := countFlags()
	if _, err := config.Load(); err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if after := countFlags(); after != before {
		t.Fatalf("Load() registered flags: before=%d after=%d", before, after)
	}
}
