package outbox

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/kernel/metautil"
	"github.com/ghbvf/gocell/pkg/errcode"
)

// ---------------------------------------------------------------------------
// T3: Close(ctx context.Context) error interface tests (R10 SUBSCRIBER-CLOSE-CTX-01)
// ---------------------------------------------------------------------------

// TestSubscriber_Close_AcceptsCtx verifies the Subscriber interface requires
// Close(ctx context.Context) error.
func TestSubscriber_Close_AcceptsCtx(t *testing.T) {
	t.Parallel()
	var sub Subscriber = &mockSubscriberCtx{}
	ctx := context.Background()
	if err := sub.Close(ctx); err != nil {
		t.Fatalf("Close(ctx) returned unexpected error: %v", err)
	}
}

// TestPublisher_Close_AcceptsCtx verifies the Publisher interface requires
// Close(ctx context.Context) error.
func TestPublisher_Close_AcceptsCtx(t *testing.T) {
	t.Parallel()
	var pub Publisher = &mockPublisherCtx{}
	ctx := context.Background()
	if err := pub.Close(ctx); err != nil {
		t.Fatalf("Close(ctx) returned unexpected error: %v", err)
	}
}

// TestSubscriberWithMiddleware_Close_ForwardsCtx verifies that the
// SubscriberWithMiddleware.Close(ctx) forwards the ctx to Inner.Close(ctx).
func TestSubscriberWithMiddleware_Close_ForwardsCtx(t *testing.T) {
	t.Parallel()
	var capturedCtx context.Context
	inner := &mockSubscriberCtx{
		closeFn: func(ctx context.Context) error {
			capturedCtx = ctx
			return nil
		},
	}
	swm, err := NewSubscriberWithMiddleware(inner, testConsumerBase(t))
	if err != nil {
		t.Fatalf("ctor error: %v", err)
	}
	key := contextKey("test-key")
	sentCtx := context.WithValue(context.Background(), key, "test-val")
	if err := swm.Close(sentCtx); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if capturedCtx != sentCtx {
		t.Fatal("Close must forward the ctx to inner Subscriber.Close")
	}
}

