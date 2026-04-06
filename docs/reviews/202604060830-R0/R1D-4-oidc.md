# R1D-4: adapters/oidc Review Report

- **Reviewer agent**: R1D-4 (`adapters/oidc`, six-seat composite)
- **Review baseline commit**: `5096d4f` (workspace HEAD on 2026-04-06)
- **Review date**: 2026-04-06
- **Seats exercised**: S1 Architecture, S2 Security, S3 Test, S4 DevOps/Operations, S5 DX/Maintainability, S6 Product/Semantics

---

## Review Scope

| Package | Source files | LOC (source) | Test files | LOC (test) |
|---------|-------------|--------------|------------|------------|
| `adapters/oidc` | `config.go`, `doc.go`, `errors.go`, `provider.go`, `token.go`, `userinfo.go`, `verifier.go` | 646 | `oidc_test.go`, `integration_test.go` | 386 |

### Verification run

- `/opt/homebrew/bin/go test ./adapters/oidc/...` from `src/` -> PASS
- `/opt/homebrew/bin/go test -cover ./adapters/oidc/...` from `src/` -> PASS, **74.0%** statement coverage

### Boundary observations

- No production package currently imports `github.com/ghbvf/gocell/adapters/oidc`; the adapter is still effectively unintegrated and will need follow-up validation in **R2-A2 Auth 全链路 review**.
- The package is intentionally stdlib-first and keeps its dependency surface small (`pkg/errcode` + `golang-jwt/jwt/v5` + stdlib), which is a good baseline.

---

## Summary

The module is small and readable, and the happy-path unit tests pass. RS256 pinning is present, basic discovery/JWKS/token/userinfo flows are covered, and the package respects the repository layering rules.

The main problems are at the **trust boundary**: JWKS refresh is advertised but not actually enforced, discovery metadata is trusted too broadly, and ID token validation stops short of the OIDC-required claim checks. These are not cosmetic issues; they directly affect whether the adapter can safely consume external identity-provider data.

**Overall risk: MEDIUM-HIGH.** No module-internal concurrency bug was found, but there are **4 P1 findings** that should be resolved before treating this adapter as production-ready for SSO login.

---

## Findings

### R1D4-F01 | P1 | JWKS cache never expires, so removed signing keys stay trusted indefinitely

- **Seat**: S2 Security + S4 Operations
- **File**: `src/adapters/oidc/config.go:21-22`, `src/adapters/oidc/verifier.go:156-176`, `src/adapters/oidc/verifier.go:226-230`
- **Status**: OPEN

`Config` exposes `JWKSCacheTTL`, and `fetchJWKS()` stores `fetchAt`, but `getKey()` returns a cached key immediately whenever `kid` exists in `keyCache`. There is no TTL check on the cached path, and `fetchAt` is never read anywhere. In practice this means key refresh happens only on cache miss, not on time-based expiry.

Impact:

- If the provider removes or revokes a signing key, this process will continue trusting the cached public key until restart.
- A compromised old key can keep validating freshly minted attacker tokens as long as the `kid` is still present in the local cache.
- The package promise in `docs/design/capability-inventory.md` ("JWKS + kid rotation") is not actually met.

Suggested fix:

- In `getKey()`, check `time.Since(fetchAt)` against `JWKSCacheTTL` before returning a cached key.
- On TTL expiry, refresh JWKS first, rebuild `keyCache`, then re-check the requested `kid`.
- Add a rotation test that proves a removed key stops validating without requiring process restart.

---

### R1D4-F02 | P1 | Discovery metadata is trusted without validating issuer or endpoint origin

- **Seat**: S2 Security
- **File**: `src/adapters/oidc/provider.go:102-123`, `src/adapters/oidc/token.go:38-57`, `src/adapters/oidc/userinfo.go:36-46`, `src/adapters/oidc/verifier.go:181-211`
- **Status**: OPEN

`fetchDiscovery()` only checks that `doc.Issuer` is non-empty before caching the document. It does not verify `doc.Issuer == config.IssuerURL`, and it does not constrain `token_endpoint`, `userinfo_endpoint`, or `jwks_uri` to the issuer origin. The rest of the package then trusts those URLs verbatim:

- `ExchangeCode()` posts `client_secret` to `doc.TokenEndpoint`
- `GetUserInfo()` calls `doc.UserinfoEndpoint`
- `fetchJWKS()` downloads keys from `doc.JWKSURI`

Impact:

- A poisoned discovery document can pivot the adapter to attacker-controlled endpoints.
- `client_secret` can be exfiltrated during code exchange.
- Discovery becomes an SSRF pivot into arbitrary internal or metadata endpoints if the issuer host is compromised or misconfigured.

Suggested fix:

- Reject discovery documents whose `issuer` does not exactly match the configured issuer.
- Parse the returned endpoints and require HTTPS plus same-host/same-origin semantics, or an explicit allowlist if cross-host deployments must be supported.
- Consider rejecting unsafe redirects for secret-bearing requests.

---

### R1D4-F03 | P1 | ID token validation enforces `iss`/`aud` only; required claims like `exp`, `iat`, and `sub` remain optional

- **Seat**: S2 Security + S6 Product
- **File**: `src/adapters/oidc/verifier.go:67-135`
- **Status**: OPEN

