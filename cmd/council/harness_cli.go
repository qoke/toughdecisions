package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"

	"github.com/qoke/toughdecisions/internal/config"
	"github.com/qoke/toughdecisions/internal/harness"
	"github.com/qoke/toughdecisions/internal/logx"
	"github.com/qoke/toughdecisions/internal/models"
)

func newHTTPClient() *http.Client {
	return &http.Client{CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
		return http.ErrUseLastResponse
	}}
}

// harnessWeekly runs plan 12.9 weekly orchestration.
func harnessWeekly(args []string) int {
	fs := flag.NewFlagSet("harness weekly", flag.ContinueOnError)
	steps := fs.String("steps", "", "comma list: sentinel,screen,compare,downstream,report (default: all)")
	fresh := fs.Bool("fresh", false, "regenerate responses with repetition+1")
	notify := fs.Bool("notify", false, "POST the report to notify_webhook_url")
	if err := fs.Parse(args); err != nil {
		return exitValidation
	}
	parsed, err := harness.ParseWeeklySteps(*steps)
	if err != nil {
		fmt.Fprintf(os.Stderr, "harness weekly: %v\n", err)
		return exitValidation
	}
	if parsed.Sentinel || parsed.Screen || parsed.Compare || parsed.Downstream {
		cfg, err := config.Load()
		if err != nil {
			fmt.Fprintf(os.Stderr, "harness weekly: %v\n", err)
			return exitError
		}
		if err := requireGatewayKey(cfg); err != nil {
			fmt.Fprintf(os.Stderr, "harness weekly: %v\n", err)
			return exitError
		}
	}
	r, _, closeDB, err := openHarnessRunner()
	if err != nil {
		fmt.Fprintf(os.Stderr, "harness weekly: %v\n", err)
		return exitError
	}
	defer closeDB()
	res, err := r.Weekly(context.Background(), harness.WeeklyOptions{
		Steps: parsed, Fresh: *fresh, Notify: *notify,
	})
	if err != nil {
		var drift *harness.SentinelDriftError
		if errors.As(err, &drift) {
			fmt.Fprintf(os.Stderr, "harness weekly: %v\n", err)
			return exitBlocked
		}
		fmt.Fprintf(os.Stderr, "harness weekly: %v\n", err)
		return exitError
	}
	fmt.Printf("harness weekly: ok (run=%s status=%s report=%s)\n", res.RunID, res.Status, res.ReportPath)
	return exitOK
}

// harnessReport writes the markdown report for one run.
func harnessReport(args []string) int {
	fs := flag.NewFlagSet("harness report", flag.ContinueOnError)
	runID := fs.String("run", "", "harness run id")
	notify := fs.Bool("notify", false, "POST the report to notify_webhook_url")
	if err := fs.Parse(args); err != nil {
		return exitValidation
	}
	if *runID == "" {
		fmt.Fprintf(os.Stderr, "harness report: --run <id> is required\n")
		return exitValidation
	}
	r, _, closeDB, err := openHarnessRunner()
	if err != nil {
		fmt.Fprintf(os.Stderr, "harness report: %v\n", err)
		return exitError
	}
	defer closeDB()
	rep, err := r.Report(context.Background(), harness.ReportOptions{
		RunID: *runID, Summary: "harness report " + *runID, Notify: *notify,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "harness report: %v\n", err)
		return exitError
	}
	fmt.Printf("harness report: ok (path=%s)\n", rep.Path)
	return exitOK
}

// openHarnessRunner opens the store, loads the models registry, and builds
// the harness runner sharing one DB handle. Close must be called.
func openHarnessRunner() (*harness.Runner, *config.Config, func(), error) {
	db, cfg, err := openStore()
	if err != nil {
		return nil, nil, nil, err
	}
	mreg, err := models.LoadRegistry(cfg.ModelsFile())
	if err != nil {
		_ = db.Close()
		return nil, nil, nil, err
	}
	log := logx.New(cfg)
	gw := gatewayFactory(cfg, log)
	return harness.NewRunner(db, gw, cfg, log, mreg), cfg, func() { _ = db.Close() }, nil
}

