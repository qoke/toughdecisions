package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// TestDocsRunInstructionsAreExecutable guards the documented bootstrap
// surfaces — the fenced ```sh blocks in README.md that invoke council plus
// the "Bootstrap sequence (README)" restatement in DEVELOPMENT_PLAN.md —
// so the copy-paste commands stay executable as written. Per surface it
// asserts that the council binary is obtained before it is invoked, that
// `make build` precedes the first `db migrate`, and — only for surfaces
// that actually invoke `db migrate` — that `mkdir -p data` precedes it. It
// also asserts that no discovered doc/build file documents the forbidden
// single-file `go run` form, and that no discovered doc/build file leaks
// known secret shapes (key prefixes, JWT/PEM, credential URLs/params, or
// the private local endpoint as copy-pasteable config; line-split secrets,
// base64 blobs, `export VAR=<non-sk value>`, bare `Bearer <token>`,
// `localhost:4000`, and surfaces outside the walk are NOT covered).
// Doc/build files are discovered by walking (repo-root *.md, Makefile, Dockerfile,
// .github/workflows/*), never by a hardcoded name list. Every failure
// names file:line.
func TestDocsRunInstructionsAreExecutable(t *testing.T) {
	root := shippedRepoRoot(t)

	// (b) No discovered doc/build file may contain `go run` followed by a
	// token ending in `.go` (e.g. `go run cmd/council/main.go`,
	// `go run *.go`). The package form `go run ./cmd/council` does not
	// match and is allowed.
	goRunFileForm := regexp.MustCompile(`\bgo\s+run\s+\S+\.go\b`)
	for _, name := range docsDocBuildFiles(t, root) {
		name := name
		t.Run("no-single-file-go-run/"+docsSubtestName(name), func(t *testing.T) {
			for i, line := range docsReadFile(t, root, name) {
				if goRunFileForm.MatchString(line) {
					t.Errorf("%s:%d: forbidden single-file go run form: %s",
						name, i+1, strings.TrimSpace(line))
				}
			}
		})
	}

	// (d) No discovered doc/build surface may leak secrets: known key
	// prefixes (local dummy literal, sk-shaped, AWS/Slack/Google API keys),
	// JWT and PEM private-key shapes, credential-bearing URLs (with or
	// without a password in userinfo), credential query params
	// (case-insensitive, incl. apiKey/access_token/X-Amz-Signature), or the
	// private local endpoint presented as copy-pasteable config.
	// Every failure names file:line.
	// Residual gaps this guard knowingly does NOT cover: secrets split
	// across lines, base64-encoded or otherwise obfuscated blobs,
	// `export VAR=<non-sk value>`, a bare `Bearer <token>`,
	// `localhost:4000` (bare localhost without 127.0.0.1:4000 is allowed),
	// and surfaces outside the walk (docs/**, scripts, *.yaml, testdata).
	secretRe := []*regexp.Regexp{
		regexp.MustCompile(`local_dummy_key`),
		regexp.MustCompile(`sk-[A-Za-z0-9_-]{8,}`),
		regexp.MustCompile(`AKIA[0-9A-Z]{16}`),
		regexp.MustCompile(`ghp_[A-Za-z0-9]{20,}`),
		regexp.MustCompile(`xox[baprs]-[A-Za-z0-9-]{10,}`),
		regexp.MustCompile(`AIza[0-9A-Za-z_-]{30,}`),
		regexp.MustCompile(`eyJ[A-Za-z0-9_-]+\.eyJ[A-Za-z0-9_-]+\.`),
		regexp.MustCompile(`-----BEGIN [A-Z ]*PRIVATE KEY-----`),
		regexp.MustCompile(`[a-zA-Z][a-zA-Z0-9+.-]*://[^\s"'<>/]+@`), // passwordless userinfo too (https://KEY@host); <...> placeholders excluded
		regexp.MustCompile(`(?i)[?&(](api_key|apikey|apiKey|access_token|token|key|secret|password|x-amz-signature)=`),
		regexp.MustCompile(`127\.0\.0\.1:4000`),
	}
	for _, name := range docsDocBuildFiles(t, root) {
		name := name
		t.Run("no-leaked-secrets/"+docsSubtestName(name), func(t *testing.T) {
			for i, line := range docsReadFile(t, root, name) {
				for _, re := range secretRe {
					if re.MatchString(line) {
						t.Errorf("%s:%d: possible secret in docs surface: %s",
							name, i+1, strings.TrimSpace(line))
					}
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
			t.Run("make-build-before-db-migrate", func(t *testing.T) {
				checkDocsBuildBeforeMigrate(t, surf)
			})
		})
	}
}

// docsDocBuildFiles discovers the doc/build files the guard covers by
// walking narrowly: every repo-root *.md file, plus Makefile and
// Dockerfile when present, plus every file directly under
// .github/workflows. It never descends into subtrees, so testdata/vendor
// trees cannot cause false positives. It fails loudly when the walk finds
// nothing — a guard with no inputs proves nothing.
func docsDocBuildFiles(t *testing.T, root string) []string {
	t.Helper()
	var out []string
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatalf("read repo root: %v", err)
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		out = append(out, e.Name())
	}
	for _, name := range []string{"Makefile", "Dockerfile"} {
		if st, err := os.Stat(filepath.Join(root, name)); err == nil && !st.IsDir() {
			out = append(out, name)
		}
	}
	if entries, err := os.ReadDir(filepath.Join(root, ".github", "workflows")); err == nil {
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			out = append(out, filepath.Join(".github", "workflows", e.Name()))
		}
	}
	if len(out) == 0 {
		t.Fatalf(".: no doc/build files discovered")
	}
	sort.Strings(out)
	return out
}

