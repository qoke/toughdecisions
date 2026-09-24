package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"

	cfggo "github.com/iqhive/cfggo"
	"github.com/qoke/toughdecisions/internal/config"
	"github.com/qoke/toughdecisions/internal/gateway"
	"github.com/qoke/toughdecisions/internal/logx"
	"github.com/qoke/toughdecisions/internal/models"
	"gopkg.in/yaml.v3"
)

// configCmd dispatches the config subcommands (show|reference|diagnose|check)
// in the same shape as cases/graders. show/reference/diagnose render masked
// cfggo output to stdout and never open the store or build a gateway client.
// check --live is opt-in only and makes exactly one gateway call.
func configCmd(args []string) int {
	if len(args) == 0 {
		fmt.Fprintf(os.Stderr, "config: missing subcommand (want show|reference|diagnose|check)\n")
		return exitValidation
	}
	switch args[0] {
	case "show", "reference", "diagnose":
		return configRender(args[0], args[1:])
	case "check":
		return configCheck(args[1:])
	default:
		fmt.Fprintf(os.Stderr, "config: unknown subcommand %q (want show|reference|diagnose|check)\n", args[0])
		return exitValidation
	}
}

// redactLoadError redacts the configured secret values
// (COUNCIL_GATEWAY_API_KEY, COUNCIL_SERVER_TOKEN, raw and trimmed) and any
// token-shaped literal (sk- + >=8 chars, plus the credential shapes the
// docs scan treats as secrets) from a config.Load() error or cfggo log
// line before it is printed. Pinned cfggo v1.0.34 quotes raw env input
// for non-secret TYPED keys, so a secret pasted into e.g.
// COUNCIL_GATEWAY_MAX_CONCURRENT would otherwise be echoed verbatim —
// possibly with ONLY the mistyped copy present, so no configured value
// exists to match. The rest of the message is kept so users still learn
// which setting is invalid (e.g. `cannot parse int "***"`).
func redactLoadError(msg string) string {
	for _, env := range []string{"COUNCIL_GATEWAY_API_KEY", "COUNCIL_SERVER_TOKEN"} {
		if v, ok := lookupSecretEnv(env); ok {
			// Minimum length >= 8: below that a value carries no meaningful
			// secrecy (a real gateway key is long), and redacting it mangles
			// the actionable message itself (e.g. value "e" rewrites
			// "gateway_max_concurrent" into "gat***way..."), defeating this
			// topic's "configuration errors must be clear" goal — so the
			// short value is knowingly allowed through. Shape rules already
			// require >=8 for the same reason.
			if len(v) < 8 {
				continue
			}
			msg = strings.ReplaceAll(msg, v, "***")
			if t := strings.TrimSpace(v); t != "" && t != v && len(t) >= 8 {
				msg = strings.ReplaceAll(msg, t, "***")
			}
		}
	}
	for _, re := range secretShapeRes {
		msg = re.ReplaceAllString(msg, "***")
	}
	return msg
}

// secretShapeRes matches token-shaped literals for redaction: the same
// credential shapes the docs secret scan treats as secrets. Every pattern
// requires >=8 non-placeholder characters so short/common words are never
// redacted; <...> placeholders never match these shapes.
var secretShapeRes = []*regexp.Regexp{
	regexp.MustCompile(`sk-[A-Za-z0-9_-]{8,}`),
	regexp.MustCompile(`ghp_[A-Za-z0-9]{20,}`),
	regexp.MustCompile(`xox[baprs]-[A-Za-z0-9-]{10,}`),
	regexp.MustCompile(`AIza[0-9A-Za-z_-]{30,}`),
	regexp.MustCompile(`AKIA[0-9A-Z]{16}`),
	regexp.MustCompile(`eyJ[A-Za-z0-9_-]+\.eyJ[A-Za-z0-9_-]+\.`),
}

// lookupSecretEnv returns the raw env value for a secret-tagged var when it
// is set and non-blank. Blank values cannot appear in an error string, so
// there is nothing to redact.
func lookupSecretEnv(name string) (string, bool) {
	v, ok := os.LookupEnv(name)
	if !ok || v == "" {
		return "", false
	}
	return v, true
}

// loadErrMessage renders a config.Load() error for stderr with secret values
// redacted (see redactLoadError).
func loadErrMessage(err error) string {
	return redactLoadError(err.Error())
}

// newLoadError wraps a config.Load() failure with its message already
// redacted, so every openStore consumer (pack, graders, cases, flags,
// feedback, harness) is safe printing it with %v without finding its own
// redaction call. Message content is unchanged apart from redaction.
func newLoadError(err error) error {
	if err == nil {
		return nil
	}
	return errors.New(redactLoadError(err.Error()))
}

