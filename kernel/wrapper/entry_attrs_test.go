package wrapper

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/ghbvf/gocell/kernel/outbox"
)

func TestEntryEnvelopeAttrs_Empty(t *testing.T) {
	got := entryEnvelopeAttrs(outbox.Entry{})
	assert.Empty(t, got, "zero Entry produces no envelope attrs")
}

func TestEntryEnvelopeAttrs_PrincipalAllFields(t *testing.T) {
	e := outbox.Entry{
		Principal: outbox.PrincipalMetadata{
			ActorID:   "actor-1",
			SubjectID: "subj-1",
			TenantID:  "tenant-1",
			SessionID: "sess-1",
		},
	}
	got := entryEnvelopeAttrs(e)
	require := assert.New(t)
	require.Len(got, 4)
	require.Equal("gocell.principal.actor_id", got[0].Key)
	require.Equal("actor-1", got[0].Value)
	require.Equal("gocell.principal.subject_id", got[1].Key)
	require.Equal("subj-1", got[1].Value)
	require.Equal("gocell.principal.tenant_id", got[2].Key)
	require.Equal("tenant-1", got[2].Value)
	require.Equal("gocell.principal.session_id", got[3].Key)
	require.Equal("sess-1", got[3].Value)
}

func TestEntryEnvelopeAttrs_PartialPrincipal(t *testing.T) {
	e := outbox.Entry{Principal: outbox.PrincipalMetadata{ActorID: "actor-only"}}
	got := entryEnvelopeAttrs(e)
	assert.Len(t, got, 1)
	assert.Equal(t, "gocell.principal.actor_id", got[0].Key)
}

func TestEntryEnvelopeAttrs_OccurredAt(t *testing.T) {
	want := time.Date(2026, 5, 28, 12, 0, 0, 0, time.UTC)
	got := entryEnvelopeAttrs(outbox.Entry{OccurredAt: want})
	assert.Len(t, got, 1)
	assert.Equal(t, "gocell.event.occurred_at_unix_nano", got[0].Key)
	assert.Equal(t, want.UnixNano(), got[0].Value)
}

func TestEntryEnvelopeAttrs_OccurredAtZeroOmitted(t *testing.T) {
	got := entryEnvelopeAttrs(outbox.Entry{OccurredAt: time.Time{}})
	assert.Empty(t, got, "zero-value OccurredAt produces no attr (avoids epoch noise)")
}

func TestEntryEnvelopeAttrs_PrincipalAndOccurredAt(t *testing.T) {
	e := outbox.Entry{
		Principal:  outbox.PrincipalMetadata{ActorID: "a", SubjectID: "s"},
		OccurredAt: time.Date(2026, 5, 28, 12, 0, 0, 0, time.UTC),
	}
	got := entryEnvelopeAttrs(e)
	assert.Len(t, got, 3)
}
