package vault

// A13 metrics tests: Prometheus counters for token renewal worker.
//
// These tests verify that:
//   - handleRenewal increments vault_token_renew_success_total on each
//     successful renewal notification.
//   - handleDone increments vault_token_renew_failure_total when the token
//     is no longer renewable (nil error on DoneCh).
//   - handleDone increments vault_token_renew_failure_total when the watcher
//     reports a non-nil error.
//   - Existing tests without counters continue to work (nil-guard).

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"

	vaultapi "github.com/hashicorp/vault/api"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/testutil/testtime"
)

const transitRenewalBackoffBudget = 3*time.Second + reauthBackoffInitial

func TestTransitKeyProvider_CacheVersionMetrics_ReportsCachedLatestVersion(t *testing.T) {
	fake := &fakeVaultClient{latestVersion: 7}
	p := newTestProvider(t, fake)

	collectors := p.CacheVersionMetrics()
	require.Len(t, collectors, 1)
	assertCachedKeyVersionMetric(t, collectors[0], 7)

	_, err := p.Rotate(context.Background())
	require.NoError(t, err)
	assertCachedKeyVersionMetric(t, collectors[0], 8)
}

func assertCachedKeyVersionMetric(t *testing.T, collector prometheus.Collector, version int) {
	t.Helper()
	helpLine := "# HELP gocell_vault_cached_key_version " +
		"Latest Vault Transit key version cached by this process; 0 means cache miss."
	metricLine := fmt.Sprintf(
		"gocell_vault_cached_key_version{key_name=\"gocell-config\",mount_path=\"transit\"} %d",
		version,
	)
	expected := strings.NewReader(helpLine + "\n# TYPE gocell_vault_cached_key_version gauge\n" + metricLine + "\n")
	require.NoError(t, testutil.CollectAndCompare(collector, expected, "gocell_vault_cached_key_version"))
}

// newRenewalCounters creates a pair of unregistered Prometheus counters for
// use in tests. The counters are NOT registered in any registry — they are
// standalone counters exercised via testutil.ToFloat64.
func newRenewalCounters() (success, failure prometheus.Counter) {
	success = prometheus.NewCounter(prometheus.CounterOpts{
		Namespace: "gocell",
		Subsystem: "vault",
		Name:      "token_renew_success_total",
		Help:      "Number of successful Vault token renewals.",
	})
	failure = prometheus.NewCounter(prometheus.CounterOpts{
		Namespace: "gocell",
		Subsystem: "vault",
		Name:      "token_renew_failure_total",
		Help:      "Number of Vault token renewal failures (token no longer renewable).",
	})
	return success, failure
}

// TestTokenRenewalWorker_HandleRenewal_IncrementsSuccessCounter verifies that
// a valid renewal notification increments renewSuccess and leaves renewFailure
// at zero.
func TestTokenRenewalWorker_HandleRenewal_IncrementsSuccessCounter(t *testing.T) {
	fw := newFakeTokenWatcher()
	successCtr, failureCtr := newRenewalCounters()

	w := &tokenRenewalWorker{
		currentWatcher: fw,
		logger:         slog.Default(),
		renewSuccess:   successCtr,
		renewFailure:   failureCtr,
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan error, 1)
	go func() {
		done <- w.Start(ctx)
	}()

	// Wait for the fake watcher's Start to be called.
	select {
	case <-fw.startedCh:
	case <-time.After(testtime.D2s):
		t.Fatal("watcher.Start() was not called within 2s")
	}

	// Send one valid renewal.
	fw.renewCh <- &vaultapi.RenewOutput{
		Secret: &vaultapi.Secret{
			Auth: &vaultapi.SecretAuth{LeaseDuration: 3600},
		},
	}

	// Wait for the loop to consume the renewal before canceling.
	require.Eventually(t, func() bool {
		return testutil.ToFloat64(successCtr) >= 1
	}, time.Second, time.Millisecond)
	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Start() returned error, want nil: %v", err)
		}
	case <-time.After(testtime.D2s):
		t.Fatal("Start() did not return after context cancel")
	}

	if got := testutil.ToFloat64(successCtr); got != 1 {
		t.Errorf("renewSuccess counter = %v, want 1", got)
	}
	if got := testutil.ToFloat64(failureCtr); got != 0 {
		t.Errorf("renewFailure counter = %v, want 0", got)
	}
}

