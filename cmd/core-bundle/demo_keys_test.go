package main

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRejectDemoKey_DevMode_AlwaysPasses(t *testing.T) {
	for _, demo := range wellKnownDemoKeys {
		err := rejectDemoKey("", "X_TEST_ENV", []byte(demo))
		require.NoError(t, err, "dev mode must not reject demo key %q", demo)
	}
}

func TestRejectDemoKey_RealMode_RejectsEachDemoValue(t *testing.T) {
	for _, demo := range wellKnownDemoKeys {
		demo := demo
		t.Run(demo, func(t *testing.T) {
			err := rejectDemoKey("real", "X_TEST_ENV", []byte(demo))
			require.Error(t, err, "real mode must reject demo key %q", demo)
			assert.Contains(t, err.Error(), "X_TEST_ENV")
			assert.Contains(t, err.Error(), "well-known demo key")
		})
	}
}

func TestRejectDemoKey_RealMode_AcceptsFreshSecret(t *testing.T) {
	fresh := bytes.Repeat([]byte("z"), 32)
	err := rejectDemoKey("real", "GOCELL_AUDIT_CURSOR_KEY", fresh)
	require.NoError(t, err, "real mode must accept a non-demo secret")
}

func TestRejectDemoKey_RealMode_EmptyKeyPasses(t *testing.T) {
	// Empty keys are handled upstream by loadSecret; rejectDemoKey must not
	// treat them as a demo match (len mismatch).
	err := rejectDemoKey("real", "GOCELL_AUDIT_CURSOR_KEY", nil)
	require.NoError(t, err)
}
