// Package harness implements the weekly evaluation harness (sentinel,
// screen, and — in later steps — compare, downstream, promotion, report).
//
// All steps share one Runner: the harness_runs row lifecycle, a worker pool
// bounded by harness_concurrency, per-call deadlines from config, and the
// Step 8 response-generation cache helper. Timeouts are stored as FAILED
// responses and counted, never retried. There are no fallbacks and no
// silent model substitution.
package harness

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/qoke/toughdecisions/internal/config"
	"github.com/qoke/toughdecisions/internal/gateway"
	"github.com/qoke/toughdecisions/internal/grading"
	"github.com/qoke/toughdecisions/internal/logx"
	"github.com/qoke/toughdecisions/internal/models"
	"github.com/qoke/toughdecisions/internal/pack"
	"github.com/qoke/toughdecisions/internal/store"
)

// GraderCallTimeout is the per-call deadline for grader calls (plan §12:
// production budgets for views/judge, 60s for graders).
const GraderCallTimeout = 60 * time.Second

// Runner owns harness steps against one DB, gateway client, and config. It
// is the shared core the later steps (compare, downstream, promotion)
// build on: construct it with NewRunner and use the run lifecycle, the
// pool, GenerateResponse, and the selection/config accessors.
type Runner struct {
	db     *store.DB
	gw     gateway.Client
	cfg    *config.Config
	log    logx.Logger
	models *models.Registry
	grade  *grading.Service
	stats  *RunStats
}

// NewRunner builds a Runner. All arguments are required.
func NewRunner(db *store.DB, gw gateway.Client, cfg *config.Config, log logx.Logger, m *models.Registry) *Runner {
	return &Runner{
		db: db, gw: gw, cfg: cfg, log: log, models: m,
		grade: grading.NewService(db, gw, cfg),
		stats: &RunStats{},
	}
}

// DB exposes the store for later steps (compare, downstream, promotion).
func (r *Runner) DB() *store.DB { return r.db }

// Gateway exposes the model client: the ONLY way to call a model.
func (r *Runner) Gateway() gateway.Client { return r.gw }

// Config exposes the loaded config.
func (r *Runner) Config() *config.Config { return r.cfg }

// Models exposes the model-capability registry.
func (r *Runner) Models() *models.Registry { return r.models }

// Grading exposes the grading service (Grade, Compare, Cover, Recheck).
func (r *Runner) Grading() *grading.Service { return r.grade }

// Concurrency is the worker-pool bound (harness_concurrency).
func (r *Runner) Concurrency() int {
	n := r.cfg.HarnessConcurrency()
	if n < 1 {
		return 1
	}
	return n
}

// ViewsDeadline is the per-call deadline for view-seat model calls.
func (r *Runner) ViewsDeadline() time.Duration { return r.cfg.ViewsDeadline() }

// JudgeDeadline is the per-call deadline for judge-seat model calls.
func (r *Runner) JudgeDeadline() time.Duration { return r.cfg.JudgeDeadline() }

// GraderDeadline is the per-call deadline for grader calls.
func (r *Runner) GraderDeadline() time.Duration { return GraderCallTimeout }

// DeadlineFor returns the model-call deadline for a seat: the judge
// deadline for the judge seat, the views deadline otherwise.
func (r *Runner) DeadlineFor(seat pack.Seat) time.Duration {
	if seat == pack.SeatJudge {
		return r.JudgeDeadline()
	}
	return r.ViewsDeadline()
}

// SentinelCount is the number of sentinel cases per run.
func (r *Runner) SentinelCount() int { return r.cfg.HarnessSentinelCount() }

// ScreenCases is the number of screening cases per candidate.
func (r *Runner) ScreenCases() int { return r.cfg.HarnessScreenCases() }

// CalibrationRecheck is the number of rotating calibration items per grader.
func (r *Runner) CalibrationRecheck() int { return r.cfg.HarnessCalibrationRecheck() }

// BlockOnDrift reports whether grader drift blocks later steps.
func (r *Runner) BlockOnDrift() bool { return r.cfg.HarnessBlockOnGraderDrift() }

// RunStats counts model calls across a Runner's lifetime. Timeouts and
// failures are stored FAILED responses, counted here and surfaced on the
// run row summary — never retried.
type RunStats struct {
	Calls     atomic.Int64
	CacheHits atomic.Int64
	Timeouts  atomic.Int64
	Failures  atomic.Int64
}

// Snapshot returns a copy of the current counters.
func (s *RunStats) Snapshot() (calls, hits, timeouts, failures int64) {
	return s.Calls.Load(), s.CacheHits.Load(), s.Timeouts.Load(), s.Failures.Load()
}

