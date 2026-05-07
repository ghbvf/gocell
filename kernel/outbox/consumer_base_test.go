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

	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/idempotency"
	"github.com/ghbvf/gocell/pkg/testutil/testtime"
)

// Non-table duration constants used in this file.
const (
	// testIdempotencyTTL48h is the explicit TTL used in SetDefaults positive-value tests.
	testIdempotencyTTL48h = 48 * time.Hour

	// testExponentialDelay3200ms is the expected delay for attempt=5 with base=100ms.
	testExponentialDelay3200ms = 3200 * time.Millisecond

	// testExpDelay8s is the expected delay for attempt=3 with base=1s (8× base).
	testExpDelay8s = 8 * time.Second

	// testExpDelay100s is a large base used to verify maxDelay clamping.
	testExpDelay100s = 100 * time.Second

	// disableLeaseRenewal is the sentinel LeaseRenewalInterval that disables
	// the lease renewal goroutine (any negative duration).
	disableLeaseRenewal time.Duration = -1

	// renewalIntervalMultiplier3 is the factor used to block a handler for
	// 3 renewal intervals in lease-renewal tests.
	renewalIntervalMultiplier3 = 3

	// renewalIntervalMultiplier5 is the factor used to block a handler for
	// 5 renewal intervals in lease-lost hard-fence tests.
	renewalIntervalMultiplier5 = 5
)

// ConsumerBase lives in kernel/outbox so tests covering its behavior must
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

var _ idempotency.Receipt = (*fakeReceipt)(nil)

type fakeClaimer struct {
	mu      sync.Mutex
	state   idempotency.ClaimState
	receipt idempotency.Receipt
	err     error
	calls   []string
}

func (c *fakeClaimer) Claim(_ context.Context, key string, _, _ time.Duration) (idempotency.ClaimState, idempotency.Receipt, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls = append(c.calls, key)
	return c.state, c.receipt, c.err
}

var _ idempotency.Claimer = (*fakeClaimer)(nil)

func testConsumerBase(t *testing.T) *ConsumerBase {
	t.Helper()
	cb, err := NewConsumerBase(
		&fakeClaimer{state: idempotency.ClaimAcquired, receipt: &fakeReceipt{}},
		ConsumerBaseConfig{LeaseRenewalInterval: disableLeaseRenewal},
		clock.Real(),
	)
	require.NoError(t, err)
	return cb
}

// signalingClaimer wraps an inner Claimer and sends on the started channel
// the first time Claim is invoked. Used to replace time.Sleep startup synchronization
// in tests that need to cancel ctx after the claimer has been called.
type signalingClaimer struct {
	inner   idempotency.Claimer
	started chan<- struct{}
	once    sync.Once
}

func (s *signalingClaimer) Claim(
	ctx context.Context, key string, leaseTTL, renewInterval time.Duration,
) (idempotency.ClaimState, idempotency.Receipt, error) {
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
	receipt idempotency.Receipt
	err     error
}

// sequenceClaimer returns the next queued outcome on each Claim call; once
// exhausted it keeps returning the last outcome.
type sequenceClaimer struct {
	mu        sync.Mutex
	outcomes  []claimOutcome
	callCount int
}

func (c *sequenceClaimer) Claim(_ context.Context, _ string, _, _ time.Duration) (idempotency.ClaimState, idempotency.Receipt, error) {
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
	assert.Equal(t, testtime.D30s, cfg.MaxRetryDelay)
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
	assert.Equal(t, testtime.D30s, cfg.MaxRetryDelay)
}

func TestConsumerBaseConfig_SetDefaults_PositiveValuesPreserved(t *testing.T) {
	cfg := ConsumerBaseConfig{
		RetryCount:          7,
		RetryBaseDelay:      testtime.D500ms,
		IdempotencyTTL:      testIdempotencyTTL48h,
		LeaseTTL:            testtime.D10min,
		ClaimRetryCount:     2,
		ClaimRetryBaseDelay: testtime.D250ms,
		MaxRetryDelay:       testtime.D5s,
	}
	cfg.SetDefaults()
	assert.Equal(t, 7, cfg.RetryCount)
	assert.Equal(t, testtime.D500ms, cfg.RetryBaseDelay)
	assert.Equal(t, testIdempotencyTTL48h, cfg.IdempotencyTTL)
	assert.Equal(t, testtime.D10min, cfg.LeaseTTL)
	assert.Equal(t, 2, cfg.ClaimRetryCount)
	assert.Equal(t, testtime.D250ms, cfg.ClaimRetryBaseDelay)
	assert.Equal(t, testtime.D5s, cfg.MaxRetryDelay)
}

// --- exponentialDelay / ExponentialDelay -----------------------------------

