package archtest

// INVARIANT: MEM-TX-LOCK-OWNERSHIP-01
//
// mem_tx_lock_ownership_test.go is the Medium double-line defense for the mem
// tx lock-ownership funnel (ADR
// docs/architecture/202605171846-adr-mem-tx-lock-ownership.md). The Hard line
// is the Go type system itself: cells/accesscore/internal/mem.memTxToken and
// its holdsLock field are unexported, so no code outside package mem can
// construct a holdsLock=true token (compiler-enforced — the upstream half of
// the funnel). This archtest closes the in-package residue: a future edit
// inside package mem must not mint a holdsLock=true token anywhere except
// memTxRunner.RunInTx (which has just called store.mu.Lock()), nor mutate the
// holdsLock field after construction, nor bypass the composite-literal form.
//
// Rule R1 — construction-site lock:
//
//	every *ast.CompositeLit of type memTxToken in a non-test .go file under
//	cells/accesscore/internal/mem MUST be lexically inside one of exactly two
//	FuncDecls:
//	  - func (memTxRunner) RunInTx   (the only holdsLock=true site)
//	  - func WithTxContext           (the only holdsLock=false site)
//	A memTxToken composite literal anywhere else (another method, a helper, a
//	package-level var) is a violation.
//
// Rule R2 — blind-spot closure (post-construction mutation / non-literal
// construction):
//
//	R2a: `new(memTxToken)` is banned (pointer-to-zero then field-set bypasses
//	the composite-literal funnel).
//	R2b: every `x.holdsLock` SelectorExpr (read OR write) must sit inside
//	(*Store).txHoldsLock — the sole legitimate accessor. The two construction
//	sites set holdsLock via composite-literal Ident keys (not SelectorExpr),
//	so R2b does not touch them; R1 funnels construction. Combined, "build a
//	token then flip holdsLock" / "read holdsLock to fork logic elsewhere" is
//	inexpressible without tripping the rule.
//
// Blind spots of the chosen tool (pure AST CompositeLit/CallExpr/AssignStmt
// scan) and their handling — required by .claude/rules/gocell/ai-collab.md:
//
//   - reflect-based field mutation (reflect.Value.SetBool on holdsLock):
//     out of scope of an AST literal scan. Closed by
//     TestMemTxLockOwnership01_NoReflectInMemPkg asserting package mem
//     (non-test) does not import "reflect" at all (true today; a reverse
//     self-check, not the primary rule).
//   - unsafe pointer field write: same class; package mem does not import
//     "unsafe" — asserted by the same reverse self-check
//     (TestMemTxLockOwnership01_NoUnsafeInMemPkg).
//   - vacuous-pass risk (matcher silently matching nothing): closed by
//     TestMemTxLockOwnership01_FindsExactlyTheTwoSites companion-index
//     precision test — it asserts the scan finds the two real construction
//     sites by name, so a regression that stops detecting memTxToken literals
//     fails loudly instead of passing empty.
//   - R2b accessor identified by FuncDecl name "txHoldsLock" + receiver type
//     name "Store" — both are Soft string anchors (receiverTypeName is a
//     string-comparison helper). Mitigated by (a) receiver type check added in
//     F-A (symmetric with allowedTokenFunc), and (b) the companion-index
//     reverse self-check TestMemTxLockOwnership01_R2bFindsHoldsLockAccessor
//     asserting the accessor FuncDecl is non-empty and contains at least one
//     "holdsLock" SelectorExpr, preventing vacuous-pass when the function is
//     renamed or its body is emptied. Rating: Medium (string-anchor, two-part
//     match, companion-index guard).

