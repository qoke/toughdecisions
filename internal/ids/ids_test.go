package ids_test

import (
	"sort"
	"testing"

	"github.com/qoke/toughdecisions/internal/ids"
)

func TestNewIDUniqueSortableAndSized(t *testing.T) {
	const n = 100
	got := make([]string, 0, n)
	seen := map[string]struct{}{}
	for i := 0; i < n; i++ {
		id := ids.NewID()
		if len(id) != 26 {
			t.Fatalf("NewID() length = %d, want 26 (id %q)", len(id), id)
		}
		if _, dup := seen[id]; dup {
			t.Fatalf("duplicate ID %q", id)
		}
		seen[id] = struct{}{}
		got = append(got, id)
	}
	if !sort.StringsAreSorted(got) {
		t.Fatalf("IDs generated in order are not lexicographically non-decreasing")
	}
}
