package rabbitmq

import "github.com/ghbvf/gocell/runtime/observability/poolstats"

// ChannelStatter returns a poolstats.Statter bound to this Connection's
// subscriber channel pool, with the supplied human-readable name
// (e.g. "rabbitmq-outbox"). The Snapshot shape is shared with database
// pools but the underlying resource is **AMQP channels inside one TCP
// connection**, not TCP connections.
//
// IMPORTANT — consume this statter via
// adapters/otel.RegisterMessagingChannelMetrics (messaging semantics),
// NOT RegisterPoolMetrics (db.client.connection.* semantics). Feeding a
// channel-pool snapshot into the DB collector emits misleading
// db.client.connection.count{state=…} time series and corrupts dashboard
// capacity signalling — the kind of silent semantic drift that only
// surfaces during an incident.
//
// Scope — publisher channels are ephemeral (open/confirm/publish/close
// per publish) and bypass the pool entirely, so the snapshot reflects
// only the subscriber channel pool. amqp091-go does not track a wait
// queue for channel acquisition (Connection.Channel() blocks on the
// broker, not on the pool), so WaitCount is always 0.
//
// ref: rabbitmq/amqp091-go channel pool (adapter-local at
// adapters/rabbitmq/connection.go). OpenTelemetry semconv database-metrics
// / messaging-metrics specs separate DB connection pools from messaging
// channels explicitly — no upstream convention justifies collapsing the
// two into one metric family.
func (c *Connection) ChannelStatter(name string) poolstats.Statter {
	return &rabbitChannelStatter{conn: c, name: name}
}

// Deprecated: use ChannelStatter. The old name implied uniform database
// pool semantics. A caller that routed the returned statter through
// RegisterPoolMetrics was emitting semantically wrong
// db.client.connection.* time series.
func (c *Connection) Statter(name string) poolstats.Statter {
	return c.ChannelStatter(name)
}

type rabbitChannelStatter struct {
	conn *Connection
	name string
}

func (s *rabbitChannelStatter) PoolName() string { return s.name }

func (s *rabbitChannelStatter) Snapshot() poolstats.Snapshot {
	if s.conn == nil {
		return poolstats.Snapshot{}
	}
	stats := s.conn.PoolStats()
	return poolstats.Snapshot{
		TotalConns: int64(stats.ChannelPoolSize),
		IdleConns:  int64(stats.IdleChannels),
		UsedConns:  int64(stats.ChannelPoolSize - stats.IdleChannels),
		MaxConns:   int64(stats.ChannelPoolSize),
		WaitCount:  0, // amqp091 has no wait queue; see godoc above.
	}
}
