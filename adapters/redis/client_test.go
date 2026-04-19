package redis

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	goredis "github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestConfigDefaults(t *testing.T) {
	cfg := Config{}
	cfg.defaults()

	assert.Equal(t, ModeStandalone, cfg.Mode)
	assert.Equal(t, "", cfg.Addr) // No unsafe localhost fallback.
	assert.Equal(t, 5*time.Second, cfg.DialTimeout)
	assert.Equal(t, 3*time.Second, cfg.ReadTimeout)
	assert.Equal(t, 3*time.Second, cfg.WriteTimeout)
	assert.Equal(t, 30*time.Second, cfg.DistLockTTL)
}

func TestConfigDefaultsPreserveExisting(t *testing.T) {
	cfg := Config{
		Addr:        "redis:6380",
		Mode:        ModeSentinel,
		DialTimeout: 10 * time.Second,
		ReadTimeout: 7 * time.Second,
		DistLockTTL: 60 * time.Second,
	}
	cfg.defaults()

	assert.Equal(t, ModeSentinel, cfg.Mode)
	assert.Equal(t, "redis:6380", cfg.Addr)
	assert.Equal(t, 10*time.Second, cfg.DialTimeout)
	assert.Equal(t, 7*time.Second, cfg.ReadTimeout)
	assert.Equal(t, 7*time.Second, cfg.WriteTimeout) // Defaults to ReadTimeout.
	assert.Equal(t, 60*time.Second, cfg.DistLockTTL)
}

func TestClientHealth_Success(t *testing.T) {
	mock := newMockCmdable()
	client := newClientFromCmdable(mock, Config{})

	err := client.Health(context.Background())
	assert.NoError(t, err)
}

func TestClientHealth_Failure(t *testing.T) {
	mock := newMockCmdable()
	mock.pingErr = errMock
	client := newClientFromCmdable(mock, Config{})

	err := client.Health(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ERR_ADAPTER_REDIS_CONNECT")
	assert.Contains(t, err.Error(), "health check failed")
}

func TestClientClose_Success(t *testing.T) {
	mock := newMockCmdable()
	client := newClientFromCmdable(mock, Config{})

	err := client.Close()
	assert.NoError(t, err)
	assert.True(t, mock.closed)
}

func TestClientClose_Failure(t *testing.T) {
	mock := newMockCmdable()
	mock.closeErr = errMock
	client := newClientFromCmdable(mock, Config{})

	err := client.Close()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ERR_ADAPTER_REDIS_CONNECT")
}

func TestClientConfigReturned(t *testing.T) {
	mock := newMockCmdable()
	cfg := Config{
		Addr: "custom:6379",
		DB:   3,
	}
	client := newClientFromCmdable(mock, cfg)

	got := client.Config()
	assert.Equal(t, "custom:6379", got.Addr)
	assert.Equal(t, 3, got.DB)
}

func TestClientConfigPreservesPassword(t *testing.T) {
	mock := newMockCmdable()
	cfg := Config{
		Addr:     "redis:6379",
		Password: "s3cret",
	}
	client := newClientFromCmdable(mock, cfg)

	got := client.Config()
	assert.Equal(t, "s3cret", got.Password, "Config() must preserve password for round-trip")
}

func TestConfigLogValueRedactsPassword(t *testing.T) {
	cfg := Config{
		Addr:     "redis:6379",
		Password: "s3cret",
		Mode:     ModeStandalone,
		DB:       2,
	}
	lv := cfg.LogValue()
	// LogValue should contain addr and db but NOT password.
	resolved := lv.Resolve().String()
	assert.Contains(t, resolved, "redis:6379")
	assert.Contains(t, resolved, "2")
	assert.NotContains(t, resolved, "s3cret", "LogValue must not contain password")
}

func TestClientPoolStats_NilProvider(t *testing.T) {
	mock := newMockCmdable()
	client := newClientFromCmdable(mock, Config{})

	stats := client.PoolStats()
	assert.Equal(t, PoolStats{}, stats, "mock client must return zero PoolStats")
}

func TestClientPoolStats_WithProvider(t *testing.T) {
	mock := newMockCmdable()
	client := newClientFromCmdable(mock, Config{})
	client.statsProvider = &mockPoolStatsProvider{
		stats: &goredis.PoolStats{
			Hits:       100,
			Misses:     5,
			Timeouts:   1,
			TotalConns: 10,
			IdleConns:  7,
			StaleConns: 2,
		},
	}

	stats := client.PoolStats()
	assert.Equal(t, uint32(100), stats.Hits)
	assert.Equal(t, uint32(5), stats.Misses)
	assert.Equal(t, uint32(1), stats.Timeouts)
	assert.Equal(t, uint32(10), stats.TotalConns)
	assert.Equal(t, uint32(7), stats.IdleConns)
	assert.Equal(t, uint32(2), stats.StaleConns)
}

func TestPoolStats_JSON_CamelCase(t *testing.T) {
	stats := PoolStats{Hits: 10, Misses: 2, TotalConns: 5, IdleConns: 3}
	b, err := json.Marshal(stats)
	require.NoError(t, err)
	s := string(b)
	assert.Contains(t, s, `"hits"`)
	assert.Contains(t, s, `"misses"`)
	assert.Contains(t, s, `"totalConns"`)
	assert.Contains(t, s, `"idleConns"`)
}

func TestNewClient_StandaloneEmptyAddr(t *testing.T) {
	_, err := NewClient(context.Background(), Config{Mode: ModeStandalone})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Config.Addr is required")
}

func TestNewClient_SentinelEmptyAddrs(t *testing.T) {
	_, err := NewClient(context.Background(), Config{
		Mode:           ModeSentinel,
		SentinelMaster: "mymaster",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "SentinelAddrs is required")
}

func TestNewClient_SentinelEmptyMaster(t *testing.T) {
	_, err := NewClient(context.Background(), Config{
		Mode:          ModeSentinel,
		SentinelAddrs: []string{"sentinel:26379"},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "SentinelMaster is required")
}

// ---------------------------------------------------------------------------
// T18: Client.CloseCtx(ctx) tests
// ---------------------------------------------------------------------------

func TestClientCloseCtx_AcceptsCtx(t *testing.T) {
	mock := newMockCmdable()
	client := newClientFromCmdable(mock, Config{})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err := client.CloseCtx(ctx)
	assert.NoError(t, err)
	assert.True(t, mock.closed)
}

func TestClientCloseCtx_PreCancelledCtxReturnsError(t *testing.T) {
	mock := newMockCmdable()
	client := newClientFromCmdable(mock, Config{})

	cancelledCtx, cancel := context.WithCancel(context.Background())
	cancel() // already cancelled

	err := client.CloseCtx(cancelledCtx)
	require.Error(t, err, "CloseCtx with pre-cancelled ctx must return error")
}

func TestClientCloseCtx_Failure(t *testing.T) {
	mock := newMockCmdable()
	mock.closeErr = errMock
	client := newClientFromCmdable(mock, Config{})

	err := client.CloseCtx(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ERR_ADAPTER_REDIS_CONNECT")
}
