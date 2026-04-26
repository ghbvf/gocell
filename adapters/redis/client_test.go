package redis

import (
	"context"
	"encoding/json"
	"strings"
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

	err := client.Close(context.Background())
	assert.NoError(t, err)
	assert.True(t, mock.closed)
}

func TestClientClose_Failure(t *testing.T) {
	mock := newMockCmdable()
	mock.closeErr = errMock
	client := newClientFromCmdable(mock, Config{})

	err := client.Close(context.Background())
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
// T18: Client.Close(ctx) tests
// ---------------------------------------------------------------------------

func TestClientClose_AcceptsCtx(t *testing.T) {
	mock := newMockCmdable()
	client := newClientFromCmdable(mock, Config{})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err := client.Close(ctx)
	assert.NoError(t, err)
	assert.True(t, mock.closed)
}

func TestClientClose_PreCancelledCtxReturnsError(t *testing.T) {
	mock := newMockCmdable()
	client := newClientFromCmdable(mock, Config{})

	cancelledCtx, cancel := context.WithCancel(context.Background())
	cancel() // already cancelled

	err := client.Close(cancelledCtx)
	require.Error(t, err, "Close with pre-cancelled ctx must return error")
}

func TestClientClose_ContextFailure(t *testing.T) {
	mock := newMockCmdable()
	mock.closeErr = errMock
	client := newClientFromCmdable(mock, Config{})

	err := client.Close(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ERR_ADAPTER_REDIS_CONNECT")
}

// TestClient_ImplementsContextCloser verifies the compile-time assertion at
// package level. This is a belt-and-suspenders test confirming the interface.
func TestClient_ImplementsContextCloser(t *testing.T) {
	var _ interface {
		Close(ctx context.Context) error
	} = (*Client)(nil)
}

// tlsErrSignal is the expected error code emitted by ValidateTLSEndpoint.
// TODO(phase2): replace with errcode.ErrAdapterEndpointNotTLS constant.
const tlsErrSignal = "ERR_ADAPTER_ENDPOINT_NOT_TLS"

// TestConfigValidate_RejectNonTLSRemote verifies that NewClient returns a TLS
// validation error (not a generic connection error) for remote non-TLS endpoints.
//
// TDD phase-1 red-light: the stub ValidateTLSEndpoint returns nil for all inputs,
// so NewClient proceeds past TLS validation and returns a network/connection error.
// The test checks that the error contains a TLS validation signal — which the
// stub-induced connection error does NOT, so these sub-tests FAIL.
//
// Phase-2 expectation: ValidateTLSEndpoint rejects before any dial, producing a
// fast error with the TLS validation code in the error message.
//
// Loopback addresses (127.0.0.1) are exempt — they may fail with a connection
// refused error but must NOT produce a TLS validation error.
func TestConfigValidate_RejectNonTLSRemote(t *testing.T) {
	tests := []struct {
		name       string
		cfg        Config
		wantTLSErr bool // expect error with TLS validation signal
	}{
		{
			name: "standalone remote non-TLS addr — reject",
			cfg: Config{
				Mode:        ModeStandalone,
				Addr:        "prod.redis.example.internal:6379",
				DialTimeout: 100 * time.Millisecond, // fast dial timeout for test
			},
			wantTLSErr: true,
		},
		{
			name: "sentinel remote non-TLS addr — reject",
			cfg: Config{
				Mode:           ModeSentinel,
				SentinelAddrs:  []string{"prod1.sentinel.example.internal:26379"},
				SentinelMaster: "mymaster",
				DialTimeout:    100 * time.Millisecond,
			},
			wantTLSErr: true,
		},
		{
			name: "standalone loopback — ok (no TLS error expected)",
			cfg: Config{
				Mode:        ModeStandalone,
				Addr:        "127.0.0.1:6379",
				DialTimeout: 100 * time.Millisecond,
			},
			wantTLSErr: false,
		},
		{
			name: "standalone rediss scheme — ok (TLS scheme accepted)",
			cfg: Config{
				Mode:        ModeStandalone,
				Addr:        "rediss://prod.redis.example.internal:6379",
				DialTimeout: 100 * time.Millisecond,
			},
			wantTLSErr: false,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := NewClient(context.Background(), tc.cfg)
			if tc.wantTLSErr {
				// Phase-1 (stub): err may be nil or a generic network error — no TLS signal.
				// The test FAILS because the error lacks the TLS validation code.
				// Phase-2: ValidateTLSEndpoint produces an immediate error with TLS code.
				require.Error(t, err,
					"NewClient(%+v): expected TLS validation error, got nil (phase-1 red-light)", tc.cfg)
				// TODO(phase2): use errors.Is(err, errcode.ErrAdapterEndpointNotTLS)
				if !strings.Contains(err.Error(), tlsErrSignal) &&
					!strings.Contains(err.Error(), "TLS") &&
					!strings.Contains(err.Error(), "tls") {
					t.Errorf("NewClient(%+v): error %q lacks TLS validation signal %q (phase-1 red-light: stub allows non-TLS through producing DNS/connection error instead)",
						tc.cfg, err.Error(), tlsErrSignal)
				}
			} else {
				// Loopback / TLS scheme: must not produce a TLS validation error.
				// Connection refused is acceptable (no real Redis server running).
				if err != nil {
					if strings.Contains(err.Error(), tlsErrSignal) ||
						strings.Contains(err.Error(), "not tls") {
						t.Errorf("NewClient(%+v): unexpected TLS validation error: %v", tc.cfg, err)
					}
				}
			}
		})
	}
}