// TestExponentialDelay_PublicAPI verifies the exported ExponentialDelay
// function that adapters should use instead of maintaining their own copies.
func TestExponentialDelay_PublicAPI(t *testing.T) {
	base := testtime.D100ms
	maxDelay := testtime.D5s
	cases := []struct {
		name     string
		base     time.Duration
		maxDelay time.Duration
		attempt  int
		want     time.Duration
	}{
		{"zero_base_returns_zero", 0, maxDelay, 3, 0},
		{"attempt_0_equals_base", base, maxDelay, 0, base},
		{"attempt_1_double", base, maxDelay, 1, testtime.D200ms},
		{"attempt_5_capped_by_base_shift", base, maxDelay, 5, testExponentialDelay3200ms},
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
		{name: "attempt 0 returns base", base: time.Second, maxDelay: testtime.D30s, attempt: 0, want: time.Second},
		{name: "attempt 1 doubles", base: time.Second, maxDelay: testtime.D30s, attempt: 1, want: testtime.D2s},
		{name: "attempt 3 is 8x", base: time.Second, maxDelay: testtime.D30s, attempt: 3, want: testExpDelay8s},
		{name: "attempt capped at maxDelay", base: time.Second, maxDelay: testtime.D30s, attempt: 10, want: testtime.D30s},
		{name: "large attempt overflow guard", base: time.Second, maxDelay: testtime.D30s, attempt: 100, want: testtime.D30s},
		{name: "zero base returns 0", base: 0, maxDelay: testtime.D30s, attempt: 5, want: 0},
		{name: "negative base returns 0", base: -time.Second, maxDelay: testtime.D30s, attempt: 3, want: 0},
		{name: "base larger than max returns max", base: testExpDelay100s, maxDelay: testtime.D30s, attempt: 0, want: testtime.D30s},
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
	assert.Equal(t, testtime.D30s, ExponentialDelay(base, testtime.D30s, maxSafeShift))
	assert.Equal(t, testtime.D30s, ExponentialDelay(base, testtime.D30s, maxSafeShift+1))
}

// --- NewConsumerBase -------------------------------------------------------

func TestNewConsumerBase_InvalidClaimPolicy(t *testing.T) {
	_, err := NewConsumerBase(&fakeClaimer{}, ConsumerBaseConfig{
		ClaimPolicy: ClaimPolicy(99),
	}, clock.Real())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid ClaimPolicy")
}

func TestNewConsumerBase_DefaultClaimPolicyFailClosed(t *testing.T) {
	cb, err := NewConsumerBase(&fakeClaimer{}, ConsumerBaseConfig{}, clock.Real())
	require.NoError(t, err)
	assert.Equal(t, ClaimPolicyFailClosed, cb.config.ClaimPolicy)
}

func TestNewConsumerBase_ExplicitFailOpenPreserved(t *testing.T) {
	cb, err := NewConsumerBase(&fakeClaimer{}, ConsumerBaseConfig{
		ClaimPolicy: ClaimPolicyFailOpen,
	}, clock.Real())
	require.NoError(t, err)
	assert.Equal(t, ClaimPolicyFailOpen, cb.config.ClaimPolicy)
}

// --- IsConstructed sentinel (N8 K#12 FU) -----------------------------------
// PR-V1-OUTBOX-FU-CLOSURE (b): a zero-value `&ConsumerBase{}` literal sets
// claimer=nil and ConsumerBaseConfig zero, which when fed into
// runtime/bootstrap.WithConsumerBase passes a non-nil pointer check yet emits
// ClaimRetryCount=0 → ClaimAcquired+nil receipt → retryLoop with 0 iterations
// → silent Reject/DLX. The IsConstructed sentinel makes "came from
// NewConsumerBase" the single source of truth so phase6 wiring rejects the
// literal even when it is not nil.

func TestNewConsumerBase_IsConstructed_True(t *testing.T) {
	cb, err := NewConsumerBase(&fakeClaimer{}, ConsumerBaseConfig{}, clock.Real())
	require.NoError(t, err)
	require.NotNil(t, cb)
	assert.True(t, cb.IsConstructed(),
		"NewConsumerBase must return a value whose IsConstructed() is true; "+
			"otherwise phase6 wiring cannot distinguish a constructed value from a literal")
}

func TestZeroValueConsumerBaseLiteral_IsConstructed_False(t *testing.T) {
	literal := &ConsumerBase{}
	assert.False(t, literal.IsConstructed(),
		"`&ConsumerBase{}` literal must report IsConstructed()==false so wiring can refuse it")
}

func TestNilConsumerBase_IsConstructed_False(t *testing.T) {
	var cb *ConsumerBase
	assert.False(t, cb.IsConstructed(),
		"typed-nil *ConsumerBase must report IsConstructed()==false (nil-receiver safe)")
}

// --- Wrap: happy paths -----------------------------------------------------

func TestConsumerBase_Wrap_ClaimAcquired_Ack_ThreadsReceipt(t *testing.T) {
	receipt := &fakeReceipt{}
	claimer := &fakeClaimer{state: idempotency.ClaimAcquired, receipt: receipt}

	cb, err := NewConsumerBase(claimer, ConsumerBaseConfig{}, clock.Real())
	require.NoError(t, err)

	called := false
	handler := cb.Wrap(Subscription{Topic: "topic", ConsumerGroup: "cg"}, func(_ context.Context, _ Entry) HandleResult {
		called = true
		return Ack()
	})

	res, settlement := handler(context.Background(), Entry{ID: "evt-1"})

	assert.True(t, called)
	assert.Equal(t, DispositionAck, res.Disposition)
	assert.Same(t, receipt, settlement)

	receipt.mu.Lock()
	defer receipt.mu.Unlock()
	assert.False(t, receipt.commitCalled, "ConsumerBase must not Commit — that's the delivery loop's job")
	assert.False(t, receipt.releaseCalled)
}

func TestConsumerBase_Wrap_ClaimDone_SkipsHandler(t *testing.T) {
	claimer := &fakeClaimer{state: idempotency.ClaimDone}

	cb, err := NewConsumerBase(claimer, ConsumerBaseConfig{}, clock.Real())
	require.NoError(t, err)

	called := false
	handler := cb.Wrap(Subscription{Topic: "topic", ConsumerGroup: "cg"}, func(_ context.Context, _ Entry) HandleResult {
		called = true
		return Ack()
	})

	res, settlement := handler(context.Background(), Entry{ID: "evt-dup"})
	assert.False(t, called, "ClaimDone must skip the handler")
	assert.Equal(t, DispositionAck, res.Disposition)
	assert.Nil(t, settlement)
}

func TestConsumerBase_Wrap_ClaimBusy_Requeues(t *testing.T) {
	claimer := &fakeClaimer{state: idempotency.ClaimBusy}

	cb, err := NewConsumerBase(claimer, ConsumerBaseConfig{
		RetryBaseDelay: testtime.FastPoll, // short backoff for test
	}, clock.Real())
	require.NoError(t, err)

	called := false
	handler := cb.Wrap(Subscription{Topic: "topic", ConsumerGroup: "cg"}, func(_ context.Context, _ Entry) HandleResult {
		called = true
		return Ack()
	})

	res, settlement := handler(context.Background(), Entry{ID: "evt-busy"})
	assert.False(t, called)
	assert.Equal(t, DispositionRequeue, res.Disposition)
	assert.Nil(t, settlement, "ClaimBusy must return nil settlement")
}

// --- Wrap: retry loop ------------------------------------------------------

func TestConsumerBase_Wrap_TransientError_RetriesUntilAck(t *testing.T) {
	receipt := &fakeReceipt{}
	claimer := &fakeClaimer{state: idempotency.ClaimAcquired, receipt: receipt}

	cb, err := NewConsumerBase(claimer, ConsumerBaseConfig{
		RetryCount:     3,
		RetryBaseDelay: time.Millisecond,
	}, clock.Real())
	require.NoError(t, err)

	attempts := 0
	handler := cb.Wrap(Subscription{Topic: "topic", ConsumerGroup: "cg"}, func(_ context.Context, _ Entry) HandleResult {
		attempts++
		if attempts == 1 {
			return Requeue(errors.New("transient"))
		}
		return Ack()
	})

	res, settlement := handler(context.Background(), Entry{ID: "evt-retry"})
	assert.Equal(t, 2, attempts)
	assert.Equal(t, DispositionAck, res.Disposition)
	assert.Same(t, receipt, settlement)
}

func TestConsumerBase_Wrap_RetryBudgetExhausted_RejectsToDLX(t *testing.T) {
	receipt := &fakeReceipt{}
	claimer := &fakeClaimer{state: idempotency.ClaimAcquired, receipt: receipt}

	cb, err := NewConsumerBase(claimer, ConsumerBaseConfig{
		RetryCount:     2,
		RetryBaseDelay: time.Millisecond,
	}, clock.Real())
	require.NoError(t, err)

	attempts := 0
	handler := cb.Wrap(Subscription{Topic: "topic", ConsumerGroup: "cg"}, func(_ context.Context, _ Entry) HandleResult {
		attempts++
		return Requeue(errors.New("always fail"))
	})

	res, settlement := handler(context.Background(), Entry{ID: "evt-exhaust"})
	assert.Equal(t, 2, attempts)
	assert.Equal(t, DispositionReject, res.Disposition)
	assert.Same(t, receipt, settlement)
}

func TestConsumerBase_Wrap_ExplicitReject_NoRetry(t *testing.T) {
	receipt := &fakeReceipt{}
	claimer := &fakeClaimer{state: idempotency.ClaimAcquired, receipt: receipt}

	cb, err := NewConsumerBase(claimer, ConsumerBaseConfig{
		RetryCount:     5,
		RetryBaseDelay: time.Millisecond,
	}, clock.Real())
	require.NoError(t, err)

	attempts := 0
	handler := cb.Wrap(Subscription{Topic: "topic", ConsumerGroup: "cg"}, func(_ context.Context, _ Entry) HandleResult {
		attempts++
		return Reject(errors.New("bad payload"))
	})

	res, settlement := handler(context.Background(), Entry{ID: "evt-explicit-reject"})
	assert.Equal(t, 1, attempts, "DispositionReject must skip retries")
	assert.Equal(t, DispositionReject, res.Disposition)
	assert.Same(t, receipt, settlement)
}

// TestConsumerBase_Wrap_WrappedPermanentErrorInRequeue_NotEscalated locks the
// Q2 decision (029 #03 ADR Decision 4): when a handler returns Requeue with a
// PermanentError-wrapped Err, ConsumerBase MUST keep the Disposition as
// Requeue and exhaust the retry budget — it does not implicitly upgrade to
// Reject. Handlers must be explicit about routing to DLX by returning
// DispositionReject themselves. This removes the legacy fallback behavior
// originally needed by WrapLegacyHandler (now deleted).
func TestConsumerBase_Wrap_WrappedPermanentErrorInRequeue_NotEscalated(t *testing.T) {
	receipt := &fakeReceipt{}
	claimer := &fakeClaimer{state: idempotency.ClaimAcquired, receipt: receipt}

	cb, err := NewConsumerBase(claimer, ConsumerBaseConfig{
		RetryCount:     3,
		RetryBaseDelay: time.Millisecond,
	}, clock.Real())
	require.NoError(t, err)

	attempts := 0
	handler := cb.Wrap(Subscription{Topic: "topic", ConsumerGroup: "cg"}, func(_ context.Context, _ Entry) HandleResult {
		attempts++
		return Requeue(fmt.Errorf("ctx: %w", NewPermanentError(errors.New("unmarshal"))))
	})

	res, _ := handler(context.Background(), Entry{ID: "evt-perm"})
	assert.Equal(t, 3, attempts, "PermanentError wrapped in Requeue must NOT short-circuit; budget must exhaust")
	assert.Equal(t, DispositionReject, res.Disposition,
		"after retry budget exhaustion, ConsumerBase rejects to DLX (this is the budget-exhaust path, not a PermErr upgrade)")
}

func TestConsumerBase_Wrap_CtxCancelled_DuringRetry_Requeues(t *testing.T) {
	claimer := &fakeClaimer{state: idempotency.ClaimAcquired}

	cb, err := NewConsumerBase(claimer, ConsumerBaseConfig{
		RetryCount:     5,
		RetryBaseDelay: testtime.D5s, // long enough that ctx cancel wins
	}, clock.Real())
	require.NoError(t, err)

	// Signal channel: handler sends when it has been called, meaning ConsumerBase
	// is about to enter the retry backoff sleep — safe to cancel ctx at that point.
	started := make(chan struct{}, 1)
	handler := cb.Wrap(Subscription{Topic: "topic", ConsumerGroup: "cg"}, func(_ context.Context, _ Entry) HandleResult {
		select {
		case started <- struct{}{}:
		default:
		}
		return Requeue(errors.New("transient"))
	})

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		<-started
		cancel()
	}()

	start := time.Now()
	res, _ := handler(ctx, Entry{ID: "evt-ctx"})
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
	}, clock.Real())
	require.NoError(t, err)

	called := false
	handler := cb.Wrap(Subscription{Topic: "topic", ConsumerGroup: "cg"}, func(_ context.Context, _ Entry) HandleResult {
		called = true
		return Ack()
	})

	res, settlement := handler(context.Background(), Entry{ID: "evt-claim-retry"})
	assert.True(t, called)
	assert.Equal(t, DispositionAck, res.Disposition)
	assert.Same(t, receipt, settlement)

	claimer.mu.Lock()
	defer claimer.mu.Unlock()
	assert.Equal(t, 3, claimer.callCount)
}

