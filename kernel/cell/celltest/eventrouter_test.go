package celltest

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/kernel/wrapper"
)

func TestStubEventRouter_AddHandler(t *testing.T) {
	r := &StubEventRouter{}
	assert.Equal(t, 0, r.HandlerCount())

	handler := outbox.EntryHandler(func(_ context.Context, _ outbox.Entry) outbox.HandleResult {
		return outbox.HandleResult{Disposition: outbox.DispositionAck}
	})

	r.AddHandler("topic.a.v1", handler, "cell-a")
	assert.Equal(t, 1, r.HandlerCount())
	assert.Equal(t, "topic.a.v1", r.Topics[0])
	assert.Equal(t, "cell-a", r.ConsumerGroups[0])

	r.AddHandler("topic.b.v1", handler, "cell-b")
	assert.Equal(t, 2, r.HandlerCount())
	assert.Equal(t, "topic.b.v1", r.Topics[1])
	assert.Equal(t, "cell-b", r.ConsumerGroups[1])
}

// TestStubEventRouter_SliceInvariant_MixedRegistrations verifies the parallel
// slices (Topics / Handlers / ConsumerGroups / Contracts) stay index-aligned
// across interleaved AddHandler and AddContractHandler calls. The stub is used
// in cell-level tests where assertions often look up by index (e.g. `r.Topics[0]`
// should correspond to `r.Contracts[0]`), so the invariant must hold.
func TestStubEventRouter_SliceInvariant_MixedRegistrations(t *testing.T) {
	r := &StubEventRouter{}
	handler := outbox.EntryHandler(func(_ context.Context, _ outbox.Entry) outbox.HandleResult {
		return outbox.HandleResult{Disposition: outbox.DispositionAck}
	})

	contractSpec := wrapper.ContractSpec{
		ID: "event.config.changed.v1", Kind: "event",
		Transport: "amqp", Topic: "event.config.changed.v1",
	}

	// Mix both entry points in the order a typical Cell.RegisterSubscriptions
	// call would issue them during the PR-A11-M migration window.
	r.AddHandler("legacy.topic.v1", handler, "legacy-cg")
	r.AddContractHandler(contractSpec, handler, "contract-cg")
	r.AddHandler("legacy.other.v1", handler, "other-cg")

	require.Len(t, r.Topics, 3, "Topics count")
	require.Len(t, r.Handlers, 3, "Handlers count")
	require.Len(t, r.ConsumerGroups, 3, "ConsumerGroups count")
	require.Len(t, r.Contracts, 3, "Contracts count — must track every call, zero-value for AddHandler")

	// Index 0 — legacy AddHandler: Contracts entry is zero-value.
	assert.Equal(t, "legacy.topic.v1", r.Topics[0])
	assert.Equal(t, "legacy-cg", r.ConsumerGroups[0])
	assert.Equal(t, wrapper.ContractSpec{}, r.Contracts[0], "AddHandler must record zero-value ContractSpec")

	// Index 1 — AddContractHandler: Topics derived from spec.Topic, Contracts carries full spec.
	assert.Equal(t, "event.config.changed.v1", r.Topics[1])
	assert.Equal(t, "contract-cg", r.ConsumerGroups[1])
	assert.Equal(t, contractSpec, r.Contracts[1])

	// Index 2 — another legacy AddHandler.
	assert.Equal(t, "legacy.other.v1", r.Topics[2])
	assert.Equal(t, "other-cg", r.ConsumerGroups[2])
	assert.Equal(t, wrapper.ContractSpec{}, r.Contracts[2])
}