// StatsJSON marshals the counters for the run summary.
func (r *Runner) StatsJSON() string {
	c, h, t, f := r.stats.Snapshot()
	raw, _ := json.Marshal(map[string]int64{
		"calls": c, "cache_hits": h, "timeouts": t, "failures": f,
	})
	return string(raw)
}

// StartRun creates a harness_runs row and marks it running.
func (r *Runner) StartRun(kind, packID, paramsJSON string) (*store.HarnessRun, error) {
	run, err := r.db.CreateHarnessRun(kind, packID, paramsJSON)
	if err != nil {
		return nil, err
	}
	if err := r.db.UpdateHarnessRunStatus(run.ID, "running"); err != nil {
		return nil, err
	}
	run.Status = "running"
	return run, nil
}

// FinishRun marks a run finished with status and a summary document.
func (r *Runner) FinishRun(id, status, summary string) error {
	return r.db.FinishHarnessRun(id, status, nil, &summary, nil)
}

// FailRun marks a run failed, recording the reason in the summary.
func (r *Runner) FailRun(id string, reason string) error {
	raw, _ := json.Marshal(map[string]string{"error": reason})
	summary := string(raw)
	return r.db.FinishHarnessRun(id, "failed", nil, &summary, nil)
}

// GetRun selects a harness run by id.
func (r *Runner) GetRun(id string) (*store.HarnessRun, error) {
	return r.db.GetHarnessRun(id)
}

// Pool runs jobs with at most Concurrency() in flight, waits for all of
// them, and returns their errors in job order. A nil entry means success.
func (r *Runner) Pool(ctx context.Context, jobs []func(context.Context) error) []error {
	sem := make(chan struct{}, r.Concurrency())
	errs := make([]error, len(jobs))
	var wg sync.WaitGroup
	for i, job := range jobs {
		wg.Add(1)
		go func(i int, job func(context.Context) error) {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-ctx.Done():
				errs[i] = ctx.Err()
				return
			}
			errs[i] = job(ctx)
		}(i, job)
	}
	wg.Wait()
	return errs
}

// ShouldInvestigate evaluates the plan §12.1 investigate conditions
// (report only — never auto-rollback): a repeat regression on the same
// seat x case, any confirmed flag, or seat p50 latency over budget. It
// returns whether to investigate and the reason.
func ShouldInvestigate(prevRegression bool, confirmedFlags int, p50Ms, budgetMs int64) (bool, string) {
	switch {
	case prevRegression:
		return true, "repeat regression on the same seat and case"
	case confirmedFlags > 0:
		return true, fmt.Sprintf("%d confirmed flag(s)", confirmedFlags)
	case p50Ms > budgetMs:
		return true, fmt.Sprintf("p50 latency %dms over budget %dms", p50Ms, budgetMs)
	default:
		return false, ""
	}
}

// ScreenPassed applies the plan §12.2 pass rule: no open flags, mean
// within ScreenMeanTolerance of the incumbent mean, and per-case totals at
// or above the incumbent on at least 4/6 (proportional) of cases.
func ScreenPassed(openFlags int, mean, incumbentMean float64, wins, n int) bool {
	if openFlags > 0 {
		return false
	}
	if mean < incumbentMean-ScreenMeanTolerance {
		return false
	}
	need := (4*n + 5) / 6
	if need < 1 {
		need = 1
	}
	return wins >= need
}

// FinalistForMode maps the candidate finalist mode to the outcome:
// auto follows passed, force is always a finalist, never is not.
func FinalistForMode(mode string, passed bool) bool {
	switch mode {
	case "force":
		return true
	case "never":
		return false
	default:
		return passed
	}
}

// P50 returns the p50 of sorted millisecond samples, or 0 when empty.
func P50(sorted []int64) int64 {
	if len(sorted) == 0 {
		return 0
	}
	cp := append([]int64(nil), sorted...)
	sort.Slice(cp, func(i, j int) bool { return cp[i] < cp[j] })
	return cp[len(cp)/2]
}

// P95 returns the p95 of millisecond samples (nearest-rank), or 0 when
// empty. Compare and promotion read per-run p95 against the seat budget;
// it never consults cross-run history.
func P95(samples []int64) int64 {
	if len(samples) == 0 {
		return 0
	}
	cp := append([]int64(nil), samples...)
	sort.Slice(cp, func(i, j int) bool { return cp[i] < cp[j] })
	idx := (95*len(cp) + 99) / 100
	if idx < 1 {
		idx = 1
	}
	if idx > len(cp) {
		idx = len(cp)
	}
	return cp[idx-1]
}
