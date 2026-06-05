// session_revoke_caller_intx_test.go — closes the DOWNSTREAM side of the
// session.Store.Revoke caller funnel.
//
// INVARIANT: SESSION-REVOKE-CALLER-INTX-01
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
// interface method (session.Store).Revoke per ENCLOSING FUNCTION (not per file):
// tx-scope is a per-function property; a new function must trigger review, but a
// second Revoke inside an already-blessed function is automatically safe. Any new
// (rel, func) pair outside the allowlist fails CI until a reviewer verifies the
// new caller is RunInTx-wrapped and adds it.
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
//   - Downstream: MEDIUM by archtest caller-allowlist at PER-FUNCTION granularity.
//     The callee is resolved via go/types (ResolveMethodCall), so import aliases
//     and dot-imports resolve to the same symbol. The scan is REFERENCE-based (not
//     call-based), so passing the method as a function value is also caught. Any
//     reference in a function not in the allowlist fails in CI.
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
// # Per-function granularity rationale
//
// The prior file-level allowlist was a coarser unit than the safety property it
// guards: tx-scope is a PER-FUNCTION property, not a per-file one. An already-
// allowlisted file such as sessionlogin/service.go holds multiple callers; adding
// a SECOND bare Revoke to that file in a NEW function would have passed CI without
// review. The per-function allowlist (rel, funcName) tightens the gate to the
// correct unit:
//
//   - A second Revoke inside an ALREADY-BLESSED function is automatically safe
//     (same function, same ambient tx, no review required).
//   - A Revoke in a NEW function forces a red CI until a reviewer verifies the
//     function wraps it in RunInTx and adds the (rel, func) entry.
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
//     concrete type. Reverse self-check: TestSessionRevokeCaller_BlindSpot_ConcreteReceiver.
//   - reflect.MethodByName("Revoke") is AST-invisible and is not matched.
//     This gap is common to all SelectorExpr-based archtests and is accepted.
//     Reverse self-check: TestSessionRevokeCaller_BlindSpot_Reflect.
//   - Import aliases and dot-imports are immune BECAUSE ResolveMethodCall
//     resolves via types.Info.Selections (symbol identity), not syntactic name.
//     This is a strength, not a blind spot.
//   - The anti-vacuity guard (every allowlisted (rel,fn) must have ≥1 observed
//     reference) proves the scanner actually resolves the real references (not a
//     vacuous pass) AND forbids stale allowlist entries — a dead entry is a
//     latent bypass slot.
package archtest

import (
	"fmt"
	"go/ast"
	"go/types"
	"sort"
	"testing"
)

const sessionPkgPath = PlatformModulePath + "/runtime/auth/session"

// sessionRevokeCallsite identifies a production reference to (session.Store).Revoke
// by the module-relative source file and the name of the enclosing FuncDecl.
// Using the enclosing function as the allowlist key rather than the file is the
// correct granularity: tx-scope is a per-function property. A new function in an
// already-allowlisted file must still pass review before its (rel, fn) entry is added.
// A reference outside any FuncDecl (top-level var init) maps to fn=="" and will
// always violate — top-level init code cannot hold a RunInTx scope.
type sessionRevokeCallsite struct {
	rel string // module-relative path, e.g. "cells/accesscore/slices/sessionlogout/service.go"
	fn  string // enclosing FuncDecl name; "" means outside any function (top-level)
}

