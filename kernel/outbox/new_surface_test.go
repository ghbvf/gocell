package outbox

// new_surface_test.go covers the sealed-construction surface introduced by
// issue #1229: NewEntry (the sole producer constructor), its EntryOption set,
// ctx-driven observability/principal injection, the EntryScan reconstruction
// funnel, the value-receiver getters, and the PrincipalMetadata value type.
//
// Construction is exercised in-package (lowercase field reads are legal here),
// using a FakeClock so the createdAt/occurredAt stamps are deterministic.

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/kernel/clock/clockmock"
	"github.com/ghbvf/gocell/pkg/ctxkeys"
	"github.com/ghbvf/gocell/pkg/idutil"
	"github.com/ghbvf/gocell/pkg/tenant"
)

// scopeTenantUUID is a canonical tenant UUID used by the scope-fallback
// principal-injection tests (#1618 F1).
const scopeTenantUUID = "3f2504e0-4f89-41d3-9a0c-0305e82c3301"

// fixedClockTime is the deterministic instant the FakeClock reports; chosen
// non-UTC-offset-zero so the .UTC() normalization in NewEntry is observable.
var fixedClockTime = time.Date(2026, 5, 29, 8, 30, 15, 0, time.FixedZone("KST", 9*3600))

func newSurfaceClock() *clockmock.FakeClock { return clockmock.New(fixedClockTime) }

// --- NewEntry defaults ---------------------------------------------------

func TestNewEntry_Defaults(t *testing.T) {
	clk := newSurfaceClock()
	e, err := NewEntry(clk, context.Background(), "test.event.v1", []byte(`{"k":"v"}`))
	require.NoError(t, err)

	assert.NotEmpty(t, e.id, "id must be auto-generated")
	assert.True(t, strings.HasPrefix(e.id, EntryIDPrefix), "auto id must carry the evt- prefix")
	assert.Equal(t, "test.event.v1", e.eventType)
	assert.Equal(t, []byte(`{"k":"v"}`), e.payload)

	want := fixedClockTime.UTC()
	assert.True(t, e.createdAt.Equal(want), "createdAt must be clk.Now().UTC()")
	assert.True(t, e.occurredAt.Equal(want), "occurredAt defaults to createdAt")
	assert.False(t, e.createdAt.IsZero())
	assert.False(t, e.occurredAt.IsZero())
	assert.Equal(t, time.UTC, e.createdAt.Location(), "createdAt must be normalized to UTC")
}

// --- NewEntry options ----------------------------------------------------

func TestNewEntry_Options(t *testing.T) {
	clk := newSurfaceClock()
	domainTime := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	sealTime := time.Date(2026, 1, 2, 4, 0, 0, 0, time.UTC)

	e, err := NewEntry(
		clk, context.Background(), "order.created.v1", []byte(`{}`),
		WithID("evt-fixed"),
		WithAggregateID("agg-1"),
		WithAggregateType("Order"),
		WithTopic("orders.routing.v1"),
		WithMetadata(map[string]string{"order_id": "o-1"}),
		WithFailurePolicy(FailurePolicyFailClosed),
		WithOccurredAt(domainTime),
		WithCreatedAt(sealTime),
	)
	require.NoError(t, err)

	assert.Equal(t, "evt-fixed", e.id, "WithID overrides the auto id")
	assert.Equal(t, "agg-1", e.aggregateID)
	assert.Equal(t, "Order", e.aggregateType)
	assert.Equal(t, "orders.routing.v1", e.topic)
	assert.Equal(t, "orders.routing.v1", e.RoutingTopic(), "RoutingTopic prefers explicit topic")
	assert.Equal(t, map[string]string{"order_id": "o-1"}, e.metadata)
	assert.Equal(t, FailurePolicyFailClosed, e.failurePolicy)
	assert.True(t, e.occurredAt.Equal(domainTime), "WithOccurredAt overrides occurredAt")
	assert.True(t, e.createdAt.Equal(sealTime), "WithCreatedAt overrides createdAt")
}

