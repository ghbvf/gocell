package celltest

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/kernel/outbox"
)

func TestStubEventRouter_AddContractHandler(t *testing.T) {
	r := &StubEventRouter{}
	assert.Equal(t, 0, r.HandlerCount())

	handler := outbox.EntryHandler(func(_ context.Context, _ outbox.Entry) outbox.HandleResult {
		return outbox.HandleResult{Disposition: outbox.DispositionAck}
	})

	r.AddContractHandler(testEventSpec("topic.a.v1"), handler, "cell-a")
	assert.Equal(t, 1, r.HandlerCount())
	assert.Equal(t, "topic.a.v1", r.Topics[0])
	assert.Equal(t, "cell-a", r.ConsumerGroups[0])

	r.AddContractHandler(testEventSpec("topic.b.v1"), handler, "cell-b")
	assert.Equal(t, 2, r.HandlerCount())
	assert.Equal(t, "topic.b.v1", r.Topics[1])
	assert.Equal(t, "cell-b", r.ConsumerGroups[1])
}

// TestStubEventRouter_SliceInvariant verifies the parallel slices (Topics /
// Handlers / ConsumerGroups / Contracts) stay index-aligned across
// AddContractHandler calls. The stub is used
// in cell-level tests where assertions often look up by index (e.g. `r.Topics[0]`
// should correspond to `r.Contracts[0]`), so the invariant must hold.
func TestStubEventRouter_SliceInvariant(t *testing.T) {
	r := &StubEventRouter{}
	handler := outbox.EntryHandler(func(_ context.Context, _ outbox.Entry) outbox.HandleResult {
		return outbox.HandleResult{Disposition: outbox.DispositionAck}
	})

	specA := testEventSpec("event.a.v1")
	specB := testEventSpec("event.b.v1")
	r.AddContractHandler(specA, handler, "cell-a")
	r.AddContractHandler(specB, handler, "cell-b")

	require.Len(t, r.Topics, 2, "Topics count")
	require.Len(t, r.Handlers, 2, "Handlers count")
	require.Len(t, r.ConsumerGroups, 2, "ConsumerGroups count")
	require.Len(t, r.Contracts, 2, "Contracts count")

	assert.Equal(t, "event.a.v1", r.Topics[0])
	assert.Equal(t, "cell-a", r.ConsumerGroups[0])
	assert.Equal(t, specA, r.Contracts[0])

	assert.Equal(t, "event.b.v1", r.Topics[1])
	assert.Equal(t, "cell-b", r.ConsumerGroups[1])
	assert.Equal(t, specB, r.Contracts[1])
}