// sessionRevokeAllowlist maps (rel, enclosingFunc) callsite pairs that may
// reference the (session.Store).Revoke interface method.
//
// Each entry is human-verified to execute Revoke inside a RunInTx scope (the
// after-commit registry that CachingSessionStore.Revoke needs is installed by
// every conformant RunInTx, including DemoTxRunner):
//
//   - cells/accesscore/slices/sessionlogout/service.go → revokeAndPublish:
//     s.sessionStore.Revoke(txCtx, sessionID) is called from revokeAndPublish,
//     itself invoked from inside a persistRevoke → txRunner.RunInTx closure.
//
//   - cells/accesscore/slices/sessionlogin/service.go → mintAndPersistSession:
//     called only from loginInTx, which runs inside Login's txRunner.RunInTx
//     closure. txCtx flows from that RunInTx, so the after-commit registry is
//     present. context.WithoutCancel preserves ctx values including the registry.
//
//   - cells/accesscore/slices/sessionlogin/service.go → persistSessionWithRefresh:
//     opens its own txRunner.RunInTx(ctx, do); the Revoke in the compensation
//     branch uses context.WithoutCancel(txCtx) where txCtx is the closure param,
//     so the registry from that RunInTx is present.
//
//   - cells/accesscore/slices/sessionlogin/service.go → cleanupIssuedSession:
//     called only from mintAndPersistSession and persistSessionWithRefresh (both
//     noop-tx branches). DemoTxRunner.RunInTx installs the registry even in demo
//     mode, so the registry is present in the txCtx propagated to cleanupIssuedSession.
//
//   - adapters/redis/session_cache_store.go → Revoke:
//     s.inner.Revoke(ctx, id) is the first statement of CachingSessionStore.Revoke;
//     the surrounding RunInTx scope is provided by the upstream caller (sessionlogout
//     / sessionlogin above). The decorator itself does not need its own tx.
//
//   - runtime/auth/session/storetest/suite.go → runRevokeDirect:
//     the store conformance suite helper (non-_test.go, caught by production scan).
//     A Factory whose Store narrows Revoke with the after-commit hook must return a
//     Store that supplies the unit-of-work scope — adapters/redis's
//     txScopedRevokeStore bridge does exactly that (suite.go godoc).
//
//   - runtime/auth/session/storetest/suite.go → runRevokeIdempotent:
//     same suite rationale as runRevokeDirect above.
//
//   - runtime/auth/session/storetest/suite.go → runRevokeNotFoundNoop:
//     same suite rationale above.
//
//   - runtime/auth/session/storetest/suite.go → seedRevokeForSubjectFixtures:
//     the fixture seeding helper that pre-revokes one session so
//     runRevokeForSubject* tests can assert RevokeForSubject does not re-stamp
//     its RevokedAt timestamp. Same suite rationale as runRevokeDirect above:
//     a Factory with CachingSessionStore must supply a txScopedRevokeStore bridge.
//
//   - runtime/auth/session/storetest/bench.go → benchMixedConcurrent:
//     the store benchmark helper (non-_test.go, caught by the production scan).
//     Today's storetest.Bench callers are the mem and PG stores, whose Revoke does
//     NOT depend on persistence.RegisterAfterCommit. CachingSessionStore is NOT
//     benchmarked via storetest.Bench; if it ever is, the bench factory must supply
//     a txScopedRevokeStore-style wrapper (as the conformance suite does) so Revoke
//     executes inside a RunInTx scope.
var sessionRevokeAllowlist = map[sessionRevokeCallsite]struct{}{
	{rel: "cells/accesscore/slices/sessionlogout/service.go", fn: "revokeAndPublish"}:         {},
	{rel: "cells/accesscore/slices/sessionlogin/service.go", fn: "mintAndPersistSession"}:     {},
	{rel: "cells/accesscore/slices/sessionlogin/service.go", fn: "persistSessionWithRefresh"}: {},
	{rel: "cells/accesscore/slices/sessionlogin/service.go", fn: "cleanupIssuedSession"}:      {},
	{rel: "adapters/redis/session_cache_store.go", fn: "Revoke"}:                              {},
	{rel: "runtime/auth/session/storetest/suite.go", fn: "runRevokeDirect"}:                   {},
	{rel: "runtime/auth/session/storetest/suite.go", fn: "runRevokeIdempotent"}:               {},
	{rel: "runtime/auth/session/storetest/suite.go", fn: "runRevokeNotFoundNoop"}:             {},
	{rel: "runtime/auth/session/storetest/suite.go", fn: "seedRevokeForSubjectFixtures"}:      {},
	{rel: "runtime/auth/session/storetest/bench.go", fn: "benchMixedConcurrent"}:              {},
}

