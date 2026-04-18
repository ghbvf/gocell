package outbox

import (
	"time"

	kout "github.com/ghbvf/gocell/kernel/outbox"
)

// RelayConfig configures the outbox relay behaviour.
// Extracted from adapters/postgres/outbox_relay.go to live at the runtime layer
// so future relay implementations (non-PG) can share the same config surface.
type RelayConfig struct {
	// PollInterval is how often the relay polls for pending entries.
	PollInterval time.Duration
	// BatchSize is the maximum number of entries fetched per poll cycle.
	BatchSize int
	// RetentionPeriod is how long published entries are kept before cleanup.
	RetentionPeriod time.Duration
	// MaxAttempts is the maximum number of publish attempts before an entry
	// is marked as dead-lettered. Default 5.
	MaxAttempts int
	// BaseRetryDelay is the base delay for exponential backoff. Default 5s.
	// Actual delay = cappedDelay(BaseRetryDelay * 2^attempts) + jitter.
	BaseRetryDelay time.Duration
	// ClaimTTL is how long a claiming entry is held before ReclaimStale
	// recovers it back to pending. Default 60s.
	ClaimTTL time.Duration
	// MaxRetryDelay caps the exponential backoff delay to prevent
	// unbounded retry intervals at high attempt counts. Default 5m.
	MaxRetryDelay time.Duration
	// ReclaimInterval controls the independent ReclaimStale goroutine
	// frequency, decoupled from cleanup interval. Default 30s.
	ReclaimInterval time.Duration
	// DeadRetentionPeriod is how long dead-lettered entries are kept before
	// cleanup. Separate from RetentionPeriod to give operators more time
	// to investigate and manually retry failed entries. Default 30 days.
	DeadRetentionPeriod time.Duration
	// Metrics is the relay metrics collector for Prometheus integration.
	// If nil, a NoopRelayCollector is used (zero overhead).
	// ref: Temporal client.Options{MetricsHandler} — inject-at-construction pattern
	Metrics kout.RelayCollector
}

// DefaultRelayConfig returns a RelayConfig with sensible defaults.
// Field values are identical to adapters/postgres DefaultRelayConfig to ensure
// zero behaviour change during Phase C migration.
func DefaultRelayConfig() RelayConfig {
	return RelayConfig{
		PollInterval:        1 * time.Second,
		BatchSize:           100,
		RetentionPeriod:     72 * time.Hour,
		MaxAttempts:         5,
		BaseRetryDelay:      5 * time.Second,
		ClaimTTL:            60 * time.Second,
		MaxRetryDelay:       5 * time.Minute,
		ReclaimInterval:     30 * time.Second,
		DeadRetentionPeriod: 30 * 24 * time.Hour, // 30 days
	}
}

// WithDefaults fills zero/negative fields with values from DefaultRelayConfig.
// It does NOT set Metrics (handled by adapter constructors which wrap in
// safeRelayCollector). Returns the filled config.
func (c RelayConfig) WithDefaults() RelayConfig {
	d := DefaultRelayConfig()
	if c.PollInterval <= 0 {
		c.PollInterval = d.PollInterval
	}
	if c.BatchSize <= 0 {
		c.BatchSize = d.BatchSize
	}
	if c.RetentionPeriod <= 0 {
		c.RetentionPeriod = d.RetentionPeriod
	}
	if c.MaxAttempts <= 0 {
		c.MaxAttempts = d.MaxAttempts
	}
	if c.BaseRetryDelay <= 0 {
		c.BaseRetryDelay = d.BaseRetryDelay
	}
	if c.ClaimTTL <= 0 {
		c.ClaimTTL = d.ClaimTTL
	}
	if c.MaxRetryDelay <= 0 {
		c.MaxRetryDelay = d.MaxRetryDelay
	}
	if c.ReclaimInterval <= 0 {
		c.ReclaimInterval = d.ReclaimInterval
	}
	if c.DeadRetentionPeriod <= 0 {
		c.DeadRetentionPeriod = d.DeadRetentionPeriod
	}
	return c
}
