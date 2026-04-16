package outbox

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

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

var _ Publisher = (*mockPublisher)(nil)

// plainSubscriber implements Subscriber but NOT SubscriberInitializer.
type plainSubscriber struct{}

func (m *plainSubscriber) Subscribe(context.Context, string, EntryHandler, string) error { return nil }
func (m *plainSubscriber) Close() error                                                  { return nil }

func TestSubscriberInitializer_IsOptional(t *testing.T) {
	var sub Subscriber = &plainSubscriber{}
	_, ok := sub.(SubscriberInitializer)
	assert.False(t, ok, "plainSubscriber should not implement SubscriberInitializer")
}

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
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "entry[1]")
}

func TestNoopWriter_Noop(t *testing.T) {
	assert.True(t, NoopWriter{}.Noop())
}

func TestDiscardPublisher_Noop(t *testing.T) {
	assert.True(t, (&DiscardPublisher{}).Noop())
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

func (m *mockSubscriber) Subscribe(ctx context.Context, topic string, handler EntryHandler, _ string) error {
	return nil
}
func (m *mockSubscriber) Close() error { return nil }

var _ Subscriber = (*mockSubscriber)(nil)

func TestSubscriberInterface(t *testing.T) {
	var sub Subscriber = &mockSubscriber{}

	t.Run("Subscribe returns nil on success", func(t *testing.T) {
		handler := func(ctx context.Context, entry Entry) HandleResult {
			return HandleResult{Disposition: DispositionAck}
		}
		err := sub.Subscribe(context.Background(), "test.topic", handler, "")
		assert.NoError(t, err)
	})

	t.Run("Close returns nil on success", func(t *testing.T) {
		err := sub.Close()
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
	capturedHandler EntryHandler
	closeErr        error
}

func (r *recordingSubscriber) Subscribe(_ context.Context, topic string, handler EntryHandler, _ string) error {
	r.subscribeCalled = true
	r.subscribeTopic = topic
	r.capturedHandler = handler
	return nil
}

func (r *recordingSubscriber) Close() error {
	return r.closeErr
}

var _ Subscriber = (*recordingSubscriber)(nil)

func TestSubscriberWithMiddleware_InterfaceCompliance(t *testing.T) {
	var _ Subscriber = (*SubscriberWithMiddleware)(nil)
}

func TestSubscriberWithMiddleware_NoMiddleware(t *testing.T) {
	inner := &recordingSubscriber{}
	sub := &SubscriberWithMiddleware{Inner: inner}

	called := false
	handler := func(_ context.Context, _ Entry) HandleResult {
		called = true
		return HandleResult{Disposition: DispositionAck}
	}

	err := sub.Subscribe(context.Background(), "test.topic", handler, "")
	assert.NoError(t, err)
	assert.True(t, inner.subscribeCalled)
	assert.Equal(t, "test.topic", inner.subscribeTopic)

	// Call the captured handler to verify it's the original.
	res := inner.capturedHandler(context.Background(), Entry{})
	assert.Equal(t, DispositionAck, res.Disposition)
	assert.True(t, called)
}

func TestSubscriberWithMiddleware_SingleMiddleware(t *testing.T) {
	inner := &recordingSubscriber{}

	var middlewareTopic string
	middleware := func(topic string, next EntryHandler) EntryHandler {
		middlewareTopic = topic
		return func(ctx context.Context, e Entry) HandleResult {
			e.Metadata = map[string]string{"wrapped": "true"}
			return next(ctx, e)
		}
	}

	sub := &SubscriberWithMiddleware{
		Inner:      inner,
		Middleware: []TopicHandlerMiddleware{middleware},
	}

	var receivedEntry Entry
	handler := func(_ context.Context, e Entry) HandleResult {
		receivedEntry = e
		return HandleResult{Disposition: DispositionAck}
	}

	err := sub.Subscribe(context.Background(), "orders.created", handler, "")
	assert.NoError(t, err)
	assert.Equal(t, "orders.created", middlewareTopic)

	// Call captured handler to verify middleware was applied.
	res := inner.capturedHandler(context.Background(), Entry{ID: "evt-1"})
	assert.Equal(t, DispositionAck, res.Disposition)
	assert.Equal(t, "evt-1", receivedEntry.ID)
	assert.Equal(t, "true", receivedEntry.Metadata["wrapped"])
}

func TestSubscriberWithMiddleware_MultipleMiddleware_OrderCorrect(t *testing.T) {
	inner := &recordingSubscriber{}

	var order []string

	makeMiddleware := func(name string) TopicHandlerMiddleware {
		return func(topic string, next EntryHandler) EntryHandler {
			return func(ctx context.Context, e Entry) HandleResult {
				order = append(order, name+"-before")
				res := next(ctx, e)
				order = append(order, name+"-after")
				return res
			}
		}
	}

	sub := &SubscriberWithMiddleware{
		Inner: inner,
		Middleware: []TopicHandlerMiddleware{
			makeMiddleware("outer"),
			makeMiddleware("inner"),
		},
	}

	handler := func(_ context.Context, _ Entry) HandleResult {
		order = append(order, "handler")
		return HandleResult{Disposition: DispositionAck}
	}

	err := sub.Subscribe(context.Background(), "test.topic", handler, "")
	assert.NoError(t, err)

	_ = inner.capturedHandler(context.Background(), Entry{})

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
	sub := &SubscriberWithMiddleware{Inner: inner}

	err := sub.Close()
	assert.NoError(t, err)
}

func TestSubscriberWithMiddleware_Close_PropagatesError(t *testing.T) {
	inner := &recordingSubscriber{closeErr: assert.AnError}
	sub := &SubscriberWithMiddleware{Inner: inner}

	err := sub.Close()
	assert.Error(t, err)
	assert.Equal(t, assert.AnError, err)
}

func TestSubscriberWithMiddleware_MiddlewareCanShortCircuit(t *testing.T) {
	inner := &recordingSubscriber{}

	shortCircuit := func(_ string, _ EntryHandler) EntryHandler {
		return func(_ context.Context, _ Entry) HandleResult {
			return HandleResult{
				Disposition: DispositionReject,
				Err:         assert.AnError,
			}
		}
	}

	sub := &SubscriberWithMiddleware{
		Inner:      inner,
		Middleware: []TopicHandlerMiddleware{shortCircuit},
	}

	handlerCalled := false
	handler := func(_ context.Context, _ Entry) HandleResult {
		handlerCalled = true
		return HandleResult{Disposition: DispositionAck}
	}

	err := sub.Subscribe(context.Background(), "test.topic", handler, "")
	assert.NoError(t, err)

	// Call captured handler — middleware should short-circuit.
	res := inner.capturedHandler(context.Background(), Entry{})
	assert.Equal(t, DispositionReject, res.Disposition)
	assert.Error(t, res.Err)
	assert.False(t, handlerCalled)
}

// --- SubscriberWithMiddleware + SubscriberInitializer forwarding (F1-1) ---

// initializerSubscriber implements both Subscriber and SubscriberInitializer.
type initializerSubscriber struct {
	recordingSubscriber
	initCalled bool
	initTopic  string
	initGroup  string
	initErr    error
}

func (s *initializerSubscriber) InitializeSubscription(_ context.Context, topic, consumerGroup string) error {
	s.initCalled = true
	s.initTopic = topic
	s.initGroup = consumerGroup
	return s.initErr
}

func TestSubscriberWithMiddleware_ForwardsSubscriberInitializer(t *testing.T) {
	inner := &initializerSubscriber{}
	sub := &SubscriberWithMiddleware{Inner: inner}

	// SubscriberWithMiddleware must implement SubscriberInitializer.
	init, ok := Subscriber(sub).(SubscriberInitializer)
	assert.True(t, ok, "SubscriberWithMiddleware should implement SubscriberInitializer")

	err := init.InitializeSubscription(context.Background(), "test.topic", "cg-1")
	assert.NoError(t, err)
	assert.True(t, inner.initCalled)
	assert.Equal(t, "test.topic", inner.initTopic)
	assert.Equal(t, "cg-1", inner.initGroup)
}

func TestSubscriberWithMiddleware_InitializeSubscription_PropagatesError(t *testing.T) {
	inner := &initializerSubscriber{initErr: errors.New("init failed")}
	sub := &SubscriberWithMiddleware{Inner: inner}

	err := sub.InitializeSubscription(context.Background(), "t", "g")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "init failed")
}

func TestSubscriberWithMiddleware_InitializeSubscription_InnerNotInitializer(t *testing.T) {
	// Inner does NOT implement SubscriberInitializer — must return
	// ErrInitializerNotSupported so callers (e.g., waitForSubscription)
	// can fall back to sleep-based waiting instead of assuming success.
	inner := &recordingSubscriber{}
	sub := &SubscriberWithMiddleware{Inner: inner}

	err := sub.InitializeSubscription(context.Background(), "t", "g")
	assert.ErrorIs(t, err, ErrInitializerNotSupported,
		"must return ErrInitializerNotSupported when inner does not implement SubscriberInitializer")
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

// --- WrapLegacyHandler Tests ---

func TestWrapLegacyHandler_Success(t *testing.T) {
	legacy := func(_ context.Context, _ Entry) error { return nil }
	handler := WrapLegacyHandler(legacy)

	res := handler(context.Background(), Entry{ID: "1"})
	assert.Equal(t, DispositionAck, res.Disposition)
	assert.NoError(t, res.Err)
}

func TestWrapLegacyHandler_Error(t *testing.T) {
	legacy := func(_ context.Context, _ Entry) error { return assert.AnError }
	handler := WrapLegacyHandler(legacy)

	res := handler(context.Background(), Entry{ID: "1"})
	assert.Equal(t, DispositionRequeue, res.Disposition)
	assert.Equal(t, assert.AnError, res.Err)
}

func TestWrapLegacyHandler_PermanentError(t *testing.T) {
	legacy := func(_ context.Context, _ Entry) error {
		return NewPermanentError(errors.New("unmarshal failed"))
	}
	handler := WrapLegacyHandler(legacy)

	res := handler(context.Background(), Entry{ID: "1"})
	assert.Equal(t, DispositionReject, res.Disposition)
	assert.Error(t, res.Err)

	var permErr *PermanentError
	assert.True(t, errors.As(res.Err, &permErr))
}

func TestWrapLegacyHandler_WrappedPermanentError(t *testing.T) {
	legacy := func(_ context.Context, _ Entry) error {
		return fmt.Errorf("handler context: %w", NewPermanentError(errors.New("bad payload")))
	}
	handler := WrapLegacyHandler(legacy)

	res := handler(context.Background(), Entry{ID: "1"})
	assert.Equal(t, DispositionReject, res.Disposition,
		"wrapped PermanentError must be detected via errors.As")
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
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "entry[1]")
	assert.Contains(t, err.Error(), "ERR_VALIDATION_FAILED")
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
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "entry[1]")
	assert.Contains(t, err.Error(), "id=e2")
	assert.Contains(t, err.Error(), "db write failed")
	assert.Len(t, w.entries, 1, "only the first entry should have been written")
}

// --- HandleResult tests ---

func TestHandleResult_Fields(t *testing.T) {
	res := HandleResult{
		Disposition: DispositionReject,
		Err:         assert.AnError,
		Receipt:     nil,
	}
	assert.Equal(t, DispositionReject, res.Disposition)
	assert.Error(t, res.Err)
	assert.Nil(t, res.Receipt)
}

// --- Metadata Validation Tests (META-SIZE-01) ---

func TestEntry_Validate_MetadataKeyCount_Exceeds(t *testing.T) {
	e := Entry{ID: "test", EventType: "test.event", Payload: []byte(`{}`)}
	e.Metadata = make(map[string]string)
	for i := 0; i < MaxMetadataKeys+1; i++ {
		e.Metadata[fmt.Sprintf("key-%d", i)] = "v"
	}
	err := e.Validate()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "metadata key count")
}

