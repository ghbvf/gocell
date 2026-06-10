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
// interface method (session.Store).Revoke per EXACT CALLSITE (file + enclosing
// function + ordinal + context expression). A new Revoke call in an already
// blessed function still triggers review. The rule also allowlists the production
// call edges into tx-scoped helpers that hide the Revoke selector
// (cleanupIssuedSession / revokeAndPublish), so a future non-transaction helper
// reuse cannot silently bypass the Revoke callsite scan.
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
//   - Downstream: MEDIUM by archtest caller-allowlist at PER-CALLSITE granularity.
//     The callee is resolved via go/types (ResolveMethodCall), so import aliases
//     and dot-imports resolve to the same symbol. The scan is REFERENCE-based (not
//     call-based), so passing the method as a function value is also caught. Any
//     new reference not in the allowlist fails in CI.
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
// # Per-callsite granularity rationale
//
// The prior file-level allowlist was a coarser unit than the safety property it
// guards, and function-level allowlisting still misses two cases:
// (1) a second Revoke call added to an already-blessed function, and
// (2) a tx-scoped helper (whose body contains Revoke) called from a new non-tx
// path. The per-callsite allowlist (rel, funcName, ordinal, ctx expression) plus
// helper-call-edge allowlist tightens the gate to the correct review unit:
//
//   - A second Revoke inside an ALREADY-BLESSED function gets a new ordinal and
//     forces red CI until reviewer verifies ctx shape / RunInTx scope.
//   - A Revoke in a NEW function likewise forces review.
//   - A new call to cleanupIssuedSession / revokeAndPublish must be allowlisted
//     as a helper edge, so helper reuse cannot hide non-tx execution.
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
//   - The anti-vacuity guard (every allowlisted callsite / helper edge must have
//     ≥1 observed reference) proves the scanner actually resolves the real
//     references (not a vacuous pass) AND forbids stale allowlist entries — a
//     dead entry is a latent bypass slot.
package archtest

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/format"
	"go/types"
	"sort"
	"testing"
)

const sessionPkgPath = PlatformModulePath + "/runtime/auth/session"

const sessionRevokeMethodValueCtx = "<method-value>"

// sessionRevokeCallsite identifies one production reference to
// (session.Store).Revoke by exact callsite.
//
// ordinal is 1-based within the enclosing function among Revoke references;
// ctx is the formatted context argument expression for calls, or
// sessionRevokeMethodValueCtx for method-value references.
// A reference outside any FuncDecl maps to fn=="" and always violates unless
// deliberately allowlisted (top-level init code cannot hold a RunInTx scope).
type sessionRevokeCallsite struct {
	rel     string
	fn      string
	ordinal int
	ctx     string
}

// sessionRevokeHelperCallsite identifies one production call edge into a helper
// whose body contains a sanctioned (session.Store).Revoke call. The helper call
// itself must stay tx-scoped, otherwise the hidden Revoke call can execute
// outside RunInTx without producing a new Revoke selector.
type sessionRevokeHelperCallsite struct {
	rel     string
	fn      string
	helper  string
	ordinal int
	ctx     string
}

var sessionRevokeTxScopedHelpers = map[string]bool{
	"cleanupIssuedSession": true,
	"revokeAndPublish":     true,
}

