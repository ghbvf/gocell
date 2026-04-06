# R1D-4: adapters/oidc Testing Review

| Field | Value |
|-------|-------|
| Reviewer seat | S3 Testing / Regression |
| Scope | `src/adapters/oidc/` production + test files |
| Review baseline | `5096d4f` (current develop/review HEAD) |
| Date | 2026-04-06 |

---

## Summary

The unit tests cover the happy path and a few basic failure cases, and the package does compile and test cleanly from the module root. The problem is depth: statement coverage is still below policy, all integration tests are placeholders, and several protocol-critical negative paths are entirely untested.

Verification run from `/Users/shengming/Documents/code/gocell/src`:

- `/opt/homebrew/bin/go test -count=1 ./adapters/oidc` -> PASS
- `/opt/homebrew/bin/go test -count=1 -cover ./adapters/oidc` -> **74.0%**

Overall verdict: **3 P1**.

---

## Findings

### T-01 | P1 | Unit coverage is 74.0%, below the 80% floor for non-kernel code

- **Evidence**: `/opt/homebrew/bin/go test -count=1 -cover ./adapters/oidc` reports `coverage: 74.0% of statements`.
- **Policy basis**: `docs/guides/cell-development-guide.md:162` and `docs/product/roadmap/202604050853-000-gocell框架补充计划.md:11` require `kernel >= 90%` and other layers `>= 80%`.
- **Hot spots**:
  - `src/adapters/oidc/verifier.go:139` `audienceMatch` -> 42.9%
  - `src/adapters/oidc/token.go:27` `ExchangeCode` -> 68.0%
  - `src/adapters/oidc/userinfo.go:25` `GetUserInfo` -> 70.8%
  - `src/adapters/oidc/verifier.go:180` `fetchJWKS` -> 70.3%
- **Impact**: The package is below the repository acceptance bar, and the uncovered branches are protocol and error-handling code rather than dead code.
- **Fix**: Add targeted tests for JWKS refresh, multi-audience claims, malformed discovery metadata, malformed token payloads, and userinfo/token endpoint failures.

### T-02 | P1 | Integration tests are all stubs, so the real protocol boundary is unverified

- **Files**: `src/adapters/oidc/integration_test.go:9-30`
- **Issue**: All four integration tests are `t.Skip(...)` placeholders. There is no real provider test for discovery, code exchange, ID token verification, or userinfo.
- **Impact**: The package has no proof that it interoperates with an actual OIDC provider. That is a significant gap for a boundary adapter whose main risk is protocol correctness.
- **Fix**: Replace the stubs with an integration harness using a real OIDC test provider under the `integration` build tag. Keycloak or a lightweight mock OIDC container would both be acceptable.

### T-03 | P1 | Critical negative-path regressions are not covered at all

- **Files**: `src/adapters/oidc/oidc_test.go:160-355`, `src/adapters/oidc/verifier.go:67-152`, `src/adapters/oidc/provider.go:70-123`
- **Missing cases**:
  - discovery document issuer mismatch
  - token missing `sub`
  - token missing `exp`
  - `aud` as array, including the returned claim shape
  - missing `kid`
  - unsupported or malformed JWKS contents
  - JWKS refresh behavior when cache should expire
  - token endpoint returns HTTP 200 with malformed/empty token payload
- **Impact**: The currently passing suite does not protect the module against the exact protocol edge cases most likely to bite at integration time.
- **Fix**: Add table-driven tests for claim semantics and discovery validation, plus one refresh-oriented test that proves JWKS cache policy works as documented.

---

## Positive Notes

- The current test server is clean and easy to extend.
- `Verify()` already has happy-path, wrong-audience, and expired-token coverage.
- Running the package tests from the module root is straightforward; there are no hidden environment dependencies for the unit suite.