// TestSubscriberWithMiddleware_Close_PropagatesCtxErr verifies that when ctx
// is already canceled, the inner Close receives the canceled ctx and can
// return early.
func TestSubscriberWithMiddleware_Close_PropagatesCtxErr(t *testing.T) {
	t.Parallel()
	cancelledCtx, cancel := context.WithCancel(context.Background())
	cancel()

	inner := &mockSubscriberCtx{
		closeFn: func(ctx context.Context) error {
			return ctx.Err()
		},
	}
	swm, ctorErr := NewSubscriberWithMiddleware(inner, testConsumerBase(t))
	if ctorErr != nil {
		t.Fatalf("ctor error: %v", ctorErr)
	}
	err := swm.Close(cancelledCtx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
}

// contextKey is an unexported key type for test context values.
type contextKey string

// mockSubscriberCtx is a minimal Subscriber implementation for T3 tests.
type mockSubscriberCtx struct {
	closeFn func(ctx context.Context) error
}

func (m *mockSubscriberCtx) Setup(_ context.Context, _ Subscription) error { return nil }
func (m *mockSubscriberCtx) Ready(_ Subscription) <-chan struct{} {
	ch := make(chan struct{})
	close(ch)
	return ch
}

func (m *mockSubscriberCtx) Subscribe(_ context.Context, _ Subscription, _ SubscriberHandler) error {
	return nil
}

func (m *mockSubscriberCtx) Close(ctx context.Context) error {
	if m.closeFn != nil {
		return m.closeFn(ctx)
	}
	return nil
}

// mockPublisherCtx is a minimal Publisher implementation for T3 tests.
type mockPublisherCtx struct {
	closeFn func(ctx context.Context) error
}

func (m *mockPublisherCtx) Publish(_ context.Context, _ string, _ []byte) error { return nil }
func (m *mockPublisherCtx) Close(ctx context.Context) error {
	if m.closeFn != nil {
		return m.closeFn(ctx)
	}
	return nil
}

// Compile-time interface checks.

type mockWriter struct{}

func (m *mockWriter) Write(ctx context.Context, entry Entry) error { return nil }

var _ Writer = (*mockWriter)(nil)

type mockRelay struct{}

func (m *mockRelay) Start(ctx context.Context) error { return nil }
func (m *mockRelay) Stop(ctx context.Context) error  { return nil }

var _ Relay = (*mockRelay)(nil)

type mockPublisher struct{}

func (m *mockPublisher) Publish(ctx context.Context, topic string, payload []byte) error { return nil }
func (m *mockPublisher) Close(_ context.Context) error                                   { return nil }

var _ Publisher = (*mockPublisher)(nil)

// plainSubscriber implements Subscriber with no-op methods. Used to test
// that optional capability interfaces (e.g. SubscriberIntakeStopper) are
// detected via type assertion rather than mandatory implementation.
type plainSubscriber struct{}

func (m *plainSubscriber) Setup(_ context.Context, _ Subscription) error { return nil }
func (m *plainSubscriber) Ready(_ Subscription) <-chan struct{} {
	ch := make(chan struct{})
	close(ch)
	return ch
}

func (m *plainSubscriber) Subscribe(_ context.Context, _ Subscription, _ SubscriberHandler) error {
	return nil
}
func (m *plainSubscriber) Close(_ context.Context) error { return nil }

func TestNoopWriter_Write(t *testing.T) {
	writer := NoopWriter{}
	err := writer.Write(context.Background(), validEntry("noop"))
	assert.NoError(t, err)
}

func TestNoopWriter_WriteRejectsInvalidEntry(t *testing.T) {
	writer := NoopWriter{}
	err := writer.Write(context.Background(), Entry{})
	assert.Error(t, err)
}

func TestNoopWriter_WriteBatch(t *testing.T) {
	writer := NoopWriter{}
	err := WriteBatchFallback(context.Background(), writer, []Entry{validEntry("noop-1"), validEntry("noop-2")})
	assert.NoError(t, err)
}

func TestNoopWriter_WriteBatchRejectsInvalidEntry(t *testing.T) {
	writer := NoopWriter{}
	err := writer.WriteBatch(context.Background(), []Entry{validEntry("noop-1"), {}})
	require.Error(t, err)

	var ecErr *errcode.Error
	require.True(t, errors.As(err, &ecErr), "error must be *errcode.Error (runtime data layered into Details, not message)")
	assert.Equal(t, errcode.ErrValidationFailed, ecErr.Code)

	idxAttr, ok := ecErr.FindAttr("entry_index")
	require.True(t, ok, "expected entry_index attribute in Details")
	assert.Equal(t, int64(1), idxAttr.Value.Int64())
}

func TestNoopWriter_Noop(t *testing.T) {
	assert.True(t, NoopWriter{}.Noop())
}

func TestDiscardPublisher_Noop(t *testing.T) {
	assert.True(t, (&DiscardPublisher{}).Noop())
}

// TestDiscardPublisher_Close_NoOp pins the documented contract that Close is
// resource-free: any ctx (including a canceled one) yields nil. Sole guard
// against a future implementation accidentally leaking ctx-aware behavior
// (e.g., returning ctx.Err()) which would surprise callers wiring the publisher
// into a graceful-shutdown chain.
func TestDiscardPublisher_Close_NoOp(t *testing.T) {
	dp := &DiscardPublisher{}
	assert.NoError(t, dp.Close(context.Background()))

	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	assert.NoError(t, dp.Close(canceled), "Close must remain a no-op even with a canceled ctx")
}

func TestDiscardPublisher_IsExplicitDiscardSink(t *testing.T) {
	var publisher Publisher = &DiscardPublisher{}
	err := publisher.Publish(context.Background(), "orders.created", []byte(`{"ok":true}`))
	assert.NoError(t, err)
	assert.True(t, isDiscardPublisher(publisher))
	assert.True(t, isDiscardPublisher(&DiscardPublisher{}), "pointer receiver must also match")
	assert.False(t, isDiscardPublisher(&mockPublisher{}))
	assert.False(t, isDiscardPublisher(nil))
}

type mockSubscriber struct{}

func (m *mockSubscriber) Setup(_ context.Context, _ Subscription) error { return nil }
func (m *mockSubscriber) Ready(_ Subscription) <-chan struct{} {
	ch := make(chan struct{})
	close(ch)
	return ch
}

func (m *mockSubscriber) Subscribe(_ context.Context, _ Subscription, _ SubscriberHandler) error {
	return nil
}
func (m *mockSubscriber) Close(_ context.Context) error { return nil }

var _ Subscriber = (*mockSubscriber)(nil)

func TestSubscriberInterface(t *testing.T) {
	var sub Subscriber = &mockSubscriber{}

	t.Run("Subscribe returns nil on success", func(t *testing.T) {
		handler := func(_ context.Context, _ Entry) (HandleResult, Settlement) {
			return Ack(), nil
		}
		err := sub.Subscribe(context.Background(), testFullSub("test.topic", "cg-test"), handler)
		assert.NoError(t, err)
	})

	t.Run("Close returns nil on success", func(t *testing.T) {
		err := sub.Close(context.Background())
		assert.NoError(t, err)
	})
}

func TestEntryFields(t *testing.T) {
	e := Entry{
		ID:            "1",
		AggregateID:   "a",
		AggregateType: "order",
		EventType:     "created",
		Payload:       []byte("{}"),
		CreatedAt:     time.Now(),
	}
	assert.NotEmpty(t, e.ID)
	assert.NotEmpty(t, e.AggregateID)
	assert.NotEmpty(t, e.AggregateType)
	assert.NotEmpty(t, e.EventType)
	assert.NotEmpty(t, e.Payload)
	assert.False(t, e.CreatedAt.IsZero())
}

// --- SubscriberWithMiddleware Tests ---

// recordingSubscriber captures the handler passed to Subscribe so tests can inspect it.
type recordingSubscriber struct {
	subscribeCalled bool
	subscribeTopic  string
	capturedHandler SubscriberHandler
	closeErr        error
}

func (r *recordingSubscriber) Setup(_ context.Context, _ Subscription) error { return nil }
func (r *recordingSubscriber) Ready(_ Subscription) <-chan struct{} {
	ch := make(chan struct{})
	close(ch)
	return ch
}

func (r *recordingSubscriber) Subscribe(_ context.Context, sub Subscription, handler SubscriberHandler) error {
	r.subscribeCalled = true
	r.subscribeTopic = sub.Topic
	r.capturedHandler = handler
	return nil
}

func (r *recordingSubscriber) Close(_ context.Context) error {
	return r.closeErr
}

var _ Subscriber = (*recordingSubscriber)(nil)

// TestSubscriberWithMiddleware_DoesNotImplementSubscriberInterface verifies that
// SubscriberWithMiddleware intentionally does NOT satisfy the Subscriber
// interface after removing Subscribe(SubscriberHandler). This prevents the
// lift→discard footgun where callers could assign *SubscriberWithMiddleware to
// outbox.Subscriber and bypass the business middleware chain.
func TestSubscriberWithMiddleware_DoesNotImplementSubscriberInterface(t *testing.T) {
	// This must NOT compile if SubscriberWithMiddleware re-gains a Subscribe method:
	//   var _ Subscriber = (*SubscriberWithMiddleware)(nil)
	// The absence of the compile-time check is itself the assertion.
	// Confirm the type only exposes SubscribeEntry (EntryHandler gateway).
	var sub SubscriberWithMiddleware
	_ = sub.SubscribeEntry // only public subscription entry point
	t.Log("SubscriberWithMiddleware.Subscribe deleted; SubscribeEntry is the sole entry point")
}

func TestSubscriberWithMiddleware_NoMiddleware(t *testing.T) {
	inner := &recordingSubscriber{}
	sub, err := NewSubscriberWithMiddleware(inner, testConsumerBase(t))
	require.NoError(t, err)

	called := false
	err = sub.SubscribeEntry(context.Background(), testFullSub("test.topic", "cg-test"),
		func(_ context.Context, _ Entry) HandleResult {
			called = true
			return Ack()
		})
	assert.NoError(t, err)
	assert.True(t, inner.subscribeCalled)
	assert.Equal(t, "test.topic", inner.subscribeTopic)

	// Call the captured handler to verify it's the original.
	res, _ := inner.capturedHandler(context.Background(), Entry{})
	assert.Equal(t, DispositionAck, res.Disposition)
	assert.True(t, called)
}

// TestSubscriberWithMiddleware_SubscribeEntry_RejectsInvalidSubscription
// pins the sub.Validate guard at the SubscribeEntry boundary: a Subscription
// missing the contract triple (ContractID/Kind/Transport) must be rejected
// before any inner Subscribe call, with the underlying *errcode.Error reachable
// via errors.As (no double-wrapping that would erase the inner Code).
func TestSubscriberWithMiddleware_SubscribeEntry_RejectsInvalidSubscription(t *testing.T) {
	inner := &recordingSubscriber{}
	swm, err := NewSubscriberWithMiddleware(inner, testConsumerBase(t))
	require.NoError(t, err)

	// Topic + ConsumerGroup but missing ContractID — Subscription.Validate fails.
	bad := Subscription{Topic: "t", ConsumerGroup: "cg"}
	err = swm.SubscribeEntry(context.Background(), bad,
		func(_ context.Context, _ Entry) HandleResult { return Ack() })

	require.Error(t, err)
	assert.Contains(t, err.Error(), "outbox: SubscriberWithMiddleware",
		"error must carry the operation prefix from the wrapping fmt.Errorf")
	var ecErr *errcode.Error
	require.True(t, errors.As(err, &ecErr),
		"inner *errcode.Error must remain accessible via errors.As (no double-wrap)")
	assert.Equal(t, errcode.ErrValidationFailed, ecErr.Code)
	assert.False(t, inner.subscribeCalled,
		"inner Subscribe must not run when Subscription.Validate fails")
}

// TestNewSubscriberWithMiddleware_RejectsNilInner verifies the constructor
// fail-fasts when the inner Subscriber is nil. This replaces the previous
// per-method ConsumerBase nil check with a structural guarantee.
func TestNewSubscriberWithMiddleware_RejectsNilInner(t *testing.T) {
	swm, err := NewSubscriberWithMiddleware(nil, testConsumerBase(t))
	require.Error(t, err)
	require.Nil(t, swm)

	var ecErr *errcode.Error
	require.True(t, errors.As(err, &ecErr))
	assert.Equal(t, errcode.ErrInternal, ecErr.Code)
	assert.Contains(t, ecErr.Message, "non-nil inner Subscriber")
}

// TestNewSubscriberWithMiddleware_RejectsNilConsumerBase verifies the
// constructor fail-fasts on nil ConsumerBase.
func TestNewSubscriberWithMiddleware_RejectsNilConsumerBase(t *testing.T) {
	inner := &recordingSubscriber{}
	swm, err := NewSubscriberWithMiddleware(inner, nil)
	require.Error(t, err)
	require.Nil(t, swm)

	var ecErr *errcode.Error
	require.True(t, errors.As(err, &ecErr))
	assert.Equal(t, errcode.ErrInternal, ecErr.Code)
	assert.Contains(t, ecErr.Message, "non-nil ConsumerBase")
}

func TestSubscriberWithMiddleware_SingleMiddleware(t *testing.T) {
	inner := &recordingSubscriber{}

	var middlewareTopic string
	middleware := func(sub Subscription, next EntryHandler) EntryHandler {
		middlewareTopic = sub.Topic
		return func(ctx context.Context, e Entry) HandleResult {
			e.Metadata = map[string]string{"wrapped": "true"}
			return next(ctx, e)
		}
	}

	sub, err := NewSubscriberWithMiddleware(inner, testConsumerBase(t), middleware)
	require.NoError(t, err)

	var receivedEntry Entry
	handler := func(_ context.Context, e Entry) HandleResult {
		receivedEntry = e
		return Ack()
	}

	err = sub.SubscribeEntry(context.Background(), testFullSub("orders.created", "cg-orders"), handler)
	assert.NoError(t, err)
	assert.Equal(t, "orders.created", middlewareTopic)

	// Call captured handler to verify middleware was applied.
	res, _ := inner.capturedHandler(context.Background(), Entry{ID: "evt-1"})
	assert.Equal(t, DispositionAck, res.Disposition)
	assert.Equal(t, "evt-1", receivedEntry.ID)
	assert.Equal(t, "true", receivedEntry.Metadata["wrapped"])
}

func TestSubscriberWithMiddleware_MultipleMiddleware_OrderCorrect(t *testing.T) {
	inner := &recordingSubscriber{}

	var order []string

	makeMiddleware := func(name string) SubscriptionMiddleware {
		return func(_ Subscription, next EntryHandler) EntryHandler {
			return func(ctx context.Context, e Entry) HandleResult {
				order = append(order, name+"-before")
				res := next(ctx, e)
				order = append(order, name+"-after")
				return res
			}
		}
	}

	sub, err := NewSubscriberWithMiddleware(
		inner,
		testConsumerBase(t),
		makeMiddleware("outer"),
		makeMiddleware("inner"),
	)
	require.NoError(t, err)

	handler := func(_ context.Context, _ Entry) HandleResult {
		order = append(order, "handler")
		return Ack()
	}

	err = sub.SubscribeEntry(context.Background(), testFullSub("test.topic", "cg-test"), handler)
	assert.NoError(t, err)

	_, _ = inner.capturedHandler(context.Background(), Entry{})

	// [0] is outermost, [len-1] is innermost.
	assert.Equal(t, []string{
		"outer-before",
		"inner-before",
		"handler",
		"inner-after",
		"outer-after",
	}, order)
}

func TestSubscriberWithMiddleware_Close_DelegatesToInner(t *testing.T) {
	inner := &recordingSubscriber{}
	sub, err := NewSubscriberWithMiddleware(inner, testConsumerBase(t))
	require.NoError(t, err)

	assert.NoError(t, sub.Close(context.Background()))
}

func TestSubscriberWithMiddleware_Close_PropagatesError(t *testing.T) {
	inner := &recordingSubscriber{closeErr: assert.AnError}
	sub, err := NewSubscriberWithMiddleware(inner, testConsumerBase(t))
	require.NoError(t, err)

	closeErr := sub.Close(context.Background())
	assert.Error(t, closeErr)
	assert.Equal(t, assert.AnError, closeErr)
}

func TestSubscriberWithMiddleware_MiddlewareCanShortCircuit(t *testing.T) {
	inner := &recordingSubscriber{}

	shortCircuit := func(_ Subscription, _ EntryHandler) EntryHandler {
		return func(_ context.Context, _ Entry) HandleResult {
			return Reject(assert.AnError)
		}
	}

	sub, err := NewSubscriberWithMiddleware(inner, testConsumerBase(t), shortCircuit)
	require.NoError(t, err)

	handlerCalled := false
	handler := func(_ context.Context, _ Entry) HandleResult {
		handlerCalled = true
		return Ack()
	}

	err = sub.SubscribeEntry(context.Background(), testFullSub("test.topic", "cg-test"), handler)
	assert.NoError(t, err)

	// Call captured handler — middleware should short-circuit.
	res, _ := inner.capturedHandler(context.Background(), Entry{})
	assert.Equal(t, DispositionReject, res.Disposition)
	assert.Error(t, res.Err)
	assert.False(t, handlerCalled)
}

// --- SubscriberWithMiddleware Setup delegation ---

// setupSubscriber tracks Setup calls for tests.
type setupSubscriber struct {
	recordingSubscriber
	setupCalled bool
	setupSub    Subscription
	setupErr    error
}

func (s *setupSubscriber) Setup(_ context.Context, sub Subscription) error {
	s.setupCalled = true
	s.setupSub = sub
	return s.setupErr
}

func TestSubscriberWithMiddleware_Setup_DelegatesToInner(t *testing.T) {
	inner := &setupSubscriber{}
	sub, err := NewSubscriberWithMiddleware(inner, testConsumerBase(t))
	require.NoError(t, err)

	setupErr := sub.Setup(context.Background(), Subscription{Topic: "test.topic", ConsumerGroup: "cg-1"})
	assert.NoError(t, setupErr)
	assert.True(t, inner.setupCalled)
	assert.Equal(t, "test.topic", inner.setupSub.Topic)
	assert.Equal(t, "cg-1", inner.setupSub.ConsumerGroup)
}

// TestSubscriberWithMiddleware_Ready_DelegatesToInner pins the contract that
// Ready returns the inner subscriber's channel verbatim. waitForSubscription's
// adapter-contract guard relies on this delegation closing once the inner
// signals ready (rabbitmq pre-closed channel; in-memory bus closes on first
// Subscribe). A regression that returned a fresh, never-closing channel here
// would re-open the OUTBOX-READY-DUAL-BARRIER footgun.
func TestSubscriberWithMiddleware_Ready_DelegatesToInner(t *testing.T) {
	inner := &recordingSubscriber{}
	sub, err := NewSubscriberWithMiddleware(inner, testConsumerBase(t))
	require.NoError(t, err)

	ch := sub.Ready(Subscription{Topic: "t", ConsumerGroup: "cg"})
	select {
	case <-ch:
		// recordingSubscriber.Ready returns a pre-closed channel.
	default:
		t.Fatal("Ready must return the inner subscriber's pre-closed channel verbatim")
	}
}

func TestSubscriberWithMiddleware_Setup_PropagatesError(t *testing.T) {
	inner := &setupSubscriber{setupErr: errors.New("init failed")}
	sub, err := NewSubscriberWithMiddleware(inner, testConsumerBase(t))
	require.NoError(t, err)

	setupErr := sub.Setup(context.Background(), Subscription{Topic: "t", ConsumerGroup: "g"})
	assert.Error(t, setupErr)
	assert.Contains(t, setupErr.Error(), "init failed")
}

func TestEntry_RoutingTopic(t *testing.T) {
	tests := []struct {
		name      string
		entry     Entry
		wantTopic string
	}{
		{
			name: "Topic set — returns Topic",
			entry: Entry{
				EventType: "order.created",
				Topic:     "orders.v2",
			},
			wantTopic: "orders.v2",
		},
		{
			name: "Topic empty — falls back to EventType",
			entry: Entry{
				EventType: "order.created",
				Topic:     "",
			},
			wantTopic: "order.created",
		},
		{
			name: "Topic zero value (not set) — falls back to EventType",
			entry: Entry{
				EventType: "session.created",
			},
			wantTopic: "session.created",
		},
		{
			name:      "Both empty — returns empty string",
			entry:     Entry{},
			wantTopic: "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.wantTopic, tt.entry.RoutingTopic())
		})
	}
}

