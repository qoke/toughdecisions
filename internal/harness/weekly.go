package harness

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/qoke/toughdecisions/internal/casepack"
)

// WeeklySteps selections mirror `council harness weekly --steps`. An empty
// Steps slice runs every step.
type WeeklySteps struct {
	Sentinel   bool
	Screen     bool
	Compare    bool
	Downstream bool
	Report     bool
}

// DefaultWeeklySteps runs every weekly step.
func DefaultWeeklySteps() WeeklySteps {
	return WeeklySteps{Sentinel: true, Screen: true, Compare: true, Downstream: true, Report: true}
}

// ParseWeeklySteps parses the --steps flag: a comma list drawn from
// sentinel,screen,compare,downstream,report. Empty input means all steps.
func ParseWeeklySteps(raw string) (WeeklySteps, error) {
	if strings.TrimSpace(raw) == "" {
		return DefaultWeeklySteps(), nil
	}
	var out WeeklySteps
	for _, part := range strings.Split(raw, ",") {
		switch strings.TrimSpace(part) {
		case "sentinel":
			out.Sentinel = true
		case "screen":
			out.Screen = true
		case "compare":
			out.Compare = true
		case "downstream":
			out.Downstream = true
		case "report":
			out.Report = true
		case "":
		default:
			return WeeklySteps{}, fmt.Errorf("harness: unknown weekly step %q (want sentinel,screen,compare,downstream,report)", strings.TrimSpace(part))
		}
	}
	return out, nil
}

// WeeklyOptions configures one `harness weekly` run. Compare, Cover, and
// LoadPack are injected hooks so tests stay offline: nil uses the production
// defaults (grading service graders, coverage recorder, casepack dir
// validation). Sentinel always uses Compare.
type WeeklyOptions struct {
	Steps    WeeklySteps
	Now      time.Time
	Fresh    bool
	Notify   bool
	Compare  CompareFunc
	Cover    CoverFunc
	LoadPack func(ctx context.Context) error
}

// WeeklyResult is the finished weekly orchestration outcome. RunID is the
// weekly parent run; the step fields carry the per-step sub-run ids the
// report renders. Skipped names zero-candidate skips; Blocked carries the
// drift message; Status is complete, blocked, or failed.
type WeeklyResult struct {
	RunID            string
	Status           string
	SentinelRunID    string
	ScreenRunID      string
	CompareRunIDs    []string
	DownstreamRunIDs []string
	Skipped          []string
	Blocked          string
	ReportPath       string
}

// Weekly runs plan §12.9: validate the case pack first (a validation error
// aborts with no run row), then sentinel (drift blocks before compare), then
// screen → compare → downstream → promotion, and always the report — on
// success, on a blocked run, and on a failed run. Zero candidates
// (missing/empty candidates.yaml) skips screen, compare, and downstream and
// records the skip for the report. Notify forwards to the report webhook.
func (r *Runner) Weekly(ctx context.Context, opts WeeklyOptions) (*WeeklyResult, error) {
	check := opts.LoadPack
	if check == nil {
		check = r.loadCasepack
	}
	if err := check(ctx); err != nil {
		return nil, fmt.Errorf("harness: load case pack: %w", err)
	}

	pk, err := packActive(r)
	if err != nil {
		return nil, err
	}
	params, _ := json.Marshal(map[string]any{"fresh": opts.Fresh, "notify": opts.Notify})
	week, err := r.StartRun("weekly", pk.ID, string(params))
	if err != nil {
		return nil, err
	}
	res := &WeeklyResult{RunID: week.ID}

	// writeReport finishes the weekly parent row and always writes the
	// markdown report — on success, blocked, and failed runs alike.
	writeReport := func(status string) error {
		summary, _ := json.Marshal(map[string]any{
			"status": res.Status, "blocked": res.Blocked, "skipped": res.Skipped,
		})
		s := string(summary)
		_ = r.db.FinishHarnessRun(res.RunID, status, nil, &s, nil)
		if !opts.Steps.Report {
			return nil
		}
		if res.Status == "" {
			res.Status = status
		}
		rep, rerr := r.Report(ctx, ReportOptions{
			RunID: res.RunID, SentinelRunID: res.SentinelRunID,
			ScreenRunID: res.ScreenRunID, CompareRunIDs: res.CompareRunIDs,
			DownstreamRunIDs: res.DownstreamRunIDs, Stats: r.StatsJSON(),
			Summary: "weekly " + res.RunID + " " + res.Status,
			Notify:  opts.Notify, Skipped: res.Skipped, Blocked: res.Blocked,
		})
		if rerr != nil {
			return rerr
		}
		res.ReportPath = rep.Path
		return nil
	}

	if opts.Steps.Sentinel {
		sum, serr := r.Sentinel(ctx, SentinelOptions{Now: opts.Now, Compare: opts.Compare})
		if serr != nil {
			var drift *SentinelDriftError
			if errors.As(serr, &drift) {
				// Drift blocks the run before compare: stop here but
				// still write the report.
				res.Status = "blocked"
				res.Blocked = drift.Error()
				if rerr := writeReport("blocked"); rerr != nil {
					return res, rerr
				}
				return res, drift
			}
			res.Status = "failed"
			_ = writeReport("failed")
			return res, serr
		}
		res.SentinelRunID = sum.RunID
	}

	specs, loadErr := r.LoadCandidates(r.cfg.CandidatesFile())
	switch {
	case loadErr != nil && !os.IsNotExist(loadErr):
		// An unreadable or invalid candidates file is a failed run, but
		// the report still goes out.
		res.Status = "failed"
		_ = writeReport("failed")
		return res, fmt.Errorf("harness: load candidates: %w", loadErr)
	case loadErr != nil || len(specs) == 0:
		res.Skipped = []string{"screen", "compare", "downstream"}
	default:
		if opts.Steps.Screen {
			var screenRes []ScreenResult
			screenRes, err = r.screenForWeekly(ctx, res, specs, opts.Fresh)
			if err != nil {
				res.Status = "failed"
				_ = writeReport("failed")
				return res, err
			}
			res.ScreenRunID = screenRes[0].RunID
		}
		if opts.Steps.Compare || opts.Steps.Downstream {
			for _, spec := range specs {
				cmpRun, dsRun, cerr := r.compareAndDownstream(ctx, res, spec, opts)
				if cerr != nil {
					res.Status = "failed"
					_ = writeReport("failed")
					return res, cerr
				}
				res.CompareRunIDs = append(res.CompareRunIDs, cmpRun...)
				res.DownstreamRunIDs = append(res.DownstreamRunIDs, dsRun...)
			}
		}
	}

	res.Status = "complete"
	if err := writeReport("complete"); err != nil {
		return res, err
	}
	return res, nil
}

