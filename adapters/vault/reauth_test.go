package vault

// reauth_test.go — unit tests for the re-authentication loop behaviour.
//
// Covers:
//   F-3a: TestReauthenticate_BackoffInterruptedByCtxCancel — reauthenticate
//         exits promptly when ctx is cancelled during the backoff sleep.
//   F-3b: TestDoReauth_InfiniteRetry_UntilCtxCancel — doReauth keeps retrying
//         buildWatcher failures indefinitely until ctx is cancelled.

import (
	"context"
	"log/slog"
	"sync"
	"testing"
	"time"

	vaultapi "github.com/hashicorp/vault/api"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/pkg/errcode"
)

// TestReauthenticate_BackoffInterruptedByCtxCancel verifies that reauthenticate
// returns quickly (within 100 ms after cancellation) even when it is sleeping
// through an exponential backoff interval.
//
// Setup: fakeAuthMethod always fails → reauthenticate enters the sleep.
// The test cancels ctx while reauthenticate is sleeping and asserts that the
// function unblocks within 100 ms of cancellation.
func TestReauthenticate_BackoffInterruptedByCtxCancel(t *testing.T) {
	permErr := errcode.New(errcode.ErrVaultAuthFailed, "always fails")
	fakeAuth := &fakeAuthMethod{
		method:       MethodAppRole,
		permanentErr: permErr,
	}

	w := &tokenRenewalWorker{
		authMethod: fakeAuth,
		logger:     slog.Default(),
	}

	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan error, 1)
	go func() {
		done <- w.reauthenticate(ctx)
	}()

	// Wait for at least one Login call so we know the backoff sleep has started.
	require.Eventually(t, func() bool {
		fakeAuth.mu.Lock()
		defer fakeAuth.mu.Unlock()
		return fakeAuth.calls >= 1
	}, 2*time.Second, time.Millisecond, "expected at least 1 Login call before cancel")

	// Cancel ctx during the backoff sleep.
	cancelAt := time.Now()
	cancel()

	select {
	case err := <-done:
		elapsed := time.Since(cancelAt)
		// Must return quickly — not stuck waiting for the full backoff interval.
		if elapsed > 100*time.Millisecond {
			t.Errorf("reauthenticate took %v to exit after ctx cancel (want < 100ms)", elapsed)
		}
		if err == nil {
			t.Error("reauthenticate must return non-nil error on ctx cancel")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("reauthenticate did not return after ctx cancel (backoff not interruptible)")
	}
}

// TestDoReauth_InfiniteRetry_UntilCtxCancel verifies that doReauth retries
// buildWatcher failures indefinitely — it does NOT give up after N attempts.
//
// Setup:
//   - fakeAuthMethod always succeeds (Login is fine).
//   - fakeAlwaysFailRenewer returns an error on every NewLifetimeWatcher call.
//   - doReauth must keep looping (reauthenticate → buildWatcher fails → retry).
//   - After several iterations ctx is cancelled and doReauth returns (nil, false).
func TestDoReauth_InfiniteRetry_UntilCtxCancel(t *testing.T) {
	// fakeAuthMethod always returns success so Login never blocks the loop.
	fakeAuth := &fakeAuthMethod{
		method: MethodAppRole,
		// No errs, no permanentErr → default: returns non-renewable token each call.
	}

	// Renewer that always fails NewLifetimeWatcher.
	watcherErr := errcode.New(errcode.ErrKeyProviderAuthFailed, "watcher build failed")
	var watcherCallsMu sync.Mutex
	var watcherCalls int
	renewer := &alwaysFailWatcherRenewer{
		watcherErr:      watcherErr,
		watcherCallsMu:  &watcherCallsMu,
		watcherCallsPtr: &watcherCalls,
	}

	w := &tokenRenewalWorker{
		client:     renewer,
		authMethod: fakeAuth,
		logger:     slog.Default(),
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan struct{})
	var gotWatcher tokenWatcher
	var gotOK bool
	go func() {
		defer close(done)
		gotWatcher, gotOK = w.doReauth(ctx)
	}()

	// Wait until buildWatcher has been called at least 3 times — proving the loop
	// is retrying beyond the old 2-attempt limit.
	require.Eventually(t, func() bool {
		watcherCallsMu.Lock()
		defer watcherCallsMu.Unlock()
		return watcherCalls >= 3
	}, 10*time.Second, 5*time.Millisecond,
		"expected at least 3 buildWatcher attempts; infinite loop is not retrying")

	// Cancel ctx to exit the loop.
	cancel()

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("doReauth did not return after ctx cancel")
	}

	if gotOK {
		t.Error("doReauth must return ok=false when ctx cancelled")
	}
	if gotWatcher != nil {
		t.Error("doReauth must return nil watcher when ctx cancelled")
	}
}

// TestDoReauth_SucceedsAfterNFailures verifies that doReauth eventually returns
// (newWatcher, true) once buildWatcher succeeds after several failures.
func TestDoReauth_SucceedsAfterNFailures(t *testing.T) {
	const failCount = 3
	fakeAuth := &fakeAuthMethod{
		method: MethodAppRole,
	}

	watcherErr := errcode.New(errcode.ErrKeyProviderAuthFailed, "watcher build failed")
	var callMu sync.Mutex
	var callCount int
	renewer := &nthSuccessWatcherRenewer{
		mu:           &callMu,
		callCountPtr: &callCount,
		failUntil:    failCount,
		failErr:      watcherErr,
	}

	authHealthy := prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace: "gocell",
		Subsystem: "vault",
		Name:      "token_auth_healthy_doauth_nth_test",
		Help:      "Test gauge.",
	})
	authHealthy.Set(0)

	w := &tokenRenewalWorker{
		client:      renewer,
		authMethod:  fakeAuth,
		logger:      slog.Default(),
		authHealthy: authHealthy,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	newWatcher, ok := w.doReauth(ctx)
	if !ok {
		t.Fatal("doReauth must return ok=true when buildWatcher eventually succeeds")
	}
	if newWatcher == nil {
		t.Fatal("doReauth must return non-nil watcher on success")
	}
	// authHealthy must be restored to 1 after success.
	if got := testutil.ToFloat64(authHealthy); got != 1 {
		t.Errorf("authHealthy after doReauth success = %v, want 1", got)
	}
	callMu.Lock()
	totalCalls := callCount
	callMu.Unlock()
	if totalCalls < failCount+1 {
		t.Errorf("expected at least %d buildWatcher calls, got %d", failCount+1, totalCalls)
	}
}

