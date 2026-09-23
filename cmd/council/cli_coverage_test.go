package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestCLIRejectsBadFlags covers argument-validation branches that must exit 2
// before any store, config, or network I/O happens.
func TestCLIRejectsBadFlags(t *testing.T) {
	cases := []struct {
		name string
		fn   func() int
		want int
	}{
		{name: "should reject when harness compare gets unexpected args", fn: func() int { return harnessCompare([]string{"positional"}) }, want: exitValidation},
		{name: "should reject when harness downstream gets unexpected args", fn: func() int { return harnessDownstream([]string{"positional"}) }, want: exitValidation},
		{name: "should reject when harness screen gets an unknown flag", fn: func() int { return harnessScreen([]string{"--bogus"}) }, want: exitValidation},
		{name: "should reject when graders calibrate gets an unknown flag", fn: func() int { return gradersCalibrate([]string{"--bogus"}) }, want: exitValidation},
		{name: "should reject when pack show gets an unknown flag", fn: func() int { return packShow([]string{"--bogus"}) }, want: exitValidation},
		{name: "should reject when feedback summary gets an unknown flag", fn: func() int { return feedbackSummary([]string{"--bogus"}) }, want: exitValidation},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Act
			got := tc.fn()

			// Assert
			if got != tc.want {
				t.Fatalf("exit = %d, want %d", got, tc.want)
			}
		})
	}
}

// TestCLIFailsWhenStoreCannotOpen points COUNCIL_DB_PATH at a directory, so
// SQLite cannot open it. Every store-backed command must exit 1 with a clean
// error instead of panicking or proceeding.
func TestCLIFailsWhenStoreCannotOpen(t *testing.T) {
	// Arrange
	testEnv(t)
	t.Setenv("COUNCIL_DB_PATH", t.TempDir()) // a directory is not a database file

	cases := []struct {
		name string
		fn   func() int
		want int
	}{
		{name: "should fail when cases validate cannot open the store", fn: func() int { return casesValidate(nil) }, want: exitError},
		{name: "should fail when cases load cannot open the store", fn: func() int { return casesLoad(nil) }, want: exitError},
		{name: "should fail when db prune cannot open the store", fn: func() int { return dbPrune(nil) }, want: exitError},
		{name: "should fail when graders calibrate cannot open the store", fn: func() int { return gradersCalibrate(nil) }, want: exitError},
		{name: "should fail when graders status cannot open the store", fn: func() int { return gradersStatus(nil) }, want: exitError},
		{name: "should fail when flags list cannot open the store", fn: func() int { return flagsList(nil) }, want: exitError},
		{name: "should fail when pack publish cannot open the store", fn: func() int { return packPublish(nil) }, want: exitError},
		{name: "should fail when harness compare cannot open the store", fn: func() int { return harnessCompare([]string{"--candidate", "x"}) }, want: exitError},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Act
			got := tc.fn()

			// Assert
			if got != tc.want {
				t.Fatalf("exit = %d, want %d", got, tc.want)
			}
		})
	}
}

// TestCasesLoadFailsWhenDirMissing verifies a bad --dir exits 1 (load error),
// not 2: the dir is parsed as a flag, then the load itself fails.
func TestCasesLoadFailsWhenDirMissing(t *testing.T) {
	// Arrange
	testEnv(t)
	missing := filepath.Join(t.TempDir(), "missing")

	// Act
	got := casesLoad([]string{"--dir", missing})

	// Assert
	if got != exitError {
		t.Fatalf("exit = %d, want %d (load failure)", got, exitError)
	}
}

// TestCasesValidateRejectsInvalidFamily verifies validation failures exit 2
// (not 1): the family parses and loads, but casepack.Validate rejects the
// unknown split.
func TestCasesValidateRejectsInvalidFamily(t *testing.T) {
	// Arrange
	dir := testEnv(t)
	writeCasepackDir(t, dir)
	src := filepath.Join(dir, "casepack", "families", "f001.yaml")
	raw, err := os.ReadFile(src)
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	bad := strings.Replace(string(raw), "split: selection", "split: bogus", 1)
	if bad == string(raw) {
		t.Fatalf("fixture marker split: selection not found in %s", src)
	}
	if err := os.WriteFile(src, []byte(bad), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	// Act
	got := casesValidate([]string{"--dir", filepath.Join(dir, "casepack")})

	// Assert: exitValidation proves we reached casepack.Validate (a load
	// failure would exit exitError instead).
	if got != exitValidation {
		t.Fatalf("exit = %d, want %d (validation failure)", got, exitValidation)
	}
}

// TestPackShowFailsForUnknownPackID verifies pack show surfaces a store
// lookup failure as exit 1 instead of printing a pack.
func TestPackShowFailsForUnknownPackID(t *testing.T) {
	// Arrange
	testEnv(t)

	// Act
	got := packShow([]string{"--id", "no-such-pack"})

	// Assert
	if got != exitError {
		t.Fatalf("exit = %d, want %d", got, exitError)
	}
}

// TestHarnessCommandsFailWhenCandidatesFileUnreadable verifies the candidate
// load failure branches exit 1 before any comparison or downstream work runs.
func TestHarnessCommandsFailWhenCandidatesFileUnreadable(t *testing.T) {
	cases := []struct {
		name string
		fn   func() int
	}{
		{
			name: "should fail when harness compare cannot read the candidates file",
			fn:   func() int { return harnessCompare([]string{"--candidate", "some-candidate"}) },
		},
		{
			name: "should fail when harness downstream cannot read the candidates file",
			fn:   func() int { return harnessDownstream([]string{"--candidate", "some-candidate", "--run", "run-1"}) },
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Arrange: testEnv never creates candidates.yaml
			testEnv(t)

			// Act
			got := tc.fn()

			// Assert
			if got != exitError {
				t.Fatalf("exit = %d, want %d", got, exitError)
			}
		})
	}
}

// TestHarnessReportRejectsPathEscapingRunID verifies report exits 1 for a
// run id that would escape the reports dir ("..") instead of writing a file.
func TestHarnessReportRejectsPathEscapingRunID(t *testing.T) {
	// Arrange
	testEnv(t)

	// Act
	got := harnessReport([]string{"--run", ".."})

	// Assert
	if got != exitError {
		t.Fatalf("exit = %d, want %d", got, exitError)
	}
}

// TestPackPublishFailsWhenModelsFileMissing verifies the models registry
// load failure exits 1 before any publish attempt.
func TestPackPublishFailsWhenModelsFileMissing(t *testing.T) {
	// Arrange
	testEnv(t)
	t.Setenv("COUNCIL_MODELS_FILE", filepath.Join(t.TempDir(), "missing.yaml"))

	// Act
	got := packPublish(nil)

	// Assert
	if got != exitError {
		t.Fatalf("exit = %d, want %d", got, exitError)
	}
}

// TestDbPruneRejectsZeroRetentionDays verifies that falling back to a zero
// retention setting is a validation error (exit 2), not a prune of everything.
func TestDbPruneRejectsZeroRetentionDays(t *testing.T) {
	// Arrange
	testEnv(t)
	t.Setenv("COUNCIL_RETENTION_PRODUCTION_DAYS", "0")

	// Act
	got := dbPrune(nil)

	// Assert
	if got != exitValidation {
		t.Fatalf("exit = %d, want %d", got, exitValidation)
	}
}
