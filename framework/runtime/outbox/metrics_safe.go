package outbox

import (
	"context"
	"log/slog"

	kout "github.com/ghbvf/gocell/framework/kernel/outbox"
	"github.com/ghbvf/gocell/framework/pkg/observability"
)

// safeRelayCollector wraps a kout.RelayCollector and recovers from any
// panic so that a misbehaving collector cannot crash relay worker goroutines.
//
// This follows the same pattern as pkg/observability.SafeObserve (pkg/observability/safe.go)
// which protects observability hooks from crashing the calling goroutine.
//
// Typed-nil implementations (e.g. a nil *prometheus.RelayCollector stored in
// an interface value) are handled implicitly: the nil-pointer dereference
// panic is caught by SafeObserve's deferred recover().
type safeRelayCollector struct {
	inner kout.RelayCollector
}

// Compile-time interface check.
var _ kout.RelayCollector = (*safeRelayCollector)(nil)

func (s *safeRelayCollector) RecordPollCycle(ctx context.Context, r kout.PollCycleResult) {
	s.safeCall(func() { s.inner.RecordPollCycle(ctx, r) })
}

func (s *safeRelayCollector) RecordBatchSize(ctx context.Context, size int) {
	s.safeCall(func() { s.inner.RecordBatchSize(ctx, size) })
}

func (s *safeRelayCollector) RecordReclaim(ctx context.Context, count int64) {
	s.safeCall(func() { s.inner.RecordReclaim(ctx, count) })
}

func (s *safeRelayCollector) RecordCleanup(ctx context.Context, publishedDeleted, deadDeleted int64) {
	s.safeCall(func() { s.inner.RecordCleanup(ctx, publishedDeleted, deadDeleted) })
}

// safeCall runs fn and recovers from any panic, logging it instead of
// letting it propagate to the relay goroutine. Delegates to
// pkg/observability.SafeObserve for single-source panic isolation logic.
func (s *safeRelayCollector) safeCall(fn func()) {
	observability.SafeObserve(slog.Default(), fn)
}
