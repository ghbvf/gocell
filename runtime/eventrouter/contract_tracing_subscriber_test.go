package eventrouter

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/kernel/wrapper"
)

type lifecycleTracingSubscriber struct {
	setupCalled      bool
	readyCalled      bool
	closeCalled      bool
	stopIntakeCalled bool
	capturedHandler  outbox.SubscriberHandler
	readyCh          chan struct{}
}

func newLifecycleTracingSubscriber() *lifecycleTracingSubscriber {
	ch := make(chan struct{})
	close(ch)
	return &lifecycleTracingSubscriber{readyCh: ch}
}

func (s *lifecycleTracingSubscriber) Setup(_ context.Context, _ outbox.Subscription) error {
	s.setupCalled = true
	return nil
}

func (s *lifecycleTracingSubscriber) Ready(_ outbox.Subscription) <-chan struct{} {
	s.readyCalled = true
	return s.readyCh
}

func (s *lifecycleTracingSubscriber) Subscribe(_ context.Context, _ outbox.Subscription, h outbox.SubscriberHandler) error {
	s.capturedHandler = h
	return nil
}

func (s *lifecycleTracingSubscriber) Close(_ context.Context) error {
	s.closeCalled = true
	return nil
}

func (s *lifecycleTracingSubscriber) StopIntake(_ context.Context) error {
	s.stopIntakeCalled = true
	return nil
}

func TestNewContractTracingSubscriber_WrapsSubscribeAndDelegatesLifecycle(t *testing.T) {
	inner := newLifecycleTracingSubscriber()
	tr := &contractSpyTracer{}
	decorated := NewContractTracingSubscriber(inner, tr)

	sub := outbox.Subscription{
		Topic:             "event.config.entry-upserted.v1",
		ConsumerGroup:     "accesscore",
		ContractID:        "event.config.entry-upserted.v1",
		ContractKind:      "event",
		ContractTransport: "amqp",
	}

	require.NoError(t, decorated.Setup(context.Background(), sub))
	assert.True(t, inner.setupCalled)
	<-decorated.Ready(sub)
	assert.True(t, inner.readyCalled)

	stopper, ok := decorated.(outbox.SubscriberIntakeStopper)
	require.True(t, ok, "decorator must preserve StopIntake")
	require.NoError(t, stopper.StopIntake(context.Background()))
	assert.True(t, inner.stopIntakeCalled)

	require.NoError(t, decorated.Subscribe(context.Background(), sub,
		func(_ context.Context, _ outbox.Entry) (outbox.HandleResult, outbox.Settlement) {
			return outbox.HandleResult{Disposition: outbox.DispositionAck}, nil
		}))
	require.NotNil(t, inner.capturedHandler)

	entry := outbox.Entry{ID: "evt-1", Topic: sub.Topic}
	res, settlement := inner.capturedHandler(context.Background(), entry)
	assert.Nil(t, settlement)
	outbox.NotifySettlement(context.Background(), res, entry,
		outbox.DispositionAck, outbox.SettlementResultSuccess, nil)

	span := tr.only(t)
	assert.True(t, span.ended)
	assert.Equal(t, wrapper.StatusOK, span.status)
	assert.Equal(t, "CONSUME event.config.entry-upserted.v1", span.name)
	assert.Equal(t, "event.config.entry-upserted.v1", span.attrMap()["gocell.contract.id"])

	require.NoError(t, decorated.Close(context.Background()))
	assert.True(t, inner.closeCalled)
}

// subscribeCapturingPanic invokes decorator.Subscribe under defer-recover so we
// can assert the post-N8 contract: missing Subscription metadata must surface as
// an error, not a panic. RED in current code (MustWrapSubscriber panics inside
// spec.Validate); GREEN after sub.Validate() is delegated to the single source
// (Subscription.Validate) and WrapSubscriber returns an error.
func subscribeCapturingPanic(t *testing.T, decorated outbox.Subscriber, sub outbox.Subscription) (panicked any, subscribeErr error) {
	t.Helper()
	func() {
		defer func() { panicked = recover() }()
		subscribeErr = decorated.Subscribe(context.Background(), sub,
			func(_ context.Context, _ outbox.Entry) (outbox.HandleResult, outbox.Settlement) {
				return outbox.HandleResult{Disposition: outbox.DispositionAck}, nil
			})
	}()
	return panicked, subscribeErr
}

func TestNewContractTracingSubscriber_Subscribe_ReturnsErrorOnEmptyContractID(t *testing.T) {
	t.Parallel()
	inner := newLifecycleTracingSubscriber()
	decorated := NewContractTracingSubscriber(inner, wrapper.NoopTracer{})

	panicked, subscribeErr := subscribeCapturingPanic(t, decorated, outbox.Subscription{
		Topic:             "event.legacy.v1",
		ConsumerGroup:     "legacy",
		ContractKind:      "event",
		ContractTransport: "amqp",
	})

	require.Nil(t, panicked, "empty ContractID must surface as error, not panic")
	require.Error(t, subscribeErr, "Subscribe must return error when ContractID is empty")
	assert.Contains(t, subscribeErr.Error(), "ContractID", "error must name the missing field")
	assert.Nil(t, inner.capturedHandler, "inner Subscribe must not run after invalid metadata")
}

func TestNewContractTracingSubscriber_Subscribe_ReturnsErrorOnEmptyContractKind(t *testing.T) {
	t.Parallel()
	inner := newLifecycleTracingSubscriber()
	decorated := NewContractTracingSubscriber(inner, wrapper.NoopTracer{})

	panicked, subscribeErr := subscribeCapturingPanic(t, decorated, outbox.Subscription{
		Topic:             "event.legacy.v1",
		ConsumerGroup:     "legacy",
		ContractID:        "event.legacy.v1",
		ContractTransport: "amqp",
	})

	require.Nil(t, panicked, "empty ContractKind must surface as error, not panic")
	require.Error(t, subscribeErr, "Subscribe must return error when ContractKind is empty")
	assert.Contains(t, subscribeErr.Error(), "ContractKind", "error must name the missing field")
	assert.Nil(t, inner.capturedHandler, "inner Subscribe must not run after invalid metadata")
}

func TestNewContractTracingSubscriber_Subscribe_ReturnsErrorOnEmptyContractTransport(t *testing.T) {
	t.Parallel()
	inner := newLifecycleTracingSubscriber()
	decorated := NewContractTracingSubscriber(inner, wrapper.NoopTracer{})

	panicked, subscribeErr := subscribeCapturingPanic(t, decorated, outbox.Subscription{
		Topic:         "event.legacy.v1",
		ConsumerGroup: "legacy",
		ContractID:    "event.legacy.v1",
		ContractKind:  "event",
	})

	require.Nil(t, panicked, "empty ContractTransport must surface as error, not panic")
	require.Error(t, subscribeErr, "Subscribe must return error when ContractTransport is empty")
	assert.Contains(t, subscribeErr.Error(), "ContractTransport", "error must name the missing field")
	assert.Nil(t, inner.capturedHandler, "inner Subscribe must not run after invalid metadata")
}
