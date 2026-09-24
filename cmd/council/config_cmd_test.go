package main

import (
	"io"
	"os"
	"strings"
	"testing"

	"github.com/qoke/toughdecisions/internal/config"
	"github.com/qoke/toughdecisions/internal/gateway"
	"github.com/qoke/toughdecisions/internal/logx"
)

// RED: config renders use cfggo's masked output, never touch the store or
// gateway, and never leak secrets. check --live makes exactly one call.
func TestConfigRendersMaskSecrets(t *testing.T) {
	const keySentinel = "sk-live-test-key-abc123"
	const tokSentinel = "tok-live-test-token-xyz789"
	t.Setenv("COUNCIL_GATEWAY_API_KEY", keySentinel)
	t.Setenv("COUNCIL_SERVER_TOKEN", tokSentinel)
	for _, sub := range []string{"show", "reference", "diagnose"} {
		t.Run(sub, func(t *testing.T) {
			out, code := captureStdout(t, func() int { return configCmd([]string{sub}) })
			if code != exitOK {
				t.Fatalf("config %s = %d, want %d", sub, code, exitOK)
			}
			if strings.Contains(out, keySentinel) || strings.Contains(out, tokSentinel) {
				t.Fatalf("config %s leaked a raw secret", sub)
			}
			if sub == "show" || sub == "diagnose" {
				if !strings.Contains(out, "****") {
					t.Fatalf("config %s = %q, want masked ****", sub, out)
				}
			}
		})
	}
}

func TestConfigReferenceListsEnvNames(t *testing.T) {
	out, code := captureStdout(t, func() int { return configCmd([]string{"reference"}) })
	if code != exitOK {
		t.Fatalf("config reference = %d, want %d", code, exitOK)
	}
	for _, want := range []string{"COUNCIL_GATEWAY_API_KEY", "COUNCIL_SERVER_TOKEN", "COUNCIL_DB_PATH"} {
		if !strings.Contains(out, want) {
			t.Fatalf("reference missing %q", want)
		}
	}
}

func TestConfigDiagnoseShowsSource(t *testing.T) {
	t.Setenv("COUNCIL_DB_PATH", "/tmp/from-env.db")
	out, code := captureStdout(t, func() int { return configCmd([]string{"diagnose"}) })
	if code != exitOK {
		t.Fatalf("config diagnose = %d, want %d", code, exitOK)
	}
	if !strings.Contains(strings.ToLower(out), "env") {
		t.Fatalf("diagnose = %q, want a SOURCE column naming env", out)
	}
	if !strings.Contains(strings.ToLower(out), "default") {
		t.Fatalf("diagnose = %q, want a SOURCE column naming default", out)
	}
}

func TestConfigRendersTouchNothing(t *testing.T) {
	dbPath := t.TempDir() + "/untouched.db"
	t.Setenv("COUNCIL_DB_PATH", dbPath)
	old := gatewayFactory
	gatewayFactory = func(_ *config.Config, _ logx.Logger) gateway.Client {
		t.Fatal("gateway client must not be built for show/reference/diagnose")
		return nil
	}
	defer func() { gatewayFactory = old }()
	for _, sub := range []string{"show", "reference", "diagnose"} {
		if code := configCmd([]string{sub}); code != exitOK {
			t.Fatalf("config %s = %d, want %d", sub, code, exitOK)
		}
	}
	if _, err := os.Stat(dbPath); !os.IsNotExist(err) {
		t.Fatalf("store file created at %q", dbPath)
	}
}

func TestConfigUnknownSubcommand(t *testing.T) {
	if got := configCmd([]string{"bogus"}); got != exitValidation {
		t.Fatalf("config bogus = %d, want %d", got, exitValidation)
	}
	if got := configCmd(nil); got != exitValidation {
		t.Fatalf("config missing = %d, want %d", got, exitValidation)
	}
}

func TestConfigCheckLiveSuccessOneCall(t *testing.T) {
	t.Setenv("COUNCIL_GATEWAY_API_KEY", "sk-live-ok-key-001")
	modelsFile := writeLiveModelsFileForCheck(t, "live-m1")
	t.Setenv("COUNCIL_MODELS_FILE", modelsFile)
	fake := gateway.NewFake(map[string][]gateway.Step{
		"live-m1": {{Content: "ok", ModelReturned: "live-m1"}},
	})
	old := gatewayFactory
	gatewayFactory = func(_ *config.Config, _ logx.Logger) gateway.Client { return fake }
	defer func() { gatewayFactory = old }()
	out, code := captureStdout(t, func() int { return configCmd([]string{"check", "--live"}) })
	if code != exitOK {
		t.Fatalf("check --live = %d, want %d: %s", code, exitOK, out)
	}
	if fake.CallCount() != 1 {
		t.Fatalf("gateway calls = %d, want exactly 1", fake.CallCount())
	}
	if !strings.Contains(strings.ToLower(out), "ok") {
		t.Fatalf("check --live output = %q, want one-line confirmation", out)
	}
}

