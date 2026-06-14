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

Callers depended on godoc convention ("Do NOT call this for password changes") to avoid field bleed. From an AI-robust viewpoint this is **Soft** enforcement: a future maintainer can inadvertently pass a `*domain.User` with a modified `password_hash` via the `Update` path and silently rotate credentials without triggering any credential-invalidation event.

### Prior narrow method introductions

- **S6** (PR introducing `UpdatePassword`): first narrow method, CAS-guarded password change path with explicit `password_version` parameter
- **PR #585 P1#3**: identified admin-unlock re-lock race — admin activating a locked user did not zero the lockout counters because the `Update` caller held responsibility for that cleanup (Soft)

### AI-robust motivation

`.claude/rules/gocell/ai-robust.md` §"Soft → Hard 改造方向":

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
| 4 | `UpdateProfile` returns `(*domain.User, error)` | Single round-trip: PG `RETURNING <explicit user columns>` reconstitutes the aggregate; mem writes in-place then calls `ReconstituteUser`. Caller uses the returned aggregate for downstream publish / audit, eliminating shadow-mutation drift |
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
| Caller passes empty-string name/email in PATCH → user record silently cleared | ⚠️ Soft — handler must check before constructing `*User` | ✅ Hard — `*domain.NonEmpty` typed wrapper; `NewNonEmpty("")` and `UnmarshalJSON("")` reject empty at the type boundary; package-external callers cannot construct an empty `NonEmpty` value (Go type-rename + funnel constructor). Direct cast `domain.NonEmpty("")` is permitted by Go but ban'd outside funnel allowlist by `USERREPO-NONEMPTY-CAST-FUNNEL-01` archtest (downstream Hard). | §2 D1 + `cells/accesscore/internal/domain/nonempty.go` |
| Future maintainer re-introduces generic `Update(*User)` | ⚠️ Soft — godoc convention only | 🛡️ Medium archtest — `USERREPO-METHOD-SET-FROZEN-01` locks the 12-method set in CI | §2 D3 |
| In-memory shadow mutation drifts from DB write | ⚠️ Soft — `applyNonAuthzFields` + `repo.Update` two separate paths | ✅ Hard — `UpdateProfile` returns `*User` from persisted row; no shadow mutation path | §2 D4 |

**AI-robust rating for USERREPO-METHOD-SET-FROZEN-01**: Medium (AST type-aware interface direct method set exact-match, same pattern as `CELL-IFACE-ISP-METHODSETS-01`). The caller-facing method-signature dimension is Hard — any regression to `Update(*User)` is a compile error because callers already use the narrow methods.

---

## 5. References

- `github.com/ory/kratos/identity/pool.go` — `UpdateIdentityColumns(ctx, identity, columns...)` (column-list escape hatch) + `UpdateTraits(ctx, id, traits)` (semantic narrow method)
- `github.com/keycloak/keycloak` UserModel — per-setter split (`setEnabled` / `setEmail`)
- `entgo.io/blog/2021/07/22/database-locking-techniques-with-ent` — version-in-Where fluent builder (considered and rejected, see §3)
- ADR `docs/architecture/202605211200-adr-pre-v1.0-direct-v1-evolution.md` — v1.0 GA 前 wire 契约直接演化, 无 deprecation
- PR #490 / PR #585 — prior narrow-method introductions (`UpdatePassword` + `UpdateLockoutFields`)
- `tools/archtest/cell_iface_isp_invariants_test.go` — sibling pattern `CELL-IFACE-ISP-METHODSETS-01` (parameterized `loadInterfaceType` helper)
- `tools/archtest/userrepo_method_set_frozen_test.go` — this ADR's enforcement artifact

---

## §Amendment 2026-06-03 — PR-2a tenant positional param (#1481 / #1340)

**EPIC**: #1337 multi-tenancy, PR-2a accesscore repo isolation.

### Changes to the method set

Every `UserRepository` method now carries a mandatory `tenant.TenantID` positional
parameter immediately after `ctx` — leaking tenant to the type system makes
"calling a write method without a tenant" a compile error (Hard, type system).

**Single carve-out retained**: `GetByID(ctx, id)` remains tenant-less. Its sole
caller (`sessionrefresh`) holds no pre-auth tenant source until PR-3 lands the
refresh-token tenant carrier + PG RLS `SET LOCAL app.tenant_id`. Tenant isolation
for this path is enforced at the DB layer by PR-3 RLS. The carve-out is
allowlisted by archtest `TENANT-REPO-CALLSITE-FUNNEL-01`.

**New method added**: `GetByIDInTenant(ctx, t, id)` — fetches a user by primary
key and asserts it belongs to tenant `t`. Returns `ErrAuthUserNotFound` for both
absent rows and cross-tenant rows (no existence leak). Used on admin / post-auth
paths (`identitymanage` user-detail, `lockUserAndRevokeSessions`) that already
hold a tenant context; those paths must NOT use the tenant-less `GetByID` carve-out.

### Updated method set (13 methods)

