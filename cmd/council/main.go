// Command council is the Relationship Council CLI entry point.
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/qoke/toughdecisions/internal/config"
	"github.com/qoke/toughdecisions/internal/logx"
)

// Exit codes: 0 ok, 1 error, 2 validation failure, 3 blocked.
const (
	exitOK         = 0
	exitError      = 1
	exitValidation = 2
	exitBlocked    = 3
)

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	if len(args) == 0 {
		usage()
		return exitValidation
	}
	switch args[0] {
	case "serve":
		return serve(args[1:])
	case "db":
		return dispatch(args[0], args[1:], map[string]handler{
			"migrate": dbMigrate,
			"prune":   dbPrune,
		})
	case "pack":
		return dispatch(args[0], args[1:], map[string]handler{
			"init":     packInit,
			"show":     packShow,
			"publish":  packPublish,
			"rollback": packRollback,
		})
	case "cases":
		return dispatch(args[0], args[1:], map[string]handler{
			"validate": phaseStub("cases validate", 3),
			"load":     phaseStub("cases load", 3),
		})
	case "graders":
		return dispatch(args[0], args[1:], map[string]handler{
			"calibrate": phaseStub("graders calibrate", 4),
			"status":    phaseStub("graders status", 4),
		})
	case "harness":
		return dispatch(args[0], args[1:], map[string]handler{
			"weekly":     phaseStub("harness weekly", 5),
			"sentinel":   phaseStub("harness sentinel", 5),
			"screen":     phaseStub("harness screen", 5),
			"compare":    phaseStub("harness compare", 5),
			"downstream": phaseStub("harness downstream", 5),
			"report":     phaseStub("harness report", 5),
		})
	case "flags":
		return dispatch(args[0], args[1:], map[string]handler{
			"list":    phaseStub("flags list", 4),
			"confirm": phaseStub("flags confirm", 4),
			"dismiss": phaseStub("flags dismiss", 4),
		})
	case "feedback":
		return dispatch(args[0], args[1:], map[string]handler{
			"summary": feedbackSummary,
		})
	case "-h", "-help", "--help", "help":
		usage()
		return exitOK
	default:
		fmt.Fprintf(os.Stderr, "unknown subcommand %q\n", args[0])
		usage()
		return exitValidation
	}
}

type handler func(args []string) int

func stub(name string) handler {
	return phaseStub(name, 0)
}

func dispatch(top string, args []string, subs map[string]handler) int {
	if len(args) == 0 {
		fmt.Fprintf(os.Stderr, "%s: missing subcommand\n", top)
		return exitValidation
	}
	h, ok := subs[args[0]]
	if !ok {
		fmt.Fprintf(os.Stderr, "%s: unknown subcommand %q\n", top, args[0])
		return exitValidation
	}
	return h(args[1:])
}

func serve(args []string) int {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	if err := fs.Parse(args); err != nil {
		return exitValidation
	}
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "serve: %v\n", err)
		return exitError
	}
	log := logx.New(cfg)
	log.Debug("effective config", "config", cfg.RedactedString())
	if err := serveHTTP(cfg, log); err != nil {
		fmt.Fprintf(os.Stderr, "serve: %v\n", err)
		return exitError
	}
	return exitOK
}

func usage() {
	fmt.Fprintf(os.Stderr, `usage: council <subcommand> [flags]

  serve
  db migrate | db prune --older-than <days>
  pack init|show|publish|rollback
  cases validate|load
  graders calibrate|status
  harness weekly|sentinel|screen|compare|downstream|report
  flags list|confirm|dismiss
  feedback summary
`)
}