// redactingWriter is an io.Writer that redacts secret values from every
// chunk before forwarding it (used for cfggo's log output, whose lines
// bypass our error-string redaction). It never replaces os.Stderr: a nil
// out resolves os.Stderr AT WRITE TIME, so a test that swapped os.Stderr
// to a pipe after install still has its writes reach the pipe instead of
// being silently dropped to the install-time file.
type redactingWriter struct {
	out io.Writer
}

func (w redactingWriter) Write(p []byte) (int, error) {
	out := w.out
	if out == nil {
		out = os.Stderr
	}
	_, err := io.WriteString(out, redactLoadError(string(p)))
	return len(p), err
}

// installCfggoRedaction routes cfggo's default log output through the
// redacting writer, once per process, before any config.Load(). cfggo
// v1.0.34 snapshots GlobalLogger() into each Structure at Init
// (structure.go:495 `c.logger = GlobalLogger()`), and SetLogOutput
// replaces the global with a logger writing to w (api.go:146-150), so
// every Load-time log line passes redactLoadError while diagnostics
// (level, key names) are preserved. Idempotent and safe under -race.
var cfggoRedactOnce sync.Once

func installCfggoRedaction() {
	cfggoRedactOnce.Do(func() {
		cfggo.SetLogOutput(redactingWriter{})
	})
}

// captureCfggoLogs redirects cfggo's global log output to buf for the
// duration of fn and restores the redacting stderr writer after. Tests
// must use this (not os.Stderr swaps) because cfggo holds the writer, not
// the *os.File. buf receives the REDACTED form (what stderr actually
// shows), not cfggo's raw bytes. Restores even if fn panics; not safe
// for parallel tests.
func captureCfggoLogs(buf *bytes.Buffer) func() {
	prev := redactingWriter{out: os.Stderr}
	cfggo.SetLogOutput(io.MultiWriter(prev, redactingWriter{out: buf}))
	return func() { cfggo.SetLogOutput(prev) }
}

// configRender prints cfg.String / ConfigReference / Diagnose output. All
// three mask secret:"true" fields via cfggo; nothing here re-implements
// masking, and nothing opens the store or builds a gateway client.
func configRender(sub string, args []string) int {
	if len(args) > 0 {
		fmt.Fprintf(os.Stderr, "config %s: unexpected args; usage: config %s\n", sub, sub)
		return exitValidation
	}
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "config %s: %s\n", sub, loadErrMessage(err))
		return exitError
	}
	switch sub {
	case "show":
		fmt.Println(cfg.String())
	case "reference":
		fmt.Println(cfg.ConfigReference())
	case "diagnose":
		fmt.Println(cfg.Diagnose().String())
	}
	return exitOK
}

// configCheck implements `config check --live`: exactly ONE gateway call via
// gatewayFactory (so the test hook works) using the first model in
// ModelsFile order with a minimal 1-token request. First-model-in-file is
// the documented choice because any configured model proves reachability;
// the registry order is the file order. Never called by any other path.
func configCheck(args []string) int {
	fs := flag.NewFlagSet("config check", flag.ContinueOnError)
	live := fs.Bool("live", false, "make exactly one live gateway call")
	if err := fs.Parse(args); err != nil {
		return exitValidation
	}
	if !*live {
		fmt.Fprintf(os.Stderr, "config check: nothing to check without --live\n")
		return exitValidation
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(os.Stderr, "config check: unexpected args; usage: config check --live\n")
		return exitValidation
	}
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "config check: %s\n", loadErrMessage(err))
		return exitError
	}
	if err := requireGatewayKey(cfg); err != nil {
		fmt.Fprintf(os.Stderr, "config check: %v\n", err)
		return exitError
	}
	model, err := firstRegistryModel(cfg.ModelsFile())
	if err != nil {
		fmt.Fprintf(os.Stderr, "config check: %v\n", err)
		return exitError
	}
	log := logx.New(cfg)
	gw := gatewayFactory(cfg, log)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	// The key travels only in the Authorization header inside the gateway
	// client; nothing here logs or prints it, the request body, the
	// response body, or any credential-bearing URL.
	_, err = gw.Chat(ctx, gateway.ChatRequest{
		Model:           model,
		Messages:        []gateway.Message{{Role: "user", Content: "hi"}},
		MaxOutputTokens: 1,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "config check: %s\n", liveFailureMessage(cfg, err))
		return exitError
	}
	fmt.Printf("config check: ok (live call to %q succeeded)\n", model)
	return exitOK
}