// sessionRevokeAllowlist maps exact callsites that may
// reference the (session.Store).Revoke interface method.
//
// Each entry is human-verified to execute Revoke inside a RunInTx scope (the
// after-commit registry that CachingSessionStore.Revoke needs is installed by
// every conformant RunInTx, including DemoTxRunner):
//
//   - corecells/accesscore/slices/sessionlogout/service.go → revokeAndPublish:
//     s.sessionStore.Revoke(txCtx, sessionID) is called from revokeAndPublish,
//     itself invoked from inside a persistRevoke → txRunner.RunInTx closure.
//
//   - corecells/accesscore/slices/sessionlogin/service.go → mintAndPersistSession:
//     called only from loginInTx, which runs inside Login's txRunner.RunInTx
//     closure. txCtx flows from that RunInTx, so the after-commit registry is
//     present. context.WithoutCancel preserves ctx values including the registry.
//
//   - corecells/accesscore/slices/sessionlogin/service.go → persistSessionWithRefresh:
//     opens its own txRunner.RunInTx(ctx, do); the Revoke in the compensation
//     branch uses context.WithoutCancel(txCtx) where txCtx is the closure param,
//     so the registry from that RunInTx is present.
//
//   - corecells/accesscore/slices/sessionlogin/service.go → cleanupIssuedSession:
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
	{
		rel:     "corecells/accesscore/slices/sessionlogout/service.go",
		fn:      "revokeAndPublish",
		ordinal: 1,
		ctx:     "txCtx",
	}: {},
	{
		rel:     "corecells/accesscore/slices/sessionlogin/service.go",
		fn:      "mintAndPersistSession",
		ordinal: 1,
		ctx:     "context.WithoutCancel(txCtx)",
	}: {},
	{
		rel:     "corecells/accesscore/slices/sessionlogin/service.go",
		fn:      "persistSessionWithRefresh",
		ordinal: 1,
		ctx:     "context.WithoutCancel(txCtx)",
	}: {},
	{
		rel:     "corecells/accesscore/slices/sessionlogin/service.go",
		fn:      "cleanupIssuedSession",
		ordinal: 1,
		ctx:     "cleanupCtx",
	}: {},
	{
		rel:     "adapters/redis/session_cache_store.go",
		fn:      "Revoke",
		ordinal: 1,
		ctx:     "ctx",
	}: {},
	{
		rel:     "runtime/auth/session/storetest/suite.go",
		fn:      "runRevokeDirect",
		ordinal: 1,
		ctx:     "context.Background()",
	}: {},
	{
		rel:     "runtime/auth/session/storetest/suite.go",
		fn:      "runRevokeIdempotent",
		ordinal: 1,
		ctx:     "context.Background()",
	}: {},
	{
		rel:     "runtime/auth/session/storetest/suite.go",
		fn:      "runRevokeIdempotent",
		ordinal: 2,
		ctx:     "context.Background()",
	}: {},
	{
		rel:     "runtime/auth/session/storetest/suite.go",
		fn:      "runRevokeNotFoundNoop",
		ordinal: 1,
		ctx:     "context.Background()",
	}: {},
	{
		rel:     "runtime/auth/session/storetest/suite.go",
		fn:      "seedRevokeForSubjectFixtures",
		ordinal: 1,
		ctx:     "ctx",
	}: {},
	{
		rel:     "runtime/auth/session/storetest/bench.go",
		fn:      "benchMixedConcurrent",
		ordinal: 1,
		ctx:     "ctx",
	}: {},
}

// sessionRevokeHelperAllowlist maps production call edges into tx-scoped helper
// methods that contain Revoke callsites.
var sessionRevokeHelperAllowlist = map[sessionRevokeHelperCallsite]struct{}{
	{
		rel:     "corecells/accesscore/slices/sessionlogout/service.go",
		fn:      "Logout",
		helper:  "revokeAndPublish",
		ordinal: 1,
		ctx:     "txCtx",
	}: {},
	{
		rel:     "corecells/accesscore/slices/sessionlogin/service.go",
		fn:      "mintAndPersistSession",
		helper:  "cleanupIssuedSession",
		ordinal: 1,
		ctx:     "txCtx",
	}: {},
	{
		rel:     "corecells/accesscore/slices/sessionlogin/service.go",
		fn:      "persistSessionWithRefresh",
		helper:  "cleanupIssuedSession",
		ordinal: 1,
		ctx:     "txCtx",
	}: {},
}

