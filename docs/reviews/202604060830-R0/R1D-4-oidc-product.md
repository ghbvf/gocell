# R1D-4: adapters/oidc Product Review

| Field | Value |
|-------|-------|
| Reviewer role | Product Manager |
| Module | `adapters/oidc` |
| Source PRs | #14 (introduced), #28 (integration test stubs), #29 (docs), #31 (integration branch) |
| Review baseline | `5096d4f` |
| Date | 2026-04-06 |

---

## Executive Summary

For a Go developer, the package is easy to discover and easy to instantiate. The problem is semantic completeness. The adapter can fetch discovery, exchange a code, verify a token, and call userinfo, but several successful return paths do not guarantee the identity semantics that upper layers need for a real login flow.

Overall verdict: **2 P1, 1 P2**.

---

## Findings

### P-01 | P1 | Successful verification does not guarantee a stable principal identity

- **Files**: `src/adapters/oidc/verifier.go:117-123`, `src/adapters/oidc/userinfo.go:66-72`
- **Issue**: `Verify()` returns success even when `sub` is missing, because it never validates a non-empty subject. `GetUserInfo()` also returns success without checking `info.Subject`.
- **Business impact**: The upper layer can receive a "verified" login result that has no durable user identity. For access/session code, that is not a soft edge case; it is a broken success contract.
- **Fix**: Reject empty `sub` in both `Verify()` and `GetUserInfo()`. Make the adapter guarantee "success means a usable principal identifier exists."

### P-02 | P1 | The returned audience model is lossy for valid multi-audience tokens

- **Files**: `src/adapters/oidc/verifier.go:35-45`, `src/adapters/oidc/verifier.go:125-126`, `src/adapters/oidc/verifier.go:138-152`
- **Issue**: `audienceMatch()` correctly accepts `aud` as either a string or an array. But `IDTokenClaims.Audience` is a single string, and the result-population path uses `claimString(claims, "aud")`. When `aud` is an array, verification succeeds but `result.Audience` is left empty.
- **Business impact**: Callers lose the verified audience information for a standards-compliant token shape. That makes downstream auditing, debugging, and policy checks unreliable.
- **Fix**: Replace `Audience string` with `Audiences []string`, or at minimum normalize the `aud` claim into a stable returned representation.

### P-03 | P2 | ExchangeCode reports syntactic success instead of protocol success

- **Files**: `src/adapters/oidc/token.go:66-83`
- **Issue**: `ExchangeCode()` treats any HTTP 200 + JSON parse as success. It does not validate `token_type`, `access_token`, or the presence of `id_token` for OIDC login use cases.
- **Business impact**: Every consumer has to re-implement the same required-field validation, which invites inconsistent behavior across call sites.
- **Fix**: Validate the returned token payload before returning success. At minimum, require non-empty `access_token` and `token_type`, and document whether `id_token` is mandatory for the intended use case.

---

## Product Notes

- The package is not yet wired into `access-core`; that keeps blast radius low today, but it also means these semantic gaps will surface later unless fixed now.
- The package doc is discoverable, but there is still no runnable example of a full OIDC login flow in the repository.