func TestNewEntry_RoutingTopicFallsBackToEventType(t *testing.T) {
	e, err := NewEntry(newSurfaceClock(), context.Background(), "no.topic.v1", []byte(`{}`))
	require.NoError(t, err)
	assert.Empty(t, e.topic)
	assert.Equal(t, "no.topic.v1", e.RoutingTopic(), "RoutingTopic falls back to eventType when topic empty")
}

func TestNewEntry_WithMetadataClones(t *testing.T) {
	src := map[string]string{"k": "v"}
	e, err := NewEntry(newSurfaceClock(), context.Background(), "t.v1", []byte(`{}`),
		WithTopic("t.v1"), WithMetadata(src))
	require.NoError(t, err)

	// Mutating the source map after construction must not reach the sealed entry.
	src["k"] = "MUTATED"
	src["new"] = "x"
	assert.Equal(t, map[string]string{"k": "v"}, e.metadata,
		"WithMetadata must clone so later source mutation is isolated")
}

func TestNewEntry_WithMetadataNilClearsMap(t *testing.T) {
	e, err := NewEntry(newSurfaceClock(), context.Background(), "t.v1", []byte(`{}`),
		WithTopic("t.v1"), WithMetadata(nil))
	require.NoError(t, err)
	assert.Nil(t, e.metadata)
}

// --- NewEntry ctx injection ----------------------------------------------

func TestNewEntry_InjectsPrincipalFromContext(t *testing.T) {
	ctx := context.Background()
	ctx = ctxkeys.WithActorID(ctx, "actor-1")
	ctx = ctxkeys.WithSubjectID(ctx, "subject-1")
	ctx = ctxkeys.WithTenantID(ctx, "tenant-1")
	ctx = ctxkeys.WithSessionID(ctx, "session-1")

	e, err := NewEntry(newSurfaceClock(), ctx, "t.v1", []byte(`{}`), WithTopic("t.v1"))
	require.NoError(t, err)

	assert.Equal(t, idutil.SafeID("actor-1"), e.principal.ActorID)
	assert.Equal(t, idutil.SafeID("subject-1"), e.principal.SubjectID)
	assert.Equal(t, idutil.SafeID("tenant-1"), e.principal.TenantID)
	assert.Equal(t, idutil.SafeID("session-1"), e.principal.SessionID)
}

func TestNewEntry_InjectsObservabilityFromContext(t *testing.T) {
	ctx := context.Background()
	ctx = ctxkeys.WithRequestID(ctx, "req-1")
	ctx = ctxkeys.WithCorrelationID(ctx, "corr-1")
	ctx = ctxkeys.WithTraceID(ctx, "4bf92f3577b34da6a3ce929d0e0e4736")

	e, err := NewEntry(newSurfaceClock(), ctx, "t.v1", []byte(`{}`), WithTopic("t.v1"))
	require.NoError(t, err)

	assert.Equal(t, idutil.SafeID("req-1"), e.observability.RequestID)
	assert.Equal(t, idutil.SafeID("corr-1"), e.observability.CorrelationID)
	assert.Equal(t, idutil.SafeID("4bf92f3577b34da6a3ce929d0e0e4736"), e.observability.TraceID)
}

func TestNewEntry_EmptyContextYieldsZeroIdentity(t *testing.T) {
	e, err := NewEntry(newSurfaceClock(), context.Background(), "t.v1", []byte(`{}`), WithTopic("t.v1"))
	require.NoError(t, err)
	assert.True(t, e.principal.IsZero(), "no ctx principal → zero PrincipalMetadata")
	assert.True(t, e.observability.IsZero(), "no ctx observability → zero ObservabilityMetadata")
}

// --- NewEntry validation rejection ---------------------------------------

