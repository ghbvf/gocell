# R1D-4 OIDC Adapter Code Style / DX Review

| Field | Value |
|---|---|
| Reviewer Seat | S5 DX / Maintainability |
| Scope | `src/adapters/oidc/*.go` plus `docs/guides/adapter-config-reference.md` for public-surface drift |
| Review Basis Commit | `5096d4f` |
| Date | 2026-04-06 |

---

## Summary

The package is small, readable, and generally consistent with repository conventions: `errcode` is used correctly, names follow the repository acronym baseline (`OIDC`, `JWKS`, `URL`, `ID`), and there is no style noise. The DX problems are more serious than formatting: a nil-constructor footgun, exported config that is never honored, and public docs that drift away from the real API.

Overall verdict: **2 P1, 1 P2**.

---

## Findings

### DX-01 | P1 | `NewVerifier(nil)` is an exported footgun that crashes later

- **Files**: `src/adapters/oidc/verifier.go:57-63`, `src/adapters/oidc/verifier.go:180-183`
- **Issue**: `NewVerifier` accepts a `nil` provider and returns a non-nil `*Verifier`. The first `Verify()` call then dereferences `v.provider` inside `fetchJWKS()` and panics.
- **Impact**: A simple constructor misuse becomes a runtime crash instead of an immediate, actionable error.
- **Fix**: Change the constructor to return `(*Verifier, error)` and reject nil input, or panic immediately with a precise constructor message instead of deferring the crash to first use.

### DX-02 | P1 | Exported config advertises behavior the package does not implement

- **Files**: `src/adapters/oidc/config.go:19-24`, `src/adapters/oidc/verifier.go:51-55`, `src/adapters/oidc/verifier.go:156-176`, `src/adapters/oidc/verifier.go:226-230`
- **Issue**: `Scopes` and `JWKSCacheTTL` are exported and documented configuration knobs, but `Scopes` is never read anywhere, `JWKSCacheTTL` is never applied, and `fetchAt` is dead state.
- **Impact**: Callers cannot trust the public surface. The code looks configurable in ways that do not actually exist.
- **Fix**: Either implement the documented behavior or remove the dead knobs. Do not keep exported fields whose semantics are fictional.

### DX-03 | P2 | Public config docs drift from the actual API surface

- **Files**: `docs/guides/adapter-config-reference.md:84-89`, `src/adapters/oidc/config.go:11-26`, `src/adapters/oidc/config.go:43-51`
- **Examples of drift**:
  - guide says `redirectURL` is required; `Config.Validate()` does not require it
  - guide says `discoveryTimeout`; code exposes `HTTPTimeout`
  - guide says `jwksRefreshInterval`; code exposes `JWKSCacheTTL`
  - guide implies `scopes` participates in the flow; code never reads it
- **Impact**: The easiest way for a developer to configure the adapter is currently misleading.
- **Fix**: Update the guide to match the code, and tighten validation rules so the code matches the intended contract.

---

## Positive Notes

- Acronym naming is correct throughout (`IssuerURL`, `JWKSURI`, `IDTokenClaims`).
- Production code consistently uses `errcode.New` / `errcode.Wrap`.
- There are no obvious formatting, logging, or naming violations inside the package itself.
