package postgres

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestConfigFromEnv_Defaults(t *testing.T) {
	// Clear any env vars that might be set.
	for _, key := range []string{"GOCELL_PG_DSN", "GOCELL_PG_MAX_CONNS", "GOCELL_PG_IDLE_TIMEOUT", "GOCELL_PG_MAX_LIFETIME"} {
		t.Setenv(key, "")
	}

	cfg := ConfigFromEnv()

	assert.Equal(t, "", cfg.DSN)
	assert.Equal(t, int32(defaultMaxConns), cfg.MaxConns)
	assert.Equal(t, defaultIdleTimeout, cfg.IdleTimeout)
	assert.Equal(t, defaultMaxLifetime, cfg.MaxLifetime)
}

func TestConfigFromEnv_CustomValues(t *testing.T) {
	t.Setenv("GOCELL_PG_DSN", "postgres://test:test@localhost:5432/testdb")
	t.Setenv("GOCELL_PG_MAX_CONNS", "25")
	t.Setenv("GOCELL_PG_IDLE_TIMEOUT", "10m")
	t.Setenv("GOCELL_PG_MAX_LIFETIME", "2h")

	cfg := ConfigFromEnv()

	assert.Equal(t, "postgres://test:test@localhost:5432/testdb", cfg.DSN)
	assert.Equal(t, int32(25), cfg.MaxConns)
	assert.Equal(t, 10*time.Minute, cfg.IdleTimeout)
	assert.Equal(t, 2*time.Hour, cfg.MaxLifetime)
}

func TestConfigFromEnv_InvalidValues(t *testing.T) {
	t.Setenv("GOCELL_PG_DSN", "postgres://localhost/db")
	t.Setenv("GOCELL_PG_MAX_CONNS", "not-a-number")
	t.Setenv("GOCELL_PG_IDLE_TIMEOUT", "bad-duration")
	t.Setenv("GOCELL_PG_MAX_LIFETIME", "bad-duration")

	cfg := ConfigFromEnv()

	// Invalid values should fall back to defaults.
	assert.Equal(t, int32(defaultMaxConns), cfg.MaxConns)
	assert.Equal(t, defaultIdleTimeout, cfg.IdleTimeout)
	assert.Equal(t, defaultMaxLifetime, cfg.MaxLifetime)
}

func TestConfigFromEnv_NegativeMaxConns(t *testing.T) {
	t.Setenv("GOCELL_PG_DSN", "postgres://localhost/db")
	t.Setenv("GOCELL_PG_MAX_CONNS", "-5")

	cfg := ConfigFromEnv()
	assert.Equal(t, int32(defaultMaxConns), cfg.MaxConns)
}

func TestConfig_ApplyDefaults(t *testing.T) {
	tests := []struct {
		name        string
		input       Config
		wantConns   int32
		wantIdle    time.Duration
		wantMaxLife time.Duration
	}{
		{
			name:        "all zero",
			input:       Config{},
			wantConns:   defaultMaxConns,
			wantIdle:    defaultIdleTimeout,
			wantMaxLife: defaultMaxLifetime,
		},
		{
			name:        "partial set",
			input:       Config{MaxConns: 20},
			wantConns:   20,
			wantIdle:    defaultIdleTimeout,
			wantMaxLife: defaultMaxLifetime,
		},
		{
			name:        "all set",
			input:       Config{MaxConns: 5, IdleTimeout: 2 * time.Minute, MaxLifetime: 30 * time.Minute},
			wantConns:   5,
			wantIdle:    2 * time.Minute,
			wantMaxLife: 30 * time.Minute,
		},
		{
			name:        "negative conns",
			input:       Config{MaxConns: -1},
			wantConns:   defaultMaxConns,
			wantIdle:    defaultIdleTimeout,
			wantMaxLife: defaultMaxLifetime,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.input.applyDefaults()
			assert.Equal(t, tt.wantConns, tt.input.MaxConns)
			assert.Equal(t, tt.wantIdle, tt.input.IdleTimeout)
			assert.Equal(t, tt.wantMaxLife, tt.input.MaxLifetime)
		})
	}
}

func TestNewPool_EmptyDSN(t *testing.T) {
	_, err := NewPool(t.Context(), Config{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "DSN is empty")
}

func TestNewPool_InvalidDSN(t *testing.T) {
	_, err := NewPool(t.Context(), Config{DSN: "://invalid"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "parse DSN")
}

func TestPoolStats_ZeroValue(t *testing.T) {
	var stats PoolStats
	assert.Equal(t, int64(0), stats.AcquireCount)
	assert.Equal(t, time.Duration(0), stats.AcquireDuration)
	assert.Equal(t, int32(0), stats.AcquiredConns)
	assert.Equal(t, int64(0), stats.CanceledAcquireCount)
	assert.Equal(t, int32(0), stats.ConstructingConns)
	assert.Equal(t, int64(0), stats.EmptyAcquireCount)
	assert.Equal(t, int32(0), stats.IdleConns)
	assert.Equal(t, int32(0), stats.MaxConns)
	assert.Equal(t, int32(0), stats.TotalConns)
	assert.Equal(t, int64(0), stats.NewConnsCount)
	assert.Equal(t, int64(0), stats.MaxLifetimeDestroyCount)
	assert.Equal(t, int64(0), stats.MaxIdleDestroyCount)
}

func TestPoolStats_JSON_CamelCase(t *testing.T) {
	stats := PoolStats{AcquireCount: 5, IdleConns: 2, TotalConns: 10}
	b, err := json.Marshal(stats)
	require.NoError(t, err)
	s := string(b)
	assert.Contains(t, s, `"acquireCount"`)
	assert.Contains(t, s, `"idleConns"`)
	assert.Contains(t, s, `"totalConns"`)
}

func TestPoolStats_NilInner(t *testing.T) {
	// Defensive: PoolStats on uninitialized Pool returns zero value, no panic.
	p := &Pool{}
	stats := p.PoolStats()
	assert.Equal(t, PoolStats{}, stats)
}

func TestNewPool_UnreachableHost(t *testing.T) {
	if os.Getenv("PG_INTEGRATION") == "" {
		t.Skip("skipping integration test; set PG_INTEGRATION=1 to run")
	}
	_, err := NewPool(t.Context(), Config{
		DSN:      "postgres://nobody:nopass@127.0.0.1:1/nonexistent",
		MaxConns: 1,
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ERR_ADAPTER_PG_CONNECT")
}

// ---------------------------------------------------------------------------
// T17: Pool.Close(ctx) tests
// ---------------------------------------------------------------------------

// TestPool_Close_PreCancelledCtxReturnsError verifies that Close
// with a pre-cancelled context returns ctx.Err() promptly without attempting
// the underlying pool drain.
func TestPool_Close_PreCancelledCtxReturnsError(t *testing.T) {
	// Use a zero Pool (inner=nil) — Close must short-circuit on ctx.Err()
	// before reaching the goroutine, so inner being nil is acceptable.
	p := &Pool{}

	cancelledCtx, cancel := context.WithCancel(context.Background())
	cancel()

	err := p.Close(cancelledCtx)
	require.Error(t, err, "Close with pre-cancelled ctx must return error")
	assert.Equal(t, context.Canceled, err)
}

// TestPool_Close_ImplementsContextCloser verifies that *Pool satisfies the
// lifecycle.ContextCloser interface (Close(context.Context) error).
func TestPool_Close_ImplementsContextCloser(t *testing.T) {
	var _ interface {
		Close(ctx context.Context) error
	} = (*Pool)(nil)
}
