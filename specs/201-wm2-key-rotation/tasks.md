# Tasks: JWT kid Rotation & HMAC Key Ring

**Input**: Design documents from `/specs/201-wm2-key-rotation/`  
**Prerequisites**: plan.md, spec.md, research.md, data-model.md, quickstart.md  
**TDD**: Required by Constitution Principle IV — all tests written first (RED), then implementation (GREEN)

**Organization**: Tasks grouped by user story. US1+US2 (both P1) are in separate phases but share foundational types. US3+US4 (both P2) are independent of each other.

## Format: `[ID] [P?] [Story] Description`

- **[P]**: Can run in parallel (different files, no dependencies)
- **[Story]**: Which user story (US1, US2, US3, US4)
- All paths relative to repository root

---

## Phase 1: Foundational (Types + kid Computation)

**Purpose**: Core types and utility functions that ALL user stories depend on. No user story work can begin until this phase is complete.

**Covers**: KeySet, VerificationKey, HMACKeyRing types; RFC 7638 thumbprint function.

- [x] T001 Write table-driven tests for RFC 7638 SHA-256 thumbprint computation (determinism, different key sizes, consistency) in src/runtime/auth/keys_test.go (TDD: RED)
- [x] T002 Implement `Thumbprint(pub *rsa.PublicKey) string` — RFC 7638 SHA-256 thumbprint that derives kid from public key material — in src/runtime/auth/keys.go (TDD: GREEN for T001)
- [x] T003 Write tests for KeySet constructor: single signing key, kid matches thumbprint, PublicKeyByKID returns correct key, PublicKeyByKID returns error for unknown kid in src/runtime/auth/keys_test.go (TDD: RED)
- [x] T004 Implement `VerificationKey` struct and `KeySet` struct with `NewKeySet(privateKey, publicKey)` constructor and `PublicKeyByKID(kid) (*rsa.PublicKey, error)` method in src/runtime/auth/keys.go (TDD: GREEN for T003)

**Checkpoint**: Foundational types compile and pass tests. `go test ./src/runtime/auth/... -run TestThumbprint` and `go test ./src/runtime/auth/... -run TestKeySet` both GREEN.

---

## Phase 2: User Story 1 — JWT Tokens Include Key Identifier (Priority: P1) MVP

**Goal**: Every issued JWT token carries a `kid` header; verification selects the matching key from KeySet by kid, not by single hardcoded key.

**Independent Test**: Issue a token, decode the header, confirm `kid` is present and matches the signing key's thumbprint. Verify round-trip: issue → verify succeeds. Verify unknown/missing kid → rejection.

### Tests for User Story 1

> **TDD: Write these tests FIRST. They MUST FAIL before implementation.**

- [x] T005 [P] [US1] Write tests for JWTIssuer: issued token header contains `kid` field matching signing key thumbprint in src/runtime/auth/jwt_test.go (TDD: RED)
- [x] T006 [P] [US1] Write tests for JWTVerifier: verifies token by looking up public key from KeySet using token's `kid` header in src/runtime/auth/jwt_test.go (TDD: RED)
- [x] T007 [P] [US1] Write tests for JWTVerifier rejection: unknown kid returns error; missing kid header returns error in src/runtime/auth/jwt_test.go (TDD: RED)

### Implementation for User Story 1

- [x] T008 [US1] Update `NewJWTIssuer` to accept `*KeySet` instead of `*rsa.PrivateKey`; update `Issue()` to set `token.Header["kid"] = keySet.SigningKeyID()` in src/runtime/auth/jwt.go (TDD: GREEN for T005)
- [x] T009 [US1] Update `NewJWTVerifier` to accept `*KeySet` instead of `*rsa.PublicKey`; update `Verify()` KeyFunc to extract `kid` from token header and call `keySet.PublicKeyByKID(kid)` in src/runtime/auth/jwt.go (TDD: GREEN for T006, T007)
- [x] T010 [US1] Update existing tests in src/runtime/auth/jwt_test.go and src/runtime/auth/middleware_test.go to use KeySet-based constructors (fix compilation)

