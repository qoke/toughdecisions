package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/qoke/toughdecisions/internal/config"
	"github.com/qoke/toughdecisions/internal/gateway"
	"github.com/qoke/toughdecisions/internal/logx"
	"github.com/qoke/toughdecisions/internal/store"
)

func testEnv(t *testing.T) (dir string) {
	t.Helper()
	dir = t.TempDir()
	t.Setenv("COUNCIL_DB_PATH", filepath.Join(dir, "c.db"))
	t.Setenv("COUNCIL_MODELS_FILE", writeModelsFile(t))
	t.Setenv("COUNCIL_PACK_FILE", writePackFile(t))
	t.Setenv("COUNCIL_CASEPACK_DIR", filepath.Join(dir, "casepack"))
	t.Setenv("COUNCIL_GRADERS_FILE", filepath.Join(dir, "graders.yaml"))
	t.Setenv("COUNCIL_CANDIDATES_FILE", filepath.Join(dir, "candidates.yaml"))
	t.Setenv("COUNCIL_REPORTS_DIR", filepath.Join(dir, "reports"))
	t.Setenv("COUNCIL_LOG_LEVEL", "error")
	return dir
}

func writeCasepackDir(t *testing.T, dir string) {
	t.Helper()
	famDir := filepath.Join(dir, "casepack", "families")
	if err := os.MkdirAll(famDir, 0o755); err != nil {
		t.Fatal(err)
	}
	fam := `key: F001
name: "safety family"
split: selection
tags: [safety, sentinel, hard]
seats_relevant: [possibility]
acceptance:
  must_notice: ["deadline"]
  cannot_assume: []
  material_errors: []
  variant_expectations: "base behaves"
planted_issues:
  - {id: F001-I1, text: "deadline matters", kind: constraint}
cases:
  - key: F001-base
    variant: base
    card: {decision: "d", context: "c", priorities: "p", unusual: "u", history: "h", deadline: "soon", style: "s"}
    messages: [{sender: them, text: "hello"}]
    question: "what now?"
`
	if err := os.WriteFile(filepath.Join(famDir, "f001.yaml"), []byte(fam), 0o644); err != nil {
		t.Fatal(err)
	}
	fam2 := `key: F002
name: "control family"
split: selection
tags: [control, sentinel, hard]
seats_relevant: [possibility]
acceptance:
  must_notice: ["context"]
  cannot_assume: []
  material_errors: []
  variant_expectations: "base behaves"
planted_issues:
  - {id: F002-I1, text: "context matters", kind: fact}
cases:
  - key: F002-base
    variant: base
    card: {decision: "d", context: "c", priorities: "p", unusual: "u", history: "h", deadline: "soon", style: "s"}
    messages: [{sender: me, text: "hi"}]
    question: "what next?"
`
	if err := os.WriteFile(filepath.Join(famDir, "f002.yaml"), []byte(fam2), 0o644); err != nil {
		t.Fatal(err)
	}
	cal := `items:
  - key: cal1
    case: F001-base
    seat: possibility
    response_text: "reference answer"
    category: grounded_support
    human_scores: {grounding_calibration: 3, context_values_fidelity: 3, decision_insight: 3, practical_robustness: 3, role_execution: 3}
    human_flags: []
    notes: ""
`
	if err := os.WriteFile(filepath.Join(dir, "casepack", "calibration.yaml"), []byte(cal), 0o644); err != nil {
		t.Fatal(err)
	}
}

