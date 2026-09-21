package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/qoke/toughdecisions/internal/casepack"
)

// casesValidate loads the casepack dir and validates it. Validation
// failures exit 2; load failures exit 1.
func casesValidate(args []string) int {
	fs := flag.NewFlagSet("cases validate", flag.ContinueOnError)
	dir := fs.String("dir", "", "casepack dir (default: casepack_dir from config)")
	if err := fs.Parse(args); err != nil {
		return exitValidation
	}
	db, cfg, err := openStore()
	if err != nil {
		fmt.Fprintf(os.Stderr, "cases validate: %v\n", err)
		return exitError
	}
	defer db.Close()
	_ = db
	if *dir == "" {
		*dir = cfg.CasepackDir()
	}
	loaded, err := casepack.LoadDir(*dir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "cases validate: %v\n", err)
		return exitError
	}
	sum, err := casepack.Validate(loaded)
	if err != nil {
		fmt.Fprintf(os.Stderr, "cases validate: %v\n", err)
		return exitValidation
	}
	fmt.Printf("cases validate: ok (%d families, %d cases, %d bundles, %d selection families, %d calibration items)\n",
		sum.Families, sum.Cases, sum.Bundles, sum.SelectionFamilies, sum.CalibrationItems)
	return exitOK
}

// casesLoad validates then upserts the casepack dir into the store.
func casesLoad(args []string) int {
	fs := flag.NewFlagSet("cases load", flag.ContinueOnError)
	dir := fs.String("dir", "", "casepack dir (default: casepack_dir from config)")
	if err := fs.Parse(args); err != nil {
		return exitValidation
	}
	db, cfg, err := openStore()
	if err != nil {
		fmt.Fprintf(os.Stderr, "cases load: %v\n", err)
		return exitError
	}
	defer db.Close()
	if *dir == "" {
		*dir = cfg.CasepackDir()
	}
	loaded, err := casepack.LoadDir(*dir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "cases load: %v\n", err)
		return exitError
	}
	res, err := casepack.Upsert(db, loaded)
	if err != nil {
		fmt.Fprintf(os.Stderr, "cases load: %v\n", err)
		return exitValidation
	}
	fmt.Printf("cases load: ok (%d families, %d cases, %d bundles)\n",
		len(res.FamilyHashes), len(res.CaseHashes), len(res.BundleHashes))
	for _, w := range res.Warnings {
		fmt.Printf("  warning: %s\n", w)
	}
	return exitOK
}