func TestConsumerBase_Wrap_ClaimError_FailClosed_ExhaustedRequeues(t *testing.T) {
	claimer := &fakeClaimer{err: errors.New("redis down")}

	cb, err := NewConsumerBase(claimer, ConsumerBaseConfig{
		ClaimRetryCount:     2,
		ClaimRetryBaseDelay: time.Millisecond,
	}, clock.Real())
	require.NoError(t, err)

	called := false
	handler := cb.Wrap(Subscription{Topic: "topic", ConsumerGroup: "cg"}, func(_ context.Context, _ Entry) HandleResult {
		called = true
		return Ack()
	})

	res, settlement := handler(context.Background(), Entry{ID: "evt-claim-fail"})
	assert.False(t, called, "handler must not run when claim is exhausted")
	assert.Equal(t, DispositionRequeue, res.Disposition)
	assert.Error(t, res.Err)
	assert.Nil(t, settlement, "fail-closed claim exhausted must return nil settlement")
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
		ClaimRetryBaseDelay: testtime.D5s,
	}, clock.Real())
	require.NoError(t, err)

	handler := cb.Wrap(Subscription{Topic: "topic", ConsumerGroup: "cg"}, func(_ context.Context, _ Entry) HandleResult {
		return Ack()
	})

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		<-claimStarted
		cancel()
	}()

	start := time.Now()
	res, settlement := handler(ctx, Entry{ID: "evt-claim-ctx"})
	elapsed := time.Since(start)

	assert.Equal(t, DispositionRequeue, res.Disposition)
	assert.Less(t, elapsed, time.Second, "ctx cancel must short-circuit claim backoff")
	assert.Nil(t, settlement, "ctx-canceled claim must return nil settlement")
}

