//go:build unix || windows

package initialadmin

import (
	"context"
	"log/slog"
	"sync"
)

// ---------------------------------------------------------------------------
// capturingHandlerCross — captures slog records for assertion in cross-platform
// tests.
// ---------------------------------------------------------------------------

type logRecordCross struct {
	level   slog.Level
	message string
	attrs   map[string]string
}

type capturingHandlerCross struct {
	mu      sync.Mutex
	records []logRecordCross
}

func (h *capturingHandlerCross) Enabled(_ context.Context, _ slog.Level) bool { return true }

func (h *capturingHandlerCross) Handle(_ context.Context, r slog.Record) error {
	rec := logRecordCross{
		level:   r.Level,
		message: r.Message,
		attrs:   make(map[string]string),
	}
	r.Attrs(func(a slog.Attr) bool {
		rec.attrs[a.Key] = a.Value.String()
		return true
	})
	h.mu.Lock()
	h.records = append(h.records, rec)
	h.mu.Unlock()
	return nil
}

func (h *capturingHandlerCross) WithAttrs(_ []slog.Attr) slog.Handler { return h }
func (h *capturingHandlerCross) WithGroup(_ string) slog.Handler      { return h }
