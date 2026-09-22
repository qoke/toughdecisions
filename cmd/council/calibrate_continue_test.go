package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/qoke/toughdecisions/internal/config"
	"github.com/qoke/toughdecisions/internal/gateway"
	"github.com/qoke/toughdecisions/internal/logx"
	"github.com/qoke/toughdecisions/internal/store"
)

// TestCalibrateAllContinuesAfterOneGraderFailure pins D6: with --all, one
// grader's hard failure (double unparseable) is recorded for that grader
// and the command still evaluates the others instead of aborting.
func TestCalibrateAllContinuesAfterOneGraderFailure(t *testing.T) {
	dir := testEnv(t)
	writeCasepackDir(t, dir)
	graders := "graders:\n" +
		"  - {key: grader-bad, model: mfail, family: openai, role: selection, max_output_tokens: 2000}\n" +
		"  - {key: grader-good, model: mok, family: anthropic, role: selection, max_output_tokens: 2000}\n"
	if err := os.WriteFile(filepath.Join(dir, "graders.yaml"), []byte(graders), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "candidates.yaml"), []byte("candidates: []\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := run([]string{"db", "migrate"}); got != exitOK {
		t.Fatalf("migrate = %d", got)
	}
	if got := run([]string{"pack", "init"}); got != exitOK {
		t.Fatalf("pack init = %d", got)
	}
	if got := run([]string{"cases", "load"}); got != exitOK {
		t.Fatalf("cases load = %d", got)
	}
	fake := gateway.NewFake(map[string][]gateway.Step{
		"mfail": {{Content: "garbage one"}, {Content: "garbage two"}},
		"mok":   {{Content: gradeContent(nil)}},
	})
	old := gatewayFactory
	gatewayFactory = func(_ *config.Config, _ logx.Logger) gateway.Client { return fake }
	defer func() { gatewayFactory = old }()

	// Act: --all with one failing grader.
	got := run([]string{"graders", "calibrate", "--all"})
	// Assert: still reports the failure via the exit code...
	if got != exitError {
		t.Fatalf("calibrate --all = %d; want %d (one grader failed)", got, exitError)
	}
	// ...but the good grader was still evaluated and admitted.
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(cfg.DBPath())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	rows, err := db.ListGraderConfigs()
	if err != nil {
		t.Fatal(err)
	}
	byKey := map[string]*store.GraderConfig{}
	for _, r := range rows {
		byKey[r.GraderKey] = r
	}
	good := byKey["grader-good"]
	if good == nil || !good.Admitted {
		t.Fatalf("grader-good = %+v; want admitted (not masked by the failure)", good)
	}
	bad := byKey["grader-bad"]
	if bad == nil || bad.Admitted {
		t.Fatalf("grader-bad = %+v; want failed/absent", bad)
	}
	if bad.CalibrationJSON == nil || !strings.Contains(*bad.CalibrationJSON, "unparseable calibration grade") {
		t.Fatalf("grader-bad calibration_json = %v; want the failure reason recorded", bad.CalibrationJSON)
	}
}