// harnessSentinel runs plan 12.1: fresh responses vs baselines with both
// selection graders. Grader drift exits 3 (blocked); missing baselines
// exits 1 naming `pack publish --baselines-only`.
func harnessSentinel(args []string) int {
	fs := flag.NewFlagSet("harness sentinel", flag.ContinueOnError)
	if err := fs.Parse(args); err != nil {
		return exitValidation
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(os.Stderr, "harness sentinel: unexpected args; usage: harness sentinel\n")
		return exitValidation
	}
	if cfg, err := config.Load(); err != nil {
		fmt.Fprintf(os.Stderr, "harness sentinel: %v\n", err)
		return exitError
	} else if err := requireGatewayKey(cfg); err != nil {
		fmt.Fprintf(os.Stderr, "harness sentinel: %v\n", err)
		return exitError
	}
	r, _, closeDB, err := openHarnessRunner()
	if err != nil {
		fmt.Fprintf(os.Stderr, "harness sentinel: %v\n", err)
		return exitError
	}
	defer closeDB()
	sum, err := r.Sentinel(context.Background(), harness.SentinelOptions{})
	if err != nil {
		var drift *harness.SentinelDriftError
		if errors.As(err, &drift) {
			fmt.Fprintf(os.Stderr, "harness sentinel: %v\n", err)
			return exitBlocked
		}
		fmt.Fprintf(os.Stderr, "harness sentinel: %v\n", err)
		return exitError
	}
	fmt.Printf("harness sentinel: ok (run=%s regressions=%d investigate=%d)\n",
		sum.RunID, len(sum.Regressions), len(sum.Investigate))
	return exitOK
}

// harnessScreen runs plan 12.2 for every candidate in the candidates file.
func harnessScreen(args []string) int {
	fs := flag.NewFlagSet("harness screen", flag.ContinueOnError)
	fresh := fs.Bool("fresh", false, "regenerate responses with repetition+1")
	if err := fs.Parse(args); err != nil {
		return exitValidation
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(os.Stderr, "harness screen: unexpected args; usage: harness screen [--fresh]\n")
		return exitValidation
	}
	if keyCfg, err := config.Load(); err != nil {
		fmt.Fprintf(os.Stderr, "harness screen: %v\n", err)
		return exitError
	} else if err := requireGatewayKey(keyCfg); err != nil {
		fmt.Fprintf(os.Stderr, "harness screen: %v\n", err)
		return exitError
	}
	r, cfg, closeDB, err := openHarnessRunner()
	if err != nil {
		fmt.Fprintf(os.Stderr, "harness screen: %v\n", err)
		return exitError
	}
	defer closeDB()
	results, err := r.Screen(context.Background(), cfg.CandidatesFile(), *fresh)
	if err != nil {
		var drift *harness.SentinelDriftError
		if errors.As(err, &drift) {
			fmt.Fprintf(os.Stderr, "harness screen: %v\n", err)
			return exitBlocked
		}
		fmt.Fprintf(os.Stderr, "harness screen: %v\n", err)
		return exitError
	}
	fmt.Printf("harness screen: ok (%d candidates)\n", len(results))
	for _, res := range results {
		fmt.Printf("  %s: passed=%t finalist=%t mean=%.2f incumbent=%.2f wins=%d/%d flags=%d\n",
			res.CandidateKey, res.Passed, res.Finalist, res.MeanTotal, res.IncMeanTotal,
			res.Wins, res.Cases, res.OpenFlags)
	}
	return exitOK
}

