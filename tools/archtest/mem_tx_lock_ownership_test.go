package archtest

// INVARIANT: MEM-TX-LOCK-OWNERSHIP-01
//
// mem_tx_lock_ownership_test.go guards the regression surface of the mem tx
// lock-ownership funnel after the self-invalidating lease rework (#945 witness →
// #972 lease; ADR docs/architecture/202605171846-adr-mem-tx-lock-ownership.md).
// The threat is "sentinel present but no lock": a tx-context value authorizing a
// repository method to skip its per-call store.mu lock when the lock is NOT held
// → concurrent map writes (the PR #558 flake).
//
// That threat is closed in the TYPE SYSTEM (the sealed txlock package), not in
// this archtest:
//
//   - Upstream Hard (forge): cells/accesscore/internal/mem/internal/txlock.Lease
//     has only unexported fields (mu *sync.Mutex, live *atomic.Bool), so no
//     package (not even mem) can write txlock.Lease{…} with a real mutex+live
//     flag. The only way to obtain a Lease whose Live(&store.mu) is true is
//     txlock.Acquire(&store.mu), which Lock()s the mutex and arms live.
//   - Downstream Hard (liveness): the lease SELF-INVALIDATES. Acquire's unlock
//     closure (the sole writer of live) flips it dead before releasing store.mu.
//     A ctx carrying the lease that escapes the runLocked closure reports
//     Live==false afterwards (fail-closed: per-call lock). Lease has no Release
//     method and no way to write live from outside the unlock closure, so a
//     ctx-carried lease can neither unlock nor re-arm itself. (database/sql
//     *Tx.done → ErrTxDone analog.)
//
// This file is the Medium regression layer over that Hard core. It runs the
// shared detector scanMemTxLockWitness over package mem (typed, via RunTyped) and
// reports:
//
//	W1 — txlock.Acquire is the single sanctioned mint: it appears ONLY as
//	     `txlock.Acquire(&r.s.mu)` in the DIRECT body of (memTxRunner).runLocked.
//	     Any Acquire elsewhere, with a foreign argument, or nested inside a
//	     closure within runLocked is reported. (Tightened in #972: the earlier
//	     rule pinned only the lexical site, not the &r.s.mu argument or the
//	     direct-body / non-closure shape.)
//	W2 — (*Store).inLiveTx returns exactly `l.Live(&s.mu)`, where l is the lease
//	     bound from `ctx.Value(memTxKey{}).(txlock.Lease)`. Dropping the Live
//	     delegation (e.g. `return true`), comparing the mutex directly (bypassing
//	     the live flag), or sourcing the lease elsewhere fails — this pins the
//	     downstream check against silent regression.
//
// The ctx now carries the txlock.Lease value directly (no memTxToken wrapper) and
// WithTxContext is gone, so the former R1 (memTxToken literal scope) is deleted:
// a live Lease can ONLY originate in txlock.Acquire (W1 pins it to runLocked) and
// a zero Lease is harmless (Live false). The seal + W1 subsume R1.
//
// The seal itself (txlock.Lease field-set: exactly two unexported fields,
// *sync.Mutex + *atomic.Bool) is frozen by reflect in the txlock package's own
// test (TestLeaseSealFrozen in internal/txlock/txlock_test.go) — tools/archtest
// cannot import the double-internal txlock package, so the freeze lives where
// Lease is defined, mirroring the FixtureOpts freeze in pass_test.go. The live
// flip lives only in Acquire's unlock closure inside that sealed package, so no
// standalone archtest can (or needs to) guard it.
//
// AI-robust rating (modifies an enforcement mechanism + reworks runtime design):
//   - Upstream Hard / Downstream Hard — type system (txlock.Lease seal; Lease has
//     no Release; live writable only by Acquire's unlock closure). This archtest
//     is NOT the primary defense.
//   - This file (W1/W2) = Medium regression layer: string/AST anchors
//     ("Acquire"/"txlock"/"runLocked"/"inLiveTx"/"Live"/"mu"/"memTxKey") + AST
//     form-uniqueness. No gh issue to "Hard-ify" — the funnel is already Hard via
//     the seal; W1/W2 only catch funnel-discipline drift.
//
// Tool blind spots (pure-AST/typed scan) + their reverse self-checks, per
// ai-robust.md §"工具选定后强制盲区自检":
//   - reflect.Value.Set / unsafe pointer write on txlock.Lease.{mu,live} would
//     forge a lease: closed by NoReflectInMemPkg / NoUnsafeInMemPkg asserting
//     neither package mem nor package txlock imports "reflect"/"unsafe".
//   - vacuous-pass (matcher silently matches nothing): closed by
//     FindsSanctionedSites asserting the scan finds the runLocked Acquire(&r.s.mu)
//     site and the inLiveTx accessor in production.
//   - W1 arg-pin / nesting traversal correctness: closed by the AST unit test
//     W1ArgAndNesting (isAcquireArgRecvStoreMu table + directBodyAcquireCalls
//     excludes nested-closure calls).
//   - detector correctness (false-negative / false-positive drift): closed by the
//     real-source reverse self-check TestMemTxLockOwnership01_FixturePattern
//     (mem_tx_lock_ownership_red_fixture_test.go) asserting exactly the three RED
//     sites are reported and the sanctioned site is not.
//   - W2 field/method-name anchors ("inLiveTx", "Live", "memTxKey", "mu"): a
//     rename breaks every call site to compile before archtest runs (build error),
//     and the txlock.Lease seal is reflect-frozen by TestLeaseSealFrozen.
//   - W2 type/source matchers are TYPED (closed, not a blind spot): isTxlockLeaseType
//     resolves the asserted type via go/types to a named Lease in a /txlock package
//     (a foreign `<pkg>.Lease` is rejected), and isCtxValueMemTxKeyCall requires the
//     `.Value(memTxKey{})` receiver to resolve to context.Context. No string-name
//     latitude remains, so bindsLeaseFromCtx cannot be satisfied by a fabricated,
//     freshly-Acquired, or foreign-typed lease source.

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const ruleMemTxLockOwnership01 = "MEM-TX-LOCK-OWNERSHIP-01"

