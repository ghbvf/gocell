package outbox

import (
	"context"
	"errors"
	"fmt"
	"math/bits"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/goleak"

	"github.com/ghbvf/gocell/kernel/idempotency"
)

// ConsumerBase lives in kernel/outbox so tests covering its behaviour must
// also live here (kernel layer requires >= 90% coverage). These tests
// previously lived in adapters/rabbitmq and were left behind when
// ConsumerBase was hoisted out of the adapter in PR #176.

// --- Test fakes ----------------------------------------------------------

type fakeReceipt struct {
	mu            sync.Mutex
	commitCalled  bool
	releaseCalled bool
	extendCalls   atomic.Int32
	extendErr     error
}

func (r *fakeReceipt) Commit(_ context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.commitCalled = true
	return nil
}

func (r *fakeReceipt) Release(_ context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.releaseCalled = true
	return nil
}

func (r *fakeReceipt) Extend(_ context.Context, _ time.Duration) error {
	r.extendCalls.Add(1)
	return r.extendErr
}

var _ Receipt = (*fakeReceipt)(nil)

type fakeClaimer struct {
	mu      sync.Mutex
	state   idempotency.ClaimState
	receipt Receipt
	err     error
	calls   []string
}

func (c *fakeClaimer) Claim(_ context.Context, key string, _, _ time.Duration) (idempotency.ClaimState, Receipt, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls = append(c.calls, key)
	return c.state, c.receipt, c.err
}

var _ idempotency.Claimer = (*fakeClaimer)(nil)

// signalingClaimer wraps an inner Claimer and sends on the started channel
// the first time Claim is invoked. Used to replace time.Sleep startup synchronisation
// in tests that need to cancel ctx after the claimer has been called.
type signalingClaimer struct {
	inner   idempotency.Claimer
	started chan<- struct{}
	once    sync.Once
}

func (s *signalingClaimer) Claim(ctx context.Context, key string, leaseTTL, renewInterval time.Duration) (idempotency.ClaimState, Receipt, error) {
	s.once.Do(func() {
		select {
		case s.started <- struct{}{}:
		default:
		}
	})
	return s.inner.Claim(ctx, key, leaseTTL, renewInterval)
}

var _ idempotency.Claimer = (*signalingClaimer)(nil)

type claimOutcome struct {
	state   idempotency.ClaimState
	receipt Receipt
	err     error
}

// sequenceClaimer returns the next queued outcome on each Claim call; once
// exhausted it keeps returning the last outcome.
type sequenceClaimer struct {
	mu        sync.Mutex
	outcomes  []claimOutcome
	callCount int
}

func (c *sequenceClaimer) Claim(_ context.Context, _ string, _, _ time.Duration) (idempotency.ClaimState, Receipt, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	idx := c.callCount
	c.callCount++
	if idx < len(c.outcomes) {
		o := c.outcomes[idx]
		return o.state, o.receipt, o.err
	}
	o := c.outcomes[len(c.outcomes)-1]
	return o.state, o.receipt, o.err
}

var _ idempotency.Claimer = (*sequenceClaimer)(nil)

// --- ClaimPolicy / config --------------------------------------------------

func TestClaimPolicy_Valid(t *testing.T) {
	assert.True(t, ClaimPolicyFailClosed.Valid())
	assert.True(t, ClaimPolicyFailOpen.Valid())
	assert.False(t, claimPolicySentinel.Valid())
	assert.False(t, ClaimPolicy(99).Valid())
}

func TestClaimPolicy_String(t *testing.T) {
	tests := []struct {
		policy ClaimPolicy
		want   string
	}{
		{ClaimPolicyFailClosed, "fail-closed"},
		{ClaimPolicyFailOpen, "fail-open"},
		{ClaimPolicy(99), "unknown(99)"},
		{claimPolicySentinel, "unknown(2)"},
	}
	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			assert.Equal(t, tt.want, tt.policy.String())
		})
	}
}

func TestConsumerBaseConfig_SetDefaults_ZeroValues(t *testing.T) {
	cfg := ConsumerBaseConfig{}
	cfg.SetDefaults()
	assert.Equal(t, 3, cfg.RetryCount)
	assert.Equal(t, time.Second, cfg.RetryBaseDelay)
	assert.Equal(t, idempotency.DefaultTTL, cfg.IdempotencyTTL)
	assert.Equal(t, idempotency.DefaultLeaseTTL, cfg.LeaseTTL)
	assert.Equal(t, 3, cfg.ClaimRetryCount)
	assert.Equal(t, time.Second, cfg.ClaimRetryBaseDelay)
	assert.Equal(t, 30*time.Second, cfg.MaxRetryDelay)
}

func TestConsumerBaseConfig_SetDefaults_NegativeValuesReplaced(t *testing.T) {
	cfg := ConsumerBaseConfig{
		RetryCount:          -1,
		RetryBaseDelay:      -time.Second,
		IdempotencyTTL:      -time.Hour,
		LeaseTTL:            -time.Minute,
		ClaimRetryCount:     -5,
		ClaimRetryBaseDelay: -time.Second,
		MaxRetryDelay:       -time.Second,
	}
	cfg.SetDefaults()
	assert.Equal(t, 3, cfg.RetryCount)
	assert.Equal(t, time.Second, cfg.RetryBaseDelay)
	assert.Equal(t, idempotency.DefaultTTL, cfg.IdempotencyTTL)
	assert.Equal(t, idempotency.DefaultLeaseTTL, cfg.LeaseTTL)
	assert.Equal(t, 3, cfg.ClaimRetryCount)
	assert.Equal(t, time.Second, cfg.ClaimRetryBaseDelay)
	assert.Equal(t, 30*time.Second, cfg.MaxRetryDelay)
}

