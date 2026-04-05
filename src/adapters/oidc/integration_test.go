//go:build integration

// Package oidc provides the OpenID Connect adapter for GoCell.
// Integration tests require a running OIDC provider (e.g. Keycloak).
package oidc

import "testing"

// TestIntegration_DiscoverEndpoints verifies OIDC discovery from /.well-known/openid-configuration.
func TestIntegration_DiscoverEndpoints(t *testing.T) {
	t.Skip("stub: requires running OIDC provider")
}

// TestIntegration_TokenExchange verifies the authorization code -> token exchange flow.
func TestIntegration_TokenExchange(t *testing.T) {
	t.Skip("stub: requires running OIDC provider")
}

// TestIntegration_ValidateToken verifies JWT signature and claims validation.
func TestIntegration_ValidateToken(t *testing.T) {
	t.Skip("stub: requires running OIDC provider")
}

// TestIntegration_RefreshToken verifies the refresh token grant flow.
func TestIntegration_RefreshToken(t *testing.T) {
	t.Skip("stub: requires running OIDC provider")
}

// TestIntegration_Close verifies graceful shutdown of the OIDC client.
func TestIntegration_Close(t *testing.T) {
	t.Skip("stub: requires running OIDC provider")
}
