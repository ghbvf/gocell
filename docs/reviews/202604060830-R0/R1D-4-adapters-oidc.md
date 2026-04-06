# R1D-4: adapters/oidc Six-Role Review

| Field | Value |
|---|---|
| Reviewer Seat | S1 Architecture + S2 Security + S3 Test + S4 Ops + S5 DX + S6 Product |
| Scope | `src/adapters/oidc/` -- 631 LOC prod, 386 LOC test |
| Review basis commit | `5096d4f` (current HEAD) |
| Date | 2026-04-06 |

---

## Summary

`adapters/oidc` is layer-clean and intentionally small: it depends only on stdlib, `pkg/errcode`, and `golang-jwt`. RS256 pinning is present in `Verifier.Verify`, and the unit test suite passes. However, the package still has several boundary-level gaps: discovery metadata is not fully validated before its endpoints are trusted, the advertised JWKS rotation contract is not implemented, and the exported claims model loses audience information for valid multi-audience tokens.

**Overall risk: MEDIUM.** No direct layering violations were found, but there are **3 P1 findings** at the external protocol boundary plus **3 P2 findings** around API contract quality and coverage depth.

---

## Inventory

| File | LOC | Notes |
|---|---:|---|
| `config.go` | 52 | config contract + defaults |
| `errors.go` | 19 | error code surface |
| `provider.go` | 124 | discovery + metadata cache |
| `token.go` | 84 | authorization-code exchange |
| `userinfo.go` | 73 | userinfo fetch |
| `verifier.go` | 279 | JWKS fetch + ID token verification |
| `oidc_test.go` | 355 | unit tests |
| `integration_test.go` | 31 | integration stubs only |

---

## Verification

- `cd src && /opt/homebrew/bin/go test ./adapters/oidc/...` -> PASS
- `cd src && /opt/homebrew/bin/go test -tags integration ./adapters/oidc/... -count=1 -v` -> PASS, but all 4 integration tests are `t.Skip(...)`
- `cd src && /opt/homebrew/bin/go test -cover ./adapters/oidc/...` -> `coverage: 74.0% of statements`

---

## Dependency Compliance

| Check | Result |
|---|---|
| Imports `runtime/`? | PASS |
| Imports `cells/`? | PASS |
| Imports other `adapters/`? | PASS |
| Depends only on stdlib + `pkg/errcode` + `golang-jwt` | PASS |
| In-tree non-test consumers found | NONE |

The lack of non-test importers means this review treats the exported API itself as the contract surface that must be safe and self-consistent.

---

## F-01: Discovery issuer mismatch is not rejected before trusting returned endpoints

| Field | Value |
|---|---|
| Seat | S2 Security + S6 Product |
| Severity | **P1** |
| Category | External boundary validation |
| Files | `src/adapters/oidc/provider.go:70-123`, `src/adapters/oidc/token.go:33-57`, `src/adapters/oidc/userinfo.go:31-46`, `src/adapters/oidc/verifier.go:181-201` |
| Status | OPEN |

**Evidence**

`fetchDiscovery` only validates that `doc.Issuer` is non-empty:

```go
// provider.go:108-110
if doc.Issuer == "" {
    return nil, errcode.New(ErrAdapterOIDCDiscovery,
        "oidc: discovery document missing issuer")
}
```

After that, the package trusts the discovery document's `TokenEndpoint`, `UserinfoEndpoint`, and `JWKSURI` directly:

```go
// token.go:33-57
if doc.TokenEndpoint == "" { ... }
resp, err := p.client.Do(req)

// userinfo.go:31-46
if doc.UserinfoEndpoint == "" { ... }
resp, err := p.client.Do(req)

// verifier.go:181-201
doc, err := v.provider.Discover(ctx)
req, err := http.NewRequestWithContext(ctx, http.MethodGet, doc.JWKSURI, nil)
```

**Why this is a problem**

The discovery document is external input at the trust boundary. Accepting any non-empty `issuer` means a mismatched or malicious discovery response can steer confidential follow-up requests to attacker-chosen endpoints. `Verifier.Verify` later compares token `iss` to `config.IssuerURL`, but that happens too late to protect `ExchangeCode` and `GetUserInfo`, both of which already consumed the discovery metadata.

**Recommendation**

