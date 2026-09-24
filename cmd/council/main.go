// Command council is the Tough Decisions Council CLI entry point.
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
		renderRootHelp()
		return exitOK
	}
	switch args[0] {
	case "serve":
		if isHelpFlag(args[1:]) {
			e, _ := registryByName("serve")
			renderCommandHelp(e)
			return exitOK
		}
		return serve(args[1:])
	case "config":
		if isHelpFlag(args[1:]) {
			e, _ := registryByName("config")
			renderCommandHelp(e)
			return exitOK
		}
		if len(args) == 3 && isHelpFlag(args[2:]) {
			renderSubHelp("config", args[1])
			return exitOK
		}
		return configCmd(args[1:])
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
			"validate": casesValidate,
			"load":     casesLoad,
		})
	case "graders":
		return dispatch(args[0], args[1:], map[string]handler{
			"calibrate": gradersCalibrate,
			"status":    gradersStatus,
		})
	case "harness":
		return dispatch(args[0], args[1:], map[string]handler{
			"weekly":     harnessWeekly,
			"sentinel":   harnessSentinel,
			"screen":     harnessScreen,
			"compare":    harnessCompare,
			"downstream": harnessDownstream,
			"report":     harnessReport,
		})
	case "flags":
		return dispatch(args[0], args[1:], map[string]handler{
			"list":    flagsList,
			"confirm": flagsConfirm,
			"dismiss": flagsDismiss,
		})
	case "feedback":
		return dispatch(args[0], args[1:], map[string]handler{
			"summary": feedbackSummary,
		})
	case "-h", "-help", "--help", "help":
		if len(args) >= 2 {
			if e, ok := registryByName(args[1]); ok {
				if len(args) >= 3 {
					for _, sub := range e.Subs {
						if sub.Name == args[2] {
							renderSubHelp(args[1], args[2])
							return exitOK
						}
					}
				}
				renderCommandHelp(e)
				return exitOK
			}
		}
		renderRootHelp()
		return exitOK
	default:
		fmt.Fprintf(os.Stderr, "unknown subcommand %q (want one of: serve db pack cases graders harness flags config feedback help; run `council help` for help)\n", args[0])
		renderRootHelp()
		return exitValidation
	}
}

type handler func(args []string) int

func stub(name string) handler {
	return phaseStub(name, 0)
}

func dispatch(top string, args []string, subs map[string]handler) int {
	if len(args) == 0 {
		if e, ok := registryByName(top); ok {
			renderCommandHelp(e)
		}
		return exitValidation
	}
	if isHelpFlag(args) {
		if e, ok := registryByName(top); ok {
			renderCommandHelp(e)
			return exitOK
		}
	}
	h, ok := subs[args[0]]
	if !ok {
		if isHelpFlag(args) {
			if e, ok := registryByName(top); ok {
				renderCommandHelp(e)
				return exitOK
			}
		}
		fmt.Fprintf(os.Stderr, "%s: unknown subcommand %q (run `council %s --help` or `council help` for help)\n", top, args[0], top)
		if e, ok := registryByName(top); ok {
			renderCommandHelp(e)
		}
		return exitValidation
	}
	if isHelpFlag(args[1:]) {
		renderSubHelp(top, args[0])
		return exitOK
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
	if err := requireGatewayKey(cfg); err != nil {
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

// usage prints root help to stdout. Kept for existing callers; new code
// calls renderRootHelp directly.
func usage() {
	renderRootHelp()
}
