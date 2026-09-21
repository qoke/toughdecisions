package main

import (
	"flag"
	"fmt"
	"os"
	"sort"
)

// flagsList prints flags by status (default open).
func flagsList(args []string) int {
	fs := flag.NewFlagSet("flags list", flag.ContinueOnError)
	status := fs.String("status", "open", "flag status: open|confirmed|dismissed")
	runID := fs.String("run", "", "filter by run id (optional)")
	if err := fs.Parse(args); err != nil {
		return exitValidation
	}
	switch *status {
	case "open", "confirmed", "dismissed":
	default:
		fmt.Fprintf(os.Stderr, "flags list: invalid --status %q (want open|confirmed|dismissed)\n", *status)
		return exitValidation
	}
	db, _, err := openStore()
	if err != nil {
		fmt.Fprintf(os.Stderr, "flags list: %v\n", err)
		return exitError
	}
	defer db.Close()
	rows, err := db.ListFlagsByStatus(*status)
	if err != nil {
		fmt.Fprintf(os.Stderr, "flags list: %v\n", err)
		return exitError
	}
	if *runID != "" {
		kept := rows[:0]
		for _, f := range rows {
			if f.RunID != nil && *f.RunID == *runID {
				kept = append(kept, f)
			}
		}
		rows = kept
	}
	ids := make([]string, 0, len(rows))
	byID := map[string]flagRow{}
	for _, f := range rows {
		ids = append(ids, f.ID)
		byID[f.ID] = flagRow{id: f.ID, typ: f.Type, passage: f.Passage, violated: f.Violated, status: f.Status}
	}
	sort.Strings(ids)
	fmt.Printf("flags list: ok (%d %s)\n", len(ids), *status)
	for _, id := range ids {
		r := byID[id]
		fmt.Printf("  %s: type=%s violated=%s passage=%.80s\n", r.id, r.typ, r.violated, r.passage)
	}
	return exitOK
}

type flagRow struct {
	id       string
	typ      string
	passage  string
	violated string
	status   string
}

// flagsConfirm marks a flag confirmed with a note.
func flagsConfirm(args []string) int {
	return resolveFlag("confirm", args)
}

// flagsDismiss marks a flag dismissed with a note.
func flagsDismiss(args []string) int {
	return resolveFlag("dismiss", args)
}

func resolveFlag(action string, args []string) int {
	// Manual parse: stdlib flag stops at the first positional arg, but the
	// CLI contract is `flags <action> <id> --note "..."`.
	var note string
	var rest []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--note" && i+1 < len(args):
			i++
			note = args[i]
		case len(a) > len("--note=") && a[:len("--note=")] == "--note=":
			note = a[len("--note="):]
		case a == "--note":
			fmt.Fprintf(os.Stderr, "flags %s: --note needs a value\n", action)
			return exitValidation
		case len(a) > 2 && a[:2] == "--":
			fmt.Fprintf(os.Stderr, "flags %s: unknown flag %q\n", action, a)
			return exitValidation
		default:
			rest = append(rest, a)
		}
	}
	if len(rest) != 1 || rest[0] == "" {
		fmt.Fprintf(os.Stderr, "flags %s: usage: flags %s <id> --note \"...\"\n", action, action)
		return exitValidation
	}
	if note == "" {
		fmt.Fprintf(os.Stderr, "flags %s: --note is required\n", action)
		return exitValidation
	}
	db, _, err := openStore()
	if err != nil {
		fmt.Fprintf(os.Stderr, "flags %s: %v\n", action, err)
		return exitError
	}
	defer db.Close()
	next := "confirmed"
	if action == "dismiss" {
		next = "dismissed"
	}
	if err := db.UpdateFlagStatus(rest[0], next, &note); err != nil {
		fmt.Fprintf(os.Stderr, "flags %s: %v\n", action, err)
		return exitError
	}
	fmt.Printf("flags %s: ok (id=%s status=%s)\n", action, rest[0], next)
	return exitOK
}