func TestNewEntry_ValidateRejections(t *testing.T) {
	clk := newSurfaceClock()
	tests := []struct {
		name      string
		eventType string
		payload   []byte
		opts      []EntryOption
		ctx       context.Context
	}{
		{
			name:      "reserved metadata key",
			eventType: "t.v1",
			payload:   []byte(`{}`),
			opts:      []EntryOption{WithTopic("t.v1"), WithMetadata(map[string]string{"trace_id": "x"})},
		},
		{
			name:      "oversized payload",
			eventType: "t.v1",
			payload:   make([]byte, MaxPayloadBytes+1),
			opts:      []EntryOption{WithTopic("t.v1")},
		},
		{
			name:      "empty payload",
			eventType: "t.v1",
			payload:   nil,
			opts:      []EntryOption{WithTopic("t.v1")},
		},
		{
			name:      "unsafe id",
			eventType: "t.v1",
			payload:   []byte(`{}`),
			opts:      []EntryOption{WithTopic("t.v1"), WithID("evt-\nbad")},
		},
		{
			name:      "missing topic and eventType",
			eventType: "",
			payload:   []byte(`{}`),
			opts:      nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := tt.ctx
			if ctx == nil {
				ctx = context.Background()
			}
			_, err := NewEntry(clk, ctx, tt.eventType, tt.payload, tt.opts...)
			require.Error(t, err, "NewEntry must reject the invalid entry shape")
		})
	}
}

// --- Getters -------------------------------------------------------------

func TestEntry_Getters(t *testing.T) {
	ctx := ctxkeys.WithSubjectID(context.Background(), "subject-g")
	ctx = ctxkeys.WithRequestID(ctx, "req-g")

	e, err := NewEntry(
		newSurfaceClock(), ctx, "evt.type.v1", []byte(`{"p":1}`),
		WithID("evt-getter"),
		WithAggregateID("agg-g"),
		WithAggregateType("Thing"),
		WithTopic("evt.topic.v1"),
		WithMetadata(map[string]string{"biz": "1"}),
		WithFailurePolicy(FailurePolicyFailOpen),
	)
	require.NoError(t, err)

	assert.Equal(t, "evt-getter", e.ID())
	assert.Equal(t, "agg-g", e.AggregateID())
	assert.Equal(t, "Thing", e.AggregateType())
	assert.Equal(t, "evt.type.v1", e.EventType())
	assert.Equal(t, "evt.topic.v1", e.Topic())
	assert.Equal(t, "evt.topic.v1", e.RoutingTopic())
	assert.Equal(t, []byte(`{"p":1}`), e.Payload())
	assert.Equal(t, FailurePolicyFailOpen, e.FailurePolicy())
	assert.True(t, e.CreatedAt().Equal(fixedClockTime.UTC()))
	assert.True(t, e.OccurredAt().Equal(fixedClockTime.UTC()))
	assert.Equal(t, idutil.SafeID("subject-g"), e.Principal().SubjectID)
	assert.Equal(t, idutil.SafeID("req-g"), e.Observability().RequestID)
}

func TestEntry_MetadataGetterReturnsClone(t *testing.T) {
	e, err := NewEntry(newSurfaceClock(), context.Background(), "t.v1", []byte(`{}`),
		WithTopic("t.v1"), WithMetadata(map[string]string{"k": "v"}))
	require.NoError(t, err)

	md := e.Metadata()
	md["k"] = "MUTATED"
	md["new"] = "x"

	assert.Equal(t, map[string]string{"k": "v"}, e.Metadata(),
		"Metadata() must return a fresh clone each call so mutation does not leak")
}

func TestEntry_MetadataGetterNilWhenUnset(t *testing.T) {
	e, err := NewEntry(newSurfaceClock(), context.Background(), "t.v1", []byte(`{}`), WithTopic("t.v1"))
	require.NoError(t, err)
	assert.Nil(t, e.Metadata())
}

// --- EntryScan reconstruction funnel -------------------------------------

