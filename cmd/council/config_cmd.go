package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"

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
		return configRender(args[0])
	case "check":
		return configCheck(args[1:])
	default:
		fmt.Fprintf(os.Stderr, "config: unknown subcommand %q (want show|reference|diagnose|check)\n", args[0])
		return exitValidation
	}
}

// configRender prints cfg.String / ConfigReference / Diagnose output. All
// three mask secret:"true" fields via cfggo; nothing here re-implements
// masking, and nothing opens the store or builds a gateway client.
func configRender(sub string) int {
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "config %s: %v\n", sub, err)
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
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "config check: %v\n", err)
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

// firstRegistryModel returns the first model id in ModelsFile order.
func firstRegistryModel(path string) (string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read models %s: %w", path, err)
	}
	var f struct {
		Models []models.Capability `yaml:"models"`
	}
	if err := yaml.Unmarshal(raw, &f); err != nil {
		return "", fmt.Errorf("parse models %s: %w", path, err)
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
// reason class (timeout, transport, gateway, bad response). It never echoes
// the key, a request/response body, or a credential-bearing URL.
func liveFailureMessage(cfg *config.Config, err error) string {
	host := ""
	if u, uerr := url.Parse(cfg.GatewayBaseURL()); uerr == nil {
		host = u.Host
	}
	class := "gateway error"
	switch {
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
