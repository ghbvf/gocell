package postgres

import (
	"log/slog"
	"runtime/debug"

	"github.com/ghbvf/gocell/kernel/outbox"
)

// safeRelayCollector wraps an outbox.RelayCollector and recovers from any
// panic so that a misbehaving collector cannot crash relay worker goroutines.
//
// This follows the same pattern as runtime/http/middleware/safe_observe.go
// which protects HTTP metrics collection from crashing request handlers.
//
// It also handles typed-nil implementations (e.g. a nil *prometheus.RelayCollector
// stored in an interface value) by checking the inner value before delegation.
type safeRelayCollector struct {
	inner outbox.RelayCollector
}

// Compile-time interface check.
var _ outbox.RelayCollector = (*safeRelayCollector)(nil)

func (s *safeRelayCollector) RecordPollCycle(r outbox.PollCycleResult) {
	s.safeCall(func() { s.inner.RecordPollCycle(r) })
}

func (s *safeRelayCollector) RecordBatchSize(size int) {
	s.safeCall(func() { s.inner.RecordBatchSize(size) })
}

func (s *safeRelayCollector) RecordReclaim(count int64) {
	s.safeCall(func() { s.inner.RecordReclaim(count) })
}

func (s *safeRelayCollector) RecordCleanup(publishedDeleted, deadDeleted int64) {
	s.safeCall(func() { s.inner.RecordCleanup(publishedDeleted, deadDeleted) })
}

// safeCall runs fn and recovers from any panic, logging it instead of
// letting it propagate to the relay goroutine.
func (s *safeRelayCollector) safeCall(fn func()) {
	defer func() {
		if v := recover(); v != nil {
			slog.Error("outbox relay: metrics collector panic (dropped observation)",
				slog.Any("panic", v),
				slog.String("stack", string(debug.Stack())),
			)
		}
	}()
	fn()
}