func TestConsumerBase_Wrap_ClaimError_FailOpen_ProceedsWithoutReceipt(t *testing.T) {
	claimer := &fakeClaimer{err: errors.New("redis down")}

	cb, err := NewConsumerBase(claimer, ConsumerBaseConfig{
		ClaimPolicy: ClaimPolicyFailOpen,
	}, clock.Real())
	require.NoError(t, err)

	called := false
	handler := cb.Wrap(Subscription{Topic: "topic", ConsumerGroup: "cg"}, func(_ context.Context, _ Entry) HandleResult {
		called = true
		return Ack()
	})

	res, settlement := handler(context.Background(), Entry{ID: "evt-fail-open"})
	assert.True(t, called, "fail-open must invoke handler despite claim failure")
	assert.Equal(t, DispositionAck, res.Disposition)
	assert.Nil(t, settlement, "no settlement when claim failed under fail-open")
}

func TestConsumerBase_Wrap_MaxRetryDelay_CapsClaimBackoff(t *testing.T) {
	claimer := &fakeClaimer{err: errors.New("redis down")}

	cb, err := NewConsumerBase(claimer, ConsumerBaseConfig{
		ClaimRetryCount:     3,
		ClaimRetryBaseDelay: testtime.D200ms,
		MaxRetryDelay:       testtime.D20ms, // clamp well below base
	}, clock.Real())
	require.NoError(t, err)

	handler := cb.Wrap(Subscription{Topic: "topic", ConsumerGroup: "cg"}, func(_ context.Context, _ Entry) HandleResult {
		return Ack()
	})

	start := time.Now()
	_, _ = handler(context.Background(), Entry{ID: "evt-cap"})
	elapsed := time.Since(start)

	// Without cap: 200ms + 400ms = 600ms. With cap 20ms: total well under 200ms.
	assert.Less(t, elapsed, testtime.D300ms, "MaxRetryDelay must cap claim backoff")
}

