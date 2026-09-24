// TestReadmeSpendTableMatchesCode binds the README spend table to the one
// in-code table (AC11): it locates the markdown table whose header line is
// exactly RenderSpendMarkdown()'s header, compares every following row to
// the render, and fails naming the drifted row. It also asserts every
// spends=true command name appears in the README's command-reference/spend
// section, so per-command spend claims cannot silently drift.
package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadmeSpendTableMatchesCode(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join(shippedRepoRoot(t), "README.md"))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(string(raw), "\n")
	want := strings.Split(strings.TrimSuffix(RenderSpendMarkdown(), "\n"), "\n")
	header := want[0]
	at := -1
	for i, line := range lines {
		if line == header {
			at = i
			break
		}
	}
	if at < 0 {
		t.Fatalf("README.md: spend table header %q not found verbatim", header)
	}
	for i, wantRow := range want {
		if at+i >= len(lines) {
			t.Fatalf("README.md: spend table truncated at row %d (%q missing)", i, wantRow)
		}
		if lines[at+i] != wantRow {
			t.Fatalf("README.md:%d: spend row drift: got %q, want %q", at+i+1, lines[at+i], wantRow)
		}
	}
	body := string(raw)
	for _, r := range SpendRows() {
		if !r.Spends {
			continue
		}
		if !strings.Contains(body, "council "+r.Name) && !strings.Contains(body, "`council "+r.Name+"`") {
			t.Fatalf("README.md: spends=true command %q not named in the command reference", r.Name)
		}
	}
}
