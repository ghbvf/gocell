package outbox

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
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

func (c *fakeClaimer) Kind() idempotency.ClaimerKind { return idempotency.ClaimerKindInMemory }

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

func (s *signalingClaimer) Kind() idempotency.ClaimerKind { return s.inner.Kind() }

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

func (c *sequenceClaimer) Kind() idempotency.ClaimerKind { return idempotency.ClaimerKindInMemory }

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

	res, settlement := handler(context.Background(), Entry{id: "evt-1"})

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

	res, settlement := handler(context.Background(), Entry{id: "evt-dup"})
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

	res, settlement := handler(context.Background(), Entry{id: "evt-busy"})
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

	res, settlement := handler(context.Background(), Entry{id: "evt-retry"})
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

	res, settlement := handler(context.Background(), Entry{id: "evt-exhaust"})
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

	res, settlement := handler(context.Background(), Entry{id: "evt-explicit-reject"})
	assert.Equal(t, 1, attempts, "DispositionReject must skip retries")
	assert.Equal(t, DispositionReject, res.Disposition)
	assert.Same(t, receipt, settlement)
}

// TestConsumerBase_Wrap_BrokerDelaySchedule_PassesThroughVerbatim locks the
// #1458 invariant: a subscription carrying a BrokerDelaySchedule delegates retry
// timing/budget/DLX to the transport, so ConsumerBase runs the handler EXACTLY
// ONCE and returns its verdict verbatim — even with RetryCount=3. Crucially a
// transient Requeue must NOT be converted into the retry-exhausted Reject (which
// would dead-letter the entry on the first failure and bypass the delay
// schedule); it must reach the subscriber as a Requeue so the broker/in-memory
// schedule can apply the per-attempt delay.
func TestConsumerBase_Wrap_BrokerDelaySchedule_PassesThroughVerbatim(t *testing.T) {
	tests := []struct {
		name   string
		result HandleResult
		want   Disposition
	}{
		{"requeue passes through, not exhaustion-reject", Requeue(errors.New("transient")), DispositionRequeue},
		{"ack passes through", Ack(), DispositionAck},
		{"reject passes through", Reject(errors.New("permanent")), DispositionReject},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			receipt := &fakeReceipt{}
			claimer := &fakeClaimer{state: idempotency.ClaimAcquired, receipt: receipt}

			cb, err := NewConsumerBase(claimer, ConsumerBaseConfig{
				RetryCount:     3,
				RetryBaseDelay: time.Millisecond,
			}, clock.Real())
			require.NoError(t, err)

			attempts := 0
			sub := Subscription{
				Topic:               "topic",
				ConsumerGroup:       "cg",
				BrokerDelaySchedule: []time.Duration{time.Second, time.Minute},
			}
			handler := cb.Wrap(sub, func(_ context.Context, _ Entry) HandleResult {
				attempts++
				return tt.result
			})

			res, settlement := handler(context.Background(), Entry{id: "evt-broker-delay"})
			assert.Equal(t, 1, attempts, "broker-delay sub must invoke the handler exactly once (transport owns retries)")
			assert.Equal(t, tt.want, res.Disposition)
			assert.Same(t, receipt, settlement)
		})
	}
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

	res, _ := handler(context.Background(), Entry{id: "evt-perm"})
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
	res, _ := handler(ctx, Entry{id: "evt-ctx"})
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

	res, settlement := handler(context.Background(), Entry{id: "evt-claim-retry"})
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

	res, settlement := handler(context.Background(), Entry{id: "evt-claim-fail"})
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
	res, settlement := handler(ctx, Entry{id: "evt-claim-ctx"})
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

	res, settlement := handler(context.Background(), Entry{id: "evt-fail-open"})
	assert.True(t, called, "fail-open must invoke handler despite claim failure")
	assert.Equal(t, DispositionAck, res.Disposition)
	assert.Nil(t, settlement, "no settlement when claim failed under fail-open")
}

