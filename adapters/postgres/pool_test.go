package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/kernel/lifecycle"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/testutil/testtime"
)

// Compile-time assertion: *Pool must implement lifecycle.ManagedResource.
var _ lifecycle.ManagedResource = (*Pool)(nil)

// testPostgresDSN is a fixture DSN used for unit-level struct tests; no real DB is contacted.
// Constructed as a concat to prevent gosec G101 false-positive on test fixture URLs.
var testPostgresDSN = "postgres://test:" + "test@localhost:5432/testdb"

// testPostgresUnreachableDSN targets a port that is never open in CI; used to test error paths.
var testPostgresUnreachableDSN = "postgres://nobody:" + "nopass@127.0.0.1:1/nonexistent"

// testPostgresBlackholeDSN targets RFC 5737 TEST-NET-1 — routers drop SYN packets,
// exercising the TCP-handshake-timeout branch (vs immediate connection-refused).
var testPostgresBlackholeDSN = "postgres://nobody:" + "nopass@192.0.2.1:5432/nonexistent"

// File-local Duration constants for ConnectTimeout tests, satisfying
// TEST-TIME-LITERAL-01 (no bare duration literals in test bodies).
const (
	poolTestConnectExplicit  = 7 * time.Second        // explicit value passed through applyDefaults
	poolTestConnectDefault5s = 5 * time.Second        // expected default after applyDefaults
	poolTestConnectShort     = 200 * time.Millisecond // tight bound for blackhole / DSN-override tests
	poolTestConnectAssertCap = 5 * time.Second        // upper bound the test asserts elapsed against (slack for CI scheduling)
	poolTestConnectNegative  = time.Duration(-1)      // negative input for default-fallback case
	poolTestConnectUnreach1s = 1 * time.Second        // bound for the immediate-refused (127.0.0.1:1) test
)

// TestConfig_ZeroValue verifies that a zero Config has empty DSN and zero
// numeric fields. applyDefaults fills them; callers supply explicit values.
func TestConfig_ZeroValue(t *testing.T) {
	cfg := Config{}
	assert.Equal(t, "", cfg.DSN)
	assert.EqualValues(t, 0, cfg.MaxConns)
	assert.Equal(t, time.Duration(0), cfg.IdleTimeout)
	assert.Equal(t, time.Duration(0), cfg.MaxLifetime)
	assert.Equal(t, time.Duration(0), cfg.ConnectTimeout)
}

// TestConfig_ExplicitValues verifies that a Config struct literal passes
// values through unchanged before applyDefaults is called.
func TestConfig_ExplicitValues(t *testing.T) {
	cfg := Config{
		DSN:         testPostgresDSN,
		MaxConns:    25,
		IdleTimeout: testtime.D10min,
		MaxLifetime: testtime.D2h,
	}
	assert.Equal(t, "postgres://test:test@localhost:5432/testdb", cfg.DSN)
	assert.EqualValues(t, 25, cfg.MaxConns)
	assert.Equal(t, testtime.D10min, cfg.IdleTimeout)
	assert.Equal(t, testtime.D2h, cfg.MaxLifetime)
}

func TestConfig_ApplyDefaults(t *testing.T) {
	tests := []struct {
		name          string
		input         Config
		wantConns     int32
		wantIdle      time.Duration
		wantMaxLife   time.Duration
		wantConnectTO time.Duration
	}{
		{
			name:          "all zero",
			input:         Config{},
			wantConns:     defaultMaxConns,
			wantIdle:      defaultIdleTimeout,
			wantMaxLife:   defaultMaxLifetime,
			wantConnectTO: defaultConnectTimeout,
		},
		{
			name:          "partial set",
			input:         Config{MaxConns: 20},
			wantConns:     20,
			wantIdle:      defaultIdleTimeout,
			wantMaxLife:   defaultMaxLifetime,
			wantConnectTO: defaultConnectTimeout,
		},
		{
			name:          "all set",
			input:         Config{MaxConns: 5, IdleTimeout: testtime.D2min, MaxLifetime: testtime.D30min, ConnectTimeout: poolTestConnectExplicit},
			wantConns:     5,
			wantIdle:      testtime.D2min,
			wantMaxLife:   testtime.D30min,
			wantConnectTO: poolTestConnectExplicit,
		},
		{
			name:          "negative conns",
			input:         Config{MaxConns: -1},
			wantConns:     defaultMaxConns,
			wantIdle:      defaultIdleTimeout,
			wantMaxLife:   defaultMaxLifetime,
			wantConnectTO: defaultConnectTimeout,
		},
		{
			name:          "negative connect timeout",
			input:         Config{ConnectTimeout: poolTestConnectNegative},
			wantConns:     defaultMaxConns,
			wantIdle:      defaultIdleTimeout,
			wantMaxLife:   defaultMaxLifetime,
			wantConnectTO: defaultConnectTimeout,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.input.applyDefaults()
			assert.Equal(t, tt.wantConns, tt.input.MaxConns)
			assert.Equal(t, tt.wantIdle, tt.input.IdleTimeout)
			assert.Equal(t, tt.wantMaxLife, tt.input.MaxLifetime)
			assert.Equal(t, tt.wantConnectTO, tt.input.ConnectTimeout)
		})
	}
}

