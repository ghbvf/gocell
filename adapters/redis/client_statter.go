package redis

import "github.com/ghbvf/gocell/runtime/observability/poolstats"

// Statter returns a poolstats.Statter bound to this Client with the
// supplied human-readable name (e.g. "redis-session-cache"). Used by the
// OTel pool collector to emit db.client.connection.* metrics without
// adapter-specific switching.
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
		MaxConns:   int64(s.client.config.PoolSize),
		WaitCount:  int64(stats.Timeouts),
	}
}
