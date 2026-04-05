// Package oidc provides an OpenID Connect adapter for the GoCell framework.
// It implements OIDC Discovery, token exchange, JWKS verification, and
// UserInfo endpoint access without external OIDC libraries.
//
// Key features:
//   - Automatic discovery via .well-known/openid-configuration
//   - JWKS key rotation with kid-based lookup
//   - RS256 token signature verification
//   - Authorization code exchange
//   - UserInfo endpoint access
//
// ref: coreos/go-oidc — discovery, JWKS caching, token verification
// Adopted: discovery metadata model, kid-based key selection.
// Deviated: no external dependencies; stdlib crypto/rsa + encoding/json only.
package oidc