// scanSessionRevokeCallsites walks all FuncDecls in p's files (excluding
// _test.go), finds every SelectorExpr that resolves to (session.Store).Revoke,
// attributes each to its enclosing FuncDecl (or fn="" if outside any function),
// and emits a Diagnostic for any callsite not in the allowlist.
//
// Also records each observed callsite into the provided observed map so the
// caller can run an anti-vacuity check.
//
// This factored function is reused by the main test and the RED fixture test.
func scanSessionRevokeCallsites(
	p *Pass,
	allowlist map[sessionRevokeCallsite]struct{},
	observed map[sessionRevokeCallsite]struct{},
) []Diagnostic {
	var d []Diagnostic

	for _, file := range p.Files {
		rel := p.Rel(file)
		// _test.go files are excluded: the archtest only guards production
		// callers. Conformance suites (suite.go, bench.go) are non-_test.go
		// production-path files and ARE scanned.
		if len(rel) > 8 && rel[len(rel)-8:] == "_test.go" {
			continue
		}
		EachInSubtree[ast.FuncDecl](file, func(fd *ast.FuncDecl) {
			if fd.Body == nil {
				return
			}
			fnName := fd.Name.Name
			EachInSubtree[ast.SelectorExpr](fd.Body, func(sel *ast.SelectorExpr) {
				if !sessionRevokeSymbol(p.TypesInfo, sel) {
					return
				}
				cs := sessionRevokeCallsite{rel: rel, fn: fnName}
				if observed != nil {
					observed[cs] = struct{}{}
				}
				if _, allowed := allowlist[cs]; !allowed {
					pos := p.Fset.Position(sel.Pos())
					d = append(d, Diagnostic{
						Rel:  rel,
						Line: pos.Line,
						Message: fmt.Sprintf(
							"SESSION-REVOKE-CALLER-INTX-01: (session.Store).Revoke is referenced "+
								"from %s in function %q, which is not in the sanctioned caller allowlist. "+
								"adapters/redis.CachingSessionStore.Revoke registers a post-commit "+
								"cache DEL via persistence.RegisterAfterCommit, which PANICS if "+
								"called outside a RunInTx scope. Before adding (%q, %q) to "+
								"sessionRevokeAllowlist, a reviewer MUST verify that this function "+
								"wraps the Revoke call in a RunInTx closure.",
							rel, fnName, rel, fnName,
						),
					})
				}
			})
		})
		// Also catch references outside any FuncDecl (top-level var/init).
		// We do this by scanning the whole file for Revoke selectors and
		// subtracting those already attributed to a FuncDecl body.
		// (Top-level references are so rare that a simpler approach is fine:
		// just scan the full file and check whether the position is inside any
		// FuncDecl body range — but for simplicity we rely on the fact that
		// EachInSubtree[ast.FuncDecl] covers all function bodies, and any
		// reference NOT inside a FuncDecl body is by definition top-level.)
		topLevelObserved := map[int]bool{} // position offset → true, recorded inside funcs
		EachInSubtree[ast.FuncDecl](file, func(fd *ast.FuncDecl) {
			if fd.Body == nil {
				return
			}
			EachInSubtree[ast.SelectorExpr](fd.Body, func(sel *ast.SelectorExpr) {
				if sessionRevokeSymbol(p.TypesInfo, sel) {
					topLevelObserved[p.Fset.Position(sel.Pos()).Offset] = true
				}
			})
		})
		EachInSubtree[ast.SelectorExpr](file, func(sel *ast.SelectorExpr) {
			if !sessionRevokeSymbol(p.TypesInfo, sel) {
				return
			}
			pos := p.Fset.Position(sel.Pos())
			if topLevelObserved[pos.Offset] {
				return // already attributed to a FuncDecl
			}
			cs := sessionRevokeCallsite{rel: rel, fn: ""}
			if observed != nil {
				observed[cs] = struct{}{}
			}
			if _, allowed := allowlist[cs]; !allowed {
				d = append(d, Diagnostic{
					Rel:  rel,
					Line: pos.Line,
					Message: fmt.Sprintf(
						"SESSION-REVOKE-CALLER-INTX-01: (session.Store).Revoke referenced outside "+
							"any function in %s (top-level init/var). Top-level code cannot hold a "+
							"RunInTx scope. This is always a violation.",
						rel,
					),
				})
			}
		})
	}
	return d
}