func TestEntryScan_ToEntry_RoundTrip(t *testing.T) {
	now := time.Date(2026, 5, 29, 0, 0, 0, 0, time.UTC)
	scan := EntryScan{
		ID:            "evt-scan",
		AggregateID:   "agg-s",
		AggregateType: "Session",
		EventType:     "session.created.v1",
		Topic:         "session.topic.v1",
		Payload:       []byte(`{"s":1}`),
		CreatedAt:     now,
		OccurredAt:    now,
		Metadata:      map[string]string{"k": "v"},
		Observability: ObservabilityMetadata{RequestID: "req-s"},
		Principal:     PrincipalMetadata{SubjectID: "subject-s"},
		FailurePolicy: FailurePolicyFailClosed,
	}

	e, err := scan.ToEntry()
	require.NoError(t, err)

	assert.Equal(t, "evt-scan", e.id)
	assert.Equal(t, "agg-s", e.aggregateID)
	assert.Equal(t, "Session", e.aggregateType)
	assert.Equal(t, "session.created.v1", e.eventType)
	assert.Equal(t, "session.topic.v1", e.topic)
	assert.Equal(t, []byte(`{"s":1}`), e.payload)
	assert.True(t, e.createdAt.Equal(now))
	assert.True(t, e.occurredAt.Equal(now))
	assert.Equal(t, map[string]string{"k": "v"}, e.metadata)
	assert.Equal(t, idutil.SafeID("req-s"), e.observability.RequestID)
	assert.Equal(t, idutil.SafeID("subject-s"), e.principal.SubjectID)
	assert.Equal(t, FailurePolicyFailClosed, e.failurePolicy)
}

func TestEntryScan_ToEntry_Rejections(t *testing.T) {
	now := time.Date(2026, 5, 29, 0, 0, 0, 0, time.UTC)
	tests := []struct {
		name string
		scan EntryScan
	}{
		{
			name: "zero occurredAt",
			scan: EntryScan{ID: "evt-1", EventType: "t.v1", Topic: "t.v1", Payload: []byte(`{}`), CreatedAt: now},
		},
		{
			name: "unsafe id",
			scan: EntryScan{ID: "evt-\nbad", EventType: "t.v1", Topic: "t.v1", Payload: []byte(`{}`), CreatedAt: now, OccurredAt: now},
		},
		{
			name: "missing payload",
			scan: EntryScan{ID: "evt-1", EventType: "t.v1", Topic: "t.v1", CreatedAt: now, OccurredAt: now},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := tt.scan.ToEntry()
			require.Error(t, err, "ToEntry must re-run Entry.Validate and reject the invalid row")
		})
	}
}

// --- PrincipalMetadata ---------------------------------------------------

func TestPrincipalMetadata_IsZero(t *testing.T) {
	assert.True(t, PrincipalMetadata{}.IsZero())
	assert.False(t, PrincipalMetadata{ActorID: "a"}.IsZero())
	assert.False(t, PrincipalMetadata{SubjectID: "s"}.IsZero())
	assert.False(t, PrincipalMetadata{TenantID: "t"}.IsZero())
	assert.False(t, PrincipalMetadata{SessionID: "x"}.IsZero())
}

func TestPrincipalMetadata_Validate(t *testing.T) {
	assert.NoError(t, PrincipalMetadata{}.Validate(), "zero value is valid")
	assert.NoError(t, PrincipalMetadata{
		ActorID: "a-1", SubjectID: "s-1", TenantID: "t-1", SessionID: "x-1",
	}.Validate())

	err := PrincipalMetadata{SubjectID: idutil.SafeID("subject\nINJECT")}.Validate()
	require.Error(t, err, "unsafe SafeID must be rejected")
}

func TestPrincipalMetadata_RestoreToContext(t *testing.T) {
	p := PrincipalMetadata{
		ActorID: "actor-r", SubjectID: "subject-r", TenantID: "tenant-r", SessionID: "session-r",
	}
	ctx := p.RestoreToContext(context.Background())

	got, ok := ctxkeys.ActorIDFrom(ctx)
	require.True(t, ok)
	assert.Equal(t, "actor-r", got)
	got, ok = ctxkeys.SubjectIDFrom(ctx)
	require.True(t, ok)
	assert.Equal(t, "subject-r", got)
	got, ok = ctxkeys.TenantIDFrom(ctx)
	require.True(t, ok)
	assert.Equal(t, "tenant-r", got)
	got, ok = ctxkeys.SessionIDFrom(ctx)
	require.True(t, ok)
	assert.Equal(t, "session-r", got)
}