func TestEntry_Validate_MetadataKeyLen_Exceeds(t *testing.T) {
	e := Entry{ID: "test", EventType: "test.event", Payload: []byte(`{}`)}
	longKey := strings.Repeat("k", MaxMetadataKeyLen+1)
	e.Metadata = map[string]string{longKey: "v"}
	err := e.Validate()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "metadata key length")
}

func TestEntry_Validate_MetadataValueLen_Exceeds(t *testing.T) {
	e := Entry{ID: "test", EventType: "test.event", Payload: []byte(`{}`)}
	longVal := strings.Repeat("v", MaxMetadataValueLen+1)
	e.Metadata = map[string]string{"k": longVal}
	err := e.Validate()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "metadata value length")
}

func TestEntry_Validate_MetadataTotalSize_Exceeds(t *testing.T) {
	e := Entry{ID: "test", EventType: "test.event", Payload: []byte(`{}`)}
	e.Metadata = make(map[string]string)
	// Fill with entries that individually fit but exceed total.
	val := strings.Repeat("x", MaxMetadataValueLen)
	for i := 0; i < (MaxMetadataTotalSize/MaxMetadataValueLen)+2; i++ {
		e.Metadata[fmt.Sprintf("k%d", i)] = val
	}
	err := e.Validate()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "metadata total size")
}

