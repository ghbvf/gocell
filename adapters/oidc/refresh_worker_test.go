package oidc

import (
	"context"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/kernel/clock/clockmock"
	"github.com/ghbvf/gocell/pkg/testutil/sloghelper"
	"github.com/ghbvf/gocell/pkg/testutil/testtime"
)

// testExplicitRefreshInterval is a site-specific deadline for the
// "explicit interval" subtest (TEST-TIME-LITERAL-01: package-level const).
const testExplicitRefreshInterval = 6 * time.Hour

// fakeRefreshCollector is an in-memory RefreshCollector that counts success
// and failure calls. Safe for concurrent use via atomic counters.
type fakeRefreshCollector struct {
	successCount atomic.Int64
	failureCount atomic.Int64
}

func (c *fakeRefreshCollector) RecordRefresh(success bool) {
	if success {
		c.successCount.Add(1)
	} else {
		c.failureCount.Add(1)
	}
}

// newTestAdapter creates a fresh *Adapter backed by the given OIDC server and
// clockmock, with the fakeRefreshCollector injected. The returned adapter is
// ready to have Worker().Start(ctx) called.
func newTestAdapter(t *testing.T, srvURL string, clk *clockmock.FakeClock, col *fakeRefreshCollector) *Adapter {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), testtime.CtxDefault)
	defer cancel()
	a, err := New(ctx, Config{
		IssuerURL:        srvURL,
		ClientID:         "test-client",
		Clock:            clk,
		RefreshCollector: col,
	})
	require.NoError(t, err)
	return a
}

// TestRefreshWorker_HappyPath verifies that after clk.Advance(refreshInterval)
// the worker fires Refresh, updates the provider, and records success in the
// collector.
func TestRefreshWorker_HappyPath(t *testing.T) {
	srv := mockOIDCServer(t)
	defer srv.Close()

	clk := clockmock.New(testEpoch)
	col := &fakeRefreshCollector{}
	a := newTestAdapter(t, srv.URL, clk, col)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	workerDoneCh := make(chan error, 1)
	go func() { workerDoneCh <- a.Worker().Start(ctx) }()

	// Wait until the goroutine has registered the ticker before advancing.
	require.Eventually(t, func() bool {
		return clk.PendingTickers() >= 1
	}, testtime.EventuallyShort, testtime.FastPoll, "ticker must be registered before Advance")

	// Advance past one interval — triggers Refresh.
	clk.Advance(a.refreshInterval() + time.Millisecond)

	// Wait for the success to be recorded.
	require.Eventually(t, func() bool {
		return col.successCount.Load() >= 1
	}, testtime.EventuallyShort, testtime.FastPoll, "expected RecordRefresh(true) after tick")

	assert.Equal(t, int64(0), col.failureCount.Load())
	assert.Equal(t, int64(0), a.consecutiveFailures.Load())

	// Provider should still be non-nil (was updated by Refresh).
	p, err := a.Provider(context.Background())
	require.NoError(t, err)
	require.NotNil(t, p)

	cancel()
	select {
	case <-time.After(testtime.SelectShutdown):
		t.Fatal("worker did not exit after ctx cancel")
	case <-workerDoneCh:
	}
}

// TestRefreshWorker_CtxCancel verifies the worker exits cleanly when the
// context is canceled and does not leak goroutines.
func TestRefreshWorker_CtxCancel(t *testing.T) {
	srv := mockOIDCServer(t)
	defer srv.Close()

	clk := clockmock.New(testEpoch)
	col := &fakeRefreshCollector{}
	a := newTestAdapter(t, srv.URL, clk, col)

	ctx, cancel := context.WithCancel(context.Background())

	doneCh := make(chan error, 1)
	go func() { doneCh <- a.Worker().Start(ctx) }()

	cancel()

	select {
	case <-time.After(testtime.SelectShutdown):
		t.Fatal("worker did not exit after ctx cancel")
	case err := <-doneCh:
		// Start returns nil on clean ctx-cancel exit.
		assert.NoError(t, err)
	}
}

