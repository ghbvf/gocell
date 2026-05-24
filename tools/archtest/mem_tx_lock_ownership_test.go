package archtest

// INVARIANT: MEM-TX-LOCK-OWNERSHIP-01
//
// mem_tx_lock_ownership_test.go guards the regression surface of the mem tx
// lock-ownership funnel after the sealed lock-witness rework (#945; ADR
// docs/architecture/202605171846-adr-mem-tx-lock-ownership.md). The threat is
// "sentinel present but no lock": a tx-context token authorizing a repository
// method to skip its per-call store.mu lock when the lock is NOT held →
// concurrent map writes (the PR #558 flake).
//
// That threat is now compile-impossible — the Hard core lives in the type
// system, not in this archtest:
//
//   - Upstream Hard: cells/accesscore/internal/mem/internal/txlock.Held's sole
//     field is unexported, so no package (not even mem) can write txlock.Held{…}
//     with a real mutex. The only way to obtain a Held whose Holds(&store.mu) is
//     true is txlock.Acquire(&store.mu), which Lock()s the mutex.
//   - Downstream Hard: a memTxToken carries that Held; txHoldsLock authorizes a
//     lock-skip only when tok.held.Holds(&s.mu). "In a tx context yet not holding
//     the lock" is inexpressible without having locked. The Held also has NO
//     Release method (Acquire returns a separate unlock closure kept local to
//     runLocked), so a ctx-carried witness cannot even release the lock.
//
// This file is the Medium regression layer over that Hard core. It runs the
// shared detector scanMemTxLockWitness over package mem (typed, via RunTyped)
// and reports:
//
//	W1 — txlock.Acquire is called only inside (memTxRunner).runLocked, the sole
//	     sanctioned witness-mint site (matched by func name "Acquire" + package
//	     path suffix "/txlock", resolved typed via ResolvePackageRef).
//	W2 — (*Store).txHoldsLock returns exactly `tok != nil && tok.held.Holds(&s.mu)`
//	     (flatten the && tree; exactly two conjuncts: a nil-guard and the Holds
//	     call on tok.held with arg &<recv>.mu). Dropping the Holds conjunct, adding
//	     a third conjunct, or weakening the call shape fails — this pins the
//	     downstream-Hard runtime check against silent regression.
//	R1 — every memTxToken composite literal (typed match: *types.Named "memTxToken"
//	     in the package under scan) sits inside runLocked or WithTxContext.
//
// The seal itself (txlock.Held field-set: exactly one unexported *sync.Mutex
// field) is frozen by reflect in the txlock package's own test
// (TestHeldSealFrozen in internal/txlock/txlock_test.go) — tools/archtest cannot
// import the double-internal txlock package, so the freeze lives where Held is
// defined, mirroring the FixtureOpts freeze in pass_test.go.
//
// Note on "const-fold": #945 originally proposed pinning a holdsLock bool field
// value via go/types constant folding. The witness rework deletes that bool
// (there is no value left to fold and no bool-const substitution blind spot);
// R1 now pins only construction SITE, while W1/W2 + the type-system seal carry
// the lock-ownership truth.
//
// AI-robust rating (modifies an enforcement mechanism + reworks runtime design):
//   - Upstream Hard / Downstream Hard — type system (txlock.Held seal; Held has
//     no Release). This archtest is NOT the primary defense.
//   - This file (W1/W2/R1) = Medium regression layer: string anchors
//     ("Acquire"/"txlock"/"runLocked"/"WithTxContext"/"txHoldsLock"/"held"/"mu")
//     + AST form-uniqueness. No gh issue to "Hard-ify" — the funnel is already
//     Hard via the seal; W1/W2/R1 only catch funnel-discipline drift.
//
// Tool blind spots (pure-AST/typed scan) + their reverse self-checks, per
// ai-robust.md §"工具选定后强制盲区自检":
//   - reflect.Value.Set / unsafe pointer write on txlock.Held.mu would forge a
//     witness: closed by NoReflectInMemPkg / NoUnsafeInMemPkg asserting neither
//     package mem nor package txlock imports "reflect"/"unsafe".
//   - vacuous-pass (matcher silently matches nothing): closed by
//     FindsSanctionedSites asserting the scan actually finds the runLocked
//     Acquire site, both memTxToken literal sites, and the txHoldsLock accessor.
//   - detector correctness (false-negative / false-positive drift): closed by
//     the real-source reverse self-check TestMemTxLockOwnership01_FixturePattern
//     (mem_tx_lock_ownership_red_fixture_test.go) asserting exactly the three RED
//     sites are reported and the sanctioned sites are not.