Reject discovery metadata unless `doc.Issuer == cfg.IssuerURL` after normalization. For defence in depth, also reject non-HTTPS endpoints outside explicitly allowed localhost/dev modes and consider checking that discovery endpoints remain within the expected issuer trust domain.

---

## F-02: JWKS refresh interval is a dead contract; cached keys never expire

| Field | Value |
|---|---|
| Seat | S1 Architecture + S2 Security + S4 Ops |
| Severity | **P1** |
| Category | Cache semantics / key rotation |
| Files | `src/adapters/oidc/config.go:21-24`, `src/adapters/oidc/config.go:37-38`, `src/adapters/oidc/verifier.go:51-55`, `src/adapters/oidc/verifier.go:156-177`, `src/adapters/oidc/verifier.go:226-230`, `docs/design/capability-inventory.md:172-177` |
| Status | OPEN |

**Evidence**

The public config advertises a JWKS cache TTL:

```go
// config.go:21-24
// JWKSCacheTTL is how long to cache the JWKS keys. Default: 1 hour.
JWKSCacheTTL time.Duration
```

The verifier records fetch time:

```go
// verifier.go:226-230
v.jwks = &jwks
v.keyCache = make(map[string]*rsa.PublicKey, len(jwks.Keys))
v.fetchAt = time.Now()
```

But cache hits never consult either `JWKSCacheTTL` or `fetchAt`:

```go
// verifier.go:156-160
if key, ok := v.keyCache[kid]; ok {
    v.mu.RUnlock()
    return key, nil
}
```

The design inventory explicitly promises `Verifier — JWKS + kid rotation + RS256 验证 + exp/iss/aud`.

**Why this is a problem**

For a previously seen `kid`, the process trusts the cached RSA key forever. That means revoked or rotated keys are not observed until restart, and the exported config/documentation promise of JWKS refresh is false. This is both a security hardening gap and an interface-semantics bug.

**Recommendation**

Honor `JWKSCacheTTL` on the read path. On cache hit, refresh JWKS when the TTL has expired; on miss, refresh immediately. Consider a forced refresh on signature failure as well, so key rollover does not require a process restart.

---

## F-03: Multi-audience tokens validate successfully but return lossy claims to callers

| Field | Value |
|---|---|
| Seat | S1 Architecture + S6 Product |
| Severity | **P1** |
| Category | Exported API semantics |
| Files | `src/adapters/oidc/verifier.go:35-45`, `src/adapters/oidc/verifier.go:125-127`, `src/adapters/oidc/verifier.go:138-152`, `src/runtime/auth/auth.go:16-23` |
| Status | OPEN |

**Evidence**

`audienceMatch` accepts both string and array audiences:

```go
// verifier.go:142-150
switch aud := claims["aud"].(type) {
case string:
    return aud == clientID
case []any:
    for _, a := range aud {
        if s, ok := a.(string); ok && s == clientID {
            return true
        }
    }
}
```

But the exported result type only exposes a single string audience:

```go
// verifier.go:35-45
type IDTokenClaims struct {
    ...
    Audience  string `json:"aud"`
}

// verifier.go:125-127
if aud := claimString(claims, "aud"); aud != "" {
    result.Audience = aud
}
```

`runtime/auth.Claims` already models `Audience []string`.

**Why this is a problem**

A valid ID token with `aud: ["client-a", "client-b"]` passes verification, but the returned `IDTokenClaims.Audience` comes back empty because `claimString` cannot represent arrays. Callers lose information that was necessary for verification and may make downstream authorization or audit decisions on incomplete claims.

**Recommendation**

Model `Audience` as `[]string` and preserve all verified audience values in the return type. If OIDC-specific semantics are needed, add `AuthorizedParty` (`azp`) explicitly rather than collapsing `aud` to a single string.

---

## F-04: Config validation is looser than the documented auth-code contract

| Field | Value |
|---|---|
| Seat | S5 DX + S6 Product |
| Severity | **P2** |
| Category | API contract drift |
| Files | `src/adapters/oidc/config.go:43-50`, `src/adapters/oidc/provider.go:44-47`, `src/adapters/oidc/token.go:38-44`, `docs/guides/adapter-config-reference.md:82-90` |
| Status | OPEN |

**Evidence**

The config guide marks `clientSecret` and `redirectURL` as required and documents cache/timeout defaults:

