// Package outboxtest provides an in-memory outbox.Emitter for use in cell-level
// unit tests.
//
// Use in tests only — package name follows the "*test" test-infrastructure
// convention; production code must not import this package (enforced by
// tools/archtest/celltest_import_scope_test.go for the cells/*/*test/ naming
// pattern and by tools/archtest/testutil_boundary_test.go for *testutil paths).
package outboxtest

import (
	"context"
	"sync"

	"github.com/ghbvf/gocell/kernel/outbox"
)

// Recorder is an in-memory outbox.Emitter that captures every entry passed to
// Emit for later assertion.
//
// Use Recorder in cell-level unit tests as a drop-in for the production
// transactional / direct emitter when the test cares about *what* was emitted
// rather than *how* it is delivered. It is concurrency-safe — production
// Emitter implementations are invoked from multiple goroutines (transactional
// writer + direct publishers), and tests that exercise those code paths must
// not race on the recorder.
//
// Recorder does NOT implement outbox.DurabilityReporter. Its durability
// semantics are equivalent to outbox.NoopEmitter — entries are captured
// in-memory with no persistence guarantee. Tests that need to validate L2
// outbox durability paths (transactional write + relay confirmation) should use
// outbox.NewWriterEmitter backed by a mem writer instead of Recorder.
//
// Plan 044 task 1 (PR0): Recorder is the canonical producer-side seam for
// `semanticForm: producer-side` journey criteria (plan 044 §2).
//
// ref: ThreeDotsLabs/watermill pubsub/gochannel — in-memory implementation
// used by upstream conformance tests for producer-side assertions.
type Recorder struct {
	mu      sync.Mutex
	entries []outbox.Entry
}

// NewRecorder returns an empty Recorder.
func NewRecorder() *Recorder { return &Recorder{} }

// CellEmitter returns the Recorder sealed as an outbox.CellEmitter so it can be
// passed to a cell/slice public WithEmitter Option (which accepts the sealed
// marker, not a raw Emitter). The Recorder is still usable for assertions via
// the original *Recorder handle — wrapping does not consume it. This is the
// test-emitter analog of outbox.DemoCellEmitter for the asserting case; the
// WrapEmitterForCell call is restricted to this file by archtest
// CELL-RAW-INFRA-WRAPPER-LOCATION-01 (outboxtest is test-only infrastructure,
// importable from production code is forbidden — see package doc).
func (r *Recorder) CellEmitter() outbox.CellEmitter {
	return outbox.WrapEmitterForCell(r)
}

// Emit captures entry. Always returns nil — Recorder does not simulate
// emit-time failures; tests that need fail-injection should compose a custom
// outbox.Emitter that wraps Recorder.
//
// Context is accepted but not inspected. Recorder captures entries as-is
// without injecting observability fields from ctx; tests that assert
// ctx-derived metadata (trace_id, span_id) must use a real emitter with a
// test Publisher.
func (r *Recorder) Emit(_ context.Context, entry outbox.Entry) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.entries = append(r.entries, deepCopyEntry(entry))
	return nil
}

// Entries returns a deep copy of captured entries in Emit order; mutating the
// returned slice, including each entry's Payload and Metadata fields, does not
// affect the Recorder.
func (r *Recorder) Entries() []outbox.Entry {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]outbox.Entry, len(r.entries))
	for i, e := range r.entries {
		out[i] = deepCopyEntry(e)
	}
	return out
}

// EntriesByType returns entries whose EventType equals the given value, in
// Emit order. Returns a deep copy; mutating the returned slice, including each
// entry's Payload and Metadata fields, does not affect the Recorder.
func (r *Recorder) EntriesByType(eventType string) []outbox.Entry {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]outbox.Entry, 0, len(r.entries))
	for _, e := range r.entries {
		if e.EventType == eventType {
			out = append(out, deepCopyEntry(e))
		}
	}
	return out
}

// Reset clears all captured entries.
func (r *Recorder) Reset() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.entries = nil
}

// deepCopyEntry returns a copy of e where the Payload and Metadata reference
// fields are independently allocated. nil Payload and nil Metadata are
// preserved as nil.
func deepCopyEntry(e outbox.Entry) outbox.Entry {
	out := e
	if e.Payload != nil {
		out.Payload = make([]byte, len(e.Payload))
		copy(out.Payload, e.Payload)
	}
	if e.Metadata != nil {
		out.Metadata = make(map[string]string, len(e.Metadata))
		for k, v := range e.Metadata {
			out.Metadata[k] = v
		}
	}
	return out
}