import (
	"fmt"
	"go/ast"
	"go/token"
	"go/types"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const ruleMemTxLockOwnership01 = "MEM-TX-LOCK-OWNERSHIP-01"

const memPkgRel = "cells/accesscore/internal/mem"

// memTxTokenTypeName is the unexported witness-bearing token whose construction
// site this rule funnels (R1).
const memTxTokenTypeName = "memTxToken"

const (
	// memTxSiteRunLocked is the holds-witness mint site: (memTxRunner).runLocked
	// is the only function allowed to call txlock.Acquire (W1) and the holds-lock
	// memTxToken construction site (R1).
	memTxSiteRunLocked = "runLocked"
	// memTxSiteWithTxContext is the no-witness site: WithTxContext constructs a
	// zero-witness memTxToken (R1) and never calls Acquire.
	memTxSiteWithTxContext = "WithTxContext"
)

// memTxTokenSiteKind reports whether fd is one of the two sanctioned memTxToken
// construction sites: func (memTxRunner) runLocked, or func WithTxContext.
func memTxTokenSiteKind(fd *ast.FuncDecl) (site string, ok bool) {
	switch fd.Name.Name {
	case memTxSiteRunLocked:
		// must be a method on memTxRunner (value or pointer receiver).
		if fd.Recv != nil && receiverTypeName(fd) == "memTxRunner" {
			return memTxSiteRunLocked, true
		}
	case memTxSiteWithTxContext:
		if fd.Recv == nil {
			return memTxSiteWithTxContext, true
		}
	}
	return "", false
}

// receiverVarName returns the receiver variable name of fd (the `s` in
// `func (s *Store) …`), or "" when there is no named receiver.
func receiverVarName(fd *ast.FuncDecl) string {
	if fd.Recv == nil || len(fd.Recv.List) == 0 || len(fd.Recv.List[0].Names) == 0 {
		return ""
	}
	return fd.Recv.List[0].Names[0].Name
}

// isMemTxTokenLitTyped reports whether cl constructs a memTxToken value declared
// in the package under scan, covering both `memTxToken{…}` and `&memTxToken{…}`.
// Typed match against *types.Named (immune to local shadowing); the package
// identity check lets the same detector run over production mem and the
// fixture's own memTxToken alike.
func isMemTxTokenLitTyped(info *types.Info, pkg *types.Package, cl *ast.CompositeLit) bool {
	if info == nil || pkg == nil || cl == nil || cl.Type == nil {
		return false
	}
	t := info.TypeOf(cl.Type)
	if t == nil {
		return false
	}
	named, ok := t.(*types.Named)
	if !ok {
		return false
	}
	obj := named.Obj()
	return obj != nil && obj.Name() == memTxTokenTypeName &&
		obj.Pkg() != nil && obj.Pkg().Path() == pkg.Path()
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

// flattenLAND splits a left-associative `a && b && c` tree into its leaf
// conjuncts. A non-&& expression returns itself as a single leaf.
func flattenLAND(e ast.Expr) []ast.Expr {
	be, ok := e.(*ast.BinaryExpr)
	if !ok || be.Op != token.LAND {
		return []ast.Expr{e}
	}
	return append(flattenLAND(be.X), flattenLAND(be.Y)...)
}

// nilGuardVar returns the non-nil operand identifier name of an `x != nil`
// BinaryExpr (either order), or ("", false) for any other shape.
func nilGuardVar(e ast.Expr) (name string, ok bool) {
	be, ok := e.(*ast.BinaryExpr)
	if !ok || be.Op != token.NEQ {
		return "", false
	}
	x, xIsIdent := be.X.(*ast.Ident)
	y, yIsIdent := be.Y.(*ast.Ident)
	if !xIsIdent || !yIsIdent {
		return "", false
	}
	switch {
	case y.Name == "nil":
		return x.Name, true
	case x.Name == "nil":
		return y.Name, true
	default:
		return "", false
	}
}

// isHeldHoldsCall reports whether e is exactly `<tok>.held.Holds(&<recv>.mu)`.
func isHeldHoldsCall(e ast.Expr, tok, recv string) bool {
	ce, ok := e.(*ast.CallExpr)
	if !ok || len(ce.Args) != 1 {
		return false
	}
	holds, ok := ce.Fun.(*ast.SelectorExpr) // <…>.Holds
	if !ok || holds.Sel.Name != "Holds" {
		return false
	}
	heldSel, ok := holds.X.(*ast.SelectorExpr) // <tok>.held
	if !ok || heldSel.Sel.Name != "held" {
		return false
	}
	if id, ok := heldSel.X.(*ast.Ident); !ok || id.Name != tok {
		return false
	}
	addr, ok := ce.Args[0].(*ast.UnaryExpr) // &<recv>.mu
	if !ok || addr.Op != token.AND {
		return false
	}
	muSel, ok := addr.X.(*ast.SelectorExpr)
	if !ok || muSel.Sel.Name != "mu" {
		return false
	}
	id, ok := muSel.X.(*ast.Ident)
	return ok && id.Name == recv
}

// txHoldsLockFormOK pins the (*Store).txHoldsLock return to exactly
// `tok != nil && tok.held.Holds(&s.mu)`: a single single-value return whose &&
// tree flattens to exactly two conjuncts — a nil-guard on tok and the Holds call
// on tok.held with arg &<recv>.mu. Order-independent; receiver/token names are
// taken from the source, not hard-coded.
func txHoldsLockFormOK(fd *ast.FuncDecl) (ok bool, why string) {
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
	conj := flattenLAND(rets[0].Results[0])
	if len(conj) != 2 {
		return false, fmt.Sprintf("expected exactly 2 && conjuncts, got %d", len(conj))
	}
	var tok string
	var sawNil bool
	for _, c := range conj {
		if v, ok := nilGuardVar(c); ok {
			tok, sawNil = v, true
		}
	}
	if !sawNil {
		return false, "missing `tok != nil` nil-guard conjunct"
	}
	for _, c := range conj {
		if isHeldHoldsCall(c, tok, recv) {
			return true, ""
		}
	}
	return false, fmt.Sprintf("missing `%s.held.Holds(&%s.mu)` conjunct", tok, recv)
}

// scanMemTxLockWitness is the shared witness-funnel detector. It runs over a
// single package Pass — production (cells/accesscore/internal/mem) and the
// memtxlockfixture reverse self-check alike — and reports W1 / W2 / R1 (see the
// file INVARIANT block). Requires a typed Pass; returns nil for a bare-AST Pass.
func scanMemTxLockWitness(p *Pass) []Diagnostic {
	if p.TypesInfo == nil || p.Pkg == nil {
		return nil
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

		// Record sanctioned construction-site spans.
		type span struct {
			kind   string
			lo, hi token.Pos
		}
		var sites []span
		EachInSubtree[ast.FuncDecl](file, func(fd *ast.FuncDecl) {
			if fd.Body == nil {
				return
			}
			if kind, ok := memTxTokenSiteKind(fd); ok {
				sites = append(sites, span{kind, fd.Pos(), fd.End()})
			}
		})
		within := func(n ast.Node, kinds ...string) bool {
			for _, s := range sites {
				if n.Pos() < s.lo || n.End() > s.hi {
					continue
				}
				for _, k := range kinds {
					if s.kind == k {
						return true
					}
				}
			}
			return false
		}

		// W1: txlock.Acquire only inside (memTxRunner).runLocked.
		EachInSubtree[ast.CallExpr](file, func(ce *ast.CallExpr) {
			sel, ok := ce.Fun.(*ast.SelectorExpr)
			if !ok || !isTxlockAcquireCall(p.TypesInfo, sel) {
				return
			}
			if within(ce, memTxSiteRunLocked) {
				return
			}
			report(ce, fmt.Sprintf(
				"txlock.Acquire in %s, outside the sole sanctioned runLocked "+
					"witness-mint site (%s W1)",
				enclosingFuncName(file, ce.Pos()), ruleMemTxLockOwnership01))
		})

		// R1: memTxToken composite literal only inside a sanctioned site.
		EachInSubtree[ast.CompositeLit](file, func(cl *ast.CompositeLit) {
			if !isMemTxTokenLitTyped(p.TypesInfo, p.Pkg, cl) {
				return
			}
			if within(cl, memTxSiteRunLocked, memTxSiteWithTxContext) {
				return
			}
			report(cl, fmt.Sprintf(
				"memTxToken composite literal in %s, outside runLocked / "+
					"WithTxContext (%s R1)",
				enclosingFuncName(file, cl.Pos()), ruleMemTxLockOwnership01))
		})

		// W2: (*Store).txHoldsLock return form.
		EachInSubtree[ast.FuncDecl](file, func(fd *ast.FuncDecl) {
			if fd.Body == nil || fd.Name.Name != "txHoldsLock" {
				return
			}
			if fd.Recv == nil || receiverTypeName(fd) != "Store" {
				return
			}
			if ok, why := txHoldsLockFormOK(fd); !ok {
				report(fd, fmt.Sprintf(
					"weakened (*Store).txHoldsLock in %s: %s — must be "+
						"`tok != nil && tok.held.Holds(&s.mu)` (%s W2)",
					enclosingFuncName(file, fd.Pos()), why, ruleMemTxLockOwnership01))
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
	RunTyped(t, TypedOpts{Tests: false}, []string{"./" + memPkgRel + "/..."},
		func(p *Pass) []Diagnostic {
			if p.Pkg == nil || p.Pkg.Path() != memPkgPath {
				return nil
			}
			diags = append(diags, scanMemTxLockWitness(p)...)
			return nil
		})
	return diags
}

// TestMemTxLockOwnership01 enforces W1 + W2 + R1 over the production mem package.
func TestMemTxLockOwnership01(t *testing.T) {
	diags := memProductionScan(t)

	var lines []string
	for _, d := range diags {
		lines = append(lines, fmt.Sprintf("  %s:%d  %s", d.Rel, d.Line, d.Message))
	}
	assert.Empty(t, lines,
		"%s: %d violation(s) — the witness funnel (txlock.Acquire only in "+
			"runLocked; txHoldsLock pins tok.held.Holds; memTxToken only in "+
			"runLocked/WithTxContext) is broken; see ADR "+
			"202605171846-adr-mem-tx-lock-ownership.md:\n%s",
		ruleMemTxLockOwnership01, len(lines), strings.Join(lines, "\n"))
}

// TestMemTxLockOwnership01_FindsSanctionedSites is the companion-index
// precision test (anti-vacuous-pass): it asserts the scan actually sees the
// witness funnel's anchors in production — the runLocked txlock.Acquire site,
// a memTxToken literal inside BOTH runLocked and WithTxContext, and the
// (*Store).txHoldsLock accessor. If a refactor renames the type / functions or
// moves construction so the matchers stop firing, this fails loudly instead of
// the primary rule passing empty.
func TestMemTxLockOwnership01_FindsSanctionedSites(t *testing.T) {
	root := findModuleRoot(t)
	modPath, err := moduleImportPath(root)
	require.NoError(t, err, "read module path from go.mod")
	memPkgPath := modPath + "/" + memPkgRel

	var acquireInRunLocked, txHoldsLockAccessors int
	litSites := map[string]bool{} // site kind -> saw memTxToken literal inside

	RunTyped(t, TypedOpts{Tests: false}, []string{"./" + memPkgRel + "/..."},
		func(p *Pass) []Diagnostic {
			if p.Pkg == nil || p.Pkg.Path() != memPkgPath {
				return nil
			}
			for _, file := range p.Files {
				EachInSubtree[ast.FuncDecl](file, func(fd *ast.FuncDecl) {
					if fd.Body == nil {
						return
					}
					if fd.Name.Name == "txHoldsLock" && fd.Recv != nil &&
						receiverTypeName(fd) == "Store" {
						txHoldsLockAccessors++
					}
					kind, isSite := memTxTokenSiteKind(fd)
					if !isSite {
						return
					}
					EachInSubtree[ast.CompositeLit](fd, func(cl *ast.CompositeLit) {
						if isMemTxTokenLitTyped(p.TypesInfo, p.Pkg, cl) {
							litSites[kind] = true
						}
					})
					if kind == memTxSiteRunLocked {
						EachInSubtree[ast.CallExpr](fd, func(ce *ast.CallExpr) {
							if sel, ok := ce.Fun.(*ast.SelectorExpr); ok &&
								isTxlockAcquireCall(p.TypesInfo, sel) {
								acquireInRunLocked++
							}
						})
					}
				})
			}
			return nil
		})

	require.Positivef(t, acquireInRunLocked,
		"%s precision: expected a txlock.Acquire call inside (memTxRunner).runLocked; "+
			"matcher may be stale", ruleMemTxLockOwnership01)
	require.Truef(t, litSites[memTxSiteRunLocked],
		"%s precision: expected a memTxToken literal inside (memTxRunner).runLocked",
		ruleMemTxLockOwnership01)
	require.Truef(t, litSites[memTxSiteWithTxContext],
		"%s precision: expected a memTxToken literal inside WithTxContext",
		ruleMemTxLockOwnership01)
	require.Equalf(t, 1, txHoldsLockAccessors,
		"%s precision: expected exactly 1 (*Store).txHoldsLock accessor in %s, got %d",
		ruleMemTxLockOwnership01, memPkgRel, txHoldsLockAccessors)
}

// TestMemTxLockOwnership01_NoReflectInMemPkg closes the reflect blind spot: an
// AST/typed scan cannot see reflect-based mutation of txlock.Held.mu (which
// would forge a witness). Neither package mem nor package txlock imports
// "reflect"; assert it absent.
func TestMemTxLockOwnership01_NoReflectInMemPkg(t *testing.T) {
	assertMemTreeDoesNotImport(t, "reflect")
}

// TestMemTxLockOwnership01_NoUnsafeInMemPkg closes the unsafe blind spot
// (unsafe-pointer write to txlock.Held.mu). Neither package must import "unsafe".
func TestMemTxLockOwnership01_NoUnsafeInMemPkg(t *testing.T) {
	assertMemTreeDoesNotImport(t, "unsafe")
}

// assertMemTreeDoesNotImport asserts that neither the mem package nor its
// internal/txlock seal package imports pkg (a witness-forge channel).
func assertMemTreeDoesNotImport(t *testing.T, pkg string) {
	t.Helper()
	root := findModuleRoot(t)
	scope := DirsScope(root, []string{memPkgRel, memPkgRel + "/internal/txlock"})

	var offenders []string
	Run(t, scope, func(p *Pass) []Diagnostic {
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
			"sealed-witness funnel by forging txlock.Held.mu); offenders: %v",
		ruleMemTxLockOwnership01, pkg, offenders)
}