// TestSessionRevokeCallerInTx01 asserts that every production reference to
// (session.Store).Revoke is in the per-function allowlist, and that no allowlist
// entry is stale (anti-vacuity reverse check).
func TestSessionRevokeCallerInTx01(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}

	observed := map[sessionRevokeCallsite]struct{}{}

	diags := Run(t, Production(TypedOpts{}), func(p *Pass) []Diagnostic {
		if !p.Typed() {
			return nil
		}
		return scanSessionRevokeCallsites(p, sessionRevokeAllowlist, observed)
	})

	// Anti-vacuity / no-stale reverse self-check: every allowlisted (rel, fn)
	// must host ≥1 live reference. A stale entry is a latent bypass slot.
	allowed := make([]sessionRevokeCallsite, 0, len(sessionRevokeAllowlist))
	for cs := range sessionRevokeAllowlist {
		allowed = append(allowed, cs)
	}
	sort.Slice(allowed, func(i, j int) bool {
		if allowed[i].rel != allowed[j].rel {
			return allowed[i].rel < allowed[j].rel
		}
		return allowed[i].fn < allowed[j].fn
	})
	for _, cs := range allowed {
		if _, seen := observed[cs]; !seen {
			diags = append(diags, Diagnostic{
				Message: fmt.Sprintf(
					"SESSION-REVOKE-CALLER-INTX-01: allowlist entry (%q, %q) is STALE — no live "+
						"reference to (session.Store).Revoke was observed in function %q of %q. "+
						"Either the scanner stopped detecting the call (regression) or the call was "+
						"removed; drop the dead allowlist entry so it cannot become a silent bypass slot.",
					cs.rel, cs.fn, cs.fn, cs.rel,
				),
			})
		}
	}

	Report(t, "SESSION-REVOKE-CALLER-INTX-01", diags)
}

// TestSessionRevokeCaller_BlindSpot_ConcreteReceiver is the reverse self-check for
// the concrete-receiver blind spot documented in the package godoc:
//
// The main archtest scans for (session.Store).Revoke INTERFACE method references
// (resolved via go/types Selections). A call via a concrete *CachingSessionStore
// receiver would resolve to the CONCRETE method, not the interface method, and
// would be invisible to the main scan.
//
// This test asserts that NO production code references Revoke via a concrete
// *adapters/redis.CachingSessionStore receiver. If any such reference appeared,
// this test would fail — alerting that the main archtest has a live blind spot.
func TestSessionRevokeCaller_BlindSpot_ConcreteReceiver(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}

	const (
		concreteReceiverTypeName = "CachingSessionStore"
		redisPkgPath             = PlatformModulePath + "/adapters/redis"
	)

	var violations []string

	_ = Run(t, Production(TypedOpts{}), func(p *Pass) []Diagnostic {
		if !p.Typed() {
			return nil
		}
		for _, file := range p.Files {
			rel := p.Rel(file)
			if len(rel) > 8 && rel[len(rel)-8:] == "_test.go" {
				continue
			}
			EachInSubtree[ast.SelectorExpr](file, func(sel *ast.SelectorExpr) {
				if sel.Sel.Name != "Revoke" {
					return
				}
				fn, ok := ResolveMethodCall(p.TypesInfo, sel)
				if !ok || fn == nil {
					return
				}
				if fn.Name() != "Revoke" {
					return
				}
				sig, ok2 := fn.Type().(*types.Signature)
				if !ok2 || sig.Recv() == nil {
					return
				}
				recv := sig.Recv().Type()
				if ptr, ok3 := recv.(*types.Pointer); ok3 {
					recv = ptr.Elem()
				}
				named, ok4 := recv.(*types.Named)
				if !ok4 || named.Obj() == nil {
					return
				}
				// If the resolved method's receiver is the CONCRETE CachingSessionStore
				// (not the interface session.Store), the main scan misses it.
				if named.Obj().Name() == concreteReceiverTypeName &&
					named.Obj().Pkg() != nil &&
					named.Obj().Pkg().Path() == redisPkgPath {
					pos := p.Fset.Position(sel.Pos())
					violations = append(violations, fmt.Sprintf(
						"%s:%d: concrete (*CachingSessionStore).Revoke called directly — "+
							"SESSION-REVOKE-CALLER-INTX-01 cannot see this call (interface-method scan blind spot)",
						rel, pos.Line,
					))
				}
			})
		}
		return nil
	})

	if len(violations) > 0 {
		t.Errorf("SESSION-REVOKE-CALLER-INTX-01 blind spot LIVE: concrete (*CachingSessionStore).Revoke "+
			"found in production. The main archtest scan will NOT detect these callers. "+
			"Either move the caller to use session.Store interface, or extend the archtest.\n%v",
			violations)
	}
}

