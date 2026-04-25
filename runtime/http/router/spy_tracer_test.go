package router

import (
	"context"
	"sync"
	"testing"

	"github.com/ghbvf/gocell/kernel/wrapper"
)

type routerSpySpan struct {
	mu     sync.Mutex
	attrs  map[string]any
	status wrapper.StatusCode
	name   string
	errs   []string
}

func (s *routerSpySpan) SetAttributes(attrs ...wrapper.Attr) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.attrs == nil {
		s.attrs = make(map[string]any)
	}
	for _, a := range attrs {
		s.attrs[a.Key] = a.Value
	}
}

func (s *routerSpySpan) RecordError(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.errs = append(s.errs, err.Error())
}

func (s *routerSpySpan) SetStatus(code wrapper.StatusCode, _ string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.status = code
}

func (s *routerSpySpan) End() {}

func (s *routerSpySpan) SetName(name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.name = name
}

func (s *routerSpySpan) Attr(key string) any {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.attrs[key]
}

type routerSpyTracer struct {
	mu    sync.Mutex
	spans []*routerSpySpan
}

func (t *routerSpyTracer) Start(ctx context.Context, name string, attrs ...wrapper.Attr) (context.Context, wrapper.Span) {
	s := &routerSpySpan{name: name, attrs: make(map[string]any)}
	s.SetAttributes(attrs...)
	t.mu.Lock()
	t.spans = append(t.spans, s)
	t.mu.Unlock()
	return ctx, s
}

func (t *routerSpyTracer) only(tb testing.TB) *routerSpySpan {
	tb.Helper()
	t.mu.Lock()
	defer t.mu.Unlock()
	if len(t.spans) != 1 {
		tb.Fatalf("expected 1 span, got %d", len(t.spans))
	}
	return t.spans[0]
}
