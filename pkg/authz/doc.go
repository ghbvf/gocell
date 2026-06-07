// Package authz is the framework-level **authorization decision vocabulary**
// (the PDP contract) returned by auth.Authorizer. It is reusable by any cell.
//
// # XACML PDP/PEP/PIP/PAP Role Mapping
//
//   - PDP contract    = auth.Authorizer + pkg/authz (Decision / Effect / Obligations)
//   - PEP             = repos / handlers that enforce the verdict (check IsAllow(),
//     apply Obligations.RowScope, mask columns per Obligations.FieldMask)
//   - PIP             = JWT claims / repos / clock that supply attribute values to
//     the policy engine
//   - PAP             = policymanage cell (policy CRUD, PR-10)
//
// # Layering
//
// pkg/authz may import ONLY pkg/* (pkg/tenant, pkg/errcode) and the standard
// library. It must NOT import kernel/, runtime/, cells/, or adapters/.
//
// # Sealed Decision — P0 Bypass Prevention
//
// Decision is the sealed authorization verdict. All fields are unexported: the
// ONLY way to produce a Decision is via Allow() or Deny(). A business handler
// cannot forge `authz.Decision{Effect: authz.EffectAllow}` because that
// struct literal is a compile error outside this package. This seals the P0
// authz-bypass vector (allowing access without an actual policy evaluation).
//
// The zero-value Decision{} has Effect() that does NOT equal EffectAllow
// (fail-closed), so any consumer testing d.IsAllow() fails closed on an
// uninitialized Decision.
//
// FR-019: a Decision never crosses an async boundary. With no exported fields
// and no Unmarshal capability it is structurally non-serializable, reinforcing
// that invariant.
//
// # Deferred PR-7 Downstream Caller-Allowlist
//
// The downstream funnel half — a caller-allowlist restricting Allow()/Deny() to
// only the PDP evaluation engine (authorizationdecide) — lands in PR-7
// (tracking issue: gh #1345) when a production producer first exists; today
// there is no production caller so an allowlist would be vacuous. The upstream
// Hard (sealed fields → literal forge impossible) is achieved here.
//
// Per ai-robust.md §"Funnel 双向锁评级": a Medium-upstream → Hard-downstream
// transitional funnel must name its Hard-ization tracker. That tracker is
// gh #1345 (PR-7). Once PR-7 ships a production Allow()/Deny() caller, the
// companion archtest in tools/archtest/authz_decision_sealed_test.go must be
// extended with an AST caller-allowlist guard (only the authorizationdecide
// engine may call Allow() or Deny()).
//
// # Combining Algorithm
//
// Deny-overrides (XACML §7.16) / forbid-wins (Cedar): when multiple policies
// apply to a request, any Deny result causes the combined decision to be Deny,
// regardless of how many Allow results are also present. This semantics is
// DOCUMENTED here but the ENFORCEMENT (an evaluator that applies deny-overrides)
// is PR-7, not this package.
//
// # References
//
//   - ref: cedar-policy/cedar-go types.go (Decision 2-value enum pattern)
//   - ref: cedar-policy/cedar-go authorize.go (Decision+Diagnostic projection)
//   - ref: XACML-3.0 §3.5 Obligations (mandatory enforcement obligations)
//   - ref: XACML-3.0 §7.16 deny-overrides combining algorithm
package authz
