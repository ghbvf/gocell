// Package credentialfence is the sealed source of the credential-invalidation
// FenceToken — a capability proof that authorizes calls to the three credential
// mutation methods (session.Store.RevokeForSubject /
// refresh.Store.RevokeUser / ports.UserRepository.BumpAuthzEpoch).
//
// # Why
//
// Before #1033 the credential-invalidate funnel was half-closed:
//
//   - DOWNSTREAM Hard — the three mutation methods' caller allowlist is locked
//     by archtests CREDENTIAL-INVALIDATE-FUNNEL-01 / USER-AUTHZ-EPOCH-BUMP-FUNNEL-01
//     / REFRESH-REVOKE-USER-FUNNEL-01 (ResolveMethodCall form-uniqueness).
//   - UPSTREAM Soft — any package could construct the receivers and call the
//     methods directly. Only CI-time archtest catches it; Go's type system did
//     not.
//
// 042 §3a row 3 and §4 ROI row 2 flagged this as the single remaining
// structural opening in the revocation safety model. This package closes it:
// the three mutation methods now require a [FenceToken] argument that only
// [Mint] can produce.
//
// # How
//
// [FenceToken] is a sealed interface — the unexported isCredentialFenceToken
// marker method makes external implementations impossible at compile time
// (Go visibility rule: only types declared in this package can implement an
// interface with an unexported method). The concrete impl [fenceToken] is
// unexported, so [fenceToken{}] cannot be constructed from outside the
// package either.
//
// [Mint] is the sole constructor. Its callers are restricted by archtest
// FENCE-TOKEN-MINT-FUNNEL-01 to:
//
//   - corecells/accesscore/internal/credentialinvalidate/ — the production funnel
//   - runtime/auth/session/storetest/ + runtime/auth/refresh/storetest/ +
//     corecells/accesscore/internal/ports/conformance/ — conformance suites that
//     must exercise the contract end-to-end
//   - *_test.go files in any package — unit tests for impls and stubs
//
// Any other caller is detected as a form-uniqueness violation and fails CI.
//
// # AI-robust evaluation
//
// Three mechanisms with distinct enforcement layers — do not collapse them
// into one "Hard":
//
//   - Construction seal — Hard, compile-time (Go type system). FenceToken is
//     unimplementable outside this package (unexported marker method) and the
//     concrete [fenceToken] is unexported, so no external composite literal can
//     build one. This is the only compile-time guarantee here; it forces every
//     external would-be revoker to obtain a token from [Mint].
//   - Mint callsite funnel — Hard via call-site form-uniqueness, archtest-bound
//     (FENCE-TOKEN-MINT-FUNNEL-01), NOT compile-time. The scanner is
//     form-complete: it flags every reference to Mint (direct call,
//     function-value capture, return, pass-through arg, reflect arg) via a
//     ResolvePackageRef SelectorExpr walk, so there is no per-form blindspot
//     self-check to drift. Go cannot express "only package X may call Mint", so
//     this is the Hard ceiling the rule's shape can reach (the
//     PANIC-REGISTERED-01 precedent).
//   - nil-FenceToken — runtime backstop, NOT a static grade. A literal nil
//     argument compiles and is invisible to the two static mechanisms above;
//     [MustHave] in each mutation impl converts it to an immediate panic
//     (pkg/validation.IsNilInterface → errcode.Assertion via
//     panicregister.Approved → 500). Defense-in-depth; it does not elevate any
//     static grade.
//
// Combined grade: funnel upstream Hard, downstream Hard — resting on the
// construction seal (compile-time) plus the two form-complete archtest funnels.
// The nil guard is a runtime backstop, not part of the static claim. See
// .claude/rules/gocell/ai-robust.md §"Hard 范本目录" entry "typed marker funnel
// for unbounded ops" and §"Funnel 双向锁评级".
//
// # Reference precedent
//
//   - kernel/outbox/cell_marker.go (CellPublisher / CellEmitter sealed
//     marker pattern)
//   - kernel/persistence/cell_marker.go (CellTxManager sealed marker pattern)
//   - pkg/panicregister/panicregister.go (typed marker funnel +
//     PANIC-REGISTERED-01 archtest form-uniqueness)
//
// # Audit / plan references
//
//   - docs/reviews/202605181109-042-archtest-six-agent-audit.md §3a row 3,
//     §4 ROI row 2
//   - docs/plans/202605191100-043-archtest-audit-and-capability-gap-parallel-plan.md
//     §1 row 4
//   - docs/architecture/202605101400-adr-credential-session-protocol.md §A16 (credential-invalidate sealed FenceToken — 上游 funnel 闭环)
//
// Open-source comparison: HashiCorp Vault audit broker sealed token,
// crypto/tls.Config sealed fields.
package credentialfence