// --- Disposition Tests ---

func TestDisposition_String(t *testing.T) {
	tests := []struct {
		d    Disposition
		want string
	}{
		{Disposition(0), "invalid"},
		{DispositionAck, "ack"},
		{DispositionRequeue, "requeue"},
		{DispositionReject, "reject"},
		{Disposition(99), "disposition(99)"},
	}
	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			assert.Equal(t, tt.want, tt.d.String())
		})
	}
}

func TestDisposition_Valid(t *testing.T) {
	tests := []struct {
		d    Disposition
		want bool
	}{
		{Disposition(0), false},
		{DispositionAck, true},
		{DispositionRequeue, true},
		{DispositionReject, true},
		{Disposition(99), false},
	}
	for _, tt := range tests {
		t.Run(tt.d.String(), func(t *testing.T) {
			assert.Equal(t, tt.want, tt.d.Valid())
		})
	}
}

func TestDisposition_ZeroValueIsNotAck(t *testing.T) {
	// R-1: The zero value of Disposition MUST NOT equal DispositionAck.
	// A forgotten/uninitialised HandleResult.Disposition must not silently ACK.
	var zero Disposition
	assert.NotEqual(t, DispositionAck, zero,
		"zero-value Disposition must differ from DispositionAck")
	assert.Equal(t, "invalid", zero.String(),
		"zero-value Disposition.String() must return \"invalid\"")
	assert.False(t, zero.Valid(),
		"zero-value Disposition must not be valid")
}