// firstRegistryModel returns the first model id in ModelsFile order,
// mirroring production's models.LoadRegistry semantics: malformed YAML or
// an entry with an empty id is an error; unknown extra fields are ignored
// (non-strict). It never echoes a file snippet, only the path.
func firstRegistryModel(path string) (string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read models %s: %w", path, err)
	}
	var f struct {
		Models []models.Capability `yaml:"models"`
	}
	if err := yaml.Unmarshal(raw, &f); err != nil {
		return "", fmt.Errorf("parse models %s", path)
	}
	if len(f.Models) == 0 {
		return "", fmt.Errorf("no models in %s", path)
	}
	if f.Models[0].ID == "" {
		return "", fmt.Errorf("first model in %s has empty id", path)
	}
	return f.Models[0].ID, nil
}

// liveFailureMessage renders an allowlisted failure: HTTP status plus a
// reason class (redirect refusal, timeout, transport, gateway, bad
// response). It never echoes the key, a request/response body, or a
// credential-bearing URL.
func liveFailureMessage(cfg *config.Config, err error) string {
	host := ""
	if u, uerr := url.Parse(cfg.GatewayBaseURL()); uerr == nil {
		host = u.Host
	}
	class := "gateway error"
	switch {
	case isRedirectErr(err):
		class = "redirect refused (gateway client does not follow redirects)"
	case isTimeoutErr(err):
		class = "timeout"
	case isTransportErr(err):
		class = "transport error"
	case isBadJSONErr(err):
		class = "bad response"
	}
	status := httpStatusOf(err)
	if host != "" {
		return fmt.Sprintf("live gateway check failed: %s (%s via %s)", status, class, host)
	}
	return fmt.Sprintf("live gateway check failed: %s (%s)", status, class)
}

func isTimeoutErr(err error) bool {
	if err == nil {
		return false
	}
	// ErrTimeout is always wrapped with %w on the timeout paths
	// (litellm.go semaphore-acquire and client-Do), so errors.Is matches.
	// The "deadline exceeded" fallback covers context-deadline errors that
	// arrive unwrapped (no exported sentinel exists for those).
	if isErr(err, gateway.ErrTimeout) {
		return true
	}
	return strings.Contains(strings.ToLower(err.Error()), "deadline exceeded")
}

func isTransportErr(err error) bool {
	if err == nil {
		return false
	}
	// No exported sentinel isolates transport failures: the transport path
	// wraps gateway.ErrGateway together with the underlying error
	// (litellm.go "gateway: do: %w: %w"), which errors.Is cannot
	// distinguish from HTTP-status failures wrapping the same sentinel.
	// Substring matching is the only honest classifier here; reported
	// rather than papered over.
	s := strings.ToLower(err.Error())
	return strings.Contains(s, "gateway: do:") || strings.Contains(s, "connection refused") || strings.Contains(s, "no such host")
}

func isBadJSONErr(err error) bool {
	return isErr(err, gateway.ErrBadJSON)
}

// isRedirectErr reports a refused 3xx: the shared no-redirect client
// surfaces it through litellm.go's status path (e.g. "gateway: status
// 302: unparseable error body"), keeping the Authorization header off the
// redirect target. Checked before transport so a 3xx names the refusal.
func isRedirectErr(err error) bool {
	if err == nil {
		return false
	}
	s := err.Error()
	idx := strings.Index(s, "status ")
	if idx < 0 {
		return false
	}
	rest := s[idx+len("status "):]
	digits := ""
	for _, c := range rest {
		if c < '0' || c > '9' {
			break
		}
		digits += string(c)
	}
	if len(digits) != 3 || digits[0] != '3' {
		return false
	}
	return true
}

// httpStatusOf extracts "status NNN" from gateway errors, else "no status".
func httpStatusOf(err error) string {
	if err == nil {
		return "no status"
	}
	s := err.Error()
	idx := strings.Index(s, "status ")
	if idx < 0 {
		return "no status"
	}
	rest := s[idx+len("status "):]
	digits := ""
	for _, c := range rest {
		if c < '0' || c > '9' {
			break
		}
		digits += string(c)
	}
	if digits == "" {
		return "no status"
	}
	return "status " + digits
}

// isErr matches wrapped gateway sentinel errors with errors.Is. Every
// gateway failure path wraps its sentinel with %w (litellm.go, fake.go),
// so errors.Is is exact; no message-substring fallback.
func isErr(err, target error) bool {
	return errors.Is(err, target)
}
