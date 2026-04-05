//go:build integration

package oidc

import (
	"testing"
)

// TestIntegration_DiscoverProvider connects to a real OIDC provider and
// verifies discovery document parsing.
func TestIntegration_DiscoverProvider(t *testing.T) {
	t.Skip("stub: requires OIDC provider (docker compose up keycloak)")
}

// TestIntegration_TokenExchange performs a full authorization-code
// exchange against a real provider.
func TestIntegration_TokenExchange(t *testing.T) {
	t.Skip("stub: requires OIDC provider (docker compose up keycloak)")
}

// TestIntegration_VerifyIDToken validates a real ID token from the
// provider, including signature and claims.
func TestIntegration_VerifyIDToken(t *testing.T) {
	t.Skip("stub: requires OIDC provider (docker compose up keycloak)")
}

// TestIntegration_UserInfo calls the userinfo endpoint and asserts the
// returned claims match the authenticated user.
func TestIntegration_UserInfo(t *testing.T) {
	t.Skip("stub: requires OIDC provider (docker compose up keycloak)")
}
