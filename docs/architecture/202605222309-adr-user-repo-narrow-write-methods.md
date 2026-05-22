# ADR 202605222309 — UserRepository Narrow Write Methods

**Status**: Accepted
**Date**: 2026-05-22
**Issue**: #828

---

## 1. Context

### Pre-refactor state

`ports.UserRepository` exposed a single generic `Update(ctx context.Context, user *domain.User) error` method backed by an 11-column SQL statement touching:

- `username` / `email` (profile fields)
- `status` (lock state)
- `password_hash` / `password_version` / `password_reset_required` (credential fields)
- `failed_login_count` / `last_failed_at` / `locked_until` (auto-lockout fields)
- `authz_epoch` (invalidation epoch)
- `updated_at`

Callers depended on godoc convention ("Do NOT call this for password changes") to avoid field bleed. From an AI-collab viewpoint this is **Soft** enforcement: a future maintainer can inadvertently pass a `*domain.User` with a modified `password_hash` via the `Update` path and silently rotate credentials without triggering any credential-invalidation event.

### Prior narrow method introductions

- **S6** (PR introducing `UpdatePassword`): first narrow method, CAS-guarded password change path with explicit `password_version` parameter
- **PR #585 P1#3**: identified admin-unlock re-lock race — admin activating a locked user did not zero the lockout counters because the `Update` caller held responsibility for that cleanup (Soft)

### AI-rebust motivation

`.claude/rules/gocell/ai-collab.md` §"Soft → Hard 改造方向":

> 字符串锚点 → typed function call

Generic `Update(*User)` is the "string anchor" equivalent at the method-signature level: any caller can silently set any field. Narrow methods make "which columns this write touches" part of the signature — an AI co-author choosing the wrong method for a use case gets either a compiler error or an archtest failure.

---

## 2. Decision

### Five decisions shipped with issue #828

| # | Decision | Landing |
|---|----------|---------|
| 1 | `UpdateProfile(ctx, userID, name, email *string, now)` — PATCH semantics in the signature | `*string` parameter + SQL `COALESCE($n, col)` / mem pointer-nil branch; returns `*domain.User` from the persisted row |
| 2 | `UpdateLockState(ctx, userID, status, now)` — no `resetLockout bool` parameter | "StatusActive auto-zeros lockout" bound to SQL `CASE WHEN $2 = 'active'` / mem `ResetFailedLogins` inside the same statement. Does not return `*User`; the `authzmutate.Mutator.ApplyInTx` epoch-bump is a separate SQL statement (`BumpAuthzEpoch`) issued only when `m.Invalidates() == true`. Callers that need the post-write aggregate call `GetByID` explicitly after the mutation. |
| 3 | Archtest `USERREPO-METHOD-SET-FROZEN-01` ships in the same PR | `tools/archtest/userrepo_method_set_frozen_test.go` — locks the 12-method set |
| 4 | `UpdateProfile` returns `(*domain.User, error)` | Single round-trip: PG `RETURNING *` reconstitutes the aggregate; mem writes in-place then calls `ReconstituteUser`. Caller uses the returned aggregate for downstream publish / audit, eliminating shadow-mutation drift |
| 5 | `loadInterfaceType` helper in `cell_iface_isp_invariants_test.go` accepts a `dirRel` parameter | Parameterized so `USERREPO-METHOD-SET-FROZEN-01` can reuse the same AST scanning helper for a different package path |

### Mutation form: apply(u, now) → persist(ctx, repo, userID, now)

Each domain mutation directly calls its narrow port method. `ApplyInTx` is compressed from 4 steps to 2 steps (eliminating a `GetByIDForUpdate` round-trip) because the narrow port method returns the post-write aggregate.

**Round-trip count**: `authzmutate.Mutator.ApplyInTx` makes exactly 1 write round-trip (via `m.persist` → one of `UpdateLockState` / `UpdatePasswordResetFlag`) plus 0 or 1 additional round-trip when `m.Invalidates() == true` (the `inv.Apply` call bumps `authz_epoch` + revokes sessions in separate SQL). The `identitymanage.Update` caller makes 1 read (`GetByIDForUpdate`) + N mutation round-trips (1 for profile, 0-1 for authz via `ApplyInTx`) + 1 re-fetch (`GetByID`), totalling at most 4 statements in a single transaction. This is a deliberate trade-off: the re-fetch is a plain `GetByID` (not `ForUpdate`) so it reads the already-committed MVCC snapshot without acquiring a new lock.

### Final 12-method interface

```
BumpAuthzEpoch
Create
Delete
GetByID
GetByIDForUpdate
GetByUsername
GetByUsernameForUpdate
UpdateLockState
UpdateLockoutFields
UpdatePassword
UpdatePasswordResetFlag
UpdateProfile
```

`UpdateLockoutFields` (auto-lockout path only) and `BumpAuthzEpoch` (credential-invalidation path) were already narrow methods introduced before this ADR; they are retained unchanged.

