# R1C-1: runtime/auth Module Review

| Field | Value |
|-------|-------|
| Reviewer | R1C-1 (Security + Architecture + Test combined) |
| Scope | `src/runtime/auth/` -- 569 LOC prod, 736 LOC test (11 files) |
| Baseline commit | `ce03ba1` (develop HEAD) |
| Date | 2026-04-06 |

---

## Summary

The `runtime/auth` module provides RS256 JWT issuing/verification, HMAC-SHA256 service tokens, and HTTP auth middleware. Post-PR#16 hardening, RS256 is correctly pinned as the only signing method. The module is well-structured with clear separation of interfaces (`auth.go`) from implementations (`jwt.go`, `servicetoken.go`, `middleware.go`, `keys.go`). Test coverage is strong with 25 test functions covering happy paths and attack vectors.

**Overall risk: LOW-MEDIUM.** No P0 blockers found. Several P1 items related to error code duplication, missing key-size validation, and a minor security hardening gap.

---

## Findings

### F-01: Duplicate ErrAuthUnauthorized definition (P1 -- Code Quality)

| Field | Value |
|-------|-------|
| Seat | S1 Architecture + S5 DX |
| Severity | P1 |
| Category | Code duplication / naming collision |
| File | `src/runtime/auth/jwt.go:54`, `src/pkg/errcode/errcode.go:24` |
| Status | OPEN |

**Evidence:**

`jwt.go:54` defines:
```go
var ErrAuthUnauthorized = errcode.Code("ERR_AUTH_UNAUTHORIZED")
```

`errcode.go:24` defines:
```go
ErrAuthUnauthorized Code = "ERR_AUTH_UNAUTHORIZED"
```

Two distinct Go symbols (`auth.ErrAuthUnauthorized` and `errcode.ErrAuthUnauthorized`) hold the same string value. This is confusing for consumers who may import the wrong one, and violates the single-source-of-truth principle for error codes.

**Fix:** Remove the local `var ErrAuthUnauthorized` in `jwt.go` and import `errcode.ErrAuthUnauthorized` instead. The same applies if `ErrKeyMissing` in `keys.go:46` has no counterpart in errcode (it currently does not -- `ERR_AUTH_KEY_MISSING` vs `ERR_AUTH_KEY_INVALID`), which is a separate naming inconsistency.

---

### F-02: No minimum RSA key size validation (P1 -- Security)

| Field | Value |
|-------|-------|
| Seat | S2 Security |
| Severity | P1 |
| Category | Key management |
| File | `src/runtime/auth/keys.go:26-36`, `src/runtime/auth/jwt.go:24-26` |
| Status | OPEN |

**Evidence:**

`NewJWTVerifier` and `NewJWTIssuer` accept any `*rsa.PublicKey`/`*rsa.PrivateKey` without checking key size. `LoadRSAKeyPairFromPEM` and `LoadKeysFromEnv` also perform no minimum-bits check. A 512-bit or 1024-bit RSA key would be accepted silently, which is cryptographically weak.

**Fix:** Add a guard in `NewJWTVerifier`, `NewJWTIssuer`, and/or `LoadRSAKeyPairFromPEM` that rejects keys with `key.N.BitLen() < 2048`. Return an `errcode` error.

---

### F-03: Bare `fmt.Errorf` in internal key parsing functions (P1 -- Error Handling)

| Field | Value |
|-------|-------|
| Seat | S5 DX / S2 Security |
| Severity | P1 |
| Category | Error handling convention violation |
| File | `src/runtime/auth/keys.go:82,90,101,109` |
| Status | OPEN |

**Evidence:**

```go
// keys.go:82
return nil, fmt.Errorf("no PEM block found")
// keys.go:90
return nil, fmt.Errorf("PKCS#8 key is not RSA")
// keys.go:101
return nil, fmt.Errorf("no PEM block found")
// keys.go:109
return nil, fmt.Errorf("PKIX key is not RSA")
```

Project convention (CLAUDE.md): "errors exposed across package boundaries must use `pkg/errcode`". While `parseRSAPrivateKey`/`parseRSAPublicKey` are unexported, their errors propagate through `LoadRSAKeyPairFromPEM` (exported) and `LoadKeysFromEnv` (which wraps them via `errcode.Wrap`). The wrapping in `LoadKeysFromEnv` mitigates this, but `LoadRSAKeyPairFromPEM` exposes bare `fmt.Errorf` errors directly to callers.

**Fix:** Wrap errors in `LoadRSAKeyPairFromPEM` with `errcode.Wrap(ErrKeyMissing, ...)` or define an appropriate error code for key parsing failures.