func TestHandleResult_ZeroValueDispositionIsInvalid(t *testing.T) {
	// R-1: HandleResult{} (zero value) must have an invalid Disposition.
	var res HandleResult
	assert.NotEqual(t, DispositionAck, res.Disposition)
	assert.False(t, res.Disposition.Valid())
}

// --- PermanentError Tests ---

func TestPermanentError(t *testing.T) {
	inner := errors.New("bad payload")
	pe := NewPermanentError(inner)

	assert.Equal(t, "permanent: bad payload", pe.Error())
	assert.Equal(t, inner, pe.Unwrap())
	assert.ErrorIs(t, pe, inner)
}

func TestPermanentError_NilErr(t *testing.T) {
	pe := NewPermanentError(nil)
	assert.Equal(t, "permanent: <nil>", pe.Error())
	assert.Nil(t, pe.Unwrap())

	// Zero-value struct — same nil Err path.
	var zero PermanentError
	assert.Equal(t, "permanent: <nil>", zero.Error())
}

func TestPermanentError_ErrorsAs_ThroughWrapping(t *testing.T) {
	inner := errors.New("decode error")
	pe := NewPermanentError(inner)
	wrapped := fmt.Errorf("handler: %w", pe)

	var target *PermanentError
	assert.True(t, errors.As(wrapped, &target))
	assert.Equal(t, inner, target.Err)
}