// AsMiddleware was removed in K#12 PR-V1-OUTBOX-RECEIPT-EXTRACT second pass.
// The equivalent behavior is now provided by ConsumerBase.Wrap, which is tested
// in the Wrap tests above, and by SubscriberWithMiddleware.SubscribeEntry with a
// non-nil ConsumerBase field, which is tested in conformance.go.

// =============================================================================
// Lease renewal tests (Task X6)
// =============================================================================

// TestWrap_LeaseRenewal_ExtendsAtInterval verifies that the lease renewal
// goroutine calls receipt.Extend at each renewal interval while the handler
// is running.
func TestWrap_LeaseRenewal_ExtendsAtInterval(t *testing.T) {
	defer goleak.VerifyNone(t)

	interval := testtime.D20ms
	receipt := &fakeReceipt{}
	claimer := &fakeClaimer{state: idempotency.ClaimAcquired, receipt: receipt}

	cb, err := NewConsumerBase(claimer, ConsumerBaseConfig{
		LeaseTTL:             testtime.D200ms,
		LeaseRenewalInterval: interval,
	}, clock.Real())
	require.NoError(t, err)

	handlerDone := make(chan struct{})
	handler := cb.Wrap(Subscription{Topic: "topic", ConsumerGroup: "cg"}, func(ctx context.Context, _ Entry) HandleResult {
		// Block for ~3 intervals so renewal fires at least twice.
		select {
		case <-time.After(renewalIntervalMultiplier3 * interval):
		case <-ctx.Done():
		}
		close(handlerDone)
		return Ack()
	})

	res, _ := handler(context.Background(), Entry{ID: "evt-renewal"})
	<-handlerDone

	assert.Equal(t, DispositionAck, res.Disposition)
	got := int(receipt.extendCalls.Load())
	assert.GreaterOrEqual(t, got, 2, "Extend should be called at least twice over 3 intervals")
}