// TestRefreshWorker_StopIdempotent verifies that calling Stop multiple times
// and calling Stop before Start does not deadlock or panic.
func TestRefreshWorker_StopIdempotent(t *testing.T) {
	srv := mockOIDCServer(t)
	defer srv.Close()

	clk := clockmock.New(testEpoch)
	col := &fakeRefreshCollector{}
	a := newTestAdapter(t, srv.URL, clk, col)

	w := a.Worker()
	stopCtx, stopCancel := context.WithTimeout(context.Background(), testtime.CtxDefault)
	defer stopCancel()

	// Stop without ever calling Start — must not deadlock (never-started fast-path).
	require.NoError(t, w.Stop(stopCtx), "Stop before Start must not error")
	// Second Stop must also not deadlock or error.
	require.NoError(t, w.Stop(stopCtx), "second Stop must be idempotent")
}

// TestRefreshWorker_CloseIdempotent verifies that Close is safe to call
// multiple times even when the worker was never started.
func TestRefreshWorker_CloseIdempotent(t *testing.T) {
	srv := mockOIDCServer(t)
	defer srv.Close()

	clk := clockmock.New(testEpoch)
	col := &fakeRefreshCollector{}
	a := newTestAdapter(t, srv.URL, clk, col)

	closeCtx, closeCancel := context.WithTimeout(context.Background(), testtime.CtxDefault)
	defer closeCancel()

	require.NoError(t, a.Close(closeCtx))
	require.NoError(t, a.Close(closeCtx), "second Close must be idempotent")
}

// TestRefreshWorker_FailOpen verifies that when the IdP is unreachable:
//   - the worker emits RecordRefresh(false)
//   - consecutiveFailures increments
//   - slog.Warn is emitted with the expected fields
//   - Provider() still returns the old (pre-failure) provider instance
func TestRefreshWorker_FailOpen(t *testing.T) {
	srv, failFlag := mockOIDCServerTogglable(t)
	defer srv.Close()

	clk := clockmock.New(testEpoch)
	col := &fakeRefreshCollector{}
	a := newTestAdapter(t, srv.URL, clk, col)

	// Capture the old provider before making IdP unreachable.
	oldProvider, err := a.Provider(context.Background())
	require.NoError(t, err)
	require.NotNil(t, oldProvider)

	// Wire a slog JSON handler so we can assert log fields.
	logBuf := sloghelper.NewSyncBuffer()
	handler := slog.NewJSONHandler(logBuf, &slog.HandlerOptions{Level: slog.LevelDebug})
	oldDefault := slog.Default()
	slog.SetDefault(slog.New(handler))
	defer slog.SetDefault(oldDefault)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	doneCh := make(chan error, 1)
	go func() { doneCh <- a.Worker().Start(ctx) }()

	// Wait until the goroutine has registered the ticker.
	require.Eventually(t, func() bool {
		return clk.PendingTickers() >= 1
	}, testtime.EventuallyShort, testtime.FastPoll, "ticker must be registered before Advance")

	// Make the IdP unreachable before the first tick.
	failFlag.Store(1)

	// Advance past one interval — triggers Refresh which will fail.
	clk.Advance(a.refreshInterval() + time.Millisecond)

	// Wait for a failure to be recorded.
	require.Eventually(t, func() bool {
		return col.failureCount.Load() >= 1
	}, testtime.EventuallyShort, testtime.FastPoll, "expected RecordRefresh(false) after IdP failure")

	assert.Equal(t, int64(0), col.successCount.Load())
	assert.Greater(t, a.consecutiveFailures.Load(), int64(0))

	// Provider() must still return the old instance (fail-open).
	currentProvider, err := a.Provider(context.Background())
	require.NoError(t, err)
	assert.Same(t, oldProvider, currentProvider, "fail-open: old provider must be preserved on Refresh error")

	// Verify slog.Warn was emitted.
	logOutput := logBuf.String()
	assert.Contains(t, logOutput, "oidc: jwks refresh failed", "expected warn log on refresh failure")

	cancel()
	<-doneCh
}