func writeGradersFile(t *testing.T, dir string) {
	t.Helper()
	content := "graders:\n" +
		"  - {key: grader-a, model: m1, family: openai, role: selection, max_output_tokens: 2000}\n"
	if err := os.WriteFile(filepath.Join(dir, "graders.yaml"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "candidates.yaml"), []byte("candidates: []\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func gradeContent(scores map[string]int) string {
	return `{"scores":{"grounding_calibration":3,"context_values_fidelity":3,"decision_insight":3,"practical_robustness":3,"role_execution":3},"supporting_passages":{"decision_insight":"p"},"notes_check":{"noticed":[],"missed":[],"beyond_notes":[]}}`
}

func TestCasesValidateLoadTable(t *testing.T) {
	dir := testEnv(t)
	writeCasepackDir(t, dir)
	if got := run([]string{"db", "migrate"}); got != exitOK {
		t.Fatalf("migrate = %d", got)
	}
	if got := run([]string{"pack", "init"}); got != exitOK {
		t.Fatalf("pack init = %d", got)
	}
	for _, args := range [][]string{{"cases", "validate"}, {"cases", "load"}} {
		if got := run(args); got != exitOK {
			t.Fatalf("%v = %d, want 0", args, got)
		}
	}
	// invalid: point at empty dir -> load error exit 1.
	t.Setenv("COUNCIL_CASEPACK_DIR", t.TempDir())
	if got := run([]string{"cases", "validate"}); got != exitError {
		t.Fatalf("validate bad dir = %d, want 1", got)
	}
	if got := run([]string{"cases", "validate", "--bogus"}); got != exitValidation {
		t.Fatalf("validate bogus = %d, want 2", got)
	}
	if got := run([]string{"cases", "load", "--bogus"}); got != exitValidation {
		t.Fatalf("load bogus = %d, want 2", got)
	}
}

func TestGradersStatusCalibrateTable(t *testing.T) {
	dir := testEnv(t)
	writeCasepackDir(t, dir)
	writeGradersFile(t, dir)
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
		"m1": {{Content: gradeContent(nil)}},
	})
	old := gatewayFactory
	gatewayFactory = func(_ *config.Config, _ logx.Logger) gateway.Client { return fake }
	defer func() { gatewayFactory = old }()
	if got := run([]string{"graders", "status"}); got != exitOK {
		t.Fatalf("status = %d", got)
	}
	if got := run([]string{"graders", "calibrate", "--all"}); got != exitOK {
		t.Fatalf("calibrate --all = %d", got)
	}
	if got := run([]string{"graders", "calibrate"}); got != exitValidation {
		t.Fatalf("calibrate no flag = %d, want 2", got)
	}
	if got := run([]string{"graders", "calibrate", "--grader", "nope"}); got != exitValidation {
		t.Fatalf("calibrate unknown = %d, want 2", got)
	}
	if got := run([]string{"graders", "calibrate", "--grader", "a", "--all"}); got != exitValidation {
		t.Fatalf("calibrate both = %d, want 2", got)
	}
	if got := run([]string{"graders", "status", "--bogus"}); got != exitValidation {
		t.Fatalf("status bogus = %d, want 2", got)
	}
}

func TestFlagsListConfirmDismissTable(t *testing.T) {
	dir := testEnv(t)
	_ = dir
	if got := run([]string{"db", "migrate"}); got != exitOK {
		t.Fatalf("migrate = %d", got)
	}
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(cfg.DBPath())
	if err != nil {
		t.Fatal(err)
	}
	ins, err := db.InsertFlag(&store.Flag{GradeID: "g1", ResponseID: "r1", Type: "fabrication", Passage: "p", Violated: "v", Status: "open"})
	if err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
	if got := run([]string{"flags", "list"}); got != exitOK {
		t.Fatalf("list = %d", got)
	}
	if got := run([]string{"flags", "list", "--status", "bogus"}); got != exitValidation {
		t.Fatalf("list bogus status = %d, want 2", got)
	}
	if got := run([]string{"flags", "confirm", ins.ID, "--note", "yes"}); got != exitOK {
		t.Fatalf("confirm = %d", got)
	}
	if got := run([]string{"flags", "dismiss", ins.ID, "--note", "no"}); got != exitOK {
		t.Fatalf("dismiss = %d", got)
	}
	if got := run([]string{"flags", "confirm", ins.ID}); got != exitValidation {
		t.Fatalf("confirm no note = %d, want 2", got)
	}
	if got := run([]string{"flags", "confirm"}); got != exitValidation {
		t.Fatalf("confirm no id = %d, want 2", got)
	}
	if got := run([]string{"flags", "confirm", "missing", "--note", "x"}); got != exitError {
		t.Fatalf("confirm missing = %d, want 1", got)
	}
	if got := run([]string{"flags", "list", "--bogus"}); got != exitValidation {
		t.Fatalf("list bogus = %d, want 2", got)
	}
}

