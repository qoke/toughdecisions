package logx_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/qoke/toughdecisions/internal/config"
	"github.com/qoke/toughdecisions/internal/logx"
)

func loadTestConfig(t *testing.T, level string, redact bool) *config.Config {
	t.Helper()
	if redact {
		t.Setenv("COUNCIL_LOG_REDACT_CONTENT", "true")
	} else {
		t.Setenv("COUNCIL_LOG_REDACT_CONTENT", "false")
	}
	t.Setenv("COUNCIL_LOG_LEVEL", level)
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("config.Load() error = %v", err)
	}
	return cfg
}

func TestNewReturnsNonNil(t *testing.T) {
	cfg := loadTestConfig(t, "info", true)
	if logx.New(cfg) == nil {
		t.Fatal("New() returned nil")
	}
}

func TestJSONOutputContainsMessageAndKey(t *testing.T) {
	cfg := loadTestConfig(t, "debug", false)
	var buf strings.Builder
	l := logx.NewWithWriter(cfg, &buf)
	l.Info("hello-world", "k", "v")
	out := buf.String()
	var rec map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &rec); err != nil {
		t.Fatalf("output is not JSON: %v (%q)", err, out)
	}
	if rec["message"] != "hello-world" {
		t.Fatalf("message = %v, want hello-world", rec["message"])
	}
	if rec["k"] != "v" {
		t.Fatalf("k = %v, want v", rec["k"])
	}
}

func TestWithAddsField(t *testing.T) {
	cfg := loadTestConfig(t, "debug", false)
	var buf strings.Builder
	l := logx.NewWithWriter(cfg, &buf).With("component", "test")
	l.Info("msg")
	var rec map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(buf.String())), &rec); err != nil {
		t.Fatalf("output is not JSON: %v", err)
	}
	if rec["component"] != "test" {
		t.Fatalf("component = %v, want test", rec["component"])
	}
}

func TestContentKeysRedactedWhenEnabled(t *testing.T) {
	cfg := loadTestConfig(t, "debug", true)
	var buf strings.Builder
	l := logx.NewWithWriter(cfg, &buf)
	l.Debug("msg", "card", "secret-card", "messages", "secret-msgs", "response_text", "secret-resp", "other", "kept")
	out := buf.String()
	for _, secret := range []string{"secret-card", "secret-msgs", "secret-resp"} {
		if strings.Contains(out, secret) {
			t.Fatalf("redacted content leaked in output: %q", out)
		}
	}
	if !strings.Contains(out, "kept") {
		t.Fatalf("non-content key dropped from output: %q", out)
	}
}

func TestContentKeysPresentWhenRedactionOff(t *testing.T) {
	cfg := loadTestConfig(t, "debug", false)
	var buf strings.Builder
	l := logx.NewWithWriter(cfg, &buf)
	l.Debug("msg", "card", "visible-card")
	if !strings.Contains(buf.String(), "visible-card") {
		t.Fatalf("content key missing when redaction off: %q", buf.String())
	}
}
