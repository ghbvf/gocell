# ADR: Operator control-plane authentication — AdminListener + operator-credential ListenerAuth

- **Status**: Accepted
- **Date**: 2026-06-04
- **Issue**: #1505
- **Supersedes (in part)**: `202605261620-adr-cqrs-projection-lifecycle-harness.md` §5
  (the projection rebuild forward contract — see that ADR's §Amendment 2026-06-04)

## 1. Context and scope

Control-plane / management HTTP endpoints (projection rebuild #1370; future saga
control, reconcile triggers) are **operator→system** actions: an administrator or
a deployment pipeline triggers them. They are *not* **cell→cell** business calls.

Before this ADR, GoCell had two control-plane shapes and neither fit:

1. **`/internal/v1/*` + `AuthServiceToken` + caller-cell allowlist** — the
   *cell→cell* control plane (`ContractSpec.Clients` names the caller cells;
   `auth.Mount` auto-injects `RequireCallerCell`). #1370's projection rebuild was
   mounted here, but an operator has no "caller cell"; using this model was a
   semantic coincidence.
2. **Route-level `auth.Route.BootstrapAuth`** (`runtime/auth.NewBootstrapMiddleware`)
   — operator HTTP Basic Auth over env credentials, but **per-route** and pinned
   by governance `FMT-28` to the per-cell `/api/v{N}/{cell}/setup/admin` path
   (first-admin bootstrap). Not reusable as a listener-scope control-plane gate.

The dominant benchmark for projection admin control planes — EventStoreDB's
projections HTTP admin API (`POST /projection/{name}/command/reset`) — uses a
**network-isolated admin port + operator (basic-auth) credentials**, not a
service-to-service caller allowlist. `kernel/cell/listener.go` already reserved a
"future **AdminListener**" extension point, and `kernel/auth.ListenerAuth` is a
sealed interface designed for exactly this kind of addition. This ADR builds that
reserved foundation and migrates #1370 onto it.

### In scope

1. `cell.AdminListener` — a sealed-enum listener class (loopback-isolated admin
   port, independent of public Primary and cell→cell Internal).
2. `auth.AuthOperator` — an operator-credential `ListenerAuth` implementation that
   wraps the existing `NewBootstrapMiddleware` (env credentials + per-IP rate
   limit + constant-time compare) at **listener** scope.
3. `/admin/v1/` path prefix + bidirectional admin-path ↔ AdminListener affinity.
4. Migration of #1370 projection rebuild onto the admin plane (no caller-cell
   allowlist).

### Out of scope

- Saga control / reconcile-trigger admin endpoints (future consumers of the same
  foundation).
- An operator *identity* model (RBAC over operators). The gate authenticates "the
  operator" via shared env credentials; per-operator identity is a future concern.
- mTLS layered under `AuthOperator` on the admin listener (the foundation does not
  forbid it, but v1 wires operator-credential-only; see §6).

## 2. Decision

### D1 — `cell.AdminListener` (sealed-enum extension)

Add `AdminListener = ListenerRef{"admin"}` to the closed `ListenerRef` set in
`kernel/cell/listener.go` (the file's godoc already named this as the reserved
extension). It is **optional** (declared only when an admin endpoint is wired —
like Internal/Webhook), bound to loopback by convention (`127.0.0.1:9093`
default). Loopback network isolation **plus** operator credentials form a
defense-in-depth pair.

### D2 — `auth.AuthOperator` (operator-credential ListenerAuth, kernel-projection idiom)

Add `kauth.AuthOperator` to the sealed `ListenerAuth` set. It mirrors
`AuthServiceToken`: the plan carries kernel-level dependencies and the bootstrap
apply-switch constructs the concrete `runtime/auth` middleware, so `kernel/auth`
holds no `net/http`.

`AuthOperator` holds raw operator credentials (`Username`/`Password []byte`), a
new kernel-projection rate-limiter interface `OperatorRateLimiter` (mirrors
`runtime/auth.BootstrapRateLimiter`, the same idiom `auth_types.go` uses for
`NonceStore`/`HMACKeyring`), and an optional observer func. The apply-switch maps
these into the **unchanged** `runtime/auth.NewBootstrapMiddleware` by structural
interface/func assignment — so the three `runtime/auth` bootstrap types and every
existing setup/admin consumer are untouched, and the two bootstrap-auth mechanisms
(route-level `Route.BootstrapAuth`/FMT-28 vs listener-level `NewBootstrapMiddleware`)
are **not** conflated.