```text
docs/guides/adapter-config-reference.md:84-90
issuerURL yes
clientID yes
clientSecret yes
redirectURL yes
discoveryTimeout default 10s
jwksRefreshInterval default 1h
```

But `Config.Validate` checks only `IssuerURL` and `ClientID`:

```go
// config.go:44-50
if c.IssuerURL == "" { ... }
if c.ClientID == "" { ... }
```

`ExchangeCode` then blindly sends potentially empty `redirect_uri` and `client_secret`:

```go
// token.go:38-44
data := url.Values{
    "grant_type":    {"authorization_code"},
    "code":          {code},
    "redirect_uri":  {p.config.RedirectURL},
    "client_id":     {p.config.ClientID},
    "client_secret": {p.config.ClientSecret},
}
```

**Why this is a problem**

Configuration errors surface late as remote provider failures instead of local fail-fast validation, and the documented defaults do not fully match the implemented behavior. That makes the adapter harder to operate and harder to compose correctly.

**Recommendation**

Either:

1. Enforce the documented auth-code requirements in `Validate`, and apply all documented defaults in `NewProvider`; or
2. Split the config surface into explicit modes (discovery-only, verify-only, auth-code exchange) so each path validates only the fields it actually requires.

---

## F-05: `NewVerifier` accepts an invalid nil dependency and defers the failure to a panic path

| Field | Value |
|---|---|
| Seat | S1 Architecture + S5 DX |
| Severity | **P2** |
| Category | Constructor safety |
| Files | `src/adapters/oidc/verifier.go:57-63`, `src/adapters/oidc/verifier.go:180-183` |
| Status | OPEN |

**Evidence**

The constructor accepts a `nil` provider without complaint:

```go
// verifier.go:57-63
func NewVerifier(provider *Provider) *Verifier {
    return &Verifier{
        provider: provider,
        keyCache: make(map[string]*rsa.PublicKey),
    }
}
```

`Verify` eventually dereferences that dependency through `fetchJWKS`:

```go
// verifier.go:180-183
doc, err := v.provider.Discover(ctx)
if err != nil {
    return errcode.Wrap(ErrAdapterOIDCJWKS, "oidc jwks: discovery failed", err)
}
```

**Why this is a problem**

An exported constructor should not allow creation of an object that can only fail by nil-pointer dereference later. This is a boundary-quality problem: wiring mistakes become runtime crashes instead of immediate validation errors.

**Recommendation**

Change `NewVerifier` to return `(*Verifier, error)` and reject `nil` providers, or panic immediately in the constructor if the package deliberately treats a nil provider as programmer error.

---

## F-06: Boundary tests are still placeholder-only and miss the highest-risk failure modes

| Field | Value |
|---|---|
| Seat | S3 Test |
| Severity | **P2** |
| Category | Coverage / protocol regression risk |
| Files | `src/adapters/oidc/integration_test.go:9-30`, `src/adapters/oidc/oidc_test.go:160-355` |
| Status | OPEN |

**Evidence**

All integration tests are still stubbed:

```go
// integration_test.go:11-30
t.Skip("stub: requires OIDC provider (docker compose up keycloak)")
```

Tagged integration execution confirms that all four tests skip. Unit coverage is only `74.0%` and the current suite does not exercise:

- discovery issuer mismatch rejection
- JWKS refresh / rotated key behavior
- multi-audience return shape
- nil-provider constructor misuse
- malformed or partial discovery/JWKS metadata beyond the current happy paths

**Why this is a problem**

This adapter's failures sit on an external protocol boundary. The missing tests are not low-value edge cases; they are exactly the scenarios that protect against regressions in key rotation, provider trust, and API contract semantics.

**Recommendation**

Turn at least one real Keycloak-backed integration path on for the happy flow, and add focused unit tests for the negative boundary cases above. Until then, future refactors will have weak protection around the riskiest parts of the module.

---

## Positive Notes

- RS256 pinning is present in `Verifier.Verify` and the current tests confirm rejection of wrong-audience and expired tokens.
- The package keeps good layer hygiene: no `runtime/`, `cells/`, or sibling `adapters/` imports.
- Error surfaces consistently use `pkg/errcode` instead of leaking raw library errors.

---

## Assumption

No non-test in-tree consumer of `adapters/oidc` was found during this review. Findings therefore focus on the exported API contract and external boundary behavior rather than on call-site integration inside `access-core`.
