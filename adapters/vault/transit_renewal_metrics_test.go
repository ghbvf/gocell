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
	"testing"
	"time"

	vaultapi "github.com/hashicorp/vault/api"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"

	"log/slog"
)

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
		watcher:      fw,
		logger:       slog.Default(),
		renewSuccess: successCtr,
		renewFailure: failureCtr,
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
	case <-time.After(2 * time.Second):
		t.Fatal("watcher.Start() was not called within 2s")
	}

	// Send one valid renewal.
	fw.renewCh <- &vaultapi.RenewOutput{
		Secret: &vaultapi.Secret{
			Auth: &vaultapi.SecretAuth{LeaseDuration: 3600},
		},
	}

	// Wait for the loop to consume the renewal before cancelling.
	require.Eventually(t, func() bool {
		return testutil.ToFloat64(successCtr) >= 1
	}, time.Second, time.Millisecond)
	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Start() returned error, want nil: %v", err)
		}
	case <-time.After(2 * time.Second):
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
		watcher:      fw,
		logger:       slog.Default(),
		renewSuccess: successCtr,
		renewFailure: failureCtr,
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan error, 1)
	go func() {
		done <- w.Start(ctx)
	}()

	select {
	case <-fw.startedCh:
	case <-time.After(2 * time.Second):
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
	case <-time.After(2 * time.Second):
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
func TestTokenRenewalWorker_HandleDone_NilError_IncrementsFailureCounter(t *testing.T) {
	fw := newFakeTokenWatcher()
	successCtr, failureCtr := newRenewalCounters()

	w := &tokenRenewalWorker{
		watcher:      fw,
		logger:       slog.Default(),
		renewSuccess: successCtr,
		renewFailure: failureCtr,
	}

	ctx := context.Background()

	done := make(chan error, 1)
	go func() {
		done <- w.Start(ctx)
	}()

	// Signal token no longer renewable (nil error on DoneCh).
	fw.doneCh <- nil

	select {
	case err := <-done:
		if err == nil {
			t.Error("Start() must return non-nil error when token is no longer renewable")
		}
	case <-time.After(2 * time.Second):
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

	w := &tokenRenewalWorker{
		watcher:      fw,
		logger:       slog.Default(),
		renewSuccess: successCtr,
		renewFailure: failureCtr,
	}

	ctx := context.Background()

	done := make(chan error, 1)
	go func() {
		done <- w.Start(ctx)
	}()

	// Signal unrecoverable renewal error.
	fw.doneCh <- context.DeadlineExceeded

	select {
	case err := <-done:
		if err == nil {
			t.Error("Start() must return non-nil error on DoneCh error")
		}
	case <-time.After(2 * time.Second):
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
	w := &tokenRenewalWorker{
		watcher: fw,
		logger:  slog.Default(),
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
	case <-time.After(2 * time.Second):
		t.Fatal("watcher.Start() not called")
	}

	fw.renewCh <- &vaultapi.RenewOutput{
		Secret: &vaultapi.Secret{
			Auth: &vaultapi.SecretAuth{LeaseDuration: 3600},
		},
	}
	// Wait for the renewal to be consumed before cancelling.
	require.Eventually(t, func() bool {
		return len(fw.renewCh) == 0
	}, time.Second, time.Millisecond)
	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Start() returned unexpected error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Start() did not return")
	}
	// No panic = pass.
}

// TestTokenRenewalWorker_NilCounters_NoopOnDone verifies that the nil counter
// guard works when DoneCh fires (no panic when renewFailure is nil).
func TestTokenRenewalWorker_NilCounters_NoopOnDone(t *testing.T) {
	fw := newFakeTokenWatcher()

	w := &tokenRenewalWorker{
		watcher: fw,
		logger:  slog.Default(),
	}

	ctx := context.Background()
	done := make(chan error, 1)
	go func() {
		done <- w.Start(ctx)
	}()

	fw.doneCh <- nil

	select {
	case err := <-done:
		if err == nil {
			t.Error("Start() must return non-nil error on nil DoneCh")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Start() did not return")
	}
	// No panic = pass.
}
