package models

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/qoke/toughdecisions/internal/pack"
	"github.com/qoke/toughdecisions/internal/store"
)

// TestRepoConfigLoadsTogether loads the REAL repo config files via pack.Init
// + LoadRegistry and asserts every pack seat's model is known to the registry
// with all referenced settings supported.
func TestRepoConfigLoadsTogether(t *testing.T) {
	root := repoRoot(t)
	packFile := filepath.Join(root, "config", "pack.yaml")
	modelsFile := filepath.Join(root, "config", "models.yaml")
	for _, f := range []string{packFile, modelsFile} {
		if _, err := os.Stat(f); err != nil {
			t.Fatalf("stat %s: %v", f, err)
		}
	}
	db, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	p, err := pack.Init(db, packFile)
	if err != nil {
		t.Fatalf("pack.Init(%s): %v", packFile, err)
	}
	reg, err := LoadRegistry(modelsFile)
	if err != nil {
		t.Fatalf("LoadRegistry: %v", err)
	}
	if len(p.Seats) != len(pack.AllSeats) {
		t.Fatalf("pack seats = %d, want %d", len(p.Seats), len(pack.AllSeats))
	}
	for _, seat := range pack.AllSeats {
		sc, ok := p.Seats[seat]
		if !ok {
			t.Fatalf("pack missing seat %q", seat)
		}
		if err := reg.ValidateSettings(sc); err != nil {
			t.Errorf("seat %q (model %q): %v", seat, sc.Model, err)
		}
	}
}

// repoRoot walks up from the current directory to the module root
// (the directory containing go.mod).
func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("go.mod not found")
		}
		dir = parent
	}
}