// scanSessionRevokeCallsites walks all FuncDecls in p's files (excluding
// _test.go), finds every reference that resolves to (session.Store).Revoke,
// attributes each to its exact callsite (or fn="" if outside any function),
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
		funcRefOffsets := map[int]bool{}
		EachInSubtree[ast.FuncDecl](file, func(fd *ast.FuncDecl) {
			if fd.Body == nil {
				return
			}
			EachInSubtree[ast.SelectorExpr](fd.Body, func(sel *ast.SelectorExpr) {
				if sessionRevokeSymbol(p.TypesInfo, sel) {
					funcRefOffsets[p.Fset.Position(sel.Pos()).Offset] = true
				}
			})
			d = append(d, scanSessionRevokeCallsitesInNode(p, rel, fd.Name.Name, fd.Body, nil, allowlist, observed)...)
		})
		d = append(d, scanSessionRevokeCallsitesInNode(p, rel, "", file, funcRefOffsets, allowlist, observed)...)
	}
	return d
}

func scanSessionRevokeCallsitesInNode(
	p *Pass,
	rel string,
	fnName string,
	node ast.Node,
	skipOffsets map[int]bool,
	allowlist map[sessionRevokeCallsite]struct{},
	observed map[sessionRevokeCallsite]struct{},
) []Diagnostic {
	var d []Diagnostic
	callSelectorOffsets := map[int]bool{}
	ordinal := 0

	EachInSubtree[ast.CallExpr](node, func(call *ast.CallExpr) {
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || !sessionRevokeSymbol(p.TypesInfo, sel) {
			return
		}
		pos := p.Fset.Position(sel.Pos())
		if skipOffsets[pos.Offset] {
			return
		}
		callSelectorOffsets[pos.Offset] = true
		ordinal++
		cs := sessionRevokeCallsite{
			rel:     rel,
			fn:      fnName,
			ordinal: ordinal,
			ctx:     sessionRevokeCallContextExpr(p, sel, call),
		}
		d = append(d, recordSessionRevokeCallsite(allowlist, observed, cs, pos.Line)...)
	})

	EachInSubtree[ast.SelectorExpr](node, func(sel *ast.SelectorExpr) {
		if !sessionRevokeSymbol(p.TypesInfo, sel) {
			return
		}
		pos := p.Fset.Position(sel.Pos())
		if skipOffsets[pos.Offset] || callSelectorOffsets[pos.Offset] {
			return
		}
		ordinal++
		cs := sessionRevokeCallsite{
			rel:     rel,
			fn:      fnName,
			ordinal: ordinal,
			ctx:     sessionRevokeMethodValueCtx,
		}
		d = append(d, recordSessionRevokeCallsite(allowlist, observed, cs, pos.Line)...)
	})

	return d
}

func recordSessionRevokeCallsite(
	allowlist map[sessionRevokeCallsite]struct{},
	observed map[sessionRevokeCallsite]struct{},
	cs sessionRevokeCallsite,
	line int,
) []Diagnostic {
	if observed != nil {
		observed[cs] = struct{}{}
	}
	if _, allowed := allowlist[cs]; allowed {
		return nil
	}
	return []Diagnostic{{
		Rel:  cs.rel,
		Line: line,
		Message: fmt.Sprintf(
			"SESSION-REVOKE-CALLER-INTX-01: (session.Store).Revoke reference "+
				"at callsite (%q, %q, ordinal=%d, ctx=%q) is not in the sanctioned "+
				"caller allowlist. adapters/redis.CachingSessionStore.Revoke registers "+
				"a post-commit cache DEL via persistence.RegisterAfterCommit, which "+
				"PANICS if called outside a RunInTx scope. Before adding this exact "+
				"callsite to sessionRevokeAllowlist, a reviewer MUST verify that the "+
				"call executes inside RunInTx and that the ctx argument carries the "+
				"after-commit registry.",
			cs.rel, cs.fn, cs.ordinal, cs.ctx,
		),
	}}
}