**Checkpoint**: `go test ./src/runtime/auth/... -run TestJWT` all GREEN. Tokens have kid. Verification works by kid lookup.

---

## Phase 3: User Story 2 — JWT Key Set Supports Multiple Verification Keys (Priority: P1)

**Goal**: KeySet holds one active signing key + zero or more verification-only keys with expiry. Tokens signed by non-expired verification keys are accepted. Expired keys are pruned.

**Independent Test**: Create KeySet with signing key + verification key. Issue token with old key's kid. Verify it succeeds. Advance time past expiry. Verify it fails (key pruned).

### Tests for User Story 2

- [x] T011 [P] [US2] Write tests for KeySet multi-key: verification-only key lookup by kid succeeds; signing always uses active key only in src/runtime/auth/keys_test.go (TDD: RED)
- [x] T012 [P] [US2] Write tests for KeySet pruning: expired verification key is removed on PruneExpired(); lookup by pruned kid fails in src/runtime/auth/keys_test.go (TDD: RED)
- [x] T013 [P] [US2] Write tests for KeySet edge cases: rapid rotation replaces oldest verification key; zero/negative expiry prunes immediately in src/runtime/auth/keys_test.go (TDD: RED)

### Implementation for User Story 2

- [x] T014 [US2] Implement `NewKeySetWithVerificationKeys(privateKey, publicKey, verificationKeys []VerificationKey)` constructor and `PruneExpired()` method in src/runtime/auth/keys.go (TDD: GREEN for T011, T012, T013)
- [x] T015 [US2] Implement `LoadKeySetFromEnv()` — load active key pair from existing env vars + optional `GOCELL_JWT_PREV_PUBLIC_KEY` and `GOCELL_JWT_PREV_KEY_EXPIRES` for verification-only key in src/runtime/auth/keys.go
- [x] T016 [US2] Write tests for `LoadKeySetFromEnv`: active-only, active+verification, missing active fails, invalid expiry fails in src/runtime/auth/keys_test.go

**Checkpoint**: `go test ./src/runtime/auth/... -run TestKeySet` all GREEN. Multi-key verification works. Pruning works. LoadKeySetFromEnv works.

---

## Phase 4: User Story 3 — HMAC Secrets Support Graceful Rotation (Priority: P2)

**Goal**: HMACKeyRing holds `[current, previous]` secrets. Signing always uses current. Verification tries current then previous. Unknown secrets rejected. Single-secret mode works.

**Independent Test**: Generate token with old secret. Create ring with new+old. Verify old token succeeds. Verify completely unknown secret fails.

### Tests for User Story 3

- [x] T017 [P] [US3] Write tests for HMACKeyRing: sign with current (position 0), verify succeeds with current, verify succeeds with previous in src/runtime/auth/servicetoken_test.go (TDD: RED)
- [x] T018 [P] [US3] Write tests for HMACKeyRing: reject unknown secret; single-secret mode (nil previous) works correctly in src/runtime/auth/servicetoken_test.go (TDD: RED)
- [x] T019 [P] [US3] Write tests for HMACKeyRing edge case: same secret in both positions works (degenerate case) in src/runtime/auth/servicetoken_test.go (TDD: RED)

### Implementation for User Story 3

- [x] T020 [US3] Implement `HMACKeyRing` struct with `Current() []byte` and `Secrets() [][]byte` methods in src/runtime/auth/servicetoken.go (TDD: GREEN for T017, T018, T019)
- [x] T021 [US3] Update `ServiceTokenMiddleware` to accept `*HMACKeyRing` — try-all-keys verification (current first, then previous) in src/runtime/auth/servicetoken.go
- [x] T022 [US3] Update `GenerateServiceToken` to accept `*HMACKeyRing` — always sign with Current() in src/runtime/auth/servicetoken.go
- [x] T023 [US3] Implement `LoadHMACKeyRingFromEnv()` — load `GOCELL_SERVICE_SECRET` + optional `GOCELL_SERVICE_SECRET_PREVIOUS` in src/runtime/auth/servicetoken.go
- [x] T024 [US3] Write tests for `LoadHMACKeyRingFromEnv`: current-only, current+previous, missing current fails in src/runtime/auth/servicetoken_test.go
- [x] T025 [US3] Update existing ServiceToken tests in src/runtime/auth/servicetoken_test.go to use HMACKeyRing-based API (fix compilation)

