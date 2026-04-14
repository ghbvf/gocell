# Isolated Architecture Review: `adapters/oidc`

## Summary

`adapters/oidc` is cleanly split into discovery, token exchange, JWKS verification, and UserInfo access, and the unit tests cover the main happy paths plus a few negative cases. The package is close to production-ready structurally, but there are two security-relevant gaps and one configuration contract drift that should be fixed before treating it as a stable authentication adapter.

## Findings

### P1 - JWKS cache never expires, so stale signing keys can remain trusted indefinitely

**Evidence**

- `Config` exposes `JWKSCacheTTL` and documents a 1-hour default in `adapters/oidc/config.go:21-39`.
- `Verifier` stores `fetchAt` and the full JWKS, but `getKey()` only checks `keyCache` and never compares the current time against `JWKSCacheTTL` in `adapters/oidc/verifier.go:48-55, 155-176`.
- `fetchJWKS()` sets `v.fetchAt = time.Now()` and repopulates `keyCache`, but nothing reads `fetchAt` afterward in `adapters/oidc/verifier.go:226-230`.

**Impact**

Once a `kid` is cached, the adapter will continue accepting tokens signed by that key until the process restarts or the cache misses for another reason. That makes provider key rotation or emergency revocation much less effective than the API and docs imply, which is a serious authentication risk for a verifier.

**Suggestion**

Make cache hits age-aware. If `time.Since(fetchAt) >= JWKSCacheTTL`, force a refresh before trusting the cached key. If the TTL is intentionally unsupported, remove it from the public config surface and docs so callers do not assume key rotation is being enforced.

### P2 - Config validation accepts incomplete auth settings that are required later

**Evidence**

- `Config.Validate()` only enforces `IssuerURL` and `ClientID` in `adapters/oidc/config.go:45-50`.
- `ExchangeCode()` directly uses `ClientSecret` and `RedirectURL` in the token request body in `adapters/oidc/token.go:38-44`.
- The configuration guide marks `clientSecret` and `redirectURL` as required in `docs/guides/adapter-config-reference.md:84-90`.

**Impact**

A caller can construct a provider successfully and only discover the missing values when the first authorization-code exchange fails. That pushes a basic setup error into runtime and makes the adapter behave inconsistently with its own published configuration contract.

**Suggestion**

Validate `ClientSecret` and `RedirectURL` up front, unless the adapter is intentionally supporting a public-client mode. If that mode is intended, document it explicitly and make the request-building path branch accordingly.

### P2 - Discovery issuer is not checked against the configured issuer

**Evidence**

- `fetchDiscovery()` only rejects an empty `issuer` field and then caches the document in `adapters/oidc/provider.go:69-123`.
- There is no equality check between `doc.Issuer` and `Config.IssuerURL` after discovery.

**Impact**

The adapter will trust whatever endpoints appear in the discovery document as long as `issuer` is non-empty. That weakens the trust boundary around discovery and makes the adapter more tolerant of substituted or redirected metadata than an OIDC client should be.

**Suggestion**

Compare the discovered issuer with the configured issuer after normalizing trailing slashes, and fail discovery if they do not match.

## Positive Notes

- The code keeps discovery, token exchange, userinfo retrieval, and verification in separate files, which makes the trust boundaries easy to follow.
- Error handling is consistent: failures are wrapped with adapter-specific error codes, and the test suite exercises cache behavior, audience mismatch, expired tokens, and userinfo success/failure paths.

## Verification

- Ran `cd /Users/shengming/Documents/code/gocell/src && /opt/homebrew/bin/go test ./adapters/oidc/... -count=1`
- Result: passed
- Integration stubs in `adapters/oidc/integration_test.go` are `t.Skip(...)`, so verification here is limited to unit tests plus source/document review.
