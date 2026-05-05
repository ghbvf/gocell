package main

import (
	"context"
	"log/slog"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

// captureHandler records every slog event passed to it so tests can assert
// on the verbose breakdown that K#08 5xx redaction keeps off the wire.
//
// NOTE: simplified implementation — WithAttrs/WithGroup do not propagate
// pre-filled attrs / groups (return self). Adequate for tests that only
// need to capture top-level slog records; not safe for code paths that
// rely on slog.With(...) propagation.
type captureHandler struct {
	mu      sync.Mutex
	records []slog.Record
}

func (h *captureHandler) Enabled(context.Context, slog.Level) bool { return true }

func (h *captureHandler) Handle(_ context.Context, r slog.Record) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.records = append(h.records, r.Clone())
	return nil
}

func (h *captureHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *captureHandler) WithGroup(string) slog.Handler      { return h }

func (h *captureHandler) snapshot() []slog.Record {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]slog.Record, len(h.records))
	copy(out, h.records)
	return out
}

// withSlogCapture redirects slog.Default for the duration of the test and
// returns a handle the test can query to assert on captured events.
func withSlogCapture(t *testing.T) *captureHandler {
	t.Helper()
	h := &captureHandler{}
	prev := slog.Default()
	slog.SetDefault(slog.New(h))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return h
}

// readyzUnhealthyDeps fetches the verbose-breakdown dependencies map from
// the captured "readyz unhealthy" slog records. Tests assert on this rather
// than on the 503 wire body because K#08 5xx redaction strips Details from
// the public envelope; verbose breakdown lives only in server-side logs.
//
// Returns the first record whose dependencies attr is non-nil — non-verbose
// readyz polls also emit "readyz unhealthy" but without dependencies.
func readyzUnhealthyDeps(t *testing.T, capture *captureHandler) map[string]map[string]any {
	t.Helper()
	const (
		recMsg  = "readyz unhealthy"
		attrKey = "dependencies"
	)
	for _, r := range capture.snapshot() {
		if r.Message != recMsg {
			continue
		}
		var depsAttr slog.Value
		r.Attrs(func(a slog.Attr) bool {
			if a.Key == attrKey {
				depsAttr = a.Value
				return false
			}
			return true
		})
		deps, ok := depsAttr.Any().(map[string]map[string]any)
		if ok && deps != nil {
			return deps
		}
	}
	require.FailNowf(t, "no verbose readyz unhealthy record",
		"no %q slog record with non-nil %q map; capture had %d records",
		recMsg, attrKey, len(capture.snapshot()))
	return nil
}