func TestDbPruneKeepsFeedbackCounts(t *testing.T) {
	dir := testEnv(t)
	_ = dir
	if got := run([]string{"db", "migrate"}); got != exitOK {
		t.Fatalf("migrate = %d", got)
	}
	if got := run([]string{"pack", "init"}); got != exitOK {
		t.Fatalf("pack init = %d", got)
	}
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(cfg.DBPath())
	if err != nil {
		t.Fatal(err)
	}
	th, _ := db.CreateThread("t")
	card, _ := db.InsertCard(th.ID, `{"decision":"d"}`)
	pk, _ := db.InsertPack("active", `{}`, "pp", "")
	ancient := time.Now().AddDate(0, 0, -100).UTC().Format(time.RFC3339)
	oldReq, err := db.InsertRequest(&store.Request{ThreadID: th.ID, CardID: card.ID, PackID: pk.ID, SnapshotJSON: "{}", InputHash: "ih", State: "complete", ViewsDeadlineAt: ancient, CreatedAt: ancient})
	if err != nil {
		t.Fatal(err)
	}
	keepReq, err := db.InsertRequest(&store.Request{ThreadID: th.ID, CardID: card.ID, PackID: pk.ID, SnapshotJSON: "{}", InputHash: "ih", State: "complete", ViewsDeadlineAt: ancient, CreatedAt: ancient})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.InsertFeedback(keepReq.ID, "too_slow", ""); err != nil {
		t.Fatal(err)
	}
	_ = oldReq
	_ = db.Close()
	if got := run([]string{"db", "prune", "--older-than", "90"}); got != exitOK {
		t.Fatalf("prune = %d", got)
	}
	db2, err := store.Open(cfg.DBPath())
	if err != nil {
		t.Fatal(err)
	}
	defer db2.Close()
	counts, err := db2.FeedbackTagCounts("2000-01-01T00:00:00Z")
	if err != nil {
		t.Fatal(err)
	}
	if counts["too_slow"] != 1 {
		t.Fatalf("counts = %v, want too_slow=1", counts)
	}
}

func TestHarnessReportAndWeeklyValidation(t *testing.T) {
	dir := testEnv(t)
	writeCasepackDir(t, dir)
	writeGradersFile(t, dir)
	if got := run([]string{"db", "migrate"}); got != exitOK {
		t.Fatalf("migrate = %d", got)
	}
	if got := run([]string{"pack", "init"}); got != exitOK {
		t.Fatalf("pack init = %d", got)
	}
	if got := run([]string{"cases", "load"}); got != exitOK {
		t.Fatalf("cases load = %d", got)
	}
	old := gatewayFactory
	gatewayFactory = func(_ *config.Config, _ logx.Logger) gateway.Client { return gateway.NewFake(nil) }
	defer func() { gatewayFactory = old }()
	// report needs --run.
	if got := run([]string{"harness", "report"}); got != exitValidation {
		t.Fatalf("report no run = %d, want 2", got)
	}
	// create a run row directly, then report on it.
	cfg, _ := config.Load()
	db, _ := store.Open(cfg.DBPath())
	rr, err := db.CreateHarnessRun("weekly", "pack", "{}")
	if err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
	if got := run([]string{"harness", "report", "--run", rr.ID}); got != exitOK {
		t.Fatalf("report = %d", got)
	}
	if got := run([]string{"harness", "weekly", "--steps", "bogus"}); got != exitValidation {
		t.Fatalf("weekly bogus steps = %d, want 2", got)
	}
	if got := run([]string{"harness", "weekly", "--bogus"}); got != exitValidation {
		t.Fatalf("weekly bogus = %d, want 2", got)
	}
	if got := run([]string{"harness", "report", "--bogus"}); got != exitValidation {
		t.Fatalf("report bogus = %d, want 2", got)
	}
}

func TestPackPublishChecklistRefusal(t *testing.T) {
	dir := testEnv(t)
	writeCasepackDir(t, dir)
	writeGradersFile(t, dir)
	if got := run([]string{"db", "migrate"}); got != exitOK {
		t.Fatalf("migrate = %d", got)
	}
	if got := run([]string{"pack", "init"}); got != exitOK {
		t.Fatalf("pack init = %d", got)
	}
	old := gatewayFactory
	gatewayFactory = func(_ *config.Config, _ logx.Logger) gateway.Client { return gateway.NewFake(nil) }
	defer func() { gatewayFactory = old }()
	// publish with missing run -> error exit 1.
	if got := run([]string{"pack", "publish", "--run", "missing", "--candidate", "c"}); got != exitError {
		t.Fatalf("publish missing = %d, want 1", got)
	}
}

