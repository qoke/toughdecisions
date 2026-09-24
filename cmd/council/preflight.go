package main

import (
	"fmt"
	"strings"

	"github.com/qoke/toughdecisions/internal/config"
)

// normalizeGatewayKey trims surrounding whitespace from a gateway key.
func normalizeGatewayKey(s string) string {
	return strings.TrimSpace(s)
}

// requireGatewayKey is the pure preflight gate later steps call at their
// command boundary: nil when a non-blank key is configured, otherwise the
// one canonical actionable error. No network, no store, no logging of the
// value (the key itself never appears in the message).
func requireGatewayKey(cfg *config.Config) error {
	if cfg != nil && normalizeGatewayKey(cfg.GatewayAPIKey()) != "" {
		return nil
	}
	return fmt.Errorf("missing COUNCIL_GATEWAY_API_KEY: run `council config diagnose` to inspect config, then export COUNCIL_GATEWAY_API_KEY='your-key'")
}
