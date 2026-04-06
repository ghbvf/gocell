package redis

import (
	"context"
	"testing"
	"time"

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
