//go:build unix || windows

package initialadmin

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

// ---------------------------------------------------------------------------
// fakeSchedulerCross + fakeTimerCross — deterministic scheduling for
// cross-platform tests.  Kept separate from cleaner_test.go (unix-only) to
// avoid duplicate symbol errors on unix builds.
// ---------------------------------------------------------------------------

type fakeTimerCross struct {
	mu       sync.Mutex
	fn       func()
	canceled bool
	fired    bool
}

func (t *fakeTimerCross) Stop() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.fired || t.canceled {
		return false
	}
	t.canceled = true
	return true
}

func (t *fakeTimerCross) fire() {
	t.mu.Lock()
	if t.canceled || t.fired {
		t.mu.Unlock()
		return
	}
	fn := t.fn
	t.fired = true
	t.mu.Unlock()
	if fn != nil {
		fn()
	}
}

type scheduledEntryCross struct {
	d     time.Duration
	timer *fakeTimerCross
}

type fakeSchedulerCross struct {
	mu       sync.Mutex
	entries  []scheduledEntryCross
	elapsed  time.Duration
	timerCh  chan struct{}
	timerOne sync.Once
}

func newFakeSchedulerCross() *fakeSchedulerCross {
	return &fakeSchedulerCross{timerCh: make(chan struct{})}
}

func (s *fakeSchedulerCross) AfterFunc(d time.Duration, fn func()) Cancellable {
	timer := &fakeTimerCross{fn: fn}
	s.mu.Lock()
	s.entries = append(s.entries, scheduledEntryCross{d: d, timer: timer})
	s.mu.Unlock()
	s.timerOne.Do(func() { close(s.timerCh) })
	return timer
}

func (s *fakeSchedulerCross) Advance(delta time.Duration) {
	s.mu.Lock()
	s.elapsed += delta
	elapsed := s.elapsed
	var toFire []*fakeTimerCross
	for _, e := range s.entries {
		if elapsed >= e.d {
			toFire = append(toFire, e.timer)
		}
	}
	s.mu.Unlock()
	for _, timer := range toFire {
		timer.fire()
	}
}

// ---------------------------------------------------------------------------
// capturingHandlerCross — captures slog records for assertion in cross-platform
// tests.  Separate from cleaner_test.go's capturingHandler (unix-only).
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
