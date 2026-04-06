# R1D-4: adapters/oidc Review Summary

| Seat | File | Headline |
|---|---|---|
| Architect | `R1D-4-oidc-architect.md` | Cache contract and encapsulation issues |
| Security | `R1D-4-oidc-security.md` | Trust-boundary validation gaps |
| Testing | `R1D-4-oidc-testing.md` | Missing coverage on protocol edge cases |
| DX | `R1D-4-oidc-codestyle.md` | Config/constructor guardrails are too weak |
| Kernel Guardian | `R1D-4-oidc-kg.md` | No layering violation, but claim contract diverges |
| Product | `R1D-4-oidc-product.md` | Returned API contract is not OIDC-faithful |

## Top Cross-Seat Findings

1. **P1**: Discovery issuer mismatch is not validated.
2. **P1**: Multi-audience ID tokens are over-accepted and then returned with degraded audience data.
3. **P1**: `JWKSCacheTTL` is advertised but not enforced; cached keys do not expire.
4. **P1**: The interesting protocol edge cases are not covered by tests, and integration tests are still stubs.

## Verification

- `/opt/homebrew/bin/go test ./adapters/oidc/...` from `src/` -> PASS
- `/opt/homebrew/bin/go test -cover ./adapters/oidc/...` from `src/` -> PASS (`coverage: 74.0% of statements`)
- `/opt/homebrew/bin/go test -tags integration ./adapters/oidc/... -count=1 -v` from `src/` -> PASS, but all 4 integration tests are `t.Skip(...)`