// TestTokenRenewalWorker_HandleRenewal_MultipleRenewals_AccumulatesSuccessCounter
// verifies that multiple renewal events accumulate on the success counter.
func TestTokenRenewalWorker_HandleRenewal_MultipleRenewals_AccumulatesSuccessCounter(t *testing.T) {
	fw := newFakeTokenWatcher()
	successCtr, failureCtr := newRenewalCounters()

	w := &tokenRenewalWorker{
		currentWatcher: fw,
		logger:         slog.Default(),
		renewSuccess:   successCtr,
		renewFailure:   failureCtr,
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan error, 1)
	go func() {
		done <- w.Start(ctx)
	}()

	select {
	case <-fw.startedCh:
	case <-time.After(testtime.D2s):
		t.Fatal("watcher.Start() was not called within 2s")
	}

	renewal := &vaultapi.RenewOutput{
		Secret: &vaultapi.Secret{
			Auth: &vaultapi.SecretAuth{LeaseDuration: 3600},
		},
	}
	fw.renewCh <- renewal
	fw.renewCh <- renewal
	fw.renewCh <- renewal

	// Wait for all three to be consumed.
	require.Eventually(t, func() bool {
		return testutil.ToFloat64(successCtr) >= 3
	}, time.Second, time.Millisecond)
	cancel()

	select {
	case <-done:
	case <-time.After(testtime.D2s):
		t.Fatal("Start() did not return after context cancel")
	}

	if got := testutil.ToFloat64(successCtr); got != 3 {
		t.Errorf("renewSuccess counter = %v, want 3", got)
	}
	if got := testutil.ToFloat64(failureCtr); got != 0 {
		t.Errorf("renewFailure counter = %v, want 0", got)
	}
}

// TestTokenRenewalWorker_HandleDone_NilError_IncrementsFailureCounter verifies
// that a nil error on DoneCh (token no longer renewable) increments
// renewFailure and leaves renewSuccess at zero.
//
// In the new re-auth design, DoneCh fires → increments renewFailure → triggers
// reauthenticate(). ctx cancellation causes Start to return nil.
func TestTokenRenewalWorker_HandleDone_NilError_IncrementsFailureCounter(t *testing.T) {
	fw := newFakeTokenWatcher()
	successCtr, failureCtr := newRenewalCounters()
	permErr := errcode.New(errcode.ErrVaultAuthFailed, "test re-auth failure")
	fakeAuth := &fakeAuthMethod{
		method:       MethodAppRole,
		permanentErr: permErr, // always fail → never calls buildWatcher on nil client
	}

	ctx, cancel := context.WithCancel(context.Background())
	w := &tokenRenewalWorker{
		currentWatcher: fw,
		authMethod:     fakeAuth,
		logger:         slog.Default(),
		renewSuccess:   successCtr,
		renewFailure:   failureCtr,
	}

	done := make(chan error, 1)
	go func() {
		done <- w.Start(ctx)
	}()

	// Signal token no longer renewable (nil error on DoneCh).
	fw.doneCh <- nil

	// Wait for renewFailure to be incremented, then cancel.
	require.Eventually(t, func() bool {
		return testutil.ToFloat64(failureCtr) >= 1
	}, time.Second, time.Millisecond)
	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Start() after ctx cancel must return nil, got: %v", err)
		}
	case <-time.After(testtime.D2s):
		t.Fatal("Start() did not return after DoneCh fired")
	}

	if got := testutil.ToFloat64(failureCtr); got != 1 {
		t.Errorf("renewFailure counter = %v, want 1", got)
	}
	if got := testutil.ToFloat64(successCtr); got != 0 {
		t.Errorf("renewSuccess counter = %v, want 0", got)
	}
}

// TestTokenRenewalWorker_HandleDone_NonNilError_IncrementsFailureCounter
// verifies that a non-nil error on DoneCh (unrecoverable failure) also
// increments renewFailure.
func TestTokenRenewalWorker_HandleDone_NonNilError_IncrementsFailureCounter(t *testing.T) {
	fw := newFakeTokenWatcher()
	successCtr, failureCtr := newRenewalCounters()
	permErr := errcode.New(errcode.ErrVaultAuthFailed, "test re-auth failure")
	fakeAuth := &fakeAuthMethod{
		method:       MethodAppRole,
		permanentErr: permErr, // always fail → never calls buildWatcher on nil client
	}

	ctx, cancel := context.WithCancel(context.Background())
	w := &tokenRenewalWorker{
		currentWatcher: fw,
		authMethod:     fakeAuth,
		logger:         slog.Default(),
		renewSuccess:   successCtr,
		renewFailure:   failureCtr,
	}

	done := make(chan error, 1)
	go func() {
		done <- w.Start(ctx)
	}()

	// Signal unrecoverable renewal error.
	fw.doneCh <- context.DeadlineExceeded

	// Wait for renewFailure to be incremented, then cancel.
	require.Eventually(t, func() bool {
		return testutil.ToFloat64(failureCtr) >= 1
	}, time.Second, time.Millisecond)
	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Start() after ctx cancel must return nil, got: %v", err)
		}
	case <-time.After(testtime.D2s):
		t.Fatal("Start() did not return after DoneCh fired with error")
	}

	if got := testutil.ToFloat64(failureCtr); got != 1 {
		t.Errorf("renewFailure counter = %v, want 1", got)
	}
	if got := testutil.ToFloat64(successCtr); got != 0 {
		t.Errorf("renewSuccess counter = %v, want 0", got)
	}
}

