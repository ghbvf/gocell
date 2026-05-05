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

func TestNewContractTracingSubscriber_PanicsOnEmptyContractIDAtSubscribe(t *testing.T) {
	t.Parallel()
	inner := newLifecycleTracingSubscriber()
	decorated := NewContractTracingSubscriber(inner, wrapper.NoopTracer{})

	defer func() {
		require.NotNil(t, recover(), "empty ContractID must panic before inner Subscribe starts")
		assert.Nil(t, inner.capturedHandler, "inner Subscribe must not run after invalid contract metadata")
	}()

	_ = decorated.Subscribe(context.Background(), outbox.Subscription{
		Topic:         "event.legacy.v1",
		ConsumerGroup: "legacy",
	}, func(_ context.Context, _ outbox.Entry) (outbox.HandleResult, outbox.Settlement) {
		return outbox.HandleResult{Disposition: outbox.DispositionAck}, nil
	})
}
