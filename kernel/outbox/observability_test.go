package outbox

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/pkg/ctxkeys"
	"github.com/ghbvf/gocell/pkg/idutil"
)

// ---------------------------------------------------------------------------
// ObservabilityMetadata.IsZero
// ---------------------------------------------------------------------------

func TestObservabilityMetadata_IsZero(t *testing.T) {
	t.Run("empty struct is zero", func(t *testing.T) {
		assert.True(t, ObservabilityMetadata{}.IsZero())
	})
	t.Run("TraceID non-empty is not zero", func(t *testing.T) {
		assert.False(t, ObservabilityMetadata{TraceID: "abc"}.IsZero())
	})
	t.Run("TraceParent non-empty is not zero", func(t *testing.T) {
		assert.False(t, ObservabilityMetadata{TraceParent: "00-aaa"}.IsZero())
	})
	t.Run("RequestID non-empty is not zero", func(t *testing.T) {
		assert.False(t, ObservabilityMetadata{RequestID: "req-1"}.IsZero())
	})
	t.Run("CorrelationID non-empty is not zero", func(t *testing.T) {
		assert.False(t, ObservabilityMetadata{CorrelationID: "corr-1"}.IsZero())
	})
}

// TestObservabilityMetadata_IsZero_FieldCoverageInvariant uses reflection
// to assert that IsZero examines every exported field of
// ObservabilityMetadata. This catches the maintenance debt of forgetting
// to extend IsZero when a new field is added — setting any single field
// to a non-zero value MUST produce IsZero() == false.
func TestObservabilityMetadata_IsZero_FieldCoverageInvariant(t *testing.T) {
	typ := reflect.TypeFor[ObservabilityMetadata]()
	require.Equal(t, reflect.Struct, typ.Kind())
	for i := 0; i < typ.NumField(); i++ {
		f := typ.Field(i)
		if !f.IsExported() {
			continue
		}
		t.Run(f.Name, func(t *testing.T) {
			v := reflect.New(typ).Elem()
			fv := v.Field(i)
			require.Truef(t, fv.CanSet(), "field %s must be settable for the invariant check", f.Name)
			switch f.Type.Kind() {
			case reflect.String:
				fv.SetString("non-zero")
			default:
				t.Skipf("ObservabilityMetadata.%s is not a string — extend the invariant test for new field types", f.Name)
			}
			o := v.Interface().(ObservabilityMetadata)
			assert.Falsef(t, o.IsZero(),
				"setting %s to non-empty must make IsZero() return false; "+
					"if you added a new field, extend ObservabilityMetadata.IsZero accordingly",
				f.Name)
		})
	}
}

// ---------------------------------------------------------------------------
// ObservabilityMetadata.Validate (size + safety guards)
// ---------------------------------------------------------------------------