// TestTokenRenewalWorker_NilCounters_NoopOnRenewal verifies that the nil
// counter guard works: existing code paths that do not supply counters do not
// panic.
func TestTokenRenewalWorker_NilCounters_NoopOnRenewal(t *testing.T) {
	fw := newFakeTokenWatcher()

	// No counters — matches existing test construction style.
	fakeAuth := &fakeAuthMethod{method: MethodAppRole}
	w := &tokenRenewalWorker{
		currentWatcher: fw,
		authMethod:     fakeAuth,
		logger:         slog.Default(),
		// renewSuccess and renewFailure are nil
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan error, 1)
	go func() {
		done <- w.Start(ctx)
	}()

	select {
	case <-fw.startedCh:
	case <-time.After(testtime.D2s):
		t.Fatal("watcher.Start() not called")
	}

	fw.renewCh <- &vaultapi.RenewOutput{
		Secret: &vaultapi.Secret{
			Auth: &vaultapi.SecretAuth{LeaseDuration: 3600},
		},
	}
	// Wait for the renewal to be consumed before canceling.
	require.Eventually(t, func() bool {
		return len(fw.renewCh) == 0
	}, time.Second, time.Millisecond)
	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Start() returned unexpected error: %v", err)
		}
	case <-time.After(testtime.D2s):
		t.Fatal("Start() did not return")
	}
	// No panic = pass.
}

// TestTokenRenewalWorker_NilCounters_NoopOnDone verifies that the nil counter
// guard works when DoneCh fires (no panic when renewFailure is nil).
// In the new re-auth design, DoneCh triggers reauthenticate(); ctx cancellation
// is the exit condition.
func TestTokenRenewalWorker_NilCounters_NoopOnDone(t *testing.T) {
	fw := newFakeTokenWatcher()
	// fakeAuth with a permanent failure so re-auth keeps retrying; ctx cancel exits.
	permErr := errcode.New(errcode.ErrVaultAuthFailed, "test failure")
	fakeAuth := &fakeAuthMethod{
		method:       MethodAppRole,
		permanentErr: permErr,
	}

	ctx, cancel := context.WithCancel(context.Background())
	w := &tokenRenewalWorker{
		currentWatcher: fw,
		authMethod:     fakeAuth,
		logger:         slog.Default(),
		// renewSuccess and renewFailure are nil — no panic test
	}

	done := make(chan error, 1)
	go func() {
		done <- w.Start(ctx)
	}()

	// Trigger re-auth by firing DoneCh with nil (token no longer renewable).
	fw.doneCh <- nil
	// Give re-auth a moment to attempt, then cancel.
	time.Sleep(testtime.MediumPoll)
	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Start() after ctx cancel must return nil, got: %v", err)
		}
	case <-time.After(testtime.D2s):
		t.Fatal("Start() did not return")
	}
	// No panic = pass.
}

// ---------------------------------------------------------------------------
// Re-auth loop tests
// ---------------------------------------------------------------------------

// newLoginOutcomeCounter creates an unregistered CounterVec with {method,result,reason}
// labels for use in tests.
func newLoginOutcomeCounter() *prometheus.CounterVec {
	return prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "gocell",
		Subsystem: "vault",
		Name:      "auth_login_total",
		Help:      "Vault auth login attempts.",
	}, []string{"method", "result", "reason"})
}