func TestConsumerBase_Wrap_FailOpen_ClaimSucceeds_RoutesViaHandleClaimState(t *testing.T) {
	// Covers the fail-open path where claimWithRetry succeeds (ClaimAcquired),
	// so Wrap calls handleClaimState (consumer_base.go line 375).
	receipt := &fakeReceipt{}
	claimer := &fakeClaimer{state: idempotency.ClaimAcquired, receipt: receipt}

	cb, err := NewConsumerBase(claimer, ConsumerBaseConfig{
		ClaimPolicy:          ClaimPolicyFailOpen,
		LeaseRenewalInterval: disableLeaseRenewal,
	}, clock.Real())
	require.NoError(t, err)

	called := false
	handler := cb.Wrap(Subscription{Topic: "t", ConsumerGroup: "cg", CellID: "cell"}, func(_ context.Context, _ Entry) HandleResult {
		called = true
		return Ack()
	})

	res, settlement := handler(context.Background(), Entry{id: "evt-fo-claim-ok"})
	assert.True(t, called, "handler must be invoked when claim acquired under fail-open")
	assert.Equal(t, DispositionAck, res.Disposition)
	assert.NotNil(t, settlement, "settlement must be non-nil when claim acquired")
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
	_, _ = handler(context.Background(), Entry{id: "evt-cap"})
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

	res, _ := handler(context.Background(), Entry{id: "evt-renewal"})
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

	res, _ := handler(context.Background(), Entry{id: "evt-expire"})

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

	entry := Entry{id: "shared-event-id-001"}
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

	res, _ := handler(context.Background(), Entry{id: "evt-quick"})
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

	res, _ := handler(context.Background(), Entry{id: "evt-transient-extend"})
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

	res, _ := handler(context.Background(), Entry{id: "evt-neg-interval"})
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

	res, _ := handler(context.Background(), Entry{id: "evt-zero"})
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

	res, _ := handler(context.Background(), Entry{id: "evt-lease-lost-ack"})

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

	res, _ := handler(context.Background(), Entry{id: "evt-lease-lost-requeue"})

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

	res, settlement := handler(context.Background(), Entry{id: "evt-normal-ack"})
	assert.Equal(t, DispositionAck, res.Disposition,
		"hard fence must not downgrade Ack when lease is always held")
	assert.Same(t, receipt, settlement,
		"settlement must be threaded through on normal Ack path")
}

// =============================================================================
// SettlementObservers transparency tests (Wave 4 review finding #1 + #2)
// =============================================================================

// TestConsumerBase_LeaseLostPath_ReturnsRequeueWithLeaseExpiredErr guards the
// hard-fence behavior in runWithRenewal: when leaseLost is detected and
// DispositionAck is downgraded to DispositionRequeue, the returned
// DeliveryOutcome must carry ErrLeaseExpired. Slim HandleResult has no
// SettlementObservers field — observer injection belongs to the SubscriberHandler
// layer (WrapSubscriber / WrapConfigEventSubscriber), not the handler return value.
func TestConsumerBase_LeaseLostPath_ReturnsRequeueWithLeaseExpiredErr(t *testing.T) {
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

	// Handler ignores ctx.Done() (stale holder), returns slim Ack.
	handler := cb.Wrap(Subscription{Topic: "topic", ConsumerGroup: "cg"}, func(_ context.Context, _ Entry) HandleResult {
		// Block long enough for the renewal goroutine to fire and set leaseLost.
		time.Sleep(renewalIntervalMultiplier5 * interval) //archtest:allow:test-sleep Renew extends TTL — polling defeats test
		return Ack()
	})

	res, _ := handler(context.Background(), Entry{id: "evt-lease-lost-observers"})

	// Hard fence must downgrade to Requeue and set ErrLeaseExpired.
	assert.Equal(t, DispositionRequeue, res.Disposition,
		"leaseLost hard fence must downgrade DispositionAck to DispositionRequeue")
	assert.ErrorIs(t, res.Err, idempotency.ErrLeaseExpired,
		"leaseLost downgrade path must set ErrLeaseExpired on DeliveryOutcome")
}

// TestConsumerBase_CtxCancelDuringBackoff_ReturnsRequeueWithCtxErr guards the
// ctx-cancel abort path in retryLoop: when context is canceled during backoff,
// the returned DeliveryOutcome must have DispositionRequeue and carry ctx.Err().
// Slim HandleResult has no SettlementObservers field — the SubscriberHandler
// layer owns observer injection, so no observer preservation is required here.
func TestConsumerBase_CtxCancelDuringBackoff_ReturnsRequeueWithCtxErr(t *testing.T) {
	receipt := &fakeReceipt{}
	claimer := &fakeClaimer{state: idempotency.ClaimAcquired, receipt: receipt}

	cb, err := NewConsumerBase(claimer, ConsumerBaseConfig{
		RetryCount:           3,
		RetryBaseDelay:       testtime.D5s, // long enough that ctx cancel wins
		LeaseRenewalInterval: disableLeaseRenewal,
	}, clock.Real())
	require.NoError(t, err)

	// Signal channel: handler sends when first called (before backoff sleep).
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
	res, _ := handler(ctx, Entry{id: "evt-ctx-cancel-observers"})
	elapsed := time.Since(start)

	// ctx cancel must abort the backoff quickly.
	assert.Less(t, elapsed, time.Second, "ctx cancel must short-circuit retry backoff")
	assert.Equal(t, DispositionRequeue, res.Disposition,
		"ctx-cancel abort path must return DispositionRequeue")
	assert.ErrorIs(t, res.Err, context.Canceled,
		"ctx-cancel abort path must carry context.Canceled as the error")
}

