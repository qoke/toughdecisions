// Package logx provides the iqlog-backed council Logger.
package logx

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/iqhive/iqlog"
	"github.com/qoke/toughdecisions/internal/config"
)

// Logger is the council logging interface (slog-style kv pairs).
type Logger interface {
	Debug(msg string, kv ...any)
	Info(msg string, kv ...any)
	Warn(msg string, kv ...any)
	Error(msg string, kv ...any)
	With(kv ...any) Logger
}

type logger struct {
	base   *iqlog.Logger
	fields map[string]any
	redact bool
}

// contentKeys are stripped from log output when redaction is enabled.
var contentKeys = map[string]struct{}{
	"card":          {},
	"messages":      {},
	"response_text": {},
}

// New builds a JSON logger on stderr with the level from cfg.
func New(cfg *config.Config) Logger {
	return newLogger(cfg, os.Stderr)
}

// NewWithWriter builds the same logger writing to w (for tests).
func NewWithWriter(cfg *config.Config, w io.Writer) Logger {
	return newLogger(cfg, w)
}

func newLogger(cfg *config.Config, w io.Writer) Logger {
	lvl := iqlog.LevelInfo
	switch strings.ToLower(cfg.LogLevel()) {
	case "debug":
		lvl = iqlog.LevelDebug
	case "warn":
		lvl = iqlog.LevelWarn
	case "error":
		lvl = iqlog.LevelError
	}
	base := iqlog.MustNew(iqlog.Config{Format: iqlog.FormatJSON, Level: lvl, Writer: w})
	return &logger{base: base, redact: cfg.LogRedactContent()}
}

func (l *logger) Debug(msg string, kv ...any) { l.emit(l.base.DebugEvent(), msg, kv) }
func (l *logger) Info(msg string, kv ...any)  { l.emit(l.base.InfoEvent(), msg, kv) }
func (l *logger) Warn(msg string, kv ...any)  { l.emit(l.base.WarnEvent(), msg, kv) }
func (l *logger) Error(msg string, kv ...any) { l.emit(l.base.ErrorEvent(), msg, kv) }

func (l *logger) emit(ev *iqlog.Event, msg string, kv []any) {
	if ev == nil {
		return
	}
	merged := make(map[string]any, len(l.fields)+len(kv)/2)
	for k, v := range l.fields {
		merged[k] = v
	}
	for k, v := range kvToMap(kv) {
		merged[k] = v
	}
	for k, v := range redactContent(merged, l.redact) {
		ev.Any(k, v)
	}
	ev.Msg(msg)
}

// With returns a Logger with the given kv pairs attached to every line.
func (l *logger) With(kv ...any) Logger {
	next := &logger{base: l.base, redact: l.redact, fields: map[string]any{}}
	for k, v := range l.fields {
		next.fields[k] = v
	}
	for k, v := range kvToMap(kv) {
		next.fields[k] = v
	}
	return next
}

func kvToMap(kv []any) map[string]any {
	out := make(map[string]any, len(kv)/2)
	for i := 0; i+1 < len(kv); i += 2 {
		key, ok := kv[i].(string)
		if !ok {
			key = fmt.Sprint(kv[i])
		}
		out[key] = kv[i+1]
	}
	return out
}

// redactContent strips content keys (and never-logged secrets) when redaction is on.
func redactContent(fields map[string]any, redact bool) map[string]any {
	delete(fields, "gateway_api_key")
	if !redact {
		return fields
	}
	for k := range contentKeys {
		delete(fields, k)
	}
	return fields
}