// docsSubtestName maps a repo-relative file path to a t.Run-safe name:
// slashes would split the name into a hierarchy, so they become dashes.
func docsSubtestName(name string) string {
	return strings.ReplaceAll(name, "/", "-")
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
// fenced ```sh block in README.md that invokes council plus the
// "Bootstrap sequence (README)" restatement line in DEVELOPMENT_PLAN.md.
// A fence that never invokes council is not a bootstrap surface and is
// skipped. It fails the test loudly when no surface can be found — an
// empty scan proves nothing.
func docsBootstrapSurfaces(t *testing.T, root string) []docsSurface {
	t.Helper()
	var out []docsSurface

	councilRe := regexp.MustCompile("(^|[\\s`(])((\\./)?bin/)?council(\\s|$)")
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
		body := readme[start:j]
		invokes := false
		for _, line := range body {
			if docsIsComment(line) {
				continue
			}
			if councilRe.MatchString(line) {
				invokes = true
				break
			}
		}
		i = j
		if !invokes {
			continue
		}
		blocks++
		out = append(out, docsSurface{
			name:  fmt.Sprintf("README.md/sh-block-%d", blocks),
			file:  "README.md",
			first: start + 1,
			lines: body,
		})
	}
	if blocks == 0 {
		t.Fatalf("README.md:1: no ```sh bootstrap blocks invoking council found")
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

// checkDocsDataProvisioning fails, naming file:line, when a surface that
// actually invokes `db migrate` lacks `mkdir -p data` before its first
// non-comment `db migrate` — either the mkdir is absent or they are
// misordered (within or across lines). A surface with no `db migrate`
// invocation passes: the mkdir-before-migrate check applies only to
// fences that actually invoke the migration.
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
		return
	case mkdirLine < 0:
		t.Errorf("%s:%d: `mkdir -p data` missing before this first `db migrate`",
			surf.file, surf.first+migrateLine)
	case mkdirLine > migrateLine || (mkdirLine == migrateLine && mkdirCol > migrateCol):
		t.Errorf("%s:%d: `mkdir -p data` must precede first `db migrate` at %s:%d",
			surf.file, surf.first+mkdirLine, surf.file, surf.first+migrateLine)
	}
}

// checkDocsBuildBeforeMigrate fails, naming file:line, when a surface
// shows `db migrate` without a `make build` step before it: the bootstrap
// block must build the council binary before migrating the store. Comment
// lines never count as build steps.
func checkDocsBuildBeforeMigrate(t *testing.T, surf docsSurface) {
	t.Helper()
	buildLine := -1
	migrateLine := -1
	for i, line := range surf.lines {
		if docsIsComment(line) {
			continue
		}
		if buildLine < 0 && strings.Contains(line, "make build") {
			buildLine = i
		}
		if migrateLine < 0 && strings.Contains(line, "db migrate") {
			migrateLine = i
		}
	}
	if migrateLine < 0 {
		return
	}
	switch {
	case buildLine < 0:
		t.Errorf("%s:%d: `make build` missing before this `db migrate`",
			surf.file, surf.first+migrateLine)
	case buildLine > migrateLine:
		t.Errorf("%s:%d: `make build` must precede `db migrate` at %s:%d",
			surf.file, surf.first+buildLine, surf.file, surf.first+migrateLine)
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