// TestSessionRevokeCaller_BlindSpot_Reflect is the reverse self-check for the
// reflect.MethodByName blind spot documented in the package godoc:
//
// The main archtest resolves (session.Store).Revoke via go/types SelectorExpr
// analysis, which cannot see reflect.MethodByName("Revoke") invocations.
// This test asserts that no production code uses reflect.MethodByName("Revoke"),
// covering the Production scan domain (not just adapters/redis as the sibling
// rule in caching_session_revoke_delegate_only_test.go does for its narrower scope).
func TestSessionRevokeCaller_BlindSpot_Reflect(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}

	var violations []string

	_ = Run(t, Production(TypedOpts{}), func(p *Pass) []Diagnostic {
		if p.TypesInfo == nil || p.Fset == nil {
			return nil
		}
		for _, file := range p.Files {
			rel := p.Rel(file)
			if len(rel) > 8 && rel[len(rel)-8:] == "_test.go" {
				continue
			}
			for _, hit := range scanReflectStringArgCalls(p, file, reflectMethodByName,
				func(n string) bool { return n == "Revoke" }) {
				violations = append(violations, fmt.Sprintf(
					"%s:%d: SESSION-REVOKE-CALLER-INTX-01: reflect.MethodByName(%q) detected — "+
						"archtest cannot see reflect-based invocations of session.Store.Revoke",
					rel, hit.Line, hit.Name))
			}
		}
		return nil
	})

	if len(violations) > 0 {
		t.Errorf("SESSION-REVOKE-CALLER-INTX-01 blind spot LIVE: reflect.MethodByName(\"Revoke\") "+
			"found in production. The main archtest will NOT detect these callers.\n%v",
			violations)
	}
}

// TestSessionRevokeCaller_RedFixture proves end-to-end that an allowlist-外
// caller (a function NOT in sessionRevokeAllowlist that calls session.Store.Revoke)
// is detected as a violation by the per-function callsite scan.
//
// The fixture package tools/archtest/testdata/session_revoke_caller_fixtures/
// unsanctioned_caller_red contains a bare Revoke call in a function named
// "unsanctionedRevoke" which is NOT in the allowlist. This test asserts ≥1
// violation is reported.
func TestSessionRevokeCaller_RedFixture(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}

	const fixturePkg = "./tools/archtest/testdata/session_revoke_caller_fixtures/unsanctioned_caller_red"

	observed := map[sessionRevokeCallsite]struct{}{}

	// Use an empty allowlist so any reference in the fixture is a violation.
	emptyAllowlist := map[sessionRevokeCallsite]struct{}{}

	var diags []Diagnostic
	_ = Run(t, Typed(TypedOpts{}, []string{fixturePkg}), func(p *Pass) []Diagnostic {
		if !p.Typed() {
			return nil
		}
		d := scanSessionRevokeCallsites(p, emptyAllowlist, observed)
		diags = append(diags, d...)
		return nil
	})

	if len(diags) == 0 {
		t.Errorf("TestSessionRevokeCaller_RedFixture: expected ≥1 violation from the RED fixture "+
			"(unsanctionedRevoke in %s is NOT in the allowlist), but got 0 violations. "+
			"The scanner may have failed to load the fixture or detect the Revoke reference.",
			fixturePkg)
	}
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
// runtime/auth/session.Store (interface), verified by both package path and
// type name, so an unrelated future Revoke method on another type (even one
// also named "Store" in a different package) does not accidentally match.
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
	return named.Obj().Name() == "Store" &&
		named.Obj().Pkg() != nil &&
		named.Obj().Pkg().Path() == sessionPkgPath
}
