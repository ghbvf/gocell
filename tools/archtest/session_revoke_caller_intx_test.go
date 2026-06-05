// session_revoke_caller_intx_test.go — closes the DOWNSTREAM side of the
// session.Store.Revoke caller funnel.
//
//   - INVARIANT: SESSION-REVOKE-CALLER-INTX-01
//
// # What this guards
//
// adapters/redis.CachingSessionStore.Revoke registers a post-commit cache DEL
// via persistence.RegisterAfterCommit, which PANICS if called outside an
// ambient RunInTx scope (the after-commit registry lives in the ctx that
// RunInTx installs). Every current production reference to (session.Store).Revoke
// executes inside a RunInTx closure, so the registry is always present:
// sessionlogout (the logout flow) and sessionlogin's failure-compensation paths
// both call it from within a txRunner.RunInTx closure. Crucially this holds even
// in demo/noop mode — DemoTxRunner.RunInTx (kernel/outbox/demo_tx_runner.go) also
// installs the registry (WithAfterCommitRegistry + RunAfterCommitHooks), so being
// inside RunInTx — not the store's concrete type — is the safety guarantee.
//
// This archtest moves the "a future caller forgets the RunInTx scope" risk from a
// runtime panic to a CI failure. It allowlists every production reference to the
// interface method (session.Store).Revoke; any new reference outside the allowlist
// fails CI until a reviewer verifies the new caller is RunInTx-wrapped and adds it.
//
// Note on tx verification: the human-verified fact is that each allowlisted
// caller wraps the Revoke call in a RunInTx scope.  go/types CANNOT prove
// ambient-tx invariants — the call to revokeAndPublish sits inside a RunInTx
// closure, but is NOT lexically inside it; a containment check would not
// survive a multi-level function-call graph. The archtest's value is
// therefore a review checkpoint, not a proof: a new caller is forced to the
// allowlist where a reviewer MUST verify tx-wrapping before adding it.
//
// # AI-robust rating (charter §"Funnel 双向锁评级")
//
//   - Downstream: MEDIUM by archtest caller-allowlist. The callee is resolved
//     via go/types (ResolveMethodCall), so import aliases and dot-imports
//     resolve to the same symbol.  The scan is REFERENCE-based (not
//     call-based), so passing the method as a function value is also caught.
//     Any reference outside the allowlist fails in CI.
//   - Upstream: MEDIUM, a GO-LANGUAGE CEILING (not a deferred TODO). Hard
//     upstream would require a typed tx-scoped revoke capability so calling
//     Revoke outside a tx is uncompilable. That path — gh #1615 F3 — was
//     evaluated and resolved won't-do: a same-package seal is unreachable
//     because adapters/redis's read path (evictBadEntry, lazyPopulate) needs a
//     synchronous cache.Delete/Set on the same CachingSessionStore field that
//     Revoke uses, and Go has no per-method field scoping; a cross-package
//     internal-subpackage seal is disproportionate for this single-caller P3
//     and has no industry precedent (Spring TransactionSynchronization,
//     Hibernate AfterTransactionCompletionProcess, ent CommitHook, and
//     Watermill forwarder all rely on runtime registration + convention, not a
//     type seal). Same permanent ceiling as SPAN-SETATTR-HOLDER-SEAL (#851) /
//     HEALTHZ-HOLDER-SEAL (#893 won't-do) / principal-write (#1282). Medium is
//     the honest ceiling for the upstream direction.
//
// # Detection is REFERENCE-based, not call-based
//
// The scanner matches every SelectorExpr that go/types resolves to the
// (session.Store).Revoke interface method — whether it is the callee of a
// call OR passed as a method value. A file that does f := store.Revoke; f(ctx,
// id) references the symbol at the store.Revoke SelectorExpr and is caught.
//
// # Tool blind spots (charter §"强制盲区自检")
//
//   - A call via a CONCRETE *CachingSessionStore receiver (redis.store.Revoke
//     rather than session.Store.Revoke) resolves to the CONCRETE method, not
//     the interface method, and is NOT matched by this archtest. Today all
//     production callers hold the interface type session.Store — the interface
//     method is what is resolved. This is a documented blind spot: if a future
//     caller holds a *CachingSessionStore directly, it would escape detection.
//     The risk is low because composition roots wire session.Store, not the
//     concrete type.
//   - reflect.MethodByName("Revoke") is AST-invisible and is not matched.
//     This gap is common to all SelectorExpr-based archtests and is accepted.
//   - Import aliases and dot-imports are immune BECAUSE ResolveMethodCall
//     resolves via types.Info.Selections (symbol identity), not syntactic name.
//     This is a strength, not a blind spot.
//   - The anti-vacuity guard (every allowlisted file must reference its
//     symbol ≥1×) is the reverse self-check: it proves the scanner actually
//     resolves the real references (not a vacuous pass) AND forbids stale
//     allowlist entries — a dead allowlist entry is a latent bypass slot.
package archtest