func TestPrincipalMetadata_RestoreToContext_ExistingValueWins(t *testing.T) {
	ctx := ctxkeys.WithSubjectID(context.Background(), "incumbent")
	p := PrincipalMetadata{SubjectID: "challenger"}

	restored := p.RestoreToContext(ctx)
	got, ok := ctxkeys.SubjectIDFrom(restored)
	require.True(t, ok)
	assert.Equal(t, "incumbent", got,
		"RestoreToContext must not stomp an existing ctx principal (idempotent restore)")
}

func TestContextPrincipal(t *testing.T) {
	t.Run("reads ctxkeys", func(t *testing.T) {
		ctx := context.Background()
		ctx = ctxkeys.WithActorID(ctx, "a")
		ctx = ctxkeys.WithSubjectID(ctx, "s")
		ctx = ctxkeys.WithTenantID(ctx, "t")
		ctx = ctxkeys.WithSessionID(ctx, "x")

		p := ContextPrincipal(ctx)
		assert.Equal(t, idutil.SafeID("a"), p.ActorID)
		assert.Equal(t, idutil.SafeID("s"), p.SubjectID)
		assert.Equal(t, idutil.SafeID("t"), p.TenantID)
		assert.Equal(t, idutil.SafeID("x"), p.SessionID)
	})

	t.Run("empty ctx yields zero", func(t *testing.T) {
		assert.True(t, ContextPrincipal(context.Background()).IsZero())
	})

	// #1618 F1: pre-auth scoped emits (login session.created, setup user.created)
	// carry the tenant only in tenant.WithScope, not in ctxkeys. ContextPrincipal
	// must fall back to the scope so the audit appender writes the correct
	// per-tenant chain instead of the all-tenant-readable tenant_id='' system chain.
	t.Run("tenant falls back to WithScope when ctxkeys absent", func(t *testing.T) {
		ctx := tenant.WithScope(context.Background(), tenant.TenantID(scopeTenantUUID))
		p := ContextPrincipal(ctx)
		assert.Equal(t, idutil.SafeID(scopeTenantUUID), p.TenantID,
			"pre-auth scoped emit must derive principal tenant from the RLS scope")
	})

	// Precedence: an authenticated-principal ctxkeys.TenantID WINS over the scope
	// fallback (post-auth emits are unchanged — the scope branch never fires).
	t.Run("ctxkeys tenant wins over scope fallback", func(t *testing.T) {
		ctx := tenant.WithScope(context.Background(), tenant.TenantID(scopeTenantUUID))
		ctx = ctxkeys.WithTenantID(ctx, "ctxkeys-tenant")
		p := ContextPrincipal(ctx)
		assert.Equal(t, idutil.SafeID("ctxkeys-tenant"), p.TenantID,
			"post-auth ctxkeys tenant must take precedence over the scope fallback")
	})

	// #1824 F1: a present-but-EMPTY ctxkeys tenant is authoritative — it is the
	// system-identity installers' deliberate tenantless assertion (reconcile
	// installSystemProducerIdentity / projection InstallSystemPrincipal /
	// consume-path clearAmbientPrincipal) and MUST suppress the scope fallback so
	// those system emits stay tenantless (→ "_notenant"). Precedence keys on key
	// PRESENCE (ok), not on a non-empty value: conflating present-empty with absent
	// would let a leaked tenant.WithScope override the install.
	t.Run("present-empty ctxkeys tenant suppresses scope fallback", func(t *testing.T) {
		ctx := tenant.WithScope(context.Background(), tenant.TenantID(scopeTenantUUID))
		ctx = ctxkeys.WithTenantID(ctx, "")
		p := ContextPrincipal(ctx)
		assert.Equal(t, idutil.SafeID(""), p.TenantID,
			"installed tenantless system identity must win over the ambient scope")
	})
}