// screenForWeekly screens every candidate on one shared weekly screen run so
// the report renders a single screening table.
func (r *Runner) screenForWeekly(ctx context.Context, res *WeeklyResult, specs []CandidateSpec, fresh bool) ([]ScreenResult, error) {
	pk, err := packActive(r)
	if err != nil {
		return nil, err
	}
	screener, err := r.ScreeningGrader()
	if err != nil {
		return nil, err
	}
	params, _ := json.Marshal(map[string]any{"fresh": fresh})
	run, err := r.StartRun("screen", pk.ID, string(params))
	if err != nil {
		return nil, err
	}
	fail := func(reason error) ([]ScreenResult, error) {
		_ = r.FailRun(run.ID, reason.Error())
		return nil, reason
	}
	var out []ScreenResult
	for _, spec := range specs {
		one, err := r.screenOne(ctx, run.ID, pk, screener, spec, fresh)
		if err != nil {
			return fail(err)
		}
		out = append(out, *one)
	}
	raw, _ := json.Marshal(out)
	if err := r.FinishRun(run.ID, "complete", string(raw)); err != nil {
		return nil, err
	}
	return out, nil
}

// compareAndDownstream runs compare, then downstream and promotion on the
// compare run for one candidate. A drift error propagates so Weekly marks
// the run blocked before any later compare.
func (r *Runner) compareAndDownstream(ctx context.Context, res *WeeklyResult, spec CandidateSpec, opts WeeklyOptions) (cmpRuns, dsRuns []string, err error) {
	runID := ""
	if opts.Steps.Compare {
		cmp, cerr := r.Compare(ctx, CompareOptions{
			Spec: spec, Fresh: opts.Fresh, Compare: opts.Compare,
		})
		if cerr != nil {
			return nil, nil, cerr
		}
		runID = cmp.RunID
		cmpRuns = append(cmpRuns, cmp.RunID)
	} else if opts.Steps.Downstream {
		pk, perr := packActive(r)
		if perr != nil {
			return nil, nil, perr
		}
		params, _ := json.Marshal(map[string]any{"fresh": opts.Fresh})
		run, rerr := r.StartRun("compare", pk.ID, string(params))
		if rerr != nil {
			return nil, nil, rerr
		}
		runID = run.ID
		cmpRuns = append(cmpRuns, run.ID)
	}
	if opts.Steps.Downstream {
		if _, derr := r.Downstream(ctx, DownstreamOptions{
			Spec: spec, RunID: runID, Fresh: opts.Fresh,
			Compare: opts.Compare, Cover: opts.Cover,
		}); derr != nil {
			return cmpRuns, nil, derr
		}
		dsRuns = append(dsRuns, runID)
		if _, perr := r.Promotion(runID, spec.Key); perr != nil {
			return cmpRuns, dsRuns, perr
		}
	}
	return cmpRuns, dsRuns, nil
}

// loadCasepack validates the configured casepack dir. A validation error
// aborts the weekly run with no run row left behind.
func (r *Runner) loadCasepack(_ context.Context) error {
	loaded, err := casepack.LoadDir(r.cfg.CasepackDir())
	if err != nil {
		return err
	}
	_, err = casepack.Validate(loaded)
	return err
}
