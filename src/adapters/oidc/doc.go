// Package oidc provides an OpenID Connect adapter for the GoCell framework.
//
// It implements the authentication interfaces defined in runtime/auth,
// providing OIDC discovery, authorization code flow, token exchange,
// JWT validation, and refresh token handling.
//
// # Configuration
//
//	IssuerURL:     "https://idp.example.com/realms/gocell"
//	ClientID:      "gocell-app"
//	ClientSecret:  "<secret>"
//	RedirectURL:   "https://app.example.com/callback"
//	Scopes:        ["openid", "profile", "email"]
//
// # Token Validation
//
// The adapter fetches the JWKS from the OIDC discovery endpoint and caches
// the signing keys. Tokens are validated for signature, issuer, audience,
// and expiry. Clock skew tolerance is configurable.
//
// # Close
//
// Always call Close to release HTTP client resources.
package oidc
