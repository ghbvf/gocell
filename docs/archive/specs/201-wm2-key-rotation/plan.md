# Implementation Plan: JWT kid Rotation & HMAC Key Ring

**Branch**: `201-wm2-key-rotation` | **Date**: 2026-04-11 | **Spec**: [spec.md](spec.md)  
**Input**: Feature specification from `/specs/201-wm2-key-rotation/spec.md`

## Summary

Extend `runtime/auth/` to support JWT key rotation via kid (key identifier) headers and HMAC secret rotation via a 2-position key ring. Currently, the module uses a single RSA key pair (no kid) and a single HMAC secret (no rotation). This plan adds:

1. **KeySet** — holds 1 active signing key + N verification-only keys, with kid derived from RFC 7638 SHA-256 thumbprint. Adopts Dex's 3-state lifecycle (Active → Verification-only → Pruned).
2. **HMACKeyRing** — ordered pair of secrets `[current, previous]` with try-all-keys verification. Adopts gorilla/go-zero positional model.
3. **Updated JWTIssuer/JWTVerifier** — issue tokens with kid header, verify by kid-based key lookup.
4. **Updated ServiceTokenMiddleware** — verify against key ring instead of single secret.

All changes are in `runtime/auth/`. No new dependencies, no database changes, no new contracts.

## Technical Context

**Language/Version**: Go (latest stable)  
**Primary Dependencies**: `github.com/golang-jwt/jwt/v5` (existing), `crypto/*` stdlib  
**Storage**: N/A (in-memory key set, loaded from static config)  
**Testing**: `go test`, `testify` assertions, table-driven tests  
**Target Platform**: Linux server  
**Project Type**: Library (runtime layer)  
**Performance Goals**: kid-based key lookup within 1ms per verification (O(1) map lookup vs current single-key)  
**Constraints**: No new external dependencies to `runtime/`; `runtime/` MUST NOT depend on `cells/` or `adapters/`  
**Scale/Scope**: Single-service key management; multi-service JWKS discovery is out of scope

## Constitution Check

*GATE: Must pass before Phase 0 research. Re-check after Phase 1 design.*

| Principle | Status | Notes |
| --- | --- | --- |
| I. Cell-Native Layering | PASS | Changes in `runtime/auth/` — depends only on `pkg/` and stdlib. No `cells/` or `adapters/` imports. |
| II. Cell Governance | N/A | No Cell/Slice metadata changes. |
| III. Contract Boundary | N/A | No new contracts. Internal runtime module. |
| IV. TDD First | GATE | Must write tests first (Red → Green → Refactor). Coverage ≥ 80% for new code. |
| V. Cell Data Sovereignty | N/A | No database changes. |
| VI. Event-Driven | N/A | No events involved. This is L0 (pure computation). |
| VII. Journey Acceptance | N/A | Runtime library, not a user journey. |
| VIII. Security Built-in | GATE | Key operations MUST produce audit logs (slog). Secrets MUST NOT appear in logs. kid values are safe to log. |
| IX. Simplicity & Incremental | PASS | Scope explicitly bounded: no auto-rotation, no JWKS, no DB storage, no multi-algorithm. |

**Six-Question Checklist (Principle VI)**: Not applicable — this feature is L0 (pure in-memory computation, no state persistence, no events). Key material is loaded from static configuration and managed in memory.

**Red Line Check**:
- RL-14 (no noop fallback): `LoadKeySetFromEnv()` and `LoadHMACKeyRingFromEnv()` fail-fast on missing keys. No silent degradation.
- RL-13 (no t.Skip abuse): All new tests will be active; no skips.
- All other red lines: not triggered by this feature.

### Post-Phase-1 Re-check

| Principle | Status | Notes |
| --- | --- | --- |
| I. Cell-Native Layering | PASS | Confirmed: all new types (`KeySet`, `VerificationKey`, `HMACKeyRing`) in `runtime/auth/`. No cross-layer imports. |
| IV. TDD First | GATE | Remains: implementation phase must follow Red → Green → Refactor. |
| VIII. Security Built-in | PASS | Design includes structured slog for lifecycle transitions. kid (thumbprint) is non-sensitive — safe to log. Secret values never logged. |
| IX. Simplicity | PASS | 3 new types, ~6 new/modified functions. No abstractions beyond what the feature requires. |

## Project Structure

### Documentation (this feature)

```text
specs/201-wm2-key-rotation/
├── plan.md              # This file
├── spec.md              # Feature specification
├── research.md          # Phase 0: consolidated research decisions
├── data-model.md        # Phase 1: entity definitions and invariants
├── quickstart.md        # Phase 1: usage examples before/after
└── checklists/
    └── requirements.md  # Spec quality checklist
```

### Source Code (repository root)

```text
runtime/auth/
├── auth.go              # (existing) Claims, TokenVerifier, Authorizer interfaces — no changes
├── middleware.go         # (existing) AuthMiddleware, RequireRole — no changes
├── keys.go              # (modify) Add KeySet, VerificationKey, LoadKeySetFromEnv, kid computation
├── jwt.go               # (modify) JWTIssuer adds kid header; JWTVerifier uses KeySet for kid lookup
├── servicetoken.go      # (modify) HMACKeyRing, try-all-keys verification, LoadHMACKeyRingFromEnv
├── keys_test.go         # (modify) Tests for KeySet lifecycle, kid generation, pruning
├── jwt_test.go          # (modify) Tests for kid in issued tokens, kid-based verification
└── servicetoken_test.go # (modify) Tests for key ring verification, rotation scenarios
```

**Structure Decision**: All changes are within the existing `runtime/auth/` package. No new packages, directories, or files beyond what exists. The feature extends existing types and functions.

## Complexity Tracking

No constitution violations to justify. All changes stay within existing package boundaries, use existing dependencies, and add no new abstractions beyond the three entities defined in the data model.

## Framework References

| GoCell Component | Reference Framework | Source File | Adoption |
| --- | --- | --- | --- |
| KeySet lifecycle | Dex | `server/rotation.go` | Adopted: 3-state model (Active → Verification-only → Pruned). Deviated: kid is SHA-256 thumbprint (not random hex); no rotation timer (static config). |
| KeyFunc verification | Kratos | `middleware/auth/jwt/jwt.go` | Adopted: `KeyFunc(token) → key` pattern, context injection. Already in use by GoCell's `JWTVerifier`. |
| HMAC key ring | go-zero | `rest/token/tokenparser.go` | Adopted: dual-key [current, previous] model. Deviated: simple ordered try (not frequency-based priority). |
| HMAC try-all-keys | gorilla/securecookie | `securecookie.go` | Adopted: `DecodeMulti` pattern — try current, fallback to previous. |