`NewAuthOperator(username, password, limiter, onAuthFail)` rejects empty
credentials and a nil limiter (an operator gate with empty credentials would
authenticate every request; one without a rate limiter cannot defeat
brute-force).

### D3 — `/admin/v1/` prefix + bidirectional affinity

`metadata.IsAdminHTTPPath` (+ `cellvocab.AdminPathPrefix` + `AuthRouteMeta.IsAdmin`)
classify the admin prefix. The router's listener-route affinity check
(`verifyInternalRouteAffinity` → `verifyListenerRouteAffinity`) is generalized to
enforce, at startup, both directions for admin (mirroring internal):

- an admin path (`/admin/v1/*`) must be mounted on `AdminListener`, and
- `AdminListener` must carry only admin paths.

This closes a real gap: before #1505 an admin-shaped path mounted on the **primary**
listener passed both internal-affinity branches (it is not `/internal/v1/*`), so
nothing caught it.

Operator-plane invariants are enforced at bootstrap phase0
(`validateAuthOperatorPlans`): `AuthOperator` only on `AdminListener`,
`AdminListener` must carry an `AuthOperator` (loopback isolation alone is not
sufficient), at most one per chain, and struct-literal dep guards.

### D4 — Migrate #1370 projection rebuild

`POST /internal/v1/{cell}/projection/{name}/rebuild` (service-token + caller-cell
allowlist, InternalListener) → `POST /admin/v1/projection/{cell}/{name}/rebuild`
(AuthOperator, AdminListener, **no caller-cell allowlist**). `NewFrameworkHTTP` is
called with no clients (an `/admin/v1/*` path is an ordinary non-internal path;
`validateHTTP` forbids `Clients` on non-internal paths, so the empty set is
correct). The admit-time audit log drops the `caller_cell` field (operator auth
has no caller cell). See the projection ADR §Amendment 2026-06-04 for the
unchanged surfaces (202/409/404, envelope, framework-owned RouteGroup mechanism).

### D5 — Operator credentials = new `GOCELL_OPERATOR_ADMIN_*` env

The admin-listener operator gate reads `GOCELL_OPERATOR_ADMIN_USERNAME` /
`GOCELL_OPERATOR_ADMIN_PASSWORD` — **separate** from the per-cell setup/admin
`GOCELL_BOOTSTRAP_ADMIN_*` (first-admin bootstrap). Different lifecycle, rotation,
and leak-surface; separation of concerns. In the todoorder demo the admin plane is
wired only when these env vars are present, so the demo still starts out of the box
(mirrors the readyz-verbose opt-in).

## 3. AI-robust ratings

The two new constructs **inherit** existing Hard mechanisms; the only genuinely
new enforcement is the admin-path↔AdminListener affinity (Medium runtime + archtest),
consistent with the established internal-affinity precedent.

| Mechanism | Rating | Notes |
|---|---|---|
| `AuthOperator` is a `ListenerAuth` | **Hard (type system, inherited)** | sealed `listenerAuthOK()` marker — external packages cannot implement `ListenerAuth`. |
| `AdminListener` is a `ListenerRef` | **Hard (type system, inherited)** | unexported `name` field — external packages cannot mint a `ListenerRef`. |
| admin-path ↔ AdminListener affinity | **Medium (runtime guard + archtest)** | `verifyListenerRouteAffinity` fails fast at startup, both directions; router unit tests mirror the internal-affinity cases. Same shape as the existing internal-route affinity (no Soft mechanism introduced). |
| operator-only-on-Admin / Admin-requires-operator | **Medium (phase0 runtime guard)** | `validateAuthOperatorPlans`. Same family as `validateAuthServiceTokenPlans`. |

**No new governance FMT rule.** The migrated endpoint is a framework-owned
RouteGroup with **no `contract.yaml`**, so the FMT governance rules (which scan
`contract.yaml`) never apply to it; the repo has no path-prefix registry or
listener fan-out golden a new admin-path rule would slot into. Adding a speculative
FMT rule for admin-path *contracts* (none exist) would outrun the codebase. If a
cell ever declares an `/admin/v1/*` contract, that is a separate concern.

## 4. Threat matrix