// --- Entry.Validate Tests (F-OB-03) ---

func TestEntry_Validate(t *testing.T) {
	tests := []struct {
		name    string
		entry   Entry
		wantErr bool
		errMsg  string
	}{
		{
			name:    "valid with Topic",
			entry:   Entry{ID: "evt-1", Topic: "t", Payload: []byte("{}")},
			wantErr: false,
		},
		{
			name:    "valid with EventType fallback",
			entry:   Entry{ID: "evt-2", EventType: "e", Payload: []byte("{}")},
			wantErr: false,
		},
		{
			name:    "missing ID",
			entry:   Entry{Topic: "t", Payload: []byte("{}")},
			wantErr: true,
			errMsg:  "missing ID",
		},
		{
			name:    "missing topic and EventType",
			entry:   Entry{ID: "evt-3", Payload: []byte("{}")},
			wantErr: true,
			errMsg:  "missing topic",
		},
		{
			name:    "missing payload",
			entry:   Entry{ID: "evt-4", Topic: "t"},
			wantErr: true,
			errMsg:  "missing payload",
		},
		{
			name:    "completely empty",
			entry:   Entry{},
			wantErr: true,
			errMsg:  "missing ID",
		},
		{
			// Pins propagation of Observability.Validate errors through
			// Entry.Validate. A regression that drops this branch would let
			// malformed observability IDs (unsafe chars / oversized fields)
			// reach the persistence layer.
			name: "invalid Observability propagates",
			entry: Entry{
				ID: "evt-obs", Topic: "t", Payload: []byte("{}"),
				Observability: ObservabilityMetadata{TraceID: "trace; DROP TABLE"},
			},
			wantErr: true,
			errMsg:  "unsafe characters",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.entry.Validate()
			if tt.wantErr {
				assert.Error(t, err)
				assert.Contains(t, err.Error(), "ERR_VALIDATION_FAILED")
				if tt.errMsg != "" {
					assert.Contains(t, err.Error(), tt.errMsg)
				}
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

// --- WriteBatchFallback Tests (F-OB-01) ---

// batchRecorder implements BatchWriter and records calls.
type batchRecorder struct {
	writeEntries []Entry
	batchEntries []Entry
	writeErr     error
	batchErr     error
}

func (r *batchRecorder) Write(ctx context.Context, entry Entry) error {
	r.writeEntries = append(r.writeEntries, entry)
	return r.writeErr
}

func (r *batchRecorder) WriteBatch(ctx context.Context, entries []Entry) error {
	r.batchEntries = entries
	return r.batchErr
}

var _ BatchWriter = (*batchRecorder)(nil)

// sequentialRecorder implements only Writer (no BatchWriter).
type sequentialRecorder struct {
	entries  []Entry
	writeErr error
	failAt   int // fail on the Nth write (0-based); -1 = never fail
}

func (r *sequentialRecorder) Write(_ context.Context, entry Entry) error {
	if r.failAt >= 0 && len(r.entries) == r.failAt {
		return r.writeErr
	}
	r.entries = append(r.entries, entry)
	return nil
}

var _ Writer = (*sequentialRecorder)(nil)

func validEntry(id string) Entry {
	return Entry{ID: id, Topic: "test.topic", Payload: []byte("{}")}
}

func TestWriteBatchFallback_EmptySlice(t *testing.T) {
	w := &batchRecorder{}
	err := WriteBatchFallback(context.Background(), w, nil)
	assert.NoError(t, err)
	assert.Nil(t, w.batchEntries)
	assert.Nil(t, w.writeEntries)

	err = WriteBatchFallback(context.Background(), w, []Entry{})
	assert.NoError(t, err)
}

func TestWriteBatchFallback_ValidationFailure(t *testing.T) {
	w := &batchRecorder{}
	entries := []Entry{
		validEntry("e1"),
		{ID: "e2", Payload: []byte("{}")}, // missing topic
		validEntry("e3"),
	}

	err := WriteBatchFallback(context.Background(), w, entries)
	require.Error(t, err)

	var ecErr *errcode.Error
	require.True(t, errors.As(err, &ecErr))
	assert.Equal(t, errcode.ErrValidationFailed, ecErr.Code)

	idxAttr, ok := ecErr.FindAttr("entry_index")
	require.True(t, ok, "expected entry_index attribute in Details")
	assert.Equal(t, int64(1), idxAttr.Value.Int64())

	// Cause chain must remain reachable: the inner Entry.Validate failure
	// (a *errcode.Error from kernel/outbox/entry.go) is wrapped into the
	// batch-validation error via errcode.Wrap; future Wrap regressions that
	// drop the Cause field would silently break observability.
	var innerErr *errcode.Error
	require.True(t, errors.As(ecErr.Cause, &innerErr),
		"inner Entry.Validate error must remain accessible via errors.As (errcode.Error)")
	assert.NotEmpty(t, innerErr.Message,
		"inner errcode.Error must carry a descriptive message about the missing field")

	assert.Nil(t, w.batchEntries, "no writes should occur on validation failure")
	assert.Nil(t, w.writeEntries)
}

func TestWriteBatchFallback_UsesBatchWriter(t *testing.T) {
	w := &batchRecorder{}
	entries := []Entry{validEntry("e1"), validEntry("e2")}

	err := WriteBatchFallback(context.Background(), w, entries)
	assert.NoError(t, err)
	assert.Len(t, w.batchEntries, 2)
	assert.Nil(t, w.writeEntries, "should not use sequential Write when BatchWriter is available")
}

func TestWriteBatchFallback_BatchWriterError(t *testing.T) {
	w := &batchRecorder{batchErr: errors.New("batch insert failed")}
	entries := []Entry{validEntry("e1")}

	err := WriteBatchFallback(context.Background(), w, entries)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "batch insert failed")
}

func TestWriteBatchFallback_SequentialFallback(t *testing.T) {
	w := &sequentialRecorder{failAt: -1}
	entries := []Entry{validEntry("e1"), validEntry("e2"), validEntry("e3")}

	err := WriteBatchFallback(context.Background(), w, entries)
	assert.NoError(t, err)
	assert.Len(t, w.entries, 3)
}

func TestWriteBatchFallback_SequentialFallbackError(t *testing.T) {
	w := &sequentialRecorder{
		failAt:   1,
		writeErr: errors.New("db write failed"),
	}
	entries := []Entry{validEntry("e1"), validEntry("e2"), validEntry("e3")}

	err := WriteBatchFallback(context.Background(), w, entries)
	require.Error(t, err)

	var ecErr *errcode.Error
	require.True(t, errors.As(err, &ecErr))
	assert.Equal(t, errcode.ErrInternal, ecErr.Code)

	idxAttr, ok := ecErr.FindAttr("entry_index")
	require.True(t, ok, "expected entry_index attribute in Details")
	assert.Equal(t, int64(1), idxAttr.Value.Int64())

	idAttr, ok := ecErr.FindAttr("entry_id")
	require.True(t, ok, "expected entry_id attribute in Details")
	assert.Equal(t, "e2", idAttr.Value.String())

	assert.ErrorContains(t, err, "db write failed", "wrapped cause must remain reachable via %%w chain")
	assert.Len(t, w.entries, 1, "only the first entry should have been written")
}

// --- HandleResult tests ---

func TestHandleResult_Fields(t *testing.T) {
	res := HandleResult{
		Disposition: DispositionReject,
		Err:         assert.AnError,
	}
	assert.Equal(t, DispositionReject, res.Disposition)
	assert.Error(t, res.Err)
}

// --- Metadata Validation Tests (META-SIZE-01) ---

func TestEntry_Validate_MetadataKeyCount_Exceeds(t *testing.T) {
	e := Entry{ID: "test", EventType: "test.event", Payload: []byte(`{}`)}
	e.Metadata = make(map[string]string)
	for i := range metautil.MaxMetadataKeys + 1 {
		e.Metadata[fmt.Sprintf("key-%d", i)] = "v"
	}
	err := e.Validate()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "metadata key count")
}

func TestEntry_Validate_MetadataKeyLen_Exceeds(t *testing.T) {
	e := Entry{ID: "test", EventType: "test.event", Payload: []byte(`{}`)}
	longKey := strings.Repeat("k", metautil.MaxMetadataKeyLen+1)
	e.Metadata = map[string]string{longKey: "v"}
	err := e.Validate()
	assert.Error(t, err)
	var ecErrKeyLen *errcode.Error
	require.True(t, errors.As(err, &ecErrKeyLen))
	assert.Contains(t, ecErrKeyLen.Message, "metadata key length")
}

func TestEntry_Validate_MetadataValueLen_Exceeds(t *testing.T) {
	e := Entry{ID: "test", EventType: "test.event", Payload: []byte(`{}`)}
	longVal := strings.Repeat("v", metautil.MaxMetadataValueLen+1)
	e.Metadata = map[string]string{"k": longVal}
	err := e.Validate()
	assert.Error(t, err)
	var ecErrValLen *errcode.Error
	require.True(t, errors.As(err, &ecErrValLen))
	assert.Contains(t, ecErrValLen.Message, "metadata value length")
}

func TestEntry_Validate_MetadataTotalSize_Exceeds(t *testing.T) {
	e := Entry{ID: "test", EventType: "test.event", Payload: []byte(`{}`)}
	e.Metadata = make(map[string]string)
	// Fill with entries that individually fit but exceed total.
	val := strings.Repeat("x", metautil.MaxMetadataValueLen)
	for i := range (metautil.MaxMetadataTotalSize / metautil.MaxMetadataValueLen) + 2 {
		e.Metadata[fmt.Sprintf("k%d", i)] = val
	}
	err := e.Validate()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "metadata total size")
}

func TestEntry_Validate_MetadataWithinLimits(t *testing.T) {
	e := Entry{ID: "test", EventType: "test.event", Payload: []byte(`{}`)}
	// Producer-owned domain keys only — observability IDs (trace_id, request_id,
	// ...) live in Entry.Observability, not here.
	e.Metadata = map[string]string{"order_id": "abc123", "tenant": "t-456"}
	assert.NoError(t, e.Validate())
}

func TestEntry_Validate_RejectsReservedMetadataKeys(t *testing.T) {
	for _, k := range ReservedMetadataKeys {
		t.Run(k, func(t *testing.T) {
			e := Entry{
				ID:        "test",
				EventType: "test.event",
				Payload:   []byte(`{}`),
				Metadata:  map[string]string{k: "v"},
			}
			err := e.Validate()
			require.Error(t, err)
			var ecErrRes *errcode.Error
			require.True(t, errors.As(err, &ecErrRes))
			assert.Contains(t, ecErrRes.Message, "reserved")
			assert.Contains(t, ecErrRes.InternalMessage, k)
		})
	}
}

func TestEntry_Validate_NilMetadata_OK(t *testing.T) {
	e := Entry{ID: "test", EventType: "test.event", Payload: []byte(`{}`)}
	assert.NoError(t, e.Validate())
}

func TestEntry_Validate_EmptyMetadata_OK(t *testing.T) {
	e := Entry{ID: "test", EventType: "test.event", Payload: []byte(`{}`)}
	e.Metadata = map[string]string{}
	assert.NoError(t, e.Validate())
}

func TestValidateMetadata_Constants(t *testing.T) {
	// Verify constants match documented values.
	assert.Equal(t, 64, metautil.MaxMetadataKeys)
	assert.Equal(t, 256, metautil.MaxMetadataKeyLen)
	assert.Equal(t, 4096, metautil.MaxMetadataValueLen)
	assert.Equal(t, 65536, metautil.MaxMetadataTotalSize)
}

func TestEntry_Validate_MetadataMultiByteUTF8(t *testing.T) {
	// len() returns byte count, not rune count. A 3-byte CJK character
	// "中" (U+4E2D) counts as 3 bytes toward the key/value limits.
	e := Entry{ID: "test", EventType: "test.event", Payload: []byte(`{}`)}
	cjkKey := strings.Repeat("中", metautil.MaxMetadataKeyLen/3) // each char is 3 bytes
	assert.Less(t, len(cjkKey), metautil.MaxMetadataKeyLen+1, "should fit within byte limit")
	e.Metadata = map[string]string{cjkKey: "value"}
	assert.NoError(t, e.Validate(), "multi-byte key within byte limit should pass")
}

// --- Payload Validation Tests (PAYLOAD-SIZE-01) ---

func TestEntry_Validate_PayloadByteLimit_Exceeds(t *testing.T) {
	e := Entry{
		ID:        "test",
		EventType: "test.event",
		Payload:   make([]byte, MaxPayloadBytes+1),
	}
	err := e.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "payload size")
	assert.Contains(t, err.Error(), "exceeds max")
}

