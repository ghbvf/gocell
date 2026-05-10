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
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/testutil/testtime"
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

	conn, err := NewConnection(Config{
		URL:                 testAMQPURL,
		ChannelPoolSize:     2,
		ReconnectBaseDelay:  testtime.D1ms,
		ReconnectMaxBackoff: testtime.FastPoll,
	}, WithDialFunc(dialFunc), WithConnectionClock(clock.Real()))
	require.NoError(t, err, "initial dial must succeed (phase=0)")
	defer func() {
		if cErr := conn.Close(context.Background()); cErr != nil {
			t.Logf("conn.Close: %v", cErr)
		}
	}()

	sub := NewSubscriber(conn, SubscriberConfig{
		DLXExchange: "test.terminal.dlx",
		Clock:       clock.Real(),
	})
	defer func() { _ = sub.Close(context.Background()) }()

	// Wait for reconnect loop to register NotifyClose handler.
	require.Eventually(t, func() bool {
		originalMock.mu.Lock()
		defer originalMock.mu.Unlock()
		return originalMock.notifyCloseCh != nil
	}, time.Second, time.Millisecond)

	ctx, cancel := context.WithTimeout(context.Background(), testtime.EventuallyLong)
	defer cancel()

	subErrCh := make(chan error, 1)
	go func() {
		subErrCh <- sub.Subscribe(ctx,
			outbox.Subscription{Topic: "t.permanent", ConsumerGroup: "g"},
			entryToSubHandler(func(_ context.Context, _ outbox.Entry) outbox.HandleResult {
				return outbox.Ack()
			}))
	}()

	// Phase 0 → 1: trigger broker-side close → reconnect dial returns ErrSASL
	// → markPermanent → WaitConnected returns ErrAdapterAMQPConnectPermanent
	// → awaitReconnect propagates it → Subscribe returns it.
	phase.Store(1)
	originalMock.mu.Lock()
	closeNotifyCh := originalMock.notifyCloseCh
	originalMock.isClosed = true
	originalMock.mu.Unlock()
	closeNotifyCh <- &amqp.Error{Code: 320, Reason: "CONNECTION_FORCED", Recover: true}

	select {
	case subErr := <-subErrCh:
		require.Error(t, subErr, "Subscribe must return permanent error, not nil")
		var ecErr *errcode.Error
		require.True(t, errors.As(subErr, &ecErr),
			"Subscribe error must wrap *errcode.Error; got %T: %v", subErr, subErr)
		assert.Equal(t, ErrAdapterAMQPConnectPermanent, ecErr.Code,
			"Subscribe must propagate ErrAdapterAMQPConnectPermanent")
	case <-time.After(testtime.EventuallyLong):
		t.Fatal("Subscribe did not return within budget; propagation contract broken")
	}
}