func TestHarnessWeeklyReportOnlySuccess(t *testing.T) {
	dir := testEnv(t)
	writeCasepackDir(t, dir)
	writeGradersFile(t, dir)
	if got := run([]string{"db", "migrate"}); got != exitOK {
		t.Fatalf("migrate = %d", got)
	}
	if got := run([]string{"pack", "init"}); got != exitOK {
		t.Fatalf("pack init = %d", got)
	}
	if got := run([]string{"cases", "load"}); got != exitOK {
		t.Fatalf("cases load = %d", got)
	}
	old := gatewayFactory
	gatewayFactory = func(_ *config.Config, _ logx.Logger) gateway.Client { return gateway.NewFake(nil) }
	defer func() { gatewayFactory = old }()
	// Report-only weekly runs fully offline: no sentinel, no candidates.
	if got := run([]string{"harness", "weekly", "--steps", "report"}); got != exitOK {
		t.Fatalf("weekly report-only = %d, want 0", got)
	}
	// Baselines-only publish fills baselines for the active pack offline.
	if got := run([]string{"pack", "publish", "--baselines-only"}); got != exitOK {
		t.Fatalf("publish baselines-only = %d, want 0", got)
	}
}

func TestCliErrorBranches(t *testing.T) {
	dir := testEnv(t)
	writeCasepackDir(t, dir)
	writeGradersFile(t, dir)
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
		"m1": {{Content: gradeContent(nil)}, {Content: gradeContent(nil)}},
	})
	old := gatewayFactory
	gatewayFactory = func(_ *config.Config, _ logx.Logger) gateway.Client { return fake }
	defer func() { gatewayFactory = old }()
	// Single-grader calibrate exercises the --grader path.
	if got := run([]string{"graders", "calibrate", "--grader", "grader-a"}); got != exitOK {
		t.Fatalf("calibrate single = %d, want 0", got)
	}
	// Graders status with a missing file -> error exit 1.
	t.Setenv("COUNCIL_GRADERS_FILE", filepath.Join(dir, "missing.yaml"))
	if got := run([]string{"graders", "status"}); got != exitError {
		t.Fatalf("status missing file = %d, want 1", got)
	}
	if got := run([]string{"graders", "calibrate", "--all"}); got != exitError {
		t.Fatalf("calibrate missing file = %d, want 1", got)
	}
	// db prune with no flag uses retention_production_days default.
	if got := run([]string{"db", "prune"}); got != exitOK {
		t.Fatalf("prune default = %d, want 0", got)
	}
	// flags list with run filter.
	if got := run([]string{"flags", "list", "--run", "nope"}); got != exitOK {
		t.Fatalf("list run filter = %d, want 0", got)
	}
	if got := run([]string{"flags", "dismiss", "x", "--note=x", "--bogus"}); got != exitValidation {
		t.Fatalf("dismiss bogus flag = %d, want 2", got)
	}
}

func TestHarnessStepSubcommandsTable(t *testing.T) {
	dir := testEnv(t)
	writeCasepackDir(t, dir)
	writeGradersFile(t, dir)
	if got := run([]string{"db", "migrate"}); got != exitOK {
		t.Fatalf("migrate = %d", got)
	}
	if got := run([]string{"pack", "init"}); got != exitOK {
		t.Fatalf("pack init = %d", got)
	}
	if got := run([]string{"cases", "load"}); got != exitOK {
		t.Fatalf("cases load = %d", got)
	}
	// Sentinel with no baselines refuses (exit 1), never a silent stub.
	old := gatewayFactory
	gatewayFactory = func(_ *config.Config, _ logx.Logger) gateway.Client { return gateway.NewFake(nil) }
	defer func() { gatewayFactory = old }()
	if got := run([]string{"harness", "sentinel"}); got != exitError {
		t.Fatalf("sentinel no baselines = %d, want 1", got)
	}
	if got := run([]string{"harness", "sentinel", "extra"}); got != exitValidation {
		t.Fatalf("sentinel extra arg = %d, want 2", got)
	}
	if got := run([]string{"harness", "sentinel", "--bogus"}); got != exitValidation {
		t.Fatalf("sentinel bogus = %d, want 2", got)
	}
	// Screen with empty candidates errors (no silent success).
	if got := run([]string{"harness", "screen"}); got != exitError {
		t.Fatalf("screen empty candidates = %d, want 1", got)
	}
	if got := run([]string{"harness", "screen", "extra"}); got != exitValidation {
		t.Fatalf("screen extra arg = %d, want 2", got)
	}
	// Compare/downstream flag validation (exit 2) without touching the gateway.
	for _, args := range [][]string{
		{"harness", "compare"},
		{"harness", "compare", "--candidate", "nope"},
		{"harness", "compare", "--bogus"},
		{"harness", "downstream"},
		{"harness", "downstream", "--candidate", "c"},
		{"harness", "downstream", "--run", "r"},
		{"harness", "downstream", "--candidate", "nope", "--run", "r"},
		{"harness", "downstream", "--bogus"},
	} {
		if got := run(args); got != exitValidation {
			t.Fatalf("%v = %d, want %d", args, got, exitValidation)
		}
	}
	_ = dir
}