// =============================================================================
// ConsumerObserver wiring tests (W2: wire ConsumerObserver into ConsumerBase)
// =============================================================================

// newCapturingLogger returns a slog.Logger writing JSON to the returned buffer
// for per-test log capture. Injected via ConsumerBaseConfig.Logger (and
// DeliveryOutcome.Logger for settlement) so tests never mutate the global slog
// default — that mutation races with t.Parallel() siblings in this package.
// Tests using it ARE parallel-safe.
func newCapturingLogger() (*slog.Logger, *bytes.Buffer) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	return logger, &buf
}

// logLevelFromBuf scans JSON log lines in buf for the first entry whose "msg"
// matches wantMsg and returns its "level" field. Returns "" if not found.
func logLevelFromBuf(buf *bytes.Buffer, wantMsg string) string {
	for _, line := range bytes.Split(buf.Bytes(), []byte("\n")) {
		if len(line) == 0 {
			continue
		}
		var rec map[string]any
		if err := json.Unmarshal(line, &rec); err != nil {
			continue
		}
		if rec["msg"] == wantMsg {
			if lvl, ok := rec["level"].(string); ok {
				return lvl
			}
		}
	}
	return ""
}

// TestConsumerBase_HandlerReject_CallsObserveReject_WithHandlerRejectReason
// verifies that when the business handler returns DispositionReject,
// ConsumerBase calls ObserveReject exactly once with reason=handler_reject.
func TestConsumerBase_HandlerReject_CallsObserveReject_WithHandlerRejectReason(t *testing.T) {
	obs := &fakeObserver{}
	receipt := &fakeReceipt{}
	claimer := &fakeClaimer{state: idempotency.ClaimAcquired, receipt: receipt}

	cb, err := NewConsumerBase(claimer, ConsumerBaseConfig{
		RetryCount:           3,
		RetryBaseDelay:       time.Millisecond,
		LeaseRenewalInterval: disableLeaseRenewal,
	}, clock.Real())
	require.NoError(t, err)
	require.NoError(t, cb.AttachObserver(obs))

	sub := Subscription{Topic: "event.test.v1", ConsumerGroup: "cg-test", CellID: "testcell"}
	handler := cb.Wrap(sub, func(_ context.Context, _ Entry) HandleResult {
		return Reject(errors.New("bad payload"))
	})

	res, _ := handler(context.Background(), Entry{id: "evt-reject"})

	assert.Equal(t, DispositionReject, res.Disposition)
	require.Len(t, obs.calls, 1)
	assert.Equal(t, rejectCall{
		cellID:        "testcell",
		topic:         "event.test.v1",
		consumerGroup: "cg-test",
		reason:        ConsumerRejectReasonHandlerReject,
	}, obs.calls[0])
}

// TestConsumerBase_RetryExhausted_CallsObserveReject_WithRetryExhaustedReason
// verifies that when the retry budget is exhausted, ConsumerBase calls
// ObserveReject exactly once with reason=retry_exhausted.
func TestConsumerBase_RetryExhausted_CallsObserveReject_WithRetryExhaustedReason(t *testing.T) {
	obs := &fakeObserver{}
	receipt := &fakeReceipt{}
	claimer := &fakeClaimer{state: idempotency.ClaimAcquired, receipt: receipt}

	cb, err := NewConsumerBase(claimer, ConsumerBaseConfig{
		RetryCount:           2,
		RetryBaseDelay:       time.Millisecond,
		LeaseRenewalInterval: disableLeaseRenewal,
	}, clock.Real())
	require.NoError(t, err)
	require.NoError(t, cb.AttachObserver(obs))

	sub := Subscription{Topic: "event.test.v1", ConsumerGroup: "cg-test", CellID: "testcell"}
	handler := cb.Wrap(sub, func(_ context.Context, _ Entry) HandleResult {
		return Requeue(errors.New("always transient"))
	})

	res, _ := handler(context.Background(), Entry{id: "evt-exhaust"})

	assert.Equal(t, DispositionReject, res.Disposition)
	require.Len(t, obs.calls, 1)
	assert.Equal(t, rejectCall{
		cellID:        "testcell",
		topic:         "event.test.v1",
		consumerGroup: "cg-test",
		reason:        ConsumerRejectReasonRetryExhausted,
	}, obs.calls[0])
}

