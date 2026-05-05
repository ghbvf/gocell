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

// TestContractTracingSubscriber_Setup_ValidatesSubscription locks the
// lifecycle-gap fix from the multi-role review (P2 #1): Setup must reject
// a malformed Subscription with the same gate as Subscribe. Without this,
// a misconfigured caller would pre-create broker topology only to fail
// later at Subscribe — leaving a dangling queue/binding behind.
func TestContractTracingSubscriber_Setup_ValidatesSubscription(t *testing.T) {
	t.Parallel()
	inner := newLifecycleTracingSubscriber()
	decorated := NewContractTracingSubscriber(inner, wrapper.NoopTracer{})

	err := decorated.Setup(context.Background(), outbox.Subscription{
		Topic:         "event.gap.v1",
		ConsumerGroup: "gap",
		// ContractID/Kind/Transport intentionally omitted.
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Setup", "error must identify the lifecycle phase")
	assert.Contains(t, err.Error(), "ContractID", "error must name the missing field")
	assert.False(t, inner.setupCalled, "inner Setup must not run after invalid metadata")
}

// TestContractTracingSubscriber_NilInnerLifecycle covers the defensive
// branches that surface when the decorator is constructed with a nil
// inner subscriber. Each lifecycle method must degrade safely instead of
// dereferencing nil.
func TestContractTracingSubscriber_NilInnerLifecycle(t *testing.T) {
	t.Parallel()
	decorated := NewContractTracingSubscriber(nil, wrapper.NoopTracer{})
	sub := outbox.Subscription{
		Topic:             "event.nilinner.v1",
		ConsumerGroup:     "nilinner",
		ContractID:        "event.nilinner.v1",
		ContractKind:      "event",
		ContractTransport: "amqp",
	}

	require.Error(t, decorated.Setup(context.Background(), sub),
		"Setup must error when inner is nil")

	ch := decorated.Ready(sub)
	select {
	case <-ch:
		// expected — Ready returns a closed chan when inner is nil.
	default:
		t.Fatal("Ready must return a closed chan when inner is nil")
	}

	require.Error(t, decorated.Subscribe(context.Background(), sub,
		func(_ context.Context, _ outbox.Entry) (outbox.HandleResult, outbox.Settlement) {
			return outbox.HandleResult{Disposition: outbox.DispositionAck}, nil
		}),
		"Subscribe must error when inner is nil")

	require.NoError(t, decorated.Close(context.Background()),
		"Close must be a no-op when inner is nil")

	stopper, ok := decorated.(outbox.SubscriberIntakeStopper)
	require.True(t, ok)
	require.NoError(t, stopper.StopIntake(context.Background()),
		"StopIntake must be a no-op when inner is nil")
}

// minimalSubscriber implements outbox.Subscriber WITHOUT
// SubscriberIntakeStopper to exercise the StopIntake type-assertion fallback
// in the decorator: when the inner subscriber lacks StopIntake, the decorator
// must return nil rather than dereferencing a missing method.
func newNonStopperSubscriber() *minimalSubscriber {
	ch := make(chan struct{})
	close(ch)
	return &minimalSubscriber{readyCh: ch}
}

type minimalSubscriber struct {
	setupCalled bool
	closeCalled bool
	readyCh     chan struct{}
}

func (s *minimalSubscriber) Setup(_ context.Context, _ outbox.Subscription) error {
	s.setupCalled = true
	return nil
}

func (s *minimalSubscriber) Ready(_ outbox.Subscription) <-chan struct{} {
	return s.readyCh
}

func (s *minimalSubscriber) Subscribe(_ context.Context, _ outbox.Subscription, _ outbox.SubscriberHandler) error {
	return nil
}

func (s *minimalSubscriber) Close(_ context.Context) error {
	s.closeCalled = true
	return nil
}

// TestContractTracingSubscriber_StopIntake_NoOpWhenInnerLacksStopper covers
// the StopIntake type-assertion fallback: when the inner subscriber does
// not implement SubscriberIntakeStopper, the decorator must return nil
// rather than dereferencing a missing method.
func TestContractTracingSubscriber_StopIntake_NoOpWhenInnerLacksStopper(t *testing.T) {
	t.Parallel()
	inner := newNonStopperSubscriber()
	decorated := NewContractTracingSubscriber(inner, wrapper.NoopTracer{})

	stopper, ok := decorated.(outbox.SubscriberIntakeStopper)
	require.True(t, ok, "decorator must always advertise StopIntake")
	require.NoError(t, stopper.StopIntake(context.Background()),
		"StopIntake must be a no-op when inner does not implement SubscriberIntakeStopper")
}

// stopIntakeErrSubscriber surfaces an explicit StopIntake error so we can
// cover the wrap-and-return branch in the decorator (line 92).
type stopIntakeErrSubscriber struct {
	lifecycleTracingSubscriber
	err error
}

func (s *stopIntakeErrSubscriber) StopIntake(_ context.Context) error {
	s.stopIntakeCalled = true
	return s.err
}

func TestContractTracingSubscriber_StopIntake_PropagatesInnerError(t *testing.T) {
	t.Parallel()
	inner := &stopIntakeErrSubscriber{
		lifecycleTracingSubscriber: lifecycleTracingSubscriber{readyCh: closedChan()},
		err:                        assert.AnError,
	}
	decorated := NewContractTracingSubscriber(inner, wrapper.NoopTracer{})

	stopper, ok := decorated.(outbox.SubscriberIntakeStopper)
	require.True(t, ok)
	err := stopper.StopIntake(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "stop intake",
		"error must wrap with the lifecycle phase for ops triage")
}

// TestContractTracingSubscriber_Subscribe_PropagatesWrapSubscriberError
// covers the case where wrapper.WrapSubscriber rejects the constructed spec
// (e.g. ContractKind != "event"). Subscription.Validate accepts arbitrary
// non-empty ContractKind strings, so this branch is reachable only when an
// upstream caller mis-populates the field.
func TestContractTracingSubscriber_Subscribe_PropagatesWrapSubscriberError(t *testing.T) {
	t.Parallel()
	inner := newLifecycleTracingSubscriber()
	decorated := NewContractTracingSubscriber(inner, wrapper.NoopTracer{})

	err := decorated.Subscribe(context.Background(), outbox.Subscription{
		Topic:             "event.bad-kind.v1",
		ConsumerGroup:     "badkind",
		ContractID:        "event.bad-kind.v1",
		ContractKind:      "command", // WrapSubscriber rejects non-"event" kinds.
		ContractTransport: "amqp",
	}, func(_ context.Context, _ outbox.Entry) (outbox.HandleResult, outbox.Settlement) {
		return outbox.HandleResult{Disposition: outbox.DispositionAck}, nil
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "must be \"event\"",
		"error must surface the WrapSubscriber kind assertion")
	assert.Nil(t, inner.capturedHandler,
		"inner Subscribe must not run after WrapSubscriber rejects the spec")
}

func closedChan() chan struct{} {
	ch := make(chan struct{})
	close(ch)
	return ch
}
