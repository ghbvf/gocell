package wrapper_test

import (
	"context"
	"sync"
	"testing"

	"github.com/ghbvf/gocell/kernel/wrapper"
)

// spySpan / spyTracer are the shared span-recording fakes used by consumer
// + correlation + tracer tests. Thread-safe so concurrent-handler tests can
// assert on them under `go test -race`.

type spySpan struct {
	mu     sync.Mutex
	name   string
	attrs  []wrapper.Attr
	errs   []error
	status wrapper.StatusCode
	stDesc string
	ended  bool
}

func (s *spySpan) SetAttributes(attrs ...wrapper.Attr) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.attrs = append(s.attrs, attrs...)
}

func (s *spySpan) RecordError(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.errs = append(s.errs, err)
}

func (s *spySpan) SetStatus(code wrapper.StatusCode, desc string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.status = code
	s.stDesc = desc
}

func (s *spySpan) SetName(name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.name = name
}

func (s *spySpan) End() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ended = true
}

func (s *spySpan) attrMap() map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[string]any, len(s.attrs))
	for _, a := range s.attrs {
		out[a.Key] = a.Value
	}
	return out
}

type spyTracer struct {
	mu    sync.Mutex
	spans []*spySpan
}

func (t *spyTracer) Start(ctx context.Context, name string, attrs ...wrapper.Attr) (context.Context, wrapper.Span) {
	s := &spySpan{name: name}
	if len(attrs) > 0 {
		s.attrs = append(s.attrs, attrs...)
	}
	t.mu.Lock()
	t.spans = append(t.spans, s)
	t.mu.Unlock()
	return ctx, s
}

func (t *spyTracer) only(tb testing.TB) *spySpan {
	tb.Helper()
	t.mu.Lock()
	defer t.mu.Unlock()
	if len(t.spans) != 1 {
		tb.Fatalf("expected exactly 1 span, got %d", len(t.spans))
	}
	return t.spans[0]
}