func TestConsumerBaseConfig_SetDefaults_PositiveValuesPreserved(t *testing.T) {
	cfg := ConsumerBaseConfig{
		RetryCount:          7,
		RetryBaseDelay:      500 * time.Millisecond,
		IdempotencyTTL:      48 * time.Hour,
		LeaseTTL:            10 * time.Minute,
		ClaimRetryCount:     2,
		ClaimRetryBaseDelay: 250 * time.Millisecond,
		MaxRetryDelay:       5 * time.Second,
	}
	cfg.SetDefaults()
	assert.Equal(t, 7, cfg.RetryCount)
	assert.Equal(t, 500*time.Millisecond, cfg.RetryBaseDelay)
	assert.Equal(t, 48*time.Hour, cfg.IdempotencyTTL)
	assert.Equal(t, 10*time.Minute, cfg.LeaseTTL)
	assert.Equal(t, 2, cfg.ClaimRetryCount)
	assert.Equal(t, 250*time.Millisecond, cfg.ClaimRetryBaseDelay)
	assert.Equal(t, 5*time.Second, cfg.MaxRetryDelay)
}

// --- exponentialDelay / ExponentialDelay -----------------------------------

// TestExponentialDelay_PublicAPI verifies the exported ExponentialDelay
// function that adapters should use instead of maintaining their own copies.
func TestExponentialDelay_PublicAPI(t *testing.T) {
	base := 100 * time.Millisecond
	maxDelay := 5 * time.Second
	cases := []struct {
		name     string
		base     time.Duration
		maxDelay time.Duration
		attempt  int
		want     time.Duration
	}{
		{"zero_base_returns_zero", 0, maxDelay, 3, 0},
		{"attempt_0_equals_base", base, maxDelay, 0, base},
		{"attempt_1_double", base, maxDelay, 1, 2 * base},
		{"attempt_5_capped_by_base_shift", base, maxDelay, 5, 3200 * time.Millisecond},
		{"capped_at_max", base, maxDelay, 10, maxDelay},
		{"overflow_protection_63", base, maxDelay, 63, maxDelay},
		{"overflow_protection_65", base, maxDelay, 65, maxDelay},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ExponentialDelay(tc.base, tc.maxDelay, tc.attempt)
			if got != tc.want {
				t.Errorf("ExponentialDelay(%v, %v, %d) = %v, want %v",
					tc.base, tc.maxDelay, tc.attempt, got, tc.want)
			}
		})
	}
}