// TestRenewalWorker_DoneChError_TriggersReauth verifies that a DoneCh error
// causes the re-auth loop to call authMethod.Login at least once, and that the
// loginOutcome counter records the failure with the "other" reason (the login
// error is ErrVaultAuthFailed, not a network timeout).
func TestRenewalWorker_DoneChError_TriggersReauth(t *testing.T) {
	fw := newFakeTokenWatcher()
	loginOutcome := newLoginOutcomeCounter()

	permErr := errcode.New(errcode.ErrVaultAuthFailed, "always fails")
	fakeAuth := &fakeAuthMethod{
		method:       MethodAppRole,
		permanentErr: permErr, // never succeeds → never calls buildWatcher on nil client
	}

	ctx, cancel := context.WithCancel(context.Background())
	w := &tokenRenewalWorker{
		currentWatcher: fw,
		authMethod:     fakeAuth,
		logger:         slog.Default(),
		loginOutcome:   loginOutcome,
	}

	done := make(chan error, 1)
	go func() {
		done <- w.Start(ctx)
	}()

	// Trigger re-auth via DoneCh.
	fw.doneCh <- context.DeadlineExceeded

	// Wait for at least one Login call, then cancel.
	require.Eventually(t, func() bool {
		fakeAuth.mu.Lock()
		defer fakeAuth.mu.Unlock()
		return fakeAuth.calls >= 1
	}, testtime.D2s, time.Millisecond)
	cancel()

	select {
	case <-done:
	case <-time.After(testtime.D2s):
		t.Fatal("Start() did not return after ctx cancel")
	}

	// ErrVaultAuthFailed errors classify as "other" (not a network/timeout error).
	failureCount := testutil.ToFloat64(loginOutcome.WithLabelValues(string(MethodAppRole), "failure", reasonOther))
	if failureCount < 1 {
		t.Errorf("expected at least 1 login failure counter increment (reason=other), got %v", failureCount)
	}
}

// TestRenewalWorker_ReauthBackoff_RetriesUntilCancelled verifies that the
// re-auth loop keeps retrying on failure and that authHealthy drops to 0 when
// re-auth starts. ctx cancellation is the exit condition.
func TestRenewalWorker_ReauthBackoff_RetriesUntilCancelled(t *testing.T) {
	fw := newFakeTokenWatcher()
	loginOutcome := newLoginOutcomeCounter()

	authHealthy := prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace: "gocell",
		Subsystem: "vault",
		Name:      "token_auth_healthy_reauth_test",
		Help:      "Test gauge.",
	})
	authHealthy.Set(1)

	// All Login calls fail permanently so we stay in the retry loop.
	permErr := errcode.New(errcode.ErrVaultAuthFailed, "always fails")
	fakeAuth := &fakeAuthMethod{
		method:       MethodAppRole,
		permanentErr: permErr,
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	w := &tokenRenewalWorker{
		currentWatcher: fw,
		authMethod:     fakeAuth,
		logger:         slog.Default(),
		loginOutcome:   loginOutcome,
		authHealthy:    authHealthy,
	}

	done := make(chan error, 1)
	go func() {
		done <- w.Start(ctx)
	}()

	// Fire DoneCh to start re-auth.
	fw.doneCh <- context.DeadlineExceeded

	// Wait for authHealthy to drop to 0 (re-auth started).
	require.Eventually(t, func() bool {
		return testutil.ToFloat64(authHealthy) == 0
	}, testtime.D2s, time.Millisecond, "authHealthy should drop to 0 on DoneCh")

	// Wait for 2 failure logins to be recorded.
	require.Eventually(t, func() bool {
		fakeAuth.mu.Lock()
		defer fakeAuth.mu.Unlock()
		return fakeAuth.calls >= 2
	}, testtime.EventuallyLong, testtime.D10ms, "expected at least 2 Login calls")

	// Cancel — re-auth loop exits.
	cancel()

	select {
	case <-done:
	case <-time.After(testtime.EventuallyDefault):
		t.Fatal("Start() did not return after ctx cancel")
	}

	// Verify failure counter.
	failureCount := testutil.ToFloat64(loginOutcome.WithLabelValues(string(MethodAppRole), "failure", reasonOther))
	if failureCount < 2 {
		t.Errorf("expected >= 2 login failure counter increments, got %v", failureCount)
	}
}

// TestRenewalWorker_CtxCancelDuringReauth_ReturnsCleanly verifies that
// canceling the context while reauthenticate is sleeping causes Start to
// return nil promptly (no hang).
func TestRenewalWorker_CtxCancelDuringReauth_ReturnsCleanly(t *testing.T) {
	fw := newFakeTokenWatcher()

	// Auth method always fails so re-auth keeps sleeping; ctx cancel must wake it.
	permErr := errcode.New(errcode.ErrVaultAuthFailed, "fail")
	fakeAuth := &fakeAuthMethod{
		method:       MethodAppRole,
		permanentErr: permErr,
	}

	ctx, cancel := context.WithCancel(context.Background())
	w := &tokenRenewalWorker{
		currentWatcher: fw,
		authMethod:     fakeAuth,
		logger:         slog.Default(),
	}

	done := make(chan error, 1)
	go func() {
		done <- w.Start(ctx)
	}()

	// Trigger re-auth.
	fw.doneCh <- context.DeadlineExceeded

	// Wait for first Login attempt.
	require.Eventually(t, func() bool {
		fakeAuth.mu.Lock()
		defer fakeAuth.mu.Unlock()
		return fakeAuth.calls >= 1
	}, testtime.D2s, time.Millisecond)

	// Cancel now — reauthenticate must wake from the sleep and return.
	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Start() after ctx cancel must return nil, got: %v", err)
		}
	case <-time.After(transitRenewalBackoffBudget):
		t.Fatal("Start() did not return promptly after ctx cancel (backoff not interruptible?)")
	}
}

