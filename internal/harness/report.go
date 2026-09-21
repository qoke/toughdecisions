package harness

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/qoke/toughdecisions/internal/pack"
	"github.com/qoke/toughdecisions/internal/store"
)

// ReportOptions selects the evidence aggregated into one markdown report.
// Sub-run ids come from the weekly orchestration (each step owns its run
// row); empty ids render their section as not applicable. Stats carries a
// pre-rendered Runner.StatsJSON snapshot; Summary is the webhook summary
// line. Notify gates the webhook POST.
type ReportOptions struct {
	RunID            string
	SentinelRunID    string
	ScreenRunID      string
	CompareRunIDs    []string
	DownstreamRunIDs []string
	Stats            string
	Summary          string
	Notify           bool
	Skipped          []string
	Blocked          string
}

// ReportResult is the written report.
type ReportResult struct {
	RunID    string
	Path     string
	Markdown string
	Notified bool
}

// Report writes reports/<run_id>.md with every §12.8 section in order,
// pulling the run's sentinel/screen/compare/downstream/promotion evidence.
// Sections with no evidence state they were not applicable. When Notify is
// set and notify_webhook_url is configured it POSTs
// {run_id, summary, report_markdown}; a webhook failure is logged and
// NEVER fails the report.
func (r *Runner) Report(ctx context.Context, opts ReportOptions) (*ReportResult, error) {
	if opts.RunID == "" {
		return nil, fmt.Errorf("harness: report needs a run id")
	}
	var b strings.Builder
	r.writeRunSummary(&b, opts)
	r.writeDrift(&b, opts)
	r.writeRecheck(&b, opts)
	r.writeScreening(&b, opts)
	r.writeCompare(&b, opts)
	r.writeDownstream(&b, opts)
	r.writeCoverage(&b, opts)
	r.writeChecklist(&b, opts)
	r.writeOpenFlags(&b, opts)
	r.writeProductionWeek(&b)
	r.writeFailures(&b, opts)
	md := b.String()
	dir := r.cfg.ReportsDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("harness: create reports dir: %w", err)
	}
	path := filepath.Join(dir, opts.RunID+".md")
	if err := os.WriteFile(path, []byte(md), 0o644); err != nil {
		return nil, fmt.Errorf("harness: write report: %w", err)
	}
	res := &ReportResult{RunID: opts.RunID, Path: path, Markdown: md}
	if opts.Notify {
		res.Notified = r.notifyWebhook(ctx, opts.RunID, opts.Summary, md)
	}
	return res, nil
}