func TestEntry_Validate_PayloadAtExactBoundary(t *testing.T) {
	e := Entry{
		ID:        "test",
		EventType: "test.event",
		Payload:   make([]byte, MaxPayloadBytes),
	}
	assert.NoError(t, e.Validate(), "payload at exactly MaxPayloadBytes must be valid")
}

func TestPayloadConstantsAlign(t *testing.T) {
	// Sentinel that locks the documented value: 1 MiB. Bumping this constant
	// is a deliberate design call (PG TOAST overhead, relay batch memory) and
	// must update this assertion together with operator-facing release notes.
	assert.Equal(t, 1<<20, MaxPayloadBytes)
}

func TestEntry_Validate_MetadataAtExactBoundary(t *testing.T) {
	// Exactly metautil.MaxMetadataKeys keys should pass.
	e := Entry{ID: "test", EventType: "test.event", Payload: []byte(`{}`)}
	e.Metadata = make(map[string]string)
	for i := range metautil.MaxMetadataKeys {
		e.Metadata[fmt.Sprintf("k%02d", i)] = "v"
	}
	assert.NoError(t, e.Validate(), "exactly metautil.MaxMetadataKeys should be valid")

	// Exactly metautil.MaxMetadataKeyLen key should pass.
	e2 := Entry{ID: "test", EventType: "test.event", Payload: []byte(`{}`)}
	exactKey := strings.Repeat("k", metautil.MaxMetadataKeyLen)
	e2.Metadata = map[string]string{exactKey: "v"}
	assert.NoError(t, e2.Validate(), "key at exactly metautil.MaxMetadataKeyLen should be valid")

	// Exactly metautil.MaxMetadataValueLen value should pass.
	e3 := Entry{ID: "test", EventType: "test.event", Payload: []byte(`{}`)}
	exactVal := strings.Repeat("v", metautil.MaxMetadataValueLen)
	e3.Metadata = map[string]string{"k": exactVal}
	assert.NoError(t, e3.Validate(), "value at exactly metautil.MaxMetadataValueLen should be valid")
}

