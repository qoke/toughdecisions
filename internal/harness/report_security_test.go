package harness

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// SEC-H1: run ids that escape the reports dir are rejected before any
// file write.
func TestValidateReportRunIDRejectsTraversal(t *testing.T) {
	for _, bad := range []string{"", "  ", "../evil", "a/b", `a\b`, "..", "/abs"} {
		if err := validateReportRunID(bad); err == nil {
			t.Fatalf("run id %q: want error, got nil", bad)
		}
		if _, err := setupHarness(t, nil).runner.Report(context.Background(), ReportOptions{RunID: bad}); err == nil {
			t.Fatalf("Report(%q): want error, got nil", bad)
		}
	}
	if err := validateReportRunID("run-2026-09-21"); err != nil {
		t.Fatalf("valid run id: %v", err)
	}
}

// SEC-M1: non-http(s) webhook URLs are rejected; SEC-M2: logged errors
// carry only host + class, never the raw URL (which may embed a token).
func TestNotifyWebhookRejectsSchemeAndRedactsErrors(t *testing.T) {
	if webhookHTTPClient.Timeout <= 0 {
		t.Fatal("webhookHTTPClient: want explicit timeout")
	}
	for _, tc := range []struct {
		name string
		url  string
	}{
		{name: "file", url: "file:///tmp/hook?token=secret"},
		{name: "ftp", url: "ftp://example.com/hook"},
		{name: "no-scheme", url: "example.com/hook"},
		{name: "empty-host", url: "https:///hook"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("COUNCIL_NOTIFY_WEBHOOK_URL", tc.url)
			fx := setupHarness(t, nil)
			if fx.runner.notifyWebhook(context.Background(), "r1", "s", "md") {
				t.Fatalf("%s webhook: want rejection (false)", tc.url)
			}
		})
	}
	s := sanitizeWebhookError("https://hooks.example.com/path?token=secret", errors.New("connection refused: dial"))
	if strings.Contains(s, "token=secret") || strings.Contains(s, "/path") {
		t.Fatalf("sanitized error leaks URL: %q", s)
	}
	if !strings.Contains(s, "hooks.example.com") || !strings.Contains(s, "connection-error") {
		t.Fatalf("sanitized error missing host+class: %q", s)
	}
}