// TestWrap_LeaseRenewal_ExtendFailure_CancelsHandler verifies that when
// Extend returns ErrLeaseExpired the handler context is canceled and the
// result disposition is Requeue.
func TestWrap_LeaseRenewal_ExtendFailure_CancelsHandler(t *testing.T) {
	defer goleak.VerifyNone(t)

	interval := testtime.D20ms
	receipt := &fakeReceipt{}
	// Set extendErr to ErrLeaseExpired on 2nd call.
	callCount := atomic.Int32{}
	receipt.extendErr = nil // default success; we override per-call below

	claimer := &fakeClaimer{state: idempotency.ClaimAcquired, receipt: receipt}

	cb, err := NewConsumerBase(claimer, ConsumerBaseConfig{
		LeaseTTL:             testtime.D200ms,
		LeaseRenewalInterval: interval,
		RetryCount:           1,
		RetryBaseDelay:       time.Millisecond,
	}, clock.Real())
	require.NoError(t, err)

	ctxCancelSeen := make(chan struct{}, 1)
	handler := cb.Wrap(Subscription{Topic: "topic", ConsumerGroup: "cg"}, func(ctx context.Context, _ Entry) HandleResult {
		// Block until context is canceled or timeout.
		select {
		case <-ctx.Done():
			ctxCancelSeen <- struct{}{}
			return Requeue(ctx.Err())
		case <-time.After(testtime.D5s):
			t.Error("handler blocked without ctx cancellation")
			return Ack()
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

	res, _ := handler(context.Background(), Entry{ID: "evt-expire"})

	select {
	case <-ctxCancelSeen:
	case <-time.After(testtime.EventuallyDefault):
		t.Fatal("handler context was not canceled after Extend failure")
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

var _ idempotency.Receipt = (*spyExtendReceipt)(nil)

// TestConsumerBase_DifferentConsumerGroupsNoCollision verifies that two distinct
// ConsumerGroups processing the same entry.ID each reach ClaimAcquired independently
// — they use different idempotency keys so neither sees ClaimDone from the other.
// This is the critical regression test for PR#180 P0.
func TestConsumerBase_DifferentConsumerGroupsNoCollision(t *testing.T) {
	receipt1 := &fakeReceipt{}
	claimer1 := &fakeClaimer{state: idempotency.ClaimAcquired, receipt: receipt1}
	receipt2 := &fakeReceipt{}
	claimer2 := &fakeClaimer{state: idempotency.ClaimAcquired, receipt: receipt2}

	sub1 := Subscription{Topic: "session.created.v1", ConsumerGroup: "cg-auditcore"}
	sub2 := Subscription{Topic: "session.created.v1", ConsumerGroup: "cg-configcore"}

	cb1, err := NewConsumerBase(claimer1, ConsumerBaseConfig{}, clock.Real())
	require.NoError(t, err)
	cb2, err := NewConsumerBase(claimer2, ConsumerBaseConfig{}, clock.Real())
	require.NoError(t, err)

	calls1 := 0
	handler1 := cb1.Wrap(sub1, func(_ context.Context, _ Entry) HandleResult {
		calls1++
		return Ack()
	})

	calls2 := 0
	handler2 := cb2.Wrap(sub2, func(_ context.Context, _ Entry) HandleResult {
		calls2++
		return Ack()
	})

	entry := Entry{ID: "shared-event-id-001"}
	res1, _ := handler1(context.Background(), entry)
	res2, _ := handler2(context.Background(), entry)

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

	assert.Equal(t, "cg-auditcore:shared-event-id-001", key1)
	assert.Equal(t, "cg-configcore:shared-event-id-001", key2)
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
		LeaseTTL:             testtime.D1s,
		LeaseRenewalInterval: testtime.MediumPoll,
	}, clock.Real())
	require.NoError(t, err)

	handler := cb.Wrap(Subscription{Topic: "topic", ConsumerGroup: "cg"}, func(_ context.Context, _ Entry) HandleResult {
		// Return immediately — renewal goroutine must exit.
		return Ack()
	})

	res, _ := handler(context.Background(), Entry{ID: "evt-quick"})
	assert.Equal(t, DispositionAck, res.Disposition)
	// goleak.VerifyNone(t) at defer will catch any leaked goroutines.
}

// TestWrap_LeaseRenewalLoop_TransientExtendError_LogsWarnAndContinues covers
// the transient-extend-error warn branch inside leaseRenewalLoop:
// non-ErrLeaseExpired Extend errors must log a warning and continue ticking
// rather than canceling the handler context.
func TestWrap_LeaseRenewalLoop_TransientExtendError_LogsWarnAndContinues(t *testing.T) {
	defer goleak.VerifyNone(t)

	interval := testtime.D20ms
	transientErr := errors.New("extend: redis timeout")

	// fakeReceipt with extendErr set returns the transient error on every
	// Extend call. This is NOT ErrLeaseExpired, so the renewal loop must
	// stay alive and NOT cancel the handler context.
	receipt := &fakeReceipt{extendErr: transientErr}
	claimer := &fakeClaimer{state: idempotency.ClaimAcquired, receipt: receipt}

	cb, err := NewConsumerBase(claimer, ConsumerBaseConfig{
		LeaseTTL:             testtime.D200ms,
		LeaseRenewalInterval: interval,
		RetryCount:           1,
		RetryBaseDelay:       time.Millisecond,
	}, clock.Real())
	require.NoError(t, err)

	// handlerDone is closed when the handler returns so the test can assert
	// that the handler ran to completion (ctx was NOT canceled).
	handlerDone := make(chan struct{})
	handler := cb.Wrap(Subscription{Topic: "topic", ConsumerGroup: "cg"}, func(ctx context.Context, _ Entry) HandleResult {
		// Block for 3 intervals so renewal fires at least twice with the
		// transient error; verify ctx stays live throughout.
		select {
		case <-time.After(renewalIntervalMultiplier3 * interval):
			// normal exit — ctx was NOT canceled by transient extend error
		case <-ctx.Done():
			t.Error("handler context was canceled on transient extend error — must not happen")
		}
		close(handlerDone)
		return Ack()
	})

	res, _ := handler(context.Background(), Entry{ID: "evt-transient-extend"})
	<-handlerDone

	// Handler must complete with Ack — transient extend failure must not affect outcome.
	assert.Equal(t, DispositionAck, res.Disposition)
	// Extend must have been called at least once (hitting the warn branch).
	assert.GreaterOrEqual(t, int(receipt.extendCalls.Load()), 1,
		"Extend must be called at least once to exercise the transient warn branch")
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
		LeaseRenewalInterval: disableLeaseRenewal, // negative disables renewal
	}, clock.Real())
	require.NoError(t, err)

	handler := cb.Wrap(Subscription{Topic: "topic", ConsumerGroup: "cg"}, func(_ context.Context, _ Entry) HandleResult {
		return Ack()
	})

	res, _ := handler(context.Background(), Entry{ID: "evt-neg-interval"})
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
	}, clock.Real())
	require.NoError(t, err)

	handler := cb.Wrap(Subscription{Topic: "topic", ConsumerGroup: "cg"}, func(_ context.Context, _ Entry) HandleResult {
		return Ack()
	})

	res, _ := handler(context.Background(), Entry{ID: "evt-zero"})
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

	interval := testtime.D20ms
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
		LeaseTTL:             testtime.D200ms,
		LeaseRenewalInterval: interval,
		RetryCount:           1,
		RetryBaseDelay:       time.Millisecond,
	}, clock.Real())
	require.NoError(t, err)

	// Handler deliberately ignores ctx.Done() and returns Ack — simulates a
	// stale holder that is not ctx-aware. It blocks for several intervals so
	// the renewal goroutine can fire and detect the expired lease.
	handler := cb.Wrap(Subscription{Topic: "topic", ConsumerGroup: "cg"}, func(ctx context.Context, _ Entry) HandleResult {
		// Block to allow renewal goroutine to fire and set leaseLost.
		// The handler deliberately does NOT check ctx.Done() to simulate a
		// stale handler that ignores cancellation.
		time.Sleep(renewalIntervalMultiplier5 * interval) //archtest:allow:test-sleep Renew extends TTL — polling defeats test
		return Ack()
	})

	res, _ := handler(context.Background(), Entry{ID: "evt-lease-lost-ack"})

	// The hard fence must downgrade Ack → Requeue.
	assert.Equal(t, DispositionRequeue, res.Disposition,
		"lease-lost hard fence must downgrade DispositionAck to DispositionRequeue")
}