import (
	"fmt"
	"go/ast"
	"go/types"
	"sort"
	"testing"
)

const sessionPkgPath = PlatformModulePath + "/runtime/auth/session"

// sessionRevokeAllowlist maps module-relative production files that may
// reference the (session.Store).Revoke interface method.
//
// Each entry is human-verified to execute Revoke inside a RunInTx scope (the
// after-commit registry that CachingSessionStore.Revoke needs is installed by
// every conformant RunInTx, including DemoTxRunner):
//
//   - cells/accesscore/slices/sessionlogout/service.go: the primary logout
//     caller. s.sessionStore.Revoke(txCtx, sessionID) is called from
//     revokeAndPublish, itself invoked from inside a
//     persistRevoke → txRunner.RunInTx closure.
//
//   - cells/accesscore/slices/sessionlogin/service.go: three failure-
//     compensation callers — mintAndPersistSession, persistSessionWithRefresh,
//     and cleanupIssuedSession (the last only reached from the isNoopTx branches
//     of the first two). All three run on txCtx, which flows from Login's own
//     txRunner.RunInTx (and persistSessionWithRefresh's RunInTx), so the
//     after-commit registry is present. The isNoopTx guard selects compensation
//     SEMANTICS (noop tx has no rollback, so revoke manually), NOT panic-safety:
//     even in noop mode DemoTxRunner.RunInTx installs the registry, so Revoke is
//     safe regardless of whether the cache decorator is wired. context.WithoutCancel
//     preserves ctx values, so the registry survives into the Revoke call.
//
//   - adapters/redis/session_cache_store.go: the decorator delegation.
//     s.inner.Revoke(ctx, id) is the first statement of
//     CachingSessionStore.Revoke; the surrounding RunInTx scope is provided by
//     the upstream caller (sessionlogout / sessionlogin above).
//
//   - runtime/auth/session/storetest/suite.go: the store conformance suite
//     (non-_test.go helper library, caught by the production scan). It calls
//     store.Revoke bare; per the session.Store.Revoke contract a Factory whose
//     Store narrows Revoke with the after-commit hook must return a Store that
//     supplies the unit-of-work scope — adapters/redis's txScopedRevokeStore
//     bridge does exactly that (suite.go godoc).
//
//   - runtime/auth/session/storetest/bench.go: the store benchmark helper
//     (non-_test.go, caught by the production scan). Same contract as the suite:
//     a benchmark targeting a narrowing store supplies the unit-of-work scope;
//     bare-store (mem) benchmarks need none.
var sessionRevokeAllowlist = map[string]struct{}{
	"cells/accesscore/slices/sessionlogout/service.go": {},
	"cells/accesscore/slices/sessionlogin/service.go":  {},
	"adapters/redis/session_cache_store.go":            {},
	"runtime/auth/session/storetest/suite.go":          {},
	"runtime/auth/session/storetest/bench.go":          {},
}

