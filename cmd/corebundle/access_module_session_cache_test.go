package main

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	adapterredis "github.com/ghbvf/gocell/adapters/redis"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/runtime/auth/session"
)

// stubSessionStore is a minimal session.Store used only to obtain a stable
// pointer identity for the wrap/no-wrap assertions below.
type stubSessionStore struct{}

func (stubSessionStore) Create(context.Context, *session.Session) error { return nil }
func (stubSessionStore) Get(context.Context, string) (*session.ValidateView, error) {
	return nil, errors.New("stub: never called")
}
func (stubSessionStore) Revoke(context.Context, string) error { return nil }
func (stubSessionStore) RevokeForSubject(context.Context, string, session.CredentialEvent) error {
	return nil
}
func (stubSessionStore) RepoReady(context.Context) error { return nil }

func newDisableTestLogger() (*slog.Logger, *bytes.Buffer) {
	buf := &bytes.Buffer{}
	logger := slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelInfo}))
	return logger, buf
}

// TestWrapSessionStoreWithCache_EnvUnset_NoWrap — default form. No env knob
// set → return inner unchanged. Verifies the default-off contract.
func TestWrapSessionStoreWithCache_EnvUnset_NoWrap(t *testing.T) {
	t.Setenv(envSessionCacheTTL, "")
	logger, buf := newDisableTestLogger()

	inner := stubSessionStore{}
	got, err := wrapSessionStoreWithCache(inner, &SharedDeps{}, logger)

	require.NoError(t, err)
	assert.Equal(t, inner, got, "env unset: must return inner unchanged")
	assert.NotContains(t, buf.String(), "session cache",
		"env unset: no warn/info should be logged")
}

// TestWrapSessionStoreWithCache_TTLZero_DisablesWithWarn — env present but
// non-positive Duration → cache disabled, slog.Warn fired, inner returned.
func TestWrapSessionStoreWithCache_TTLZero_DisablesWithWarn(t *testing.T) {
	t.Setenv(envSessionCacheTTL, "0s")
	logger, buf := newDisableTestLogger()

	inner := stubSessionStore{}
	got, err := wrapSessionStoreWithCache(inner, &SharedDeps{}, logger)

	require.NoError(t, err)
	assert.Equal(t, inner, got)
	assert.True(t, strings.Contains(buf.String(), "session cache disabled"),
		"zero ttl: must log disable reason; got %q", buf.String())
}

// TestWrapSessionStoreWithCache_TTLInvalid_DisablesWithWarn — un-parseable
// Duration → cache disabled, slog.Warn fired, inner returned.
func TestWrapSessionStoreWithCache_TTLInvalid_DisablesWithWarn(t *testing.T) {
	t.Setenv(envSessionCacheTTL, "not-a-duration")
	logger, buf := newDisableTestLogger()

	inner := stubSessionStore{}
	got, err := wrapSessionStoreWithCache(inner, &SharedDeps{}, logger)

	require.NoError(t, err)
	assert.Equal(t, inner, got)
	assert.True(t, strings.Contains(buf.String(), "session cache disabled"),
		"invalid ttl: must log disable reason; got %q", buf.String())
}

// TestWrapSessionStoreWithCache_TTLNegative_DisablesWithWarn — explicit
// negative duration → disabled, warn, inner returned.
func TestWrapSessionStoreWithCache_TTLNegative_DisablesWithWarn(t *testing.T) {
	t.Setenv(envSessionCacheTTL, "-1s")
	logger, buf := newDisableTestLogger()

	inner := stubSessionStore{}
	got, err := wrapSessionStoreWithCache(inner, &SharedDeps{}, logger)

	require.NoError(t, err)
	assert.Equal(t, inner, got)
	assert.True(t, strings.Contains(buf.String(), "session cache disabled"))
}

// TestWrapSessionStoreWithCache_NoRedisClient_DisablesWithWarn — env set but
// SharedDeps.RedisClient is nil → cache disabled (cannot construct), warn,
// inner returned. Documents that the wiring layer treats Redis-not-configured
// as a soft disable rather than a fail-fast (consistent with default-off
// semantics: cache is best-effort, never required).
func TestWrapSessionStoreWithCache_NoRedisClient_DisablesWithWarn(t *testing.T) {
	t.Setenv(envSessionCacheTTL, "30s")
	logger, buf := newDisableTestLogger()

	inner := stubSessionStore{}
	got, err := wrapSessionStoreWithCache(inner, &SharedDeps{}, logger)

	require.NoError(t, err)
	assert.Equal(t, inner, got)
	assert.True(t, strings.Contains(buf.String(), "no Redis client"),
		"no Redis client: must log specific reason; got %q", buf.String())
}

