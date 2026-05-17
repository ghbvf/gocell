// Package healthtest provides shared test helpers for packages that need to
// assert on the slog channel-d verbose breakdown emitted by
// runtime/http/health.Handler.
//
// Rationale: the same CaptureHandler + ReadyzUnhealthyDeps pattern is needed
// by runtime/bootstrap and cmd/corebundle tests; extracting them here
// avoids copy-paste drift across packages.
//
// Note: runtime/http/health tests themselves (package health, white-box)
// cannot import this package — that would create an import cycle. Those tests
// keep local unexported equivalents.
package healthtest

import (
	"context"
	"log/slog"
	"sync"
	"testing"

	"github.com/ghbvf/gocell/runtime/http/health"
)

// CaptureHandler records every slog event passed to it so tests can assert
// on the verbose breakdown that K#08 5xx redaction keeps off the wire.
//
// NOTE: simplified implementation — WithAttrs/WithGroup do not propagate
// pre-filled attrs / groups (return self). Adequate for tests that only
// need to capture top-level slog events; not safe for code paths that
// rely on slog.With(...) propagation.
type CaptureHandler struct {
	mu      sync.Mutex
	records []slog.Record
}

// Enabled implements slog.Handler.
func (h *CaptureHandler) Enabled(context.Context, slog.Level) bool { return true }

// Handle implements slog.Handler.
func (h *CaptureHandler) Handle(_ context.Context, r slog.Record) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.records = append(h.records, r.Clone())
	return nil
}

// WithAttrs implements slog.Handler (returns self — attrs not propagated).
func (h *CaptureHandler) WithAttrs([]slog.Attr) slog.Handler { return h }

// WithGroup implements slog.Handler (returns self — group not propagated).
func (h *CaptureHandler) WithGroup(string) slog.Handler { return h }

// Snapshot returns a defensive copy of recorded events.
func (h *CaptureHandler) Snapshot() []slog.Record {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]slog.Record, len(h.records))
	copy(out, h.records)
	return out
}

// NewCapture redirects slog.Default for the duration of the test and
// returns a *CaptureHandler the test can query to assert on captured events.
func NewCapture(t *testing.T) *CaptureHandler {
	t.Helper()
	h := &CaptureHandler{}
	prev := slog.Default()
	slog.SetDefault(slog.New(h))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return h
}

// ReadyzUnhealthyDeps fetches the verbose-breakdown dependencies map from
// the captured "readyz unhealthy" slog records. Tests assert on this rather
// than on the 503 wire body because:
//   - K#08 5xx redaction strips Details from the public envelope.
//   - PR391-HEALTH-VERBOSE-REDACTION-01 / ADR 202605171200 forbid error text
//     on wire entirely; full (redacted) error lives only in slog channel d.
//
// Bootstrap integration tests typically poll /readyz (non-verbose) before
// issuing a single verbose request, so multiple "readyz unhealthy" records
// accumulate. Non-verbose records carry only status/reason; verbose records
// add cells/dependencies/adapters. We return the first record whose
// dependencies attr is a non-empty slog.Group — that is the verbose 503.
//
// Return type is map[string]health.SlogDependencyEntry. The slog payload
// shape is slog.Group("dependencies", slog.Any(name, entry)...) per
// (*readyzResult).logDiagnostics — handlers call entry.LogValue() during
// resolve, but inside the Group each sub-attr's raw Value (.Any()) still
// holds the original SlogDependencyEntry. This helper unwraps that.
func ReadyzUnhealthyDeps(t *testing.T, capture *CaptureHandler) map[string]health.SlogDependencyEntry {
	t.Helper()
	const (
		recMsg  = "readyz unhealthy"
		attrKey = "dependencies"
	)
	for _, r := range capture.Snapshot() {
		if r.Message != recMsg {
			continue
		}
		deps := unwrapDependenciesGroup(r, attrKey)
		if len(deps) > 0 {
			return deps
		}
	}
	t.Fatalf("no verbose %q slog record with a non-empty %q Group; capture had %d records",
		recMsg, attrKey, len(capture.Snapshot()))
	return nil
}

// HasReadyzDependencyStatus reports whether any captured "readyz unhealthy"
// verbose slog record contains a dependency entry matching both depName and
// status. Returns false when no matching record exists (not a test failure —
// callers decide how to handle the boolean).
func HasReadyzDependencyStatus(capture *CaptureHandler, depName, status string) bool {
	const (
		recMsg  = "readyz unhealthy"
		attrKey = "dependencies"
	)
	for _, r := range capture.Snapshot() {
		if r.Message != recMsg {
			continue
		}
		deps := unwrapDependenciesGroup(r, attrKey)
		if entry, ok := deps[depName]; ok && entry.Status() == status {
			return true
		}
	}
	return false
}

// unwrapDependenciesGroup walks the "dependencies" slog.Group inside a
// "readyz unhealthy" record and returns a typed map keyed by dep name.
// Returns nil when the record has no dependencies attr or the attr is not a
// Group (e.g. non-verbose record).
func unwrapDependenciesGroup(r slog.Record, attrKey string) map[string]health.SlogDependencyEntry {
	var depsAttr slog.Value
	r.Attrs(func(a slog.Attr) bool {
		if a.Key == attrKey {
			depsAttr = a.Value
			return false
		}
		return true
	})
	if depsAttr.Kind() != slog.KindGroup {
		return nil
	}
	out := make(map[string]health.SlogDependencyEntry, len(depsAttr.Group()))
	for _, sub := range depsAttr.Group() {
		entry, ok := sub.Value.Any().(health.SlogDependencyEntry)
		if !ok {
			continue
		}
		out[sub.Key] = entry
	}
	return out
}
