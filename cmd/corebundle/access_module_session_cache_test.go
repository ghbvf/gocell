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
