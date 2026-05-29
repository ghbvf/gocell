package wrapper

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/pkg/ctxkeys"
)

// newEntryForAttrs builds a sealed outbox.Entry through the only producer
// constructor. Principal identity is injected from ctx (NewEntry reads the
// principal ctxkeys at its single injection trust boundary); occurredAt is set
// via WithOccurredAt. NewEntry.Validate requires a non-empty payload and a
// non-zero occurredAt, so the populated cases always carry both. Construction
// errors signal a test-helper bug.
func newEntryForAttrs(t *testing.T, ctx context.Context, occurredAt time.Time) outbox.Entry {
	t.Helper()
	e, err := outbox.NewEntry(clock.Real(), ctx, "event.attrs.test.v1", []byte(`{}`),
		outbox.WithOccurredAt(occurredAt))
	require.NoError(t, err)
	return e
}

// attrMap collapses the []Attr slice into key→value for order-independent
// assertions.
func attrMap(attrs []Attr) map[string]any {
	m := make(map[string]any, len(attrs))
	for _, a := range attrs {
		m[a.Key] = a.Value
	}
	return m
}

// TestEntryEnvelopeAttrs_ZeroEntryOmitsAll uses the zero-value Entry (the only
// way to obtain an Entry with empty Principal AND zero OccurredAt — NewEntry's
// Validate rejects a zero occurredAt). It exercises the all-omitted defensive
// branch: empty principal fields and a zero OccurredAt produce no attrs.
func TestEntryEnvelopeAttrs_ZeroEntryOmitsAll(t *testing.T) {
	assert.Empty(t, entryEnvelopeAttrs(outbox.Entry{}),
		"zero Entry (empty principal + zero OccurredAt) produces no envelope attrs")
}

func TestEntryEnvelopeAttrs_PrincipalAllFields(t *testing.T) {
	ctx := context.Background()
	ctx = ctxkeys.WithActorID(ctx, "actor-1")
	ctx = ctxkeys.WithSubjectID(ctx, "subj-1")
	ctx = ctxkeys.WithTenantID(ctx, "tenant-1")
	ctx = ctxkeys.WithSessionID(ctx, "sess-1")

	occurredAt := time.Date(2026, 5, 28, 12, 0, 0, 0, time.UTC)
	e := newEntryForAttrs(t, ctx, occurredAt)
	got := attrMap(entryEnvelopeAttrs(e))

	assert.Len(t, got, 5)
	assert.Equal(t, "actor-1", got["gocell.principal.actor_id"])
	assert.Equal(t, "subj-1", got["gocell.principal.subject_id"])
	assert.Equal(t, "tenant-1", got["gocell.principal.tenant_id"])
	// session_id is emitted raw here; key-aware masking happens at the otel
	// boundary (IsSensitiveKey on gocell.principal.session_id), not here.
	assert.Equal(t, "sess-1", got["gocell.principal.session_id"])
	assert.Equal(t, occurredAt.UnixNano(), got["gocell.event.occurred_at_unix_nano"])
}

func TestEntryEnvelopeAttrs_PartialPrincipalOmitsEmptyFields(t *testing.T) {
	// Only ActorID set; the other three principal fields stay empty and are
	// omitted. OccurredAt is always present (NewEntry stamps it).
	ctx := ctxkeys.WithActorID(context.Background(), "actor-only")
	occurredAt := time.Date(2026, 5, 28, 12, 0, 0, 0, time.UTC)
	e := newEntryForAttrs(t, ctx, occurredAt)
	got := attrMap(entryEnvelopeAttrs(e))

	assert.Len(t, got, 2)
	assert.Equal(t, "actor-only", got["gocell.principal.actor_id"])
	assert.Equal(t, occurredAt.UnixNano(), got["gocell.event.occurred_at_unix_nano"])
	_, hasSubject := got["gocell.principal.subject_id"]
	_, hasTenant := got["gocell.principal.tenant_id"]
	_, hasSession := got["gocell.principal.session_id"]
	assert.False(t, hasSubject, "empty subject_id must be omitted")
	assert.False(t, hasTenant, "empty tenant_id must be omitted")
	assert.False(t, hasSession, "empty session_id must be omitted")
}

func TestEntryEnvelopeAttrs_OccurredAtIsInt64Typed(t *testing.T) {
	// No principal ctxkeys ⇒ only the occurred_at attr is produced.
	want := time.Date(2026, 5, 28, 12, 0, 0, 0, time.UTC)
	e := newEntryForAttrs(t, context.Background(), want)
	got := entryEnvelopeAttrs(e)

	require.Len(t, got, 1)
	assert.Equal(t, "gocell.event.occurred_at_unix_nano", got[0].Key)
	assert.Equal(t, want.UnixNano(), got[0].Value)
	// int64-typed value (NOT a string) so the otel adapter takes the native
	// int64 path and bypasses the free-form string redactor. This is a
	// correctness requirement, not a stylistic one.
	assert.IsType(t, int64(0), got[0].Value, "occurred_at must be int64-typed, not a string")
}