// TestRenewalWorker_AuthHealthyGauge_TransitionsOnStates verifies the
// authHealthy gauge: starts at 1, drops to 0 on DoneCh, and stays 0 (because
// ctx is canceled during re-auth before success).
func TestRenewalWorker_AuthHealthyGauge_TransitionsOnStates(t *testing.T) {
	fw := newFakeTokenWatcher()

	authHealthy := prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace: "gocell",
		Subsystem: "vault",
		Name:      "token_auth_healthy_gauge_test",
		Help:      "Test gauge.",
	})
	authHealthy.Set(1)

	permErr := errcode.New(errcode.ErrVaultAuthFailed, "always fails")
	fakeAuth := &fakeAuthMethod{
		method:       MethodAppRole,
		permanentErr: permErr,
	}

	ctx, cancel := context.WithCancel(context.Background())
	w := &tokenRenewalWorker{
		currentWatcher: fw,
		authMethod:     fakeAuth,
		logger:         slog.Default(),
		authHealthy:    authHealthy,
	}

	done := make(chan error, 1)
	go func() {
		done <- w.Start(ctx)
	}()

	// Initial value should be 1 (set before Start).
	if got := testutil.ToFloat64(authHealthy); got != 1 {
		t.Errorf("initial authHealthy = %v, want 1", got)
	}

	// Trigger re-auth.
	fw.doneCh <- nil

	// Wait for gauge to drop to 0.
	require.Eventually(t, func() bool {
		return testutil.ToFloat64(authHealthy) == 0
	}, testtime.D2s, time.Millisecond, "authHealthy should drop to 0 after DoneCh")

	cancel()

	select {
	case <-done:
	case <-time.After(testtime.EventuallyDefault):
		t.Fatal("Start() did not return")
	}
}

// TestRenewalWorker_LoginOutcomeCounter_LabelsSet verifies that the
// loginOutcome counter records the correct {method, result, reason} labels.
// Two timeout failures (context.DeadlineExceeded) are followed by a permanent
// failure — ctx cancel exits the loop.
func TestRenewalWorker_LoginOutcomeCounter_LabelsSet(t *testing.T) {
	fw := newFakeTokenWatcher()
	loginOutcome := newLoginOutcomeCounter()

	// Two timeout failures, then permanently fail to prevent buildWatcher on nil client.
	permErr := errcode.New(errcode.ErrVaultAuthFailed, "permanent other")
	fakeAuth := &fakeAuthMethod{
		method: MethodAppRole,
		errs: []error{
			context.DeadlineExceeded, // call 0 → timeout
			context.DeadlineExceeded, // call 1 → timeout
		},
		permanentErr: permErr, // all subsequent calls → other
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	w := &tokenRenewalWorker{
		currentWatcher: fw,
		authMethod:     fakeAuth,
		logger:         slog.Default(),
		loginOutcome:   loginOutcome,
	}

	done := make(chan error, 1)
	go func() {
		done <- w.Start(ctx)
	}()

	fw.doneCh <- context.DeadlineExceeded

	// Wait for at least 2 timeout failures to be recorded.
	require.Eventually(t, func() bool {
		return testutil.ToFloat64(loginOutcome.WithLabelValues(
			string(MethodAppRole), "failure", reasonTimeout)) >= 2
	}, testtime.EventuallyLong, testtime.D10ms, "expected 2 timeout failures")

	cancel()
	select {
	case <-done:
	case <-time.After(testtime.EventuallyDefault):
		t.Fatal("Start() did not return")
	}

	// Two timeout failures.
	timeoutFailures := testutil.ToFloat64(
		loginOutcome.WithLabelValues(string(MethodAppRole), "failure", reasonTimeout))
	if timeoutFailures < 2 {
		t.Errorf("timeout failure counter = %v, want >= 2", timeoutFailures)
	}
}