| # | Threat | Mechanism | Rating |
|---|---|---|---|
| 1 | **Unauthenticated operator access** to the admin plane | `AuthOperator` HTTP Basic Auth (env credentials) + loopback bind (defense in depth); `AdminListener`-requires-`AuthOperator` phase0 guard rejects an admin listener with no operator gate | Hard (sealed plan) + Medium (phase0) |
| 2 | **Operator credential brute-force** | per-IP token-bucket rate limiter (required by `NewAuthOperator`; nil limiter rejected at construction) + constant-time compare (`subtle.ConstantTimeCompare`) + uniform 401 (no username/password oracle) | Medium |
| 3 | **Admin endpoint leaks onto the public listener** | bidirectional `verifyListenerRouteAffinity`: an admin path on a non-admin listener fails fast; the primary listener already 404s non-primary control-plane prefixes (port-level isolation) | Medium |
| 4 | **Operator credentials accepted on the wrong (public/internal) listener** | `AuthOperator`-only-on-`AdminListener` phase0 guard | Medium |
| 5 | **Credential leak via logs/spans** | the operator credentials live in the `AuthOperator` plan and the request `Authorization: Basic` header; the framework's fail-closed slog/span redaction masks `authorization`/`bearer`/`password` keys. The admit-time audit log records only `cell`/`projection` + correlation IDs (no credentials, no caller cell) | Medium (inherited redaction) |
| 6 | **Cross-tenant / cross-cell rebuild via stolen caller identity** | not applicable to the admin plane — there is no caller-cell identity to forge; operator authority is all-or-nothing behind the credential gate. (This is the deliberate trade vs. the prior cell→cell model: an operator is trusted for the whole admin plane.) | n/a by design |

**Residual / accepted.** Operator authority is coarse (one shared credential pair
gates the whole admin plane). Per-operator identity/RBAC is out of scope (§1) and
a future concern; the loopback bind bounds the reachable surface in the interim.

## 5. Consequences

- A reserved kernel extension point (`AdminListener`) and a reserved auth shape
  (operator-credential `ListenerAuth`) are now load-bearing, with one real
  consumer (projection rebuild). Future operator endpoints (saga control,
  reconcile triggers) reuse the same foundation with no new geography.
- The kernel-projection idiom keeps the change small: one new kernel interface +
  one new plan, zero churn to `runtime/auth` or the setup/admin consumers.
- Operators must provision `GOCELL_OPERATOR_ADMIN_*` to expose the admin plane;
  absent them, projection rebuild stays programmatic-only (`Coordinator.Rebuild`).

## 6. Alternatives rejected

- **Keep projection rebuild on `/internal/v1/*` (cell→cell)** — semantically wrong
  (operator is not a caller cell) and forces a fake caller-cell allowlist entry.
- **`AuthOperator` holds a pre-built `func(http.Handler) http.Handler`** — would
  drag `net/http` into `kernel/auth` and break the "plan = pure data, runtime
  builds middleware" pattern that every other `ListenerAuth` follows.
- **Physically move `BootstrapCredentials`/`BootstrapRateLimiter`/`BootstrapAuthFailObserver`
  into `kernel/auth`** — a ~12-file blast radius (incl. integration tests) for no
  gain over the kernel-projection idiom already established by `auth_types.go`.
- **A new FMT governance rule for admin paths** — vacuous (no admin `contract.yaml`
  exists) and outruns the codebase (§3).
- **Reuse `GOCELL_BOOTSTRAP_ADMIN_*` for the operator gate** — couples first-admin
  bootstrap and the ongoing operator control-plane to one credential (§D5).

## 7. References

- `kernel/cell/listener.go` (`AdminListener`), `kernel/auth/operator.go`
  (`AuthOperator` / `OperatorRateLimiter` / `NewAuthOperator`).
- `runtime/bootstrap/auth_plan_apply.go` (apply-switch),
  `auth_plan_validate.go` (`validateAuthOperatorPlans`),
  `projection_rebuild.go` (migrated endpoint).
- `runtime/http/router/router.go` (`verifyListenerRouteAffinity`).
- `docs/ops/listener-topology.md` (AdminListener topology).
- Projection lifecycle ADR `202605261620-...` §5 + §Amendment 2026-06-04.
- EventStoreDB projections HTTP admin API (admin-cred + network isolation benchmark).