const memPkgRel = "cells/accesscore/internal/mem"

// memTxSiteRunLocked is the sole sanctioned lease-mint site: (memTxRunner).runLocked
// is the only function allowed to call txlock.Acquire, and only as the
// direct-body `&r.s.mu` call (W1).
const memTxSiteRunLocked = "runLocked"

// receiverVarName returns the receiver variable name of fd (the `s` in
// `func (s *Store) …`), or "" when there is no named receiver.
func receiverVarName(fd *ast.FuncDecl) string {
	if fd.Recv == nil || len(fd.Recv.List) == 0 || len(fd.Recv.List[0].Names) == 0 {
		return ""
	}
	return fd.Recv.List[0].Names[0].Name
}

// isTxlockAcquireCall reports whether sel is a reference to func Acquire in a
// package whose import path ends in "/txlock" (production
// …/mem/internal/txlock or the fixture mirror …/memtxlockfixture/txlock).
func isTxlockAcquireCall(info *types.Info, sel *ast.SelectorExpr) bool {
	if sel.Sel == nil || sel.Sel.Name != "Acquire" {
		return false
	}
	pkgPath, name, ok := ResolvePackageRef(info, sel)
	return ok && name == "Acquire" && strings.HasSuffix(pkgPath, "/txlock")
}

// isAcquireArgRecvStoreMu reports whether ce's sole argument is exactly
// `&<recv>.s.mu` — the store mutex owned by runLocked's receiver. This is the W1
// argument pin (#972): it rejects `txlock.Acquire(&otherMu)`,
// `txlock.Acquire(&r.s.someOtherField)`, and `txlock.Acquire(&x.mu)` even when
// they sit inside runLocked, so the mint can only lock THIS store's mutex.
func isAcquireArgRecvStoreMu(ce *ast.CallExpr, recv string) bool {
	if len(ce.Args) != 1 {
		return false
	}
	addr, ok := ce.Args[0].(*ast.UnaryExpr) // &…
	if !ok || addr.Op != token.AND {
		return false
	}
	muSel, ok := addr.X.(*ast.SelectorExpr) // ….mu
	if !ok || muSel.Sel.Name != "mu" {
		return false
	}
	sSel, ok := muSel.X.(*ast.SelectorExpr) // r.s
	if !ok || sSel.Sel.Name != "s" {
		return false
	}
	id, ok := sSel.X.(*ast.Ident) // r
	return ok && id.Name == recv
}