// harnessCompare runs plan 12.4 for one candidate: absolute grades by both
// selection graders, pairwise vs incumbent, fragility, latency.
func harnessCompare(args []string) int {
	fs := flag.NewFlagSet("harness compare", flag.ContinueOnError)
	candidate := fs.String("candidate", "", "candidate key (required)")
	fresh := fs.Bool("fresh", false, "regenerate responses with repetition+1")
	if err := fs.Parse(args); err != nil {
		return exitValidation
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(os.Stderr, "harness compare: unexpected args; usage: harness compare --candidate <key> [--fresh]\n")
		return exitValidation
	}
	if *candidate == "" {
		fmt.Fprintf(os.Stderr, "harness compare: --candidate <key> is required\n")
		return exitValidation
	}
	if keyCfg, err := config.Load(); err != nil {
		fmt.Fprintf(os.Stderr, "harness compare: %v\n", err)
		return exitError
	} else if err := requireGatewayKey(keyCfg); err != nil {
		fmt.Fprintf(os.Stderr, "harness compare: %v\n", err)
		return exitError
	}
	r, cfg, closeDB, err := openHarnessRunner()
	if err != nil {
		fmt.Fprintf(os.Stderr, "harness compare: %v\n", err)
		return exitError
	}
	defer closeDB()
	specs, err := r.LoadCandidates(cfg.CandidatesFile())
	if err != nil {
		fmt.Fprintf(os.Stderr, "harness compare: %v\n", err)
		return exitError
	}
	var spec *harness.CandidateSpec
	for i := range specs {
		if specs[i].Key == *candidate {
			spec = &specs[i]
		}
	}
	if spec == nil {
		fmt.Fprintf(os.Stderr, "harness compare: unknown candidate %q\n", *candidate)
		return exitValidation
	}
	res, err := r.Compare(context.Background(), harness.CompareOptions{Spec: *spec, Fresh: *fresh})
	if err != nil {
		var drift *harness.SentinelDriftError
		if errors.As(err, &drift) {
			fmt.Fprintf(os.Stderr, "harness compare: %v\n", err)
			return exitBlocked
		}
		fmt.Fprintf(os.Stderr, "harness compare: %v\n", err)
		return exitError
	}
	fmt.Printf("harness compare: ok (run=%s candidate=%s cases=%d hard=%dW/%dL/%dO)\n",
		res.RunID, res.CandidateKey, len(res.Cases), res.HardWins, res.HardLosses, res.HardOther)
	return exitOK
}

// harnessDownstream runs plan 12.5 for one candidate on the compare run,
// then evaluates the promotion checklist.
func harnessDownstream(args []string) int {
	fs := flag.NewFlagSet("harness downstream", flag.ContinueOnError)
	candidate := fs.String("candidate", "", "candidate key (required)")
	runID := fs.String("run", "", "compare run id (required)")
	fresh := fs.Bool("fresh", false, "regenerate responses with repetition+1")
	if err := fs.Parse(args); err != nil {
		return exitValidation
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(os.Stderr, "harness downstream: unexpected args; usage: harness downstream --candidate <key> --run <id> [--fresh]\n")
		return exitValidation
	}
	if *candidate == "" || *runID == "" {
		fmt.Fprintf(os.Stderr, "harness downstream: --candidate <key> and --run <id> are required\n")
		return exitValidation
	}
	if keyCfg, err := config.Load(); err != nil {
		fmt.Fprintf(os.Stderr, "harness downstream: %v\n", err)
		return exitError
	} else if err := requireGatewayKey(keyCfg); err != nil {
		fmt.Fprintf(os.Stderr, "harness downstream: %v\n", err)
		return exitError
	}
	r, cfg, closeDB, err := openHarnessRunner()
	if err != nil {
		fmt.Fprintf(os.Stderr, "harness downstream: %v\n", err)
		return exitError
	}
	defer closeDB()
	specs, err := r.LoadCandidates(cfg.CandidatesFile())
	if err != nil {
		fmt.Fprintf(os.Stderr, "harness downstream: %v\n", err)
		return exitError
	}
	var spec *harness.CandidateSpec
	for i := range specs {
		if specs[i].Key == *candidate {
			spec = &specs[i]
		}
	}
	if spec == nil {
		fmt.Fprintf(os.Stderr, "harness downstream: unknown candidate %q\n", *candidate)
		return exitValidation
	}
	res, err := r.Downstream(context.Background(), harness.DownstreamOptions{
		Spec: *spec, RunID: *runID, Fresh: *fresh,
	})
	if err != nil {
		var drift *harness.SentinelDriftError
		if errors.As(err, &drift) {
			fmt.Fprintf(os.Stderr, "harness downstream: %v\n", err)
			return exitBlocked
		}
		fmt.Fprintf(os.Stderr, "harness downstream: %v\n", err)
		return exitError
	}
	cl, err := r.Promotion(res.RunID, spec.Key)
	if err != nil {
		fmt.Fprintf(os.Stderr, "harness downstream: %v\n", err)
		return exitError
	}
	fmt.Printf("harness downstream: ok (run=%s candidate=%s net=%d promote=%t)\n",
		res.RunID, res.CandidateKey, res.AgreedNet, cl.PromoteRecommended)
	return exitOK
}
