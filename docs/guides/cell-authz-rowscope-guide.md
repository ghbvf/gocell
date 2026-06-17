# Cell Authorization and RowScope Guide

This guide is the consumer-facing checklist for wiring a business endpoint into
GoCell's permission-based authorization path. ADRs explain why the model exists;
this page explains what to change.

## Endpoint Checklist

For each non-public business endpoint:

1. Declare a sealed permission accessor in `framework/pkg/authz/permission.go`.
   Permissions use resource/action strings such as `config:write`; callers use
   accessor functions such as `authz.PermConfigWrite()`, never exported vars or
   raw strings.
2. Attach the generated handler to the PDP route gate:
   `auth.RequirePermission(authz.PermXxx())`.
3. For owner/self endpoints, use
   `auth.RequirePermissionForResource("pathParam", authz.PermXxx())`. It
   canonicalizes the path parameter and forwards it as `resource.id` for PDP
   ownership rules. Do not replace it with plain `RequirePermission`.
4. Register the baseline grant in
   `corecells/accesscore/slices/authorizationdecide/baseline.go` when the
   endpoint should be available to the built-in admin/super-admin or ownership
   model. A missing baseline means the endpoint is default-deny unless tenant
   policy grants it.
5. Wire the composition root so the primary listener gets an Authorizer:
   `bootstrap.WithPrimaryAuthorizer(authorizer)` or
   `bootstrap.PrimaryAuthorizerOption(cells)`. Without this, the route gate
   fails closed.
6. Add contract/slice coverage for 403 and for the exact permission action passed
   to the Authorizer. Add e2e coverage when the endpoint depends on baseline
   registration or composition-root wiring.

## Route Gate vs Data Boundary

`auth.RequirePermission` is a coarse allow/deny gate. It does not enforce
obligations such as RowScope or FieldMask. If a permit carries a non-zero
obligation that the route gate cannot discharge, the route gate denies rather
than silently dropping it.

Data visibility must be enforced where data is read:

- Read/list data PEPs derive `tenant.RowVisibility` from the principal and apply
  it to the repository query. For audit reads, a tenant policy may widen the
  route gate, but it cannot widen the principal-derived RowScope.
- Owner/self route grants only allow the caller through the gate. The data layer
  still needs its own tenant/RowVisibility checks.
- Write endpoints generally have no RowScope dimension. Their isolation boundary
  is the typed tenant axis, ctx tenant propagation, and PostgreSQL FORCE RLS.

## RowScope Values

`tenant.RowScope` has four non-zero values:

| Scope | Meaning | Enforcement location |
|-------|---------|----------------------|
| `self` | Caller sees rows whose owner/actor matches the subject. | Data PEP predicate |
| `device` | Device-scoped variant of self-style visibility. | Data PEP predicate |
| `tenant` | Caller sees rows in the current tenant. | Typed tenant + RLS + data predicate |
| `all` | Cross-tenant visibility for explicit admin paths. | Dedicated audited path; serving pools fail closed where unsupported |

Zero RowScope is invalid as principal visibility. Inside `authz.Obligations`,
zero RowScope means only "this policy did not impose a row-scope obligation."
The current evaluator merges RowScope only among matching policy permits; data
PEPs must not assume policy obligations have already been merged with the
principal-derived RowScope.

## Common Failure Modes

| Failure | Symptom | Fix |
|---------|---------|-----|
| Permission minted but no baseline rule | Admin receives 403 from default-deny. | Add the matching baseline rule or document tenant-policy-only access. |
| Handler uses `auth.AnyRole` | Authorization bypasses the PDP funnel and role-literal governance fails. | Use `auth.RequirePermission` or `auth.RequirePermissionForResource`. |
| Owner endpoint uses plain `RequirePermission` | PDP sees the URL path instead of canonical `resource.id`; ownership rule does not match. | Use `RequirePermissionForResource` with the path-param name. |
| Composition root omits the Authorizer | Every permission-gated request fails closed. | Install `WithPrimaryAuthorizer` / `PrimaryAuthorizerOption`. |
| Route gate assumed to enforce RowScope | A future data PEP may apply a policy-only wider scope. | Merge/enforce principal-derived RowVisibility at the data boundary. |

## Examples

Positive examples:

- `corecells/configcore/slices/configwrite/handler.go` wires
  `auth.RequirePermission(authz.PermConfigWrite())`.
- `corecells/accesscore/slices/identitymanage/handler.go` uses
  `auth.RequirePermissionForResource("id", authz.PermUserRead())` for owner
  reads.
- `cmd/corebundle/run.go` wires the primary Authorizer for the bundled
  accesscore/configcore/auditcore assembly.

Primary rationale:

- Cedar and XACML both keep deny/default-deny and obligation handling explicit.
- Spring Security role hierarchy is explicit configuration; GoCell likewise uses
  explicit baseline rules rather than implicit role inheritance.
