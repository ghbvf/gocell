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
	"log/slog"
	"testing"
	"time"

	vaultapi "github.com/hashicorp/vault/api"
	prom "github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/testutil/testtime"
	"github.com/ghbvf/gocell/pkg/testutil/testwait"
)

const transitRenewalBackoffBudget = 3*time.Second + reauthBackoffInitial

func TestTransitKeyProvider_CacheVersionMetrics_ReportsCachedLatestVersion(t *testing.T) {
	reg := prom.NewRegistry()
	metrics, err := NewTransitMetrics(reg)
	require.NoError(t, err)

	metrics.StoreCachedVersion(7)
	if got := scrapeGauge(t, reg, "gocell_vault_cached_key_version"); got != 7 {
		t.Errorf("cached_key_version = %v, want 7", got)
	}

	// Simulate a Rotate by storing a new version (0 then 8, as Rotate does).
	metrics.StoreCachedVersion(0)
	metrics.StoreCachedVersion(8)
	if got := scrapeGauge(t, reg, "gocell_vault_cached_key_version"); got != 8 {
		t.Errorf("cached_key_version after rotate = %v, want 8", got)
	}
}

// TestTokenRenewalWorker_HandleRenewal_IncrementsSuccessCounter verifies that
// a valid renewal notification increments renewSuccess and leaves renewFailure
// at zero.
func TestTokenRenewalWorker_HandleRenewal_IncrementsSuccessCounter(t *testing.T) {
	fw := newFakeTokenWatcher()
	reg := prom.NewRegistry()
	metrics, mErr := NewTransitMetrics(reg)
	if mErr != nil {
		t.Fatalf("NewTransitMetrics: %v", mErr)
	}

	w := &tokenRenewalWorker{
		currentWatcher: fw,
		logger:         slog.Default(),
		metrics:        metrics,
		clock:          clock.Real(),
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
	testwait.External(t, "vault-auth-renewed", func() bool {
		return testutil.ToFloat64(metrics.renewSuccess) >= 1
	}, testtime.D2s, testtime.D1ms, "successCtr must reach 1 after renewal event")
	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Start() returned error, want nil: %v", err)
		}
	case <-time.After(testtime.D2s):
		t.Fatal("Start() did not return after context cancel")
	}

	if got := testutil.ToFloat64(metrics.renewSuccess); got != 1 {
		t.Errorf("renewSuccess counter = %v, want 1", got)
	}
	if got := testutil.ToFloat64(metrics.renewFailure); got != 0 {
		t.Errorf("renewFailure counter = %v, want 0", got)
	}
}

// TestTokenRenewalWorker_HandleRenewal_MultipleRenewals_AccumulatesSuccessCounter
// verifies that multiple renewal events accumulate on the success counter.
func TestTokenRenewalWorker_HandleRenewal_MultipleRenewals_AccumulatesSuccessCounter(t *testing.T) {
	fw := newFakeTokenWatcher()
	reg2 := prom.NewRegistry()
	metrics2, mErr2 := NewTransitMetrics(reg2)
	if mErr2 != nil {
		t.Fatalf("NewTransitMetrics: %v", mErr2)
	}

	w := &tokenRenewalWorker{
		currentWatcher: fw,
		logger:         slog.Default(),
		metrics:        metrics2,
		clock:          clock.Real(),
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
	testwait.External(t, "vault-auth-renewed", func() bool {
		return testutil.ToFloat64(metrics2.renewSuccess) >= 3
	}, testtime.D2s, testtime.D1ms, "successCtr must reach 3 after three renewal events")
	cancel()

	select {
	case <-done:
	case <-time.After(testtime.D2s):
		t.Fatal("Start() did not return after context cancel")
	}

	if got := testutil.ToFloat64(metrics2.renewSuccess); got != 3 {
		t.Errorf("renewSuccess counter = %v, want 3", got)
	}
	if got := testutil.ToFloat64(metrics2.renewFailure); got != 0 {
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
	reg3 := prom.NewRegistry()
	metrics3, mErr3 := NewTransitMetrics(reg3)
	if mErr3 != nil {
		t.Fatalf("NewTransitMetrics: %v", mErr3)
	}
	permErr := errcode.New(errcode.KindUnavailable, errcode.ErrVaultAuthFailed, "test re-auth failure")
	fakeAuth := &fakeAuthMethod{
		method:       MethodAppRole,
		permanentErr: permErr, // always fail → never calls buildWatcher on nil client
	}

	ctx, cancel := context.WithCancel(context.Background())
	w := &tokenRenewalWorker{
		currentWatcher: fw,
		authMethod:     fakeAuth,
		logger:         slog.Default(),
		metrics:        metrics3,
		clock:          clock.Real(),
	}

	done := make(chan error, 1)
	go func() {
		done <- w.Start(ctx)
	}()

	// Signal token no longer renewable (nil error on DoneCh).
	fw.doneCh <- nil

	// Wait for renewFailure to be incremented, then cancel.
	testwait.External(t, "vault-transit-key-rotated", func() bool {
		return testutil.ToFloat64(metrics3.renewFailure) >= 1
	}, testtime.D2s, testtime.D1ms)
	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Start() after ctx cancel must return nil, got: %v", err)
		}
	case <-time.After(testtime.D2s):
		t.Fatal("Start() did not return after DoneCh fired")
	}

	if got := testutil.ToFloat64(metrics3.renewFailure); got != 1 {
		t.Errorf("renewFailure counter = %v, want 1", got)
	}
	if got := testutil.ToFloat64(metrics3.renewSuccess); got != 0 {
		t.Errorf("renewSuccess counter = %v, want 0", got)
	}
}

