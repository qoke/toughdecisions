package main

import (
	"strings"
)

// SpendRow is one row of the token-spend table (R-06, AC11): the single
// in-code source of truth for which CLI commands spend model calls.
// Name is the dispatched command path ("serve", "harness sentinel", ...).
// Spends=false rows spend nothing. Calls and PerRun describe the spend for
// Spends=true rows and are empty for no-spend rows.
type SpendRow struct {
	Name   string
	Spends bool
	Calls  string
	PerRun string
}

// spendTable is the full table, derived from the actual call sites:
//   - serve: serve.go/serveHTTP per POST /api/requests = 3 views + 1 judge
//     (internal/council ViewSeats + judge), rewrite = 1 call; startup none.
//   - harness sentinel/screen/compare/downstream/weekly: internal/harness
//     (SentinelCount/ScreenCases defaults in internal/config).
//   - graders calibrate: internal/grading Calibrate grades every item once
//     (+1 retry on parse failure).
//   - pack publish: internal/harness publishSeeds generates baselines on
//     both branches (4 cached generations per selection case).
//   - config check --live: exactly 1 gateway call (config_cmd.go).
var spendTable = []SpendRow{
	{Name: "serve", Spends: true, Calls: "4 model calls per request (3 views + 1 judge), 1 per rewrite", PerRun: "per POST /api/requests; no cache reuse"},
	{Name: "harness sentinel", Spends: true, Calls: "4 seats x harness_sentinel_count generations + drift rechecks + 2 pairwise calls", PerRun: "default 16 generations + admitted-graders x harness_calibration_recheck + 2 pairwise"},
	{Name: "harness screen", Spends: true, Calls: "per candidate: harness_screen_cases x 2 generations + 2 grades", PerRun: "default 6 cases x 2 generations + 2 grades per candidate"},
	{Name: "harness compare", Spends: true, Calls: "per candidate: all selection cases x 2 generations + 2 absolute grades x 2 graders + 2 pairwise + 2 fragility picks x 2 generations + 2 pairwise", PerRun: "per candidate over all selection cases"},
	{Name: "harness downstream", Spends: true, Calls: "per case: 3 cached incumbent views + 1 candidate view + 2 judge calls + 2 pairwise + up to 2 cover calls + 2-call fresh judge repeat when close", PerRun: "per case"},
	{Name: "harness weekly", Spends: true, Calls: "sum of the selected steps", PerRun: "per --steps selection (default all)"},
	{Name: "graders calibrate", Spends: true, Calls: "1 call per calibration item per grader (+1 retry on parse failure)", PerRun: "per calibration item per grader"},
	{Name: "pack publish", Spends: true, Calls: "4 cached generations per selection case, on both branches", PerRun: "per publish (baselines generation happens either way)"},
	{Name: "config check --live", Spends: true, Calls: "exactly 1 gateway call", PerRun: "opt-in only, never called by any other path"},
	{Name: "serve startup", Spends: false},
	{Name: "db migrate", Spends: false},
	{Name: "db prune", Spends: false},
	{Name: "pack init", Spends: false},
	{Name: "pack show", Spends: false},
	{Name: "pack rollback", Spends: false},
	{Name: "cases validate", Spends: false},
	{Name: "cases load", Spends: false},
	{Name: "graders status", Spends: false},
	{Name: "flags list", Spends: false},
	{Name: "flags confirm", Spends: false},
	{Name: "flags dismiss", Spends: false},
	{Name: "feedback summary", Spends: false},
	{Name: "harness report", Spends: false},
	{Name: "config show", Spends: false},
	{Name: "config reference", Spends: false},
	{Name: "config diagnose", Spends: false},
	{Name: "config check", Spends: false},
}

// SpendRows returns the full spend table, one row per dispatched command
// path. Step 6 (readme_spend_test.go) consumes the markdown render of this
// table; keep row names stable.
func SpendRows() []SpendRow {
	out := make([]SpendRow, len(spendTable))
	copy(out, spendTable)
	return out
}

// spendByName looks up one row by command path.
func spendByName(name string) (SpendRow, bool) {
	for _, r := range spendTable {
		if r.Name == name {
			return r, true
		}
	}
	return SpendRow{}, false
}

// SpendLine returns the per-command spend line for that command's help
// (Step 3 embeds it). The signature is stable by contract: it names the
// row's own command and states its spend status. Unknown names report
// no spend rather than inventing numbers.
func SpendLine(name string) string {
	r, ok := spendByName(name)
	if !ok || !r.Spends {
		return name + ": no model spend"
	}
	return name + ": spends model calls (" + r.Calls + "; " + r.PerRun + ")"
}

// RenderSpendMarkdown renders the full table as markdown for the README
// (Step 6 consumes it). Every row appears exactly once.
func RenderSpendMarkdown() string {
	var sb strings.Builder
	sb.WriteString("| Command | Spends | Calls | Per run |\n")
	sb.WriteString("| --- | --- | --- | --- |\n")
	for _, r := range spendTable {
		spends := "no"
		if r.Spends {
			spends = "yes"
		}
		sb.WriteString("| " + r.Name + " | " + spends + " | " + r.Calls + " | " + r.PerRun + " |\n")
	}
	return sb.String()
}