func TestConfigCheckLiveFailureOneCallNoLeak(t *testing.T) {
	const keySentinel = "sk-live-fail-key-999"
	t.Setenv("COUNCIL_GATEWAY_API_KEY", keySentinel)
	modelsFile := writeLiveModelsFileForCheck(t, "live-m1")
	t.Setenv("COUNCIL_MODELS_FILE", modelsFile)
	fake := gateway.NewFake(map[string][]gateway.Step{
		"live-m1": {{Err: gateway.ErrGateway, ModelReturned: "live-m1"}},
	})
	old := gatewayFactory
	gatewayFactory = func(_ *config.Config, _ logx.Logger) gateway.Client { return fake }
	defer func() { gatewayFactory = old }()
	var msg string
	oldStderr := os.Stderr
	r, w, _ := os.Pipe()
	os.Stderr = w
	code := configCmd([]string{"check", "--live"})
	_ = w.Close()
	os.Stderr = oldStderr
	raw, _ := io.ReadAll(r)
	msg = string(raw)
	if code != exitError {
		t.Fatalf("check --live failing = %d, want %d", code, exitError)
	}
	if fake.CallCount() != 1 {
		t.Fatalf("gateway calls = %d, want exactly 1", fake.CallCount())
	}
	if strings.Contains(msg, keySentinel) {
		t.Fatal("failure message leaked the key")
	}
	bodyProbe := ""
	if len(fake.Calls) > 0 && len(fake.Calls[0].Messages) > 0 {
		bodyProbe = fake.Calls[0].Messages[0].Content
	}
	if bodyProbe != "" && strings.Contains(msg, bodyProbe) && len(bodyProbe) > 2 {
		t.Fatalf("failure message leaked request body %q", bodyProbe)
	}
	lower := strings.ToLower(msg)
	if !strings.Contains(lower, "status") && !strings.Contains(lower, "gateway") && !strings.Contains(lower, "transport") && !strings.Contains(lower, "timeout") && !strings.Contains(lower, "bad response") {
		t.Fatalf("failure message = %q, want HTTP status plus reason class", msg)
	}
}

func TestConfigCheckLiveMissingKeyBuildsNoGateway(t *testing.T) {
	for _, tc := range []struct {
		name string
		key  string
	}{
		{name: "empty", key: ""},
		{name: "whitespace", key: "   "},
	} {
		t.Run(tc.name, func(t *testing.T) {
			modelsFile := writeLiveModelsFileForCheck(t, "live-m1")
			t.Setenv("COUNCIL_MODELS_FILE", modelsFile)
			t.Setenv("COUNCIL_GATEWAY_API_KEY", tc.key)
			reached := false
			old := gatewayFactory
			gatewayFactory = func(_ *config.Config, _ logx.Logger) gateway.Client {
				reached = true
				t.Fatal("no gateway client may be built without a key")
				return nil
			}
			defer func() { gatewayFactory = old }()
			oldStderr := os.Stderr
			r, w, _ := os.Pipe()
			os.Stderr = w
			code := configCmd([]string{"check", "--live"})
			_ = w.Close()
			os.Stderr = oldStderr
			raw, _ := io.ReadAll(r)
			msg := string(raw)
			if code != exitError {
				t.Fatalf("check --live without key = %d, want %d", code, exitError)
			}
			if !strings.Contains(msg, "COUNCIL_GATEWAY_API_KEY") {
				t.Fatalf("stderr = %q, want it to name COUNCIL_GATEWAY_API_KEY", msg)
			}
			if reached {
				t.Fatal("gateway factory was reached without a key")
			}
		})
	}
}

// writeLiveModelsFileForCheck writes a one-model registry for check --live.
func writeLiveModelsFileForCheck(t *testing.T, id string) string {
	t.Helper()
	p := t.TempDir() + "/live-models.yaml"
	content := "models:\n  - id: " + id + "\n    family: openai\n    expected_response_model_prefixes: [\"" + id + "\"]\n    supports: {temperature: true, top_p: true, reasoning_effort: true, json_schema: true, json_object: true}\n"
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}
