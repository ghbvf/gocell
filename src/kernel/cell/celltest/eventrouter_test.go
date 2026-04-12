package celltest

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/ghbvf/gocell/kernel/outbox"
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
