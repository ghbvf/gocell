# R1D-4: adapters/oidc Architecture Review

| Field | Value |
|-------|-------|
| Reviewer seat | S1 Architecture |
| Scope | `src/adapters/oidc/` -- 5 production files, 2 test files, 1 package doc |
| Review baseline | `5096d4f` (current develop/review HEAD) |
| Date | 2026-04-06 |

---

## Summary

The package is intentionally small and layer-clean: `adapters/oidc` depends only on stdlib, `pkg/errcode`, and `golang-jwt`. The discovery, token exchange, JWKS fetch, and userinfo flows are easy to read, and the public surface is compact. The main architectural problems are not size or complexity; they are contract drift and missing extension points at the module boundary.

Overall verdict: **2 P1, 1 P2**.

---

## Findings

### A-01 | P1 | JWKS cache architecture is internally inconsistent

- **Files**: `src/adapters/oidc/config.go:21-24`, `src/adapters/oidc/verifier.go:51-55`, `src/adapters/oidc/verifier.go:156-176`, `src/adapters/oidc/verifier.go:226-230`
- **Issue**: `Config` exposes `JWKSCacheTTL`, and `Verifier` records `fetchAt`, but the cache lookup path never consults either. `getKey()` returns cached keys indefinitely when `kid` is already present, and only refetches JWKS on a cache miss.
- **Impact**: The adapter advertises a bounded JWKS cache but actually implements an unbounded cache for existing `kid` values. Operators cannot reason about key freshness, revocation, or rotation latency from the exported config.
- **Fix**: Check `time.Since(fetchAt)` against `config.JWKSCacheTTL` before returning a cached key. If expired, refetch JWKS even when `kid` is already in `keyCache`. If indefinite caching is intentional, remove `JWKSCacheTTL` and `fetchAt` from the public/API surface.

### A-02 | P1 | No HTTP transport injection point for enterprise IdP integration

- **Files**: `src/adapters/oidc/provider.go:29-31`, `src/adapters/oidc/provider.go:44-52`
- **Issue**: `NewProvider` always constructs its own `http.Client` and only exposes a timeout knob. There is no way to supply a custom `http.Client`, `RoundTripper`, root CA bundle, mTLS cert, proxy configuration, or tracing transport.
- **Impact**: The adapter cannot be cleanly integrated with enterprise OIDC deployments that require custom trust roots or controlled outbound routing. It also cannot participate in runtime-wide HTTP instrumentation without forking the package.
- **Fix**: Add an explicit `HTTPClient *http.Client` field or an option such as `WithHTTPClient(*http.Client)`. Preserve `HTTPTimeout` only as the default for the internally constructed client.

### A-03 | P2 | Public config implies a fuller auth-code client than the package actually provides

- **Files**: `src/adapters/oidc/config.go:17-20`, `src/adapters/oidc/provider.go:20-25`, `src/adapters/oidc/token.go:38-44`, `src/adapters/oidc/doc.go:5-10`
- **Issue**: `Config` includes `RedirectURL` and `Scopes`, and discovery stores `AuthorizationEndpoint`, but the package has no API for building the authorization URL, no state/nonce helpers, and no PKCE support. `Scopes` is never read anywhere in the package.
- **Impact**: The boundary is misleading. A caller reading the exported config can reasonably assume the adapter owns the full authorization-code flow, while in reality it only covers the back-channel half.
- **Fix**: Either narrow the public surface to what the package actually implements, or add an explicit front-channel helper API and document what the adapter does not own.

---

## Positive Notes

- Dependency direction is clean: no imports from `runtime/`, `cells/`, or other adapters.
- The package keeps protocol logic isolated from business logic, which makes later access-core integration easier.
- The core flows are small enough that architectural fixes are localized rather than structural rewrites.