// --- DiscardPublisher Logger + Counter Tests (DISCARD-OBS-01) ---

func TestDiscardPublisher_Logger_Injection(t *testing.T) {
	var buf bytes.Buffer
	handler := slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn})
	logger := slog.New(handler)

	dp := &DiscardPublisher{Logger: logger}
	err := dp.Publish(context.Background(), "test.topic", []byte(`{}`))
	assert.NoError(t, err)
	assert.Contains(t, buf.String(), "test.topic", "injected logger must capture discard warning")
}

func TestDiscardPublisher_Counter_Increments(t *testing.T) {
	dp := &DiscardPublisher{}
	for range 3 {
		err := dp.Publish(context.Background(), "t", []byte(`{}`))
		assert.NoError(t, err)
	}
	assert.Equal(t, uint64(3), dp.DiscardCount())
}

func TestDiscardPublisher_ZeroValue_Safe(t *testing.T) {
	// Zero-value DiscardPublisher{} must work without panic.
	var dp DiscardPublisher
	err := dp.Publish(context.Background(), "t", []byte(`{}`))
	assert.NoError(t, err)
	assert.Equal(t, uint64(1), dp.DiscardCount())
}

// TestSubscriberWithMiddleware_PassesFullSubscription asserts that when Subscribe
// is called with a Subscription, the middleware receives the *full* Subscription
// (both Topic and ConsumerGroup), not only the topic string. Locks the post-PR-A39
// invariant that SubscriptionMiddleware is the only middleware shape (the
// topic-only TopicHandlerMiddleware was deleted with the PR-A39 deprecated nuke).
func TestSubscriberWithMiddleware_PassesFullSubscription(t *testing.T) {
	inner := &recordingSubscriberFull{}

	var capturedSub Subscription
	mw := func(sub Subscription, next EntryHandler) EntryHandler {
		capturedSub = sub
		return next
	}

	swm, err := NewSubscriberWithMiddleware(inner, testConsumerBase(t), mw)
	require.NoError(t, err)

	wantSub := Subscription{
		Topic: "orders.created.v1", ConsumerGroup: "cg-auditcore", CellID: "auditcore",
		ContractID: "event.orders.created.v1", ContractKind: "event", ContractTransport: "memory",
	}
	err = swm.SubscribeEntry(context.Background(), wantSub, func(_ context.Context, _ Entry) HandleResult {
		return Ack()
	})
	assert.NoError(t, err)
	assert.Equal(t, wantSub.Topic, capturedSub.Topic, "middleware must receive Topic")
	assert.Equal(t, wantSub.ConsumerGroup, capturedSub.ConsumerGroup, "middleware must receive ConsumerGroup")
	assert.Equal(t, wantSub.CellID, capturedSub.CellID, "middleware must receive CellID")
}

// recordingSubscriberFull implements the new Subscriber interface (Subscribe takes Subscription).
type recordingSubscriberFull struct {
	subscribedSub Subscription
}

func (r *recordingSubscriberFull) Setup(_ context.Context, _ Subscription) error { return nil }
func (r *recordingSubscriberFull) Ready(_ Subscription) <-chan struct{} {
	ch := make(chan struct{})
	close(ch)
	return ch
}

func (r *recordingSubscriberFull) Subscribe(_ context.Context, sub Subscription, _ SubscriberHandler) error {
	r.subscribedSub = sub
	return nil
}
func (r *recordingSubscriberFull) Close(_ context.Context) error { return nil }

// --- SubscriberIntakeStopper Tests ---