import (
	"go/ast"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const ruleMemTxLockOwnership01 = "MEM-TX-LOCK-OWNERSHIP-01"

const memPkgRel = "cells/accesscore/internal/mem"

// memTxTokenTypeName is the unexported struct whose construction this rule
// funnels. Same-package AST ident match is sufficient (no cross-package type
// resolution): the type is unexported, so the identifier memTxToken inside
// package mem can only denote this type.
const memTxTokenTypeName = "memTxToken"

// isMemTxTokenLit reports whether cl constructs a memTxToken value, covering
// both `memTxToken{…}` and `&memTxToken{…}` (the unary & wraps the same
// CompositeLit node, so only the lit's Type matters).
func isMemTxTokenLit(cl *ast.CompositeLit) bool {
	id, ok := cl.Type.(*ast.Ident)
	return ok && id.Name == memTxTokenTypeName
}

// allowedTokenFunc reports whether fd is one of the two legitimate
// construction sites: func (memTxRunner) RunInTx, or func WithTxContext.
func allowedTokenFunc(fd *ast.FuncDecl) bool {
	switch fd.Name.Name {
	case "RunInTx":
		// must be a method on memTxRunner (value or pointer receiver).
		// receiverTypeName is the shared archtest helper (pg_repo_ambient_tx).
		return fd.Recv != nil && receiverTypeName(fd) == "memTxRunner"
	case "WithTxContext":
		return fd.Recv == nil
	default:
		return false
	}
}

// TestMemTxLockOwnership01 enforces R1 + R2 over the mem package.
func TestMemTxLockOwnership01(t *testing.T) {
	root := findModuleRoot(t)
	scope := DirsScope(root, []string{memPkgRel})

	type violation struct {
		file, detail string
		line         int
	}
	var violations []violation

	// DirsScope without IncludeTests() excludes *_test.go.
	Run(t, scope, func(p *Pass) []Diagnostic {
		for _, file := range p.Files {
			// Record the Pos/End spans of the two allowed funcs.
			type span struct{ lo, hi ast.Node }
			var allowed []span
			EachInSubtree[ast.FuncDecl](file, func(fd *ast.FuncDecl) {
				if fd.Body != nil && allowedTokenFunc(fd) {
					allowed = append(allowed, span{fd, fd})
				}
			})
			inAllowed := func(n ast.Node) bool {
				for _, s := range allowed {
					if n.Pos() >= s.lo.Pos() && n.End() <= s.hi.End() {
						return true
					}
				}
				return false
			}

			// R1: every memTxToken composite literal must sit inside an
			// allowed func.
			EachInSubtree[ast.CompositeLit](file, func(cl *ast.CompositeLit) {
				if !isMemTxTokenLit(cl) {
					return
				}
				if inAllowed(cl) {
					return
				}
				pos := p.Fset.Position(cl.Pos())
				violations = append(violations, violation{
					file:   p.Rel(file),
					line:   pos.Line,
					detail: "memTxToken composite literal outside (memTxRunner).RunInTx / WithTxContext (R1)",
				})
			})

			// R2a: ban new(memTxToken).
			EachInSubtree[ast.CallExpr](file, func(ce *ast.CallExpr) {
				id, ok := ce.Fun.(*ast.Ident)
				if !ok || id.Name != "new" || len(ce.Args) != 1 {
					return
				}
				arg, ok := ce.Args[0].(*ast.Ident)
				if !ok || arg.Name != memTxTokenTypeName {
					return
				}
				pos := p.Fset.Position(ce.Pos())
				violations = append(violations, violation{
					file:   p.Rel(file),
					line:   pos.Line,
					detail: "new(memTxToken) banned; construct via the composite-literal funnel (R2a)",
				})
			})

			// R2b: every `x.holdsLock` selector (read OR write) must sit inside
			// (*Store).txHoldsLock — the sole legitimate field accessor. The
			// two construction sites use composite-literal Ident keys
			// (KeyValueExpr.Key), NOT SelectorExpr, so they are not matched
			// here; R1 already funnels construction. Funneling all selector
			// access to txHoldsLock makes "build then flip / read elsewhere"
			// uniformly catchable with a single-node walk (no []ast.Expr
			// for-range — SCANNER-FRAMEWORK-USAGE-01 compliant).
			//
			// Receiver check: the accessor must have receiver *Store (value or
			// pointer), symmetric with allowedTokenFunc's receiver check for
			// RunInTx. This prevents a same-named function on a different type
			// from being falsely accepted as the allowed accessor.
			var accessor []ast.Node
			EachInSubtree[ast.FuncDecl](file, func(fd *ast.FuncDecl) {
				if fd.Body != nil && fd.Name.Name == "txHoldsLock" &&
					fd.Recv != nil && receiverTypeName(fd) == "Store" {
					accessor = append(accessor, fd)
				}
			})
			inAccessor := func(n ast.Node) bool {
				for _, a := range accessor {
					if n.Pos() >= a.Pos() && n.End() <= a.End() {
						return true
					}
				}
				return false
			}
			EachInSubtree[ast.SelectorExpr](file, func(sel *ast.SelectorExpr) {
				if sel.Sel.Name != "holdsLock" || inAccessor(sel) {
					return
				}
				pos := p.Fset.Position(sel.Pos())
				violations = append(violations, violation{
					file:   p.Rel(file),
					line:   pos.Line,
					detail: ".holdsLock accessed outside (*Store).txHoldsLock (R2b)",
				})
			})
		}
		return nil
	})

	if len(violations) > 0 {
		t.Logf("%s: %d violation(s):", ruleMemTxLockOwnership01, len(violations))
		for _, v := range violations {
			t.Logf("  %s:%d  %s", v.file, v.line, v.detail)
		}
	}
	assert.Empty(t, violations,
		"%s: memTxToken (holdsLock truth token) may only be constructed in "+
			"(memTxRunner).RunInTx / WithTxContext and never post-mutated; see ADR "+
			"202605171846-adr-mem-tx-lock-ownership.md", ruleMemTxLockOwnership01)
}

// TestMemTxLockOwnership01_FindsExactlyTheTwoSites is the companion-index
// precision test (anti-vacuous-pass): it asserts the scan actually sees a
// memTxToken composite literal inside BOTH RunInTx and WithTxContext. If a
// refactor renames the type or moves construction such that the matcher stops
// firing, this fails loudly instead of the primary rule passing empty.
func TestMemTxLockOwnership01_FindsExactlyTheTwoSites(t *testing.T) {
	root := findModuleRoot(t)
	scope := DirsScope(root, []string{memPkgRel})

	sites := map[string]bool{} // func name -> saw memTxToken lit inside
	Run(t, scope, func(p *Pass) []Diagnostic {
		for _, file := range p.Files {
			EachInSubtree[ast.FuncDecl](file, func(fd *ast.FuncDecl) {
				if fd.Body == nil || !allowedTokenFunc(fd) {
					return
				}
				EachInSubtree[ast.CompositeLit](fd, func(cl *ast.CompositeLit) {
					if isMemTxTokenLit(cl) {
						sites[fd.Name.Name] = true
					}
				})
			})
		}
		return nil
	})

	require.Truef(t, sites["RunInTx"],
		"%s precision: expected a memTxToken literal inside (memTxRunner).RunInTx; "+
			"matcher may be stale", ruleMemTxLockOwnership01)
	require.Truef(t, sites["WithTxContext"],
		"%s precision: expected a memTxToken literal inside WithTxContext; "+
			"matcher may be stale", ruleMemTxLockOwnership01)
}

// TestMemTxLockOwnership01_R2bFindsHoldsLockAccessor is the companion-index
// reverse self-check for R2b: it asserts that the scan actually finds a
// FuncDecl named "txHoldsLock" with receiver *Store in the mem package AND
// that function body contains at least one "holdsLock" SelectorExpr. If the
// accessor is renamed or its body emptied, this fails loudly instead of R2b
// passing vacuously (no accessor found → everything outside the empty set
// passes trivially).
func TestMemTxLockOwnership01_R2bFindsHoldsLockAccessor(t *testing.T) {
	root := findModuleRoot(t)
	scope := DirsScope(root, []string{memPkgRel})

	var accessorFound bool
	var holdsLockSelCount int

	Run(t, scope, func(p *Pass) []Diagnostic {
		for _, file := range p.Files {
			EachInSubtree[ast.FuncDecl](file, func(fd *ast.FuncDecl) {
				if fd.Body == nil || fd.Name.Name != "txHoldsLock" {
					return
				}
				if fd.Recv == nil || receiverTypeName(fd) != "Store" {
					return
				}
				accessorFound = true
				EachInSubtree[ast.SelectorExpr](fd, func(sel *ast.SelectorExpr) {
					if sel.Sel.Name == "holdsLock" {
						holdsLockSelCount++
					}
				})
			})
		}
		return nil
	})

	require.Truef(t, accessorFound,
		"%s R2b companion-index: expected a FuncDecl named txHoldsLock with "+
			"receiver *Store in %s; matcher may be stale after rename",
		ruleMemTxLockOwnership01, memPkgRel)
	require.Positivef(t, holdsLockSelCount,
		"%s R2b companion-index: expected at least one .holdsLock SelectorExpr "+
			"inside (*Store).txHoldsLock; body may be empty or renamed",
		ruleMemTxLockOwnership01)
}

// TestMemTxLockOwnership01_NoReflectInMemPkg closes the reflect blind spot:
// an AST literal scan cannot see reflect.Value.SetBool mutation of holdsLock.
// Package mem does not (and must not) import "reflect"; assert it absent.
func TestMemTxLockOwnership01_NoReflectInMemPkg(t *testing.T) {
	assertMemPkgDoesNotImport(t, "reflect")
}

// TestMemTxLockOwnership01_NoUnsafeInMemPkg closes the unsafe blind spot
// (unsafe-pointer field write). Package mem must not import "unsafe".
func TestMemTxLockOwnership01_NoUnsafeInMemPkg(t *testing.T) {
	assertMemPkgDoesNotImport(t, "unsafe")
}

func assertMemPkgDoesNotImport(t *testing.T, pkg string) {
	t.Helper()
	root := findModuleRoot(t)
	scope := DirsScope(root, []string{memPkgRel})

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
		"%s blind-spot guard: package %s must not import %q (would defeat the "+
			"AST construction-site funnel); offenders: %v",
		ruleMemTxLockOwnership01, memPkgRel, pkg, offenders)
}
