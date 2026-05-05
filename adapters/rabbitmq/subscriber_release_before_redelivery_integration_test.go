//go:build integration

package rabbitmq

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/idempotency"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/pkg/testutil/testtime"
)

// TestIntegration_CommitFailedAllowsRedeliveryToSameProcess covers the N8 K#12
// release-first invariant end-to-end against a real RabbitMQ broker:
//
//   1. Publish a single message.
//   2. Handler returns DispositionAck on every attempt.
//   3. The first idempotency.Receipt's Commit returns an error (simulated lease
//      expiration). Release passes through to the in-memory claimer.
//   4. The subscriber's commit_failed path MUST call Release before broker Nack,
//      otherwise the second delivery (redelivery) would observe the claim still
//      held in this process and short-circuit as ClaimBusy → DispositionRequeue,
//      blocking redelivery until lease TTL expires (default 5m, well beyond the
//      test's 10s budget).
//
// Asserts: handler is invoked at least twice (initial + redelivery) within
// testtime.D10s. Under release-first this completes in <1s; under the legacy
// Nack-first path the handler is gated on lease TTL and the test fails.
//
// ref: IBM/sarama consumer_group.go release() L801-L824 — handler.Cleanup
// before offsets.Close(); same principle on the per-message commit_failed path.
// ref: runtime/eventbus/eventbus.go:469→473 — release-first already adopted
// in-process; this test pins the RMQ subscriber to the same order under broker.
func TestIntegration_CommitFailedAllowsRedeliveryToSameProcess(t *testing.T) {
	conn, cleanup := startRabbitMQ(t)
	defer cleanup()

	pub := NewPublisher(conn, WithPublisherClock(clock.Real()))
	const (
		topic     = "test.release-before-redelivery.events"
		queueName = "test.release-before-redelivery.queue"
		group     = "test-release-before-redelivery"
	)

	inner := idempotency.NewInMemClaimer(clock.Real())
	claimer := &flakyCommitOnceClaimer{inner: inner}

	cb, err := outbox.NewConsumerBase(claimer, outbox.ConsumerBaseConfig{
		ClaimRetryCount:    2,
		RetryCount:         2,
		LeaseTTL:           5 * time.Minute,
		IdempotencyTTL:     24 * time.Hour,
		LeaseRenewalInterval: -1, // disable renewal for determinism
	}, clock.Real())
	require.NoError(t, err)

	var handlerCalls atomic.Int32
	wrapped := cb.Wrap(outbox.Subscription{Topic: topic, ConsumerGroup: group},
		func(_ context.Context, _ outbox.Entry) outbox.HandleResult {
			handlerCalls.Add(1)
			return outbox.HandleResult{Disposition: outbox.DispositionAck}
		})

	sub := NewSubscriber(conn, SubscriberConfig{
		QueueName:     queueName,
		PrefetchCount: 1,
		DLXExchange:   "test.release-before-redelivery.dlx",
		Clock:         clock.Real(),
	})

	subCtx, subCancel := context.WithTimeout(context.Background(), testtime.D15s)
	defer subCancel()

	subErrCh := make(chan error, 1)
	go func() {
		subErrCh <- sub.Subscribe(subCtx, outbox.Subscription{Topic: topic, ConsumerGroup: group}, wrapped)
	}()

	waitForSubscriberReady(t, conn, queueName, subErrCh, testtime.EventuallyLong)

	entry := outbox.Entry{
		ID:        "evt-release-before-redelivery",
		EventType: "test.event",
		Payload:   []byte(`{}`),
		CreatedAt: time.Now().UTC(),
	}
	payload, err := outbox.MarshalEnvelope(entry)
	require.NoError(t, err)
	require.NoError(t, pub.Publish(context.Background(), topic, payload))

	require.Eventually(t, func() bool {
		return handlerCalls.Load() >= 2
	}, testtime.D10s, testtime.FastPoll,
		"handler must be invoked at least twice within 10s: "+
			"attempt #1 commit fails → release-first lets broker redelivery proceed → attempt #2 succeeds. "+
			"Nack-first ordering would gate redelivery on lease TTL (5m).")

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

func (c *flakyCommitOnceClaimer) Claim(
	ctx context.Context, key string, leaseTTL, doneTTL time.Duration,
) (idempotency.ClaimState, idempotency.Receipt, error) {
	state, r, err := c.inner.Claim(ctx, key, leaseTTL, doneTTL)
	if err != nil || state != idempotency.ClaimAcquired {
		return state, r, err
	}
	idx := c.receiptIdx.Add(1)
	if idx == 1 {
		return state, &flakyCommitReceipt{inner: r, attempts: &c.commitAttempts, failOn: 1, idx: idx}, nil
	}
	return state, &flakyCommitReceipt{inner: r, attempts: &c.commitAttempts, failOn: 0, idx: idx}, nil
}

type flakyCommitReceipt struct {
	inner    idempotency.Receipt
	attempts *atomic.Int32
	failOn   int32 // when idx == failOn, return error from Commit
	idx      int32
}

func (r *flakyCommitReceipt) Commit(ctx context.Context) error {
	r.attempts.Add(1)
	if r.idx == r.failOn {
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