// TestWrapSessionStoreWithCache_RedisStubFailsConstruction — env set + a
// stub RedisClient (no underlying cmdable) → NewCache fails fast at the
// constructor, propagated as a startup error. Documents the contract that
// Redis-misconfiguration is a wiring bug, not a runtime tolerance.
func TestWrapSessionStoreWithCache_RedisStubFailsConstruction(t *testing.T) {
	t.Setenv(envSessionCacheTTL, "30s")
	logger, _ := newDisableTestLogger()

	inner := stubSessionStore{}
	got, err := wrapSessionStoreWithCache(inner, &SharedDeps{
		RedisClient: new(adapterredis.Client), // empty Client; cmdable is nil
	}, logger)

	require.Error(t, err, "stub redis client: NewCache must fail fast")
	assert.Nil(t, got)
	assert.Contains(t, err.Error(), "session cache")
}

// TestWrapSessionStoreWithCache_NilLogger_FallsBackToDefault — the helper
// follows the cell-constructor nil-fallback convention so the production
// Provide call site can pass nil and let the helper own the slog.Default()
// snapshot. Tests inject a real logger; production passes nil.
func TestWrapSessionStoreWithCache_NilLogger_FallsBackToDefault(t *testing.T) {
	t.Setenv(envSessionCacheTTL, "")

	inner := stubSessionStore{}
	got, err := wrapSessionStoreWithCache(inner, &SharedDeps{}, nil)

	require.NoError(t, err, "nil logger must fall back to slog.Default(), not crash")
	assert.Equal(t, inner, got)
}

// TestWrapSessionStoreWithCache_TTLExceedsMax_FailFast — T4 RED test.
//
// GOCELL_SESSION_CACHE_TTL=31s exceeds the documented ≤ 30s recommended
// maximum. The wiring function must fail-fast with errcode.ErrValidationFailed
// (not silently return inner unchanged) because a TTL above the documented
// maximum is a wiring misconfiguration, not a runtime tolerance.
//
// The type godoc (session_cache_store.go:57) declares "≤ 30s recommended",
// and Q7 from the plan aligns this to a hard wiring upper bound.
//
// Current code (access_module.go:288) only checks `ttl <= 0` and has no Redis
// client → falls through to the "no Redis client" warn path and returns
// (inner, nil) unchanged. This test asserts that behavior is WRONG — we
// should get a validation error before reaching the Redis-nil check.
//
// RED state: current code returns (inner, nil) for TTL=31s + nil Redis.
// GREEN fix: adds a 30s upper-bound check after `ttl <= 0`, returning an error
// before the Redis-nil guard runs.
func TestWrapSessionStoreWithCache_TTLExceedsMax_FailFast(t *testing.T) {
	t.Setenv(envSessionCacheTTL, "31s")
	logger, _ := newDisableTestLogger()

	inner := stubSessionStore{}
	// SharedDeps with nil RedisClient — current code warns and returns (inner, nil).
	// After the GREEN fix, the TTL upper-bound check fires first and returns an error.
	got, err := wrapSessionStoreWithCache(inner, &SharedDeps{}, logger)

	// RED: current code returns (inner, nil) here; GREEN fix returns (nil, err).
	require.Error(t, err,
		"TTL=31s exceeds documented max (30s): wrapSessionStoreWithCache must fail-fast with an error, "+
			"not silently return inner unchanged")
	assert.Nil(t, got,
		"wrapSessionStoreWithCache must return nil store on TTL-exceeds-max error")

	var coded *errcode.Error
	require.ErrorAs(t, err, &coded,
		"error must be *errcode.Error for TTL-exceeds-max; got %T: %v", err, err)
	assert.Equal(t, errcode.ErrValidationFailed, coded.Code,
		"error code must be ErrValidationFailed for TTL-exceeds-max wiring misconfiguration")
}