```
BumpAuthzEpoch(ctx, t, userID, tok)
Create(ctx, t, user)
Delete(ctx, t, id)
GetByID(ctx, id)                       ← tenant-less carve-out (unchanged)
GetByIDForUpdate(ctx, t, id)
GetByIDInTenant(ctx, t, id)            ← NEW
GetByUsername(ctx, t, username)
GetByUsernameForUpdate(ctx, t, username)
UpdateLockState(ctx, t, userID, status, now)
UpdateLockoutFields(ctx, t, user)
UpdatePassword(ctx, t, userID, newHash, resetRequired, expectedPasswordVersion)
UpdatePasswordResetFlag(ctx, t, userID, required, now)
UpdateProfile(ctx, t, userID, name, email, now)
```

### Threat matrix re-evaluation

| Threat (original §4) | Post-amendment | Notes |
|---|---|---|
| Silent password rotation via generic `Update` | ✅ Hard — unchanged | Narrow methods still hold |
| Activate-without-lockout-reset race | ✅ Hard — unchanged | `UpdateLockState` SQL CASE WHEN unchanged |
| Empty-string name/email in PATCH | ✅ Hard — unchanged | `*domain.NonEmpty` funnel unchanged |
| Generic `Update(*User)` regression | 🛡️ Medium archtest — unchanged | `USERREPO-METHOD-SET-FROZEN-01` golden updated to 13 methods |
| In-memory shadow mutation drift | ✅ Hard — unchanged | `UpdateProfile` returns `*User` from persisted row |
| **NEW**: Cross-tenant data read via username/write methods | ✅ Hard — typed `tenant.TenantID` position param; omitting it = compile error; `GetByID` carve-out allowlisted by `TENANT-REPO-CALLSITE-FUNNEL-01` | PR-2a enforcement; RLS backstop in PR-3 |
| **NEW**: Admin path using tenant-less `GetByID` to leak existence | ✅ Hard — `GetByIDInTenant` added for admin paths; `TENANT-REPO-CALLSITE-FUNNEL-01` archtest blocks new callers of `GetByID` outside allowlist | Existence leak closed at interface layer |

### Enforcement artifacts updated in this PR

- `tools/archtest/userrepo_method_set_frozen_test.go` — `expectedUserRepoMethodSignatures` golden updated from 12 to 13 entries; all methods gained `t tenant.TenantID` param except `GetByID`; `GetByIDInTenant` added.
- `tools/archtest/tenant_repo_param_funnel_test.go` — new archtest `TENANT-REPO-PARAM-FUNNEL-01` + `TENANT-REPO-CALLSITE-FUNNEL-01` (see `.claude/rules/gocell/tenancy.md`).

### Sessions table / session.Store

Session repo `tenant.TenantID` typed param and refresh-token tenant carrier are
**deferred to PR-3** (refresh path has no pre-auth tenant source until PR-3 RLS
`SET LOCAL app.tenant_id` lands). The `sessionrefresh` / `sessionvalidate`
callers of `GetByID` are therefore still correct for this PR.

## §Amendment 2026-06-06 — PR-3b: tenant-less `GetByID` DELETED (#1617)

**EPIC**: #1337 multi-tenancy, PR-3b accesscore RLS + auth-path tenant wiring.

PR-3b places `users` / `roles` / `role_assignments` under PG `FORCE ROW LEVEL
SECURITY`. A tenant-less `GetByID(ctx, id)` (`WHERE id = $1`, no tenant predicate)
then fails-closed to 0 rows under a restricted role, so the carve-out can no longer
function. The PR-2a amendment's prediction ("RLS backstop in PR-3") is realised by
**deleting the method**, not by an RLS-protected tenant-less read.

### Changes to the method set

- **`GetByID(ctx, id)` REMOVED.** All former callers now derive the tenant and use
  `GetByIDInTenant(ctx, t, id)` under a tenant scope:
  - `sessionrefresh` / `sessionvalidate` — tenant derived from the **session row**
    (`sessions.tenant_id` carrier, migration 054; the composite FK
    `(tenant_id, subject_id) → users(tenant_id, id)` makes that carrier DB-Hard
    trustworthy), not from a tenant-less by-PK read.
  - `rbacassign` / role-revoke — tenant supplied by the service-token caller via
    the internal request contract (`tenantId`).
- Net method set: **13 → 12** (remove `GetByID`; every remaining `UserRepository`
  method now carries `tenant.TenantID`).

### Updated method set (12 methods)

```
BumpAuthzEpoch(ctx, t, userID, tok)
Create(ctx, t, user)
Delete(ctx, t, id)
GetByIDForUpdate(ctx, t, id)
GetByIDInTenant(ctx, t, id)            ← the sole by-PK read (tenant-typed)
GetByUsername(ctx, t, username)
GetByUsernameForUpdate(ctx, t, username)
UpdateLockState(ctx, t, userID, status, now)
UpdateLockoutFields(ctx, t, user)
UpdatePassword(ctx, t, userID, newHash, resetRequired, expectedPasswordVersion)
UpdatePasswordResetFlag(ctx, t, userID, required, now)
UpdateProfile(ctx, t, userID, name, email, now)
```

