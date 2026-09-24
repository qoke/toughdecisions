package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestConfigMistypedSecretBlackBox execs the BUILT binary (not run()
// in-process) with a secret mistyped into a typed var and asserts the
// secret appears in neither stdout nor stderr. This is the HIGH finding's
// discriminating test: cfggo's own log lines write to the raw stderr fd,
// bypassing redactLoadError, so in-process os.Stderr capture is blind to
// them — only a real subprocess sees the fd output. Green requires both
// the redacting cfggo writer (installCfggoRedaction) and the redacted
// error string; neutering either must fail this test.
// The serve/pack-publish rows automate the auditor's manual check that
// openStore consumers are clean via newLoadError; serve/publish exit 1 at
// Load before binding or touching the gateway.
func TestConfigMistypedSecretBlackBox(t *testing.T) {
	const secret = "sk-dummy-mistype-111"
	bin := filepath.Join(t.TempDir(), "council")
	build := exec.Command("go", "build", "-o", bin, "./cmd/council")
	build.Dir = shippedRepoRoot(t)
	build.Env = os.Environ()
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build council: %v (%s)", err, out)
	}
	for _, args := range [][]string{
		{"config", "show"},
		{"config", "diagnose"},
		{"config", "check", "--live"},
		{"harness", "sentinel"},
		{"serve"},
		{"pack", "publish", "--run", "r1", "--candidate", "cand-a"},
	} {
		t.Run(strings.Join(args, "-"), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, bin, args...)
			cmd.Env = append(os.Environ(),
				"COUNCIL_GATEWAY_MAX_CONCURRENT="+secret,
				"COUNCIL_GATEWAY_API_KEY=",
			)
			out, err := cmd.CombinedOutput()
			if err == nil {
				t.Fatalf("%v = 0, want 1 (out=%q)", args, out)
			}
			if ee, ok := err.(*exec.ExitError); ok {
				if ee.ExitCode() != exitError {
					t.Fatalf("%v = %d, want %d (out=%q)", args, ee.ExitCode(), exitError, out)
				}
			}
			if strings.Contains(string(out), secret) {
				t.Fatalf("%v leaked the secret (out=%q)", args, out)
			}
			if !strings.Contains(strings.ToLower(string(out)), "gateway_max_concurrent") {
				t.Fatalf("%v = %q, want it to name gateway_max_concurrent", args, out)
			}
		})
	}
}