**Checkpoint**: `go test ./src/runtime/auth/... -run TestServiceToken` and `go test ./src/runtime/auth/... -run TestHMAC` all GREEN. Key ring rotation works.

---

## Phase 5: User Story 4 — Key Lifecycle Transitions (Priority: P2)

**Goal**: All key lifecycle transitions (activation, demotion to verification-only, pruning) produce structured slog entries. System fails fast on missing signing key configuration.

**Independent Test**: Create KeySet, trigger lifecycle transitions, capture slog output, verify structured fields (kid, transition type, timestamp) are present.

### Tests for User Story 4

- [x] T026 [P] [US4] Write tests for slog output on KeySet creation (key activation log), verification key addition (demotion log), and pruning (pruning log) in src/runtime/auth/keys_test.go (TDD: RED)
- [x] T027 [P] [US4] Write tests for fail-fast: NewKeySet with nil key panics or returns error; LoadKeySetFromEnv with missing env var returns clear error in src/runtime/auth/keys_test.go (TDD: RED)

### Implementation for User Story 4

- [x] T028 [US4] Add structured slog.Info logging to KeySet constructor (activation), AddVerificationKey/NewKeySetWithVerificationKeys (demotion), and PruneExpired (pruning) with fields: kid, transition, timestamp in src/runtime/auth/keys.go (TDD: GREEN for T026)
- [x] T029 [US4] Add fail-fast validation: NewKeySet returns error on nil/invalid key; LoadKeySetFromEnv returns errcode error on missing GOCELL_JWT_PRIVATE_KEY or GOCELL_JWT_PUBLIC_KEY in src/runtime/auth/keys.go (TDD: GREEN for T027)

**Checkpoint**: `go test ./src/runtime/auth/... -run TestLifecycle` and `go test ./src/runtime/auth/... -run TestFailFast` all GREEN. Lifecycle events observable in logs.

---

## Phase 6: Polish & Cross-Cutting Concerns

**Purpose**: Update callers, verify full build, ensure coverage.

- [x] T030 [P] Update callers in src/cells/access-core/ to use KeySet-based JWTIssuer/JWTVerifier API
- [x] T031 [P] Update callers in src/examples/sso-bff/main.go to use KeySet and HMACKeyRing API
- [x] T032 [P] Update src/adapters/oidc/oidc.go if it references old JWTVerifier constructor
- [x] T033 Run `go build ./...` — verify zero compilation errors across entire codebase
- [x] T034 Run `go test ./src/runtime/auth/...` — verify all tests pass, coverage ≥ 80%
- [x] T035 Run `go vet ./src/runtime/auth/...` — verify no vet warnings

**Checkpoint**: Full codebase compiles. All auth tests pass. Coverage meets threshold.

---

## Dependencies & Execution Order

### Phase Dependencies

```
Phase 1 (Foundational)    ← No dependencies, start immediately
    │
    ▼
Phase 2 (US1: kid in tokens) ← Depends on Phase 1 complete
    │
    ▼
Phase 3 (US2: multi-key)     ← Depends on Phase 2 (US1 introduces KeySet API)
    │
    ├──→ Phase 4 (US3: HMAC ring)       ← Independent of US2, depends on Phase 1 only
    │
    └──→ Phase 5 (US4: lifecycle logs)   ← Depends on Phase 3 (needs KeySet lifecycle methods)
              │
              ▼
         Phase 6 (Polish)               ← Depends on all user stories complete
```