---

## 3. Alternatives Considered

### 2-method merge: UpdateProfile + UpdateAuthzState

Merge `UpdatePasswordResetFlag` into a combined "authz-state" update alongside `UpdateLockState`. Rejected: pointer-field "I didn't say to change this" semantics re-introduce Soft caller knowledge. A caller passing a zero-value `required bool` to clear `password_reset_required` is indistinguishable from "I don't want to change this field".

### Dex updater-closure: `UpdateXxx(ctx, id, func(x) (x, error))`

PG ambient transaction + `SELECT ... FOR UPDATE` already serializes read-modify-write at the DB layer. The closure pattern adds no correctness benefit and couples the port interface to a Go closure shape, making code generation harder.

### ent fluent builder: `Update().SetXxx().Where(ver).Save()`

Generated code path increases build pipeline complexity. GoCell does not use the ent ecosystem, so this would add a new dependency for a pattern already solved by explicit narrow methods.

### Retain generic Update + archtest to lock touched columns

Archtest detecting which columns a SQL statement writes is Soft (the test must be updated on every schema change) and fragile (string matching on SQL literals). Narrow method signatures are Hard: the type system prevents passing `password_hash` to a method that has no such parameter.

### Activate-without-lockout-reset escape hatch

A future caller may legitimately need to transition a user to `StatusActive` without resetting the lockout counters (e.g., administrative override that preserves the failure history for audit). This is **not expressible** at the `UpdateLockState` call site by design: the "auto-zero when active" invariant is encoded at the SQL layer (`CASE WHEN $2 = 'active' THEN 0 …`). Any caller needing this escape hatch must add a new narrow method (e.g., `UpdateStatusOnly`) paired with an ADR amendment and a corresponding archtest update to `USERREPO-METHOD-SET-FROZEN-01`. The current lock-at-schema-layer approach is the correct default because zero known callers need the escape hatch, and adding it prematurely would re-introduce the PR #585 P1#3 race as a latent footgun.

---

## 4. Threat Matrix (first version)

| Threat | Pre-refactor | Post-refactor | Evidence |
|--------|--------------|---------------|----------|
| Caller passes whole `*User` after modifying `password_hash` → silent password rotation through `Update` path | ⚠️ Soft — godoc warning only | ✅ Hard — `UpdateProfile` signature has no password parameter; only `*string` name/email accepted | §2 D1 (port signature) |
| Caller calls `Update` without resetting lockout on Activate → user re-locks on next failed login (PR #585 P1#3 race) | ⚠️ Soft — caller responsibility | ✅ Hard — `UpdateLockState(status=Active)` SQL `CASE WHEN` auto-zeros lockout counters atomically | §2 D2 (column-level invariant) |
| Caller passes empty-string name/email in PATCH → user record silently cleared | ⚠️ Soft — handler must check before constructing `*User` | ✅ Hard — `*string` + COALESCE; nil means "do not touch this column" | §2 D1 |
| Future maintainer re-introduces generic `Update(*User)` | ⚠️ Soft — godoc convention only | 🛡️ Medium archtest — `USERREPO-METHOD-SET-FROZEN-01` locks the 12-method set in CI | §2 D3 |
| In-memory shadow mutation drifts from DB write | ⚠️ Soft — `applyNonAuthzFields` + `repo.Update` two separate paths | ✅ Hard — `UpdateProfile` returns `*User` from persisted row; no shadow mutation path | §2 D4 |

**AI-rebust rating for USERREPO-METHOD-SET-FROZEN-01**: Medium (AST type-aware interface direct method set exact-match, same pattern as `CELL-IFACE-ISP-METHODSETS-01`). The caller-facing method-signature dimension is Hard — any regression to `Update(*User)` is a compile error because callers already use the narrow methods.

---

## 5. References

- `github.com/ory/kratos/identity/pool.go` — `UpdateIdentityColumns(ctx, identity, columns...)` (column-list escape hatch) + `UpdateTraits(ctx, id, traits)` (semantic narrow method)
- `github.com/keycloak/keycloak` UserModel — per-setter split (`setEnabled` / `setEmail`)
- `entgo.io/blog/2021/07/22/database-locking-techniques-with-ent` — version-in-Where fluent builder (considered and rejected, see §3)
- ADR `docs/architecture/202605211200-adr-pre-v1.0-direct-v1-evolution.md` — v1.0 GA 前 wire 契约直接演化, 无 deprecation
- PR #490 / PR #585 — prior narrow-method introductions (`UpdatePassword` + `UpdateLockoutFields`)
- `tools/archtest/cell_iface_isp_invariants_test.go` — sibling pattern `CELL-IFACE-ISP-METHODSETS-01` (parameterized `loadInterfaceType` helper)
- `tools/archtest/userrepo_method_set_frozen_test.go` — this ADR's enforcement artifact