// TestConsumerBase_AckPath_DoesNotCallObserveReject verifies that the clean
// Ack path does not trigger the observer.
func TestConsumerBase_AckPath_DoesNotCallObserveReject(t *testing.T) {
	obs := &fakeObserver{}
	receipt := &fakeReceipt{}
	claimer := &fakeClaimer{state: idempotency.ClaimAcquired, receipt: receipt}

	cb, err := NewConsumerBase(claimer, ConsumerBaseConfig{
		LeaseRenewalInterval: disableLeaseRenewal,
	}, clock.Real())
	require.NoError(t, err)
	require.NoError(t, cb.AttachObserver(obs))

	sub := Subscription{Topic: "event.test.v1", ConsumerGroup: "cg-test", CellID: "testcell"}
	handler := cb.Wrap(sub, func(_ context.Context, _ Entry) HandleResult {
		return Ack()
	})

	res, _ := handler(context.Background(), Entry{id: "evt-ack"})

	assert.Equal(t, DispositionAck, res.Disposition)
	assert.Empty(t, obs.calls, "ObserveReject must not be called on Ack path")
}

// TestConsumerBase_Wrap_NilObserver_FallsBackToNop verifies that a freshly
// constructed ConsumerBase (no AttachObserver call) does not panic when
// processing a reject — the Nop observer is active and silently discards.
func TestConsumerBase_Wrap_NilObserver_FallsBackToNop(t *testing.T) {
	receipt := &fakeReceipt{}
	claimer := &fakeClaimer{state: idempotency.ClaimAcquired, receipt: receipt}

	cb, err := NewConsumerBase(claimer, ConsumerBaseConfig{
		RetryCount:           1,
		RetryBaseDelay:       time.Millisecond,
		LeaseRenewalInterval: disableLeaseRenewal,
	}, clock.Real())
	require.NoError(t, err)
	// Deliberately do NOT call AttachObserver — Nop must be active.

	sub := Subscription{Topic: "event.test.v1", ConsumerGroup: "cg-test", CellID: "testcell"}
	handler := cb.Wrap(sub, func(_ context.Context, _ Entry) HandleResult {
		return Reject(errors.New("permanent"))
	})

	// Must not panic.
	res, _ := handler(context.Background(), Entry{id: "evt-nop"})
	assert.Equal(t, DispositionReject, res.Disposition)
}

// TestConsumerBase_AttachObserver_Idempotent_ReturnsError verifies that a
// second AttachObserver call returns ErrObserverAlreadyAttached.
func TestConsumerBase_AttachObserver_Idempotent_ReturnsError(t *testing.T) {
	cb := testConsumerBase(t)

	require.NoError(t, cb.AttachObserver(&fakeObserver{}))
	err := cb.AttachObserver(&fakeObserver{})
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrObserverAlreadyAttached)
}

// TestConsumerBase_AttachObserver_NilObserver_ReturnsError verifies that both
// bare-nil and typed-nil observers are rejected with a validation error.
func TestConsumerBase_AttachObserver_NilObserver_ReturnsError(t *testing.T) {
	t.Run("bare_nil", func(t *testing.T) {
		cb := testConsumerBase(t)
		err := cb.AttachObserver(nil)
		require.Error(t, err)
		assert.NotErrorIs(t, err, ErrObserverAlreadyAttached,
			"nil rejection must not be confused with already-attached error")
	})

	t.Run("typed_nil", func(t *testing.T) {
		cb := testConsumerBase(t)
		var p *fakeObserver
		var o ConsumerObserver = p
		err := cb.AttachObserver(o)
		require.Error(t, err)
		assert.NotErrorIs(t, err, ErrObserverAlreadyAttached)
	})
}

