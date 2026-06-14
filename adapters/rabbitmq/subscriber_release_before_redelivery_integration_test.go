//go:build integration

package rabbitmq

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/framework/kernel/clock"
	"github.com/ghbvf/gocell/framework/kernel/idempotency"
	"github.com/ghbvf/gocell/framework/kernel/outbox"
	"github.com/ghbvf/gocell/framework/pkg/testutil/testtime"
	"github.com/ghbvf/gocell/framework/pkg/testutil/testwait"
)

const (
	// releaseBeforeRedeliveryLeaseTTL is the processing-lease TTL passed to
	// ConsumerBase. Long enough that a Nack-first path would wait the full
	// duration before retrying — which is exactly the regression this test
	// gates against (the inter-delivery gap must be broker-RTT scale, not
	// lease-TTL scale).
	releaseBeforeRedeliveryLeaseTTL = 5 * time.Minute

	// releaseBeforeRedeliveryDoneTTL is the idempotency-done TTL passed to
	// ConsumerBase; standard 24h matches the production default.
	releaseBeforeRedeliveryDoneTTL = 24 * time.Hour

	// releaseBeforeRedeliveryNoRenewal disables the lease-renewal goroutine
	// so the test deterministically observes one Commit attempt per delivery.
	releaseBeforeRedeliveryNoRenewal = -1 * time.Second

	// releaseBeforeRedeliveryUpperBound bounds the redelivery test wall-clock.
	// 2s ≈ 40× expected Docker-local broker RTT (≤50ms) and 150× smaller than
	// releaseBeforeRedeliveryLeaseTTL (5m). Anchored on N×P99 rather than a
	// fraction of the fallback TTL, per nats-streaming-server
	// server/server_redelivery_test.go (test timeouts on N×ackWait scale).
	releaseBeforeRedeliveryUpperBound = testtime.D2s

	// releaseBeforeRedeliveryMaxGap bounds the gap between the first and
	// second handler invocation. 1s ≈ 12-20× P99 redelivery gap on shared
	// GitHub-Actions runners (CPU starve + testcontainer warm path) yet still
	// 300× smaller than releaseBeforeRedeliveryLeaseTTL — any gap exceeding
	// this means the redelivery was actually gated on the 5m lease-TTL
	// fallback (the regression this test exists to catch), not broker-level
	// requeue. 500ms was the strictest reviewer-recommended bound but left
	// only ~6× CI margin; 1s trades minor precision (still 300× vs 600× to
	// fallback TTL) for negligible flake risk on shared runners.
	releaseBeforeRedeliveryMaxGap = testtime.D1s
)