### Threat matrix re-evaluation (per ai-robust.md §"ADR amendment 落地必查")

The two PR-2a `GetByID`-carve-out rows are upgraded — the residual Medium archtest
caller-allowlist becomes a compile-time Hard because the method no longer exists:

| Threat | PR-2a state | PR-3b state | Notes |
|---|---|---|---|
| Cross-tenant data read via a tenant-less read | ✅ Hard write methods + ⚠️ `GetByID` carve-out allowlisted by `TENANT-REPO-CALLSITE-FUNNEL-01` (Medium) | ✅ **Hard, fully** — `GetByID` deleted; **every** `UserRepository` method takes `tenant.TenantID` → omitting it is a compile error; no tenant-less escape hatch | carve-out closed at the type level |
| Admin/new path using tenant-less `GetByID` to leak existence | ✅ `GetByIDInTenant` added; new `GetByID` callers blocked by `TENANT-REPO-CALLSITE-FUNNEL-01` (Medium archtest) | ✅ **Hard** — the method is gone; there is nothing to call. `TENANT-REPO-CALLSITE-FUNNEL-01` **retired** (no subject left to fence) | upstream direction now compiler-Hard |
| Generic `Update(*User)` regression | 🛡️ Medium archtest (golden 13) | 🛡️ Medium archtest (golden **12**) | `USERREPO-METHOD-SET-FROZEN-01` golden − `GetByID` |

No row regresses (no ✅→⚠️/❌); both `GetByID`-carve-out rows strengthen
Medium→Hard. The DB-layer FORCE RLS (migration 053) is an additional independent
backstop, runtime-active once the restricted app-serving pool lands (backlog #1676).

### Enforcement artifacts updated in this PR

- `tools/archtest/userrepo_method_set_frozen_test.go` — golden `−GetByID` (13 → 12).
- `tools/archtest/tenant_repo_param_funnel_test.go` — `GetByID` carve-out removed from
  `TENANT-REPO-PARAM-FUNNEL-01`; **`TENANT-REPO-CALLSITE-FUNNEL-01` retired** (with an
  in-file retirement note).
- New mid-tx scope primitive `CellTxManager.ApplyTenantScope` + single accesscore
  WithScope funnel `cells/accesscore/internal/scopedtx` (TENANT-TXSCOPE-WRITE-CALLER-01
  allowlist +1). See `.claude/rules/gocell/tenancy.md` PR-3b row.

## Amendment 2026-06-15 (#1709): `GetByIDInTenant` row-visibility read obligation

`UserRepository.GetByIDInTenant` gains a `tenant.RowVisibility` obligation positional
parameter at param[2] (`GetByIDInTenant(ctx, t tenant.TenantID, vis tenant.RowVisibility,
id string)`), enrolling `UserRepository` (and `RoleRepository.GetByUserID` /
`ListByUserID`) in `ROWSCOPE-REPO-PARAM-FUNNEL-01`. This is triggered by #1977, which
introduced the PDP-ownership subject-self read endpoints (`GET /api/v1/access/users/{id}`,
`GET /api/v1/access/roles/{userID}`): the obligation is now load-bearing (a non-admin who
passes the coarse route gate is scoped to their own rows by the principal-derived
`RowScope`), no longer the permanently-dead param that justified deferral in #1342.

### Threat matrix re-evaluation (per ai-robust.md §"ADR amendment 落地必查")

This ADR's scope is the **write-side** narrow-method contract; the #1709 change is a
**read-side** owner-dimension obligation, orthogonal to write-column narrowing. No write
threat row changes.

| Threat | Pre-#1709 | Post-#1709 | Notes |
|---|---|---|---|
| Over-broad accesscore read when a tenant policy widens the coarse route gate (non-admin granted tenant-wide `user:read`/`role:read`) | ⚠️ no data-layer RowScope on accesscore reads — route gate sole control (D3 promised but unimplemented) | ✅ principal-derived `RowScope` collapses non-self rows at the repo PEP (IDOR-safe NotFound/empty); RowScopeAll fail-closes 501 | closes the accesscore D3 gap; auditcore already had it |
| Generic `Update(*User)` regression (this ADR's subject) | 🛡️ Medium archtest (golden 12) | 🛡️ Medium archtest (golden 12, unchanged) | method set unchanged — only one signature widened |

Method set is **unchanged** (still 12 methods); only `GetByIDInTenant`'s signature
widened. No write-side row regresses.

### Enforcement artifacts updated

- `tools/archtest/userrepo_method_set_frozen_test.go` — `GetByIDInTenant` frozen
  signature updated to carry `vis tenant.RowVisibility` at param[2] (golden count 12,
  unchanged).
- `tools/archtest/rowscope_repo_param_funnel_test.go` — `UserRepository` +
  `RoleRepository` enrolled (`ROWSCOPE-REPO-PARAM-FUNNEL-01`); 18 non-read methods
  carved out with per-method rationale. See `.claude/rules/gocell/tenancy.md` D3.