// TestConsumerBase_RetryExhausted_LogLevelError verifies that the
// retry-budget-exhausted log entry is emitted at ERROR level (upgraded from
// WARN per observability.md: DLX-routed reject is correctness-affecting).
func TestConsumerBase_RetryExhausted_LogLevelError(t *testing.T) {
	t.Parallel()
	logger, buf := newCapturingLogger()

	receipt := &fakeReceipt{}
	claimer := &fakeClaimer{state: idempotency.ClaimAcquired, receipt: receipt}

	cb, err := NewConsumerBase(claimer, ConsumerBaseConfig{
		RetryCount:           1,
		RetryBaseDelay:       time.Millisecond,
		LeaseRenewalInterval: disableLeaseRenewal,
		Logger:               logger,
	}, clock.Real())
	require.NoError(t, err)

	sub := Subscription{Topic: "event.test.v1", ConsumerGroup: "cg-test", CellID: "testcell"}
	handler := cb.Wrap(sub, func(_ context.Context, _ Entry) HandleResult {
		return Requeue(errors.New("always fail"))
	})

	res, _ := handler(context.Background(), Entry{id: "evt-loglevel"})
	assert.Equal(t, DispositionReject, res.Disposition)

	level := logLevelFromBuf(buf, "outbox: retry budget exhausted, rejecting to DLX")
	assert.Equal(t, "ERROR", level,
		"retry-exhausted log entry must be at ERROR level (observability.md §slog 日志级别)")
}

// TestConsumerBase_HandlerReject_ObserveReject_CellIDFromSubscription verifies
// that the cellID forwarded to ObserveReject is taken from sub.CellID, not
// from consumerGroup or a fallback. This guards the F10 capture-chain.
func TestConsumerBase_HandlerReject_ObserveReject_CellIDFromSubscription(t *testing.T) {
	obs := &fakeObserver{}
	receipt := &fakeReceipt{}
	claimer := &fakeClaimer{state: idempotency.ClaimAcquired, receipt: receipt}

	cb, err := NewConsumerBase(claimer, ConsumerBaseConfig{
		LeaseRenewalInterval: disableLeaseRenewal,
	}, clock.Real())
	require.NoError(t, err)
	require.NoError(t, cb.AttachObserver(obs))

	// CellID and ConsumerGroup are deliberately different to prove CellID wins.
	sub := Subscription{
		Topic:         "event.distinct.v1",
		ConsumerGroup: "cg-for-broker-partitioning",
		CellID:        "distinct-cell-id",
	}
	handler := cb.Wrap(sub, func(_ context.Context, _ Entry) HandleResult {
		return Reject(errors.New("permanent"))
	})

	_, _ = handler(context.Background(), Entry{id: "evt-cellid"})

	require.Len(t, obs.calls, 1)
	assert.Equal(t, "distinct-cell-id", obs.calls[0].cellID,
		"ObserveReject cellID must equal sub.CellID, not consumerGroup")
	assert.Equal(t, "cg-for-broker-partitioning", obs.calls[0].consumerGroup)
}

