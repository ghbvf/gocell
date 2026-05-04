package refresh

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/pkg/testutil/testtime"
)

type staticClock struct {
	now time.Time
}

func (c staticClock) Now() time.Time                  { return c.now }
func (c staticClock) Since(t time.Time) time.Duration { return c.now.Sub(t) }
func (c staticClock) Until(t time.Time) time.Duration { return t.Sub(c.now) }
func (c staticClock) NewTimerAt(_ time.Time) clock.Timer {
	panic("staticClock.NewTimerAt not implemented")
}

func (c staticClock) NewTicker(d time.Duration) clock.Ticker {
	return clock.Real().NewTicker(d)
}

func (c staticClock) AfterFunc(_ time.Time, _ func()) clock.Timer {
	panic("staticClock.AfterFunc not implemented")
}

func (c staticClock) Sleep(_ context.Context, _ time.Time) error {
	panic("staticClock.Sleep not implemented")
}

type gcStoreSpy struct {
	mu      sync.Mutex
	calls   []time.Time
	removed int
	err     error
	called  chan struct{}
}

func newGCStoreSpy(removed int, err error) *gcStoreSpy {
	return &gcStoreSpy{removed: removed, err: err, called: make(chan struct{}, 8)}
}

func (s *gcStoreSpy) Issue(context.Context, string, string) (string, *Token, error) {
	return "", nil, errors.New("not implemented")
}

func (s *gcStoreSpy) Peek(context.Context, string) (*Token, error) {
	return nil, errors.New("not implemented")
}

func (s *gcStoreSpy) Rotate(context.Context, string) (string, *Token, error) {
	return "", nil, errors.New("not implemented")
}

func (s *gcStoreSpy) RevokeSession(context.Context, string) error {
	return errors.New("not implemented")
}

func (s *gcStoreSpy) RevokeUser(context.Context, string) error {
	return errors.New("not implemented")
}

func (s *gcStoreSpy) GC(_ context.Context, olderThan time.Time) (int, error) {
	s.mu.Lock()
	s.calls = append(s.calls, olderThan)
	s.mu.Unlock()
	select {
	case s.called <- struct{}{}:
	default:
	}
	return s.removed, s.err
}

func (s *gcStoreSpy) snapshotCalls() []time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]time.Time(nil), s.calls...)
}

type gcObservation struct {
	result  string
	removed int
}

type gcCollectorSpy struct {
	mu    sync.Mutex
	calls []gcObservation
}

func (c *gcCollectorSpy) ObserveRefreshGC(_ context.Context, result string, removed int, _ time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls = append(c.calls, gcObservation{result: result, removed: removed})
}

func (c *gcCollectorSpy) snapshotCalls() []gcObservation {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]gcObservation(nil), c.calls...)
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestNewGCWorker_ValidatesConfigAndDefaults(t *testing.T) {
	now := time.Date(2026, 4, 25, 8, 0, 0, 0, time.UTC)
	store := newGCStoreSpy(0, nil)
	valid := GCWorkerConfig{
		Store:     store,
		Clock:     staticClock{now: now},
		Interval:  time.Second,
		Retention: time.Minute,
	}

	tests := []struct {
		name string
		cfg  GCWorkerConfig
	}{
		{name: "missing store", cfg: GCWorkerConfig{Clock: valid.Clock, Interval: valid.Interval, Retention: valid.Retention}},
		{name: "non-positive interval", cfg: GCWorkerConfig{Store: store, Clock: valid.Clock, Interval: 0, Retention: valid.Retention}},
		{name: "non-positive retention", cfg: GCWorkerConfig{Store: store, Clock: valid.Clock, Interval: valid.Interval, Retention: 0}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			worker, err := NewGCWorker(tc.cfg)
			require.Error(t, err)
			assert.Nil(t, worker)
		})
	}
	// nil clock panics at construction (fail-fast via MustHaveClock).
	t.Run("missing clock panics", func(t *testing.T) {
		assert.Panics(t, func() {
			_, _ = NewGCWorker(GCWorkerConfig{Store: store, Interval: valid.Interval, Retention: valid.Retention})
		}, "nil clock must panic at construction via MustHaveClock")
	})

	worker, err := NewGCWorker(valid)
	require.NoError(t, err)
	require.NotNil(t, worker)
	assert.Same(t, store, worker.store)
	assert.NotNil(t, worker.logger)
	assert.IsType(t, NoopGCCollector{}, worker.metrics)
}

func TestGCWorker_StartStopRunsImmediateGC(t *testing.T) {
	now := time.Date(2026, 4, 25, 8, 30, 0, 0, time.UTC)
	retention := testtime.D30min
	store := newGCStoreSpy(3, nil)
	collector := &gcCollectorSpy{}
	worker, err := NewGCWorker(GCWorkerConfig{
		Store:     store,
		Clock:     staticClock{now: now},
		Interval:  time.Hour,
		Retention: retention,
		Logger:    discardLogger(),
		Metrics:   collector,
	})
	require.NoError(t, err)

	require.NoError(t, worker.Start(context.Background()))
	select {
	case <-store.called:
	case <-time.After(time.Second):
		t.Fatal("GC worker did not run its immediate cleanup")
	}
	require.NoError(t, worker.Start(context.Background()), "starting twice is idempotent")
	require.NoError(t, worker.Stop(context.Background()))
	require.NoError(t, worker.Stop(context.Background()), "stopping an idle worker is idempotent")

	calls := store.snapshotCalls()
	require.NotEmpty(t, calls)
	assert.Equal(t, now.Add(-retention), calls[0])

	observations := collector.snapshotCalls()
	require.NotEmpty(t, observations)
	assert.Equal(t, gcObservation{result: "success", removed: 3}, observations[0])
}

func TestGCWorker_RunOnceRecordsFailure(t *testing.T) {
	now := time.Date(2026, 4, 25, 9, 0, 0, 0, time.UTC)
	store := newGCStoreSpy(0, errors.New("storage unavailable"))
	collector := &gcCollectorSpy{}
	worker, err := NewGCWorker(GCWorkerConfig{
		Store:     store,
		Clock:     staticClock{now: now},
		Interval:  time.Hour,
		Retention: time.Minute,
		Logger:    discardLogger(),
		Metrics:   collector,
	})
	require.NoError(t, err)

	worker.runOnce(context.Background())

	observations := collector.snapshotCalls()
	require.Len(t, observations, 1)
	assert.Equal(t, gcObservation{result: "failure", removed: 0}, observations[0])
}