func scanSessionRevokeHelperCallsites(
	p *Pass,
	allowlist map[sessionRevokeHelperCallsite]struct{},
	observed map[sessionRevokeHelperCallsite]struct{},
) []Diagnostic {
	var d []Diagnostic
	for _, file := range p.Files {
		rel := p.Rel(file)
		if len(rel) > 8 && rel[len(rel)-8:] == "_test.go" {
			continue
		}
		EachInSubtree[ast.FuncDecl](file, func(fd *ast.FuncDecl) {
			if fd.Body == nil {
				return
			}
			ordinals := map[string]int{}
			EachInSubtree[ast.CallExpr](fd.Body, func(call *ast.CallExpr) {
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok || !sessionRevokeTxScopedHelpers[sel.Sel.Name] {
					return
				}
				ordinals[sel.Sel.Name]++
				pos := p.Fset.Position(sel.Pos())
				cs := sessionRevokeHelperCallsite{
					rel:     rel,
					fn:      fd.Name.Name,
					helper:  sel.Sel.Name,
					ordinal: ordinals[sel.Sel.Name],
					ctx:     sessionRevokeHelperContextExpr(p, sel, call),
				}
				if observed != nil {
					observed[cs] = struct{}{}
				}
				if _, allowed := allowlist[cs]; allowed {
					return
				}
				d = append(d, Diagnostic{
					Rel:  rel,
					Line: pos.Line,
					Message: fmt.Sprintf(
						"SESSION-REVOKE-CALLER-INTX-01: tx-scoped helper %s called "+
							"from (%q, %q, ordinal=%d, ctx=%q) without a sanctioned "+
							"helper-edge allowlist entry. This helper hides a "+
							"(session.Store).Revoke call, so every production caller "+
							"must be reviewed to ensure it passes a RunInTx ctx.",
						cs.helper, cs.rel, cs.fn, cs.ordinal, cs.ctx,
					),
				})
			})
		})
	}
	return d
}