// TestConsumerBase_LeaseLost_HandlerCancellation_StillRequeue verifies that
// when the lease is lost AND the handler is ctx-aware (returns Requeue on
// ctx.Done()), the final result is still Requeue — the same safe path.
func TestConsumerBase_LeaseLost_HandlerCancellation_StillRequeue(t *testing.T) {
	defer goleak.VerifyNone(t)

	interval := testtime.D20ms
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
		LeaseTTL:             testtime.D200ms,
		LeaseRenewalInterval: interval,
		RetryCount:           1,
		RetryBaseDelay:       time.Millisecond,
	}, clock.Real())
	require.NoError(t, err)

	ctxCancelSeen := make(chan struct{}, 1)
	handler := cb.Wrap(Subscription{Topic: "topic", ConsumerGroup: "cg"}, func(ctx context.Context, _ Entry) HandleResult {
		select {
		case <-ctx.Done():
			ctxCancelSeen <- struct{}{}
			return Requeue(ctx.Err())
		case <-time.After(testtime.D5s):
			t.Error("handler blocked without ctx cancellation")
			return Ack()
		}
	})

	res, _ := handler(context.Background(), Entry{ID: "evt-lease-lost-requeue"})

	select {
	case <-ctxCancelSeen:
	case <-time.After(testtime.EventuallyDefault):
		t.Fatal("handler context was not canceled after ErrLeaseExpired")
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
		LeaseTTL:             testtime.D200ms,
		LeaseRenewalInterval: testtime.D20ms,
		RetryCount:           1,
		RetryBaseDelay:       time.Millisecond,
	}, clock.Real())
	require.NoError(t, err)

	handler := cb.Wrap(Subscription{Topic: "topic", ConsumerGroup: "cg"}, func(_ context.Context, _ Entry) HandleResult {
		return Ack()
	})

	res, settlement := handler(context.Background(), Entry{ID: "evt-normal-ack"})
	assert.Equal(t, DispositionAck, res.Disposition,
		"hard fence must not downgrade Ack when lease is always held")
	assert.Same(t, receipt, settlement,
		"settlement must be threaded through on normal Ack path")
}

// =============================================================================
// SettlementObservers transparency tests (Wave 4 review finding #1 + #2)
// =============================================================================

