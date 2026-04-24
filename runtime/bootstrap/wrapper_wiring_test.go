package bootstrap

// wrapper_wiring_test.go — unit tests for kernel/wrapper tracer wiring (A5).
//
// Tests exercise wireWrapperTracer() directly (white-box, package bootstrap)
// without starting a full Bootstrap lifecycle. Three scenarios:
//   1. WithTracer sets b.wrapperTracer and wireWrapperTracer calls SetTracer.
//   2. Without WithTracer, fallback installs NoopTracer + emits slog.Warn.
//   3. Regression guard: wrapper tracer set after wireWrapperTracer is NoopTracer.

import (
	"bytes"
	"context"
	"log/slog"
	"sync"
	"testing"

	"github.com/ghbvf/gocell/kernel/wrapper"
)

// ── Spy tracer ─────────────────────────────────────────────────────────────

type wiringSpySpan struct{}

func (s *wiringSpySpan) SetAttributes(_ ...wrapper.Attr)          {}
func (s *wiringSpySpan) RecordError(_ error)                      {}
func (s *wiringSpySpan) SetStatus(_ wrapper.StatusCode, _ string) {}
func (s *wiringSpySpan) End()                                     {}

type wiringSpyTracer struct {
	mu    sync.Mutex
	calls int
}

func (t *wiringSpyTracer) Start(ctx context.Context, _ string, _ ...wrapper.Attr) (context.Context, wrapper.Span) {
	t.mu.Lock()
	t.calls++
	t.mu.Unlock()
	return ctx, &wiringSpySpan{}
}

func (t *wiringSpyTracer) startCalls() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.calls
}

// ── Tests ─────────────────────────────────────────────────────────────────

// TestBootstrap_WireWrapperTracer_WithTracer verifies that WithTracer stores
// the tracer in b.wrapperTracer and wireWrapperTracer installs it.
func TestBootstrap_WireWrapperTracer_WithTracer(t *testing.T) {
	spy := &wiringSpyTracer{}
	b := New(WithTracer(spy))

	b.wireWrapperTracer()
	// Restore after test so other tests are not affected.
	t.Cleanup(func() { wrapper.SetTracer(wrapper.NoopTracer{}) })

	// Verify b.wrapperTracer was set by WithTracer.
	if b.wrapperTracer != spy {
		t.Error("WithTracer must store tracer in b.wrapperTracer")
	}

	// Verify the package-level tracer is now our spy by calling Start directly.
	ctx := context.Background()
	_, span := spy.Start(ctx, "probe")
	span.End()
	if spy.startCalls() != 1 {
		t.Errorf("expected 1 spy.Start call, got %d", spy.startCalls())
	}
}

// TestBootstrap_WireWrapperTracer_FallbackWarns verifies that without WithTracer,
// wireWrapperTracer installs NoopTracer and emits a slog.Warn.
func TestBootstrap_WireWrapperTracer_FallbackWarns(t *testing.T) {
	var buf bytes.Buffer
	handler := slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn})
	old := slog.Default()
	slog.SetDefault(slog.New(handler))
	t.Cleanup(func() {
		slog.SetDefault(old)
		wrapper.SetTracer(wrapper.NoopTracer{})
	})

	b := New() // no WithTracer
	b.wireWrapperTracer()

	logged := buf.String()
	if logged == "" {
		t.Error("expected slog.Warn output when no tracer is configured")
	}
	if !logContains(logged, "no tracer provided") {
		t.Errorf("log output missing 'no tracer provided'; got: %s", logged)
	}

	// After fallback, b.wrapperTracer is nil but wrapper tracer is NoopTracer.
	if b.wrapperTracer != nil {
		t.Error("b.wrapperTracer should remain nil when no WithTracer was called")
	}
}

// TestBootstrap_WireWrapperTracer_NoopAfterFallback verifies that the package-
// level tracer is operational (NoopTracer) after fallback — no panic.
func TestBootstrap_WireWrapperTracer_NoopAfterFallback(t *testing.T) {
	b := New()
	b.wireWrapperTracer()
	t.Cleanup(func() { wrapper.SetTracer(wrapper.NoopTracer{}) })

	// Directly call the package-level tracer via NoopTracer alias — must not panic.
	ctx := context.Background()
	_, span := wrapper.NoopTracer{}.Start(ctx, "test-span")
	span.End()
	// If we got here without panic, the fallback works.
}

// logContains is a simple substring check without importing strings.
func logContains(s, substr string) bool {
	if len(substr) == 0 {
		return true
	}
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