// TestConfig_ApplyDefaults_ConnectTimeout asserts the default 5s value is
// applied when zero, locking the field's existence and default in one place.
func TestConfig_ApplyDefaults_ConnectTimeout(t *testing.T) {
	cfg := Config{}
	cfg.applyDefaults()
	assert.Equal(t, poolTestConnectDefault5s, cfg.ConnectTimeout,
		"zero ConnectTimeout must default to 5s")
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

func TestPool_Stats_NotInitialized(t *testing.T) {
	var nilPool *Pool
	assert.Equal(t, "pool not initialized", nilPool.Stats())
	assert.Equal(t, "pool not initialized", (&Pool{}).Stats())
}

// TestNewPool_UnreachableHost exercises the immediate connection-refused branch
// (127.0.0.1:1 — kernel returns RST, no SYN timeout). Modernized to drive the
// adapter-level Config.ConnectTimeout instead of a caller-side
// context.WithTimeout, so the new field is on the production hot path.
func TestNewPool_UnreachableHost(t *testing.T) {
	_, err := NewPool(t.Context(), Config{
		DSN:            testPostgresUnreachableDSN,
		MaxConns:       1,
		ConnectTimeout: poolTestConnectUnreach1s,
	})
	require.Error(t, err)

	var ec *errcode.Error
	require.True(t, errors.As(err, &ec), "NewPool unreachable error must be structured errcode: %v", err)
	assert.Equal(t, ErrAdapterPGConnect, ec.Code)
}

// TestNewPool_ConnectTimeout_Blackhole verifies Config.ConnectTimeout actually
// bounds a TCP-handshake-blackhole scenario. Without the field, pgxpool falls
// back to its 2 min internal default; with ConnectTimeout=200ms the call must
// fail in well under 5s. Skipped under -short because it depends on the
// runner's network reaching RFC 5737 TEST-NET-1 (192.0.2.0/24) as a black
// hole (the standard cloud and most corporate networks do; a few exotic
// setups may RST instead, exercising the wrong code path).
func TestNewPool_ConnectTimeout_Blackhole(t *testing.T) {
	if testing.Short() {
		t.Skip("blackhole timeout test requires network reach to RFC 5737 TEST-NET-1")
	}
	start := time.Now()
	_, err := NewPool(t.Context(), Config{
		DSN:            testPostgresBlackholeDSN,
		MaxConns:       1,
		ConnectTimeout: poolTestConnectShort,
	})
	elapsed := time.Since(start)

	require.Error(t, err)
	var ec *errcode.Error
	require.True(t, errors.As(err, &ec), "blackhole error must be structured errcode: %v", err)
	assert.Equal(t, ErrAdapterPGConnect, ec.Code)
	assert.Less(t, elapsed, poolTestConnectAssertCap,
		"NewPool must respect ConnectTimeout=200ms; elapsed=%v (would otherwise hang ~2 min on pgxpool fallback)",
		elapsed)
}

// TestNewPool_ConnectTimeout_DSNOverride_CodeWins documents the DSN-vs-Config
// precedence: explicit Config.ConnectTimeout overrides any DSN connect_timeout.
// The DSN is unreachable so we just inspect the elapsed time — if Config didn't
// win, the DSN's 30s would dominate over Config's 200ms. Skipped under -short
// (same network-reachability caveat as TestNewPool_ConnectTimeout_Blackhole).
func TestNewPool_ConnectTimeout_DSNOverride_CodeWins(t *testing.T) {
	if testing.Short() {
		t.Skip("DSN-override test requires network reach to RFC 5737 TEST-NET-1")
	}
	dsn := "postgres://nobody:" + "nopass@192.0.2.1:5432/nonexistent?connect_timeout=30"
	start := time.Now()
	_, err := NewPool(t.Context(), Config{
		DSN:            dsn,
		MaxConns:       1,
		ConnectTimeout: poolTestConnectShort,
	})
	elapsed := time.Since(start)

	require.Error(t, err)
	assert.Less(t, elapsed, poolTestConnectAssertCap,
		"Config.ConnectTimeout=200ms must override DSN connect_timeout=30s; elapsed=%v",
		elapsed)
}

// TestNewPool_InvalidDSN_NoPasswordLeak asserts that a parse error never echoes
// the raw password back into the error string (PII safety).
func TestNewPool_InvalidDSN_NoPasswordLeak(t *testing.T) {
	const sensitivePassword = "verysecretpass"
	dsn := "postgres://user:" + sensitivePassword + "@localhost/db?sslmode=invalid_mode_value"
	_, err := NewPool(t.Context(), Config{DSN: dsn})
	require.Error(t, err)
	assert.NotContains(t, err.Error(), sensitivePassword,
		"NewPool error must not echo raw password from DSN")
}

// ---------------------------------------------------------------------------
// T17: Pool.Close(ctx) tests
// ---------------------------------------------------------------------------

// TestPool_Close_PreCancelledCtxReturnsError verifies that Close
// with a pre-canceled context returns ctx.Err() promptly without attempting
// the underlying pool drain.
func TestPool_Close_PreCancelledCtxReturnsError(t *testing.T) {
	// Use a zero Pool (inner=nil) — Close must short-circuit on ctx.Err()
	// before reaching the goroutine, so inner being nil is acceptable.
	p := &Pool{}

	cancelledCtx, cancel := context.WithCancel(context.Background())
	cancel()

	err := p.Close(cancelledCtx)
	require.Error(t, err, "Close with pre-canceled ctx must return error")
	assert.Equal(t, context.Canceled, err)
}

// TestPool_Close_ImplementsContextCloser verifies that *Pool satisfies the
// lifecycle.ContextCloser interface (Close(context.Context) error).
func TestPool_Close_ImplementsContextCloser(t *testing.T) {
	var _ interface {
		Close(ctx context.Context) error
	} = (*Pool)(nil)
}

// ---------------------------------------------------------------------------
// ManagedResource interface tests (absorbed from pool_resource_test.go)
// ---------------------------------------------------------------------------

// TestPool_CheckersReturnsBothProbes verifies that Checkers() returns exactly
// two named probes: "postgres_ready" and "postgres_indexes_valid_ready".
// No real DB is required — we only verify the map structure.
func TestPool_CheckersReturnsBothProbes(t *testing.T) {
	p := &Pool{} // stub: inner=nil; Checkers() must not dereference inner
	checkers := p.Checkers()
	require.Len(t, checkers, 2, "expected 2 checkers (postgres_ready + postgres_indexes_valid_ready)")
	for _, name := range []string{"postgres_ready", "postgres_indexes_valid_ready"} {
		fn, ok := checkers[name]
		if !ok {
			t.Errorf("expected checker named %q", name)
		}
		if fn == nil {
			t.Errorf("checker %q function must not be nil", name)
		}
	}
}

// TestPool_WorkerReturnsNil verifies that Worker() always returns nil —
// Pool itself has no background goroutine; outbox relay is a separate resource.
func TestPool_WorkerReturnsNil(t *testing.T) {
	p := &Pool{}
	if p.Worker() != nil {
		t.Error("expected Worker() to return nil: pool has no background worker")
	}
}

// TestPool_CloseDelegatesAndAcceptsCtx verifies that Close(ctx) delegates to
// the underlying pool drain and that a pre-canceled context surfaces an error
// (same behavior as the prior PGResource wrapper).
func TestPool_CloseDelegatesAndAcceptsCtx(t *testing.T) {
	// Pre-canceled context — Close must propagate ctx.Err().
	canceledCtx, cancel := context.WithCancel(context.Background())
	cancel()

	p := &Pool{} // inner=nil; CloseWithDeadline checks ctx.Err() first.
	err := p.Close(canceledCtx)
	require.Error(t, err, "Close with pre-canceled ctx must return error")
	assert.Equal(t, context.Canceled, err)
}

// TestPool_CheckerTimeout verifies that the "postgres_ready" probe applies an
// inner ~5-second context deadline regardless of the caller's context.
func TestPool_CheckerTimeout(t *testing.T) {
	var receivedDeadline time.Time

	p := &Pool{}
	// Override the health function via the checkerHealthFnForTest field so we
	// can inspect the context without a real DB.  Because Pool is in the same
	// package we can set the unexported test hook directly.
	p.checkerHealthFnForTest = func(ctx context.Context) error {
		dl, _ := ctx.Deadline()
		receivedDeadline = dl
		return nil
	}

	checkers := p.Checkers()
	fn := checkers["postgres_ready"]
	require.NotNil(t, fn, "postgres_ready checker must not be nil")

	require.NoError(t, fn(context.Background()))

	diff := time.Until(receivedDeadline)
	if diff < testtime.D3s || diff > testtime.D7s {
		t.Errorf("expected checker deadline ~5s from now, got %v", diff)
	}
}