// TestRefreshWorker_FailureThenSuccess verifies that after a failure followed
// by a success:
//   - consecutiveFailures resets to 0
//   - slog.Info recovery log is emitted
//   - RecordRefresh(true) is called
func TestRefreshWorker_FailureThenSuccess(t *testing.T) {
	srv, failFlag := mockOIDCServerTogglable(t)
	defer srv.Close()

	clk := clockmock.New(testEpoch)
	col := &fakeRefreshCollector{}
	a := newTestAdapter(t, srv.URL, clk, col)

	// Capture logs.
	logBuf := sloghelper.NewSyncBuffer()
	handler := slog.NewJSONHandler(logBuf, &slog.HandlerOptions{Level: slog.LevelDebug})
	oldDefault := slog.Default()
	slog.SetDefault(slog.New(handler))
	defer slog.SetDefault(oldDefault)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	doneCh := make(chan error, 1)
	go func() { doneCh <- a.Worker().Start(ctx) }()

	// Wait until the goroutine has registered the ticker.
	require.Eventually(t, func() bool {
		return clk.PendingTickers() >= 1
	}, testtime.EventuallyShort, testtime.FastPoll, "ticker must be registered before Advance")

	interval := a.refreshInterval()

	// First tick — IdP healthy → success.
	clk.Advance(interval + time.Millisecond)
	require.Eventually(t, func() bool {
		return col.successCount.Load() >= 1
	}, testtime.EventuallyShort, testtime.FastPoll, "expected success on first tick")

	// Make IdP fail.
	failFlag.Store(1)
	clk.Advance(interval)
	require.Eventually(t, func() bool {
		return col.failureCount.Load() >= 1
	}, testtime.EventuallyShort, testtime.FastPoll, "expected failure on second tick")

	assert.Greater(t, a.consecutiveFailures.Load(), int64(0), "consecutiveFailures must be > 0 after failure")

	// Restore IdP.
	failFlag.Store(0)
	clk.Advance(interval)
	require.Eventually(t, func() bool {
		return col.successCount.Load() >= 2
	}, testtime.EventuallyShort, testtime.FastPoll, "expected success on recovery tick")

	assert.Equal(t, int64(0), a.consecutiveFailures.Load(), "consecutiveFailures must reset to 0 after recovery")

	// Verify recovery log.
	logOutput := logBuf.String()
	assert.Contains(t, logOutput, "oidc: jwks refresh recovered", "expected info recovery log")

	cancel()
	<-doneCh
}

// TestRefreshWorker_RefreshInterval_DefaultAndExplicit verifies the
// refreshInterval() helper returns the correct value for both zero (default)
// and non-zero (explicit) Config.RefreshInterval.
func TestRefreshWorker_RefreshInterval_DefaultAndExplicit(t *testing.T) {
	srv := mockOIDCServer(t)
	defer srv.Close()

	clk := clockmock.New(testEpoch)

	t.Run("default interval", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), testtime.CtxDefault)
		defer cancel()
		a, err := New(ctx, Config{
			IssuerURL: srv.URL,
			ClientID:  "test-client",
			Clock:     clk,
		})
		require.NoError(t, err)
		assert.Equal(t, defaultOIDCRefreshInterval, a.refreshInterval())
	})

	t.Run("explicit interval", func(t *testing.T) {
		explicit := testExplicitRefreshInterval
		ctx, cancel := context.WithTimeout(context.Background(), testtime.CtxDefault)
		defer cancel()
		a, err := New(ctx, Config{
			IssuerURL:       srv.URL,
			ClientID:        "test-client",
			Clock:           clk,
			RefreshInterval: explicit,
		})
		require.NoError(t, err)
		assert.Equal(t, explicit, a.refreshInterval())
	})
}