func TestObservabilityMetadata_Validate(t *testing.T) {
	validTP := "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"
	tooLong := strings.Repeat("a", idutil.MaxMetadataIDLen+1)

	cases := []struct {
		name      string
		o         ObservabilityMetadata
		wantError string // empty = expect nil
	}{
		{name: "zero is valid", o: ObservabilityMetadata{}},
		{name: "all fields valid", o: ObservabilityMetadata{
			TraceID: "4bf92f3577b34da6a3ce929d0e0e4736", TraceParent: validTP,
			RequestID: "req-1", CorrelationID: "corr-1",
		}},
		{name: "TraceID too long", o: ObservabilityMetadata{TraceID: tooLong}, wantError: "traceId length"},
		{name: "RequestID too long", o: ObservabilityMetadata{RequestID: tooLong}, wantError: "requestId length"},
		{name: "CorrelationID too long", o: ObservabilityMetadata{CorrelationID: tooLong}, wantError: "correlationId length"},
		{name: "TraceID unsafe chars", o: ObservabilityMetadata{TraceID: "trace; DROP TABLE"}, wantError: "unsafe characters"},
		{name: "TraceParent malformed", o: ObservabilityMetadata{TraceParent: "not-a-valid-traceparent"}, wantError: "valid W3C traceparent"},
		{name: "total exceeds cap", o: ObservabilityMetadata{
			TraceID:       strings.Repeat("a", idutil.MaxMetadataIDLen),
			RequestID:     strings.Repeat("b", idutil.MaxMetadataIDLen),
			CorrelationID: strings.Repeat("c", idutil.MaxMetadataIDLen),
			TraceParent:   validTP, // 55B; total = 768 + 55 = 823 ≤ 1024 — adjust to push over
		}, wantError: ""}, // 823 < 1024, still ok
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.o.Validate()
			if tc.wantError == "" {
				assert.NoError(t, err)
			} else {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tc.wantError)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// ContextObservability
// ---------------------------------------------------------------------------

func TestContextObservability_ReadsAllReservedKeys(t *testing.T) {
	ctx := context.Background()
	ctx = ctxkeys.WithRequestID(ctx, "req-123")
	ctx = ctxkeys.WithCorrelationID(ctx, "corr-123")
	ctx = ctxkeys.WithTraceID(ctx, "trace-123")
	ctx = ctxkeys.WithTraceParent(ctx, "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01")

	got := ContextObservability(ctx)

	assert.Equal(t, "req-123", got.RequestID)
	assert.Equal(t, "corr-123", got.CorrelationID)
	assert.Equal(t, "trace-123", got.TraceID)
	assert.Equal(t, "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01", got.TraceParent)
	assert.False(t, got.IsZero())
}

func TestContextObservability_SynthesizesTraceParentFromTraceAndSpan(t *testing.T) {
	ctx := context.Background()
	ctx = ctxkeys.WithTraceID(ctx, "4bf92f3577b34da6a3ce929d0e0e4736")
	ctx = ctxkeys.WithSpanID(ctx, "00f067aa0ba902b7")

	got := ContextObservability(ctx)

	assert.Equal(t, "4bf92f3577b34da6a3ce929d0e0e4736", got.TraceID)
	assert.Equal(t, "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01", got.TraceParent)
}

func TestContextObservability_UsesContextTraceParentWhenPresent(t *testing.T) {
	ctx := context.Background()
	ctx = ctxkeys.WithTraceID(ctx, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	ctx = ctxkeys.WithSpanID(ctx, "bbbbbbbbbbbbbbbb")
	ctx = ctxkeys.WithTraceParent(ctx, "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01")

	got := ContextObservability(ctx)

	// Explicit traceparent wins over synthesized one.
	assert.Equal(t, "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01", got.TraceParent)
}

func TestContextObservability_EmptyContextReturnsZero(t *testing.T) {
	got := ContextObservability(context.Background())
	assert.True(t, got.IsZero())
}

// ---------------------------------------------------------------------------
// ObservabilityMetadata.RestoreToContext
// ---------------------------------------------------------------------------

func TestObservabilityMetadata_RestoreToContext_RestoresAllFields(t *testing.T) {
	o := ObservabilityMetadata{
		RequestID:     "req-456",
		CorrelationID: "corr-456",
		TraceID:       "4bf92f3577b34da6a3ce929d0e0e4736",
		TraceParent:   "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01",
	}

	ctx := o.RestoreToContext(context.Background())

	requestID, ok := ctxkeys.RequestIDFrom(ctx)
	require.True(t, ok)
	assert.Equal(t, "req-456", requestID)

	correlationID, ok := ctxkeys.CorrelationIDFrom(ctx)
	require.True(t, ok)
	assert.Equal(t, "corr-456", correlationID)

	traceID, ok := ctxkeys.TraceIDFrom(ctx)
	require.True(t, ok)
	assert.Equal(t, "4bf92f3577b34da6a3ce929d0e0e4736", traceID)

	traceParent, ok := ctxkeys.TraceParentFrom(ctx)
	require.True(t, ok)
	assert.Equal(t, "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01", traceParent)
}

func TestObservabilityMetadata_RestoreToContext_IdempotentOverExistingValues(t *testing.T) {
	base := context.Background()
	base = ctxkeys.WithRequestID(base, "req-existing")
	base = ctxkeys.WithTraceID(base, "trace-existing")

	o := ObservabilityMetadata{
		RequestID:     "req-from-obs",
		CorrelationID: "corr-from-obs",
		TraceID:       "4bf92f3577b34da6a3ce929d0e0e4736",
	}

	ctx := o.RestoreToContext(base)

	// Existing context values win.
	requestID, ok := ctxkeys.RequestIDFrom(ctx)
	require.True(t, ok)
	assert.Equal(t, "req-existing", requestID, "existing ctx value must not be overwritten")

	traceID, ok := ctxkeys.TraceIDFrom(ctx)
	require.True(t, ok)
	assert.Equal(t, "trace-existing", traceID, "existing ctx trace_id must not be overwritten")

	// Non-conflicting fields are written.
	correlationID, ok := ctxkeys.CorrelationIDFrom(ctx)
	require.True(t, ok)
	assert.Equal(t, "corr-from-obs", correlationID)
}

func TestObservabilityMetadata_RestoreToContext_RejectsUnsafeValues(t *testing.T) {
	o := ObservabilityMetadata{
		RequestID:     "req-safe-1",
		CorrelationID: "has spaces",
		TraceID:       "has\nnewline",
	}

	ctx := o.RestoreToContext(context.Background())

	requestID, ok := ctxkeys.RequestIDFrom(ctx)
	require.True(t, ok)
	assert.Equal(t, "req-safe-1", requestID, "safe value should be restored")

	_, ok = ctxkeys.CorrelationIDFrom(ctx)
	assert.False(t, ok, "unsafe value with spaces should be rejected")

	_, ok = ctxkeys.TraceIDFrom(ctx)
	assert.False(t, ok, "unsafe value with newlines should be rejected")
}

// TestObservabilityMetadata_RestoreToContext_TraceIDOnlyDoesNotSynthesizeTraceParent
// pins the asymmetry between inject and restore: ContextObservability synthesizes
// a TraceParent from ctx trace_id+span_id at write time when none is set, but
// RestoreToContext must NOT synthesize on the consumer side — the entry already
// carried whatever traceparent the producer captured (or none, deliberately).
// Restoring a metadata with TraceID set but TraceParent empty therefore puts
// only TraceID into ctx and leaves TraceParent absent.
func TestObservabilityMetadata_RestoreToContext_TraceIDOnlyDoesNotSynthesizeTraceParent(t *testing.T) {
	o := ObservabilityMetadata{TraceID: "4bf92f3577b34da6a3ce929d0e0e4736"}
	ctx := o.RestoreToContext(context.Background())

	gotTrace, ok := ctxkeys.TraceIDFrom(ctx)
	require.True(t, ok, "TraceID must be set into ctx")
	assert.Equal(t, "4bf92f3577b34da6a3ce929d0e0e4736", gotTrace)

	_, hasTP := ctxkeys.TraceParentFrom(ctx)
	assert.False(t, hasTP, "RestoreToContext must NOT synthesize TraceParent — the field is producer-captured, not consumer-derived")
}

func TestObservabilityMetadata_RestoreToContext_RejectsOverlongValues(t *testing.T) {
	longID := make([]byte, idutil.MaxMetadataIDLen+1)
	for i := range longID {
		longID[i] = 'a'
	}
	o := ObservabilityMetadata{
		RequestID: string(longID),
	}

	ctx := o.RestoreToContext(context.Background())

	_, ok := ctxkeys.RequestIDFrom(ctx)
	assert.False(t, ok, "overlong value should be rejected")
}

func TestObservabilityMetadata_RestoreToContext_RejectsInvalidTraceParent(t *testing.T) {
	cases := []struct {
		name        string
		traceParent string
	}{
		{
			name:        "malformed trace_id segment",
			traceParent: "00-not-a-valid-trace-id-00f067aa0ba902b7-01",
		},
		{
			name:        "all-zero trace_id rejected per W3C spec",
			traceParent: "00-00000000000000000000000000000000-00f067aa0ba902b7-01",
		},
		{
			name:        "all-zero span_id rejected per W3C spec",
			traceParent: "00-4bf92f3577b34da6a3ce929d0e0e4736-0000000000000000-01",
		},
		{
			name:        "version ff is forbidden per W3C spec",
			traceParent: "ff-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01",
		},
		{
			name:        "uppercase hex rejected per W3C Level 2 lowercase requirement",
			traceParent: "00-4BF92F3577B34DA6A3CE929D0E0E4736-00f067aa0ba902b7-01",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			o := ObservabilityMetadata{TraceParent: tc.traceParent}
			ctx := o.RestoreToContext(context.Background())
			_, ok := ctxkeys.TraceParentFrom(ctx)
			assert.False(t, ok, "invalid traceparent should be rejected: %s", tc.traceParent)
		})
	}
}

func TestObservabilityMetadata_RestoreToContext_TraceParentSeedsTraceIDWhenMissing(t *testing.T) {
	o := ObservabilityMetadata{
		TraceParent: "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01",
	}

	ctx := o.RestoreToContext(context.Background())

	traceParent, ok := ctxkeys.TraceParentFrom(ctx)
	require.True(t, ok)
	assert.Equal(t, "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01", traceParent)

	traceID, ok := ctxkeys.TraceIDFrom(ctx)
	require.True(t, ok)
	assert.Equal(t, "4bf92f3577b34da6a3ce929d0e0e4736", traceID)
}

func TestObservabilityMetadata_RestoreToContext_ZeroStructIsNoOp(t *testing.T) {
	base := context.Background()
	base = ctxkeys.WithRequestID(base, "req-existing")

	ctx := ObservabilityMetadata{}.RestoreToContext(base)

	requestID, ok := ctxkeys.RequestIDFrom(ctx)
	require.True(t, ok)
	assert.Equal(t, "req-existing", requestID)
	_, ok = ctxkeys.TraceIDFrom(ctx)
	assert.False(t, ok)
}

func TestObservabilityMetadata_RestoreToContext_EmptyStringContextValueIsOverwritten(t *testing.T) {
	// Verify the contract: when ctx already holds a key with an empty-string
	// value ("", ok=true), RestoreToContext must overwrite it with the non-empty
	// value from ObservabilityMetadata. An empty existing value does NOT count
	// as "already set" — withContextMetadata guards on existing != "".
	base := context.Background()
	base = ctxkeys.WithRequestID(base, "") // explicit empty — not the same as missing

	o := ObservabilityMetadata{RequestID: "req-nonempty"}
	ctx := o.RestoreToContext(base)

	requestID, ok := ctxkeys.RequestIDFrom(ctx)
	require.True(t, ok)
	assert.Equal(t, "req-nonempty", requestID,
		"empty-string ctx value must be overwritten by non-empty ObservabilityMetadata value")
}

// ---------------------------------------------------------------------------
// Entry.InjectObservabilityFromContext
// ---------------------------------------------------------------------------

func TestEntry_InjectObservabilityFromContext_RoundTrip(t *testing.T) {
	ctx := context.Background()
	ctx = ctxkeys.WithRequestID(ctx, "req-round-trip")
	ctx = ctxkeys.WithCorrelationID(ctx, "corr-round-trip")
	ctx = ctxkeys.WithTraceID(ctx, "4bf92f3577b34da6a3ce929d0e0e4736")
	ctx = ctxkeys.WithTraceParent(ctx, "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01")

	e := Entry{ID: "e1", EventType: "test.v1", Payload: []byte(`{}`)}
	e.InjectObservabilityFromContext(ctx)

	assert.Equal(t, "req-round-trip", e.Observability.RequestID)
	assert.Equal(t, "corr-round-trip", e.Observability.CorrelationID)
	assert.Equal(t, "4bf92f3577b34da6a3ce929d0e0e4736", e.Observability.TraceID)
	assert.Equal(t, "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01", e.Observability.TraceParent)

	// Restore back to a clean context and verify round-trip.
	restored := e.Observability.RestoreToContext(context.Background())

	reqID, ok := ctxkeys.RequestIDFrom(restored)
	require.True(t, ok)
	assert.Equal(t, "req-round-trip", reqID)

	corrID, ok := ctxkeys.CorrelationIDFrom(restored)
	require.True(t, ok)
	assert.Equal(t, "corr-round-trip", corrID)

	traceID, ok := ctxkeys.TraceIDFrom(restored)
	require.True(t, ok)
	assert.Equal(t, "4bf92f3577b34da6a3ce929d0e0e4736", traceID)

	tp, ok := ctxkeys.TraceParentFrom(restored)
	require.True(t, ok)
	assert.Equal(t, "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01", tp)
}

func TestEntry_InjectObservabilityFromContext_OverwritesPriorValue(t *testing.T) {
	e := Entry{ID: "e1", EventType: "test.v1", Payload: []byte(`{}`)}
	e.Observability = ObservabilityMetadata{RequestID: "old-req"}

	ctx := context.Background()
	ctx = ctxkeys.WithRequestID(ctx, "new-req")

	e.InjectObservabilityFromContext(ctx)

	assert.Equal(t, "new-req", e.Observability.RequestID, "InjectObservabilityFromContext must overwrite prior value")
}

func TestEntry_InjectObservabilityFromContext_EmptyContextYieldsZero(t *testing.T) {
	e := Entry{ID: "e1", EventType: "test.v1", Payload: []byte(`{}`)}
	e.InjectObservabilityFromContext(context.Background())
	assert.True(t, e.Observability.IsZero())
}

// ---------------------------------------------------------------------------
// SubscriberWithMiddleware built-in observability restore (replaces the
// pre-A6 ObservabilityContextMiddleware unit tests; restoration is now
// an invariant of SubscriberWithMiddleware.Subscribe, not a separate
// middleware that callers can forget to install).
// ---------------------------------------------------------------------------

// captureSubscriber is a stub Subscriber that captures the handler passed to
// Subscribe so the test can invoke it directly with a synthetic Entry.
type captureSubscriber struct {
	handler EntryHandler
}

func (c *captureSubscriber) Setup(context.Context, Subscription) error { return nil }
func (c *captureSubscriber) Ready(Subscription) <-chan struct{} {
	ch := make(chan struct{})
	close(ch)
	return ch
}
func (c *captureSubscriber) Subscribe(_ context.Context, _ Subscription, h EntryHandler) error {
	c.handler = h
	return nil
}
func (c *captureSubscriber) Close(context.Context) error { return nil }

func TestSubscriberWithMiddleware_BuiltInRestore_RestoresAllFields(t *testing.T) {
	cap := &captureSubscriber{}
	wrapped := &SubscriberWithMiddleware{Inner: cap}

	require.NoError(t, wrapped.Subscribe(context.Background(),
		Subscription{Topic: "event.test.v1"}, func(ctx context.Context, _ Entry) HandleResult {
			requestID, ok := ctxkeys.RequestIDFrom(ctx)
			require.True(t, ok)
			assert.Equal(t, "req-789", requestID)

			correlationID, ok := ctxkeys.CorrelationIDFrom(ctx)
			require.True(t, ok)
			assert.Equal(t, "corr-789", correlationID)

			traceID, ok := ctxkeys.TraceIDFrom(ctx)
			require.True(t, ok)
			assert.Equal(t, "4bf92f3577b34da6a3ce929d0e0e4736", traceID)

			traceParent, ok := ctxkeys.TraceParentFrom(ctx)
			require.True(t, ok)
			assert.Equal(t, "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01", traceParent)

			return HandleResult{Disposition: DispositionAck}
		}))

	require.NotNil(t, cap.handler)
	res := cap.handler(context.Background(), Entry{
		ID: "evt-789",
		Observability: ObservabilityMetadata{
			RequestID:     "req-789",
			CorrelationID: "corr-789",
			TraceID:       "4bf92f3577b34da6a3ce929d0e0e4736",
			TraceParent:   "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01",
		},
	})
	assert.Equal(t, DispositionAck, res.Disposition)
}

func TestSubscriberWithMiddleware_BuiltInRestore_ZeroObservabilityIsNoOp(t *testing.T) {
	cap := &captureSubscriber{}
	wrapped := &SubscriberWithMiddleware{Inner: cap}

	called := false
	require.NoError(t, wrapped.Subscribe(context.Background(),
		Subscription{Topic: "test.v1"}, func(ctx context.Context, _ Entry) HandleResult {
			called = true
			_, ok := ctxkeys.RequestIDFrom(ctx)
			assert.False(t, ok, "no request_id should be set from zero ObservabilityMetadata")
			return HandleResult{Disposition: DispositionAck}
		}))

	require.NotNil(t, cap.handler)
	res := cap.handler(context.Background(), Entry{ID: "e1", Observability: ObservabilityMetadata{}})
	assert.True(t, called)
	assert.Equal(t, DispositionAck, res.Disposition)
}

// TestSubscriberWithMiddleware_RestoreIsOutermost asserts that built-in
// observability restore runs BEFORE any user middleware: a user middleware
// reading ctxkeys.RequestIDFrom must observe the restored value.
func TestSubscriberWithMiddleware_RestoreIsOutermost(t *testing.T) {
	cap := &captureSubscriber{}
	var seenInMiddleware string
	userMW := func(_ Subscription, next EntryHandler) EntryHandler {
		return func(ctx context.Context, entry Entry) HandleResult {
			seenInMiddleware, _ = ctxkeys.RequestIDFrom(ctx)
			return next(ctx, entry)
		}
	}
	wrapped := &SubscriberWithMiddleware{Inner: cap, Middleware: []SubscriptionMiddleware{userMW}}

	require.NoError(t, wrapped.Subscribe(context.Background(), Subscription{Topic: "test.v1"}, func(_ context.Context, _ Entry) HandleResult {
		return HandleResult{Disposition: DispositionAck}
	}))
	require.NotNil(t, cap.handler)
	cap.handler(context.Background(), Entry{
		ID:            "e1",
		Observability: ObservabilityMetadata{RequestID: "req-outermost"},
	})
	assert.Equal(t, "req-outermost", seenInMiddleware,
		"user middleware must observe ctx after built-in observability restore (outermost)")
}

// ---------------------------------------------------------------------------
// Round-trip: Inject → RestoreToContext round-trip via EntryID
// ---------------------------------------------------------------------------

func TestEntryID_RoundTrip_InjectAndRestore(t *testing.T) {
	entryID := MustNewEntryID()
	ctx := ctxkeys.WithRequestID(context.Background(), entryID)

	e := Entry{ID: "e1", EventType: "test.v1", Payload: []byte(`{}`)}
	e.InjectObservabilityFromContext(ctx)

	restored := e.Observability.RestoreToContext(context.Background())
	got, ok := ctxkeys.RequestIDFrom(restored)
	require.True(t, ok)
	assert.Equal(t, entryID, got)
	assert.True(t, idutil.IsSafeID(got))
}
