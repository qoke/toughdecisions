package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"sort"

	"github.com/qoke/toughdecisions/internal/gateway"
	"github.com/qoke/toughdecisions/internal/harness"
	"github.com/qoke/toughdecisions/internal/logx"
	"github.com/qoke/toughdecisions/internal/models"
	"github.com/qoke/toughdecisions/internal/pack"
)

// packInit loads a seats YAML file into pack v1 (active), validating every
// seat's settings against the models registry. Validation failures exit 2.
func packInit(args []string) int {
	fs := flag.NewFlagSet("pack init", flag.ContinueOnError)
	file := fs.String("file", "", "seats YAML file (default: pack_file from config)")
	if err := fs.Parse(args); err != nil {
		return exitValidation
	}
	db, cfg, err := openStore()
	if err != nil {
		fmt.Fprintf(os.Stderr, "pack init: %v\n", err)
		return exitError
	}
	defer db.Close()
	if *file == "" {
		*file = cfg.PackFile()
	}
	mreg, err := models.LoadRegistry(cfg.ModelsFile())
	if err != nil {
		fmt.Fprintf(os.Stderr, "pack init: %v\n", err)
		return exitError
	}
	p, err := pack.InitValidated(db, *file, func(sc pack.SeatConfig) error {
		if err := mreg.ValidateSettings(sc); err != nil {
			return err
		}
		if _, _, _, jsonSchema, _ := mreg.Supports(sc.Model); !jsonSchema {
			return fmt.Errorf("models: model %q does not support strict structured outputs: %w", sc.Model, gateway.ErrUnsupportedSetting)
		}
		return nil
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "pack init: %v\n", err)
		return exitValidation
	}
	fmt.Printf("pack init: ok (id=%s)\n", p.ID)
	return exitOK
}

func packShow(args []string) int {
	fs := flag.NewFlagSet("pack show", flag.ContinueOnError)
	id := fs.String("id", "", "pack id (default: active)")
	if err := fs.Parse(args); err != nil {
		return exitValidation
	}
	db, _, err := openStore()
	if err != nil {
		fmt.Fprintf(os.Stderr, "pack show: %v\n", err)
		return exitError
	}
	defer db.Close()
	p, err := pack.Show(db, *id)
	if err != nil {
		fmt.Fprintf(os.Stderr, "pack show: %v\n", err)
		return exitError
	}
	fmt.Printf("pack %s (%s) prompt_pack_hash=%s\n", p.ID, p.Status, p.PromptPackHash)
	seats := make([]pack.Seat, 0, len(p.Seats))
	for s := range p.Seats {
		seats = append(seats, s)
	}
	sort.Slice(seats, func(i, j int) bool { return seats[i] < seats[j] })
	for _, s := range seats {
		sc := p.Seats[s]
		fmt.Printf("  %s: model=%s family=%s max_output_tokens=%d\n",
			s, sc.Model, sc.Family, sc.MaxOutputTokens)
	}
	return exitOK
}

// packPublish publishes a candidate pack after its promotion checklist
// passes. A failing checklist refuses with exit 3 unless --force records
// the override. --baselines-only fills baselines for the active pack.
func packPublish(args []string) int {
	fs := flag.NewFlagSet("pack publish", flag.ContinueOnError)
	runID := fs.String("run", "", "harness run id")
	candidate := fs.String("candidate", "", "candidate key")
	force := fs.Bool("force", false, "publish despite a failing checklist (recorded)")
	baselinesOnly := fs.Bool("baselines-only", false, "fill baselines for the active pack without publishing")
	if err := fs.Parse(args); err != nil {
		return exitValidation
	}
	db, cfg, err := openStore()
	if err != nil {
		fmt.Fprintf(os.Stderr, "pack publish: %v\n", err)
		return exitError
	}
	defer db.Close()
	mreg, err := models.LoadRegistry(cfg.ModelsFile())
	if err != nil {
		fmt.Fprintf(os.Stderr, "pack publish: %v\n", err)
		return exitError
	}
	log := logx.New(cfg)
	gw := gatewayFactory(cfg, log)
	r := harness.NewRunner(db, gw, cfg, log, mreg)
	res, err := r.Publish(context.Background(), harness.PublishOptions{
		RunID: *runID, CandidateKey: *candidate,
		Force: *force, BaselinesOnly: *baselinesOnly,
	})
	if err != nil {
		var blocked *harness.ChecklistBlockedError
		if errors.As(err, &blocked) {
			fmt.Fprintf(os.Stderr, "pack publish: %v\n", err)
			return exitBlocked
		}
		fmt.Fprintf(os.Stderr, "pack publish: %v\n", err)
		return exitError
	}
	if *baselinesOnly {
		fmt.Printf("pack publish: ok (baselines-only pack=%s baselines=%d bundles=%d)\n",
			res.Pack.ID, res.Baselines, res.Bundles)
		return exitOK
	}
	msg := "pack publish: ok"
	if res.Forced {
		msg += " (forced)"
	}
	fmt.Printf("%s (pack=%s baselines=%d bundles=%d)\n", msg, res.Pack.ID, res.Baselines, res.Bundles)
	return exitOK
}

func packRollback([]string) int {
	db, _, err := openStore()
	if err != nil {
		fmt.Fprintf(os.Stderr, "pack rollback: %v\n", err)
		return exitError
	}
	defer db.Close()
	p, err := pack.Rollback(db)
	if err != nil {
		fmt.Fprintf(os.Stderr, "pack rollback: %v\n", err)
		return exitError
	}
	fmt.Printf("pack rollback: ok (active=%s)\n", p.ID)
	return exitOK
}
