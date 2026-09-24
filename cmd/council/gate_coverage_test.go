// TestGateCoversEverySpendingRow is the anti-bypass test (AC16/R-06): the
// spend table's spends=true rows must be exactly the set the missing-key
// gate covers. It is driven by SpendRows(), never by a hand-written list,
// so adding a spending command without a gate (or without an argv mapping
// here) fails the suite instead of being silently ignored.
package main

import (
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/qoke/toughdecisions/internal/config"
	"github.com/qoke/toughdecisions/internal/gateway"
	"github.com/qoke/toughdecisions/internal/logx"
)

// gateArgv maps a spend-table row Name to the argv that reaches the gate.
// Every spends=true row must have an entry; a missing entry is a FAIL.
var gateArgv = map[string][]string{
	"serve":               {"serve"},
	"harness sentinel":    {"harness", "sentinel"},
	"harness screen":      {"harness", "screen"},
	"harness compare":     {"harness", "compare", "--candidate", "x"},
	"harness downstream":  {"harness", "downstream", "--candidate", "x", "--run", "r"},
	"harness weekly":      {"harness", "weekly"},
	"graders calibrate":   {"graders", "calibrate", "--all"},
	"pack publish":        {"pack", "publish"},
	"config check --live": {"config", "check", "--live"},
}

func TestGateCoversEverySpendingRow(t *testing.T) {
	rows := SpendRows()
	spending := 0
	for _, r := range rows {
		if r.Spends {
			spending++
		}
	}
	if spending == 0 {
		t.Fatal("SpendRows has no spends=true rows; test is vacuous")
	}
	for _, r := range rows {
		if !r.Spends {
			continue
		}
		argv, ok := gateArgv[r.Name]
		if !ok {
			t.Fatalf("spend row %q has no argv mapping: add one or the gate escapes coverage", r.Name)
		}
		t.Run(r.Name, func(t *testing.T) {
			testEnv(t)
			t.Setenv("COUNCIL_GATEWAY_API_KEY", "")
			oldFactory := gatewayFactory
			gatewayFactory = func(_ *config.Config, _ logx.Logger) gateway.Client {
				t.Error("gateway client must not be built without a key")
				return gateway.NewFake(nil)
			}
			defer func() { gatewayFactory = oldFactory }()
			start := time.Now()
			out, errOut, code := runCaptured(t, argv)
			if elapsed := time.Since(start); elapsed > 30*time.Second {
				t.Fatalf("%q took %v; the gate must fire before binding or work", r.Name, elapsed)
			}
			if code != exitError {
				t.Fatalf("%q with no key = %d, want %d (output %q, stderr %q)", r.Name, code, exitError, out, errOut)
			}
			if !strings.Contains(errOut, "COUNCIL_GATEWAY_API_KEY") {
				t.Fatalf("%q stderr = %q, want it to name COUNCIL_GATEWAY_API_KEY", r.Name, errOut)
			}
			if !strings.Contains(errOut, "council config diagnose") {
				t.Fatalf("%q stderr = %q, want pointer at `council config diagnose`", r.Name, errOut)
			}
		})
	}
}

// The exemption (AC9): report-only weekly work stays offline with no key.
func TestGateExemptsWeeklyReportOnly(t *testing.T) {
	dir := testEnv(t)
	t.Setenv("COUNCIL_GATEWAY_API_KEY", "")
	writeCasepackDir(t, dir)
	writeGradersFile(t, dir)
	if got := run([]string{"db", "migrate"}); got != exitOK {
		t.Fatalf("migrate = %d, want %d", got, exitOK)
	}
	if got := run([]string{"pack", "init"}); got != exitOK {
		t.Fatalf("pack init = %d, want %d", got, exitOK)
	}
	if got := run([]string{"cases", "load"}); got != exitOK {
		t.Fatalf("cases load = %d, want %d", got, exitOK)
	}
	_, errOut, code := runCaptured(t, []string{"harness", "weekly", "--steps", "report"})
	if code != exitOK {
		t.Fatalf("weekly --steps report with no key = %d, want %d (stderr %q)", code, exitOK, errOut)
	}
	if strings.Contains(errOut, "COUNCIL_GATEWAY_API_KEY") {
		t.Fatalf("weekly --steps report stderr = %q, must not hit the key gate", errOut)
	}
}

// The contrast that makes the exemption meaningful: default steps gate.
func TestGateCoversWeeklyDefaultSteps(t *testing.T) {
	testEnv(t)
	t.Setenv("COUNCIL_GATEWAY_API_KEY", "")
	_, errOut, code := runCaptured(t, []string{"harness", "weekly"})
	if code != exitError {
		t.Fatalf("weekly default steps with no key = %d, want %d", code, exitError)
	}
	if !strings.Contains(errOut, "COUNCIL_GATEWAY_API_KEY") {
		t.Fatalf("weekly stderr = %q, want it to name COUNCIL_GATEWAY_API_KEY", errOut)
	}
}

// serve must fail in dispatch before binding: no listener left behind.
func TestGateServeBindsNothing(t *testing.T) {
	testEnv(t)
	t.Setenv("COUNCIL_GATEWAY_API_KEY", "")
	start := time.Now()
	_, errOut, code := runCaptured(t, []string{"serve"})
	if elapsed := time.Since(start); elapsed > 30*time.Second {
		t.Fatalf("serve took %v; the gate must fire before binding", elapsed)
	}
	if code != exitError {
		t.Fatalf("serve with no key = %d, want %d", code, exitError)
	}
	if !strings.Contains(errOut, "COUNCIL_GATEWAY_API_KEY") {
		t.Fatalf("serve stderr = %q, want it to name COUNCIL_GATEWAY_API_KEY", errOut)
	}
}

// config check --live must fail before building any gateway client.
func TestGateCheckLiveBuildsNoClient(t *testing.T) {
	testEnv(t)
	t.Setenv("COUNCIL_GATEWAY_API_KEY", "")
	oldFactory := gatewayFactory
	gatewayFactory = func(_ *config.Config, _ logx.Logger) gateway.Client {
		t.Fatal("no gateway client may be built without a key")
		return nil
	}
	defer func() { gatewayFactory = oldFactory }()
	_, errOut, code := runCaptured(t, []string{"config", "check", "--live"})
	if code != exitError {
		t.Fatalf("check --live with no key = %d, want %d", code, exitError)
	}
	if !strings.Contains(errOut, "COUNCIL_GATEWAY_API_KEY") {
		t.Fatalf("check --live stderr = %q, want it to name COUNCIL_GATEWAY_API_KEY", errOut)
	}
}

// runCaptured runs argv through dispatch capturing stdout and stderr.
func runCaptured(t *testing.T, argv []string) (string, string, int) {
	t.Helper()
	outR, outW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	errR, errW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	oldOut, oldErr := os.Stdout, os.Stderr
	os.Stdout, os.Stderr = outW, errW
	outCh := make(chan string, 1)
	errCh := make(chan string, 1)
	go func() {
		raw, _ := io.ReadAll(outR)
		outCh <- string(raw)
	}()
	go func() {
		raw, _ := io.ReadAll(errR)
		errCh <- string(raw)
	}()
	code := run(argv)
	_ = outW.Close()
	_ = errW.Close()
	os.Stdout, os.Stderr = oldOut, oldErr
	return <-outCh, <-errCh, code
}