func TestHarnessSentinelSuccessOffline(t *testing.T) {
	dir := testEnv(t)
	writeCasepackDir(t, dir)
	// Views run on m1 (pack seats); graders run on distinct models so each
	// Fake script queue holds only one content kind (views vs grades).
	models := "models:\n" +
		"  - id: m1\n    family: openai\n    expected_response_model_prefixes: [\"m1\"]\n" +
		"    supports: {temperature: true, top_p: true, reasoning_effort: true, json_schema: true, json_object: true}\n" +
		"  - id: mga\n    family: openai\n    expected_response_model_prefixes: [\"mga\"]\n" +
		"    supports: {temperature: true, top_p: true, reasoning_effort: true, json_schema: true, json_object: true}\n" +
		"  - id: mgb\n    family: anthropic\n    expected_response_model_prefixes: [\"mgb\"]\n" +
		"    supports: {temperature: true, top_p: true, reasoning_effort: true, json_schema: true, json_object: true}\n"
	mp := t.TempDir() + "/models.yaml"
	if err := os.WriteFile(mp, []byte(models), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("COUNCIL_MODELS_FILE", mp)
	graders := "graders:\n" +
		"  - {key: grader-a, model: mga, family: openai, role: selection, max_output_tokens: 2000}\n" +
		"  - {key: grader-b, model: mgb, family: anthropic, role: selection, max_output_tokens: 2000}\n"
	if err := os.WriteFile(filepath.Join(dir, "graders.yaml"), []byte(graders), 0o644); err != nil {
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
	viewJSON := `{"urgent_danger":{"present":false},"qualification":"q","suggested_reply":"r","decisive_insight":"i","tradeoff_or_objection":"t","depends_on":"d","fallback":"f"}`
	judgeJSON := `{"urgent_danger":{"present":false},"qualification":"q","recommended_reply":"r","why":"w","accepted_cost":"c","next":{"immediate":"i","forward":"f"},"change_course_if":"cc"}`
	gradeJSON := `{"scores":{"grounding_calibration":3,"context_values_fidelity":3,"decision_insight":3,"practical_robustness":3,"role_execution":3},"supporting_passages":{"decision_insight":"p"},"notes_check":{"noticed":[],"missed":[],"beyond_notes":[]}}`
	pairJSON := `{"verdict":"tie","margin":"clear","consequential_difference":"d"}`
	views := []gateway.Step{}
	for i := 0; i < 200; i++ {
		views = append(views, gateway.Step{Content: viewJSON})
	}
	views = append(views, gateway.Step{Content: judgeJSON}, gateway.Step{Content: judgeJSON})
	grades := []gateway.Step{}
	for i := 0; i < 120; i++ {
		grades = append(grades, gateway.Step{Content: gradeJSON}, gateway.Step{Content: pairJSON})
	}
	fake := gateway.NewFake(map[string][]gateway.Step{
		"m1":  views,
		"mga": append([]gateway.Step{}, grades...),
		"mgb": append([]gateway.Step{}, grades...),
	})
	old := gatewayFactory
	gatewayFactory = func(_ *config.Config, _ logx.Logger) gateway.Client { return fake }
	defer func() { gatewayFactory = old }()
	if got := run([]string{"graders", "calibrate", "--all"}); got != exitOK {
		t.Fatalf("calibrate = %d", got)
	}
	if got := run([]string{"pack", "publish", "--baselines-only"}); got != exitOK {
		t.Fatalf("baselines = %d", got)
	}
	if got := run([]string{"harness", "sentinel"}); got != exitOK {
		t.Fatalf("sentinel = %d, want 0", got)
	}
}

func TestHarnessCompareDownstreamSuccessOffline(t *testing.T) {
	dir := testEnv(t)
	writeCasepackDir(t, dir)
	// Screen selects development-split families; the shared fixture only
	// loads selection families, so add one development family here.
	devFam := `key: FDEV
name: "dev family"
split: development
tags: [priority]
seats_relevant: [possibility]
acceptance:
  must_notice: ["deadline"]
  cannot_assume: []
  material_errors: []
  variant_expectations: "base behaves"
planted_issues: []
cases:
  - key: FDEV-base
    variant: base
    card: {decision: "d", context: "c", priorities: "p", unusual: "u", history: "h", deadline: "soon", style: "s"}
    messages: [{sender: them, text: "hello"}]
    question: "what now?"
`
	if err := os.WriteFile(filepath.Join(dir, "casepack", "families", "fdev.yaml"), []byte(devFam), 0o644); err != nil {
		t.Fatal(err)
	}
	// Views on m1; graders on distinct models so each Fake script queue
	// holds only one content kind.
	models := "models:\n" +
		"  - id: m1\n    family: openai\n    expected_response_model_prefixes: [\"m1\"]\n" +
		"    supports: {temperature: true, top_p: true, reasoning_effort: true, json_schema: true, json_object: true}\n" +
		"  - id: mga\n    family: openai\n    expected_response_model_prefixes: [\"mga\"]\n" +
		"    supports: {temperature: true, top_p: true, reasoning_effort: true, json_schema: true, json_object: true}\n" +
		"  - id: mgb\n    family: anthropic\n    expected_response_model_prefixes: [\"mgb\"]\n" +
		"    supports: {temperature: true, top_p: true, reasoning_effort: true, json_schema: true, json_object: true}\n" +
		"  - id: mgs\n    family: other\n    expected_response_model_prefixes: [\"mgs\"]\n" +
		"    supports: {temperature: true, top_p: true, reasoning_effort: true, json_schema: true, json_object: true}\n"
	mp := t.TempDir() + "/models.yaml"
	if err := os.WriteFile(mp, []byte(models), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("COUNCIL_MODELS_FILE", mp)
	// Candidates file with one possibility-seat challenger on model m1.
	cands := "candidates:\n  - {key: cand-a, seat: possibility, model: m1, family: openai, finalist: auto}\n"
	if err := os.WriteFile(filepath.Join(dir, "candidates.yaml"), []byte(cands), 0o644); err != nil {
		t.Fatal(err)
	}
	graders := "graders:\n" +
		"  - {key: grader-a, model: mga, family: openai, role: selection, max_output_tokens: 2000}\n" +
		"  - {key: grader-b, model: mgb, family: anthropic, role: selection, max_output_tokens: 2000}\n" +
		"  - {key: grader-s, model: mgs, family: other, role: screening, max_output_tokens: 2000}\n"
	if err := os.WriteFile(filepath.Join(dir, "graders.yaml"), []byte(graders), 0o644); err != nil {
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
	viewJSON := `{"urgent_danger":{"present":false},"qualification":"q","suggested_reply":"r","decisive_insight":"i","tradeoff_or_objection":"t","depends_on":"d","fallback":"f"}`
	judgeJSON := `{"urgent_danger":{"present":false},"qualification":"q","recommended_reply":"r","why":"w","accepted_cost":"c","next":{"immediate":"i","forward":"f"},"change_course_if":"cc"}`
	gradeJSON := `{"scores":{"grounding_calibration":3,"context_values_fidelity":3,"decision_insight":3,"practical_robustness":3,"role_execution":3},"supporting_passages":{"decision_insight":"p"},"notes_check":{"noticed":[],"missed":[],"beyond_notes":[]}}`
	pairJSON := `{"verdict":"tie","margin":"clear","consequential_difference":"d"}`
	views := []gateway.Step{}
	for i := 0; i < 200; i++ {
		views = append(views, gateway.Step{Content: viewJSON})
	}
	views = append(views, gateway.Step{Content: judgeJSON}, gateway.Step{Content: judgeJSON})
	grades := []gateway.Step{}
	for i := 0; i < 160; i++ {
		grades = append(grades, gateway.Step{Content: gradeJSON}, gateway.Step{Content: pairJSON})
	}
	fake := gateway.NewFake(map[string][]gateway.Step{
		"m1":  views,
		"mga": append([]gateway.Step{}, grades...),
		"mgb": append([]gateway.Step{}, grades...),
		"mgs": append([]gateway.Step{}, grades...),
	})
	old := gatewayFactory
	gatewayFactory = func(_ *config.Config, _ logx.Logger) gateway.Client { return fake }
	defer func() { gatewayFactory = old }()
	if got := run([]string{"graders", "calibrate", "--all"}); got != exitOK {
		t.Fatalf("calibrate = %d", got)
	}
	if got := run([]string{"harness", "screen"}); got != exitOK {
		t.Fatalf("screen = %d, want 0", got)
	}
	if got := run([]string{"harness", "compare", "--candidate", "cand-a"}); got != exitOK {
		t.Fatalf("compare = %d, want 0", got)
	}
	// --fresh compare exercises the fragility re-run path offline.
	if got := run([]string{"harness", "compare", "--candidate", "cand-a", "--fresh"}); got != exitOK {
		t.Fatalf("compare fresh = %d, want 0", got)
	}
	// --fresh screen exercises the fresh-response branch offline.
	if got := run([]string{"harness", "screen", "--fresh"}); got != exitOK {
		t.Fatalf("screen fresh = %d, want 0", got)
	}
	// Downstream needs the compare run id. The CLI prints
	// "harness compare: ok (run=<id> ...)", but run() returns only the
	// exit code, so resolve the one candidates row compare just wrote by
	// scanning run ids directly (covers both modernc and cgo sqlite).
	cfg, _ := config.Load()
	db, _ := store.Open(cfg.DBPath())
	compareRunID := latestCandidateRunID(t, db)
	_ = db.Close()
	// Downstream reuses the compare run id so Promotion can read both
	// stored rows; success prints run/candidate/net/promote.
	if got := run([]string{"harness", "downstream", "--candidate", "cand-a", "--run", compareRunID}); got != exitOK {
		t.Fatalf("downstream = %d, want 0", got)
	}
	// Downstream on a fresh (empty) run id fails loudly, never silently.
	if got := run([]string{"harness", "downstream", "--candidate", "cand-a", "--run", "no-such-run"}); got != exitError {
		t.Fatalf("downstream bad run = %d, want 1", got)
	}
}

func latestCandidateRunID(t *testing.T, db *store.DB) string {
	t.Helper()
	id, err := db.LatestCandidateRunID()
	if err != nil {
		t.Fatalf("latest candidate run: %v", err)
	}
	if id == "" {
		t.Fatal("no candidate rows; want one from harness compare")
	}
	return id
}

func TestHarnessRunnerErrorBranches(t *testing.T) {
	dir := testEnv(t)
	writeCasepackDir(t, dir)
	writeGradersFile(t, dir)
	if got := run([]string{"db", "migrate"}); got != exitOK {
		t.Fatalf("migrate = %d", got)
	}
	if got := run([]string{"pack", "init"}); got != exitOK {
		t.Fatalf("pack init = %d", got)
	}
	if got := run([]string{"cases", "load"}); got != exitOK {
		t.Fatalf("cases load = %d", got)
	}
	// Broken models registry: every harness entry exits 1 via openHarnessRunner.
	bad := filepath.Join(dir, "bad-models.yaml")
	if err := os.WriteFile(bad, []byte("not: [valid"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("COUNCIL_MODELS_FILE", bad)
	for _, args := range [][]string{
		{"harness", "sentinel"},
		{"harness", "screen"},
		{"harness", "compare", "--candidate", "x"},
		{"harness", "downstream", "--candidate", "x", "--run", "y"},
		{"harness", "weekly", "--steps", "report"},
		{"harness", "report", "--run", "r"},
	} {
		if got := run(args); got != exitError {
			t.Fatalf("%v = %d, want 1", args, got)
		}
	}
}
