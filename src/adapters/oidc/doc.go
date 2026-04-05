// Package oidc provides an OpenID Connect adapter for GoCell.
//
// This adapter implements the runtime/auth.TokenVerifier interface using OIDC
// discovery and JWKS-based JWT verification. It replaces the Phase 2 in-memory
// JWT verifier in access-core with a production-ready OIDC-compliant verifier
// supporting RS256, ES256, and other asymmetric algorithms.
//
// Configuration is done via OIDCConfig, which can be populated from environment
// variables using ConfigFromEnv().
//
// # Usage
//
//	cfg := oidc.ConfigFromEnv()
//	verifier, err := oidc.New(ctx, cfg)
//	if err != nil { ... }
//
//	// Use as runtime/auth.TokenVerifier
//	claims, err := verifier.Verify(ctx, token)
//
// # Environment Variables
//
// See docs/guides/adapter-config-reference.md for the full variable listing.
// Key variables: OIDC_ISSUER_URL, OIDC_CLIENT_ID, OIDC_CLIENT_SECRET,
// OIDC_AUDIENCE, OIDC_JWKS_REFRESH_INTERVAL.
//
// # Error Codes
//
// All errors use the ERR_ADAPTER_OIDC_* code family from pkg/errcode.
package oidc
