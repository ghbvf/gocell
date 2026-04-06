# R1D-4: adapters/oidc -- Kernel Guardian Review

**Reviewer role**: Kernel Guardian  
**Scope**: `src/adapters/oidc/` (5 production `.go` files + tests + package doc)  
**Date**: 2026-04-06  
**Baseline**: `5096d4f`

---

## 1. File Inventory

| File | LOC | Purpose |
|------|-----|---------|
| `config.go` | 52 | Public configuration surface |
| `errors.go` | 19 | `ERR_ADAPTER_OIDC_*` codes |
| `provider.go` | 124 | Discovery fetch + cache |
| `token.go` | 84 | Authorization-code token exchange |
| `userinfo.go` | 73 | UserInfo request |
| `verifier.go` | 279 | JWKS fetch + ID token verification |
| `oidc_test.go` | 355 | Unit tests |
| `integration_test.go` | 31 | Integration placeholders |
| `doc.go` | 15 | Package contract |

---

## 2. Layer Isolation Check

### 2.1 Production imports

| File | Internal imports | Verdict |
|------|------------------|---------|
| `config.go` | `pkg/errcode` | GREEN |
| `errors.go` | `pkg/errcode` | GREEN |
| `provider.go` | `pkg/errcode` | GREEN |
| `token.go` | `pkg/errcode` | GREEN |
| `userinfo.go` | `pkg/errcode` | GREEN |
| `verifier.go` | `pkg/errcode` | GREEN |

**Result**: No imports from `runtime/`, `cells/`, or other adapters. Layer direction is clean.

---

## 3. Contract Findings

### KG-01 | P1 | OIDC discovery contract is only partially enforced

- **Files**: `src/adapters/oidc/provider.go:102-121`
- **Issue**: The adapter accepts any non-empty discovery `issuer`, but an OIDC client is expected to reject metadata whose `issuer` does not exactly match the configured issuer. The returned endpoints are then trusted without further validation.
- **Why this is a contract issue**: This package is an external-protocol adapter. Its main job is to turn an untrusted OIDC boundary into a trustworthy local contract. Today it stops halfway.
- **Fix**: Enforce `doc.Issuer == config.IssuerURL` and validate endpoint schemes/consistency before caching the document.

### KG-02 | P1 | ID token verification contract is weaker than the package name implies

- **Files**: `src/adapters/oidc/verifier.go:67-135`
- **Issue**: `Verify()` guarantees signature validity plus `iss`/`aud`, but not the full minimum identity contract. It accepts tokens with empty `sub` and no required `exp`.
- **Why this is a contract issue**: Callers reasonably expect `Verify()` success to mean "usable OIDC identity token." The current implementation only proves a narrower JWT property.
- **Fix**: Enforce required OIDC claims (`sub`, `exp`) in the verifier, not in every downstream caller.

### KG-03 | P1 | Test-policy compliance is still yellow: 74.0% < 80%

- **Evidence**: `/opt/homebrew/bin/go test -count=1 -cover ./adapters/oidc` run from `src/` reports `74.0%`.
- **Policy basis**: non-kernel code must stay at or above 80%.
- **Impact**: The package is not yet meeting the repository's verification floor for adapter code.
- **Fix**: Fill the missing negative-path and integration coverage rather than trying to inflate coverage with low-value assertions.

---

## 4. Positive Checks

- Error-code namespace is module-local and consistent: `ERR_ADAPTER_OIDC_*`.
- The package boundary is compact and free of upward framework coupling.
- RS256 pinning is explicit and correct.

---

## 5. Guardian Verdict

| Dimension | Result | Notes |
|-----------|--------|-------|
| Layer isolation | GREEN | No forbidden imports |
| External protocol contract | YELLOW | Discovery and verification semantics are incomplete |
| Error code discipline | GREEN | `errcode` used consistently |
| Coverage policy | YELLOW | 74.0% < 80% |

**Overall**: GREEN on layering, **YELLOW** on protocol contract strength and verification depth.