// TestSessionRevokeCallerInTx01 asserts that every production reference to
// (session.Store).Revoke is in the per-callsite allowlist, and that no allowlist
// entry is stale (anti-vacuity reverse check).
func TestSessionRevokeCallerInTx01(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}

	observed := map[sessionRevokeCallsite]struct{}{}
	observedHelpers := map[sessionRevokeHelperCallsite]struct{}{}

	diags := Run(t, Production(TypedOpts{}), func(p *Pass) []Diagnostic {
		if !p.Typed() {
			return nil
		}
		out := scanSessionRevokeCallsites(p, sessionRevokeAllowlist, observed)
		out = append(out, scanSessionRevokeHelperCallsites(p, sessionRevokeHelperAllowlist, observedHelpers)...)
		return out
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
						"reference to (session.Store).Revoke was observed at ordinal %d with ctx %q in function %q of %q. "+
						"Either the scanner stopped detecting the call (regression) or the call was "+
						"removed; drop the dead allowlist entry so it cannot become a silent bypass slot.",
					cs.rel, cs.fn, cs.ordinal, cs.ctx, cs.fn, cs.rel,
				),
			})
		}
	}
	allowedHelpers := make([]sessionRevokeHelperCallsite, 0, len(sessionRevokeHelperAllowlist))
	for cs := range sessionRevokeHelperAllowlist {
		allowedHelpers = append(allowedHelpers, cs)
	}
	sort.Slice(allowedHelpers, func(i, j int) bool {
		if allowedHelpers[i].rel != allowedHelpers[j].rel {
			return allowedHelpers[i].rel < allowedHelpers[j].rel
		}
		if allowedHelpers[i].fn != allowedHelpers[j].fn {
			return allowedHelpers[i].fn < allowedHelpers[j].fn
		}
		if allowedHelpers[i].helper != allowedHelpers[j].helper {
			return allowedHelpers[i].helper < allowedHelpers[j].helper
		}
		return allowedHelpers[i].ordinal < allowedHelpers[j].ordinal
	})
	for _, cs := range allowedHelpers {
		if _, seen := observedHelpers[cs]; !seen {
			diags = append(diags, Diagnostic{
				Message: fmt.Sprintf(
					"SESSION-REVOKE-CALLER-INTX-01: helper allowlist entry "+
						"(%q, %q, %q, ordinal=%d, ctx=%q) is STALE — no live helper call was observed. "+
						"Either the scanner stopped detecting the helper edge (regression) or the call was "+
						"removed; drop the dead allowlist entry so it cannot become a silent bypass slot.",
					cs.rel, cs.fn, cs.helper, cs.ordinal, cs.ctx,
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

// TestSessionRevokeCaller_RedFixture_AllowlistedFunctionExtraCall reproduces
// the function-level allowlist blind spot: the enclosing function is sanctioned,
// but it contains a second Revoke call with a non-transaction context.
func TestSessionRevokeCaller_RedFixture_AllowlistedFunctionExtraCall(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}

	const (
		fixturePkg = "./tools/archtest/testdata/session_revoke_caller_fixtures/allowlisted_function_extra_call_red"
		fixtureRel = "tools/archtest/testdata/session_revoke_caller_fixtures/allowlisted_function_extra_call_red/red.go"
	)

	observed := map[sessionRevokeCallsite]struct{}{}
	allowlist := map[sessionRevokeCallsite]struct{}{
		{rel: fixtureRel, fn: "allowedButBad", ordinal: 1, ctx: "txCtx"}: {},
	}

	var diags []Diagnostic
	_ = Run(t, Typed(TypedOpts{}, []string{fixturePkg}), func(p *Pass) []Diagnostic {
		if !p.Typed() {
			return nil
		}
		d := scanSessionRevokeCallsites(p, allowlist, observed)
		diags = append(diags, d...)
		return nil
	})

	if len(diags) == 0 {
		t.Errorf("TestSessionRevokeCaller_RedFixture_AllowlistedFunctionExtraCall: expected violation " +
			"from the second Revoke call in an otherwise allowlisted function, but got 0. " +
			"The scanner is still function-granular instead of callsite-granular.")
	}
}

// TestSessionRevokeCaller_RedFixture_HelperNonTxCall proves helper-edge
// scanning catches a tx-scoped helper reused with a non-transaction context.
func TestSessionRevokeCaller_RedFixture_HelperNonTxCall(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}

	const (
		fixturePkg = "./tools/archtest/testdata/session_revoke_caller_fixtures/helper_nontx_call_red"
		fixtureRel = "tools/archtest/testdata/session_revoke_caller_fixtures/helper_nontx_call_red/red.go"
	)

	observed := map[sessionRevokeHelperCallsite]struct{}{}
	allowlist := map[sessionRevokeHelperCallsite]struct{}{
		{rel: fixtureRel, fn: "caller", helper: "cleanupIssuedSession", ordinal: 1, ctx: "txCtx"}: {},
	}

	var diags []Diagnostic
	_ = Run(t, Typed(TypedOpts{}, []string{fixturePkg}), func(p *Pass) []Diagnostic {
		if !p.Typed() {
			return nil
		}
		d := scanSessionRevokeHelperCallsites(p, allowlist, observed)
		diags = append(diags, d...)
		return nil
	})

	if len(diags) == 0 {
		t.Errorf("TestSessionRevokeCaller_RedFixture_HelperNonTxCall: expected violation " +
			"from the second cleanupIssuedSession call with context.Background(), but got 0. " +
			"The scanner is not guarding tx-scoped helper call edges.")
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

func sessionRevokeCallContextExpr(p *Pass, sel *ast.SelectorExpr, call *ast.CallExpr) string {
	return callContextExpr(p, sel, call, 0)
}

func sessionRevokeHelperContextExpr(p *Pass, sel *ast.SelectorExpr, call *ast.CallExpr) string {
	return callContextExpr(p, sel, call, 0)
}

func callContextExpr(p *Pass, sel *ast.SelectorExpr, call *ast.CallExpr, methodValIndex int) string {
	if call == nil {
		return ""
	}
	idx := methodValIndex
	if p != nil && p.TypesInfo != nil && sel != nil {
		if selection := p.TypesInfo.Selections[sel]; selection != nil && selection.Kind() == types.MethodExpr {
			idx++
		}
	}
	if idx < 0 || idx >= len(call.Args) {
		return ""
	}
	return formatSessionRevokeExpr(p, call.Args[idx])
}

func formatSessionRevokeExpr(p *Pass, expr ast.Expr) string {
	if p == nil || p.Fset == nil || expr == nil {
		return ""
	}
	var b bytes.Buffer
	if err := format.Node(&b, p.Fset, expr); err != nil {
		return fmt.Sprintf("<unprintable:%T>", expr)
	}
	return b.String()
}
