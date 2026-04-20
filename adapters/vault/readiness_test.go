//go:build integration

package vault_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"testing"
	"time"

	vaultapi "github.com/hashicorp/vault/api"

	vaultadapter "github.com/ghbvf/gocell/adapters/vault"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// isErrCode reports whether err, anywhere in its chain, carries the given errcode.Code.
func isErrCode(err error, code errcode.Code) bool {
	var ec *errcode.Error
	if !errors.As(err, &ec) {
		return false
	}
	return ec.Code == code
}

// ---------------------------------------------------------------------------
// TC-INT-6: healthy transit → Checkers["vault_transit_ready"]() == nil
// ---------------------------------------------------------------------------

// TestTransitReadiness_Healthy verifies that Checkers["vault_transit_ready"]()
// returns nil when Vault is running and the transit key exists.
//
// ref: external-secrets/external-secrets pkg/provider/vault — ValidateStore
//
//	uses token/lookup + business-path probe (not sys/health, vault#28846)
func TestTransitReadiness_Healthy(t *testing.T) {
	ctx := context.Background()
	addr, token, teardown := startVaultContainer(t)
	defer teardown()

	_ = ctx
	p := newProviderFromEnv(t, addr, token)

	checkers := p.Checkers()
	require.NotNil(t, checkers, "Checkers must not return nil")

	probe, ok := checkers["vault_transit_ready"]
	require.True(t, ok, "Checkers must contain 'vault_transit_ready' key")
	require.NotNil(t, probe, "vault_transit_ready probe must not be nil")

	err := probe()
	assert.NoError(t, err, "vault_transit_ready probe should return nil for healthy vault")
}

// ---------------------------------------------------------------------------
// TC-INT-7: transit mount deleted → errcode.Is(err, ErrKeyProviderKeyNotFound)
//           or ErrKeyProviderTransient depending on classifyVaultReadError output
// ---------------------------------------------------------------------------

// TestTransitReadiness_MountDeleted verifies that after deleting the transit
// mount, Checkers["vault_transit_ready"]() returns an errcode-classified error.
// The error must be either ErrKeyProviderKeyNotFound (permanent 404/403) or
// ErrKeyProviderTransient (transient).
//
// ref: hashicorp/vault builtin/logical/transit — mount deletion causes 404
// ref: external-secrets/external-secrets — business-path probe detects missing mount
func TestTransitReadiness_MountDeleted(t *testing.T) {
	ctx := context.Background()
	addr, token, teardown := startVaultContainer(t)
	defer teardown()

	p := newProviderFromEnv(t, addr, token)

	// Delete the transit mount via Vault API using the high-level Sys() client.
	cfg := vaultapi.DefaultConfig()
	cfg.Address = addr
	rawClient, err := vaultapi.NewClient(cfg)
	require.NoError(t, err)
	rawClient.SetToken(token)

	err = rawClient.Sys().UnmountWithContext(ctx, "transit")
	require.NoError(t, err, "Unmount transit must succeed (dev root token has sys capability)")

	checkers := p.Checkers()
	probe := checkers["vault_transit_ready"]
	require.NotNil(t, probe)

	probeErr := probe()
	require.Error(t, probeErr, "vault_transit_ready must return error when transit mount is deleted")

	// classifyVaultReadError routes 404/403 → ErrKeyProviderKeyNotFound.
	// Accept either KeyNotFound or Transient (depending on Vault response to missing mount).
	isKeyNotFound := isErrCode(probeErr, errcode.ErrKeyProviderKeyNotFound)
	isTransient := isErrCode(probeErr, errcode.ErrKeyProviderTransient)
	assert.True(t, isKeyNotFound || isTransient,
		"error must be ErrKeyProviderKeyNotFound or ErrKeyProviderTransient, got: %v", probeErr)
}

// ---------------------------------------------------------------------------
// TC-INT-8: context 3 seconds timeout → errcode.Is(err, ErrKeyProviderTransient)
// ---------------------------------------------------------------------------

// TestTransitReadiness_ContextTimeout verifies that a context that times out
// causes the probe to return ErrKeyProviderTransient (network error is transient).
//
// We simulate timeout by using a provider pointed at an unreachable address.
//
// ref: isTransientVaultError — pure network errors are classified as transient
func TestTransitReadiness_ContextTimeout(t *testing.T) {
	// Point the provider at a non-routable address to guarantee timeout.
	// Use TEST-NET-1 (192.0.2.0/24, RFC 5737) which is guaranteed to black-hole.
	unreachableAddr := "http://192.0.2.1:8200"

	cfg := vaultapi.DefaultConfig()
	cfg.Address = unreachableAddr
	// Set a very short timeout so the test doesn't wait 3 full seconds.
	cfg.HttpClient = &http.Client{Timeout: 500 * time.Millisecond}
	rawClient, err := vaultapi.NewClient(cfg)
	require.NoError(t, err)
	rawClient.SetToken("any-token")

	client := vaultadapter.NewVaultAPIClient(rawClient)
	p := vaultadapter.NewTransitKeyProvider(client, "transit", "gocell-config")

	checkers := p.Checkers()
	probe := checkers["vault_transit_ready"]
	require.NotNil(t, probe)

	// The probe internally uses context.WithTimeout(3s), but since we also set
	// a 500ms HTTP client timeout, it will fail fast.
	probeErr := probe()
	require.Error(t, probeErr, "probe must return error when vault is unreachable")

	assert.True(t, isErrCode(probeErr, errcode.ErrKeyProviderTransient),
		"unreachable vault must be classified as ErrKeyProviderTransient, got: %v", probeErr)
}

