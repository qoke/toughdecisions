package schema_test

import (
	"strings"
	"testing"

	"github.com/qoke/toughdecisions/internal/render"
	"github.com/qoke/toughdecisions/internal/schema"
)

// TestParseRenderRawConsistent asserts parse/render agree on unparseable
// input: ParseLenient fails and render.Raw still produces a stable,
// non-empty rendering containing the raw text.
func TestParseRenderRawConsistent(t *testing.T) {
	raw := "not json at all — just prose with { unbalanced braces"
	if _, ok := schema.ParseLenient[schema.View](raw); ok {
		t.Fatal("ParseLenient[View] accepted unparseable input")
	}
	if _, ok := schema.ParseLenient[schema.Judge](raw); ok {
		t.Fatal("ParseLenient[Judge] accepted unparseable input")
	}
	first := render.Raw(raw)
	second := render.Raw(raw)
	if first != second {
		t.Fatalf("render.Raw not deterministic:\n%q\n%q", first, second)
	}
	if strings.TrimSpace(first) == "" {
		t.Fatal("render.Raw returned empty output")
	}
	if !strings.Contains(first, raw) {
		t.Fatalf("render.Raw dropped raw text:\n%s", first)
	}
}