// intakeStopperSubscriber implements Subscriber + SubscriberIntakeStopper.
type intakeStopperSubscriber struct {
	recordingSubscriber
	stopIntakeCalls int
}

func (s *intakeStopperSubscriber) StopIntake(_ context.Context) error {
	s.stopIntakeCalls++
	return nil
}

var (
	_ Subscriber              = (*intakeStopperSubscriber)(nil)
	_ SubscriberIntakeStopper = (*intakeStopperSubscriber)(nil)
)

func TestSubscriberWithMiddleware_ForwardsStopIntake(t *testing.T) {
	inner := &intakeStopperSubscriber{}
	sub, err := NewSubscriberWithMiddleware(inner, testConsumerBase(t))
	require.NoError(t, err)

	stopper, ok := any(sub).(SubscriberIntakeStopper)
	assert.True(t, ok, "SubscriberWithMiddleware must implement SubscriberIntakeStopper when inner does")

	ctx := context.Background()

	assert.NoError(t, stopper.StopIntake(ctx))
	assert.Equal(t, 1, inner.stopIntakeCalls, "StopIntake must be forwarded to inner on first call")

	assert.NoError(t, stopper.StopIntake(ctx), "second call must return nil (idempotent)")
	assert.Equal(t, 2, inner.stopIntakeCalls, "StopIntake must be forwarded to inner on second call")
}

func TestSubscriberWithMiddleware_StopIntake_InnerNotStopper(t *testing.T) {
	inner := &plainSubscriber{}
	sub, err := NewSubscriberWithMiddleware(inner, testConsumerBase(t))
	require.NoError(t, err)

	assert.NoError(t, sub.StopIntake(context.Background()),
		"StopIntake must return nil when inner does not implement SubscriberIntakeStopper")
}

// TestNotifySettlement_ObserverPanic_DoesNotKillCaller verifies that a panicking
// observer does not propagate the panic to the caller and that subsequent
// observers in the chain are still notified.
//
// ref: Finding 3 — observer panic isolation (PR #334 L4 review)
func TestNotifySettlement_ObserverPanic_DoesNotKillCaller(t *testing.T) {
	// Do NOT call t.Parallel(): this test mutates the global slog default logger
	// and must not race with other tests that also call slog.SetDefault.

	// Save the original default logger and restore it after the test so that
	// parallel or sequential sibling tests see the expected global state.
	originalLogger := slog.Default()
	t.Cleanup(func() { slog.SetDefault(originalLogger) })

	// Capture slog output to verify the panic is logged.
	var logBuf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	slog.SetDefault(logger)

	var spy1Called, spy3Called bool

	spy1 := SettlementObserverFunc(func(_ context.Context, _ SettlementObservation) {
		spy1Called = true
	})
	panicObserver := SettlementObserverFunc(func(_ context.Context, _ SettlementObservation) {
		panic("observer panicked intentionally")
	})
	spy3 := SettlementObserverFunc(func(_ context.Context, _ SettlementObservation) {
		spy3Called = true
	})

	result := HandleResult{
		Disposition:         DispositionAck,
		SettlementObservers: []SettlementObserver{spy1, panicObserver, spy3},
	}
	entry := Entry{ID: "test-panic-entry", Topic: "event.test.v1"}

	// Verify NotifySettlement does not re-panic.
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("NotifySettlement must not propagate observer panic, got: %v", r)
		}
	}()

	NotifySettlement(context.Background(), result, entry, DispositionAck, SettlementResultSuccess, nil)

	assert.True(t, spy1Called, "spy1 (before panic observer) must be called")
	assert.True(t, spy3Called, "spy3 (after panic observer) must still be called after panic isolation")
	assert.Contains(t, logBuf.String(), "settlement observer panicked",
		"panic must be logged with structured message")
}

// TestNotifySettlement_NoObservers_NoOp pins the early-return when the result
// carries no observers. Subscriber delivery loops invoke NotifySettlement on
// every message; the no-op path is the hot path and must remain allocation-
// free (no SettlementObservation construction) to avoid GC pressure.
func TestNotifySettlement_NoObservers_NoOp(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("NotifySettlement must be a no-op when no observers attached, got panic: %v", r)
		}
	}()
	NotifySettlement(context.Background(),
		HandleResult{Disposition: DispositionAck},
		Entry{ID: "evt-noop", Topic: "t"},
		DispositionAck, SettlementResultSuccess, nil)
}

// TestNotifySettlement_NilObserverInList_Skipped pins the nil-tolerant
// contract: a nil entry inside SettlementObservers must be skipped without
// panic, and subsequent observers must still be notified. Defends against a
// regression that removes the nil-check, which would NPE on the first nil
// observer and silently drop later observers.
func TestNotifySettlement_NilObserverInList_Skipped(t *testing.T) {
	called := 0
	spy := SettlementObserverFunc(func(_ context.Context, _ SettlementObservation) {
		called++
	})
	result := HandleResult{
		Disposition:         DispositionAck,
		SettlementObservers: []SettlementObserver{nil, spy, nil},
	}

	NotifySettlement(context.Background(), result,
		Entry{ID: "evt-nil-obs", Topic: "t"},
		DispositionAck, SettlementResultSuccess, nil)

	assert.Equal(t, 1, called, "non-nil observer must run exactly once; nil entries skipped")
}

func TestDiscardPublisher_TypedNil_NoPanic(t *testing.T) {
	// Typed nil: interface is non-nil but underlying pointer is nil.
	// Must not panic — this is the key regression from value→pointer migration.
	var p *DiscardPublisher
	var pub Publisher = p // interface non-nil at Go level

	// Go interface nil semantics: pub != nil because it carries type info,
	// but the underlying pointer IS nil — document both with reflect (testify
	// NotNil treats typed-nil as nil, so we check the interface type header
	// directly to express the runtime invariant unambiguously).
	assert.True(t, reflect.ValueOf(pub).IsNil(), "underlying *DiscardPublisher pointer must be nil")
	assert.NotNil(t, reflect.TypeOf(pub), "interface header must carry type info even when underlying pointer is nil")
	assert.NotPanics(t, func() {
		_ = pub.Publish(context.Background(), "test.topic", []byte(`{}`))
	}, "Publish on typed nil must not panic")
	assert.Equal(t, uint64(0), p.DiscardCount(), "DiscardCount on nil returns 0")
}