// directBodyAcquireCalls collects the Acquire calls (per isAcquire) reachable
// from fd.Body WITHOUT descending into a nested func literal. An Acquire wrapped
// in `go func(){ … }()` or any closure inside runLocked is therefore excluded
// from the sanctioned set (W1 #972): the single synchronous mint must be a direct
// statement of runLocked, not deferred to another stack frame / goroutine.
func directBodyAcquireCalls(fd *ast.FuncDecl, isAcquire func(*ast.CallExpr) bool) []*ast.CallExpr {
	var out []*ast.CallExpr
	EachInSubtreeStopAt[ast.CallExpr](fd.Body, func(n ast.Node) bool {
		_, ok := n.(*ast.FuncLit)
		return ok // do not descend into nested closures
	}, func(ce *ast.CallExpr) {
		if isAcquire(ce) {
			out = append(out, ce)
		}
	})
	return out
}

// leaseLiveCall reports whether e is exactly `<lease>.Live(&<recv>.mu)` and
// returns the lease identifier name. Used to pin (*Store).inLiveTx's return (W2).
func leaseLiveCall(e ast.Expr, recv string) (leaseVar string, ok bool) {
	ce, ok := e.(*ast.CallExpr)
	if !ok || len(ce.Args) != 1 {
		return "", false
	}
	live, ok := ce.Fun.(*ast.SelectorExpr) // <lease>.Live
	if !ok || live.Sel.Name != "Live" {
		return "", false
	}
	lid, ok := live.X.(*ast.Ident)
	if !ok {
		return "", false
	}
	addr, ok := ce.Args[0].(*ast.UnaryExpr) // &<recv>.mu
	if !ok || addr.Op != token.AND {
		return "", false
	}
	muSel, ok := addr.X.(*ast.SelectorExpr)
	if !ok || muSel.Sel.Name != "mu" {
		return "", false
	}
	rid, ok := muSel.X.(*ast.Ident)
	if !ok || rid.Name != recv {
		return "", false
	}
	return lid.Name, true
}

// isTxlockLeaseType reports whether the type expression e resolves (typed) to a
// named "Lease" declared in a package whose import path ends in "/txlock"
// (production …/mem/internal/txlock or the fixture mirror). Typed resolution via
// info — not a bare Sel.Name=="Lease" string match — so a foreign package's Lease
// type cannot satisfy the W2 lease-source check.
func isTxlockLeaseType(info *types.Info, e ast.Expr) bool {
	t := info.TypeOf(e)
	if t == nil {
		return false
	}
	named, ok := t.(*types.Named)
	if !ok {
		return false
	}
	obj := named.Obj()
	return obj != nil && obj.Name() == "Lease" &&
		obj.Pkg() != nil && strings.HasSuffix(obj.Pkg().Path(), "/txlock")
}

// isContextContextType reports whether t is the named interface context.Context.
func isContextContextType(t types.Type) bool {
	named, ok := t.(*types.Named)
	if !ok {
		return false
	}
	obj := named.Obj()
	return obj != nil && obj.Name() == "Context" &&
		obj.Pkg() != nil && obj.Pkg().Path() == "context"
}

