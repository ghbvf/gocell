package config

import "time"

// WatcherCollector records watcher operational metrics. Implementations must
// be safe for concurrent use.
//
// ref: prometheus/prometheus cmd/prometheus/main.go — 4-metric baseline:
// reload total, success gauge, last timestamp, event count.
type WatcherCollector interface {
	// RecordEvent records a watcher event by type ("write", "create", "symlink_pivot").
	RecordEvent(eventType string)

	// RecordLastEventTimestamp records the time of the most recent event.
	RecordLastEventTimestamp(t time.Time)

	// RecordDebounceCoalesced increments the count of events absorbed by debounce.
	RecordDebounceCoalesced()
}

// NoopWatcherCollector is a no-op implementation used when metrics are disabled.
// All methods are safe for concurrent use.
type NoopWatcherCollector struct{}

// Intentionally empty: NoopWatcherCollector discards all metrics when no
// collector is configured. Real implementations live in adapters/ (e.g.
// Prometheus, OTel) and are injected via WithMetrics.

func (NoopWatcherCollector) RecordEvent(string)                 {}
func (NoopWatcherCollector) RecordLastEventTimestamp(time.Time) {}
func (NoopWatcherCollector) RecordDebounceCoalesced()           {}
