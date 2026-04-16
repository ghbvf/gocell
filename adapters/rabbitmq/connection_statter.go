package rabbitmq

import "github.com/ghbvf/gocell/runtime/observability/poolstats"

// Statter returns a poolstats.Statter bound to this Connection with the
// supplied human-readable name (e.g. "rabbitmq-outbox"). The OTel pool
// collector consumes Statter snapshots to emit uniform
// db.client.connection.* metrics across every GoCell adapter.
//
// Scope note: the rabbitmq adapter manages a **channel pool** inside one
// AMQP connection — TotalConns counts channels, not connections. The
// publisher bypasses the pool and uses ephemeral channels (Publisher
// opens/confirms/closes per publish), so this statter only reflects
// subscriber channel pool utilisation. The AMQP library does not track a
// "wait queue" for channel acquisition (Connection.Channel() blocks on
// the broker, not on the pool), so WaitCount is always 0.
//
// ref: rabbitmq/amqp091-go — no built-in wait counter; channel pool is
// maintained by our adapter at adapters/rabbitmq/connection.go.
func (c *Connection) Statter(name string) poolstats.Statter {
	return &rabbitPoolStatter{conn: c, name: name}
}

type rabbitPoolStatter struct {
	conn *Connection
	name string
}

func (s *rabbitPoolStatter) PoolName() string { return s.name }

func (s *rabbitPoolStatter) Snapshot() poolstats.Snapshot {
	if s.conn == nil {
		return poolstats.Snapshot{}
	}
	stats := s.conn.PoolStats()
	return poolstats.Snapshot{
		TotalConns: int64(stats.ChannelPoolSize),
		IdleConns:  int64(stats.IdleChannels),
		UsedConns:  int64(stats.ChannelPoolSize - stats.IdleChannels),
		MaxConns:   int64(stats.ChannelPoolSize),
		WaitCount:  0, // amqp091 has no wait queue; see package doc above.
	}
}
