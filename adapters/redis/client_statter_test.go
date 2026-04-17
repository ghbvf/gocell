package redis

import (
	"testing"

	goredis "github.com/redis/go-redis/v9"

	"github.com/ghbvf/gocell/runtime/observability/poolstats"
)

type fakePoolStatsProvider struct {
	stats *goredis.PoolStats
}

func (f *fakePoolStatsProvider) PoolStats() *goredis.PoolStats { return f.stats }

func TestClient_Statter_NoStatsProvider_ReturnsZeroSnapshot(t *testing.T) {
	// Constructor failure path (statsProvider nil) — Snapshot must not
	// panic and must not emit bogus data.
	c := &Client{}
	snap := c.Statter("redis-nil").Snapshot()
	if (snap != poolstats.Snapshot{}) {
		t.Fatalf("expected zero-value snapshot, got %+v", snap)
	}
}

func TestClient_Statter_MapsGoRedisStats(t *testing.T) {
	c := &Client{
		config: Config{PoolSize: 20},
		statsProvider: &fakePoolStatsProvider{stats: &goredis.PoolStats{
			TotalConns: 12,
			IdleConns:  4,
			StaleConns: 1,
			Timeouts:   7,
		}},
	}
	s := c.Statter("redis-main")
	if s.PoolName() != "redis-main" {
		t.Fatalf("PoolName = %q, want redis-main", s.PoolName())
	}
	snap := s.Snapshot()
	if snap.TotalConns != 12 || snap.IdleConns != 4 {
		t.Fatalf("Total/IdleConns mismatch: %+v", snap)
	}
	if snap.UsedConns != 7 {
		t.Fatalf("UsedConns = %d, want 7 (total-idle-stale)", snap.UsedConns)
	}
	if snap.MaxConns != 20 {
		t.Fatalf("MaxConns = %d, want 20 (from Config.PoolSize)", snap.MaxConns)
	}
	if snap.WaitCount != 7 {
		t.Fatalf("WaitCount = %d, want 7 (from Timeouts)", snap.WaitCount)
	}
}

func TestClient_Statter_NegativeUsedIsClamped(t *testing.T) {
	// StaleConns > TotalConns - IdleConns can occur transiently; ensure
	// UsedConns never goes negative.
	c := &Client{
		config: Config{PoolSize: 5},
		statsProvider: &fakePoolStatsProvider{stats: &goredis.PoolStats{
			TotalConns: 3,
			IdleConns:  2,
			StaleConns: 5, // would compute used = 3-2-5 = -4 without clamp
		}},
	}
	if got := c.Statter("x").Snapshot().UsedConns; got != 0 {
		t.Fatalf("UsedConns = %d, want 0 (clamped)", got)
	}
}

var _ poolstats.Statter = (*redisPoolStatter)(nil)