// TestTokenRenewalWorker_HandleDone_NonNilError_IncrementsFailureCounter
// verifies that a non-nil error on DoneCh (unrecoverable failure) also
// increments renewFailure.
func TestTokenRenewalWorker_HandleDone_NonNilError_IncrementsFailureCounter(t *testing.T) {
	fw := newFakeTokenWatcher()
	reg4 := prom.NewRegistry()
	metrics4, mErr4 := NewTransitMetrics(reg4)
	if mErr4 != nil {
		t.Fatalf("NewTransitMetrics: %v", mErr4)
	}
	permErr := errcode.New(errcode.KindUnavailable, errcode.ErrVaultAuthFailed, "test re-auth failure")
	fakeAuth := &fakeAuthMethod{
		method:       MethodAppRole,
		permanentErr: permErr, // always fail → never calls buildWatcher on nil client
	}

	ctx, cancel := context.WithCancel(context.Background())
	w := &tokenRenewalWorker{
		currentWatcher: fw,
		authMethod:     fakeAuth,
		logger:         slog.Default(),
		metrics:        metrics4,
		clock:          clock.Real(),
	}

	done := make(chan error, 1)
	go func() {
		done <- w.Start(ctx)
	}()

	// Signal unrecoverable renewal error.
	fw.doneCh <- context.DeadlineExceeded

	// Wait for renewFailure to be incremented, then cancel.
	testwait.External(t, "vault-transit-key-rotated", func() bool {
		return testutil.ToFloat64(metrics4.renewFailure) >= 1
	}, testtime.D2s, testtime.D1ms)
	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Start() after ctx cancel must return nil, got: %v", err)
		}
	case <-time.After(testtime.D2s):
		t.Fatal("Start() did not return after DoneCh fired with error")
	}

	if got := testutil.ToFloat64(metrics4.renewFailure); got != 1 {
		t.Errorf("renewFailure counter = %v, want 1", got)
	}
	if got := testutil.ToFloat64(metrics4.renewSuccess); got != 0 {
		t.Errorf("renewSuccess counter = %v, want 0", got)
	}
}