func TestExponentialDelay_Table(t *testing.T) {
	tests := []struct {
		name     string
		base     time.Duration
		maxDelay time.Duration
		attempt  int
		want     time.Duration
	}{
		{name: "attempt 0 returns base", base: time.Second, maxDelay: 30 * time.Second, attempt: 0, want: time.Second},
		{name: "attempt 1 doubles", base: time.Second, maxDelay: 30 * time.Second, attempt: 1, want: 2 * time.Second},
		{name: "attempt 3 is 8x", base: time.Second, maxDelay: 30 * time.Second, attempt: 3, want: 8 * time.Second},
		{name: "attempt capped at maxDelay", base: time.Second, maxDelay: 30 * time.Second, attempt: 10, want: 30 * time.Second},
		{name: "large attempt overflow guard", base: time.Second, maxDelay: 30 * time.Second, attempt: 100, want: 30 * time.Second},
		{name: "zero base returns 0", base: 0, maxDelay: 30 * time.Second, attempt: 5, want: 0},
		{name: "negative base returns 0", base: -time.Second, maxDelay: 30 * time.Second, attempt: 3, want: 0},
		{name: "base larger than max returns max", base: 100 * time.Second, maxDelay: 30 * time.Second, attempt: 0, want: 30 * time.Second},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ExponentialDelay(tt.base, tt.maxDelay, tt.attempt)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestExponentialDelay_ExactMaxSafeShift(t *testing.T) {
	base := time.Second
	maxSafeShift := 63 - bits.Len64(uint64(base))
	assert.Equal(t, 30*time.Second, ExponentialDelay(base, 30*time.Second, maxSafeShift))
	assert.Equal(t, 30*time.Second, ExponentialDelay(base, 30*time.Second, maxSafeShift+1))
}

// --- NewConsumerBase -------------------------------------------------------

func TestNewConsumerBase_InvalidClaimPolicy(t *testing.T) {
	_, err := NewConsumerBase(&fakeClaimer{}, ConsumerBaseConfig{
		ClaimPolicy: ClaimPolicy(99),
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid ClaimPolicy")
}

func TestNewConsumerBase_DefaultClaimPolicyFailClosed(t *testing.T) {
	cb, err := NewConsumerBase(&fakeClaimer{}, ConsumerBaseConfig{})
	require.NoError(t, err)
	assert.Equal(t, ClaimPolicyFailClosed, cb.config.ClaimPolicy)
}

func TestNewConsumerBase_ExplicitFailOpenPreserved(t *testing.T) {
	cb, err := NewConsumerBase(&fakeClaimer{}, ConsumerBaseConfig{
		ClaimPolicy: ClaimPolicyFailOpen,
	})
	require.NoError(t, err)
	assert.Equal(t, ClaimPolicyFailOpen, cb.config.ClaimPolicy)
}

// --- Wrap: happy paths -----------------------------------------------------

func TestConsumerBase_Wrap_ClaimAcquired_Ack_ThreadsReceipt(t *testing.T) {
	receipt := &fakeReceipt{}
	claimer := &fakeClaimer{state: idempotency.ClaimAcquired, receipt: receipt}

	cb, err := NewConsumerBase(claimer, ConsumerBaseConfig{})
	require.NoError(t, err)

	called := false
	handler := cb.Wrap(Subscription{Topic: "topic", ConsumerGroup: "cg"}, func(_ context.Context, _ Entry) HandleResult {
		called = true
		return HandleResult{Disposition: DispositionAck}
	})

	res := handler(context.Background(), Entry{ID: "evt-1"})

	assert.True(t, called)
	assert.Equal(t, DispositionAck, res.Disposition)
	assert.Same(t, receipt, res.Receipt)

	receipt.mu.Lock()
	defer receipt.mu.Unlock()
	assert.False(t, receipt.commitCalled, "ConsumerBase must not Commit — that's the delivery loop's job")
	assert.False(t, receipt.releaseCalled)
}

func TestConsumerBase_Wrap_ClaimDone_SkipsHandler(t *testing.T) {
	claimer := &fakeClaimer{state: idempotency.ClaimDone}

	cb, err := NewConsumerBase(claimer, ConsumerBaseConfig{})
	require.NoError(t, err)

	called := false
	handler := cb.Wrap(Subscription{Topic: "topic", ConsumerGroup: "cg"}, func(_ context.Context, _ Entry) HandleResult {
		called = true
		return HandleResult{Disposition: DispositionAck}
	})

	res := handler(context.Background(), Entry{ID: "evt-dup"})
	assert.False(t, called, "ClaimDone must skip the handler")
	assert.Equal(t, DispositionAck, res.Disposition)
	assert.Nil(t, res.Receipt)
}

func TestConsumerBase_Wrap_ClaimBusy_Requeues(t *testing.T) {
	claimer := &fakeClaimer{state: idempotency.ClaimBusy}

	cb, err := NewConsumerBase(claimer, ConsumerBaseConfig{
		RetryBaseDelay: 5 * time.Millisecond, // short backoff for test
	})
	require.NoError(t, err)

	called := false
	handler := cb.Wrap(Subscription{Topic: "topic", ConsumerGroup: "cg"}, func(_ context.Context, _ Entry) HandleResult {
		called = true
		return HandleResult{Disposition: DispositionAck}
	})

	res := handler(context.Background(), Entry{ID: "evt-busy"})
	assert.False(t, called)
	assert.Equal(t, DispositionRequeue, res.Disposition)
}

// --- Wrap: retry loop ------------------------------------------------------

func TestConsumerBase_Wrap_TransientError_RetriesUntilAck(t *testing.T) {
	receipt := &fakeReceipt{}
	claimer := &fakeClaimer{state: idempotency.ClaimAcquired, receipt: receipt}

	cb, err := NewConsumerBase(claimer, ConsumerBaseConfig{
		RetryCount:     3,
		RetryBaseDelay: time.Millisecond,
	})
	require.NoError(t, err)

	attempts := 0
	handler := cb.Wrap(Subscription{Topic: "topic", ConsumerGroup: "cg"}, func(_ context.Context, _ Entry) HandleResult {
		attempts++
		if attempts == 1 {
			return HandleResult{Disposition: DispositionRequeue, Err: errors.New("transient")}
		}
		return HandleResult{Disposition: DispositionAck}
	})

	res := handler(context.Background(), Entry{ID: "evt-retry"})
	assert.Equal(t, 2, attempts)
	assert.Equal(t, DispositionAck, res.Disposition)
	assert.Same(t, receipt, res.Receipt)
}

func TestConsumerBase_Wrap_RetryBudgetExhausted_RejectsToDLX(t *testing.T) {
	receipt := &fakeReceipt{}
	claimer := &fakeClaimer{state: idempotency.ClaimAcquired, receipt: receipt}

	cb, err := NewConsumerBase(claimer, ConsumerBaseConfig{
		RetryCount:     2,
		RetryBaseDelay: time.Millisecond,
	})
	require.NoError(t, err)

	attempts := 0
	handler := cb.Wrap(Subscription{Topic: "topic", ConsumerGroup: "cg"}, func(_ context.Context, _ Entry) HandleResult {
		attempts++
		return HandleResult{Disposition: DispositionRequeue, Err: errors.New("always fail")}
	})

	res := handler(context.Background(), Entry{ID: "evt-exhaust"})
	assert.Equal(t, 2, attempts)
	assert.Equal(t, DispositionReject, res.Disposition)
	assert.Same(t, receipt, res.Receipt)
}

func TestConsumerBase_Wrap_ExplicitReject_NoRetry(t *testing.T) {
	receipt := &fakeReceipt{}
	claimer := &fakeClaimer{state: idempotency.ClaimAcquired, receipt: receipt}

	cb, err := NewConsumerBase(claimer, ConsumerBaseConfig{
		RetryCount:     5,
		RetryBaseDelay: time.Millisecond,
	})
	require.NoError(t, err)

	attempts := 0
	handler := cb.Wrap(Subscription{Topic: "topic", ConsumerGroup: "cg"}, func(_ context.Context, _ Entry) HandleResult {
		attempts++
		return HandleResult{Disposition: DispositionReject, Err: errors.New("bad payload")}
	})

	res := handler(context.Background(), Entry{ID: "evt-explicit-reject"})
	assert.Equal(t, 1, attempts, "DispositionReject must skip retries")
	assert.Equal(t, DispositionReject, res.Disposition)
	assert.Same(t, receipt, res.Receipt)
}

func TestConsumerBase_Wrap_WrappedPermanentError_DetectedAndRejected(t *testing.T) {
	receipt := &fakeReceipt{}
	claimer := &fakeClaimer{state: idempotency.ClaimAcquired, receipt: receipt}

	cb, err := NewConsumerBase(claimer, ConsumerBaseConfig{
		RetryCount:     5,
		RetryBaseDelay: time.Millisecond,
	})
	require.NoError(t, err)

	attempts := 0
	handler := cb.Wrap(Subscription{Topic: "topic", ConsumerGroup: "cg"}, func(_ context.Context, _ Entry) HandleResult {
		attempts++
		return HandleResult{
			Disposition: DispositionRequeue,
			Err:         fmt.Errorf("ctx: %w", NewPermanentError(errors.New("unmarshal"))),
		}
	})

	res := handler(context.Background(), Entry{ID: "evt-perm"})
	assert.Equal(t, 1, attempts, "wrapped PermanentError must be detected on first attempt")
	assert.Equal(t, DispositionReject, res.Disposition)
}

func TestConsumerBase_Wrap_CtxCancelled_DuringRetry_Requeues(t *testing.T) {
	claimer := &fakeClaimer{state: idempotency.ClaimAcquired}

	cb, err := NewConsumerBase(claimer, ConsumerBaseConfig{
		RetryCount:     5,
		RetryBaseDelay: 5 * time.Second, // long enough that ctx cancel wins
	})
	require.NoError(t, err)

	// Signal channel: handler sends when it has been called, meaning ConsumerBase
	// is about to enter the retry backoff sleep — safe to cancel ctx at that point.
	started := make(chan struct{}, 1)
	handler := cb.Wrap(Subscription{Topic: "topic", ConsumerGroup: "cg"}, func(_ context.Context, _ Entry) HandleResult {
		select {
		case started <- struct{}{}:
		default:
		}
		return HandleResult{Disposition: DispositionRequeue, Err: errors.New("transient")}
	})

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		<-started
		cancel()
	}()

	start := time.Now()
	res := handler(ctx, Entry{ID: "evt-ctx"})
	elapsed := time.Since(start)

	assert.Equal(t, DispositionRequeue, res.Disposition)
	assert.Less(t, elapsed, time.Second, "ctx cancel must short-circuit retry backoff")
}

// --- Wrap: claim failure paths --------------------------------------------

func TestConsumerBase_Wrap_ClaimError_FailClosed_LocalRetryThenSuccess(t *testing.T) {
	receipt := &fakeReceipt{}
	claimer := &sequenceClaimer{outcomes: []claimOutcome{
		{err: errors.New("redis down")},
		{err: errors.New("redis down")},
		{state: idempotency.ClaimAcquired, receipt: receipt},
	}}

	cb, err := NewConsumerBase(claimer, ConsumerBaseConfig{
		ClaimRetryCount:     3,
		ClaimRetryBaseDelay: time.Millisecond,
	})
	require.NoError(t, err)

	called := false
	handler := cb.Wrap(Subscription{Topic: "topic", ConsumerGroup: "cg"}, func(_ context.Context, _ Entry) HandleResult {
		called = true
		return HandleResult{Disposition: DispositionAck}
	})

	res := handler(context.Background(), Entry{ID: "evt-claim-retry"})
	assert.True(t, called)
	assert.Equal(t, DispositionAck, res.Disposition)
	assert.Same(t, receipt, res.Receipt)

	claimer.mu.Lock()
	defer claimer.mu.Unlock()
	assert.Equal(t, 3, claimer.callCount)
}

func TestConsumerBase_Wrap_ClaimError_FailClosed_ExhaustedRequeues(t *testing.T) {
	claimer := &fakeClaimer{err: errors.New("redis down")}

	cb, err := NewConsumerBase(claimer, ConsumerBaseConfig{
		ClaimRetryCount:     2,
		ClaimRetryBaseDelay: time.Millisecond,
	})
	require.NoError(t, err)

	called := false
	handler := cb.Wrap(Subscription{Topic: "topic", ConsumerGroup: "cg"}, func(_ context.Context, _ Entry) HandleResult {
		called = true
		return HandleResult{Disposition: DispositionAck}
	})

	res := handler(context.Background(), Entry{ID: "evt-claim-fail"})
	assert.False(t, called, "handler must not run when claim is exhausted")
	assert.Equal(t, DispositionRequeue, res.Disposition)
	assert.Error(t, res.Err)
}

func TestConsumerBase_Wrap_ClaimError_FailClosed_CtxCancel(t *testing.T) {
	// Signal channel: claimer sends when first called, meaning ConsumerBase is about
	// to enter the claim retry backoff sleep — safe to cancel ctx at that point.
	claimStarted := make(chan struct{}, 1)
	claimer := &signalingClaimer{
		inner:   &fakeClaimer{err: errors.New("redis down")},
		started: claimStarted,
	}

	cb, err := NewConsumerBase(claimer, ConsumerBaseConfig{
		ClaimRetryCount:     5,
		ClaimRetryBaseDelay: 5 * time.Second,
	})
	require.NoError(t, err)

	handler := cb.Wrap(Subscription{Topic: "topic", ConsumerGroup: "cg"}, func(_ context.Context, _ Entry) HandleResult {
		return HandleResult{Disposition: DispositionAck}
	})

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		<-claimStarted
		cancel()
	}()

	start := time.Now()
	res := handler(ctx, Entry{ID: "evt-claim-ctx"})
	elapsed := time.Since(start)

	assert.Equal(t, DispositionRequeue, res.Disposition)
	assert.Less(t, elapsed, time.Second, "ctx cancel must short-circuit claim backoff")
}

func TestConsumerBase_Wrap_ClaimError_FailOpen_ProceedsWithoutReceipt(t *testing.T) {
	claimer := &fakeClaimer{err: errors.New("redis down")}

	cb, err := NewConsumerBase(claimer, ConsumerBaseConfig{
		ClaimPolicy: ClaimPolicyFailOpen,
	})
	require.NoError(t, err)

	called := false
	handler := cb.Wrap(Subscription{Topic: "topic", ConsumerGroup: "cg"}, func(_ context.Context, _ Entry) HandleResult {
		called = true
		return HandleResult{Disposition: DispositionAck}
	})

	res := handler(context.Background(), Entry{ID: "evt-fail-open"})
	assert.True(t, called, "fail-open must invoke handler despite claim failure")
	assert.Equal(t, DispositionAck, res.Disposition)
	assert.Nil(t, res.Receipt, "no receipt when claim failed under fail-open")
}

func TestConsumerBase_Wrap_MaxRetryDelay_CapsClaimBackoff(t *testing.T) {
	claimer := &fakeClaimer{err: errors.New("redis down")}

	cb, err := NewConsumerBase(claimer, ConsumerBaseConfig{
		ClaimRetryCount:     3,
		ClaimRetryBaseDelay: 200 * time.Millisecond,
		MaxRetryDelay:       20 * time.Millisecond, // clamp well below base
	})
	require.NoError(t, err)

	handler := cb.Wrap(Subscription{Topic: "topic", ConsumerGroup: "cg"}, func(_ context.Context, _ Entry) HandleResult {
		return HandleResult{Disposition: DispositionAck}
	})

	start := time.Now()
	_ = handler(context.Background(), Entry{ID: "evt-cap"})
	elapsed := time.Since(start)

	// Without cap: 200ms + 400ms = 600ms. With cap 20ms: total well under 200ms.
	assert.Less(t, elapsed, 300*time.Millisecond, "MaxRetryDelay must cap claim backoff")
}

// --- AsMiddleware ---------------------------------------------------------

func TestConsumerBase_AsMiddleware_AppliesWrap(t *testing.T) {
	receipt := &fakeReceipt{}
	claimer := &fakeClaimer{state: idempotency.ClaimDone, receipt: receipt}

	cb, err := NewConsumerBase(claimer, ConsumerBaseConfig{})
	require.NoError(t, err)

	mw := cb.AsMiddleware()
	require.NotNil(t, mw)

	called := false
	wrapped := mw(Subscription{Topic: "topic", ConsumerGroup: "cg"}, func(_ context.Context, _ Entry) HandleResult {
		called = true
		return HandleResult{Disposition: DispositionAck}
	})

	res := wrapped(context.Background(), Entry{ID: "evt-mw"})
	assert.False(t, called, "ClaimDone should short-circuit the wrapped handler")
	assert.Equal(t, DispositionAck, res.Disposition)
}

// =============================================================================
// Lease renewal tests (Task X6)
// =============================================================================

// TestWrap_LeaseRenewal_ExtendsAtInterval verifies that the lease renewal
// goroutine calls receipt.Extend at each renewal interval while the handler
// is running.
func TestWrap_LeaseRenewal_ExtendsAtInterval(t *testing.T) {
	defer goleak.VerifyNone(t)

	interval := 20 * time.Millisecond
	receipt := &fakeReceipt{}
	claimer := &fakeClaimer{state: idempotency.ClaimAcquired, receipt: receipt}

	cb, err := NewConsumerBase(claimer, ConsumerBaseConfig{
		LeaseTTL:             200 * time.Millisecond,
		LeaseRenewalInterval: interval,
	})
	require.NoError(t, err)

	handlerDone := make(chan struct{})
	handler := cb.Wrap(Subscription{Topic: "topic", ConsumerGroup: "cg"}, func(ctx context.Context, _ Entry) HandleResult {
		// Block for ~3 intervals so renewal fires at least twice.
		select {
		case <-time.After(3 * interval):
		case <-ctx.Done():
		}
		close(handlerDone)
		return HandleResult{Disposition: DispositionAck}
	})

	res := handler(context.Background(), Entry{ID: "evt-renewal"})
	<-handlerDone

	assert.Equal(t, DispositionAck, res.Disposition)
	got := int(receipt.extendCalls.Load())
	assert.GreaterOrEqual(t, got, 2, "Extend should be called at least twice over 3 intervals")
}

// TestWrap_LeaseRenewal_ExtendFailure_CancelsHandler verifies that when
// Extend returns ErrLeaseExpired the handler context is cancelled and the
// result disposition is Requeue.
func TestWrap_LeaseRenewal_ExtendFailure_CancelsHandler(t *testing.T) {
	defer goleak.VerifyNone(t)

	interval := 20 * time.Millisecond
	receipt := &fakeReceipt{}
	// Set extendErr to ErrLeaseExpired on 2nd call.
	callCount := atomic.Int32{}
	receipt.extendErr = nil // default success; we override per-call below

	claimer := &fakeClaimer{state: idempotency.ClaimAcquired, receipt: receipt}

	cb, err := NewConsumerBase(claimer, ConsumerBaseConfig{
		LeaseTTL:             200 * time.Millisecond,
		LeaseRenewalInterval: interval,
		RetryCount:           1,
		RetryBaseDelay:       time.Millisecond,
	})
	require.NoError(t, err)

	ctxCancelSeen := make(chan struct{}, 1)
	handler := cb.Wrap(Subscription{Topic: "topic", ConsumerGroup: "cg"}, func(ctx context.Context, _ Entry) HandleResult {
		// Block until context is cancelled or timeout.
		select {
		case <-ctx.Done():
			ctxCancelSeen <- struct{}{}
			return HandleResult{Disposition: DispositionRequeue, Err: ctx.Err()}
		case <-time.After(5 * time.Second):
			t.Error("handler blocked without ctx cancellation")
			return HandleResult{Disposition: DispositionAck}
		}
	})

	// Override extendErr to fail on 2nd call via a spy receipt.
	spyReceipt := &spyExtendReceipt{
		receipt: receipt,
		failOn:  2,
		err:     idempotency.ErrLeaseExpired,
		calls:   &callCount,
	}
	claimer.receipt = spyReceipt

	res := handler(context.Background(), Entry{ID: "evt-expire"})

	select {
	case <-ctxCancelSeen:
	case <-time.After(3 * time.Second):
		t.Fatal("handler context was not cancelled after Extend failure")
	}

	assert.Equal(t, DispositionRequeue, res.Disposition)
}

// spyExtendReceipt delegates to a fakeReceipt but injects an error on the Nth Extend call.
type spyExtendReceipt struct {
	receipt *fakeReceipt
	failOn  int32
	err     error
	calls   *atomic.Int32
}

func (s *spyExtendReceipt) Commit(ctx context.Context) error  { return s.receipt.Commit(ctx) }
func (s *spyExtendReceipt) Release(ctx context.Context) error { return s.receipt.Release(ctx) }
func (s *spyExtendReceipt) Extend(ctx context.Context, ttl time.Duration) error {
	n := s.calls.Add(1)
	if n >= s.failOn {
		return s.err
	}
	return s.receipt.Extend(ctx, ttl)
}

var _ Receipt = (*spyExtendReceipt)(nil)

// TestConsumerBase_DifferentConsumerGroupsNoCollision verifies that two distinct
// ConsumerGroups processing the same entry.ID each reach ClaimAcquired independently
// — they use different idempotency keys so neither sees ClaimDone from the other.
// This is the critical regression test for PR#180 P0.
func TestConsumerBase_DifferentConsumerGroupsNoCollision(t *testing.T) {
	receipt1 := &fakeReceipt{}
	claimer1 := &fakeClaimer{state: idempotency.ClaimAcquired, receipt: receipt1}
	receipt2 := &fakeReceipt{}
	claimer2 := &fakeClaimer{state: idempotency.ClaimAcquired, receipt: receipt2}

	sub1 := Subscription{Topic: "session.created.v1", ConsumerGroup: "cg-audit-core"}
	sub2 := Subscription{Topic: "session.created.v1", ConsumerGroup: "cg-config-core"}

	cb1, err := NewConsumerBase(claimer1, ConsumerBaseConfig{})
	require.NoError(t, err)
	cb2, err := NewConsumerBase(claimer2, ConsumerBaseConfig{})
	require.NoError(t, err)

	calls1 := 0
	handler1 := cb1.Wrap(sub1, func(_ context.Context, _ Entry) HandleResult {
		calls1++
		return HandleResult{Disposition: DispositionAck}
	})

	calls2 := 0
	handler2 := cb2.Wrap(sub2, func(_ context.Context, _ Entry) HandleResult {
		calls2++
		return HandleResult{Disposition: DispositionAck}
	})

	entry := Entry{ID: "shared-event-id-001"}
	res1 := handler1(context.Background(), entry)
	res2 := handler2(context.Background(), entry)

	assert.Equal(t, DispositionAck, res1.Disposition, "handler1 must reach ClaimAcquired")
	assert.Equal(t, DispositionAck, res2.Disposition, "handler2 must reach ClaimAcquired")
	assert.Equal(t, 1, calls1, "handler1 must be invoked")
	assert.Equal(t, 1, calls2, "handler2 must be invoked — different namespace, no collision")

	// Verify the idempotency keys differ — each claimer was called with its own namespace.
	claimer1.mu.Lock()
	key1 := claimer1.calls[0]
	claimer1.mu.Unlock()

	claimer2.mu.Lock()
	key2 := claimer2.calls[0]
	claimer2.mu.Unlock()

	assert.Equal(t, "cg-audit-core:shared-event-id-001", key1)
	assert.Equal(t, "cg-config-core:shared-event-id-001", key2)
	assert.NotEqual(t, key1, key2, "idempotency keys must differ across ConsumerGroups")
}

// TestWrap_LeaseRenewal_HandlerComplete_StopsGoroutine verifies that when the
// handler completes normally, the lease renewal goroutine exits cleanly (no
// goroutine leak).
func TestWrap_LeaseRenewal_HandlerComplete_StopsGoroutine(t *testing.T) {
	defer goleak.VerifyNone(t)

	receipt := &fakeReceipt{}
	claimer := &fakeClaimer{state: idempotency.ClaimAcquired, receipt: receipt}

	cb, err := NewConsumerBase(claimer, ConsumerBaseConfig{
		LeaseTTL:             1 * time.Second,
		LeaseRenewalInterval: 50 * time.Millisecond,
	})
	require.NoError(t, err)

	handler := cb.Wrap(Subscription{Topic: "topic", ConsumerGroup: "cg"}, func(_ context.Context, _ Entry) HandleResult {
		// Return immediately — renewal goroutine must exit.
		return HandleResult{Disposition: DispositionAck}
	})

	res := handler(context.Background(), Entry{ID: "evt-quick"})
	assert.Equal(t, DispositionAck, res.Disposition)
	// goleak.VerifyNone(t) at defer will catch any leaked goroutines.
}

// TestWrap_LeaseRenewal_DisabledWhenIntervalNegative verifies that setting
// LeaseRenewalInterval to a negative value disables the renewal goroutine:
// Receipt.Extend is never called, no goroutines are leaked, and the handler
// runs to completion normally.
func TestWrap_LeaseRenewal_DisabledWhenIntervalNegative(t *testing.T) {
	defer goleak.VerifyNone(t)

	receipt := &fakeReceipt{}
	claimer := &fakeClaimer{state: idempotency.ClaimAcquired, receipt: receipt}

	cb, err := NewConsumerBase(claimer, ConsumerBaseConfig{
		LeaseTTL:             idempotency.DefaultLeaseTTL,
		LeaseRenewalInterval: -1, // negative disables renewal
	})
	require.NoError(t, err)

	handler := cb.Wrap(Subscription{Topic: "topic", ConsumerGroup: "cg"}, func(_ context.Context, _ Entry) HandleResult {
		return HandleResult{Disposition: DispositionAck}
	})

	res := handler(context.Background(), Entry{ID: "evt-neg-interval"})
	assert.Equal(t, DispositionAck, res.Disposition)
	assert.Equal(t, int32(0), receipt.extendCalls.Load(), "Extend must not be called when interval is negative")
}

// TestWrap_LeaseRenewal_DisabledWhenIntervalZeroAndTTLZero verifies that when
// both LeaseRenewalInterval and LeaseTTL are zero (after defaults applied),
// the handler is still called and returns normally.
func TestWrap_LeaseRenewal_DisabledWhenIntervalZeroAndTTLZero(t *testing.T) {
	defer goleak.VerifyNone(t)

	receipt := &fakeReceipt{}
	claimer := &fakeClaimer{state: idempotency.ClaimAcquired, receipt: receipt}

	// Use a very large interval that will never fire during the test.
	cb, err := NewConsumerBase(claimer, ConsumerBaseConfig{
		LeaseTTL:             idempotency.DefaultLeaseTTL,
		LeaseRenewalInterval: 0, // should default to LeaseTTL/3
	})
	require.NoError(t, err)

	handler := cb.Wrap(Subscription{Topic: "topic", ConsumerGroup: "cg"}, func(_ context.Context, _ Entry) HandleResult {
		return HandleResult{Disposition: DispositionAck}
	})

	res := handler(context.Background(), Entry{ID: "evt-zero"})
	assert.Equal(t, DispositionAck, res.Disposition)
	// With very fast handler, no Extend should have been called.
	assert.Equal(t, int32(0), receipt.extendCalls.Load())
}

// =============================================================================
// Lease-lost hard fence tests (Commit 2)
// =============================================================================

// TestConsumerBase_LeaseLost_ForceRequeue_EvenWhenHandlerReturnsAck verifies
// Layer 1 hard fence: if the lease expires (ErrLeaseExpired during Extend) and
// the handler ignores ctx.Done() returning DispositionAck, runWithRenewal must
// force-downgrade the result to DispositionRequeue.
func TestConsumerBase_LeaseLost_ForceRequeue_EvenWhenHandlerReturnsAck(t *testing.T) {
	defer goleak.VerifyNone(t)

	interval := 20 * time.Millisecond
	callCount := atomic.Int32{}
	baseReceipt := &fakeReceipt{}

	// Fail on 2nd Extend call with ErrLeaseExpired.
	spyR := &spyExtendReceipt{
		receipt: baseReceipt,
		failOn:  2,
		err:     idempotency.ErrLeaseExpired,
		calls:   &callCount,
	}
	claimer := &fakeClaimer{state: idempotency.ClaimAcquired, receipt: spyR}

	cb, err := NewConsumerBase(claimer, ConsumerBaseConfig{
		LeaseTTL:             200 * time.Millisecond,
		LeaseRenewalInterval: interval,
		RetryCount:           1,
		RetryBaseDelay:       time.Millisecond,
	})
	require.NoError(t, err)

	// Handler deliberately ignores ctx.Done() and returns Ack — simulates a
	// stale holder that is not ctx-aware. It blocks for several intervals so
	// the renewal goroutine can fire and detect the expired lease.
	handler := cb.Wrap(Subscription{Topic: "topic", ConsumerGroup: "cg"}, func(ctx context.Context, _ Entry) HandleResult {
		// Block to allow renewal goroutine to fire and set leaseLost.
		// The handler deliberately does NOT check ctx.Done() to simulate a
		// stale handler that ignores cancellation.
		time.Sleep(5 * interval)
		return HandleResult{Disposition: DispositionAck}
	})

	res := handler(context.Background(), Entry{ID: "evt-lease-lost-ack"})

	// The hard fence must downgrade Ack → Requeue.
	assert.Equal(t, DispositionRequeue, res.Disposition,
		"lease-lost hard fence must downgrade DispositionAck to DispositionRequeue")
}

// TestConsumerBase_LeaseLost_HandlerCancellation_StillRequeue verifies that
// when the lease is lost AND the handler is ctx-aware (returns Requeue on
// ctx.Done()), the final result is still Requeue — the same safe path.
func TestConsumerBase_LeaseLost_HandlerCancellation_StillRequeue(t *testing.T) {
	defer goleak.VerifyNone(t)

	interval := 20 * time.Millisecond
	callCount := atomic.Int32{}
	baseReceipt := &fakeReceipt{}

	spyR := &spyExtendReceipt{
		receipt: baseReceipt,
		failOn:  2,
		err:     idempotency.ErrLeaseExpired,
		calls:   &callCount,
	}
	claimer := &fakeClaimer{state: idempotency.ClaimAcquired, receipt: spyR}

	cb, err := NewConsumerBase(claimer, ConsumerBaseConfig{
		LeaseTTL:             200 * time.Millisecond,
		LeaseRenewalInterval: interval,
		RetryCount:           1,
		RetryBaseDelay:       time.Millisecond,
	})
	require.NoError(t, err)

	ctxCancelSeen := make(chan struct{}, 1)
	handler := cb.Wrap(Subscription{Topic: "topic", ConsumerGroup: "cg"}, func(ctx context.Context, _ Entry) HandleResult {
		select {
		case <-ctx.Done():
			ctxCancelSeen <- struct{}{}
			return HandleResult{Disposition: DispositionRequeue, Err: ctx.Err()}
		case <-time.After(5 * time.Second):
			t.Error("handler blocked without ctx cancellation")
			return HandleResult{Disposition: DispositionAck}
		}
	})

	res := handler(context.Background(), Entry{ID: "evt-lease-lost-requeue"})

	select {
	case <-ctxCancelSeen:
	case <-time.After(3 * time.Second):
		t.Fatal("handler context was not cancelled after ErrLeaseExpired")
	}

	assert.Equal(t, DispositionRequeue, res.Disposition,
		"ctx-aware handler returning Requeue after lease-lost must remain Requeue")
}

// TestConsumerBase_LeaseHeld_NormalAck verifies that the hard fence does NOT
// interfere with the normal path where the lease is always valid and the
// handler returns DispositionAck.
func TestConsumerBase_LeaseHeld_NormalAck(t *testing.T) {
	defer goleak.VerifyNone(t)

	receipt := &fakeReceipt{} // extendErr defaults to nil → always succeeds
	claimer := &fakeClaimer{state: idempotency.ClaimAcquired, receipt: receipt}

	cb, err := NewConsumerBase(claimer, ConsumerBaseConfig{
		LeaseTTL:             200 * time.Millisecond,
		LeaseRenewalInterval: 20 * time.Millisecond,
		RetryCount:           1,
		RetryBaseDelay:       time.Millisecond,
	})
	require.NoError(t, err)

	handler := cb.Wrap(Subscription{Topic: "topic", ConsumerGroup: "cg"}, func(_ context.Context, _ Entry) HandleResult {
		return HandleResult{Disposition: DispositionAck}
	})

	res := handler(context.Background(), Entry{ID: "evt-normal-ack"})
	assert.Equal(t, DispositionAck, res.Disposition,
		"hard fence must not downgrade Ack when lease is always held")
	assert.Same(t, receipt, res.Receipt,
		"receipt must be threaded through on normal Ack path")
}