### User Story Dependencies

- **US1 (P1)**: Depends on Foundational (Phase 1). No dependency on other stories.
- **US2 (P1)**: Depends on US1 (Phase 2) — extends the KeySet API introduced by US1.
- **US3 (P2)**: Depends on Foundational (Phase 1) only. Can run in **parallel** with US1/US2.
- **US4 (P2)**: Depends on US2 (Phase 3) — logs lifecycle events from KeySet methods.

### Within Each User Story

1. Tests MUST be written first and FAIL (TDD: RED)
2. Implementation makes tests pass (TDD: GREEN)
3. Type changes before function changes
4. Internal API before public API (LoadFromEnv)
5. Existing test fixup after API changes

### Parallel Opportunities

- **Phase 1**: T001→T002 sequential, T003→T004 sequential, but T001+T003 can start in parallel (different test functions)
- **Phase 2 (US1)**: T005, T006, T007 can run in parallel (different test functions in same file)
- **Phase 3 (US2)**: T011, T012, T013 can run in parallel
- **Phase 4 (US3)**: T017, T018, T019 can run in parallel. **US3 can run in parallel with US1/US2** (different files entirely)
- **Phase 5 (US4)**: T026, T027 can run in parallel
- **Phase 6**: T030, T031, T032 can all run in parallel (different files)

---

## Parallel Example: US1 + US3 Concurrent

```text
# These two stories touch different files and can be worked on simultaneously:

# Developer A: US1 (JWT kid)
T005: Test JWTIssuer kid header in jwt_test.go
T006: Test JWTVerifier kid lookup in jwt_test.go
T007: Test rejection in jwt_test.go
T008: Update JWTIssuer in jwt.go
T009: Update JWTVerifier in jwt.go

# Developer B: US3 (HMAC ring) — can start after Phase 1, no dependency on US1
T017: Test HMACKeyRing sign/verify in servicetoken_test.go
T018: Test rejection/single-secret in servicetoken_test.go
T019: Test edge cases in servicetoken_test.go
T020: Implement HMACKeyRing in servicetoken.go
T021: Update ServiceTokenMiddleware in servicetoken.go
```

---

## Implementation Strategy

### MVP First (US1 Only)

1. Complete Phase 1: Foundational types (T001-T004)
2. Complete Phase 2: US1 — JWT kid in tokens (T005-T010)
3. **STOP and VALIDATE**: Issue a token, verify kid is present, round-trip works
4. This alone enables kid-based key identification — the foundation for all rotation

### Incremental Delivery

1. Phase 1 (Foundational) → Types ready
2. Phase 2 (US1) → Tokens carry kid → **MVP deployable**
3. Phase 3 (US2) → Multi-key verification → **Zero-downtime JWT rotation**
4. Phase 4 (US3) → HMAC key ring → **Zero-downtime service token rotation**
5. Phase 5 (US4) → Lifecycle logging → **Operational visibility**
6. Phase 6 (Polish) → Full integration → **Feature complete**

### Test Coverage Target

| File | Target | Method |
| --- | --- | --- |
| keys.go | ≥ 85% | Table-driven tests for Thumbprint, KeySet, VerificationKey, LoadKeySetFromEnv |
| jwt.go | ≥ 80% | Tests for kid header, kid-based verification, rejection paths |
| servicetoken.go | ≥ 80% | Tests for HMACKeyRing sign/verify, LoadHMACKeyRingFromEnv |

---

## Notes

- All changes are within `src/runtime/auth/` — no new packages or directories
- No new external dependencies — uses existing `github.com/golang-jwt/jwt/v5` + stdlib `crypto/*`
- Constitution requires TDD: every test task (RED) must FAIL before its matching implementation task (GREEN)
- `auth.go` and `middleware.go` are NOT modified — new types integrate behind existing interfaces
- kid values (SHA-256 thumbprints) are safe to log; secret values MUST NOT appear in logs
- Commit after each phase checkpoint with Conventional Commits format
