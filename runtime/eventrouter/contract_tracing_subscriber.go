package eventrouter

import (
	"context"
	"fmt"

	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/kernel/wrapper"
)

// NewContractTracingSubscriber decorates an outbox.Subscriber so every
// contract-bound delivery attempt gets one span that ends after final broker
// settlement. It delegates lifecycle methods unchanged and wraps Subscribe
// handlers with wrapper.MustWrapSubscriber at subscription registration time.
func NewContractTracingSubscriber(inner outbox.Subscriber, tr wrapper.Tracer) outbox.Subscriber {
	return &contractTracingSubscriber{inner: inner, tracer: tr}
}

type contractTracingSubscriber struct {
	inner  outbox.Subscriber
	tracer wrapper.Tracer
}

func (s *contractTracingSubscriber) Setup(ctx context.Context, sub outbox.Subscription) error {
	if s.inner == nil {
		return fmt.Errorf("eventrouter: contract tracing subscriber has nil inner subscriber")
	}
	return s.inner.Setup(ctx, sub)
}

func (s *contractTracingSubscriber) Ready(sub outbox.Subscription) <-chan struct{} {
	if s.inner == nil {
		ch := make(chan struct{})
		close(ch)
		return ch
	}
	return s.inner.Ready(sub)
}

func (s *contractTracingSubscriber) Subscribe(
	ctx context.Context, sub outbox.Subscription, handler outbox.SubscriberHandler,
) error {
	if s.inner == nil {
		return fmt.Errorf("eventrouter: contract tracing subscriber has nil inner subscriber")
	}
	spec := wrapper.ContractSpec{
		ID:        sub.ContractID,
		Kind:      sub.ContractKind,
		Transport: sub.ContractTransport,
		Topic:     sub.Topic,
	}
	wrapped := wrapper.MustWrapSubscriber(s.tracer, spec, handler)
	return s.inner.Subscribe(ctx, sub, wrapped)
}

func (s *contractTracingSubscriber) Close(ctx context.Context) error {
	if s.inner == nil {
		return nil
	}
	return s.inner.Close(ctx)
}

func (s *contractTracingSubscriber) StopIntake(ctx context.Context) error {
	if s.inner == nil {
		return nil
	}
	stopper, ok := s.inner.(outbox.SubscriberIntakeStopper)
	if !ok {
		return nil
	}
	if err := stopper.StopIntake(ctx); err != nil {
		return fmt.Errorf("contract tracing subscriber: stop intake: %w", err)
	}
	return nil
}

var (
	_ outbox.Subscriber              = (*contractTracingSubscriber)(nil)
	_ outbox.SubscriberIntakeStopper = (*contractTracingSubscriber)(nil)
)
