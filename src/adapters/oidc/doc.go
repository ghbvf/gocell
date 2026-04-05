// Package oidc implements an OpenID Connect adapter for the GoCell framework.
//
// It provides OIDC Discovery (/.well-known/openid-configuration), Authorization
// Code token exchange, JWKS-based ID token verification with kid rotation and
// RS256 signature validation, and UserInfo endpoint access.
//
// The adapter implements runtime/auth.TokenVerifier for ID token verification
// and can be wired into the GoCell bootstrap lifecycle.
//
// No external OIDC libraries are used; the implementation relies on net/http,
// encoding/json, crypto/rsa, and math/big from the standard library.
//
// ref: coreos/go-oidc — Discovery + JWKS verification pattern
// Adopted: Provider with Discovery metadata caching, remote key set with kid-based
// lookup and refresh-on-miss.
// Deviated: no dependency on golang.org/x/oauth2; token exchange implemented
// directly via net/http; Config is a simple struct rather than oauth2.Config.
package oidc
