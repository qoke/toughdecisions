package main

import (
	"fmt"
	"os"
	"strings"
)

// HelpFlag describes one CLI flag for help output.
type HelpFlag struct {
	Name    string
	Type    string
	Default string
	Usage   string
}

// HelpSub describes one subcommand of a group command.
type HelpSub struct {
	Name    string
	Purpose string
	Flags   []HelpFlag
}

// HelpEntry is one top-level command in the registry: the single source of
// truth for help. SpendKey maps the entry (or a subcommand path) to a
// spend-table row; empty means the entry itself spends nothing and each
// spending subcommand renders its own SpendLine(subPath).
type HelpEntry struct {
	Name     string
	Purpose  string
	Subs     []HelpSub
	SpendKey string
}

// commandRegistry lists every dispatched top-level command. run() dispatches
// from it; root help lists it. A new command must add an entry here or the
// registry-completeness test fails.
var commandRegistry = []HelpEntry{
	{
		Name:    "serve",
		Purpose: "start the HTTP API server",
	},
	{
		Name:    "db",
		Purpose: "manage the store",
		Subs: []HelpSub{
			{Name: "migrate", Purpose: "apply pending migrations"},
			{Name: "prune", Purpose: "prune production rows older than N days", Flags: []HelpFlag{
				{Name: "older-than", Type: "int", Default: "0", Usage: "prune production rows older than N days (default: retention_production_days)"},
			}},
		},
	},
	{
		Name:    "pack",
		Purpose: "manage the prompt pack",
		Subs: []HelpSub{
			{Name: "init", Purpose: "load a seats YAML file into pack v1", Flags: []HelpFlag{
				{Name: "file", Type: "string", Default: "", Usage: "seats YAML file (default: pack_file from config)"},
			}},
			{Name: "show", Purpose: "show a pack (default: active)", Flags: []HelpFlag{
				{Name: "id", Type: "string", Default: "", Usage: "pack id (default: active)"},
			}},
			{Name: "publish", Purpose: "publish a candidate pack after its promotion checklist passes", Flags: []HelpFlag{
				{Name: "run", Type: "string", Default: "", Usage: "harness run id"},
				{Name: "candidate", Type: "string", Default: "", Usage: "candidate key"},
				{Name: "force", Type: "bool", Default: "false", Usage: "publish despite a failing checklist (recorded)"},
				{Name: "baselines-only", Type: "bool", Default: "false", Usage: "fill baselines for the active pack without publishing"},
			}},
			{Name: "rollback", Purpose: "roll back to the previous pack"},
		},
	},
	{
		Name:    "cases",
		Purpose: "validate and load the casepack",
		Subs: []HelpSub{
			{Name: "validate", Purpose: "validate the casepack dir", Flags: []HelpFlag{
				{Name: "dir", Type: "string", Default: "", Usage: "casepack dir (default: casepack_dir from config)"},
			}},
			{Name: "load", Purpose: "validate then upsert the casepack dir into the store", Flags: []HelpFlag{
				{Name: "dir", Type: "string", Default: "", Usage: "casepack dir (default: casepack_dir from config)"},
			}},
		},
	},
	{
		Name:    "graders",
		Purpose: "manage graders",
		Subs: []HelpSub{
			{Name: "calibrate", Purpose: "calibrate the selected graders", Flags: []HelpFlag{
				{Name: "file", Type: "string", Default: "", Usage: "graders YAML file (default: graders_file from config)"},
				{Name: "grader", Type: "string", Default: "", Usage: "calibrate only this grader key"},
				{Name: "all", Type: "bool", Default: "false", Usage: "calibrate all graders"},
			}},
			{Name: "status", Purpose: "sync the graders file and print admission state", Flags: []HelpFlag{
				{Name: "file", Type: "string", Default: "", Usage: "graders YAML file (default: graders_file from config)"},
			}},
		},
	},
	{
		Name:    "harness",
		Purpose: "run the evaluation harness",
		Subs: []HelpSub{
			{Name: "weekly", Purpose: "run weekly orchestration", Flags: []HelpFlag{
				{Name: "steps", Type: "string", Default: "", Usage: "comma list: sentinel,screen,compare,downstream,report (default: all)"},
				{Name: "fresh", Type: "bool", Default: "false", Usage: "regenerate responses with repetition+1"},
				{Name: "notify", Type: "bool", Default: "false", Usage: "POST the report to notify_webhook_url"},
			}},
			{Name: "sentinel", Purpose: "check fresh responses against baselines"},
			{Name: "screen", Purpose: "screen every candidate in the candidates file", Flags: []HelpFlag{
				{Name: "fresh", Type: "bool", Default: "false", Usage: "regenerate responses with repetition+1"},
			}},
			{Name: "compare", Purpose: "compare one candidate against the incumbent", Flags: []HelpFlag{
				{Name: "candidate", Type: "string", Default: "", Usage: "candidate key (required)"},
				{Name: "fresh", Type: "bool", Default: "false", Usage: "regenerate responses with repetition+1"},
			}},
			{Name: "downstream", Purpose: "evaluate one candidate downstream, then the promotion checklist", Flags: []HelpFlag{
				{Name: "candidate", Type: "string", Default: "", Usage: "candidate key (required)"},
				{Name: "run", Type: "string", Default: "", Usage: "compare run id (required)"},
				{Name: "fresh", Type: "bool", Default: "false", Usage: "regenerate responses with repetition+1"},
			}},
			{Name: "report", Purpose: "write the markdown report for one run", Flags: []HelpFlag{
				{Name: "run", Type: "string", Default: "", Usage: "harness run id"},
				{Name: "notify", Type: "bool", Default: "false", Usage: "POST the report to notify_webhook_url"},
			}},
		},
	},
	{
		Name:    "flags",
		Purpose: "list and resolve review flags",
		Subs: []HelpSub{
			{Name: "list", Purpose: "print flags by status", Flags: []HelpFlag{
				{Name: "status", Type: "string", Default: "open", Usage: "flag status: open|confirmed|dismissed"},
				{Name: "run", Type: "string", Default: "", Usage: "filter by run id (optional)"},
			}},
			{Name: "confirm", Purpose: "mark a flag confirmed with a note", Flags: []HelpFlag{
				{Name: "note", Type: "string", Default: "", Usage: "resolution note (required): flags <action> <id> --note \"...\""},
			}},
			{Name: "dismiss", Purpose: "mark a flag dismissed with a note", Flags: []HelpFlag{
				{Name: "note", Type: "string", Default: "", Usage: "resolution note (required): flags <action> <id> --note \"...\""},
			}},
		},
	},
	{
		Name:    "config",
		Purpose: "inspect configuration and check gateway reachability",
		Subs: []HelpSub{
			{Name: "show", Purpose: "print effective config with secrets masked"},
			{Name: "reference", Purpose: "print every config key with its env name and default"},
			{Name: "diagnose", Purpose: "print effective config with each value's source"},
			{Name: "check", Purpose: "check gateway reachability (opt-in live call)", Flags: []HelpFlag{
				{Name: "live", Type: "bool", Default: "false", Usage: "make exactly one live gateway call"},
			}},
		},
	},
	{
		Name:    "feedback",
		Purpose: "summarize response feedback",
		Subs: []HelpSub{
			{Name: "summary", Purpose: "print per-tag feedback counts for the last N days", Flags: []HelpFlag{
				{Name: "days", Type: "int", Default: "7", Usage: "look back N days"},
			}},
		},
	},
	{
		Name:    "help",
		Purpose: "print help for a command",
	},
}

