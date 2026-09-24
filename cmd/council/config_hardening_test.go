package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

// TestConfigLoadErrorRedactsSecrets is the MEDIUM 1 regression test: a
// secret-shaped value pasted into a TYPED var must not be echoed by the
// config.Load() failure. It asserts exit 1, that the secret appears in
// neither stdout nor stderr, and that the message still names the
// offending setting. It fails without redactLoadError.
func TestConfigLoadErrorRedactsSecrets(t *testing.T) {
	const secret = "sk-live-abc123"
	// The real key is configured and ALSO mistyped into a typed var: the
	// Load() error then echoes it via the typed var's parse failure, and
	// redaction (which knows the API_KEY value) must mask every occurrence.
	t.Setenv("COUNCIL_GATEWAY_API_KEY", secret)
	t.Setenv("COUNCIL_GATEWAY_MAX_CONCURRENT", secret)
	for _, sub := range []string{"show", "diagnose"} {
		t.Run(sub, func(t *testing.T) {
			oldErr := os.Stderr
			r, w, _ := os.Pipe()
			os.Stderr = w
			out, code := captureStdout(t, func() int { return configCmd([]string{sub}) })
			_ = w.Close()
			os.Stderr = oldErr
			raw, _ := io.ReadAll(r)
			msg := string(raw)
			if code != exitError {
				t.Fatalf("config %s = %d, want %d (out=%q err=%q)", sub, code, exitError, out, msg)
			}
			if strings.Contains(out, secret) || strings.Contains(msg, secret) {
				t.Fatalf("config %s echoed the secret (out=%q err=%q)", sub, out, msg)
			}
			if !strings.Contains(strings.ToLower(out+msg), "gateway_max_concurrent") {
				t.Fatalf("config %s = %q %q, want it to name gateway_max_concurrent", sub, out, msg)
			}
		})
	}
}

// TestConfigShowRejectsPositionals covers L4: extra positionals exit 2.
func TestConfigShowRejectsPositionals(t *testing.T) {
	oldErr := os.Stderr
	r, w, _ := os.Pipe()
	os.Stderr = w
	code := configCmd([]string{"show", "junk"})
	_ = w.Close()
	os.Stderr = oldErr
	raw, _ := io.ReadAll(r)
	if code != exitValidation {
		t.Fatalf("config show junk = %d, want %d (%q)", code, exitValidation, string(raw))
	}
	if !strings.Contains(string(raw), "unexpected args") {
		t.Fatalf("stderr = %q, want unknown-argument message", string(raw))
	}
}

// TestConfigCheckLiveDoesNotFollowRedirects covers L5: a 302 must surface
// as a failure with no request to the redirect target, so the
// Authorization header is never forwarded to another host.
func TestConfigCheckLiveDoesNotFollowRedirects(t *testing.T) {
	var targetHits int
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		targetHits++
		w.WriteHeader(http.StatusOK)
	}))
	defer target.Close()
	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Location", target.URL+"/v1/chat/completions")
		w.WriteHeader(http.StatusFound)
	}))
	defer redirector.Close()
	t.Setenv("COUNCIL_GATEWAY_API_KEY", "sk-live-redirect-probe-001")
	t.Setenv("COUNCIL_GATEWAY_BASE_URL", redirector.URL)
	modelsFile := writeLiveModelsFileForCheck(t, "live-m1")
	t.Setenv("COUNCIL_MODELS_FILE", modelsFile)
	oldErr := os.Stderr
	r, w, _ := os.Pipe()
	os.Stderr = w
	code := configCmd([]string{"check", "--live"})
	_ = w.Close()
	os.Stderr = oldErr
	raw, _ := io.ReadAll(r)
	msg := string(raw)
	if code != exitError {
		t.Fatalf("check --live via redirect = %d, want %d (%q)", code, exitError, msg)
	}
	if targetHits != 0 {
		t.Fatalf("redirect target hit %d times, want 0 (no credential forwarding)", targetHits)
	}
	lower := strings.ToLower(msg)
	if !strings.Contains(lower, "status") && !strings.Contains(lower, "gateway") && !strings.Contains(lower, "transport") && !strings.Contains(lower, "timeout") && !strings.Contains(lower, "bad response") {
		t.Fatalf("failure message = %q, want allowlisted text", msg)
	}
}

// TestConfigCheckRejectsPositionals covers L4 for check --live.
func TestConfigCheckRejectsPositionals(t *testing.T) {
	oldErr := os.Stderr
	r, w, _ := os.Pipe()
	os.Stderr = w
	code := configCmd([]string{"check", "--live", "junk"})
	_ = w.Close()
	os.Stderr = oldErr
	raw, _ := io.ReadAll(r)
	if code != exitValidation {
		t.Fatalf("config check --live junk = %d, want %d (%q)", code, exitValidation, string(raw))
	}
	if !strings.Contains(string(raw), "unexpected args") {
		t.Fatalf("stderr = %q, want unknown-argument message", string(raw))
	}
}