func TestEntry_Validate_MetadataWithinLimits(t *testing.T) {
	e := Entry{ID: "test", EventType: "test.event", Payload: []byte(`{}`)}
	e.Metadata = map[string]string{"trace_id": "abc123", "request_id": "req-456"}
	assert.NoError(t, e.Validate())
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
	assert.Equal(t, 64, MaxMetadataKeys)
	assert.Equal(t, 256, MaxMetadataKeyLen)
	assert.Equal(t, 4096, MaxMetadataValueLen)
	assert.Equal(t, 65536, MaxMetadataTotalSize)
}

func TestEntry_Validate_MetadataMultiByteUTF8(t *testing.T) {
	// len() returns byte count, not rune count. A 3-byte CJK character
	// "中" (U+4E2D) counts as 3 bytes toward the key/value limits.
	e := Entry{ID: "test", EventType: "test.event", Payload: []byte(`{}`)}
	cjkKey := strings.Repeat("中", MaxMetadataKeyLen/3) // each char is 3 bytes
	assert.Less(t, len(cjkKey), MaxMetadataKeyLen+1, "should fit within byte limit")
	e.Metadata = map[string]string{cjkKey: "value"}
	assert.NoError(t, e.Validate(), "multi-byte key within byte limit should pass")
}

func TestEntry_Validate_MetadataAtExactBoundary(t *testing.T) {
	// Exactly MaxMetadataKeys keys should pass.
	e := Entry{ID: "test", EventType: "test.event", Payload: []byte(`{}`)}
	e.Metadata = make(map[string]string)
	for i := 0; i < MaxMetadataKeys; i++ {
		e.Metadata[fmt.Sprintf("k%02d", i)] = "v"
	}
	assert.NoError(t, e.Validate(), "exactly MaxMetadataKeys should be valid")

	// Exactly MaxMetadataKeyLen key should pass.
	e2 := Entry{ID: "test", EventType: "test.event", Payload: []byte(`{}`)}
	exactKey := strings.Repeat("k", MaxMetadataKeyLen)
	e2.Metadata = map[string]string{exactKey: "v"}
	assert.NoError(t, e2.Validate(), "key at exactly MaxMetadataKeyLen should be valid")

	// Exactly MaxMetadataValueLen value should pass.
	e3 := Entry{ID: "test", EventType: "test.event", Payload: []byte(`{}`)}
	exactVal := strings.Repeat("v", MaxMetadataValueLen)
	e3.Metadata = map[string]string{"k": exactVal}
	assert.NoError(t, e3.Validate(), "value at exactly MaxMetadataValueLen should be valid")
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

func TestDiscardPublisher_TypedNil_NoPanic(t *testing.T) {
	// Typed nil: interface is non-nil but underlying pointer is nil.
	// Must not panic — this is the key regression from value→pointer migration.
	var p *DiscardPublisher //nolint:staticcheck // SA4023: typed nil used to verify interface-nil semantics below
	var pub Publisher = p   // interface non-nil at Go level

	// Go interface nil semantics: pub != nil because it carries type info.
	// The comparison is tautologically false at compile time; staticcheck
	// (SA4023) flags it but it documents the invariant the test guards.
	if pub == nil { //nolint:staticcheck // SA4023: intentional — asserts typed-nil wrapped in interface is not == nil
		t.Fatal("typed nil wrapped in interface should not be == nil")
	}
	assert.NotPanics(t, func() {
		_ = pub.Publish(context.Background(), "test.topic", []byte(`{}`))
	}, "Publish on typed nil must not panic")
	assert.Equal(t, uint64(0), p.DiscardCount(), "DiscardCount on nil returns 0")
}