// registryByName looks up one top-level command by name.
func registryByName(name string) (HelpEntry, bool) {
	for _, e := range commandRegistry {
		if e.Name == name {
			return e, true
		}
	}
	return HelpEntry{}, false
}

// isHelpFlag reports whether args is exactly one help flag.
func isHelpFlag(args []string) bool {
	return len(args) == 1 && (args[0] == "-h" || args[0] == "-help" || args[0] == "--help")
}

// renderRootHelp prints every registered command with its one-line purpose.
func renderRootHelp() {
	var sb strings.Builder
	sb.WriteString("usage: council <command> [args]\n\ncommands:\n")
	for _, e := range commandRegistry {
		fmt.Fprintf(&sb, "  %-10s %s\n", e.Name, e.Purpose)
	}
	sb.WriteString("\nrun `council help <command>` for command help; config keys are listed under `council config reference`.\n")
	os.Stdout.WriteString(sb.String())
}

// renderCommandHelp prints one command's purpose, its flags (for leaf
// commands), or its subcommands with their flags (for groups), plus the
// spend line from SpendLine for commands whose spend row has Spends=true.
// The spend text is never hand-copied: it always comes from SpendLine(key).
func renderCommandHelp(e HelpEntry) {
	var sb strings.Builder
	fmt.Fprintf(&sb, "usage: council %s", e.Name)
	if len(e.Subs) > 0 {
		sb.WriteString(" <subcommand> [flags]")
	} else {
		sb.WriteString(" [flags]")
	}
	sb.WriteString("\n\n  " + e.Name + ": " + e.Purpose + "\n")
	if len(e.Subs) == 0 {
		sb.WriteString(SpendLine(e.Name) + "\n")
		os.Stdout.WriteString(sb.String())
		return
	}
	for _, s := range e.Subs {
		fmt.Fprintf(&sb, "\n  %s %s: %s\n", e.Name, s.Name, s.Purpose)
		for _, f := range s.Flags {
			fmt.Fprintf(&sb, "    --%s (%s, default %s): %s\n", f.Name, f.Type, quoteDefault(f), f.Usage)
		}
		if key := subSpendKey(e.Name, s.Name); key != "" {
			sb.WriteString("    " + SpendLine(key) + "\n")
		}
	}
	sb.WriteString("\nconfig keys are listed under `council config reference`.\n")
	os.Stdout.WriteString(sb.String())
}

// renderSubHelp prints help for one subcommand path (e.g. "pack publish").
func renderSubHelp(top, sub string) {
	e, ok := registryByName(top)
	if !ok {
		return
	}
	for _, s := range e.Subs {
		if s.Name != sub {
			continue
		}
		var sb strings.Builder
		fmt.Fprintf(&sb, "usage: council %s %s [flags]\n\n  %s %s: %s\n", top, sub, top, sub, s.Purpose)
		for _, f := range s.Flags {
			fmt.Fprintf(&sb, "    --%s (%s, default %s): %s\n", f.Name, f.Type, quoteDefault(f), f.Usage)
		}
		if key := subSpendKey(top, sub); key != "" {
			sb.WriteString("    " + SpendLine(key) + "\n")
		}
		sb.WriteString("\nconfig keys are listed under `council config reference`.\n")
		os.Stdout.WriteString(sb.String())
		return
	}
}

// subSpendKey returns the spend-table key for a subcommand path, or "" when
// that path has no Spends=true row (nothing extra to render).
func subSpendKey(top, sub string) string {
	key := top + " " + sub
	if r, ok := spendByName(key); ok && r.Spends {
		return key
	}
	if r, ok := spendByName(key + " --live"); ok && r.Spends {
		return key + " --live"
	}
	return ""
}

// quoteDefault renders a flag default for help output.
func quoteDefault(f HelpFlag) string {
	if f.Type == "string" && f.Default != "" {
		return fmt.Sprintf("%q", f.Default)
	}
	return f.Default
}
