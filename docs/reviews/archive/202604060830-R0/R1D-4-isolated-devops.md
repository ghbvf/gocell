# Independent DevOps Review: `src/adapters/oidc`

Review date: 2026-04-06

Scope reviewed:
`/Users/shengming/Documents/code/gocell/src/adapters/oidc/config.go`,
`/Users/shengming/Documents/code/gocell/src/adapters/oidc/provider.go`,
`/Users/shengming/Documents/code/gocell/src/adapters/oidc/verifier.go`,
`/Users/shengming/Documents/code/gocell/src/adapters/oidc/token.go`,
`/Users/shengming/Documents/code/gocell/src/adapters/oidc/userinfo.go`,
`/Users/shengming/Documents/code/gocell/src/adapters/oidc/errors.go`,
`/Users/shengming/Documents/code/gocell/src/adapters/oidc/doc.go`,
`/Users/shengming/Documents/code/gocell/src/adapters/oidc/oidc_test.go`,
`/Users/shengming/Documents/code/gocell/src/adapters/oidc/integration_test.go`

Verification:
`go test ./adapters/oidc`

## Findings

1. `JWKSCacheTTL` is documented but not enforced, so key refresh semantics do not match the configuration surface.

`Config.JWKSCacheTTL` is defined in `config.go`, but the runtime never reads it. In `verifier.go`, cached keys are reused until a requested `kid` is missing, at which point the adapter fetches the JWKS again. That means a key that remains present in `keyCache` stays trusted indefinitely, even after the provider rotates or removes it. The `fetchAt` timestamp is also written but never consulted.

Operational impact: the adapter can keep accepting stale signing keys far longer than intended, and the configured TTL gives operators a false sense of control over cache freshness.

Evidence:
`/Users/shengming/Documents/code/gocell/src/adapters/oidc/config.go:21-24`,
`/Users/shengming/Documents/code/gocell/src/adapters/oidc/verifier.go:155-176`,
`/Users/shengming/Documents/code/gocell/src/adapters/oidc/verifier.go:226-230`

2. Refreshes can stampede the IdP because cache misses are not deduplicated.

Both `Provider.Discover()` and `Verifier.getKey()` release their locks before performing network I/O. Under concurrent login load, or during provider key rotation, multiple goroutines can observe the same cache miss and issue the same discovery/JWKS fetch in parallel. There is no singleflight or in-flight refresh guard.

Operational impact: a cold start or upstream wobble can fan out into many duplicate requests, increasing latency on the auth path and putting unnecessary pressure on the provider. With the current per-client timeout model, those concurrent requests can also tie up a lot of goroutines at once.

Evidence:
`/Users/shengming/Documents/code/gocell/src/adapters/oidc/provider.go:57-66`,
`/Users/shengming/Documents/code/gocell/src/adapters/oidc/verifier.go:155-176`

3. The config validation path misses a required deployment guardrail for authorization-code exchange.

`Config.Validate()` checks issuer and client ID, but `ExchangeCode()` always sends `redirect_uri` from `Config.RedirectURL`. A blank redirect URL therefore passes startup validation and only fails when the first auth-code exchange hits the token endpoint.

Operational impact: a misconfigured deployment can reach production and fail only at runtime, which is exactly the kind of failure that should be caught by config validation or readiness checks.

Evidence:
`/Users/shengming/Documents/code/gocell/src/adapters/oidc/config.go:43-51`,
`/Users/shengming/Documents/code/gocell/src/adapters/oidc/token.go:38-44`

4. Observability is too thin to support fast incident triage.

The adapter emits `Info` logs when discovery and JWKS fetches succeed, and `Warn` logs only for response-body close failures or JWKS parsing issues. There are no counters or structured signals for cache hits, cache misses, refresh latency, refresh failures, or stale-cache usage. In practice, that means an operator cannot tell from the adapter boundary whether auth failures are caused by a provider outage, a stale cache, or a local misconfiguration.

Operational impact: diagnosing provider-related incidents requires inference from downstream behavior instead of direct adapter telemetry.

Evidence:
`/Users/shengming/Documents/code/gocell/src/adapters/oidc/provider.go:118-121`,
`/Users/shengming/Documents/code/gocell/src/adapters/oidc/verifier.go:247-249`,
`/Users/shengming/Documents/code/gocell/src/adapters/oidc/token.go:59-63`,
`/Users/shengming/Documents/code/gocell/src/adapters/oidc/userinfo.go:48-52`

## Notes

The package-level tests pass, but the current test suite does not exercise cache expiry, key rotation, or concurrent refresh behavior against a real provider. The integration tests are still stubs, so the highest-risk operational paths remain unverified end to end.