// ---------------------------------------------------------------------------
// TC-INT-9: 短命子 token + revoke-accessor 吊销 → errcode.Is(...)
// ---------------------------------------------------------------------------

// TestTransitReadiness_RevokedToken verifies that after revoking the token
// used by the provider, the readiness probe returns a classified errcode error.
//
// Implementation notes:
//   - Dev root token cannot be revoked (Vault dev mode restriction).
//   - We create a short-lived child token via auth/token/create (ttl=60s),
//     build a provider with that child token, then revoke it via
//     auth/token/revoke-accessor (using the root token to do the revocation).
//   - After revocation, Vault returns 403, which classifyVaultReadError routes
//     to ErrKeyProviderKeyNotFound (permanent 4xx).
//
// ref: testcontainers-go modules/vault — dev root token cannot be revoked
// ref: hashicorp/vault api/auth/token — create child token + revoke-accessor
func TestTransitReadiness_RevokedToken(t *testing.T) {
	ctx := context.Background()
	addr, token, teardown := startVaultContainer(t)
	defer teardown()

	// Build a root client to create and revoke child tokens.
	cfg := vaultapi.DefaultConfig()
	cfg.Address = addr
	rootClient, err := vaultapi.NewClient(cfg)
	require.NoError(t, err)
	rootClient.SetToken(token)

	// Create a short-lived child token with a policy that allows transit reads.
	// Use the high-level Auth().Token().CreateWithContext API instead of
	// RawRequestWithContext (which is deprecated in vault/api v1.16+).
	renewable := false
	secret, err := rootClient.Auth().Token().CreateWithContext(ctx, &vaultapi.TokenCreateRequest{
		TTL:       "60s",
		Renewable: &renewable,
		Policies:  []string{"default"},
		NoParent:  false,
	})
	require.NoError(t, err, "create child token must succeed")
	require.NotNil(t, secret, "create token response must not be nil")
	require.NotNil(t, secret.Auth, "create token response must have 'auth' field")

	childToken := secret.Auth.ClientToken
	require.NotEmpty(t, childToken, "child token must not be empty")
	accessor := secret.Auth.Accessor
	require.NotEmpty(t, accessor, "accessor must not be empty")

	// Build a provider using the child token.
	childCfg := vaultapi.DefaultConfig()
	childCfg.Address = addr
	childClient, err := vaultapi.NewClient(childCfg)
	require.NoError(t, err)
	childClient.SetToken(childToken)

	childVaultClient := vaultadapter.NewVaultAPIClient(childClient)
	p := vaultadapter.NewTransitKeyProvider(childVaultClient, "transit", "gocell-config")

	// Verify the probe works before revocation.
	checkers := p.Checkers()
	probe := checkers["vault_transit_ready"]
	require.NotNil(t, probe)
	require.NoError(t, probe(), "probe must succeed with valid child token")

	// Revoke the child token via revoke-accessor using the high-level API.
	// Root token can revoke any accessor without knowing the child token value.
	err = rootClient.Auth().Token().RevokeAccessorWithContext(ctx, accessor)
	require.NoError(t, err, "revoke-accessor must succeed using root token")

	// After revocation, probe must return a classified error.
	probeErr := probe()
	require.Error(t, probeErr, "probe must fail after token is revoked")

	isKeyNotFound := isErrCode(probeErr, errcode.ErrKeyProviderKeyNotFound)
	isTransient := isErrCode(probeErr, errcode.ErrKeyProviderTransient)
	assert.True(t, isKeyNotFound || isTransient,
		"revoked token must return ErrKeyProviderKeyNotFound or ErrKeyProviderTransient, got: %v", probeErr)
}

// ---------------------------------------------------------------------------
// TC-INT-10: /readyz HTTP integration
// Skipped with explanation: current bootstrap wiring does not expose a
// standalone /readyz HTTP server in unit/integration test context.
// Checkers() return value is verified directly in TC-INT-6 to TC-INT-9.
// ---------------------------------------------------------------------------

// TestTransitReadiness_ReadyzHTTPIntegration is intentionally skipped.
//
// The /readyz HTTP endpoint is assembled at the bootstrap layer
// (runtime/bootstrap), which is out of scope for adapters/vault integration
// tests. The readiness probe function itself is verified by TC-INT-6 to
// TC-INT-9 at the Checkers() level. A full bootstrap-layer /readyz HTTP test
// requires the composition root and is tracked as a separate journey test
// (see journeys/J-readiness.yaml).
func TestTransitReadiness_ReadyzHTTPIntegration(t *testing.T) {
	t.Skipf("TC-INT-10: /readyz HTTP integration requires bootstrap composition root; " +
		"Checkers() readiness probe is covered by TC-INT-6 to TC-INT-9. " +
		"See journeys/J-readiness.yaml for full HTTP integration tracking.")
	fmt.Println("skipped")
}