// isCtxValueMemTxKeyCall reports whether e is `<ctx>.Value(memTxKey{})` where the
// receiver is typed context.Context (typed — not any variable calling .Value).
func isCtxValueMemTxKeyCall(info *types.Info, e ast.Expr) bool {
	ce, ok := e.(*ast.CallExpr)
	if !ok || len(ce.Args) != 1 {
		return false
	}
	sel, ok := ce.Fun.(*ast.SelectorExpr)
	if !ok || sel.Sel == nil || sel.Sel.Name != "Value" {
		return false
	}
	if rt := info.TypeOf(sel.X); rt == nil || !isContextContextType(rt) {
		return false
	}
	cl, ok := ce.Args[0].(*ast.CompositeLit)
	if !ok {
		return false
	}
	id, ok := cl.Type.(*ast.Ident)
	return ok && id.Name == "memTxKey"
}

// bindsLeaseFromCtx reports whether fd assigns leaseVar from
// `<ctx>.Value(memTxKey{}).(txlock.Lease)` — typed: the asserted type resolves to
// a /txlock-package Lease and the receiver is context.Context. Pins the W2 lease
// source so the delegation cannot read a fabricated, freshly-Acquired, or
// foreign-typed lease.
func bindsLeaseFromCtx(info *types.Info, fd *ast.FuncDecl, leaseVar string) bool {
	_, ok := FindFirstInSubtree[ast.AssignStmt](fd, func(as *ast.AssignStmt) bool {
		if len(as.Lhs) == 0 || len(as.Rhs) != 1 {
			return false
		}
		id, ok := as.Lhs[0].(*ast.Ident)
		if !ok || id.Name != leaseVar {
			return false
		}
		ta, ok := as.Rhs[0].(*ast.TypeAssertExpr)
		if !ok || ta.Type == nil || !isTxlockLeaseType(info, ta.Type) {
			return false
		}
		return isCtxValueMemTxKeyCall(info, ta.X)
	})
	return ok
}

// inLiveTxFormOK pins (*Store).inLiveTx to exactly `return l.Live(&s.mu)` where l
// is bound from `ctx.Value(memTxKey{}).(txlock.Lease)`: a single single-value
// return delegating to the sealed liveness check on the ctx-sourced lease.
//
// Form, not semantics, is pinned (intentional, to block silent regression of a
// security invariant): an early-return refactor producing MORE than one
// single-value return trips this even when semantically equivalent. The one-liner
// is the canonical form; weakening it (dropping the Live delegation, comparing
// l.mu directly to bypass the live flag, sourcing l elsewhere) must be a
// deliberate edit that also updates this rule.
func inLiveTxFormOK(info *types.Info, fd *ast.FuncDecl) (ok bool, why string) {
	recv := receiverVarName(fd)
	if recv == "" {
		return false, "no named receiver"
	}
	var rets []*ast.ReturnStmt
	EachInSubtree[ast.ReturnStmt](fd, func(rs *ast.ReturnStmt) {
		if len(rs.Results) == 1 {
			rets = append(rets, rs)
		}
	})
	if len(rets) != 1 {
		return false, fmt.Sprintf("expected exactly 1 single-value return, got %d", len(rets))
	}
	leaseVar, ok := leaseLiveCall(rets[0].Results[0], recv)
	if !ok {
		return false, fmt.Sprintf("return must be `<lease>.Live(&%s.mu)`", recv)
	}
	if !bindsLeaseFromCtx(info, fd, leaseVar) {
		return false, fmt.Sprintf("`%s` must be bound from ctx.Value(memTxKey{}).(txlock.Lease)", leaseVar)
	}
	return true, ""
}