`Verify()` calls `jwt.ParseWithClaims(rawIDToken, claims, ...)` and then manually checks only:

- signing algorithm is `RS256`
- issuer matches `config.IssuerURL`
- audience contains `config.ClientID`

But the function never rejects missing `sub`, and it only copies `exp` / `iat` into the result if they happen to exist:

```go
if exp, ok := claims["exp"].(float64); ok {
    result.ExpiresAt = int64(exp)
}
if iat, ok := claims["iat"].(float64); ok {
    result.IssuedAt = int64(iat)
}
```

The result is that a token can pass verification with:

- no `exp` claim
- no `iat` claim
- an empty `sub`

as long as the signature, `iss`, and `aud` pass. That violates the package's advertised `exp/iss/aud` validation semantics and weakens the external input boundary.

Suggested fix:

- Construct a parser with explicit claim requirements instead of relying on the default parser behavior.
- Fail closed if `sub == ""`.
- Add `azp` validation when `aud` contains multiple values.
- Add unit tests for: missing `exp`, missing `iat`, missing `sub`, and multi-audience tokens with invalid `azp`.

---

### R1D4-F04 | P1 | Multi-audience tokens validate correctly but the returned claims silently lose the audience data

- **Seat**: S1 Architecture + S6 Product
- **File**: `src/adapters/oidc/verifier.go:117-127`, `src/adapters/oidc/verifier.go:138-152`
- **Status**: OPEN

`audienceMatch()` correctly accepts both string and array forms of the `aud` claim. However, the exported result type stores `Audience` as a single string, and the assignment path uses `claimString(claims, "aud")`:

```go
if aud := claimString(claims, "aud"); aud != "" {
    result.Audience = aud
}
```

When the token carries `aud` as an array, verification succeeds, but `result.Audience` is returned as the zero value (`""`). The adapter therefore loses verified claim data on the success path.

Impact:

- Callers cannot reliably inspect the validated audience set.
- Logging/auditing based on `IDTokenClaims.Audience` becomes misleading for valid multi-audience tokens.
- The exported API does not faithfully represent what the verifier already accepts.

Suggested fix:

- Change `IDTokenClaims.Audience` to `[]string`, or preserve both the raw and normalized forms.
- Reuse one parser/normalizer for both validation and result projection so accepted inputs are never silently degraded.

---

### R1D4-F05 | P2 | Config contract and documentation drift at the external API boundary

- **Seat**: S5 DX + S1 Architecture
- **File**: `src/adapters/oidc/config.go:43-51`, `docs/guides/adapter-config-reference.md:82-90`
- **Status**: OPEN

The documented adapter contract and the code contract have drifted:

- Docs mark `clientSecret` and `redirectURL` as required, but `Config.Validate()` checks only `issuerURL` and `clientID`.
- Docs advertise `discoveryTimeout` and `jwksRefreshInterval`; the code instead exposes `HTTPTimeout`, `DiscoveryCacheTTL`, and `JWKSCacheTTL`.
- `JWKSCacheTTL` is currently dead configuration (see F01), which makes the drift user-visible rather than theoretical.

Impact:

- Misconfiguration is detected late, after remote calls have started.
- Users configuring the adapter from docs cannot infer the real behavior of timeout vs cache vs refresh.
- The package boundary is harder to integrate safely because the public contract is ambiguous.

Suggested fix:

- Align the docs and code to one vocabulary.
- Either enforce `RedirectURL` / `ClientSecret` in `Validate()`, or explicitly document supported public-client / PKCE scenarios.
- Remove or implement configuration knobs that are currently no-ops.

---

### R1D4-F06 | P2 | Test coverage misses the exact boundary cases where this adapter is most likely to fail in production

- **Seat**: S3 Test + S4 Operations
- **File**: `src/adapters/oidc/oidc_test.go:160-355`, `src/adapters/oidc/integration_test.go:9-30`
- **Status**: OPEN

The unit suite covers the happy path, wrong audience, expired token, and basic userinfo calls. That is enough to keep the package compiling, but not enough to defend the real boundary risks identified above:

- no JWKS rotation / key revocation test
- no discovery issuer mismatch test
- no missing `exp` / `iat` / `sub` test
- no multi-audience projection test
- no non-RS256 rejection test in this package
- all integration tests are still `t.Skip(...)`

With 74.0% coverage, the missing 26% is concentrated in the most failure-prone code paths rather than in harmless glue.

Suggested fix:

- Add table-driven negative tests for every trust-boundary rule the adapter claims to enforce.
- Replace the OIDC integration stubs with a real Keycloak-backed or mock-provider-backed flow covering discovery, exchange, verification, and userinfo end to end.

---

## Positive confirmations

- `RS256` pinning is present in `src/adapters/oidc/verifier.go:89-94`; the verifier does not accept arbitrary signing methods.
- The package respects repository dependency direction: it imports only `pkg/errcode`, `golang-jwt/jwt/v5`, and stdlib.
- The unit tests are stable in the current environment once run from the module root (`src/`).

---

## Recommended next actions

1. Fix F01-F03 first. Those are the core trust-boundary defects.
2. Fix F04 together with the caller-facing API shape, because it changes the exported claim model.
3. Align config/docs and add the missing negative/integration tests before wiring `adapters/oidc` into the auth flow.