// ---------------------------------------------------------------------------
// Test fakes for doReauth tests
// ---------------------------------------------------------------------------

// alwaysFailWatcherRenewer implements TokenRenewer where NewLifetimeWatcher
// always returns an error. LookupSelfToken always succeeds.
// Used by TestDoReauth_InfiniteRetry_UntilCtxCancel.
type alwaysFailWatcherRenewer struct {
	watcherErr      error
	watcherCallsMu  *sync.Mutex
	watcherCallsPtr *int
}

func (r *alwaysFailWatcherRenewer) LookupSelfToken(_ context.Context) (*vaultapi.Secret, error) {
	return &vaultapi.Secret{}, nil
}

func (r *alwaysFailWatcherRenewer) NewLifetimeWatcher(_ *vaultapi.LifetimeWatcherInput) (*vaultapi.LifetimeWatcher, error) {
	r.watcherCallsMu.Lock()
	*r.watcherCallsPtr++
	r.watcherCallsMu.Unlock()
	return nil, r.watcherErr
}

// nthSuccessWatcherRenewer implements TokenRenewer where NewLifetimeWatcher
// fails for the first failUntil calls, then returns nil (success path in
// buildWatcher — nil from NewLifetimeWatcher triggers the nil-check guard
// which returns ErrKeyProviderAuthFailed). To actually succeed, we return
// a non-nil result after failUntil attempts. We use a zero-value
// *vaultapi.LifetimeWatcher which will cause buildWatcher to create the
// vaultLifetimeWatcherAdapter.
//
// NOTE: since a zero-value *vaultapi.LifetimeWatcher will panic on use, the
// test only verifies the watcher is non-nil and that authHealthy=1; it does
// not call Start on the returned watcher.
type nthSuccessWatcherRenewer struct {
	mu           *sync.Mutex
	callCountPtr *int
	failUntil    int
	failErr      error
}

func (r *nthSuccessWatcherRenewer) LookupSelfToken(_ context.Context) (*vaultapi.Secret, error) {
	return &vaultapi.Secret{}, nil
}

func (r *nthSuccessWatcherRenewer) NewLifetimeWatcher(_ *vaultapi.LifetimeWatcherInput) (*vaultapi.LifetimeWatcher, error) {
	r.mu.Lock()
	n := *r.callCountPtr
	*r.callCountPtr++
	r.mu.Unlock()
	if n < r.failUntil {
		return nil, r.failErr
	}
	// Return a non-nil LifetimeWatcher pointer (zero value) so buildWatcher
	// wraps it in a vaultLifetimeWatcherAdapter successfully.
	// Do not call Start/Stop/DoneCh/RenewCh on this watcher in this test.
	return new(vaultapi.LifetimeWatcher), nil
}

// ---------------------------------------------------------------------------
// F-4b: doReauth applies backoff after buildWatcher failures (not a hot loop)
// ---------------------------------------------------------------------------

// TestDoReauth_BuildWatcherFailureBackoff verifies that consecutive buildWatcher
// failures do NOT hot-loop. Specifically: after N failures of NewLifetimeWatcher,
// the call count within a bounded wall-clock window must not exceed N+1.
//
// Without the F-4b fix, the tight loop would call NewLifetimeWatcher hundreds
// of times per second when Login always succeeds. With the fix, the first
// backoff of reauthBackoffInitial (1s) enforces at most N+1 calls in the window.
//
// We use a short window (slightly over reauthBackoffInitial) and assert that
// the renewer was called at most N+1 times. This avoids timer races while still
// distinguishing the "no sleep" hot-loop from the "sleeping" correct case.
func TestDoReauth_BuildWatcherFailureBackoff(t *testing.T) {
	const failCount = 2

	fakeAuth := &fakeAuthMethod{
		method: MethodAppRole,
		// No errs → default: returns non-renewable token each call.
	}

	watcherErr := errcode.New(errcode.ErrKeyProviderAuthFailed, "watcher fail")
	var callMu sync.Mutex
	var callCount int
	renewer := &nthSuccessWatcherRenewer{
		mu:           &callMu,
		callCountPtr: &callCount,
		failUntil:    failCount,
		failErr:      watcherErr,
	}

	w := &tokenRenewalWorker{
		client:     renewer,
		authMethod: fakeAuth,
		logger:     slog.Default(),
	}

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	// doReauth will fail the first failCount times, then succeed (or ctx expires).
	// With backoff, the first sleep is reauthBackoffInitial (1s) which is longer
	// than our 200ms window, so only failCount+1 calls at most are expected.
	w.doReauth(ctx)

	callMu.Lock()
	got := callCount
	callMu.Unlock()

	// Allow up to failCount+1 calls (the failCount failures plus at most the
	// first retry attempt when the backoff wakes after ctx expiry).
	// Without the fix, this could be in the hundreds.
	if got > failCount+1 {
		t.Errorf("buildWatcher called %d times within 200ms window; want <= %d (backoff must be applied after failure)",
			got, failCount+1)
	}
}
