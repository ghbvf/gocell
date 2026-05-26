package main

import (
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNewSSOBFFAppFailsFastWithoutServiceSecret(t *testing.T) {
	t.Setenv(ssobffServiceKeyEnv, "")

	app, err := NewSSOBFFApp(WithSSOBFFLogger(slog.New(slog.NewTextHandler(io.Discard, nil))))
	require.Error(t, err)
	require.Nil(t, app)
	require.True(t, strings.Contains(err.Error(), ssobffServiceKeyEnv), "error must name missing env var: %v", err)
}

func TestNewSSOBFFAppFailsFastWithoutDatabaseURL(t *testing.T) {
	t.Setenv(ssobffDatabaseURLEnv, "")

	app, err := NewSSOBFFApp(
		WithSSOBFFLogger(slog.New(slog.NewTextHandler(io.Discard, nil))),
		WithSSOBFFInternalServiceSecret("ssobff-test-service-secret-32b!!!"),
	)
	require.Error(t, err)
	require.Nil(t, app)
	require.True(t, strings.Contains(err.Error(), ssobffDatabaseURLEnv), "error must name missing env var: %v", err)
}

// NOTE: TestNewSSOBFFApp_AcceptsInjectedListeners was removed in the #941
// un-skip PR. It silently skipped without DATABASE_URL (the same anti-pattern
// this PR eliminates for smoke/walkthrough) and only asserted the three
// listen-addr getters echo the injected listeners — a path TestWalkthrough
// already covers strictly (it injects all three listeners via WithSSOBFFListener
// and boots+serves on them, dialing via the same getters). Re-adding a
// container-spinning getter test would be redundant.
