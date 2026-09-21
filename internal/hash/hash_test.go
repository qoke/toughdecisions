package hash_test

import (
	"strings"
	"testing"

	"github.com/qoke/toughdecisions/internal/hash"
)

func TestCanonicalJSONIgnoresMapKeyOrder(t *testing.T) {
	a := map[string]any{"b": 2, "a": 1, "c": map[string]any{"z": 1, "y": 2}}
	b := map[string]any{"c": map[string]any{"y": 2, "z": 1}, "a": 1, "b": 2}

	if got := string(hash.CanonicalJSON(a)); got != string(hash.CanonicalJSON(b)) {
		t.Fatalf("key order changed canonical output: %q vs %q", hash.CanonicalJSON(a), hash.CanonicalJSON(b))
	}
}

func TestCanonicalJSONHasNoInsignificantWhitespace(t *testing.T) {
	out := string(hash.CanonicalJSON(map[string]any{"a": []any{1, 2}, "b": "x"}))
	if strings.ContainsAny(out, " \n\t") {
		t.Fatalf("canonical JSON contains whitespace: %q", out)
	}
	if want := `{"a":[1,2],"b":"x"}`; out != want {
		t.Fatalf("got %q want %q", out, want)
	}
}

func TestSHA256HexStableAndSensitive(t *testing.T) {
	h1 := hash.SHA256Hex([]byte("hello"), []byte("world"))
	h2 := hash.SHA256Hex([]byte("hello"), []byte("world"))
	h3 := hash.SHA256Hex([]byte("hello!"), []byte("world"))
	if h1 != h2 {
		t.Fatalf("SHA256Hex not stable: %q vs %q", h1, h2)
	}
	if h1 == h3 {
		t.Fatalf("SHA256Hex did not change on input change: %q", h1)
	}
	if len(h1) != 64 {
		t.Fatalf("SHA256Hex length = %d, want 64", len(h1))
	}
}
