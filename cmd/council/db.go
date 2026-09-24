package main

import (
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/qoke/toughdecisions/internal/config"
	"github.com/qoke/toughdecisions/internal/store"
)

// phaseStub returns a handler that reports the subcommand as deferred to a
// later phase and exits 1 (error), per the CLI contract.
func phaseStub(name string, phase int) handler {
	return func([]string) int {
		fmt.Fprintf(os.Stderr, "%s: not implemented until Phase %d\n", name, phase)
		return exitError
	}
}

// openStore loads config and opens the migrated store. A Load() failure is
// redacted (see redactLoadError) so a secret mistyped into a typed var
// cannot leak through any command that prints an openStore error.
func openStore() (*store.DB, *config.Config, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, nil, newLoadError(err)
	}
	db, err := store.Open(cfg.DBPath())
	if err != nil {
		return nil, nil, err
	}
	return db, cfg, nil
}

func dbMigrate([]string) int {
	db, _, err := openStore()
	if err != nil {
		fmt.Fprintf(os.Stderr, "db migrate: %v\n", err)
		return exitError
	}
	defer db.Close()
	applied, err := db.AppliedMigrations()
	if err != nil {
		fmt.Fprintf(os.Stderr, "db migrate: %v\n", err)
		return exitError
	}
	fmt.Printf("db migrate: ok (%d migrations recorded)\n", len(applied))
	for _, v := range applied {
		fmt.Printf("  %s\n", v)
	}
	return exitOK
}

func dbPrune(args []string) int {
	fs := flag.NewFlagSet("db prune", flag.ContinueOnError)
	olderThan := fs.Int("older-than", 0, "prune production rows older than N days (default: retention_production_days)")
	if err := fs.Parse(args); err != nil {
		return exitValidation
	}
	if *olderThan < 0 {
		fmt.Fprintf(os.Stderr, "db prune: --older-than <days> must be >= 0\n")
		return exitValidation
	}
	db, cfg, err := openStore()
	if err != nil {
		fmt.Fprintf(os.Stderr, "db prune: %v\n", err)
		return exitError
	}
	defer db.Close()
	days := *olderThan
	if days == 0 {
		days = cfg.RetentionProductionDays()
	}
	if days <= 0 {
		fmt.Fprintf(os.Stderr, "db prune: --older-than <days> must be > 0\n")
		return exitValidation
	}
	res, err := db.PruneProduction(time.Now().AddDate(0, 0, -days))
	if err != nil {
		fmt.Fprintf(os.Stderr, "db prune: %v\n", err)
		return exitError
	}
	fmt.Printf("db prune: ok (older-than=%d requests=%d views=%d judge=%d rewrites=%d sent=%d responses=%d)\n",
		days, res.Requests, res.RequestViews, res.RequestJudge, res.Rewrites, res.SentMessages, res.Responses)
	return exitOK
}
