package outbox

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/framework/kernel/cellvocab"
	"github.com/ghbvf/gocell/framework/kernel/clock"
	"github.com/ghbvf/gocell/framework/pkg/ctxkeys"
)

// outbox.Entry implements cellvocab.ProjectionEvent (EPIC #1609 PR-01). Mirrors
// the production compile-time assertion in outbox.go.
var _ cellvocab.ProjectionEvent = Entry{}

// TestEntry_ProjectionEvent_EventIDMatchesID asserts EventID() is the polymorphic
// rename of ID() — the same underlying id field, the carrier's source-global-unique
// identity accessor.
func TestEntry_ProjectionEvent_EventIDMatchesID(t *testing.T) {
	e, err := NewEntry(clock.Real(), context.Background(), "test.v1", []byte(`{}`))
	require.NoError(t, err)
	assert.NotEmpty(t, e.EventID())
	assert.Equal(t, e.ID(), e.EventID(), "EventID must equal ID (same field, polymorphic rename)")
}

// TestEntry_ProjectionEvent_StreamMatchesRoutingTopic asserts Stream() is
// RoutingTopic() — both with an explicit topic and with the eventType fallback.
func TestEntry_ProjectionEvent_StreamMatchesRoutingTopic(t *testing.T) {
	withTopic, err := NewEntry(clock.Real(), context.Background(), "evt.type.v1", []byte(`{}`),
		WithTopic("routing.topic.v1"))
	require.NoError(t, err)
	assert.Equal(t, withTopic.RoutingTopic(), withTopic.Stream())
	assert.Equal(t, "routing.topic.v1", withTopic.Stream())

	noTopic, err := NewEntry(clock.Real(), context.Background(), "evt.type.v1", []byte(`{}`))
	require.NoError(t, err)
	assert.Equal(t, noTopic.RoutingTopic(), noTopic.Stream(),
		"Stream must fall back to eventType exactly like RoutingTopic when topic is empty")
	assert.Equal(t, "evt.type.v1", noTopic.Stream())
}

// TestEntry_ProjectionEvent_RestoreContext_RestoresObservabilityAndPrincipal
// asserts RestoreContext folds BOTH the observability and principal restore onto
// a clean context (the behavior the projection rebuild path and the live consumer
// path both rely on).
func TestEntry_ProjectionEvent_RestoreContext_RestoresObservabilityAndPrincipal(t *testing.T) {
	ctx := context.Background()
	ctx = ctxkeys.WithTraceID(ctx, "4bf92f3577b34da6a3ce929d0e0e4736")
	ctx = ctxkeys.WithRequestID(ctx, "req-rc")
	ctx = ctxkeys.WithActorID(ctx, "actor-rc") // test-only principal injection (_test.go is exempt from CTXKEYS-PRINCIPAL-WRITE-CALLER-01)
	ctx = ctxkeys.WithSubjectID(ctx, "subject-rc")

	e, err := NewEntry(clock.Real(), ctx, "test.v1", []byte(`{}`))
	require.NoError(t, err)

	restored := e.RestoreContext(context.Background())

	traceID, ok := ctxkeys.TraceIDFrom(restored)
	require.True(t, ok)
	assert.Equal(t, "4bf92f3577b34da6a3ce929d0e0e4736", traceID)
	reqID, ok := ctxkeys.RequestIDFrom(restored)
	require.True(t, ok)
	assert.Equal(t, "req-rc", reqID)
	actorID, ok := ctxkeys.ActorIDFrom(restored)
	require.True(t, ok, "RestoreContext must restore the principal family, not just observability")
	assert.Equal(t, "actor-rc", actorID)
	subjectID, ok := ctxkeys.SubjectIDFrom(restored)
	require.True(t, ok)
	assert.Equal(t, "subject-rc", subjectID)
}

// TestEntry_ProjectionEvent_RestoreContext_DoesNotOverwriteExisting asserts the
// restore is idempotent / no-overwrite: an existing non-empty ctx value wins.
// This is the security-relevant property the rebuild path depends on (a rebuild's
// ambient identity must not be clobbered) and matches both RestoreToContext halves.
func TestEntry_ProjectionEvent_RestoreContext_DoesNotOverwriteExisting(t *testing.T) {
	src := ctxkeys.WithTraceID(context.Background(), "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	e, err := NewEntry(clock.Real(), src, "test.v1", []byte(`{}`))
	require.NoError(t, err)

	dst := ctxkeys.WithTraceID(context.Background(), "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb")
	restored := e.RestoreContext(dst)

	traceID, ok := ctxkeys.TraceIDFrom(restored)
	require.True(t, ok)
	assert.Equal(t, "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", traceID,
		"existing ctx value must win — RestoreContext is no-overwrite")
}
