// Package config is the production wiring entrypoint for runtime/auth
// components. Callers MUST use NewJWTIssuerFromRegistry /
// NewJWTVerifierFromRegistry to obtain issuer/verifier instances; direct
// calls to auth.NewJWTIssuer / auth.NewJWTVerifier are reserved for
// tests and future internal wiring.
//
// The Registry struct is the single source of truth for (Issuer,
// Audiences, KeySet, Clock) — established in F1 per
// docs/plans/202604191515-auth-federated-whistle.md.
//
// ref: Hydra internal/driver/config.DefaultProvider — single Registry pattern
// ref: Kratos middleware/auth/jwt WithParserOptions — one-time injection
package config
