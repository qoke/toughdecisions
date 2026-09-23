package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestDocsRunInstructionsAreExecutable guards the documented bootstrap
// surfaces — the fenced ```sh blocks in README.md and the "Bootstrap
// sequence (README)" restatement in DEVELOPMENT_PLAN.md — so the copy-paste
// commands stay executable as written. Per surface it asserts that the
// council binary is obtained before it is invoked, that no tracked
// doc/build file documents the forbidden single-file `go run` form, and
// that `mkdir -p data` precedes the first `db migrate`. Every failure
// names file:line.
func TestDocsRunInstructionsAreExecutable(t *testing.T) {
	root := shippedRepoRoot(t)

	// (b) No tracked doc/build file may contain `go run` followed by a
	// token ending in `.go` (e.g. `go run cmd/council/main.go`,
	// `go run *.go`). The package form `go run ./cmd/council` does not
	// match and is allowed.
	goRunFileForm := regexp.MustCompile(`\bgo\s+run\s+\S+\.go\b`)
	for _, name := range []string{"README.md", "DEVELOPMENT_PLAN.md", "Makefile", "Dockerfile"} {
		name := name
		t.Run("no-single-file-go-run/"+name, func(t *testing.T) {
			for i, line := range docsReadFile(t, root, name) {
				if goRunFileForm.MatchString(line) {
					t.Errorf("%s:%d: forbidden single-file go run form: %s",
						name, i+1, strings.TrimSpace(line))
				}
			}
		})
	}

	// (a) and (c) per documented bootstrap surface.
	for _, surf := range docsBootstrapSurfaces(t, root) {
		surf := surf
		t.Run(surf.name, func(t *testing.T) {
			t.Run("obtain-binary-before-invocation", func(t *testing.T) {
				checkDocsObtainBinary(t, surf)
			})
			t.Run("mkdir-data-before-db-migrate", func(t *testing.T) {
				checkDocsDataProvisioning(t, surf)
			})
		})
	}
}

// docsSurface is one documented bootstrap surface: a contiguous run of
// lines from a repo-root doc, with first the 1-based line number of
// lines[0] in its file.
type docsSurface struct {
	name  string // subtest name, e.g. "README.md/sh-block-1"
	file  string // repo-relative file the lines come from
	first int    // 1-based line number of lines[0]
	lines []string
}

// docsBootstrapSurfaces extracts the documented bootstrap surfaces: every
// fenced ```sh block in README.md plus the "Bootstrap sequence (README)"
// restatement line in DEVELOPMENT_PLAN.md. It fails the test loudly when
// no surface can be found — an empty scan proves nothing.
func docsBootstrapSurfaces(t *testing.T, root string) []docsSurface {
	t.Helper()
	var out []docsSurface

	readme := docsReadFile(t, root, "README.md")
	blocks := 0
	for i := 0; i < len(readme); i++ {
		if !strings.HasPrefix(strings.TrimSpace(readme[i]), "```sh") {
			continue
		}
		start := i + 1
		j := start
		for j < len(readme) && !strings.HasPrefix(strings.TrimSpace(readme[j]), "```") {
			j++
		}
		if j >= len(readme) {
			t.Fatalf("README.md:%d: unterminated ```sh fence", start)
		}
		blocks++
		out = append(out, docsSurface{
			name:  fmt.Sprintf("README.md/sh-block-%d", blocks),
			file:  "README.md",
			first: start + 1,
			lines: readme[start:j],
		})
		i = j
	}
	if blocks == 0 {
		t.Fatalf("README.md:1: no ```sh bootstrap blocks found")
	}

	plan := docsReadFile(t, root, "DEVELOPMENT_PLAN.md")
	for i, line := range plan {
		if strings.Contains(line, "Bootstrap sequence (README)") {
			out = append(out, docsSurface{
				name:  "DEVELOPMENT_PLAN.md/bootstrap-sequence",
				file:  "DEVELOPMENT_PLAN.md",
				first: i + 1,
				lines: []string{line},
			})
			return out
		}
	}
	t.Fatalf("DEVELOPMENT_PLAN.md:1: \"Bootstrap sequence (README)\" restatement not found")
	return out
}