// TestRefreshWorker_WarnLogFields verifies the slog.Warn call on failure
// includes the required structured fields: issuer, error, consecutive_failures.
func TestRefreshWorker_WarnLogFields(t *testing.T) {
	srv, failFlag := mockOIDCServerTogglable(t)
	defer srv.Close()

	clk := clockmock.New(testEpoch)
	col := &fakeRefreshCollector{}
	a := newTestAdapter(t, srv.URL, clk, col)

	logBuf := sloghelper.NewSyncBuffer()
	handler := slog.NewJSONHandler(logBuf, &slog.HandlerOptions{Level: slog.LevelDebug})
	oldDefault := slog.Default()
	slog.SetDefault(slog.New(handler))
	defer slog.SetDefault(oldDefault)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	doneCh := make(chan error, 1)
	go func() { doneCh <- a.Worker().Start(ctx) }()

	// Wait until the goroutine has registered the ticker.
	require.Eventually(t, func() bool {
		return clk.PendingTickers() >= 1
	}, testtime.EventuallyShort, testtime.FastPoll, "ticker must be registered before Advance")

	failFlag.Store(1)
	clk.Advance(a.refreshInterval() + time.Millisecond)
	require.Eventually(t, func() bool {
		return col.failureCount.Load() >= 1
	}, testtime.EventuallyShort, testtime.FastPoll)

	cancel()
	<-doneCh

	warnEntry := sloghelper.FindLogEntry(logBuf.String(), "oidc: jwks refresh failed")
	require.NotNil(t, warnEntry, "warn log entry must be present")
	assert.Equal(t, "WARN", warnEntry["level"])
	// F-4: assert exact issuer value and type-assert numeric consecutive_failures.
	assert.Equal(t, srv.URL, warnEntry["issuer"])
	assert.NotNil(t, warnEntry["error"], "log must include error field")
	assert.Greater(t, warnEntry["consecutive_failures"].(float64), float64(0))
}

// TestRefreshWorker_Stop_DrainAfterStarted verifies that Stop called after
// Start waits for the worker goroutine to exit and returns nil.
func TestRefreshWorker_Stop_DrainAfterStarted(t *testing.T) {
	srv := mockOIDCServer(t)
	defer srv.Close()

	clk := clockmock.New(testEpoch)
	col := &fakeRefreshCollector{}
	a := newTestAdapter(t, srv.URL, clk, col)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() { _ = a.Worker().Start(ctx) }()

	// Wait until the goroutine has registered the ticker before calling Stop.
	require.Eventually(t, func() bool {
		return clk.PendingTickers() >= 1
	}, testtime.EventuallyShort, testtime.FastPoll, "ticker must be registered before Stop")

	stopCtx, stopCancel := context.WithTimeout(context.Background(), testtime.CtxDefault)
	defer stopCancel()

	require.NoError(t, a.Worker().Stop(stopCtx))

	select {
	case <-a.workerDone:
		// worker exited cleanly
	case <-time.After(testtime.SelectShutdown):
		t.Fatal("workerDone not closed after Stop returned")
	}
}

// TestRefreshWorker_Close_DrainAfterStarted verifies that Close called after
// Start waits for the worker goroutine to exit and returns nil.
func TestRefreshWorker_Close_DrainAfterStarted(t *testing.T) {
	srv := mockOIDCServer(t)
	defer srv.Close()

	clk := clockmock.New(testEpoch)
	col := &fakeRefreshCollector{}
	a := newTestAdapter(t, srv.URL, clk, col)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() { _ = a.Worker().Start(ctx) }()

	// Wait until the goroutine has registered the ticker before calling Close.
	require.Eventually(t, func() bool {
		return clk.PendingTickers() >= 1
	}, testtime.EventuallyShort, testtime.FastPoll, "ticker must be registered before Close")

	closeCtx, closeCancel := context.WithTimeout(context.Background(), testtime.CtxDefault)
	defer closeCancel()

	require.NoError(t, a.Close(closeCtx))

	select {
	case <-a.workerDone:
		// worker exited cleanly
	case <-time.After(testtime.SelectShutdown):
		t.Fatal("workerDone not closed after Close returned")
	}
}
