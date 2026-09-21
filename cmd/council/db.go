package main

import (
	"flag"
	"fmt"
	"os"

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

// openStore loads config and opens the migrated store.
func openStore() (*store.DB, *config.Config, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, nil, err
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
	fmt.Printf("db migrate: ok (%d migrations applied)\n", len(applied))
	for _, v := range applied {
		fmt.Printf("  %s\n", v)
	}
	return exitOK
}

func dbPrune(args []string) int {
	fs := flag.NewFlagSet("db prune", flag.ContinueOnError)
	olderThan := fs.Int("older-than", 0, "prune production rows older than N days")
	if err := fs.Parse(args); err != nil {
		return exitValidation
	}
	if *olderThan <= 0 {
		fmt.Fprintf(os.Stderr, "db prune: --older-than <days> must be > 0\n")
		return exitValidation
	}
	fmt.Fprintf(os.Stderr, "db prune: not implemented until Phase 6\n")
	return exitError
}