// scanMemTxLockWitness is the shared lease-funnel detector. It runs over a single
// package Pass — production (cells/accesscore/internal/mem) and the
// memtxlockfixture reverse self-check alike — and reports W1 / W2 (see the file
// INVARIANT block). Requires a typed Pass; returns nil for a bare-AST Pass.
func scanMemTxLockWitness(p *Pass) []Diagnostic {
	if p.TypesInfo == nil || p.Pkg == nil {
		return nil
	}
	isAcquire := func(ce *ast.CallExpr) bool {
		sel, ok := ce.Fun.(*ast.SelectorExpr)
		return ok && isTxlockAcquireCall(p.TypesInfo, sel)
	}

	var ds []Diagnostic
	for _, file := range p.Files {
		rel := p.Rel(file)
		report := func(node ast.Node, msg string) {
			ds = append(ds, Diagnostic{
				Rel:     rel,
				Line:    p.Fset.Position(node.Pos()).Line,
				Message: msg,
			})
		}

		// Locate the sole sanctioned mint site: (memTxRunner).runLocked, and record
		// the Acquire(&r.s.mu) calls in its direct body (excluding nested closures
		// and foreign-arg calls) as sanctioned.
		sanctioned := map[*ast.CallExpr]bool{}
		EachInSubtree[ast.FuncDecl](file, func(fd *ast.FuncDecl) {
			if fd.Body == nil || fd.Name.Name != memTxSiteRunLocked {
				return
			}
			if fd.Recv == nil || receiverTypeName(fd) != "memTxRunner" {
				return
			}
			recv := receiverVarName(fd)
			for _, ce := range directBodyAcquireCalls(fd, isAcquire) {
				if isAcquireArgRecvStoreMu(ce, recv) {
					sanctioned[ce] = true
				}
			}
		})

		// W1: every txlock.Acquire call must be the sanctioned direct-body &r.s.mu
		// call in runLocked. EachInSubtree descends into closures, so a hidden
		// nested Acquire is still seen here and (not being sanctioned) reported.
		EachInSubtree[ast.CallExpr](file, func(ce *ast.CallExpr) {
			if !isAcquire(ce) || sanctioned[ce] {
				return
			}
			report(ce, fmt.Sprintf(
				"txlock.Acquire in %s — the only sanctioned mint is the single "+
					"`txlock.Acquire(&r.s.mu)` in (memTxRunner).runLocked's direct body "+
					"(not a foreign mutex, not nested in a closure) (%s W1)",
				enclosingFuncName(file, ce.Pos()), ruleMemTxLockOwnership01,
			))
		})

		// W2: (*Store).inLiveTx return form.
		EachInSubtree[ast.FuncDecl](file, func(fd *ast.FuncDecl) {
			if fd.Body == nil || fd.Name.Name != "inLiveTx" {
				return
			}
			if fd.Recv == nil || receiverTypeName(fd) != "Store" {
				return
			}
			if ok, why := inLiveTxFormOK(p.TypesInfo, fd); !ok {
				report(fd, fmt.Sprintf(
					"weakened (*Store).inLiveTx in %s: %s — must be `return "+
						"l.Live(&s.mu)` with l from ctx.Value(memTxKey{}).(txlock.Lease) "+
						"(%s W2)",
					enclosingFuncName(file, fd.Pos()), why, ruleMemTxLockOwnership01,
				))
			}
		})
	}
	return ds
}

// memProductionScan runs scanMemTxLockWitness over the production mem package
// only (skipping the txlock sub-package and any other loaded package).
func memProductionScan(t *testing.T) []Diagnostic {
	t.Helper()
	root := findModuleRoot(t)
	modPath, err := moduleImportPath(root)
	require.NoError(t, err, "read module path from go.mod")
	memPkgPath := modPath + "/" + memPkgRel

	var diags []Diagnostic
	// Load mem/... (not just mem) so the typed resolver has the txlock sub-package
	// in the loaded set — isTxlockAcquireCall → ResolvePackageRef needs it. The
	// p.Pkg.Path() filter then restricts the scan to the mem package itself.
	Run(t, Typed(TypedOpts{Tests: false}, []string{"./" + memPkgRel + "/..."}),
		func(p *Pass) []Diagnostic {
			if p.Pkg == nil || p.Pkg.Path() != memPkgPath {
				return nil
			}
			diags = append(diags, scanMemTxLockWitness(p)...)
			return nil
		})

	return diags
}

