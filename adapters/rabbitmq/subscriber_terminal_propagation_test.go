package rabbitmq

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/clock/clockmock"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/testutil/testtime"
	"github.com/ghbvf/gocell/pkg/testutil/testwait"
)

// TestSubscriber_Subscribe_PropagatesPermanentError is a regression guard
// for the subscriber→connection terminal-error propagation contract.
//
// Contract (PR#379, subscriber.go:425-430): when Connection.WaitConnected
// returns ErrAdapterAMQPConnectPermanent (revoked credentials, deleted
// vhost, hard protocol error), Subscriber.Subscribe propagates that error
// to the caller — EventRouter / Bootstrap depend on this so /readyz can
// flip to 503 and operator remediation is observable.
//
// Connection-level propagation is locked by
// connection_runtime_terminal_test.go:TestReconnectLoop_PermanentAndRecovery;
// this test guards the public Subscriber boundary.
//
// Deliberate deviation from watermill-amqp: that library's Subscribe
// returns a channel and silently closes it on permanent errors — caller
// never sees the cause. GoCell's blocking Subscribe + return permanent
// err is the intentional improvement.
//
// ref: connection_runtime_terminal_test.go newMockConnection injection pattern.
func TestSubscriber_Subscribe_PropagatesPermanentError(t *testing.T) {
	t.Parallel()

	originalMock := newMockConnection()

	// dialPhase orchestrates two regimes:
	//  - phase=0: initial dial succeeds (returns originalMock)
	//  - phase=1: amqp.ErrSASL — definitive permanent sentinel, single
	//    hit promotes to permanentErr (no confirmThreshold delay).
	var phase atomic.Int32

	dialFunc := func(string) (AMQPConnection, error) {
		if phase.Load() == 0 {
			return originalMock, nil
		}
		return nil, amqp.ErrSASL
	}

	// FakeClock on the connection makes reconnect backoff deterministic: the
	// backoff timer fires only when the test Advances fc, so propagation timing
	// no longer races a real wall-clock deadline (the #930 flake).
	fc := clockmock.New(time.Time{})

	conn, err := NewConnection(Config{
		URL:                 testAMQPURL,
		ChannelPoolSize:     2,
		ReconnectBaseDelay:  testtime.D1ms,
		ReconnectMaxBackoff: testtime.FastPoll,
	}, WithDialFunc(dialFunc), WithConnectionClock(fc))
	require.NoError(t, err, "initial dial must succeed (phase=0)")
	defer func() {
		if cErr := conn.Close(context.Background()); cErr != nil {
			t.Logf("conn.Close: %v", cErr)
		}
	}()

	// Subscriber keeps a real clock: the FakeClock only needs to gate the
	// connection-level reconnect backoff. The subscriber clock drives the
	// graceful-drain timer, which this scenario never enters (the propagation
	// path is consumeLoop→awaitReconnect→WaitConnected, no subscriber timer).
	sub := NewSubscriber(conn, SubscriberConfig{
		DLXExchange: "test.terminal.dlx",
		Clock:       clock.Real(),
	})
	defer func() { _ = sub.Close(context.Background()) }()

	// Wait for reconnect loop to register NotifyClose handler.
	testwait.External(t, "amqp-notify-close-registered", func() bool {
		originalMock.mu.Lock()
		defer originalMock.mu.Unlock()
		return originalMock.notifyCloseCh != nil
	}, testtime.D2s, testtime.D1ms)

	ctx, cancel := context.WithTimeout(context.Background(), testtime.EventuallyLong)
	defer cancel()

	subErrCh := make(chan error, 1)
	go func() {
		subErrCh <- sub.Subscribe(ctx,
			outbox.Subscription{Topic: "t.permanent", ConsumerGroup: "g", CellID: "g"},
			entryToSubHandler(func(_ context.Context, _ outbox.Entry) outbox.HandleResult {
				return outbox.Ack()
			}))
	}()

	// Barrier: wait until the subscriber is consuming (parked in consumeLoop on
	// <-deliveries). Without this, the broker close can land while subscribeOnce
	// is still in setup, taking the AcquireChannel terminal path and never
	// exercising the consumeLoop→awaitReconnect→WaitConnected contract this test
	// guards (and the path that flaked under CI load).
	testwait.External(t, "amqp-consumer-parked", func() bool {
		return originalMock.consumingStarted()
	}, testtime.D2s, testtime.D1ms)

	// Phase 0 → 1: trigger a broker-forced close. triggerBrokerClose closes the
	// consumer delivery channel (so the parked consumeLoop returns
	// errSubscriptionLost → awaitReconnect → WaitConnected) and fires NotifyClose
	// (so the reconnect loop re-dials). The reconnect dial returns ErrSASL →
	// markPermanent → WaitConnected returns ErrAdapterAMQPConnectPermanent →
	// awaitReconnect propagates it → Subscribe returns it.
	phase.Store(1)
	originalMock.triggerBrokerClose()

	// Drive the reconnect backoff deterministically via fc until Subscribe returns.
	subErr := advanceFakeReconnectUntil(t, fc, subErrCh)
	require.Error(t, subErr, "Subscribe must return permanent error, not nil")
	var ecErr *errcode.Error
	require.True(t, errors.As(subErr, &ecErr),
		"Subscribe error must wrap *errcode.Error; got %T: %v", subErr, subErr)
	assert.Equal(t, ErrAdapterAMQPConnectPermanent, ecErr.Code,
		"Subscribe must propagate ErrAdapterAMQPConnectPermanent")
}
