package redis

import "github.com/ghbvf/gocell/runtime/observability/poolstats"

// Statter returns a poolstats.Statter bound to this Client with the
// supplied human-readable name (e.g. "redis-session-cache"). Used by the
// OTel pool collector to emit db.client.connection.* metrics without
// adapter-specific switching.
//
// Cluster mode aggregation: *ClusterClient.PoolStats() returns counters
// summed across every cluster node, while Config.PoolSize is the *per-node*
// connection cap. To keep db.client.connection.{count,max} on the same
// scale (otherwise UsedConns can exceed MaxConns at any non-trivial pool
// utilization, which dashboards interpret as saturation), MaxConns is
// scaled by the configured seed count: MaxConns = PoolSize × len(ClusterAddrs).
// Seed count is a lower bound — a real cluster usually has more nodes
// (replicas plus rebalanced shards), so the reported max underestimates
// the true cap; for an exact figure consult Redis CLUSTER NODES out-of-band.
// Aggregation is the right comparison surface for OTel because UsedConns
// is itself aggregated; mismatched scopes were the bug, not the magnitude.
//
// ref: redis/go-redis internal/pool/pool.go — PoolStats exposes
// TotalConns/IdleConns/StaleConns/Timeouts/etc. UsedConns is derived as
// TotalConns - IdleConns - StaleConns (stale connections are scheduled
// for removal but still counted in TotalConns until the background
// reaper prunes them).
func (c *Client) Statter(name string) poolstats.Statter {
	return &redisPoolStatter{client: c, name: name}
}

type redisPoolStatter struct {
	client *Client
	name   string
}

func (s *redisPoolStatter) PoolName() string { return s.name }

func (s *redisPoolStatter) Snapshot() poolstats.Snapshot {
	if s.client == nil || s.client.statsProvider == nil {
		return poolstats.Snapshot{}
	}
	stats := s.client.statsProvider.PoolStats()
	if stats == nil {
		return poolstats.Snapshot{}
	}
	// Defensive clamp: StaleConns are counted inside TotalConns but pruned
	// asynchronously; under heavy churn the arithmetic can momentarily dip
	// below zero. Report zero rather than emit an invalid UpDownCounter.
	used := max(int64(stats.TotalConns)-int64(stats.IdleConns)-int64(stats.StaleConns), 0)
	return poolstats.Snapshot{
		TotalConns: int64(stats.TotalConns),
		IdleConns:  int64(stats.IdleConns),
		UsedConns:  used,
		MaxConns:   maxConnsForMode(s.client.config),
		WaitCount:  int64(stats.Timeouts),
	}
}

// maxConnsForMode returns the connection cap on the same scope as
// PoolStats reports usage. Standalone and Sentinel both have a single pool
// per process, so the cap is just Config.PoolSize. ClusterClient pools per
// node and aggregates PoolStats across nodes, so the comparable cap is
// PoolSize × seed count (lower bound; topology refresh may discover more).
func maxConnsForMode(cfg Config) int64 {
	if cfg.Mode == ModeCluster {
		seeds := int64(len(cfg.ClusterAddrs))
		if seeds < 1 {
			seeds = 1
		}
		return int64(cfg.PoolSize) * seeds
	}
	return int64(cfg.PoolSize)
}