// TestMemTxLockOwnership01 enforces W1 + W2 over the production mem package.
func TestMemTxLockOwnership01(t *testing.T) {
	diags := memProductionScan(t)

	var lines []string
	for _, d := range diags {
		lines = append(lines, fmt.Sprintf("  %s:%d  %s", d.Rel, d.Line, d.Message))
	}
	assert.Empty(t, lines,
		"%s: %d violation(s) — the lease funnel (txlock.Acquire only as &r.s.mu in "+
			"runLocked; inLiveTx pins l.Live(&s.mu)) is broken; see ADR "+
			"202605171846-adr-mem-tx-lock-ownership.md:\n%s",
		ruleMemTxLockOwnership01, len(lines), strings.Join(lines, "\n"))
}

// TestMemTxLockOwnership01_FindsSanctionedSites is the precision test
// (anti-vacuous-pass): it asserts the scan actually sees the funnel's anchors in
// production — exactly one txlock.Acquire(&r.s.mu) in (memTxRunner).runLocked's
// direct body, and exactly one (*Store).inLiveTx accessor. If a refactor renames
// the type / functions or moves the mint so the matchers stop firing, this fails
// loudly instead of the primary rule passing empty.
func TestMemTxLockOwnership01_FindsSanctionedSites(t *testing.T) {
	root := findModuleRoot(t)
	modPath, err := moduleImportPath(root)
	require.NoError(t, err, "read module path from go.mod")
	memPkgPath := modPath + "/" + memPkgRel

	var acquireInRunLocked, inLiveTxAccessors int

	Run(t, Typed(TypedOpts{Tests: false}, []string{"./" + memPkgRel + "/..."}),
		func(p *Pass) []Diagnostic {
			if p.Pkg == nil || p.Pkg.Path() != memPkgPath {
				return nil
			}
			isAcquire := func(ce *ast.CallExpr) bool {
				sel, ok := ce.Fun.(*ast.SelectorExpr)
				return ok && isTxlockAcquireCall(p.TypesInfo, sel)
			}
			for _, file := range p.Files {
				EachInSubtree[ast.FuncDecl](file, func(fd *ast.FuncDecl) {
					if fd.Body == nil {
						return
					}
					if fd.Name.Name == "inLiveTx" && fd.Recv != nil &&
						receiverTypeName(fd) == "Store" {
						inLiveTxAccessors++
					}
					if fd.Name.Name == memTxSiteRunLocked && fd.Recv != nil &&
						receiverTypeName(fd) == "memTxRunner" {
						recv := receiverVarName(fd)
						for _, ce := range directBodyAcquireCalls(fd, isAcquire) {
							if isAcquireArgRecvStoreMu(ce, recv) {
								acquireInRunLocked++
							}
						}
					}
				})
			}
			return nil
		})

	require.Equalf(t, 1, acquireInRunLocked,
		"%s precision: expected exactly 1 txlock.Acquire(&r.s.mu) in (memTxRunner).runLocked's "+
			"direct body, got %d — 0 means the matcher is stale (rename/arg drift); >1 means a "+
			"second mint (a re-entrant double-lock deadlock bug)", ruleMemTxLockOwnership01, acquireInRunLocked)
	require.Equalf(t, 1, inLiveTxAccessors,
		"%s precision: expected exactly 1 (*Store).inLiveTx accessor in %s, got %d",
		ruleMemTxLockOwnership01, memPkgRel, inLiveTxAccessors)
}