// TestConsumerBase_LeaseLostPath_PreservesSettlementObservers guards finding #1:
// when runWithRenewal detects leaseLost and force-downgrades DispositionAck →
// DispositionRequeue, the handler's SettlementObservers must be preserved in
// the returned HandleResult. Previously the hard-fence code path silently
// dropped them.
func TestConsumerBase_LeaseLostPath_PreservesSettlementObservers(t *testing.T) {
	defer goleak.VerifyNone(t)

	interval := testtime.D20ms
	callCount := atomic.Int32{}
	baseReceipt := &fakeReceipt{}

	// Fail on 2nd Extend call with ErrLeaseExpired to trigger leaseLost latch.
	spyR := &spyExtendReceipt{
		receipt: baseReceipt,
		failOn:  2,
		err:     idempotency.ErrLeaseExpired,
		calls:   &callCount,
	}
	claimer := &fakeClaimer{state: idempotency.ClaimAcquired, receipt: spyR}

	cb, err := NewConsumerBase(claimer, ConsumerBaseConfig{
		LeaseTTL:             testtime.D200ms,
		LeaseRenewalInterval: interval,
		RetryCount:           1,
		RetryBaseDelay:       time.Millisecond,
	}, clock.Real())
	require.NoError(t, err)

	// Capture the observer call via SettlementObserverFunc.
	var observerCalled bool
	testObserver := SettlementObserverFunc(func(_ context.Context, _ SettlementObservation) {
		observerCalled = true
	})

	// Handler ignores ctx.Done() (stale holder), returns Ack with an observer.
	handler := cb.Wrap(Subscription{Topic: "topic", ConsumerGroup: "cg"}, func(_ context.Context, _ Entry) HandleResult {
		// Block long enough for the renewal goroutine to fire and set leaseLost.
		time.Sleep(renewalIntervalMultiplier5 * interval) //archtest:allow:test-sleep Renew extends TTL — polling defeats test
		return HandleResult{
			Disposition:         DispositionAck,
			SettlementObservers: []SettlementObserver{testObserver},
		}
	})

	res, _ := handler(context.Background(), Entry{ID: "evt-lease-lost-observers"})

	// Hard fence must downgrade to Requeue and preserve SettlementObservers.
	assert.Equal(t, DispositionRequeue, res.Disposition,
		"leaseLost hard fence must downgrade DispositionAck to DispositionRequeue")
	require.Len(t, res.SettlementObservers, 1,
		"leaseLost downgrade path must preserve handler SettlementObservers")

	// Invoke observer to confirm it is functional.
	res.SettlementObservers[0].ObserveSettlement(context.Background(), SettlementObservation{})
	assert.True(t, observerCalled,
		"preserved SettlementObserver must be callable after leaseLost downgrade")
}

// TestConsumerBase_CtxCancelDuringBackoff_PreservesSettlementObservers guards
// finding #2: when retryLoop aborts via ctx.Done() during waitBackoff, the
// requeueResult must carry SettlementObservers from lastResult so
// business-middleware observers (e.g. ConfigEventMiddleware) are notified on
// graceful shutdown. Previously requeueResult had no observers parameter and
// silently dropped them.
func TestConsumerBase_CtxCancelDuringBackoff_PreservesSettlementObservers(t *testing.T) {
	receipt := &fakeReceipt{}
	claimer := &fakeClaimer{state: idempotency.ClaimAcquired, receipt: receipt}

	cb, err := NewConsumerBase(claimer, ConsumerBaseConfig{
		RetryCount:           3,
		RetryBaseDelay:       testtime.D5s, // long enough that ctx cancel wins
		LeaseRenewalInterval: disableLeaseRenewal,
	}, clock.Real())
	require.NoError(t, err)

	// Capture the observer call via SettlementObserverFunc.
	var observerCalled bool
	testObserver := SettlementObserverFunc(func(_ context.Context, _ SettlementObservation) {
		observerCalled = true
	})

	// Signal channel: handler sends when first called (before backoff sleep).
	started := make(chan struct{}, 1)
	handler := cb.Wrap(Subscription{Topic: "topic", ConsumerGroup: "cg"}, func(_ context.Context, _ Entry) HandleResult {
		select {
		case started <- struct{}{}:
		default:
		}
		return HandleResult{
			Disposition:         DispositionRequeue,
			Err:                 errors.New("transient"),
			SettlementObservers: []SettlementObserver{testObserver},
		}
	})

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		<-started
		cancel()
	}()

	start := time.Now()
	res, _ := handler(ctx, Entry{ID: "evt-ctx-cancel-observers"})
	elapsed := time.Since(start)

	// ctx cancel must abort the backoff quickly.
	assert.Less(t, elapsed, time.Second, "ctx cancel must short-circuit retry backoff")
	assert.Equal(t, DispositionRequeue, res.Disposition,
		"ctx-cancel abort path must return DispositionRequeue")

	// SettlementObservers from lastResult must be preserved.
	require.Len(t, res.SettlementObservers, 1,
		"ctx-cancel abort path must preserve lastResult.SettlementObservers")

	// Invoke observer to confirm it is functional.
	res.SettlementObservers[0].ObserveSettlement(context.Background(), SettlementObservation{})
	assert.True(t, observerCalled,
		"preserved SettlementObserver must be callable after ctx-cancel abort")
}