// TestConsumerBase_RetryExhausted_NoObserveReject_OnCtxCancel verifies that
// ctx-cancel Requeue paths do not call ObserveReject (not a terminal Reject).
func TestConsumerBase_RetryExhausted_NoObserveReject_OnCtxCancel(t *testing.T) {
	obs := &fakeObserver{}
	receipt := &fakeReceipt{}
	claimer := &fakeClaimer{state: idempotency.ClaimAcquired, receipt: receipt}

	cb, err := NewConsumerBase(claimer, ConsumerBaseConfig{
		RetryCount:           5,
		RetryBaseDelay:       testtime.D5s,
		LeaseRenewalInterval: disableLeaseRenewal,
	}, clock.Real())
	require.NoError(t, err)
	require.NoError(t, cb.AttachObserver(obs))

	started := make(chan struct{}, 1)
	sub := Subscription{Topic: "event.test.v1", ConsumerGroup: "cg-test", CellID: "testcell"}
	handler := cb.Wrap(sub, func(_ context.Context, _ Entry) HandleResult {
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

	res, _ := handler(ctx, Entry{id: "evt-ctxcancel"})

	assert.Equal(t, DispositionRequeue, res.Disposition)
	assert.Empty(t, obs.calls, "ObserveReject must NOT be called on ctx-cancel Requeue path")
}

// TestConsumerBase_ObserveReject_PanicingObserver_DoesNotEscape verifies that
// a panic from ConsumerObserver.ObserveReject does not propagate out of the
// Wrap-produced handler. The goroutine must survive and a log line must be
// emitted.
//
// RED: consumer_base.go calls cb.observer.ObserveReject(...) without a
// panic-recovery wrapper, so the panic currently escapes.
func TestConsumerBase_ObserveReject_PanicingObserver_DoesNotEscape(t *testing.T) {
	t.Parallel()
	logger, buf := newCapturingLogger()

	receipt := &fakeReceipt{}
	claimer := &fakeClaimer{state: idempotency.ClaimAcquired, receipt: receipt}

	cb, err := NewConsumerBase(claimer, ConsumerBaseConfig{
		LeaseRenewalInterval: disableLeaseRenewal,
		Logger:               logger,
	}, clock.Real())
	require.NoError(t, err)
	require.NoError(t, cb.AttachObserver(&panicingObserver{}))

	sub := Subscription{Topic: "event.test.v1", ConsumerGroup: "cg-test", CellID: "testcell"}
	handler := cb.Wrap(sub, func(_ context.Context, _ Entry) HandleResult {
		return Reject(errors.New("permanent"))
	})

	// Must not panic — target behavior: panic is recovered inside ConsumerBase.
	require.NotPanics(t, func() {
		_, _ = handler(context.Background(), Entry{id: "evt-panic-observer"})
	}, "panic from ConsumerObserver.ObserveReject must NOT escape the Wrap handler")

	// A WARN or ERROR log line must be emitted to record the recovered panic.
	found := false
	for _, line := range bytes.Split(buf.Bytes(), []byte("\n")) {
		if bytes.Contains(line, []byte("observer")) || bytes.Contains(line, []byte("panic")) {
			found = true
			break
		}
	}
	assert.True(t, found,
		"expected a log line mentioning observer panic, but found none in captured slog output")
}

// panicingObserver is a ConsumerObserver that always panics in ObserveReject,
// used to verify panic isolation in ConsumerBase.Wrap.
type panicingObserver struct{}

func (p *panicingObserver) ObserveReject(_ context.Context, _, _, _, _ string) {
	panic("panicingObserver: intentional panic for isolation test")
}

// TestConsumerBase_DeliveryDims_AllThreeFieldsForwardedToObserveReject asserts
// that deliveryDims.cellID, deliveryDims.consumerGroup, and deliveryDims.topic
// are all independently forwarded to ObserveReject on a handler-reject path.
// Each sub-case uses a distinct value for each field so a field swap or
// truncation is immediately visible.
func TestConsumerBase_DeliveryDims_AllThreeFieldsForwardedToObserveReject(t *testing.T) {
	cases := []struct {
		name          string
		cellID        string
		consumerGroup string
		topic         string
	}{
		{
			name:          "distinct_values",
			cellID:        "cell-alpha",
			consumerGroup: "cg-beta",
			topic:         "event.gamma.v1",
		},
		{
			name:          "long_topic",
			cellID:        "accesscore",
			consumerGroup: "cg-accesscore-session",
			topic:         "event.session.created.v1",
		},
		{
			name:          "minimal_ids",
			cellID:        "c",
			consumerGroup: "g",
			topic:         "t",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			obs := &fakeObserver{}
			receipt := &fakeReceipt{}
			claimer := &fakeClaimer{state: idempotency.ClaimAcquired, receipt: receipt}

			cb, err := NewConsumerBase(claimer, ConsumerBaseConfig{
				LeaseRenewalInterval: disableLeaseRenewal,
			}, clock.Real())
			require.NoError(t, err)
			require.NoError(t, cb.AttachObserver(obs))

			sub := Subscription{
				CellID:        tc.cellID,
				ConsumerGroup: tc.consumerGroup,
				Topic:         tc.topic,
			}
			handler := cb.Wrap(sub, func(_ context.Context, _ Entry) HandleResult {
				return Reject(errors.New("bad payload"))
			})

			_, _ = handler(context.Background(), Entry{id: "evt-dims"})

			require.Len(t, obs.calls, 1, "ObserveReject must be called exactly once")
			assert.Equal(t, tc.cellID, obs.calls[0].cellID,
				"deliveryDims.cellID must flow to ObserveReject")
			assert.Equal(t, tc.consumerGroup, obs.calls[0].consumerGroup,
				"deliveryDims.consumerGroup must flow to ObserveReject")
			assert.Equal(t, tc.topic, obs.calls[0].topic,
				"deliveryDims.topic must flow to ObserveReject")
			assert.Equal(t, ConsumerRejectReasonHandlerReject, obs.calls[0].reason)
		})
	}
}
