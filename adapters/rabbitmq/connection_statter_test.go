package rabbitmq

import (
	"testing"

	"github.com/ghbvf/gocell/runtime/observability/poolstats"
)

func TestConnection_ChannelStatter_NilConn_ReturnsZeroSnapshot(t *testing.T) {
	var c *Connection
	// White-box safety net: construct rabbitChannelStatter directly with a
	// nil *Connection so we can verify Snapshot() does not panic when its
	// inner connection is nil. The public ChannelStatter() entry point is
	// not exercised here because invoking it on a nil receiver would panic
	// before reaching the statter; production callers always hold a live
	// *Connection. This guard exists to catch regressions in the nil branch
	// of (*rabbitChannelStatter).Snapshot.
	s := (&rabbitChannelStatter{conn: c, name: "rmq-nil"})
	if s.PoolName() != "rmq-nil" {
		t.Fatalf("PoolName = %q, want rmq-nil", s.PoolName())
	}
	if (s.Snapshot() != poolstats.Snapshot{}) {
		t.Fatalf("nil conn must yield zero-value snapshot")
	}
}

func TestConnection_ChannelStatter_MapsChannelPoolStats(t *testing.T) {
	// Construct a Connection with a pre-sized channel pool to exercise
	// Snapshot projection. We bypass the dial/handshake machinery —
	// PoolStats reads cap()/len() of the channelPool buffered channel.
	c := &Connection{
		channelPool: make(chan AMQPChannel, 8), // capacity 8, 0 idle
	}
	// Put 3 idle channel placeholders in the pool. nil AMQPChannel is
	// legal in the typed channel since the interface type allows it; only
	// cap/len are read by Snapshot.
	for range 3 {
		c.channelPool <- nil
	}
	s := c.ChannelStatter("rmq-outbox")
	snap := s.Snapshot()
	if snap.TotalConns != 8 {
		t.Fatalf("TotalConns = %d, want 8", snap.TotalConns)
	}
	if snap.IdleConns != 3 {
		t.Fatalf("IdleConns = %d, want 3", snap.IdleConns)
	}
	if snap.UsedConns != 5 {
		t.Fatalf("UsedConns = %d, want 5 (cap-len)", snap.UsedConns)
	}
	if snap.WaitCount != 0 {
		t.Fatalf("WaitCount = %d, want 0 (amqp091 has no wait queue)", snap.WaitCount)
	}
}

var _ poolstats.Statter = (*rabbitChannelStatter)(nil)
