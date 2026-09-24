package main

import (
	"strings"
	"testing"
)

// RED: the spend table is the single in-code source of truth for token
// spend (R-06, AC11). Step 3 consumes SpendLine and Step 6 consumes
// RenderSpendMarkdown, so both signatures are part of the contract.
func TestSpendInvariants(t *testing.T) {
	rows := SpendRows()
	if len(rows) == 0 {
		t.Fatal("SpendRows() is empty")
	}
	seen := map[string]bool{}
	for _, r := range rows {
		if r.Name == "" {
			t.Fatal("spend row with empty Name")
		}
		if seen[r.Name] {
			t.Fatalf("duplicate spend row %q", r.Name)
		}
		seen[r.Name] = true
		if r.Spends && (r.Calls == "" || r.PerRun == "") {
			t.Fatalf("spend row %q must have Calls and PerRun", r.Name)
		}
	}
}

func TestSpendNoSpendSetExact(t *testing.T) {
	rows := SpendRows()
	byName := map[string]SpendRow{}
	for _, r := range rows {
		byName[r.Name] = r
	}
	wantNoSpend := []string{
		"serve startup",
		"db migrate", "db prune",
		"pack init", "pack show", "pack rollback",
		"cases validate", "cases load",
		"graders status",
		"flags list", "flags confirm", "flags dismiss",
		"feedback summary",
		"harness report",
		"config show", "config reference", "config diagnose", "config check",
	}
	if len(wantNoSpend) != 18 {
		t.Fatalf("test lists %d no-spend rows, want 18", len(wantNoSpend))
	}
	for _, name := range wantNoSpend {
		r, ok := byName[name]
		if !ok {
			t.Fatalf("missing no-spend row %q", name)
		}
		if r.Spends {
			t.Fatalf("row %q marked Spends, want no-spend", name)
		}
	}
	for _, r := range rows {
		if !r.Spends {
			continue
		}
		for _, name := range wantNoSpend {
			if r.Name == name {
				t.Fatalf("row %q in both spend and no-spend sets", name)
			}
		}
	}
}

func TestSpendRowsPresent(t *testing.T) {
	rows := SpendRows()
	byName := map[string]SpendRow{}
	for _, r := range rows {
		byName[r.Name] = r
	}
	for _, name := range []string{
		"serve", "harness sentinel", "harness screen", "harness compare",
		"harness downstream", "harness weekly", "graders calibrate",
		"pack publish", "config check --live",
	} {
		r, ok := byName[name]
		if !ok {
			t.Fatalf("missing spend row %q", name)
		}
		if !r.Spends {
			t.Fatalf("row %q must Spend", name)
		}
	}
}

func TestSpendLineMentionsCommandAndStatus(t *testing.T) {
	for _, name := range []string{"serve", "pack publish", "db migrate", "config check"} {
		line := SpendLine(name)
		if !strings.Contains(line, name) {
			t.Fatalf("SpendLine(%q) = %q, want it to mention the command", name, line)
		}
		r, ok := spendByName(name)
		if !ok {
			t.Fatalf("no spend row %q", name)
		}
		if r.Spends && !strings.Contains(strings.ToLower(line), "spend") {
			t.Fatalf("SpendLine(%q) = %q, want spend status", name, line)
		}
		if !r.Spends && strings.Contains(strings.ToLower(line), "1 model call") {
			t.Fatalf("SpendLine(%q) = %q, want no-spend status", name, line)
		}
	}
}

func TestSpendMarkdownContainsEveryRow(t *testing.T) {
	md := RenderSpendMarkdown()
	for _, r := range SpendRows() {
		if !strings.Contains(md, r.Name) {
			t.Fatalf("markdown missing row %q", r.Name)
		}
	}
	for _, want := range []string{"|", "Command", "Calls"} {
		if !strings.Contains(md, want) {
			t.Fatalf("markdown missing %q", want)
		}
	}
}
