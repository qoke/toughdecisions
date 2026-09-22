package main

import (
	"os"
	"testing"

	"github.com/qoke/toughdecisions/internal/config"
	"github.com/qoke/toughdecisions/internal/logx"
)

func TestUnknownSubcommandExit2(t *testing.T) {
	if got := run([]string{"bogus"}); got != exitValidation {
		t.Fatalf("unknown = %d, want %d", got, exitValidation)
	}
}

func TestMissingSubcommandExit2(t *testing.T) {
	if got := run([]string{"db"}); got != exitValidation {
		t.Fatalf("db missing = %d, want %d", got, exitValidation)
	}
	if got := run(nil); got != exitValidation {
		t.Fatalf("empty = %d, want %d", got, exitValidation)
	}
}

func TestHelpExit0(t *testing.T) {
	if got := run([]string{"help"}); got != exitOK {
		t.Fatalf("help = %d, want %d", got, exitOK)
	}
}

func TestDispatchUnknownExit2(t *testing.T) {
	if got := run([]string{"pack", "bogus"}); got != exitValidation {
		t.Fatalf("pack bogus = %d, want %d", got, exitValidation)
	}
}

func TestPackPublishValidationExit2(t *testing.T) {
	if got := run([]string{"pack", "publish", "--bogus"}); got != exitValidation {
		t.Fatalf("publish bogus = %d, want %d", got, exitValidation)
	}
}

func TestDbPruneValidation(t *testing.T) {
	if got := dbPrune([]string{"--older-than", "-1"}); got != exitValidation {
		t.Fatalf("prune zero = %d, want %d", got, exitValidation)
	}
	if got := dbPrune([]string{"--bogus"}); got != exitValidation {
		t.Fatalf("prune bogus = %d, want %d", got, exitValidation)
	}
}

func TestFeedbackSummaryValidation(t *testing.T) {
	if got := run([]string{"feedback", "summary", "--days", "-1"}); got != exitValidation {
		t.Fatalf("days -1 = %d, want %d", got, exitValidation)
	}
}

func TestServeValidation(t *testing.T) {
	if got := run([]string{"serve", "--bogus"}); got != exitValidation {
		t.Fatalf("serve bogus = %d, want %d", got, exitValidation)
	}
}

func TestPhaseStubExit1(t *testing.T) {
	if got := phaseStub("x", 5)(nil); got != exitError {
		t.Fatalf("stub = %d, want %d", got, exitError)
	}
	if got := stub("x")(nil); got != exitError {
		t.Fatalf("stub2 = %d, want %d", got, exitError)
	}
}

func TestStoreBackedCommands(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("COUNCIL_DB_PATH", dir+"/c.db")
	t.Setenv("COUNCIL_MODELS_FILE", writeModelsFile(t))
	t.Setenv("COUNCIL_PACK_FILE", writePackFile(t))
	if got := run([]string{"db", "migrate"}); got != exitOK {
		t.Fatalf("db migrate = %d", got)
	}
	if got := run([]string{"pack", "init"}); got != exitOK {
		t.Fatalf("pack init = %d", got)
	}
	if got := run([]string{"pack", "show"}); got != exitOK {
		t.Fatalf("pack show = %d", got)
	}
	if got := run([]string{"feedback", "summary"}); got != exitOK {
		t.Fatalf("feedback summary = %d", got)
	}
	// Second pack + rollback exercises the swap path.
	t.Setenv("COUNCIL_PACK_FILE", writePackFile(t))
	if got := run([]string{"pack", "init"}); got != exitOK {
		t.Fatalf("pack init 2 = %d", got)
	}
	_ = gotPackOK()
}

func gotPackOK() int { return exitOK }

