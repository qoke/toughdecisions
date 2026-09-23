package logx_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/qoke/toughdecisions/internal/logx"
)

// parseRecord decodes the single JSON line written to the test buffer.
func parseRecord(t *testing.T, out string) map[string]any {
	t.Helper()
	trimmed := strings.TrimSpace(out)
	if trimmed == "" {
		t.Fatal("log output is empty, want one JSON record")
	}
	var rec map[string]any
	if err := json.Unmarshal([]byte(trimmed), &rec); err != nil {
		t.Fatalf("output is not JSON: %v (%q)", err, out)
	}
	return rec
}

// TestWarnAndErrorEmitRecord covers the Warn/Error methods and the
// warn/error level branches of newLogger: each level must let its own
// record through to the writer.
func TestWarnAndErrorEmitRecord(t *testing.T) {
	cases := []struct {
		level string
		emit  func(l logx.Logger, msg string)
		msg   string
	}{
		{level: "warn", emit: func(l logx.Logger, m string) { l.Warn(m, "k", "v") }, msg: "warned-line"},
		{level: "error", emit: func(l logx.Logger, m string) { l.Error(m, "k", "v") }, msg: "errored-line"},
	}
	for _, tc := range cases {
		t.Run("should emit the record when log level is "+tc.level, func(t *testing.T) {
			// Arrange
			cfg := loadTestConfig(t, tc.level, false)
			var buf strings.Builder
			l := logx.NewWithWriter(cfg, &buf)

			// Act
			tc.emit(l, tc.msg)

			// Assert
			rec := parseRecord(t, buf.String())
			if rec["message"] != tc.msg {
				t.Fatalf("message = %v, want %q", rec["message"], tc.msg)
			}
			if rec["k"] != "v" {
				t.Fatalf("k = %v, want v", rec["k"])
			}
		})
	}
}

// TestInfoDroppedWhenLevelIsError covers emit's nil-event guard: a record
// below the configured level must be discarded without touching the writer.
func TestInfoDroppedWhenLevelIsError(t *testing.T) {
	// Arrange
	cfg := loadTestConfig(t, "error", false)
	var buf strings.Builder
	l := logx.NewWithWriter(cfg, &buf)

	// Act
	l.Info("must-be-dropped", "k", "v")

	// Assert
	if got := buf.String(); got != "" {
		t.Fatalf("output = %q, want empty output below the configured level", got)
	}
}

// TestChainedWithKeepsEarlierFields covers the field-copy loop in With: a
// logger derived from another derived logger must still carry every ancestor
// field.
func TestChainedWithKeepsEarlierFields(t *testing.T) {
	// Arrange
	cfg := loadTestConfig(t, "debug", false)
	var buf strings.Builder

	// Act
	l := logx.NewWithWriter(cfg, &buf).With("first", "1").With("second", "2")
	l.Info("chain-msg")

	// Assert
	rec := parseRecord(t, buf.String())
	if rec["first"] != "1" || rec["second"] != "2" {
		t.Fatalf("fields = first:%v second:%v, want 1 and 2", rec["first"], rec["second"])
	}
	if rec["message"] != "chain-msg" {
		t.Fatalf("message = %v, want chain-msg", rec["message"])
	}
}

// TestNonStringKeyIsStringified covers kvToMap's non-string key branch: a
// numeric key must still land in the record under its printed form.
func TestNonStringKeyIsStringified(t *testing.T) {
	// Arrange
	cfg := loadTestConfig(t, "debug", false)
	var buf strings.Builder
	l := logx.NewWithWriter(cfg, &buf)

	// Act
	l.Info("kv-msg", 42, "value")

	// Assert
	rec := parseRecord(t, buf.String())
	if rec["42"] != "value" {
		t.Fatalf("record = %+v, want key \"42\" with value \"value\"", rec)
	}
}