// TestIntegration_CommitFailedAllowsRedeliveryToSameProcess covers the N8 K#12
// release-first invariant end-to-end against a real RabbitMQ broker:
//
//  1. Publish a single message.
//  2. Handler returns DispositionAck on every attempt.
//  3. The first idempotency.Receipt's Commit returns an error (simulated lease
//     expiration). Release passes through to the in-memory claimer.
//  4. The subscriber's commit_failed path MUST call Release before broker Nack,
//     otherwise the second delivery (redelivery) would observe the claim still
//     held in this process and short-circuit as ClaimBusy → DispositionRequeue,
//     blocking redelivery until lease TTL expires (default 5m, well beyond any
//     reasonable test budget).
//
// Asserts two bounds anchored on N×P99 broker RTT, not on the lease-TTL
// fallback path (per nats-streaming-server redelivery-test pattern):
//
//   - Upper bound: handler is invoked at least twice within
//     releaseBeforeRedeliveryUpperBound (2s ≈ 40× broker RTT).
//   - Lower bound on gap: the wall-clock gap between attempt #1 and attempt #2
//     is less than releaseBeforeRedeliveryMaxGap (1s ≈ 12-20× shared-runner
//     P99 gap, still 300× smaller than the 5m lease-TTL fallback). A larger
//     gap means redelivery actually waited on the 5m lease-TTL fallback —
//     exactly the regression a Nack-first path would introduce.
//
// ref: IBM/sarama consumer_group.go release() L801-L824 — handler.Cleanup
// before offsets.Close(); same principle on the per-message commit_failed path.
// ref: runtime/eventbus/eventbus.go:469→473 — release-first already adopted
// in-process; this test pins the RMQ subscriber to the same order under broker.
func TestIntegration_CommitFailedAllowsRedeliveryToSameProcess(t *testing.T) {
	conn, cleanup := startRabbitMQ(t)
	defer cleanup()

	pub := NewPublisher(clock.Real(), conn)
	const (
		topic     = "test.release-before-redelivery.events"
		queueName = "test.release-before-redelivery.queue"
		group     = "test-release-before-redelivery"
	)

	inner := idempotency.NewInMemClaimer(clock.Real())
	claimer := &flakyCommitOnceClaimer{inner: inner}

	cb, err := outbox.NewConsumerBase(claimer, outbox.ConsumerBaseConfig{
		ClaimRetryCount:      2,
		RetryCount:           2,
		LeaseTTL:             releaseBeforeRedeliveryLeaseTTL,
		IdempotencyTTL:       releaseBeforeRedeliveryDoneTTL,
		LeaseRenewalInterval: releaseBeforeRedeliveryNoRenewal,
	}, clock.Real())
	require.NoError(t, err)

	var (
		callTimesMu sync.Mutex
		callTimes   []time.Time
	)
	wrapped := cb.Wrap(outbox.Subscription{Topic: topic, ConsumerGroup: group, CellID: group},
		func(_ context.Context, _ outbox.Entry) outbox.HandleResult {
			callTimesMu.Lock()
			callTimes = append(callTimes, time.Now())
			callTimesMu.Unlock()
			return outbox.Ack()
		})

	sub := NewSubscriber(clock.Real(), conn, SubscriberConfig{
		QueueName:     queueName,
		PrefetchCount: 1,
		DLXExchange:   "test.release-before-redelivery.dlx",
	})

	subCtx, subCancel := context.WithTimeout(context.Background(), testtime.D15s)
	defer subCancel()

	subErrCh := make(chan error, 1)
	go func() {
		subErrCh <- sub.Subscribe(subCtx, outbox.Subscription{Topic: topic, ConsumerGroup: group, CellID: group}, wrapped)
	}()

	waitForSubscriberReady(t, conn, queueName, subErrCh, testtime.EventuallyLong)

	entry := mustNewEntry(t, "test.event", []byte(`{}`), outbox.WithID("evt-release-before-redelivery"), outbox.WithCreatedAt(time.Now().UTC()))
	payload, err := outbox.MarshalEnvelope(entry)
	require.NoError(t, err)
	require.NoError(t, pub.Publish(context.Background(), topic, payload))

	testwait.External(t, "amqp-handler-redelivery-confirmed", func() bool {
		callTimesMu.Lock()
		defer callTimesMu.Unlock()
		return len(callTimes) >= 2
	}, releaseBeforeRedeliveryUpperBound, testtime.FastPoll,
		"handler must be invoked at least twice within %s: "+
			"attempt #1 commit fails → release-first lets broker redelivery proceed → attempt #2 succeeds. "+
			"Nack-first ordering would gate redelivery on lease TTL (5m).",
		releaseBeforeRedeliveryUpperBound)

	callTimesMu.Lock()
	redeliveryGap := callTimes[1].Sub(callTimes[0])
	callTimesMu.Unlock()
	assert.Less(t, redeliveryGap, releaseBeforeRedeliveryMaxGap,
		"redelivery gap %s indicates lease-TTL gating, not broker-level requeue "+
			"(shared-runner P99 gap ≤80ms; lease TTL %s)",
		redeliveryGap, releaseBeforeRedeliveryLeaseTTL)

	subCancel()
	_ = sub.Close(context.Background())

	assert.GreaterOrEqual(t, claimer.commitAttempts.Load(), int32(2),
		"Commit must be attempted on each delivery (first fails, second succeeds)")
}

// flakyCommitOnceClaimer wraps an in-memory Claimer so the FIRST receipt's
// Commit returns an error; subsequent receipts pass through unchanged. Release
// and Extend always pass through.
type flakyCommitOnceClaimer struct {
	inner          idempotency.Claimer
	receiptIdx     atomic.Int32
	commitAttempts atomic.Int32
}

func (c *flakyCommitOnceClaimer) Kind() idempotency.ClaimerKind { return c.inner.Kind() }

func (c *flakyCommitOnceClaimer) Claim(
	ctx context.Context, key string, leaseTTL, doneTTL time.Duration,
) (idempotency.ClaimState, idempotency.Receipt, error) {
	state, r, err := c.inner.Claim(ctx, key, leaseTTL, doneTTL)
	if err != nil || state != idempotency.ClaimAcquired {
		return state, r, err
	}
	idx := c.receiptIdx.Add(1)
	return state, &flakyCommitReceipt{inner: r, attempts: &c.commitAttempts, failFirst: idx == 1}, nil
}

type flakyCommitReceipt struct {
	inner     idempotency.Receipt
	attempts  *atomic.Int32
	failFirst bool // first invocation of Commit returns error; subsequent pass through
}

func (r *flakyCommitReceipt) Commit(ctx context.Context) error {
	r.attempts.Add(1)
	if r.failFirst {
		return errors.New("simulated commit failure (lease expired)")
	}
	return r.inner.Commit(ctx)
}

func (r *flakyCommitReceipt) Release(ctx context.Context) error {
	return r.inner.Release(ctx)
}

func (r *flakyCommitReceipt) Extend(ctx context.Context, ttl time.Duration) error {
	return r.inner.Extend(ctx, ttl)
}