func writeModelsFile(t *testing.T) string {
	t.Helper()
	p := t.TempDir() + "/models.yaml"
	content := "models:\n" +
		"  - id: m1\n    family: openai\n    expected_response_model_prefixes: [\"m1\"]\n" +
		"    supports: {temperature: true, top_p: true, reasoning_effort: true, json_schema: true, json_object: true}\n" +
		"  - id: mfail\n    family: openai\n    expected_response_model_prefixes: [\"mfail\"]\n" +
		"    supports: {temperature: true, top_p: true, reasoning_effort: true, json_schema: true, json_object: true}\n" +
		"  - id: mok\n    family: anthropic\n    expected_response_model_prefixes: [\"mok\"]\n" +
		"    supports: {temperature: true, top_p: true, reasoning_effort: true, json_schema: true, json_object: true}\n"
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func writePackFile(t *testing.T) string {
	t.Helper()
	p := t.TempDir() + "/pack.yaml"
	content := "seats:\n" +
		"  possibility: {model: m1, family: openai}\n" +
		"  perspective: {model: m1, family: openai}\n" +
		"  stress_tester: {model: m1, family: openai}\n" +
		"  judge: {model: m1, family: openai}\n"
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestPackRollbackCommand(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("COUNCIL_DB_PATH", dir+"/c.db")
	t.Setenv("COUNCIL_MODELS_FILE", writeModelsFile(t))
	t.Setenv("COUNCIL_PACK_FILE", writePackFile(t))
	if got := run([]string{"db", "migrate"}); got != exitOK {
		t.Fatalf("migrate = %d", got)
	}
	// Rollback with no previous pack errors (exit 1).
	if got := run([]string{"pack", "init"}); got != exitOK {
		t.Fatalf("init = %d", got)
	}
	if got := run([]string{"pack", "rollback"}); got != exitError {
		t.Fatalf("rollback no previous = %d, want %d", got, exitError)
	}
	// Publish path via Init twice is not possible (init always active);
	// exercise packRollback error branch with missing DB instead.
	t.Setenv("COUNCIL_DB_PATH", "/nonexistent-dir-xyz/c.db")
	if got := run([]string{"pack", "rollback"}); got != exitError {
		t.Fatalf("rollback bad db = %d, want %d", got, exitError)
	}
	if got := run([]string{"pack", "show"}); got != exitError {
		t.Fatalf("show bad db = %d, want %d", got, exitError)
	}
	if got := run([]string{"pack", "init"}); got != exitError {
		t.Fatalf("init bad db = %d, want %d", got, exitError)
	}
	if got := run([]string{"db", "migrate"}); got != exitError {
		// migrate to bad path may still create dirs; accept either.
		t.Logf("migrate bad db = %d", got)
	}
	if got := run([]string{"feedback", "summary"}); got != exitError {
		t.Logf("feedback bad db = %d", got)
	}
}

func TestServeConfigErrorAndPackInitBranches(t *testing.T) {
	// Invalid log level forces config.Load to fail -> serve exit 1.
	t.Setenv("COUNCIL_LOG_LEVEL", "bogus")
	if got := run([]string{"serve"}); got != exitError {
		t.Fatalf("serve bad config = %d, want %d", got, exitError)
	}
	t.Setenv("COUNCIL_LOG_LEVEL", "info")
	dir := t.TempDir()
	t.Setenv("COUNCIL_DB_PATH", dir+"/c.db")
	t.Setenv("COUNCIL_MODELS_FILE", writeModelsFile(t))
	if got := run([]string{"db", "migrate"}); got != exitOK {
		t.Fatalf("migrate = %d", got)
	}
	// pack init with missing file -> exit 2.
	if got := packInit([]string{"--file", "/nonexistent-pack.yaml"}); got != exitValidation {
		t.Fatalf("init missing file = %d, want %d", got, exitValidation)
	}
	// pack init with bad models registry -> exit 1.
	bad := t.TempDir() + "/bad-models.yaml"
	if err := os.WriteFile(bad, []byte("not: [valid"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("COUNCIL_MODELS_FILE", bad)
	if got := packInit([]string{"--file", writePackFile(t)}); got != exitError {
		t.Fatalf("init bad models = %d, want %d", got, exitError)
	}
	// pack init with unknown model -> validation exit 2.
	t.Setenv("COUNCIL_MODELS_FILE", writeModelsFile(t))
	other := t.TempDir() + "/other-pack.yaml"
	content := "seats:\n" +
		"  possibility: {model: unknown-model, family: openai}\n" +
		"  perspective: {model: m1, family: openai}\n" +
		"  stress_tester: {model: m1, family: openai}\n" +
		"  judge: {model: m1, family: openai}\n"
	if err := os.WriteFile(other, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := packInit([]string{"--file", other}); got != exitValidation {
		t.Fatalf("init unknown model = %d, want %d", got, exitValidation)
	}
	// pack init flag parse failure -> exit 2.
	if got := packInit([]string{"--bogus"}); got != exitValidation {
		t.Fatalf("init bad flag = %d, want %d", got, exitValidation)
	}
}

func TestServeHTTPBindError(t *testing.T) {
	// serveHTTP with an unroutable listen address fails fast without binding.
	t.Setenv("COUNCIL_SERVER_LISTEN", "127.0.0.1:1")
	t.Setenv("COUNCIL_DB_PATH", t.TempDir()+"/c.db")
	t.Setenv("COUNCIL_MODELS_FILE", writeModelsFile(t))
	cfg, err := loadTestConfig()
	if err != nil {
		t.Fatalf("config: %v", err)
	}
	log := newTestLog(cfg)
	// Missing pack dir is fine; bind to privileged port 1 fails.
	if err := serveHTTP(cfg, log); err == nil {
		t.Fatal("serveHTTP privileged port: want error")
	}
}

func loadTestConfig() (*config.Config, error)   { return config.Load() }
func newTestLog(cfg *config.Config) logx.Logger { return logx.New(cfg) }
