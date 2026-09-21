package main

import (
	"flag"
	"fmt"
	"os"
	"sort"

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
	p, err := pack.Init(db, *file)
	if err != nil {
		fmt.Fprintf(os.Stderr, "pack init: %v\n", err)
		return exitValidation
	}
	for _, seat := range pack.AllSeats {
		if err := mreg.ValidateSettings(p.Seats[seat]); err != nil {
			fmt.Fprintf(os.Stderr, "pack init: seat %q: %v\n", seat, err)
			return exitValidation
		}
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

// packPublish is blocked until Phase 5: publishing requires the harness
// promotion checklist. Exit 3 (blocked) per the CLI contract.
func packPublish([]string) int {
	fmt.Fprintf(os.Stderr, "pack publish: blocked: requires harness run (Phase 5)\n")
	return exitBlocked
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
