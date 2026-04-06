# R1D-4: adapters/oidc Security Review

| Field | Value |
|-------|-------|
| Reviewer seat | S2 Security |
| Scope | `src/adapters/oidc/` all production files plus relevant tests |
| Review baseline | `5096d4f` (current develop/review HEAD) |
| Date | 2026-04-06 |

---

## Summary

The package correctly pins ID token verification to `RS256` and does not allow `HS256` or `none`. That hardening is good. The remaining security risks are at the protocol boundary: discovery metadata is trusted too broadly, required ID token claims are not fully enforced, and confidential-client secrets can be sent over plaintext HTTP without an explicit opt-in.

Overall verdict: **2 P1, 1 P2**.

---

## Findings

### S-01 | P1 | Discovery metadata issuer and endpoint trust are not validated

- **Files**: `src/adapters/oidc/provider.go:102-121`, `src/adapters/oidc/token.go:33-54`, `src/adapters/oidc/userinfo.go:31-43`, `src/adapters/oidc/verifier.go:181-197`
- **Issue**: `fetchDiscovery()` only checks that `doc.Issuer` is non-empty. It does not require `doc.Issuer == config.IssuerURL`, and it accepts `token_endpoint`, `userinfo_endpoint`, and `jwks_uri` exactly as returned by the discovery document.
- **Impact**: A mismatched or malicious discovery document can redirect token exchange, userinfo calls, and JWKS fetches to foreign endpoints. That can leak `client_secret` and shift signing-key trust away from the configured issuer.
- **Fix**: Reject discovery documents whose `issuer` does not exactly equal `config.IssuerURL`. Also validate endpoint schemes and, at minimum, require the returned endpoints to be consistent with the discovered issuer unless an explicit override is configured.

### S-02 | P1 | Verify accepts ID tokens that are signed correctly but miss required OIDC claims

- **Files**: `src/adapters/oidc/verifier.go:67-135`
- **Issue**: `Verify()` uses `jwt.ParseWithClaims(rawIDToken, jwt.MapClaims{}, ...)` without parser options such as `jwt.WithExpirationRequired()`. In `jwt/v5`, `exp` is optional by default unless explicitly required. The code later checks only `iss` and `aud`; it never rejects missing `sub`, and it accepts tokens with no `exp`.
- **Impact**: A signed token can be accepted even if it has no stable subject or no expiration. That breaks the minimum security contract expected from an OIDC ID token verifier and can enable replay of non-expiring tokens.
- **Fix**: Parse with `jwt.NewParser(jwt.WithExpirationRequired())` and explicitly reject empty `sub`. If `iat` and `nonce` are required by the intended flow, enforce those too instead of only passing them through.

### S-03 | P2 | Confidential-client traffic can use plaintext HTTP with no explicit opt-in

- **Files**: `src/adapters/oidc/config.go:11-26`, `src/adapters/oidc/provider.go:71-73`, `src/adapters/oidc/token.go:38-52`
- **Issue**: `Config.Validate()` allows any issuer URL scheme. `ExchangeCode()` always sends `client_secret`, but there is no HTTPS-by-default guard and no `AllowInsecureHTTP` style flag for local development.
- **Impact**: Misconfiguration can send client credentials to a plaintext token endpoint. Even if this is acceptable for loopback/test setups, the adapter currently makes insecure transport indistinguishable from production-safe transport.
- **Fix**: Require `https://` by default. If local development over `http://` is needed, gate it behind an explicit insecure-development flag and document the risk.

---

## Confirmed Positive Controls

- `Verify()` rejects non-`RS256` signing methods at `src/adapters/oidc/verifier.go:89-95`.
- The package does not log `client_secret` or token payloads directly.
- HTTP requests are context-aware and timeout-bounded when `HTTPTimeout` is configured sanely.