// TestSessionRevokeCallerInTx01 asserts that every production reference to
// (session.Store).Revoke sits in the allowlist, and that no allowlist entry is
// stale (anti-vacuity reverse check).
func TestSessionRevokeCallerInTx01(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}

	// observed[relFile] = true for each live reference found.
	observed := map[string]struct{}{}
	record := func(rel string) {
		observed[rel] = struct{}{}
	}

	diags := Run(t, Production(TypedOpts{}), func(p *Pass) []Diagnostic {
		if !p.Typed() {
			return nil
		}
		var d []Diagnostic
		for _, file := range p.Files {
			rel := p.Rel(file)
			EachInSubtree[ast.SelectorExpr](file, func(sel *ast.SelectorExpr) {
				if !sessionRevokeSymbol(p.TypesInfo, sel) {
					return
				}
				record(rel)
				if _, allowed := sessionRevokeAllowlist[rel]; !allowed {
					pos := p.Fset.Position(sel.Pos())
					d = append(d, Diagnostic{
						Rel:  rel,
						Line: pos.Line,
						Message: fmt.Sprintf(
							"SESSION-REVOKE-CALLER-INTX-01: (session.Store).Revoke is referenced "+
								"from %s, which is not in the sanctioned caller allowlist. "+
								"adapters/redis.CachingSessionStore.Revoke registers a post-commit "+
								"cache DEL via persistence.RegisterAfterCommit, which PANICS if "+
								"called outside a RunInTx scope. Before adding this file to "+
								"sessionRevokeAllowlist, a reviewer MUST verify that every call "+
								"path from this file to Revoke is wrapped in a RunInTx closure.",
							rel,
						),
					})
				}
			})
		}
		return d
	})

	// Anti-vacuity / no-stale reverse self-check: every allowlisted file must
	// host a live reference. A stale entry is a latent bypass slot.
	allowed := make([]string, 0, len(sessionRevokeAllowlist))
	for f := range sessionRevokeAllowlist {
		allowed = append(allowed, f)
	}
	sort.Strings(allowed)
	for _, f := range allowed {
		if _, seen := observed[f]; !seen {
			diags = append(diags, Diagnostic{
				Message: fmt.Sprintf(
					"SESSION-REVOKE-CALLER-INTX-01: allowlist entry %q is STALE — no live "+
						"reference to (session.Store).Revoke was observed. Either the scanner "+
						"stopped detecting the call (regression) or the call was removed; drop "+
						"the dead allowlist entry so it cannot become a silent bypass slot.",
					f,
				),
			})
		}
	}

	Report(t, "SESSION-REVOKE-CALLER-INTX-01", diags)
}

// sessionRevokeSymbol reports whether sel is a reference (call or value) to the
// interface method (session.Store).Revoke, alias-proof via go/types.
// Returns false for any other selector.
func sessionRevokeSymbol(info *types.Info, sel *ast.SelectorExpr) bool {
	fn, ok := ResolveMethodCall(info, sel)
	if !ok || fn == nil {
		return false
	}
	if fn.Name() != "Revoke" {
		return false
	}
	if fn.Pkg() == nil || fn.Pkg().Path() != sessionPkgPath {
		return false
	}
	return isSessionStoreReceiver(fn)
}

// isSessionStoreReceiver reports whether fn's receiver base type is
// runtime/auth/session.Store (interface), so an unrelated future Revoke method
// on another type in the package does not accidentally match.
func isSessionStoreReceiver(fn *types.Func) bool {
	sig, ok := fn.Type().(*types.Signature)
	if !ok || sig.Recv() == nil {
		return false
	}
	t := sig.Recv().Type()
	if ptr, ok2 := t.(*types.Pointer); ok2 {
		t = ptr.Elem()
	}
	named, ok := t.(*types.Named)
	if !ok || named.Obj() == nil {
		return false
	}
	return named.Obj().Name() == "Store"
}