// checkDocsObtainBinary fails, naming file:line, when a surface invokes
// the council binary (bare `council `, `./bin/council `, or `bin/council `)
// on a non-comment line without first showing how to obtain it — one of
// `make build`, `go run ./cmd/council`, `go install`, or `docker`.
func checkDocsObtainBinary(t *testing.T, surf docsSurface) {
	t.Helper()
	// invocation = non-comment line (see docsIsComment) whose first
	// non-space character is not '#'.
	invocation := regexp.MustCompile("(^|[\\s`(])((\\./)?bin/)?council(\\s|$)")
	first := -1
	firstCol := -1
	for i, line := range surf.lines {
		if docsIsComment(line) {
			continue
		}
		if loc := invocation.FindStringIndex(line); loc != nil {
			first = i
			firstCol = loc[0]
			break
		}
	}
	if first < 0 {
		return
	}
	for i := 0; i < first; i++ {
		if docsIsComment(surf.lines[i]) {
			continue
		}
		if docsShowsBinary(surf.lines[i]) {
			return
		}
	}
	if col := docsObtainCol(surf.lines[first]); col >= 0 && col <= firstCol {
		return
	}
	t.Errorf("%s:%d: council invoked without obtaining the binary first (need `make build`, `go run ./cmd/council`, `go install`, or `docker` above this line)",
		surf.file, surf.first+first)
}

// checkDocsDataProvisioning fails, naming file:line, when a surface lacks
// `mkdir -p data` before its first non-comment `db migrate` invocation —
// either the mkdir is absent, the migration is absent, or they are
// misordered (within or across lines).
func checkDocsDataProvisioning(t *testing.T, surf docsSurface) {
	t.Helper()
	mkdirLine, mkdirCol := -1, -1
	migrateLine, migrateCol := -1, -1
	for i, line := range surf.lines {
		if docsIsComment(line) {
			continue
		}
		if migrateLine < 0 {
			if j := strings.Index(line, "db migrate"); j >= 0 {
				migrateLine, migrateCol = i, j
			}
		}
		if mkdirLine < 0 {
			if j := strings.Index(line, "mkdir -p data"); j >= 0 {
				mkdirLine, mkdirCol = i, j
			}
		}
	}
	switch {
	case migrateLine < 0:
		t.Errorf("%s:%d: bootstrap surface has no `db migrate` invocation",
			surf.file, surf.first)
	case mkdirLine < 0:
		t.Errorf("%s:%d: `mkdir -p data` missing before this first `db migrate`",
			surf.file, surf.first+migrateLine)
	case mkdirLine > migrateLine || (mkdirLine == migrateLine && mkdirCol > migrateCol):
		t.Errorf("%s:%d: `mkdir -p data` must precede first `db migrate` at %s:%d",
			surf.file, surf.first+mkdirLine, surf.file, surf.first+migrateLine)
	}
}

// docsObtainCol returns the column of the earliest binary-obtain marker
// (`make build`, `go run ./cmd/council`, `go install`, `docker`) in line,
// or -1 when the line shows none of them.
func docsObtainCol(line string) int {
	col := -1
	for _, marker := range []string{"make build", "go run ./cmd/council", "go install", "docker"} {
		if j := strings.Index(line, marker); j >= 0 && (col < 0 || j < col) {
			col = j
		}
	}
	return col
}

// docsShowsBinary reports whether a doc line shows how to obtain the
// council binary before invoking it.
func docsShowsBinary(line string) bool {
	return docsObtainCol(line) >= 0
}

// docsIsComment reports whether a doc line is a comment: a line whose
// first non-space character is '#'. Comments never count as invocations.
func docsIsComment(line string) bool {
	return strings.HasPrefix(strings.TrimSpace(line), "#")
}

// docsReadFile reads a repo-root doc/build file and returns its lines,
// failing the test loudly when the file cannot be read — a guard whose
// input is missing proves nothing.
func docsReadFile(t *testing.T, root, name string) []string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(root, name))
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return strings.Split(string(raw), "\n")
}