// TestTokenRenewalWorker_NilCounters_NoopOnRenewal verifies that the nil
// metrics guard works: worker with nil metrics does not panic.
func TestTokenRenewalWorker_NilCounters_NoopOnRenewal(t *testing.T) {
	fw := newFakeTokenWatcher()

	// nil metrics — matches worker construction that does not care about metrics.
	fakeAuth := &fakeAuthMethod{method: MethodAppRole}
	w := &tokenRenewalWorker{
		currentWatcher: fw,
		authMethod:     fakeAuth,
		logger:         slog.Default(),
		clock:          clock.Real(),
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
	testwait.External(t, "vault-auth-renewed", func() bool {
		return len(fw.renewCh) == 0
	}, testtime.D2s, testtime.D1ms, "renewCh must be drained after renewal event is sent")
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
	permErr := errcode.New(errcode.KindUnavailable, errcode.ErrVaultAuthFailed, "test failure")
	fakeAuth := &fakeAuthMethod{
		method:       MethodAppRole,
		permanentErr: permErr,
	}

	ctx, cancel := context.WithCancel(context.Background())
	w := &tokenRenewalWorker{
		currentWatcher: fw,
		authMethod:     fakeAuth,
		logger:         slog.Default(),
		clock:          clock.Real(),
		// renewSuccess and renewFailure are nil — no panic test
	}

	done := make(chan error, 1)
	go func() {
		done <- w.Start(ctx)
	}()

	// Trigger re-auth by firing DoneCh with nil (token no longer renewable).
	fw.doneCh <- nil
	// Give re-auth a moment to attempt, then cancel.
	time.Sleep(testtime.MediumPoll) //archtest:allow:test-sleep wait for goroutine to enter blocking re-auth; no started observable
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

// TestRenewalWorker_DoneChError_TriggersReauth verifies that a DoneCh error
// causes the re-auth loop to call authMethod.Login at least once, and that the
// loginOutcome counter records the failure with the "other" reason (the login
// error is ErrVaultAuthFailed, not a network timeout).
func TestRenewalWorker_DoneChError_TriggersReauth(t *testing.T) {
	fw := newFakeTokenWatcher()
	reg5 := prom.NewRegistry()
	metrics5, mErr5 := NewTransitMetrics(reg5)
	if mErr5 != nil {
		t.Fatalf("NewTransitMetrics: %v", mErr5)
	}

	permErr := errcode.New(errcode.KindUnavailable, errcode.ErrVaultAuthFailed, "always fails")
	fakeAuth := &fakeAuthMethod{
		method:       MethodAppRole,
		permanentErr: permErr, // never succeeds → never calls buildWatcher on nil client
	}

	ctx, cancel := context.WithCancel(context.Background())
	w := &tokenRenewalWorker{
		currentWatcher: fw,
		authMethod:     fakeAuth,
		logger:         slog.Default(),
		metrics:        metrics5,
		clock:          clock.Real(),
	}

	done := make(chan error, 1)
	go func() {
		done <- w.Start(ctx)
	}()

	// Trigger re-auth via DoneCh.
	fw.doneCh <- context.DeadlineExceeded

	// Wait for at least one Login call, then cancel.
	testwait.External(t, "vault-auth-renewed", func() bool {
		fakeAuth.mu.Lock()
		defer fakeAuth.mu.Unlock()
		return fakeAuth.calls >= 1
	}, testtime.D2s, time.Millisecond, "fakeAuth.calls must reach 1 after DoneCh triggers re-auth")
	cancel()

	select {
	case <-done:
	case <-time.After(testtime.D2s):
		t.Fatal("Start() did not return after ctx cancel")
	}

	// ErrVaultAuthFailed errors classify as "other" (not a network/timeout error).
	failureCount := testutil.ToFloat64(metrics5.loginOutcome.WithLabelValues(string(MethodAppRole), "failure", reasonOther))
	if failureCount < 1 {
		t.Errorf("expected at least 1 login failure counter increment (reason=other), got %v", failureCount)
	}
}

// TestRenewalWorker_ReauthBackoff_RetriesUntilCancelled verifies that the
// re-auth loop keeps retrying on failure and that authHealthy drops to 0 when
// re-auth starts. ctx cancellation is the exit condition.
func TestRenewalWorker_ReauthBackoff_RetriesUntilCancelled(t *testing.T) {
	fw := newFakeTokenWatcher()
	reg6 := prom.NewRegistry()
	metrics6, mErr6 := NewTransitMetrics(reg6)
	if mErr6 != nil {
		t.Fatalf("NewTransitMetrics: %v", mErr6)
	}

	// authHealthy starts at 0 by default from NewTransitMetrics.

	// All Login calls fail permanently so we stay in the retry loop.
	permErr := errcode.New(errcode.KindUnavailable, errcode.ErrVaultAuthFailed, "always fails")
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
		metrics:        metrics6,
		clock:          clock.Real(),
	}

	done := make(chan error, 1)
	go func() {
		done <- w.Start(ctx)
	}()

	// Fire DoneCh to start re-auth.
	fw.doneCh <- context.DeadlineExceeded

	// Wait for authHealthy to stay at 0 (re-auth ongoing, no buildWatcher success).
	// authHealthy starts at 0 in NewTransitMetrics; doReauth sets it 0 on entry and
	// 1 only after a successful buildWatcher — but fakeAuth always fails so it stays 0.
	testwait.External(t, "vault-readiness-flipped", func() bool {
		return testutil.ToFloat64(metrics6.authHealthy) == 0
	}, testtime.D2s, time.Millisecond, "authHealthy should remain 0 on DoneCh (re-authing)")

	// Wait for 2 failure logins to be recorded.
	testwait.External(t, "vault-auth-renewed", func() bool {
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
	failureCount := testutil.ToFloat64(metrics6.loginOutcome.WithLabelValues(string(MethodAppRole), "failure", reasonOther))
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
	permErr := errcode.New(errcode.KindUnavailable, errcode.ErrVaultAuthFailed, "fail")
	fakeAuth := &fakeAuthMethod{
		method:       MethodAppRole,
		permanentErr: permErr,
	}

	ctx, cancel := context.WithCancel(context.Background())
	w := &tokenRenewalWorker{
		currentWatcher: fw,
		authMethod:     fakeAuth,
		logger:         slog.Default(),
		clock:          clock.Real(),
	}

	done := make(chan error, 1)
	go func() {
		done <- w.Start(ctx)
	}()

	// Trigger re-auth.
	fw.doneCh <- context.DeadlineExceeded

	// Wait for first Login attempt.
	testwait.External(t, "vault-auth-renewed", func() bool {
		fakeAuth.mu.Lock()
		defer fakeAuth.mu.Unlock()
		return fakeAuth.calls >= 1
	}, testtime.D2s, time.Millisecond, "fakeAuth.calls must reach 1 before ctx cancel to confirm backoff started")

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
// authHealthy gauge state machine: Start flips it 0→1, DoneCh drops it back
// to 0, and it stays 0 because ctx is canceled during re-auth before success.
func TestRenewalWorker_AuthHealthyGauge_TransitionsOnStates(t *testing.T) {
	fw := newFakeTokenWatcher()
	reg7 := prom.NewRegistry()
	metrics7, mErr7 := NewTransitMetrics(reg7)
	if mErr7 != nil {
		t.Fatalf("NewTransitMetrics: %v", mErr7)
	}
	// Note: NewTransitMetrics leaves authHealthy at 0; Start (below) is the
	// path that flips it to 1 after the nil-watcher guard.

	permErr := errcode.New(errcode.KindUnavailable, errcode.ErrVaultAuthFailed, "always fails")
	fakeAuth := &fakeAuthMethod{
		method:       MethodAppRole,
		permanentErr: permErr,
	}

	ctx, cancel := context.WithCancel(context.Background())
	w := &tokenRenewalWorker{
		currentWatcher: fw,
		authMethod:     fakeAuth,
		logger:         slog.Default(),
		metrics:        metrics7,
		clock:          clock.Real(),
	}

	done := make(chan error, 1)
	go func() {
		done <- w.Start(ctx)
	}()

	// Wait for Start to enter its loop and flip authHealthy 0→1 (round-2 fix:
	// Set(1) now lives inside Start after the nil-watcher guard, not in
	// initTokenRenewal). Without this wait we'd race against goroutine scheduling.
	testwait.External(t, "vault-readiness-set", func() bool {
		return testutil.ToFloat64(metrics7.authHealthy) == 1
	}, testtime.D2s, time.Millisecond, "Start must flip authHealthy 0→1 after nil-watcher guard")

	// Trigger re-auth.
	fw.doneCh <- nil

	// Wait for gauge to drop to 0.
	testwait.External(t, "vault-readiness-flipped", func() bool {
		return testutil.ToFloat64(metrics7.authHealthy) == 0
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
	reg8 := prom.NewRegistry()
	metrics8, mErr8 := NewTransitMetrics(reg8)
	if mErr8 != nil {
		t.Fatalf("NewTransitMetrics: %v", mErr8)
	}

	// Two timeout failures, then permanently fail to prevent buildWatcher on nil client.
	permErr := errcode.New(errcode.KindUnavailable, errcode.ErrVaultAuthFailed, "permanent other")
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
		metrics:        metrics8,
		clock:          clock.Real(),
	}

	done := make(chan error, 1)
	go func() {
		done <- w.Start(ctx)
	}()

	fw.doneCh <- context.DeadlineExceeded

	// Wait for at least 2 timeout failures to be recorded.
	testwait.External(t, "vault-auth-renewed", func() bool {
		return testutil.ToFloat64(metrics8.loginOutcome.WithLabelValues(
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
		metrics8.loginOutcome.WithLabelValues(string(MethodAppRole), "failure", reasonTimeout))
	if timeoutFailures < 2 {
		t.Errorf("timeout failure counter = %v, want >= 2", timeoutFailures)
	}
}