// TestMemTxLockOwnership01_W1ArgAndNesting closes the W1-tightening blind spot
// (#972): the pure-AST helpers isAcquireArgRecvStoreMu (argument pin) and
// directBodyAcquireCalls (nested-closure exclusion) are exercised on parsed
// source, since the real-source fixture can host only one runLocked and cannot
// model a foreign-arg / nested mint INSIDE the sanctioned site.
func TestMemTxLockOwnership01_W1ArgAndNesting(t *testing.T) {
	const src = `package p
type Store struct{ mu int }
type memTxRunner struct{ s *Store }
func (r memTxRunner) runLocked() {
	direct(&r.s.mu)
	go func() { nested(&r.s.mu) }()      // goroutine closure → excluded
	g := func() { nestedLocal(&r.s.mu) } // local-assignment closure → excluded
	g()
}
`
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "src.go", src, 0)
	require.NoError(t, err)

	var runLocked *ast.FuncDecl
	EachInChildren[ast.FuncDecl](f, func(fd *ast.FuncDecl) {
		if fd.Name.Name == "runLocked" {
			runLocked = fd
		}
	})
	require.NotNil(t, runLocked)

	// Syntactic acquire predicate (no type info): exercises the traversal only.
	synAcq := func(ce *ast.CallExpr) bool {
		id, ok := ce.Fun.(*ast.Ident)
		return ok && (id.Name == "direct" || id.Name == "nested" || id.Name == "nestedLocal")
	}
	got := directBodyAcquireCalls(runLocked, synAcq)
	require.Lenf(t, got, 1,
		"directBodyAcquireCalls must exclude BOTH the goroutine and local-assignment "+
			"closure Acquires, keeping only the direct-body call (got %d)", len(got))
	assert.True(t, isAcquireArgRecvStoreMu(got[0], "r"),
		"the direct-body direct(&r.s.mu) must satisfy the &recv.s.mu arg pin")

	// Argument pin: positive and negative shapes.
	cases := []struct {
		expr string
		want bool
	}{
		{"f(&r.s.mu)", true},
		{"f(&r.s.other)", false},       // wrong field
		{"f(&other)", false},           // not recv.s.mu
		{"f(&x.mu)", false},            // wrong base (x.mu, not r.s.mu)
		{"f(&r.s.mu, &r.s.mu)", false}, // wrong arity
		{"f(r.s.mu)", false},           // missing address-of
	}
	for _, tc := range cases {
		e, err := parser.ParseExpr(tc.expr)
		require.NoErrorf(t, err, "parse %s", tc.expr)
		ce, ok := e.(*ast.CallExpr)
		require.Truef(t, ok, "%s is not a call", tc.expr)
		assert.Equalf(t, tc.want, isAcquireArgRecvStoreMu(ce, "r"),
			"isAcquireArgRecvStoreMu(%s, \"r\")", tc.expr)
	}
}

// TestMemTxLockOwnership01_NoReflectInMemPkg closes the reflect blind spot: an
// AST/typed scan cannot see reflect-based mutation of txlock.Lease.{mu,live}
// (which would forge a live lease). Neither package mem nor package txlock
// imports "reflect"; assert it absent.
func TestMemTxLockOwnership01_NoReflectInMemPkg(t *testing.T) {
	assertMemTreeDoesNotImport(t, "reflect")
}

// TestMemTxLockOwnership01_NoUnsafeInMemPkg closes the unsafe blind spot
// (unsafe-pointer write to txlock.Lease fields). Neither package must import
// "unsafe".
func TestMemTxLockOwnership01_NoUnsafeInMemPkg(t *testing.T) {
	assertMemTreeDoesNotImport(t, "unsafe")
}

// assertMemTreeDoesNotImport asserts that neither the mem package nor its
// internal/txlock seal package imports pkg (a lease-forge channel).
func assertMemTreeDoesNotImport(t *testing.T, pkg string) {
	t.Helper()
	root := findModuleRoot(t)
	scope := DirsScope(root, []string{memPkgRel, memPkgRel + "/internal/txlock"})

	var offenders []string
	// Bare Run (not RunTyped): a direct import-path string match suffices for
	// stdlib "reflect"/"unsafe" — they have no alias form in ImportSpec.Path.Value
	// and the threat is a direct import within mem/txlock, not a transitive one.
	Run(t, AST(scope), func(p *Pass) []Diagnostic {
		for _, file := range p.Files {
			for _, imp := range file.Imports {
				if imp.Path != nil && imp.Path.Value == `"`+pkg+`"` {
					offenders = append(offenders, p.Rel(file))
				}
			}
		}
		return nil
	})

	assert.Emptyf(t, offenders,
		"%s blind-spot guard: mem tree must not import %q (would defeat the "+
			"sealed-lease funnel by forging txlock.Lease fields); offenders: %v",
		ruleMemTxLockOwnership01, pkg, offenders)
}
