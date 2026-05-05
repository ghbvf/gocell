// Package poolstats defines a provider-neutral connection-pool snapshot
// interface. Each concrete pool adapter (postgres, redis, rabbitmq)
// exposes a Statter so a single OTel collector can emit uniform
// gocell.pool.* metrics without type-switching per adapter.
//
// ref: opentelemetry.io/docs/specs/semconv/database/database-metrics —
// db.client.connection.count (state=idle|used), db.client.connection.max,
// db.client.connection.pending_requests form the semantic backbone we
// target. We expose slightly adapter-specific counters (e.g. wait/timeout)
// through the WaitCount field so consumers can map to the nearest
// semconv attribute.
//
// ref: jackc/pgx pgxpool/stat.go, redis/go-redis internal/pool/pool.go —
// the superset of metrics available on GoCell's three pool libraries.
// The Snapshot fields here are the narrowest set that every adapter can
// fill truthfully.
package poolstats

// Snapshot is a point-in-time snapshot of connection-pool counters. All
// fields are int64 so the OTel collector can emit them without casts.
type Snapshot struct {
	// TotalConns = IdleConns + UsedConns + (adapter-specific transitional
	// states, e.g. pg's ConstructingConns). Matches the OTel
	// db.client.connection.count semantic convention sum.
	TotalConns int64

	// IdleConns — connections ready for immediate checkout. Maps to
	// db.client.connection.count{state="idle"}.
	IdleConns int64

	// UsedConns — connections currently checked out by callers. Maps to
	// db.client.connection.count{state="used"}. Callers can derive this
	// from TotalConns - IdleConns when an adapter does not track it
	// directly.
	UsedConns int64

	// MaxConns — configured pool capacity. Maps to db.client.connection.max.
	MaxConns int64

	// WaitCount — running count of callers that waited (or timed out)
	// because the pool was at capacity. pgxpool: EmptyAcquireCount;
	// go-redis: Timeouts; rabbitmq: 0 (channel pool has no wait queue).
	WaitCount int64
}

// Statter exposes a Snapshot plus a stable identifier used as the OTel
// db.client.connection.pool.name attribute. Implementations must be safe
// for concurrent use; Snapshot() is typically called from a callback in
// the OTel collector goroutine.
type Statter interface {
	// PoolName returns a stable, human-readable identifier for dashboard
	// pivoting. Typical values: "postgres-main", "redis-session-cache",
	// "rabbitmq-outbox".
	PoolName() string

	// Snapshot returns a current counter sample. Cheap and non-blocking by
	// contract — adapters read from in-memory pool state, not from the
	// remote server.
	Snapshot() Snapshot
}