// notifyWebhook POSTs {run_id, summary, report_markdown}. Any failure is
// logged and returns false; it never fails the run nor suppresses the
// already-written report.
func (r *Runner) notifyWebhook(ctx context.Context, runID, summary, md string) bool {
	url := strings.TrimSpace(r.cfg.NotifyWebhookURL())
	if url == "" {
		return false
	}
	raw, _ := json.Marshal(map[string]string{
		"run_id": runID, "summary": summary, "report_markdown": md,
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(raw))
	if err != nil {
		r.log.Warn("harness: webhook request failed", "run_id", runID, "err", err)
		return false
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		r.log.Warn("harness: webhook post failed", "run_id", runID, "err", err)
		return false
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		r.log.Warn("harness: webhook post failed", "run_id", runID, "status", resp.Status)
		return false
	}
	return true
}

func (r *Runner) writeRunSummary(b *strings.Builder, opts ReportOptions) {
	b.WriteString("# Harness report " + opts.RunID + "\n\n## Run summary\n\n")
	run, err := r.db.GetHarnessRun(opts.RunID)
	packID := ""
	duration := "not applicable"
	if err == nil {
		packID = run.PackID
		duration = runDuration(run)
	}
	hashes := "not applicable"
	if packID != "" {
		if pk, err := r.db.GetPack(packID); err == nil {
			hashes = "prompt_pack_hash=" + pk.PromptPackHash
		}
	}
	cost := r.totalCost(opts)
	fmt.Fprintf(b, "- pack id: %s\n- hashes: %s\n- cost: %s\n- duration: %s\n",
		orNA(packID), hashes, cost, duration)
	if len(opts.Skipped) > 0 {
		fmt.Fprintf(b, "- skipped: %s\n", strings.Join(opts.Skipped, ", "))
	}
	if opts.Blocked != "" {
		fmt.Fprintf(b, "- blocked: %s\n", opts.Blocked)
	}
	b.WriteString("\n")
}

func (r *Runner) totalCost(opts ReportOptions) string {
	total := 0.0
	n := 0
	for _, id := range append(append([]string{}, opts.CompareRunIDs...), opts.DownstreamRunIDs...) {
		cands, err := r.db.ListCandidatesByRun(id)
		if err != nil {
			continue
		}
		for _, c := range cands {
			if c.CompareResultJSON == nil {
				continue
			}
			var cmp CompareResult
			if err := json.Unmarshal([]byte(*c.CompareResultJSON), &cmp); err != nil {
				continue
			}
			total += cmp.MeanCostCand * float64(len(cmp.Cases))
			n += len(cmp.Cases)
		}
	}
	if n == 0 {
		return "not applicable"
	}
	return fmt.Sprintf("%.6f USD over %d cases", total, n)
}

func (r *Runner) writeDrift(b *strings.Builder, opts ReportOptions) {
	b.WriteString("## Incumbent drift\n\n")
	if opts.SentinelRunID == "" {
		b.WriteString("not applicable: no sentinel run\n\n")
		return
	}
	rows, err := r.db.ListSentinelResultsByRun(opts.SentinelRunID)
	if err != nil || len(rows) == 0 {
		b.WriteString("not applicable: no sentinel results\n\n")
		return
	}
	for _, row := range rows {
		var v map[string]any
		_ = json.Unmarshal([]byte(row.VerdictsJSON), &v)
		fmt.Fprintf(b, "- %s x %s: regression=%v latency=%dms verdicts=%s\n",
			row.Seat, row.CaseID, row.Regression, row.LatencyMs, row.VerdictsJSON)
	}
	b.WriteString("\nInvestigate list:\n\n")
	run, err := r.db.GetHarnessRun(opts.SentinelRunID)
	inv := []string{}
	if err == nil && run.SummaryJSON != nil {
		var s SentinelSummary
		if err := json.Unmarshal([]byte(*run.SummaryJSON), &s); err == nil {
			inv = s.Investigate
		}
	}
	if len(inv) == 0 {
		b.WriteString("not applicable: nothing to investigate\n\n")
		return
	}
	for _, line := range inv {
		b.WriteString("- " + line + "\n")
	}
	b.WriteString("\n")
}

func (r *Runner) writeRecheck(b *strings.Builder, opts ReportOptions) {
	b.WriteString("## Grader recheck\n\n")
	if opts.SentinelRunID == "" {
		b.WriteString("not applicable: no sentinel run\n\n")
		return
	}
	run, err := r.db.GetHarnessRun(opts.SentinelRunID)
	if err != nil || run.SummaryJSON == nil {
		b.WriteString("not applicable: no recheck recorded\n\n")
		return
	}
	var s SentinelSummary
	if err := json.Unmarshal([]byte(*run.SummaryJSON), &s); err != nil || len(s.Recheck) == 0 {
		b.WriteString("not applicable: no recheck recorded\n\n")
		return
	}
	keys := make([]string, 0, len(s.Recheck))
	for k := range s.Recheck {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		fmt.Fprintf(b, "- %s: reversals=%d\n", k, s.Recheck[k])
	}
	b.WriteString("\n")
}

func (r *Runner) writeScreening(b *strings.Builder, opts ReportOptions) {
	b.WriteString("## Screening table\n\n")
	if opts.ScreenRunID == "" {
		b.WriteString("not applicable: screening skipped\n\n")
		return
	}
	cands, err := r.db.ListCandidatesByRun(opts.ScreenRunID)
	if err != nil || len(cands) == 0 {
		b.WriteString("not applicable: no screening candidates\n\n")
		return
	}
	for _, c := range cands {
		if c.ScreenResultJSON == nil {
			fmt.Fprintf(b, "- %s: not applicable: no screen result\n", c.CandidateKey)
			continue
		}
		var res ScreenResult
		if err := json.Unmarshal([]byte(*c.ScreenResultJSON), &res); err != nil {
			fmt.Fprintf(b, "- %s: not applicable: unreadable screen result\n", c.CandidateKey)
			continue
		}
		fmt.Fprintf(b, "- %s: mean=%.2f incumbent=%.2f wins=%d/%d flags=%d passed=%v finalist=%v weakest=%s\n",
			c.CandidateKey, res.MeanTotal, res.IncMeanTotal, res.Wins, res.Cases,
			res.OpenFlags, res.Passed, res.Finalist, res.Weakest)
	}
	b.WriteString("\n")
}

func (r *Runner) writeCompare(b *strings.Builder, opts ReportOptions) {
	b.WriteString("## Compare per family\n\n")
	found := false
	for _, id := range opts.CompareRunIDs {
		cands, err := r.db.ListCandidatesByRun(id)
		if err != nil {
			continue
		}
		for _, c := range cands {
			if c.CompareResultJSON == nil {
				continue
			}
			var cmp CompareResult
			if err := json.Unmarshal([]byte(*c.CompareResultJSON), &cmp); err != nil {
				continue
			}
			found = true
			byFam := map[string][]CaseVerdict{}
			for _, cv := range cmp.Cases {
				byFam[cv.FamilyKey] = append(byFam[cv.FamilyKey], cv)
			}
			fams := make([]string, 0, len(byFam))
			for f := range byFam {
				fams = append(fams, f)
			}
			sort.Strings(fams)
			for _, f := range fams {
				wins, losses, ties := 0, 0, 0
				var diffs []string
				for _, cv := range byFam[f] {
					if !cv.Agreed {
						ties++
						continue
					}
					switch cv.Verdict {
					case "left":
						wins++
					case "right":
						losses++
					default:
						ties++
					}
					if cv.Difference != "" {
						diffs = append(diffs, cv.CaseKey+": "+cv.Difference)
					}
				}
				fmt.Fprintf(b, "### %s / %s\n\n- wins=%d losses=%d ties=%d\n",
					cmp.CandidateKey, f, wins, losses, ties)
				for _, d := range diffs {
					b.WriteString("- difference " + d + "\n")
				}
				b.WriteString("\n")
			}
			fmt.Fprintf(b, "- %s: fragile=%v p50cand=%dms p95cand=%dms p50inc=%dms p95inc=%dms meancost=%.6f inccost=%.6f timeouts=%d\n\n",
				cmp.CandidateKey, cmp.Fragile, cmp.P50Cand, cmp.P95Cand,
				cmp.P50Inc, cmp.P95Inc, cmp.MeanCostCand, cmp.MeanCostInc, cmp.Timeouts)
		}
	}
	if !found {
		b.WriteString("not applicable: no compare results\n\n")
	}
}

func (r *Runner) writeDownstream(b *strings.Builder, opts ReportOptions) {
	b.WriteString("## Downstream per family\n\n")
	found := false
	for _, id := range opts.DownstreamRunIDs {
		cands, err := r.db.ListCandidatesByRun(id)
		if err != nil {
			continue
		}
		for _, c := range cands {
			if c.DownstreamResultJSON == nil {
				continue
			}
			var ds DownstreamResult
			if err := json.Unmarshal([]byte(*c.DownstreamResultJSON), &ds); err != nil {
				continue
			}
			found = true
			fmt.Fprintf(b, "- %s: agreed_net=%d fresh_repeats=%d cases=%d\n",
				ds.CandidateKey, ds.AgreedNet, ds.FreshRepeats, len(ds.Cases))
			for _, cv := range ds.Cases {
				fmt.Fprintf(b, "  - %s/%s: verdict=%s agreed=%v diff=%s\n",
					cv.FamilyKey, cv.CaseKey, cv.Verdict, cv.Agreed, cv.Difference)
			}
		}
	}
	if !found {
		b.WriteString("not applicable: no downstream results\n\n")
		return
	}
	b.WriteString("\n")
}

func (r *Runner) writeCoverage(b *strings.Builder, opts ReportOptions) {
	b.WriteString("## Issue-coverage tables\n\n")
	found := false
	for _, id := range append(append([]string{}, opts.CompareRunIDs...), opts.DownstreamRunIDs...) {
		rows, err := r.db.ListCoverageByRun(id)
		if err != nil || len(rows) == 0 {
			continue
		}
		found = true
		for _, row := range rows {
			fmt.Fprintf(b, "- %s/%s: planted=%v noticed=%s unsupported=%s outcome=%s\n",
				row.CaseID, row.IssueID, row.IsPlanted,
				row.NoticedByJSON, row.UnsupportedByJSON, row.JudgeOutcome)
		}
	}
	if !found {
		b.WriteString("not applicable: no issue coverage recorded\n\n")
		return
	}
	b.WriteString("\n")
}

func (r *Runner) writeChecklist(b *strings.Builder, opts ReportOptions) {
	b.WriteString("## Promotion checklist per candidate\n\n")
	found := false
	seen := map[string]bool{}
	for _, id := range append(append([]string{}, opts.CompareRunIDs...), opts.DownstreamRunIDs...) {
		cands, err := r.db.ListCandidatesByRun(id)
		if err != nil {
			continue
		}
		for _, c := range cands {
			if c.PromotionJSON == nil || seen[c.CandidateKey] {
				continue
			}
			seen[c.CandidateKey] = true
			var cl Checklist
			if err := json.Unmarshal([]byte(*c.PromotionJSON), &cl); err != nil {
				continue
			}
			found = true
			fmt.Fprintf(b, "### %s: promote_recommended=%v forced=%v\n\n",
				cl.CandidateKey, cl.PromoteRecommended, cl.Forced)
			for _, row := range cl.Rows {
				fmt.Fprintf(b, "- %s: %s (%s)\n", row.Name, row.Status, row.Evidence)
			}
			b.WriteString("\n")
		}
	}
	if !found {
		b.WriteString("not applicable: no promotion checklists\n\n")
	}
}

func (r *Runner) writeOpenFlags(b *strings.Builder, opts ReportOptions) {
	b.WriteString("## Open flags\n\n")
	allowed := map[string]bool{}
	for _, id := range append(append([]string{opts.SentinelRunID, opts.ScreenRunID}, opts.CompareRunIDs...), opts.DownstreamRunIDs...) {
		if id != "" {
			allowed[id] = true
		}
	}
	flags, err := r.db.ListFlagsByStatus("open")
	if err != nil {
		b.WriteString("not applicable: flags unreadable\n\n")
		return
	}
	n := 0
	for _, f := range flags {
		if len(allowed) > 0 && (f.RunID == nil || !allowed[*f.RunID]) {
			continue
		}
		fmt.Fprintf(b, "- %s: type=%s passage=%s\n", f.ID, f.Type, f.Passage)
		n++
	}
	if n == 0 {
		b.WriteString("not applicable: no open flags\n\n")
		return
	}
	b.WriteString("\n")
}

func (r *Runner) writeProductionWeek(b *strings.Builder) {
	b.WriteString("## Production week\n\n")
	since := time.Now().UTC().Add(-7 * 24 * time.Hour).Format(time.RFC3339)
	first, finals, count, err := r.db.RequestTimings(since)
	if err != nil {
		b.WriteString("not applicable: production metrics unreadable\n\n")
		return
	}
	views, _ := r.db.ViewStateCounts(since)
	judges, _ := r.db.JudgeStateCounts(since)
	timeouts, _ := r.db.TimeoutCounts(since)
	feedback, _ := r.db.FeedbackTagCounts(since)
	fmt.Fprintf(b, "- requests=%d p50_first_usable=%dms p50_final=%dms\n",
		count, P50(first), P50(finals))
	fmt.Fprintf(b, "- views=%s judges=%s timeouts=%s feedback=%s\n\n",
		flatMap(views), flatMap(judges), flatMap(timeouts), flatMap(feedback))
}

func (r *Runner) writeFailures(b *strings.Builder, opts ReportOptions) {
	b.WriteString("## Failures/timeouts\n\n")
	if opts.Stats == "" {
		b.WriteString("not applicable: no stats recorded\n\n")
		return
	}
	b.WriteString("- stats: " + opts.Stats + "\n\n")
}

func flatMap(m map[string]int) string {
	if len(m) == 0 {
		return "{}"
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s=%d", k, m[k]))
	}
	return "{" + strings.Join(parts, " ") + "}"
}

func orNA(s string) string {
	if strings.TrimSpace(s) == "" {
		return "not applicable"
	}
	return s
}

func runDuration(run *store.HarnessRun) string {
	if run.FinishedAt == nil {
		return "running"
	}
	start, err1 := time.Parse(time.RFC3339, run.StartedAt)
	end, err2 := time.Parse(time.RFC3339, *run.FinishedAt)
	if err1 != nil || err2 != nil {
		return *run.FinishedAt
	}
	return end.Sub(start).String()
}

var _ = pack.AllSeats