---

### F-04: `writeAuthError` ignores JSON encoding error (P2 -- Error Handling)

| Field | Value |
|-------|-------|
| Seat | S5 DX |
| Severity | P2 |
| Category | Error handling |
| File | `src/runtime/auth/middleware.go:133` |
| Status | OPEN |

**Evidence:**

```go
_ = json.NewEncoder(w).Encode(map[string]any{...})
```

The blank identifier discards the encoding error. Per CLAUDE.md: "Prohibit `_ = someFunc()` ignoring errors". While this is an HTTP response write (failure typically means the client disconnected), it should at minimum be logged at Debug level per observability convention.

**Fix:** Replace `_ =` with `if err := json.NewEncoder(w).Encode(...); err != nil { slog.Debug("failed to write auth error response", slog.Any("error", err)) }`.

---

### F-05: Public endpoint whitelist uses exact path match only (P2 -- Security)

| Field | Value |
|-------|-------|
| Seat | S2 Security |
| Severity | P2 |
| Category | Auth bypass surface |
| File | `src/runtime/auth/middleware.go:40` |
| Status | OPEN |

**Evidence:**

```go
if publicSet[r.URL.Path] {
```

This is an exact-match lookup. It correctly handles query strings (`r.URL.Path` does not include the query string in Go's `net/http`). However, it does not handle trailing-slash variants (e.g., `/healthz/` would NOT match `/healthz`). This is generally safe (fail-closed: unmatched paths require auth), but could surprise users when reverse proxies normalize trailing slashes.

Additionally, there is no prefix-matching support for versioned API paths (e.g., `/api/v1/auth/` subtree). This is a design choice, not a bug, but should be documented.

**Fix:** Add a godoc note on `AuthMiddleware` clarifying exact-match semantics and trailing-slash behavior. Consider offering an optional prefix-match mode in a future iteration.

---

### F-06: `nowFunc` package-level mutable var is not concurrency-safe for tests (P2 -- Test Quality)

| Field | Value |
|-------|-------|
| Seat | S3 Test |
| Severity | P2 |
| Category | Test isolation / concurrency |
| File | `src/runtime/auth/servicetoken.go:19` |
| Status | OPEN |

**Evidence:**

```go
var nowFunc = time.Now
```

This exported-scope mutable variable is overridden in tests without synchronization. If `go test -parallel` or `-count=N` runs servicetoken tests concurrently, data races on `nowFunc` are possible. Current tests use `defer` cleanup but no mutex.

**Fix:** Either (a) use `sync.Mutex` around `nowFunc` access, (b) inject a `Clock` interface via `ServiceTokenMiddleware` options instead of a package-level var, or (c) mark all servicetoken tests with `t.Parallel()` removed (they already are not parallel, so this is low-risk in practice -- downgrade to advisory).

---

### F-07: No token revocation / JTI support (P2 -- Security Design)

| Field | Value |
|-------|-------|
| Seat | S2 Security |
| Severity | P2 |
| Category | JWT lifecycle |
| File | `src/runtime/auth/jwt.go` (entire file) |
| Status | OPEN |

**Evidence:**

The `JWTIssuer.Issue` method does not emit a `jti` (JWT ID) claim. The `JWTVerifier.Verify` method does not check for revoked tokens. There is no revocation interface or token blocklist mechanism anywhere in the module.

For short-lived tokens (the `ttl` parameter controls this), revocation is less critical. However, there is no enforcement of a maximum TTL, and callers could pass `24 * time.Hour` or longer, making stolen tokens dangerous.

**Fix:** (1) Add `jti` claim generation (UUID) in `Issue`. (2) Consider adding a `TokenBlacklist` interface that `JWTVerifier` can optionally check. (3) Document recommended TTL ranges in godoc. These are design improvements, not urgent for current scope.

---

### F-08: `ErrKeyMissing` vs `ErrAuthKeyInvalid` naming inconsistency (P2 -- DX)

| Field | Value |
|-------|-------|
| Seat | S5 DX |
| Severity | P2 |
| Category | Naming consistency |
| File | `src/runtime/auth/keys.go:46` vs `src/pkg/errcode/errcode.go:37` |
| Status | OPEN |

**Evidence:**

`keys.go:46`:
```go
var ErrKeyMissing = errcode.Code("ERR_AUTH_KEY_MISSING")
```

`errcode.go:37-39` defines:
```go
ErrAuthKeyInvalid   Code = "ERR_AUTH_KEY_INVALID"
ErrAuthTokenInvalid Code = "ERR_AUTH_TOKEN_INVALID"
ErrAuthTokenExpired Code = "ERR_AUTH_TOKEN_EXPIRED"
```

The string `"ERR_AUTH_KEY_MISSING"` does not appear in the central errcode registry. Meanwhile `"ERR_AUTH_KEY_INVALID"` exists in errcode but is not used by the auth module. This creates two separate code families for auth key errors.

**Fix:** Consolidate: use `errcode.ErrAuthKeyInvalid` for invalid key data and add `ErrAuthKeyMissing` to the central errcode package if the distinction is needed.

---

### F-09: RS256 algorithm pinning is correct -- confirmed (No Finding)

Positive confirmation: `JWTVerifier.Verify` at `jwt.go:33` checks `token.Method.(*jwt.SigningMethodRSA)` before returning the public key. This correctly rejects HS256, `none`, and all non-RSA algorithms. Tests confirm rejection of HS256 (`TestJWTVerifier_RejectsHS256`) and `none` (`TestJWTVerifier_RejectsAlgNone`). The PR#16 fix is properly in place.

---

### F-10: HMAC comparison uses constant-time `hmac.Equal` -- confirmed (No Finding)

Positive confirmation: `servicetoken.go:83` uses `hmac.Equal(providedMAC, expectedMAC)` which is constant-time, preventing timing attacks. The empty-secret fail-fast at `servicetoken.go:29-36` is also correct.

---

## Layering Compliance Check

| Check | Result |
|-------|--------|
| kernel/ imports runtime/auth? | N/A (not applicable -- this IS runtime/) |
| runtime/auth imports cells/? | PASS -- no cell imports |
| runtime/auth imports adapters/? | PASS -- no adapter imports |
| runtime/auth imports only pkg/ + stdlib + golang-jwt | PASS |
| Cross-Cell communication via contract? | N/A |
| CUD operations with consistency level? | N/A (no CUD -- pure auth logic) |

---

## Test Coverage Assessment

| File | Test File | Test Count | Assessment |
|------|-----------|------------|------------|
| auth.go (61 LOC) | auth_test.go | 2 | Good -- context round-trip covered |
| jwt.go (142 LOC) | jwt_test.go | 8 | Strong -- RS256 valid, expired, HS256 reject, none reject, wrong key, malformed, round-trip, no-roles |
| keys.go (115 LOC) | keys_test.go + jwt_test.go | 5 | Good -- PKCS#1, PKCS#8, missing env, invalid PEM, valid keys |
| middleware.go (139 LOC) | middleware_test.go | 11 | Strong -- valid, missing, invalid, non-bearer, public endpoints (default+custom), require-role (has/missing/no-claims/authorizer-fallback/authorizer-error) |
| servicetoken.go (112 LOC) | servicetoken_test.go | 11 | Strong -- valid, invalid, missing, wrong scheme, different path, empty secret, expired, exact boundary, within window, future timestamp, deterministic |

Estimated coverage: ~85-90% (above 80% threshold). Missing edge cases: empty-string subject in Issue, very large Extra claims map, concurrent Verify calls (though the implementation is stateless and safe).

---

## Risk Matrix

| Risk | Level | Mitigation |
|------|-------|------------|
| Algorithm confusion (HS256/none) | LOW | RS256 pinned, tested |
| Timing attack on HMAC | LOW | `hmac.Equal` used |
| Weak RSA key accepted | MEDIUM | No min-key-size check (F-02) |
| Token revocation gap | LOW-MEDIUM | Short TTL recommended but not enforced (F-07) |
| Error code duplication | LOW | Functional but confusing (F-01) |
| Layering violation | NONE | Clean dependency graph |

---

## Findings Summary

| ID | Severity | Category | File | Description |
|----|----------|----------|------|-------------|
| F-01 | P1 | Code quality | jwt.go:54, errcode.go:24 | Duplicate ErrAuthUnauthorized definition |
| F-02 | P1 | Security | keys.go, jwt.go | No minimum RSA key size validation |
| F-03 | P1 | Error handling | keys.go:82,90,101,109 | Bare fmt.Errorf in exported-path functions |
| F-04 | P2 | Error handling | middleware.go:133 | writeAuthError ignores JSON encode error |
| F-05 | P2 | Security | middleware.go:40 | Exact-match whitelist not documented |
| F-06 | P2 | Test quality | servicetoken.go:19 | Package-level mutable nowFunc |
| F-07 | P2 | Security design | jwt.go | No JTI / revocation support |
| F-08 | P2 | DX/naming | keys.go:46, errcode.go:37 | ErrKeyMissing vs ErrAuthKeyInvalid inconsistency |

**P0: 0 | P1: 3 | P2: 5 | Total: 8**
