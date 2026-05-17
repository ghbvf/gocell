package main

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/idempotency"
	"github.com/ghbvf/gocell/pkg/testutil/testtime"
	"github.com/ghbvf/gocell/runtime/auth"
	"github.com/ghbvf/gocell/runtime/auth/keystest"
	"github.com/ghbvf/gocell/runtime/bootstrap"
	"github.com/ghbvf/gocell/runtime/eventbus"
)

// minimalSharedDepsForAuditTest builds a SharedDeps that passes Validate() for
// memory topology with an empty AdapterMode. It sets GOCELL_STATE_DIR and the
// JWT env vars required by the build path, but leaves all cell-specific secrets
// (HMAC key, cursor keys) for the caller to set via t.Setenv.
func minimalSharedDepsForAuditTest(t *testing.T, adapterMode string) *SharedDeps {
	t.Helper()
	t.Setenv("GOCELL_STATE_DIR", t.TempDir())
	t.Setenv("GOCELL_JWT_ISSUER", "test-issuer")
	t.Setenv("GOCELL_JWT_AUDIENCE", "test-audience")

	eb := eventbus.New(eventbus.WithClock(clock.Real()))
	privKey, pubKey := keystest.MustGenerateKeyPair()
	keySet, err := auth.NewKeySet(privKey, pubKey, clock.Real())
	require.NoError(t, err)
	issuer, err := auth.NewJWTIssuer(keySet, "test-issuer", testtime.D15min, clock.Real(),
		auth.WithIssuerAudiencesFromSlice([]string{"test-audience"}))
	require.NoError(t, err)
	verifier, err := auth.NewJWTVerifier(keySet, clock.Real(),
		auth.WithExpectedAudiences("test-audience"))
	require.NoError(t, err)

	ps, err := buildPromStack()
	require.NoError(t, err)

	return &SharedDeps{
		Clock:               clock.Real(),
		Topology:            bootstrap.Topology{StorageBackend: "memory", AdapterMode: adapterMode},
		JWTDeps:             jwtDeps{issuer: issuer, verifier: verifier},
		PromStack:           ps,
		EventBus:            eb,
		ConsumerClaimer:     idempotency.NewInMemClaimer(clock.Real()),
		ConsumerClaimerKind: consumerClaimerKindInMemory,
	}
}

// TestAuditCoreModule_Provide_HMACKeyMissing_RealMode verifies that when the
// HMAC key env var is absent in adapter mode "real", AuditCoreModule.Provide
// returns an error that:
//   - contains the env variable name (GOCELL_AUDITCORE_HMAC_KEY) for operator
//     diagnosis, and
//   - contains the label prefix "auditcore HMAC" exactly once (outer module
//     wrapper), not twice (which would indicate buildHMACKey also embeds it).
//
// This test will FAIL on HEAD where the error chain is:
//
//	"auditcore HMAC key: auditcore HMAC: GOCELL_AUDITCORE_HMAC_KEY must be set ..."
//
// giving strings.Count("auditcore HMAC") == 2.
//
// Refs: PR#232 review finding P2 (double label in audit module error chain).
func TestAuditCoreModule_Provide_HMACKeyMissing_RealMode(t *testing.T) {
	// Ensure the HMAC key env var is absent.
	t.Setenv("GOCELL_AUDITCORE_HMAC_KEY", "")
	// Provide a valid cursor key so the error comes from the HMAC path, not the
	// cursor path.
	t.Setenv("GOCELL_AUDITCORE_CURSOR_KEY", "audit-cursor-key-32-bytes-padded!")

	shared := minimalSharedDepsForAuditTest(t, "real")

	_, _, _, err := AuditCoreModule{}.Provide(context.Background(), shared)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "GOCELL_AUDITCORE_HMAC_KEY",
		"error must name the env var for operator diagnosis")
	// The label "auditcore HMAC" must appear exactly once (from the outer module
	// wrapper "auditcore HMAC key: ..."). If buildHMACKey also embeds it the
	// count rises to 2, which this assertion will catch.
	assert.Equal(t, 1, strings.Count(err.Error(), "auditcore HMAC"),
		"label must appear exactly once in the error chain; got %q", err.Error())
}
