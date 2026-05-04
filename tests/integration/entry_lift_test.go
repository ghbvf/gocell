//go:build integration

package integration

import (
	"context"

	"github.com/ghbvf/gocell/kernel/outbox"
)

// entryToSubHandler wraps a business EntryHandler as a SubscriberHandler with
// nil Settlement. Integration tests call Subscriber.Subscribe directly and do
// not need ConsumerBase idempotency — nil Settlement is safe because the
// subscriber nil-checks before calling Commit/Release.
//
// This helper replaces the deleted outbox.EntryToSubscriberHandler in test-only
// code. Production callers use SubscriberWithMiddleware.SubscribeEntry instead.
func entryToSubHandler(h outbox.EntryHandler) outbox.SubscriberHandler {
	return func(ctx context.Context, entry outbox.Entry) (outbox.HandleResult, outbox.Settlement) {
		return h(ctx, entry), nil
	}
}
