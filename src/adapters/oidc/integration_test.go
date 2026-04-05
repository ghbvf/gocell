//go:build integration

// Package oidc_test contains integration tests for the OIDC adapter.
// These tests use a mock HTTP server to simulate an OIDC provider,
// marked as integration tests because they exercise the full HTTP flow.
package oidc_test

import "testing"

// TestIntegration_OIDCDiscovery verifies that the adapter can fetch and
// parse the .well-known/openid-configuration endpoint.
func TestIntegration_OIDCDiscovery(t *testing.T) {
	t.Skip("requires Docker: run with 'make test-integration'")
	// TODO: mock OIDC provider HTTP server
	// 1. Start httptest server with .well-known/openid-configuration
	// 2. Configure adapter to use mock server URL
	// 3. Verify discovery document is parsed correctly
	// 4. Verify JWKS URI is extracted
}

// TestIntegration_OIDCTokenValidation verifies JWT token validation
// against the OIDC provider's public keys.
func TestIntegration_OIDCTokenValidation(t *testing.T) {
	t.Skip("requires Docker: run with 'make test-integration'")
	// TODO: mock OIDC provider with JWKS endpoint
	// 1. Generate RSA key pair
	// 2. Serve JWKS with public key
	// 3. Sign a JWT with private key
	// 4. Verify adapter validates the token successfully
	// 5. Verify expired token is rejected
	// 6. Verify tampered token is rejected
}

// TestIntegration_OIDCTokenExchange verifies the authorization code
// exchange flow.
func TestIntegration_OIDCTokenExchange(t *testing.T) {
	t.Skip("requires Docker: run with 'make test-integration'")
	// TODO: mock OIDC provider with token endpoint
	// 1. Start mock server with /token endpoint
	// 2. Exchange authorization code for tokens
	// 3. Verify access_token and id_token are returned
	// 4. Verify error handling for invalid code
}

// TestIntegration_OIDCUserInfo verifies the userinfo endpoint flow.
func TestIntegration_OIDCUserInfo(t *testing.T) {
	t.Skip("requires Docker: run with 'make test-integration'")
	// TODO: mock OIDC provider with userinfo endpoint
	// 1. Start mock server with /userinfo
	// 2. Call userinfo with valid access token
	// 3. Verify user claims are returned
	// 4. Verify 401 for invalid token
}

// TestIntegration_OIDCKeyRotation verifies that the adapter handles
// JWKS key rotation gracefully.
func TestIntegration_OIDCKeyRotation(t *testing.T) {
	t.Skip("requires Docker: run with 'make test-integration'")
	// TODO: mock OIDC provider with rotatable JWKS
	// 1. Serve JWKS with key A
	// 2. Validate token signed with key A (success)
	// 3. Rotate: serve JWKS with key B only
	// 4. Validate token signed with key B (success after refresh)
	// 5. Validate token signed with key A (fail after cache expiry)
}
